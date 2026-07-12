package parse

import (
    "errors"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/jedi-knights/publish-test-results/internal/ir"
)

// The JUnit parser registers itself in init(). These tests exercise it
// against real-world-shaped fixtures under ../../testdata/.

func TestJUnitParser_Registered(t *testing.T) {
    var found bool
    for _, p := range Registered() {
        if p.Name() == "junit" {
            found = true
            break
        }
    }
    if !found {
        t.Fatal("junit parser did not self-register at init()")
    }
}

func TestJUnitParser_CanParse(t *testing.T) {
    cases := []struct {
        name string
        in   string
        want bool
    }{
        {"testsuites with decl", `<?xml version="1.0"?><testsuites>`, true},
        {"testsuites no decl", `<testsuites tests="1">`, true},
        {"testsuite ant with decl", `<?xml version="1.0"?><testsuite name="x">`, true},
        {"testsuite ant no attrs", `<testsuite>`, true},
        {"leading whitespace", "\n\n  <testsuites>", true},
        {"comment before root", `<?xml version="1.0"?><!--hi--><testsuites>`, true},
        {"xunit assemblies", `<?xml version="1.0"?><assemblies>`, false},
        {"nunit test-run", `<?xml version="1.0"?><test-run>`, false},
        {"json", `{"tests":[]}`, false},
        {"tap", "TAP version 14\n1..2\nok 1", false},
        {"empty", ``, false},
    }
    p := junitParser{}
    for _, tc := range cases {
        if got := p.CanParse([]byte(tc.in)); got != tc.want {
            t.Errorf("%s: CanParse(%q) = %v, want %v", tc.name, tc.in, got, tc.want)
        }
    }
}

// readFixture loads a testdata file relative to the package directory.
func readFixture(t *testing.T, path string) []byte {
    t.Helper()
    // internal/parse -> repo root -> testdata
    full := filepath.Join("..", "..", "testdata", path)
    data, err := os.ReadFile(full)
    if err != nil {
        t.Fatalf("read %s: %v", full, err)
    }
    return data
}

func TestJUnitParser_Parse_Passing(t *testing.T) {
    p := junitParser{}
    data := readFixture(t, "junit-surefire/passing.xml")
    report, err := p.Parse(strings.NewReader(string(data)))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }
    if len(report.Results) != 3 {
        t.Fatalf("results = %d, want 3", len(report.Results))
    }
    for i, r := range report.Results {
        if r.Status != ir.StatusPassed {
            t.Errorf("result %d: status = %v, want Passed", i, r.Status)
        }
        if r.Suite != "rle" {
            t.Errorf("result %d: suite = %q, want rle", i, r.Suite)
        }
        if r.Class != "rle" {
            t.Errorf("result %d: class = %q, want rle", i, r.Class)
        }
    }
    if report.Metadata["dialect"] != "junit" {
        t.Errorf("metadata.dialect = %q, want junit", report.Metadata["dialect"])
    }
    if report.Metadata["root"] != "testsuites" {
        t.Errorf("metadata.root = %q, want testsuites", report.Metadata["root"])
    }
}

func TestJUnitParser_Parse_Mixed(t *testing.T) {
    p := junitParser{}
    data := readFixture(t, "junit-surefire/mixed.xml")
    report, err := p.Parse(strings.NewReader(string(data)))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }
    if got := len(report.Results); got != 4 {
        t.Fatalf("results = %d, want 4", got)
    }

    byName := map[string]ir.TestResult{}
    for _, r := range report.Results {
        byName[r.Name] = r
    }

    checkStatus := func(name string, want ir.Status) {
        r, ok := byName[name]
        if !ok {
            t.Fatalf("test %q missing", name)
        }
        if r.Status != want {
            t.Errorf("%q: status = %v, want %v", name, r.Status, want)
        }
    }
    checkStatus("test_pass", ir.StatusPassed)
    checkStatus("test_fail", ir.StatusFailed)
    checkStatus("test_error", ir.StatusError)
    checkStatus("test_skip", ir.StatusSkipped)

    if got := byName["test_fail"].Message; got != "expected 2, got 3" {
        t.Errorf("test_fail.Message = %q", got)
    }
    if got := byName["test_error"].Detail; got != "stack trace here" {
        t.Errorf("test_error.Detail = %q", got)
    }
    if got := byName["test_skip"].Message; got != "not applicable" {
        t.Errorf("test_skip.Message = %q", got)
    }
}

func TestJUnitParser_Parse_Nested(t *testing.T) {
    p := junitParser{}
    data := readFixture(t, "junit-surefire/nested.xml")
    report, err := p.Parse(strings.NewReader(string(data)))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }
    if got := len(report.Results); got != 4 {
        t.Fatalf("results = %d, want 4", got)
    }
    suites := map[string]int{}
    for _, r := range report.Results {
        suites[r.Suite]++
    }
    if suites["rle"] != 2 || suites["huffman"] != 2 {
        t.Errorf("suite distribution = %v, want rle:2 huffman:2", suites)
    }
}

func TestJUnitParser_Parse_LocationFromBody(t *testing.T) {
    p := junitParser{}
    data := readFixture(t, "junit-surefire/with-location-body.xml")
    report, err := p.Parse(strings.NewReader(string(data)))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }
    if len(report.Results) != 1 {
        t.Fatalf("results = %d, want 1", len(report.Results))
    }
    r := report.Results[0]
    if r.File != "tests/foo.c" {
        t.Errorf("File = %q, want tests/foo.c", r.File)
    }
    if r.Line != 42 {
        t.Errorf("Line = %d, want 42", r.Line)
    }
}

func TestJUnitParser_Parse_LocationFromAttr(t *testing.T) {
    p := junitParser{}
    data := readFixture(t, "junit-surefire/with-location-attr.xml")
    report, err := p.Parse(strings.NewReader(string(data)))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }
    if len(report.Results) != 2 {
        t.Fatalf("results = %d, want 2", len(report.Results))
    }
    for _, r := range report.Results {
        if r.File != "tests/foo.c" {
            t.Errorf("%q File = %q, want tests/foo.c", r.Name, r.File)
        }
    }
    // Passing test line = 10, failing test line = 42.
    lineByName := map[string]int{}
    for _, r := range report.Results {
        lineByName[r.Name] = r.Line
    }
    if lineByName["passing"] != 10 || lineByName["failing"] != 42 {
        t.Errorf("lines = %v", lineByName)
    }
}

func TestJUnitParser_Parse_AntSimple(t *testing.T) {
    p := junitParser{}
    data := readFixture(t, "junit-ant/simple.xml")
    report, err := p.Parse(strings.NewReader(string(data)))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }
    if got := len(report.Results); got != 2 {
        t.Fatalf("results = %d, want 2", got)
    }
    if report.Metadata["root"] != "testsuite" {
        t.Errorf("root = %q, want testsuite", report.Metadata["root"])
    }
    // Both should have suite "my.module" since the bare-testsuite root
    // is treated as a one-suite testsuites.
    for _, r := range report.Results {
        if r.Suite != "my.module" {
            t.Errorf("%q suite = %q, want my.module", r.Name, r.Suite)
        }
    }
}

func TestJUnitParser_Parse_Entities(t *testing.T) {
    p := junitParser{}
    data := readFixture(t, "junit-ant/entities.xml")
    report, err := p.Parse(strings.NewReader(string(data)))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }
    if len(report.Results) != 1 {
        t.Fatalf("results = %d, want 1", len(report.Results))
    }
    r := report.Results[0]
    // XML entities should have been decoded during Unmarshal.
    if r.Name != "test & friends <check>" {
        t.Errorf("Name = %q", r.Name)
    }
    if r.Class != "q&a" {
        t.Errorf("Class = %q", r.Class)
    }
    if r.Message != `want "a", got 'b'` {
        t.Errorf("Message = %q", r.Message)
    }
}

// TestJUnitParser_Parse_RealCtestprobeXML runs the parser against a
// real Surefire document captured from ctestprobe running the compression
// repo's rle suite. Guards against future refactors regressing on the
// real-world shape that motivated this project.
func TestJUnitParser_Parse_RealCtestprobeXML(t *testing.T) {
    p := junitParser{}
    data := readFixture(t, "junit-surefire/real-ctestprobe-rle.xml")
    report, err := p.Parse(strings.NewReader(string(data)))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }
    if got := len(report.Results); got != 12 {
        t.Fatalf("results = %d, want 12", got)
    }
    for _, r := range report.Results {
        if r.Status != ir.StatusPassed {
            t.Errorf("%q: status = %v, want Passed", r.Name, r.Status)
        }
        if r.Suite != "ctestprobe" {
            t.Errorf("%q: suite = %q, want ctestprobe", r.Name, r.Suite)
        }
    }
}

func TestJUnitParser_Parse_ThroughDetect(t *testing.T) {
    // End-to-end sanity: Detect routes to junitParser and returns the
    // same shape as calling Parse directly.
    data := readFixture(t, "junit-surefire/mixed.xml")
    report, name, err := Detect(strings.NewReader(string(data)))
    if err != nil {
        t.Fatalf("Detect: %v", err)
    }
    if name != "junit" {
        t.Fatalf("Detect routed to %q, want junit", name)
    }
    if len(report.Results) != 4 {
        t.Errorf("results = %d, want 4", len(report.Results))
    }
}

func TestJUnitParser_Parse_NotJUnit(t *testing.T) {
    p := junitParser{}
    _, err := p.Parse(strings.NewReader(`<?xml version="1.0"?><assemblies/>`))
    if err == nil {
        t.Fatal("expected error on non-junit document")
    }
    if !strings.Contains(err.Error(), "assemblies") {
        t.Errorf("error should mention the offending root, got %v", err)
    }
}

func TestParseSeconds(t *testing.T) {
    cases := []struct {
        in   string
        want time.Duration
    }{
        {"", 0},
        {"0", 0},
        {"0.001", time.Millisecond},
        {"1.5", 1500 * time.Millisecond},
        {"garbage", 0},
    }
    for _, tc := range cases {
        if got := parseSeconds(tc.in); got != tc.want {
            t.Errorf("parseSeconds(%q) = %v, want %v", tc.in, got, tc.want)
        }
    }
}

func TestParseSurefireLocation(t *testing.T) {
    cases := []struct {
        in       string
        wantFile string
        wantLine int
        wantOK   bool
    }{
        {"tests/foo.c:42: assertion failed", "tests/foo.c", 42, true},
        {"  tests/foo.py:15: AssertionError", "tests/foo.py", 15, true},
        {"src/main.go:123:", "src/main.go", 123, true},
        {"just a message", "", 0, false},
        {"no line info in message", "", 0, false},
        // Requires extension in filename to reduce false positives.
        {"module::name:42:", "", 0, false},
        {"", "", 0, false},
    }
    for _, tc := range cases {
        f, l, ok := parseSurefireLocation(tc.in)
        if f != tc.wantFile || l != tc.wantLine || ok != tc.wantOK {
            t.Errorf("parseSurefireLocation(%q) = (%q, %d, %v), want (%q, %d, %v)",
                tc.in, f, l, ok, tc.wantFile, tc.wantLine, tc.wantOK)
        }
    }
}

// failingReader always returns a synthetic error, exercising the
// io.ReadAll path in junitParser.Parse.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestJUnitParser_Parse_ReadError(t *testing.T) {
    // Arrange
    boom := errors.New("read boom")

    // Act
    _, err := junitParser{}.Parse(failingReader{err: boom})

    // Assert
    if err == nil {
        t.Fatal("expected read error")
    }
    if !errors.Is(err, boom) {
        t.Errorf("error should wrap %v, got %v", boom, err)
    }
}

func TestJUnitParser_Parse_MalformedRoot(t *testing.T) {
    // Arrange — no root element at all. findRootElement fails.
    _, err := junitParser{}.Parse(strings.NewReader(`<?xml version="1.0"?>`))

    // Assert
    if err == nil {
        t.Fatal("expected error when no root element present")
    }
}

func TestJUnitParser_Parse_MalformedTestsuitesBody(t *testing.T) {
    // Arrange — valid root, but broken XML inside forces xml.Unmarshal
    // to error out.
    _, err := junitParser{}.Parse(strings.NewReader(
        `<testsuites><testsuite><testcase name="x" & broken></testsuite></testsuites>`,
    ))

    // Assert
    if err == nil {
        t.Fatal("expected unmarshal error on broken testsuites body")
    }
}

func TestJUnitParser_Parse_MalformedTestsuiteBody(t *testing.T) {
    // Arrange — bare <testsuite> root with a malformed inner element.
    _, err := junitParser{}.Parse(strings.NewReader(
        `<testsuite name="x"><testcase name="y" & broken></testsuite>`,
    ))

    // Assert
    if err == nil {
        t.Fatal("expected unmarshal error on broken testsuite body")
    }
}

func TestFirstNonEmpty(t *testing.T) {
    cases := []struct {
        a, b, want string
    }{
        {"first", "second", "first"},
        {"", "fallback", "fallback"},
        {"", "", ""},
    }
    for _, tc := range cases {
        if got := firstNonEmpty(tc.a, tc.b); got != tc.want {
            t.Errorf("firstNonEmpty(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
        }
    }
}

func TestSkipXMLProlog(t *testing.T) {
    cases := []struct {
        name string
        in   string
        want string
    }{
        {"leading whitespace", "   \n\t<testsuites/>", "<testsuites/>"},
        {"xml decl", `<?xml version="1.0"?><testsuite/>`, "<testsuite/>"},
        {"xml comment", `<!-- header --><testsuites/>`, "<testsuites/>"},
        {"unterminated xml decl", `<?xml no close`, ""},
        {"unterminated comment", `<!-- no close`, ""},
        {"multiple comments", `<!-- a --><!-- b --><root/>`, "<root/>"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if got := skipXMLProlog(tc.in); got != tc.want {
                t.Errorf("skipXMLProlog(%q) = %q, want %q", tc.in, got, tc.want)
            }
        })
    }
}

func TestFindRootElement_Empty(t *testing.T) {
    // Empty input: tokenizer sees EOF before any element.
    _, err := findRootElement([]byte(""))
    if err == nil {
        t.Fatal("expected error for empty input")
    }
    if !strings.Contains(err.Error(), "no root element") {
        t.Errorf("wrong error: %v", err)
    }
}

func TestFindRootElement_MalformedToken(t *testing.T) {
    // Unbalanced open — the tokenizer's error path (not the EOF path).
    _, err := findRootElement([]byte(`<?xml version="1.0"?><<<`))
    if err == nil {
        t.Fatal("expected tokenize error")
    }
}
