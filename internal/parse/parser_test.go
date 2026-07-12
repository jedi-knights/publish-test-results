package parse

import (
    "errors"
    "io"
    "strings"
    "testing"

    "github.com/jedi-knights/publish-test-results/internal/ir"
)

// stubParser matches inputs whose preview begins with a given prefix
// and emits a fixed one-test Report. Purely a test aid.
type stubParser struct {
    name   string
    prefix string
    want   ir.Report
    err    error
}

func (s *stubParser) Name() string                    { return s.name }
func (s *stubParser) CanParse(preview []byte) bool    { return strings.HasPrefix(string(preview), s.prefix) }
func (s *stubParser) Parse(_ io.Reader) (ir.Report, error) { return s.want, s.err }

// resetRegistry swaps the package-level registry for the test's
// lifetime and restores the original on cleanup. Guards against tests
// polluting each other.
func resetRegistry(t *testing.T, replacement []Parser) {
    t.Helper()
    saved := registry
    registry = replacement
    t.Cleanup(func() { registry = saved })
}

func TestDetect_RoutesByPreview(t *testing.T) {
    want := ir.Report{Results: []ir.TestResult{{Name: "expected"}}}
    resetRegistry(t, []Parser{
        &stubParser{name: "alpha", prefix: "A", want: want},
        &stubParser{name: "beta", prefix: "B", want: ir.Report{}},
    })

    report, name, err := Detect(strings.NewReader("AAAA-payload"))
    if err != nil {
        t.Fatalf("Detect returned error: %v", err)
    }
    if name != "alpha" {
        t.Errorf("routed to %q, want %q", name, "alpha")
    }
    if len(report.Results) != 1 || report.Results[0].Name != "expected" {
        t.Errorf("unexpected report: %+v", report)
    }
}

func TestDetect_FirstMatchWins(t *testing.T) {
    resetRegistry(t, []Parser{
        &stubParser{name: "first", prefix: "X", want: ir.Report{Metadata: map[string]string{"picked": "first"}}},
        &stubParser{name: "second", prefix: "X", want: ir.Report{Metadata: map[string]string{"picked": "second"}}},
    })

    report, name, err := Detect(strings.NewReader("XYZ"))
    if err != nil {
        t.Fatalf("Detect returned error: %v", err)
    }
    if name != "first" {
        t.Errorf("routed to %q, want %q", name, "first")
    }
    if report.Metadata["picked"] != "first" {
        t.Errorf("report picked = %q, want first", report.Metadata["picked"])
    }
}

func TestDetect_NoMatch(t *testing.T) {
    resetRegistry(t, []Parser{
        &stubParser{name: "alpha", prefix: "A"},
    })

    _, _, err := Detect(strings.NewReader("nope"))
    if !errors.Is(err, ErrNoParserMatched) {
        t.Errorf("err = %v, want ErrNoParserMatched", err)
    }
}

func TestDetect_ShortInput(t *testing.T) {
    // Preview shorter than PreviewSize: ReadFull returns io.ErrUnexpectedEOF,
    // which Detect should ignore so parsing proceeds normally.
    want := ir.Report{Results: []ir.TestResult{{Name: "tiny"}}}
    resetRegistry(t, []Parser{
        &stubParser{name: "alpha", prefix: "hi", want: want},
    })

    report, _, err := Detect(strings.NewReader("hi"))
    if err != nil {
        t.Fatalf("Detect on short input: %v", err)
    }
    if len(report.Results) != 1 || report.Results[0].Name != "tiny" {
        t.Errorf("unexpected report on short input: %+v", report)
    }
}

func TestDetect_EmptyInput(t *testing.T) {
    resetRegistry(t, []Parser{
        &stubParser{name: "alpha", prefix: "any"},
    })

    _, _, err := Detect(strings.NewReader(""))
    if !errors.Is(err, ErrNoParserMatched) {
        t.Errorf("empty input err = %v, want ErrNoParserMatched", err)
    }
}

func TestRegister_AppendsInOrder(t *testing.T) {
    resetRegistry(t, nil)
    Register(&stubParser{name: "one", prefix: "1"})
    Register(&stubParser{name: "two", prefix: "2"})
    Register(&stubParser{name: "three", prefix: "3"})

    got := Registered()
    if len(got) != 3 {
        t.Fatalf("Registered length = %d, want 3", len(got))
    }
    wantOrder := []string{"one", "two", "three"}
    for i, p := range got {
        if p.Name() != wantOrder[i] {
            t.Errorf("Registered[%d].Name() = %q, want %q", i, p.Name(), wantOrder[i])
        }
    }
}
