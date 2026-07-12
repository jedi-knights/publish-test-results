package parse

import (
    "bytes"
    "encoding/xml"
    "errors"
    "fmt"
    "io"
    "regexp"
    "strconv"
    "strings"
    "time"

    "github.com/jedi-knights/publish-test-results/internal/ir"
)

// junitParser handles JUnit XML in both the Surefire dialect
// (<testsuites> wrapping one or more <testsuite>) and the Ant dialect
// (a bare <testsuite> root). The two dialects share the same
// <testcase> shape so we normalize by treating a bare <testsuite> as
// a testsuites collection of one.
//
// The parser also opportunistically fills File / Line from two
// sources: a non-standard `file` / `line` attribute on <testcase>
// (which the ctestprobe producer will emit in a follow-up), and the
// Surefire convention of a "path/to/file.ext:LINE: ..." prefix at the
// start of a <failure> / <error> body.
type junitParser struct{}

func (junitParser) Name() string { return "junit" }

func (junitParser) CanParse(preview []byte) bool {
    s := skipXMLProlog(string(preview))
    return strings.HasPrefix(s, "<testsuites") ||
        strings.HasPrefix(s, "<testsuite ") ||
        strings.HasPrefix(s, "<testsuite>") ||
        s == "<testsuite" // exactly at buffer boundary
}

// XML element models. Only the fields we consume.

type junitTestsuites struct {
    XMLName    xml.Name         `xml:"testsuites"`
    Name       string           `xml:"name,attr"`
    Testsuites []junitTestsuite `xml:"testsuite"`
}

type junitTestsuite struct {
    XMLName   xml.Name        `xml:"testsuite"`
    Name      string          `xml:"name,attr"`
    Time      string          `xml:"time,attr"`
    Timestamp string          `xml:"timestamp,attr"`
    Hostname  string          `xml:"hostname,attr"`
    Testcases []junitTestcase `xml:"testcase"`
    SystemOut string          `xml:"system-out"`
    SystemErr string          `xml:"system-err"`
}

type junitTestcase struct {
    Name      string        `xml:"name,attr"`
    Classname string        `xml:"classname,attr"`
    Time      string        `xml:"time,attr"`
    // File and Line are producer extensions. Standard Surefire XML
    // does not carry source location per test; we accept it here
    // because at least one downstream producer (ctestprobe) plans to
    // emit it, and ignoring unknown attributes is idiomatic.
    File      string        `xml:"file,attr"`
    Line      int           `xml:"line,attr"`
    Failure   *junitFault   `xml:"failure"`
    Error     *junitFault   `xml:"error"`
    Skipped   *junitSkipped `xml:"skipped"`
    SystemOut string        `xml:"system-out"`
    SystemErr string        `xml:"system-err"`
}

type junitFault struct {
    Message string `xml:"message,attr"`
    Type    string `xml:"type,attr"`
    Body    string `xml:",chardata"`
}

type junitSkipped struct {
    Message string `xml:"message,attr"`
    Body    string `xml:",chardata"`
}

func (p junitParser) Parse(r io.Reader) (ir.Report, error) {
    data, err := io.ReadAll(r)
    if err != nil {
        return ir.Report{}, fmt.Errorf("read: %w", err)
    }

    root, err := findRootElement(data)
    if err != nil {
        return ir.Report{}, err
    }

    var suites []junitTestsuite
    switch root {
    case "testsuites":
        var wrapper junitTestsuites
        if err := xml.Unmarshal(data, &wrapper); err != nil {
            return ir.Report{}, fmt.Errorf("unmarshal testsuites: %w", err)
        }
        suites = wrapper.Testsuites
    case "testsuite":
        var single junitTestsuite
        if err := xml.Unmarshal(data, &single); err != nil {
            return ir.Report{}, fmt.Errorf("unmarshal testsuite: %w", err)
        }
        suites = []junitTestsuite{single}
    default:
        return ir.Report{}, fmt.Errorf("not a JUnit document: root element is %q", root)
    }

    results := make([]ir.TestResult, 0)
    for _, suite := range suites {
        for _, tc := range suite.Testcases {
            results = append(results, junitConvert(suite, tc))
        }
    }

    return ir.Report{
        Results: results,
        Metadata: map[string]string{
            "dialect": "junit",
            "root":    root,
        },
    }, nil
}

// junitConvert normalizes a <testcase> to ir.TestResult.
func junitConvert(suite junitTestsuite, tc junitTestcase) ir.TestResult {
    result := ir.TestResult{
        Suite:     suite.Name,
        Class:     tc.Classname,
        Name:      tc.Name,
        Duration:  parseSeconds(tc.Time),
        File:      tc.File,
        Line:      tc.Line,
        SystemOut: firstNonEmpty(tc.SystemOut, suite.SystemOut),
        SystemErr: firstNonEmpty(tc.SystemErr, suite.SystemErr),
    }

    switch {
    case tc.Failure != nil:
        result.Status = ir.StatusFailed
        result.Message = tc.Failure.Message
        result.Detail = strings.TrimSpace(tc.Failure.Body)
        applyBodyLocation(&result)
    case tc.Error != nil:
        result.Status = ir.StatusError
        result.Message = tc.Error.Message
        result.Detail = strings.TrimSpace(tc.Error.Body)
        applyBodyLocation(&result)
    case tc.Skipped != nil:
        result.Status = ir.StatusSkipped
        result.Message = tc.Skipped.Message
        result.Detail = strings.TrimSpace(tc.Skipped.Body)
    default:
        result.Status = ir.StatusPassed
    }
    return result
}

// applyBodyLocation fills File / Line from the failure/error body when
// the producer follows the Surefire convention of "file:line: message".
// Only overwrites zero-valued fields, so an explicit `file`/`line`
// attribute on <testcase> always wins.
func applyBodyLocation(result *ir.TestResult) {
    if result.File != "" && result.Line != 0 {
        return
    }
    if f, l, ok := parseSurefireLocation(result.Detail); ok {
        if result.File == "" {
            result.File = f
        }
        if result.Line == 0 {
            result.Line = l
        }
    }
}

// surefireLocationRE matches "path/to/file.ext:LINE:" at the start of
// a failure body. Extensions are required to reduce false positives
// on messages that happen to contain word:number sequences.
var surefireLocationRE = regexp.MustCompile(`^\s*([^\s:]+\.[A-Za-z0-9]+):(\d+):`)

func parseSurefireLocation(body string) (string, int, bool) {
    m := surefireLocationRE.FindStringSubmatch(body)
    if m == nil {
        return "", 0, false
    }
    line, err := strconv.Atoi(m[2])
    if err != nil {
        return "", 0, false
    }
    return m[1], line, true
}

// parseSeconds parses a JUnit time attribute (fractional seconds) into
// a time.Duration. Malformed or empty strings yield zero.
func parseSeconds(s string) time.Duration {
    if s == "" {
        return 0
    }
    seconds, err := strconv.ParseFloat(s, 64)
    if err != nil {
        return 0
    }
    return time.Duration(seconds * float64(time.Second))
}

func firstNonEmpty(a, b string) string {
    if a != "" {
        return a
    }
    return b
}

// skipXMLProlog trims leading whitespace, an XML declaration, and
// any XML comments so the caller can inspect the true root element.
func skipXMLProlog(s string) string {
    s = strings.TrimLeft(s, " \t\r\n")
    if strings.HasPrefix(s, "<?xml") {
        if end := strings.Index(s, "?>"); end != -1 {
            s = strings.TrimLeft(s[end+2:], " \t\r\n")
        } else {
            return ""
        }
    }
    for strings.HasPrefix(s, "<!--") {
        end := strings.Index(s, "-->")
        if end == -1 {
            return ""
        }
        s = strings.TrimLeft(s[end+3:], " \t\r\n")
    }
    return s
}

// findRootElement walks the token stream and returns the local name of
// the first opening element, ignoring XML decl / comments / whitespace.
func findRootElement(data []byte) (string, error) {
    dec := xml.NewDecoder(bytes.NewReader(data))
    for {
        tok, err := dec.Token()
        if err != nil {
            if errors.Is(err, io.EOF) {
                return "", fmt.Errorf("no root element in document")
            }
            return "", fmt.Errorf("tokenize: %w", err)
        }
        if se, ok := tok.(xml.StartElement); ok {
            return se.Name.Local, nil
        }
    }
}

func init() {
    Register(junitParser{})
}
