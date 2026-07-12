package parse

import (
    "bufio"
    "encoding/json"
    "fmt"
    "io"
    "regexp"
    "strconv"
    "strings"
    "time"

    "github.com/jedi-knights/publish-test-results/internal/ir"
)

// goTestParser handles `go test -json` streaming output: newline-
// delimited JSON events, one per line, produced by the Go toolchain.
// The format is a stream — not a document — so we accumulate output
// events per (Package, Test) key and finalize each result on its
// `pass`, `fail`, or `skip` action event.
//
// Two design choices worth noting:
//   1. Parent tests whose subtests were also observed are suppressed
//      from the output. A parent's pass/fail is a rollup of its subs;
//      emitting both parent and children produces duplicate rows and
//      duplicate annotations. Leaves carry the actionable detail.
//   2. File / Line are inferred from output events using the standard
//      `basename_test.go:LINE:` marker that `t.Errorf` and friends emit
//      before every failure message. Only the basename is available at
//      this stage; the filesystem locator resolves it to a repo-relative
//      path.
type goTestParser struct{}

func (goTestParser) Name() string { return "gotest" }

// CanParse detects the format by looking for a JSON object with an
// "Action" key and a "Package" key in the preview window — the pair is
// what distinguishes go test's event stream from Jest / Vitest / other
// JSON test reports whose top-level shape is a single object.
func (goTestParser) CanParse(preview []byte) bool {
    s := strings.TrimLeft(string(preview), " \t\r\n")
    if !strings.HasPrefix(s, "{") {
        return false
    }
    // Look at the first line only; the whole document is line-delimited
    // events, so the discriminator has to be in the first event.
    if nl := strings.IndexByte(s, '\n'); nl != -1 {
        s = s[:nl]
    }
    return strings.Contains(s, `"Action"`) && strings.Contains(s, `"Package"`)
}

// goTestEvent mirrors the schema emitted by `go test -json`. Fields
// not present on every event (Test, Output, Elapsed) are optional.
type goTestEvent struct {
    Time    time.Time `json:"Time"`
    Action  string    `json:"Action"`
    Package string    `json:"Package"`
    Test    string    `json:"Test"`
    Output  string    `json:"Output"`
    Elapsed float64   `json:"Elapsed"`
}

// goTestAccumulator collects streaming events for a single test until
// its terminal action arrives.
type goTestAccumulator struct {
    pkg     string
    test    string
    outputs []string
    status  ir.Status
    elapsed time.Duration
    done    bool
}

func (p goTestParser) Parse(r io.Reader) (ir.Report, error) {
    scanner := bufio.NewScanner(r)
    // Individual output events can be long (goroutine dumps, panics).
    // Default max is 64 KiB per line; bump so a single event of up to
    // 1 MiB fits without a false-negative parse.
    scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

    // Preserve insertion order of tests so the emitted results follow
    // the order the toolchain ran them in.
    var keys []string
    accs := map[string]*goTestAccumulator{}

    for scanner.Scan() {
        line := scanner.Bytes()
        if len(line) == 0 {
            continue
        }
        var ev goTestEvent
        if err := json.Unmarshal(line, &ev); err != nil {
            // Non-JSON lines are common in the wild — a panic banner or
            // stderr line from a broken test binary can end up in the
            // stream. Skip and keep going rather than aborting the
            // whole report.
            continue
        }
        // Package-level events (no Test field) drive nothing except
        // the summary. Ignore for per-test result extraction.
        if ev.Test == "" {
            continue
        }
        key := ev.Package + "::" + ev.Test
        acc, ok := accs[key]
        if !ok {
            acc = &goTestAccumulator{pkg: ev.Package, test: ev.Test}
            accs[key] = acc
            keys = append(keys, key)
        }
        switch ev.Action {
        case "output":
            acc.outputs = append(acc.outputs, ev.Output)
        case "pass":
            acc.status = ir.StatusPassed
            acc.elapsed = time.Duration(ev.Elapsed * float64(time.Second))
            acc.done = true
        case "fail":
            acc.status = ir.StatusFailed
            acc.elapsed = time.Duration(ev.Elapsed * float64(time.Second))
            acc.done = true
        case "skip":
            acc.status = ir.StatusSkipped
            acc.elapsed = time.Duration(ev.Elapsed * float64(time.Second))
            acc.done = true
        }
    }
    if err := scanner.Err(); err != nil {
        return ir.Report{}, fmt.Errorf("scan: %w", err)
    }

    // A parent test whose subtests were also observed is a rollup.
    // Emit only leaves so the annotation grid and summary table stay
    // one-row-per-actionable-test.
    hasChild := make(map[string]bool, len(keys))
    for _, k := range keys {
        if idx := strings.LastIndex(accs[k].test, "/"); idx > 0 {
            parentKey := accs[k].pkg + "::" + accs[k].test[:idx]
            hasChild[parentKey] = true
        }
    }

    results := make([]ir.TestResult, 0, len(keys))
    for _, k := range keys {
        acc := accs[k]
        if !acc.done {
            // Test ran but never terminated (crash mid-stream) — record
            // as an error so the failure surfaces.
            acc.status = ir.StatusError
        }
        if hasChild[k] {
            continue
        }
        results = append(results, goTestResultFrom(acc))
    }

    return ir.Report{
        Results: results,
        Metadata: map[string]string{
            "dialect": "gotest",
            "tool":    "go test -json",
        },
    }, nil
}

func goTestResultFrom(acc *goTestAccumulator) ir.TestResult {
    detail := strings.Join(acc.outputs, "")
    r := ir.TestResult{
        Suite:    acc.pkg,
        Name:     acc.test,
        Status:   acc.status,
        Duration: acc.elapsed,
    }
    switch acc.status {
    case ir.StatusFailed, ir.StatusError:
        r.Detail = strings.TrimSpace(detail)
        r.Message = firstErrorLine(detail)
        if f, l, ok := extractGoTestLocation(detail); ok {
            r.File = f
            r.Line = l
        }
    case ir.StatusSkipped:
        r.Detail = strings.TrimSpace(detail)
        r.Message = firstErrorLine(detail)
    case ir.StatusPassed:
        r.SystemOut = strings.TrimSpace(detail)
    }
    return r
}

// gotestLocationRE finds the first `basename_test.go:LINE:` marker in a
// test's output. Restricting to `_test.go` cuts false positives on
// paths that happen to appear inside error messages.
var gotestLocationRE = regexp.MustCompile(`([A-Za-z0-9_./-]+_test\.go):(\d+):`)

func extractGoTestLocation(output string) (string, int, bool) {
    m := gotestLocationRE.FindStringSubmatch(output)
    if m == nil {
        return "", 0, false
    }
    line, err := strconv.Atoi(m[2])
    if err != nil {
        return "", 0, false
    }
    return m[1], line, true
}

// firstErrorLine returns a one-line summary from the accumulated output
// — the first line containing the location marker, stripped of it. This
// keeps the annotation title readable when the full Detail is a
// multi-line stack.
func firstErrorLine(output string) string {
    for line := range strings.SplitSeq(output, "\n") {
        trimmed := strings.TrimSpace(line)
        if trimmed == "" {
            continue
        }
        if strings.HasPrefix(trimmed, "=== ") || strings.HasPrefix(trimmed, "--- ") {
            continue
        }
        // Strip `basename_test.go:LINE:` prefix if present so the
        // message is just the assertion text.
        if m := gotestLocationRE.FindStringIndex(trimmed); m != nil && m[0] == 0 {
            rest := strings.TrimSpace(trimmed[m[1]:])
            if rest != "" {
                return rest
            }
        }
        return trimmed
    }
    return ""
}

func init() {
    Register(goTestParser{})
}
