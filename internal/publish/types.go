// Package publish drives the GitHub Checks API to turn a parsed
// []ir.TestResult into a drilldownable check-run with per-test
// annotations, a markdown table summary, and an expandable body.
//
// The types in this file mirror the Checks API shapes documented at
// https://docs.github.com/en/rest/checks/runs. Only the fields we
// actually populate are modeled; unknown fields in server responses
// are discarded by encoding/json.
package publish

// Annotation is one clickable pin on the code diff. `Path` must be
// repo-relative; `StartLine` is one-based. `AnnotationLevel` is one of
// "notice", "warning", or "failure" — the value determines the pin's
// color and whether it counts against required checks.
type Annotation struct {
    Path            string `json:"path"`
    StartLine       int    `json:"start_line"`
    EndLine         int    `json:"end_line"`
    AnnotationLevel string `json:"annotation_level"`
    Message         string `json:"message"`
    Title           string `json:"title,omitempty"`
    RawDetails      string `json:"raw_details,omitempty"`
}

// Output is the rich body of the check-run: the summary shown at the
// top, the collapsible text below, and up to 50 annotations per
// request.
type Output struct {
    Title       string       `json:"title"`
    Summary     string       `json:"summary"`
    Text        string       `json:"text,omitempty"`
    Annotations []Annotation `json:"annotations,omitempty"`
}

// CheckRunCreate is the POST body for creating a new check-run.
// Conclusion is required when Status="completed" and must be one of
// "success", "failure", "neutral", "cancelled", "skipped",
// "timed_out", or "action_required".
type CheckRunCreate struct {
    Name       string `json:"name"`
    HeadSHA    string `json:"head_sha"`
    Status     string `json:"status"`
    Conclusion string `json:"conclusion,omitempty"`
    Output     Output `json:"output"`
}

// CheckRunUpdate is the PATCH body used to append more annotations to
// an existing check-run once the first CheckRunCreate has consumed
// the API's 50-annotation-per-request limit.
type CheckRunUpdate struct {
    Output Output `json:"output"`
}

// CheckRunResponse is the subset of the API's create/update response
// we care about. `HTMLURL` is what you'd click on to open the check
// in the browser.
type CheckRunResponse struct {
    ID      int64  `json:"id"`
    Name    string `json:"name"`
    HeadSHA string `json:"head_sha"`
    HTMLURL string `json:"html_url"`
    Status  string `json:"status"`
}

// Conclusion values recognized by the Checks API.
const (
    ConclusionSuccess        = "success"
    ConclusionFailure        = "failure"
    ConclusionNeutral        = "neutral"
    ConclusionSkipped        = "skipped"
    ConclusionCancelled      = "cancelled"
    ConclusionTimedOut       = "timed_out"
    ConclusionActionRequired = "action_required"
)

// Annotation levels recognized by the Checks API.
const (
    LevelNotice  = "notice"
    LevelWarning = "warning"
    LevelFailure = "failure"
)

// MaxAnnotationsPerRequest is the API's hard limit. Bodies with more
// than this many entries are rejected; the client batches around it.
const MaxAnnotationsPerRequest = 50
