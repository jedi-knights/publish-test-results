package publish

import (
    "github.com/jedi-knights/publish-test-results/internal/ir"
)

// Options controls annotation generation.
type Options struct {
    // IncludePassed emits annotations for passing tests too (as
    // `notice` level), so users can click any test — not just the
    // failures — and jump to its source line.
    IncludePassed bool
    // IncludeSkipped emits annotations for skipped tests.
    IncludeSkipped bool
}

// DefaultOptions represents the recommended defaults: annotate every
// test we can locate, so drill-down works across the full grid.
func DefaultOptions() Options {
    return Options{
        IncludePassed:  true,
        IncludeSkipped: true,
    }
}

// AnnotationsFor turns a slice of results into the annotation slice
// that gets attached to the check-run. Tests without a File are
// skipped: the Checks API rejects annotations without `path`, and a
// tests-without-location bucket lives in the markdown body instead.
func AnnotationsFor(results []ir.TestResult, opts Options) []Annotation {
    out := make([]Annotation, 0, len(results))
    for _, r := range results {
        if r.File == "" {
            continue
        }
        switch r.Status {
        case ir.StatusPassed:
            if !opts.IncludePassed {
                continue
            }
        case ir.StatusSkipped:
            if !opts.IncludeSkipped {
                continue
            }
        }
        out = append(out, annotationFor(r))
    }
    return out
}

func annotationFor(r ir.TestResult) Annotation {
    line := r.Line
    if line <= 0 {
        line = 1
    }
    a := Annotation{
        Path:            r.File,
        StartLine:       line,
        EndLine:         line,
        AnnotationLevel: levelFor(r.Status),
        Title:           r.Name,
    }
    switch r.Status {
    case ir.StatusPassed:
        a.Message = "passed"
    case ir.StatusSkipped:
        a.Message = fallbackMessage(r.Message, "skipped")
    case ir.StatusFailed, ir.StatusError:
        a.Message = fallbackMessage(r.Message, "failed")
        a.RawDetails = r.Detail
    }
    return a
}

func fallbackMessage(msg, fallback string) string {
    if msg == "" {
        return fallback
    }
    return msg
}

func levelFor(s ir.Status) string {
    switch s {
    case ir.StatusFailed, ir.StatusError:
        return LevelFailure
    default:
        return LevelNotice
    }
}

// Chunk splits a slice of annotations into batches of size ≤ n, ready
// to hand to CreateCheckRun (first batch) and UpdateCheckRun (each
// following batch). Returns nil for empty input.
func Chunk(annotations []Annotation, n int) [][]Annotation {
    if len(annotations) == 0 {
        return nil
    }
    if n <= 0 {
        n = MaxAnnotationsPerRequest
    }
    var out [][]Annotation
    for i := 0; i < len(annotations); i += n {
        end := i + n
        if end > len(annotations) {
            end = len(annotations)
        }
        out = append(out, annotations[i:end])
    }
    return out
}
