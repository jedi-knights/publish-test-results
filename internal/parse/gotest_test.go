package parse

import (
    "errors"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/jedi-knights/publish-test-results/internal/ir"
)

func TestGoTest_CanParse(t *testing.T) {
    // Arrange
    cases := []struct {
        name    string
        preview string
        want    bool
    }{
        {"real event with Action", `{"Time":"2026-07-12T07:29:50.5Z","Action":"start","Package":"foo"}`, true},
        {"event with Test field", `{"Action":"run","Package":"foo","Test":"TestBar"}`, true},
        {"leading whitespace", "\n  {\"Action\":\"pass\",\"Package\":\"x\"}", true},
        {"junit xml", `<?xml version="1.0"?><testsuites/>`, false},
        {"jest json wrapper", `{"numFailedTests":0,"testResults":[]}`, false},
        {"empty", "", false},
        {"plain text", "hello world", false},
    }

    // Act / Assert
    p := goTestParser{}
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if got := p.CanParse([]byte(tc.preview)); got != tc.want {
                t.Errorf("CanParse(%q) = %v, want %v", tc.preview, got, tc.want)
            }
        })
    }
}

func TestGoTest_ParsesRealFixture(t *testing.T) {
    // Arrange
    data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "gotest", "real-ir-package.jsonl"))
    if err != nil {
        t.Fatalf("read fixture: %v", err)
    }

    // Act
    report, err := goTestParser{}.Parse(strings.NewReader(string(data)))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }

    // Assert
    if len(report.Results) != 1 {
        t.Fatalf("Results = %d, want 1", len(report.Results))
    }
    r := report.Results[0]
    if r.Name != "TestStatus_String" {
        t.Errorf("Name = %q, want TestStatus_String", r.Name)
    }
    if r.Status != ir.StatusPassed {
        t.Errorf("Status = %v, want passed", r.Status)
    }
    if !strings.HasSuffix(r.Suite, "internal/ir") {
        t.Errorf("Suite = %q, want ends with internal/ir", r.Suite)
    }
    if report.Metadata["dialect"] != "gotest" {
        t.Errorf("dialect = %q, want gotest", report.Metadata["dialect"])
    }
}

func TestGoTest_ParsesMixedStatuses(t *testing.T) {
    // Arrange
    data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "gotest", "mixed.jsonl"))
    if err != nil {
        t.Fatalf("read fixture: %v", err)
    }

    // Act
    report, err := goTestParser{}.Parse(strings.NewReader(string(data)))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }

    // Assert
    // Fixture has: TestAddition (pass), TestSubtraction (fail), TestPending
    // (skip), TestTable/positive (pass), TestTable/negative (fail). Parent
    // TestTable is skipped because its subtests were seen — leaves only.
    byName := make(map[string]ir.TestResult)
    for _, r := range report.Results {
        byName[r.Name] = r
    }
    if len(byName) != 5 {
        t.Fatalf("Results = %d, want 5 (leaves only, no TestTable parent); got %v", len(byName), keysOf(byName))
    }
    wantStatus := map[string]ir.Status{
        "TestAddition":       ir.StatusPassed,
        "TestSubtraction":    ir.StatusFailed,
        "TestPending":        ir.StatusSkipped,
        "TestTable/positive": ir.StatusPassed,
        "TestTable/negative": ir.StatusFailed,
    }
    for name, want := range wantStatus {
        got, ok := byName[name]
        if !ok {
            t.Errorf("missing result %q", name)
            continue
        }
        if got.Status != want {
            t.Errorf("%s: status = %v, want %v", name, got.Status, want)
        }
    }
    if _, ok := byName["TestTable"]; ok {
        t.Errorf("TestTable parent should be suppressed when subtests exist")
    }
}

func TestGoTest_ExtractsFileAndLineFromFailureOutput(t *testing.T) {
    // Arrange
    data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "gotest", "mixed.jsonl"))
    if err != nil {
        t.Fatalf("read fixture: %v", err)
    }

    // Act
    report, err := goTestParser{}.Parse(strings.NewReader(string(data)))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }

    // Assert
    byName := make(map[string]ir.TestResult)
    for _, r := range report.Results {
        byName[r.Name] = r
    }
    cases := []struct {
        name     string
        wantFile string
        wantLine int
    }{
        {"TestSubtraction", "math_test.go", 42},
        {"TestTable/negative", "table_test.go", 88},
    }
    for _, c := range cases {
        r := byName[c.name]
        if r.File != c.wantFile {
            t.Errorf("%s: File = %q, want %q", c.name, r.File, c.wantFile)
        }
        if r.Line != c.wantLine {
            t.Errorf("%s: Line = %d, want %d", c.name, r.Line, c.wantLine)
        }
    }
    // Detail should include the failure output line so annotations show it.
    if !strings.Contains(byName["TestSubtraction"].Detail, "expected 2, got 3") {
        t.Errorf("TestSubtraction.Detail = %q, want to contain the failure text", byName["TestSubtraction"].Detail)
    }
}

func TestGoTest_DetectRoutesToGoTestParser(t *testing.T) {
    // Arrange
    data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "gotest", "mixed.jsonl"))
    if err != nil {
        t.Fatalf("read fixture: %v", err)
    }

    // Act
    _, name, err := Detect(strings.NewReader(string(data)))

    // Assert
    if err != nil {
        t.Fatalf("Detect: %v", err)
    }
    if name != "gotest" {
        t.Errorf("parser name = %q, want gotest", name)
    }
}

func TestGoTest_MalformedLineIsIgnored(t *testing.T) {
    // Arrange — one valid line, one junk line, one valid line
    input := strings.Join([]string{
        `{"Action":"run","Package":"foo","Test":"TestA"}`,
        `not a json object`,
        `{"Action":"pass","Package":"foo","Test":"TestA","Elapsed":0.01}`,
    }, "\n")

    // Act
    report, err := goTestParser{}.Parse(strings.NewReader(input))

    // Assert
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }
    if len(report.Results) != 1 {
        t.Errorf("Results = %d, want 1 (malformed line skipped)", len(report.Results))
    }
}

func keysOf(m map[string]ir.TestResult) []string {
    out := make([]string, 0, len(m))
    for k := range m {
        out = append(out, k)
    }
    return out
}

func TestGoTest_BlankLinesAreSkipped(t *testing.T) {
    // Arrange — interleave blank lines between valid events.
    input := strings.Join([]string{
        `{"Action":"run","Package":"foo","Test":"TestA"}`,
        ``,
        ``,
        `{"Action":"pass","Package":"foo","Test":"TestA","Elapsed":0.01}`,
    }, "\n")

    // Act
    report, err := goTestParser{}.Parse(strings.NewReader(input))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }

    // Assert
    if len(report.Results) != 1 {
        t.Errorf("Results = %d, want 1", len(report.Results))
    }
}

func TestGoTest_UnterminatedTestBecomesError(t *testing.T) {
    // Arrange — the stream ends mid-test with no pass/fail/skip. This
    // simulates a crashed test binary or truncated log.
    input := `{"Action":"run","Package":"foo","Test":"TestCrashed"}`

    // Act
    report, err := goTestParser{}.Parse(strings.NewReader(input))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }

    // Assert
    if len(report.Results) != 1 {
        t.Fatalf("Results = %d, want 1", len(report.Results))
    }
    if report.Results[0].Status != ir.StatusError {
        t.Errorf("Status = %v, want StatusError", report.Results[0].Status)
    }
}

func TestGoTest_SkippedResultCarriesDetailAndMessage(t *testing.T) {
    // Arrange — mixed.jsonl has TestPending with the skip reason in output.
    data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "gotest", "mixed.jsonl"))
    if err != nil {
        t.Fatalf("read fixture: %v", err)
    }

    // Act
    report, err := goTestParser{}.Parse(strings.NewReader(string(data)))
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }

    // Assert
    var pending ir.TestResult
    for _, r := range report.Results {
        if r.Name == "TestPending" {
            pending = r
        }
    }
    if pending.Status != ir.StatusSkipped {
        t.Fatalf("TestPending status = %v, want skipped", pending.Status)
    }
    if !strings.Contains(pending.Detail, "not implemented yet") {
        t.Errorf("Detail should include skip reason, got %q", pending.Detail)
    }
    if !strings.Contains(pending.Message, "not implemented yet") {
        t.Errorf("Message should include skip reason, got %q", pending.Message)
    }
}

func TestExtractGoTestLocation(t *testing.T) {
    cases := []struct {
        name     string
        input    string
        wantFile string
        wantLine int
        wantOK   bool
    }{
        {"typical failure line", "    foo_test.go:42: expected 2 got 3", "foo_test.go", 42, true},
        {"path with slash", "    a/b/foo_test.go:10: msg", "a/b/foo_test.go", 10, true},
        {"no match", "this has no location marker", "", 0, false},
        {"missing suffix", "not a _test.go file:1: nope", "", 0, false},
        {"empty", "", "", 0, false},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            f, l, ok := extractGoTestLocation(tc.input)
            if f != tc.wantFile || l != tc.wantLine || ok != tc.wantOK {
                t.Errorf("got (%q, %d, %v), want (%q, %d, %v)",
                    f, l, ok, tc.wantFile, tc.wantLine, tc.wantOK)
            }
        })
    }
}

// gotestFailingReader emits a valid line, then a synthetic error on
// the next Read. Exercises the bufio.Scanner error branch in Parse.
type gotestFailingReader struct {
    buf  []byte
    read bool
    err  error
}

func (r *gotestFailingReader) Read(p []byte) (int, error) {
    if !r.read {
        n := copy(p, r.buf)
        r.read = true
        return n, nil
    }
    return 0, r.err
}

func TestGoTest_ScannerErrorSurfaces(t *testing.T) {
    // Arrange — first Read returns a partial line (no newline), second
    // Read returns a real error. bufio.Scanner surfaces the error via
    // scanner.Err() after the loop.
    boom := errors.New("io broke")
    r := &gotestFailingReader{
        buf: []byte(`{"Action":"run","Package":"foo","Test":"TestA"}`),
        err: boom,
    }

    // Act
    _, err := goTestParser{}.Parse(r)

    // Assert
    if err == nil {
        t.Fatal("expected scan error to surface")
    }
    if !errors.Is(err, boom) {
        t.Errorf("error should wrap %v, got %v", boom, err)
    }
}

func TestFirstErrorLine(t *testing.T) {
    cases := []struct {
        name string
        in   string
        want string
    }{
        {"empty", "", ""},
        {"only headers", "=== RUN   TestX\n--- PASS: TestX (0.00s)\n", ""},
        {"strips location prefix", "    foo_test.go:42: expected 2 got 3\n", "expected 2 got 3"},
        {"passes through plain line", "unexpected panic\n", "unexpected panic"},
        {"location with nothing after keeps line", "    foo_test.go:42:\nmore text\n", "foo_test.go:42:"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if got := firstErrorLine(tc.in); got != tc.want {
                t.Errorf("firstErrorLine(%q) = %q, want %q", tc.in, got, tc.want)
            }
        })
    }
}
