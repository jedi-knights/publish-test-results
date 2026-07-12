// Package ir defines the intermediate representation that every input-format
// parser produces and every publisher consumes. Keeping this small and
// dialect-neutral is what lets the parser bank grow without touching the
// publish path.
package ir

import "time"

// Status is the outcome of a single test.
type Status int

const (
    // StatusPassed — the test ran and its assertions held.
    StatusPassed Status = iota
    // StatusFailed — the test ran and at least one assertion failed.
    StatusFailed
    // StatusSkipped — the test was skipped (filter, precondition, tag).
    StatusSkipped
    // StatusError — the test could not be run to completion: crash,
    // timeout, or infrastructure failure. Distinct from an assertion
    // failure because most CI parsers surface it differently.
    StatusError
)

// String returns a stable lower-case token used in metadata and diagnostics.
func (s Status) String() string {
    switch s {
    case StatusPassed:
        return "passed"
    case StatusFailed:
        return "failed"
    case StatusSkipped:
        return "skipped"
    case StatusError:
        return "error"
    default:
        return "unknown"
    }
}

// TestResult is the normalized shape every parser emits per test. Fields
// that a given dialect does not carry are left at their zero value; the
// filesystem locator fills File / Line when possible for producers that
// don't emit source location.
type TestResult struct {
    Suite    string
    Class    string
    Name     string
    Status   Status
    Duration time.Duration

    // Message is a one-line failure summary, empty for passes/skips.
    Message string
    // Detail is the full failure body — stack trace, stderr, whatever
    // the producer captured.
    Detail string

    // File and Line locate the assertion (or the test function, for
    // producers that don't distinguish). Empty / 0 when unknown.
    File string
    Line int

    // Attempt tracks retries: 0 for first run, higher for reruns. Most
    // dialects don't carry this; parsers set it when they can.
    Attempt int

    SystemOut string
    SystemErr string
}

// Report is a parsed test-run: every result plus dialect-level metadata
// (tool name, tool version, timestamp, hostname where available).
type Report struct {
    Results  []TestResult
    Metadata map[string]string
}
