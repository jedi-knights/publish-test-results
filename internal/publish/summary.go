package publish

import (
    "fmt"
    "sort"
    "strings"

    "github.com/jedi-knights/publish-test-results/internal/ir"
)

// Totals holds the aggregate outcome counts used in the check-run
// title, summary line, and conclusion.
type Totals struct {
    Passed  int
    Failed  int
    Skipped int
    Errored int
    Total   int
}

// Compute walks results once and folds the per-status counts.
func Compute(results []ir.TestResult) Totals {
    var t Totals
    for _, r := range results {
        t.Total++
        switch r.Status {
        case ir.StatusPassed:
            t.Passed++
        case ir.StatusFailed:
            t.Failed++
        case ir.StatusSkipped:
            t.Skipped++
        case ir.StatusError:
            t.Errored++
        }
    }
    return t
}

// Conclusion picks the Checks API conclusion string that best matches
// the aggregate outcomes.
//
//   - any failed or errored → "failure"
//   - all skipped (with no failed/errored, no passed) → "skipped"
//   - otherwise → "success"
func Conclusion(t Totals) string {
    if t.Failed > 0 || t.Errored > 0 {
        return ConclusionFailure
    }
    if t.Passed == 0 && t.Skipped > 0 {
        return ConclusionSkipped
    }
    return ConclusionSuccess
}

// Title is what appears in the Checks tab list entry.
func Title(t Totals) string {
    switch Conclusion(t) {
    case ConclusionFailure:
        return fmt.Sprintf("%d failed of %d test(s)", t.Failed+t.Errored, t.Total)
    case ConclusionSkipped:
        return fmt.Sprintf("all %d test(s) skipped", t.Total)
    default:
        return fmt.Sprintf("%d test(s) passed", t.Passed)
    }
}

// SummaryMarkdown renders the per-suite counts as a table. Rendered
// at the top of the check-run detail page.
func SummaryMarkdown(results []ir.TestResult) string {
    perSuite := map[string]*Totals{}
    order := []string{}
    for _, r := range results {
        suite := r.Suite
        if suite == "" {
            suite = "(unspecified)"
        }
        agg, ok := perSuite[suite]
        if !ok {
            agg = &Totals{}
            perSuite[suite] = agg
            order = append(order, suite)
        }
        agg.Total++
        switch r.Status {
        case ir.StatusPassed:
            agg.Passed++
        case ir.StatusFailed:
            agg.Failed++
        case ir.StatusSkipped:
            agg.Skipped++
        case ir.StatusError:
            agg.Errored++
        }
    }
    sort.Strings(order)

    var b strings.Builder
    b.WriteString("| Suite | Passed | Failed | Errored | Skipped | Total |\n")
    b.WriteString("|---|---|---|---|---|---|\n")
    grand := Totals{}
    for _, name := range order {
        t := perSuite[name]
        fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d |\n",
            name, t.Passed, t.Failed, t.Errored, t.Skipped, t.Total)
        grand.Passed += t.Passed
        grand.Failed += t.Failed
        grand.Errored += t.Errored
        grand.Skipped += t.Skipped
        grand.Total += t.Total
    }
    fmt.Fprintf(&b, "| **Total** | **%d** | **%d** | **%d** | **%d** | **%d** |\n",
        grand.Passed, grand.Failed, grand.Errored, grand.Skipped, grand.Total)
    return b.String()
}

// BodyMarkdown renders the collapsible per-test detail below the
// summary. Suites with many tests are collapsed inside <details> so
// the initial view stays scannable.
func BodyMarkdown(results []ir.TestResult) string {
    bySuite := map[string][]ir.TestResult{}
    order := []string{}
    for _, r := range results {
        suite := r.Suite
        if suite == "" {
            suite = "(unspecified)"
        }
        if _, ok := bySuite[suite]; !ok {
            order = append(order, suite)
        }
        bySuite[suite] = append(bySuite[suite], r)
    }
    sort.Strings(order)

    const collapseThreshold = 20

    var b strings.Builder
    for _, name := range order {
        rs := bySuite[name]
        fmt.Fprintf(&b, "### %s (%d test(s))\n\n", name, len(rs))
        collapsed := len(rs) > collapseThreshold
        if collapsed {
            fmt.Fprintf(&b, "<details><summary>Expand</summary>\n\n")
        }
        for _, r := range rs {
            b.WriteString(renderTestLine(r))
            b.WriteString("\n")
        }
        if collapsed {
            fmt.Fprintf(&b, "\n</details>\n")
        }
        b.WriteString("\n")
    }
    return b.String()
}

// renderTestLine formats one test as a single markdown bullet.
func renderTestLine(r ir.TestResult) string {
    icon := statusIcon(r.Status)
    var suffix string
    if r.Message != "" {
        suffix = " — " + r.Message
    }
    return fmt.Sprintf("- %s `%s`%s", icon, r.Name, suffix)
}

func statusIcon(s ir.Status) string {
    switch s {
    case ir.StatusPassed:
        return "✅"
    case ir.StatusFailed:
        return "❌"
    case ir.StatusSkipped:
        return "⏭️"
    case ir.StatusError:
        return "⚠️"
    default:
        return "❓"
    }
}
