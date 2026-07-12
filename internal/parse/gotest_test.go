package parse

import (
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
