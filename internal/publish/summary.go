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
        addStatusToTotals(&t, r.Status)
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

// Title is what appears in the Checks tab list entry. Failed and
// errored counts are surfaced separately when both are non-zero so
// the reader can distinguish assertion failures from infra crashes.
func Title(t Totals) string {
    switch Conclusion(t) {
    case ConclusionFailure:
        return failureTitle(t)
    case ConclusionSkipped:
        return fmt.Sprintf("all %d %s skipped", t.Total, pluralTest(t.Total))
    default:
        return fmt.Sprintf("%d %s passed", t.Passed, pluralTest(t.Passed))
    }
}

func failureTitle(t Totals) string {
    var parts []string
    if t.Failed > 0 {
        parts = append(parts, fmt.Sprintf("%d failed", t.Failed))
    }
    if t.Errored > 0 {
        parts = append(parts, fmt.Sprintf("%d errored", t.Errored))
    }
    return fmt.Sprintf("%s of %d %s", strings.Join(parts, ", "), t.Total, pluralTest(t.Total))
}

// SummaryMarkdown renders a compact glance bar plus a per-suite count
// table. The glance bar sits at the top so an at-a-glance reader can
// judge the run in one line; the table gives the per-suite breakdown.
// Suite names share their longest common slash-terminated prefix
// stripped so the Suite column stays readable when every row is under
// the same Go module.
func SummaryMarkdown(results []ir.TestResult) string {
    order, perSuite := tallyBySuite(results)
    prefix := commonSuitePrefix(order)

    var b strings.Builder
    if bar := glanceBar(Compute(results)); bar != "" {
        b.WriteString(bar)
        b.WriteString("\n\n")
    }
    b.WriteString("| Suite | Passed | Failed | Errored | Skipped | Total |\n")
    b.WriteString("|---|---|---|---|---|---|\n")
    grand := Totals{}
    for _, name := range order {
        t := perSuite[name]
        fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d |\n",
            trimSuite(name, prefix), t.Passed, t.Failed, t.Errored, t.Skipped, t.Total)
        addTotals(&grand, t)
    }
    fmt.Fprintf(&b, "| **Total** | **%d** | **%d** | **%d** | **%d** | **%d** |\n",
        grand.Passed, grand.Failed, grand.Errored, grand.Skipped, grand.Total)
    return b.String()
}

// tallyBySuite groups results by suite, folding per-status counters
// as it walks. Suite names are returned in stable alphabetical order.
func tallyBySuite(results []ir.TestResult) (order []string, per map[string]*Totals) {
    per = map[string]*Totals{}
    for _, r := range results {
        name := suiteKey(r.Suite)
        agg, ok := per[name]
        if !ok {
            agg = &Totals{}
            per[name] = agg
            order = append(order, name)
        }
        agg.Total++
        addStatusToTotals(agg, r.Status)
    }
    sort.Strings(order)
    return order, per
}

// suiteKey folds the empty-suite bucket into a stable placeholder so
// downstream code never has to special-case it.
func suiteKey(suite string) string {
    if suite == "" {
        return "(unspecified)"
    }
    return suite
}

// addStatusToTotals increments the per-status counter for s. Kept
// separate from Compute so both call sites share one implementation.
func addStatusToTotals(t *Totals, s ir.Status) {
    switch s {
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

// addTotals folds one suite's counters into the grand total.
func addTotals(grand, t *Totals) {
    grand.Passed += t.Passed
    grand.Failed += t.Failed
    grand.Errored += t.Errored
    grand.Skipped += t.Skipped
    grand.Total += t.Total
}

// BodyMarkdown renders the per-test detail below the summary. Every
// test is listed as a bullet — the earlier "all N passed" summary
// line hid detail readers wanted to audit. Mixed suites still list
// failures/errors/skips at the top and hide the passing tests inside
// a details block so the eye lands on what needs attention.
func BodyMarkdown(results []ir.TestResult) string {
    order, bySuite := groupBySuite(results)
    prefix := commonSuitePrefix(order)

    var b strings.Builder
    for _, name := range order {
        writeSuite(&b, trimSuite(name, prefix), bySuite[name])
    }
    return b.String()
}

// groupBySuite bins results by suite, preserving first-seen order
// before returning the alphabetically sorted slice of names.
func groupBySuite(results []ir.TestResult) (order []string, per map[string][]ir.TestResult) {
    per = map[string][]ir.TestResult{}
    for _, r := range results {
        name := suiteKey(r.Suite)
        if _, ok := per[name]; !ok {
            order = append(order, name)
        }
        per[name] = append(per[name], r)
    }
    sort.Strings(order)
    return order, per
}

// writeSuite emits the heading, then the non-pass and pass blocks for
// one suite. Fully-passing suites still list every test — the earlier
// one-line summary hid audit detail readers wanted.
func writeSuite(b *strings.Builder, name string, rs []ir.TestResult) {
    fmt.Fprintf(b, "### %s (%d %s)\n\n", name, len(rs), pluralTest(len(rs)))
    nonPass, passed := partitionByStatus(rs)

    if len(nonPass) == 0 {
        sort.SliceStable(passed, func(i, j int) bool {
            return passed[i].Name < passed[j].Name
        })
        writeBlock(b, passed)
        b.WriteString("\n")
        return
    }

    sort.SliceStable(nonPass, func(i, j int) bool {
        a, c := nonPass[i], nonPass[j]
        if a.Status != c.Status {
            return statusOrder(a.Status) < statusOrder(c.Status)
        }
        return a.Name < c.Name
    })
    writeBlock(b, nonPass)

    if len(passed) > 0 {
        fmt.Fprintf(b, "\n<details><summary>%d passed %s</summary>\n\n",
            len(passed), pluralTest(len(passed)))
        sort.SliceStable(passed, func(i, j int) bool {
            return passed[i].Name < passed[j].Name
        })
        writeBlock(b, passed)
        b.WriteString("\n</details>\n")
    }
    b.WriteString("\n")
}

// partitionByStatus splits results into the non-passing and passing
// slices in a single pass.
func partitionByStatus(rs []ir.TestResult) (nonPass, passed []ir.TestResult) {
    for _, r := range rs {
        if r.Status == ir.StatusPassed {
            passed = append(passed, r)
        } else {
            nonPass = append(nonPass, r)
        }
    }
    return nonPass, passed
}

// writeBlock renders a set of results as a bullet list, nesting
// subtests under their parent when both live in the same block.
func writeBlock(b *strings.Builder, rs []ir.TestResult) {
    present := make(map[string]bool, len(rs))
    for _, r := range rs {
        present[r.Name] = true
    }
    for _, r := range rs {
        b.WriteString(renderTestLine(r, nestingDepth(r.Name, present)))
        b.WriteString("\n")
    }
}

// renderTestLine formats one test as a single markdown bullet, indented
// by depth. Failures append a `File:Line` locator and the first line
// of the failure message so multi-line stack traces don't bleed into
// the bullet.
func renderTestLine(r ir.TestResult, depth int) string {
    var b strings.Builder
    b.WriteString(strings.Repeat("  ", depth))
    fmt.Fprintf(&b, "- %s `%s`", statusIcon(r.Status), r.Name)
    if r.Status != ir.StatusPassed {
        if r.File != "" {
            fmt.Fprintf(&b, " — %s", locString(r.File, r.Line))
        }
        if msg := firstLine(r.Message); msg != "" {
            fmt.Fprintf(&b, " — %s", msg)
        }
    }
    return b.String()
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

// statusOrder orders non-passing statuses so failures land above
// errors, and errors above skips. Passed is last so mixed blocks that
// somehow include it don't push failures out of view.
func statusOrder(s ir.Status) int {
    switch s {
    case ir.StatusFailed:
        return 0
    case ir.StatusError:
        return 1
    case ir.StatusSkipped:
        return 2
    default:
        return 3
    }
}

// glanceBar renders a one-line status summary of nonzero counts so a
// reader can judge the run without scanning the table. Returns "" for
// an empty totals struct.
func glanceBar(t Totals) string {
    if t.Total == 0 {
        return ""
    }
    parts := []string{}
    if t.Passed > 0 {
        parts = append(parts, fmt.Sprintf("✅ %d", t.Passed))
    }
    if t.Failed > 0 {
        parts = append(parts, fmt.Sprintf("❌ %d", t.Failed))
    }
    if t.Errored > 0 {
        parts = append(parts, fmt.Sprintf("⚠️ %d", t.Errored))
    }
    if t.Skipped > 0 {
        parts = append(parts, fmt.Sprintf("⏭️ %d", t.Skipped))
    }
    return strings.Join(parts, " · ")
}

// commonSuitePrefix returns the longest slash-terminated prefix shared
// by every named suite. Empty when fewer than two named suites are
// present or when they share no leading path segment — trimming
// nothing is preferable to trimming a single character. The
// "(unspecified)" placeholder never participates in the calculation.
func commonSuitePrefix(names []string) string {
    real := realSuiteNames(names)
    if len(real) < 2 {
        return ""
    }
    prefix := longestCommonPrefix(real)
    i := strings.LastIndex(prefix, "/")
    if i < 0 {
        return ""
    }
    return prefix[:i+1]
}

// realSuiteNames filters out the "(unspecified)" placeholder so that
// bucket does not drag the shared prefix down to "".
func realSuiteNames(names []string) []string {
    out := make([]string, 0, len(names))
    for _, s := range names {
        if s != "" && s != "(unspecified)" {
            out = append(out, s)
        }
    }
    return out
}

// longestCommonPrefix returns the longest byte-wise prefix shared by
// every string; empty when any pair disagrees at position 0.
func longestCommonPrefix(ss []string) string {
    prefix := ss[0]
    for _, s := range ss[1:] {
        prefix = commonByteRun(prefix, s)
        if prefix == "" {
            return ""
        }
    }
    return prefix
}

// commonByteRun returns the longest byte-wise common prefix of a and b.
func commonByteRun(a, b string) string {
    n := 0
    for n < len(a) && n < len(b) && a[n] == b[n] {
        n++
    }
    return a[:n]
}

// trimSuite strips the shared prefix, leaving the placeholder name
// alone so the fallback bucket stays recognizable.
func trimSuite(suite, prefix string) string {
    if prefix == "" || suite == "(unspecified)" {
        return suite
    }
    return strings.TrimPrefix(suite, prefix)
}

// nestingDepth returns how many ancestors of name are present in the
// same block. A subtest indents once per ancestor that also renders
// so the parent/child relationship is visible; when the parent is in
// a different block (e.g. passed while the child failed) we do not
// indent because there is nothing to nest under.
func nestingDepth(name string, present map[string]bool) int {
    d := 0
    for {
        i := strings.LastIndex(name, "/")
        if i < 0 {
            return d
        }
        parent := name[:i]
        if !present[parent] {
            return d
        }
        d++
        name = parent
    }
}

// pluralTest returns "test" for one, "tests" otherwise.
func pluralTest(n int) string {
    if n == 1 {
        return "test"
    }
    return "tests"
}

// locString formats a file location; Line == 0 is treated as unknown
// and omitted so we don't emit a nonsensical ":0" suffix.
func locString(file string, line int) string {
    if line <= 0 {
        return file
    }
    return fmt.Sprintf("%s:%d", file, line)
}

// firstLine returns the first non-blank line of s, trimmed. Used to
// keep multi-line failure messages from bleeding into a bullet.
func firstLine(s string) string {
    for _, ln := range strings.Split(s, "\n") {
        ln = strings.TrimSpace(ln)
        if ln != "" {
            return ln
        }
    }
    return ""
}
