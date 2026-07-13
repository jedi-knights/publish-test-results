package publish

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/jedi-knights/publish-test-results/internal/ir"
)

// recordedRequest is what our mock server captures for each API call
// so tests can assert against method / path / body without touching
// real HTTP infrastructure.
type recordedRequest struct {
    Method string
    Path   string
    Body   json.RawMessage
    Header http.Header
}

// mockServer stands in for api.github.com. Each request is appended
// to Requests, and the response is looked up in Responses (fifo). If
// Responses is empty, a plausible 201 with an incrementing ID is
// returned so tests that don't care about the response don't need to
// build one.
type mockServer struct {
    mu        sync.Mutex
    server    *httptest.Server
    Requests  []recordedRequest
    Responses []mockResponse
    nextID    int64
}

type mockResponse struct {
    StatusCode int
    Body       any
    Headers    map[string]string
}

func newMockServer() *mockServer {
    m := &mockServer{}
    m.server = httptest.NewServer(http.HandlerFunc(m.handle))
    return m
}

func (m *mockServer) URL() string { return m.server.URL }
func (m *mockServer) Close()      { m.server.Close() }

func (m *mockServer) handle(w http.ResponseWriter, r *http.Request) {
    body, _ := io.ReadAll(r.Body)
    _ = r.Body.Close()

    m.mu.Lock()
    m.Requests = append(m.Requests, recordedRequest{
        Method: r.Method,
        Path:   r.URL.Path,
        Body:   body,
        Header: r.Header.Clone(),
    })
    var resp mockResponse
    if len(m.Responses) > 0 {
        resp = m.Responses[0]
        m.Responses = m.Responses[1:]
    } else {
        m.nextID++
        resp = mockResponse{
            StatusCode: 201,
            Body: CheckRunResponse{
                ID:      m.nextID,
                Name:    "Test Results",
                Status:  "completed",
                HTMLURL: fmt.Sprintf("https://github.com/o/r/runs/%d", m.nextID),
            },
        }
    }
    m.mu.Unlock()

    for k, v := range resp.Headers {
        w.Header().Set(k, v)
    }
    if resp.StatusCode == 0 {
        resp.StatusCode = 200
    }
    w.WriteHeader(resp.StatusCode)
    if resp.Body != nil {
        _ = json.NewEncoder(w).Encode(resp.Body)
    }
}

// newTestClient wires a Client at the mock server with instant retries.
func newTestClient(t *testing.T, url string) *Client {
    t.Helper()
    return &Client{
        HTTPClient: &http.Client{Timeout: 5 * time.Second},
        BaseURL:    url,
        Token:      "fake-token",
        Owner:      "o",
        Repo:       "r",
        UserAgent:  "test/1.0",
        MaxRetries: 3,
        Now:        time.Now,
        Sleep:      func(time.Duration) {}, // instant retries in tests
    }
}

func TestPublish_HappyPath(t *testing.T) {
    m := newMockServer()
    defer m.Close()
    c := newTestClient(t, m.URL())

    results := []ir.TestResult{
        {Suite: "alpha", Name: "one", Status: ir.StatusPassed, File: "a.go", Line: 10},
        {Suite: "alpha", Name: "two", Status: ir.StatusFailed, File: "a.go", Line: 20, Message: "want 2 got 3"},
    }

    resp, err := Publish(context.Background(), c, Config{
        CheckName: "Test Results",
        HeadSHA:   "deadbeef",
        Options:   Options{IncludePassed: true, IncludeSkipped: true},
    }, results)
    if err != nil {
        t.Fatalf("Publish: %v", err)
    }
    if resp.ID != 1 {
        t.Errorf("resp.ID = %d, want 1", resp.ID)
    }

    if got := len(m.Requests); got != 1 {
        t.Fatalf("HTTP calls = %d, want 1", got)
    }
    req := m.Requests[0]
    if req.Method != http.MethodPost {
        t.Errorf("method = %q, want POST", req.Method)
    }
    if req.Path != "/repos/o/r/check-runs" {
        t.Errorf("path = %q", req.Path)
    }
    var create CheckRunCreate
    if err := json.Unmarshal(req.Body, &create); err != nil {
        t.Fatalf("decode create body: %v", err)
    }
    if create.HeadSHA != "deadbeef" {
        t.Errorf("head_sha = %q", create.HeadSHA)
    }
    if create.Conclusion != ConclusionFailure {
        t.Errorf("conclusion = %q, want failure", create.Conclusion)
    }
    if got := len(create.Output.Annotations); got != 2 {
        t.Errorf("annotations = %d, want 2", got)
    }
}

func TestPublish_BatchesAnnotations(t *testing.T) {
    m := newMockServer()
    defer m.Close()
    c := newTestClient(t, m.URL())

    // 120 results with file/line so all become annotations.
    // 120 → three batches of 50, 50, 20 → one CREATE + two PATCH calls.
    results := make([]ir.TestResult, 120)
    for i := range results {
        results[i] = ir.TestResult{
            Suite:  "big",
            Name:   fmt.Sprintf("test_%03d", i),
            Status: ir.StatusPassed,
            File:   "big.go",
            Line:   i + 1,
        }
    }
    _, err := Publish(context.Background(), c, Config{
        HeadSHA: "abc123",
        Options: Options{IncludePassed: true, IncludeSkipped: true},
    }, results)
    if err != nil {
        t.Fatalf("Publish: %v", err)
    }

    if got := len(m.Requests); got != 3 {
        t.Fatalf("HTTP calls = %d, want 3 (1 create + 2 patches)", got)
    }
    if m.Requests[0].Method != http.MethodPost {
        t.Errorf("first call = %q, want POST", m.Requests[0].Method)
    }
    if m.Requests[1].Method != http.MethodPatch {
        t.Errorf("second call = %q, want PATCH", m.Requests[1].Method)
    }
    if m.Requests[2].Method != http.MethodPatch {
        t.Errorf("third call = %q, want PATCH", m.Requests[2].Method)
    }

    // First body should have exactly 50 annotations.
    var create CheckRunCreate
    _ = json.Unmarshal(m.Requests[0].Body, &create)
    if got := len(create.Output.Annotations); got != 50 {
        t.Errorf("batch 1 size = %d, want 50", got)
    }
    // Last body should have the remaining 20.
    var update CheckRunUpdate
    _ = json.Unmarshal(m.Requests[2].Body, &update)
    if got := len(update.Output.Annotations); got != 20 {
        t.Errorf("batch 3 size = %d, want 20", got)
    }
}

func TestPublish_ResultsWithoutFileAreSkippedForAnnotations(t *testing.T) {
    m := newMockServer()
    defer m.Close()
    c := newTestClient(t, m.URL())

    results := []ir.TestResult{
        {Suite: "alpha", Name: "with_loc", Status: ir.StatusPassed, File: "a.go", Line: 1},
        {Suite: "alpha", Name: "no_loc", Status: ir.StatusPassed},
        {Suite: "alpha", Name: "also_no_loc", Status: ir.StatusFailed, Message: "boom"},
    }
    _, err := Publish(context.Background(), c, Config{
        HeadSHA: "sha", Options: Options{IncludePassed: true, IncludeSkipped: true},
    }, results)
    if err != nil {
        t.Fatalf("Publish: %v", err)
    }

    var create CheckRunCreate
    _ = json.Unmarshal(m.Requests[0].Body, &create)
    if got := len(create.Output.Annotations); got != 1 {
        t.Errorf("annotations = %d, want 1 (only located result annotated)", got)
    }
    // But the summary/text should still cover all three tests.
    if !strings.Contains(create.Output.Text, "no_loc") {
        t.Errorf("body should still mention no_loc, got: %q", create.Output.Text)
    }
}

func TestPublish_RetriesOn5xx(t *testing.T) {
    m := newMockServer()
    defer m.Close()

    // First call fails with 503, second succeeds.
    m.Responses = []mockResponse{
        {StatusCode: 503, Body: map[string]string{"message": "service unavailable"}},
        {StatusCode: 201, Body: CheckRunResponse{ID: 42, HTMLURL: "https://ok"}},
    }
    c := newTestClient(t, m.URL())

    resp, err := Publish(context.Background(), c, Config{
        HeadSHA: "sha", Options: DefaultOptions(),
    }, []ir.TestResult{{Suite: "x", Name: "y", Status: ir.StatusPassed}})
    if err != nil {
        t.Fatalf("Publish: %v", err)
    }
    if resp.ID != 42 {
        t.Errorf("resp.ID = %d, want 42", resp.ID)
    }
    if got := len(m.Requests); got != 2 {
        t.Errorf("HTTP calls = %d, want 2", got)
    }
}

func TestPublish_GivesUpAfterMaxRetries(t *testing.T) {
    m := newMockServer()
    defer m.Close()
    // Four 429s in a row — client's MaxRetries=3 means 4 attempts.
    for i := 0; i < 4; i++ {
        m.Responses = append(m.Responses, mockResponse{
            StatusCode: 429,
            Body:       map[string]string{"message": "slow down"},
            Headers:    map[string]string{"Retry-After": "0"},
        })
    }
    c := newTestClient(t, m.URL())

    _, err := Publish(context.Background(), c, Config{
        HeadSHA: "sha", Options: DefaultOptions(),
    }, []ir.TestResult{{Suite: "x", Name: "y", Status: ir.StatusPassed}})
    if err == nil {
        t.Fatal("expected error after exhausted retries")
    }
    if got := len(m.Requests); got != 4 {
        t.Errorf("HTTP calls = %d, want 4 (initial + 3 retries)", got)
    }
}

func TestPublish_HeaderAuthAndVersion(t *testing.T) {
    m := newMockServer()
    defer m.Close()
    c := newTestClient(t, m.URL())

    _, err := Publish(context.Background(), c, Config{
        HeadSHA: "sha", Options: DefaultOptions(),
    }, []ir.TestResult{{Suite: "x", Name: "y", Status: ir.StatusPassed}})
    if err != nil {
        t.Fatalf("Publish: %v", err)
    }

    hdr := m.Requests[0].Header
    if got := hdr.Get("Authorization"); got != "Bearer fake-token" {
        t.Errorf("Authorization = %q", got)
    }
    if got := hdr.Get("X-Github-Api-Version"); got != "2022-11-28" {
        t.Errorf("X-GitHub-Api-Version = %q", got)
    }
    if !strings.Contains(hdr.Get("Accept"), "vnd.github+json") {
        t.Errorf("Accept = %q", hdr.Get("Accept"))
    }
}

// DefaultOptions is failure-only: passing and skipped tests are covered
// by the check-run's summary table, so the diff-level annotations stay
// as a scarce resource for things that actually need attention. Callers
// opt back in via IncludePassed / IncludeSkipped.
func TestDefaultOptions_IsFailureOnly(t *testing.T) {
    // Arrange / Act
    opts := DefaultOptions()

    // Assert
    if opts.IncludePassed {
        t.Errorf("DefaultOptions().IncludePassed = true, want false")
    }
    if opts.IncludeSkipped {
        t.Errorf("DefaultOptions().IncludeSkipped = true, want false")
    }
}

func TestAnnotationsFor_DefaultsAnnotateFailuresOnly(t *testing.T) {
    // Arrange
    results := []ir.TestResult{
        {Suite: "s", Name: "p", Status: ir.StatusPassed, File: "a.go", Line: 1},
        {Suite: "s", Name: "s", Status: ir.StatusSkipped, File: "a.go", Line: 2},
        {Suite: "s", Name: "f", Status: ir.StatusFailed, File: "a.go", Line: 3, Message: "boom"},
        {Suite: "s", Name: "e", Status: ir.StatusError, File: "a.go", Line: 4, Message: "kapow"},
    }

    // Act
    got := AnnotationsFor(results, DefaultOptions())

    // Assert
    if len(got) != 2 {
        t.Fatalf("annotations = %d, want 2 (only failed + errored)", len(got))
    }
    for _, a := range got {
        if a.AnnotationLevel != LevelFailure {
            t.Errorf("annotation level = %q, want failure", a.AnnotationLevel)
        }
    }
}

func TestConclusion(t *testing.T) {
    cases := []struct {
        name string
        t    Totals
        want string
    }{
        {"all pass", Totals{Passed: 3, Total: 3}, ConclusionSuccess},
        {"one fail", Totals{Passed: 2, Failed: 1, Total: 3}, ConclusionFailure},
        {"only error", Totals{Errored: 1, Total: 1}, ConclusionFailure},
        {"only skipped", Totals{Skipped: 5, Total: 5}, ConclusionSkipped},
        {"pass and skip", Totals{Passed: 2, Skipped: 3, Total: 5}, ConclusionSuccess},
        {"empty", Totals{}, ConclusionSuccess},
    }
    for _, tc := range cases {
        if got := Conclusion(tc.t); got != tc.want {
            t.Errorf("%s: Conclusion = %q, want %q", tc.name, got, tc.want)
        }
    }
}

func TestChunk(t *testing.T) {
    a := make([]Annotation, 7)
    got := Chunk(a, 3)
    if len(got) != 3 {
        t.Fatalf("chunks = %d, want 3", len(got))
    }
    if len(got[0]) != 3 || len(got[1]) != 3 || len(got[2]) != 1 {
        t.Errorf("chunk sizes = %d,%d,%d, want 3,3,1", len(got[0]), len(got[1]), len(got[2]))
    }
    if Chunk(nil, 3) != nil {
        t.Errorf("Chunk(nil) should be nil")
    }
}

func TestCompute_AllStatusBranches(t *testing.T) {
    // Arrange
    results := []ir.TestResult{
        {Status: ir.StatusPassed},
        {Status: ir.StatusFailed},
        {Status: ir.StatusSkipped},
        {Status: ir.StatusError},
    }

    // Act
    got := Compute(results)

    // Assert
    want := Totals{Passed: 1, Failed: 1, Skipped: 1, Errored: 1, Total: 4}
    if got != want {
        t.Errorf("Compute = %+v, want %+v", got, want)
    }
}

func TestTitle_AllBranches(t *testing.T) {
    // Arrange
    cases := []struct {
        name string
        t    Totals
        want string
    }{
        {"success plural", Totals{Passed: 5, Total: 5}, "5 tests passed"},
        {"success singular", Totals{Passed: 1, Total: 1}, "1 test passed"},
        {"failure only failed", Totals{Passed: 2, Failed: 2, Total: 4}, "2 failed of 4 tests"},
        {"failure split", Totals{Passed: 2, Failed: 1, Errored: 1, Total: 4}, "1 failed, 1 errored of 4 tests"},
        {"failure singular total", Totals{Failed: 1, Total: 1}, "1 failed of 1 test"},
        {"skipped plural", Totals{Skipped: 3, Total: 3}, "all 3 tests skipped"},
        {"skipped singular", Totals{Skipped: 1, Total: 1}, "all 1 test skipped"},
    }

    // Act / Assert
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if got := Title(tc.t); got != tc.want {
                t.Errorf("Title = %q, want %q", got, tc.want)
            }
        })
    }
}

func TestSummaryMarkdown_UnspecifiedSuiteAndAllStatuses(t *testing.T) {
    // Arrange — empty Suite hits the "(unspecified)" fallback; four
    // statuses hit all four counter branches inside SummaryMarkdown.
    results := []ir.TestResult{
        {Status: ir.StatusPassed},
        {Status: ir.StatusFailed},
        {Status: ir.StatusSkipped},
        {Status: ir.StatusError},
    }

    // Act
    md := SummaryMarkdown(results)

    // Assert
    if !strings.Contains(md, "(unspecified)") {
        t.Errorf("missing fallback suite name: %q", md)
    }
    // Totals row should show 1 of each.
    if !strings.Contains(md, "| **Total** | **1** | **1** | **1** | **1** | **4** |") {
        t.Errorf("wrong totals row: %q", md)
    }
}

func TestBodyMarkdown_UnspecifiedSuite(t *testing.T) {
    // Arrange
    results := []ir.TestResult{
        {Name: "orphan", Status: ir.StatusPassed},
    }

    // Act
    md := BodyMarkdown(results)

    // Assert
    if !strings.Contains(md, "(unspecified)") {
        t.Errorf("missing fallback suite name: %q", md)
    }
}

func TestBodyMarkdown_AllPassSuiteCollapsesToOneLine(t *testing.T) {
    // Arrange — 21 all-passing tests. Instead of 21 bullets of noise,
    // the render should compress to a single "all N tests passed" line
    // because none of the entries deserve individual attention.
    results := make([]ir.TestResult, 21)
    for i := range results {
        results[i] = ir.TestResult{Suite: "big", Name: fmt.Sprintf("t%d", i), Status: ir.StatusPassed}
    }

    // Act
    md := BodyMarkdown(results)

    // Assert
    if !strings.Contains(md, "all 21 tests passed") {
        t.Errorf("all-pass suite should collapse to summary line: %q", md)
    }
    // No individual bullets should leak through.
    if strings.Contains(md, "`t0`") {
        t.Errorf("individual test bullets must be hidden in all-pass suite: %q", md)
    }
}

func TestBodyMarkdown_MixedSuiteShowsFailuresFirstAndCollapsesPasses(t *testing.T) {
    // Arrange — one failure and two passes. Failure must appear before
    // any pass, and passes must be hidden inside a <details> block so
    // the eye lands on the failing test.
    results := []ir.TestResult{
        {Suite: "mix", Name: "PassA", Status: ir.StatusPassed},
        {Suite: "mix", Name: "FailX", Status: ir.StatusFailed, Message: "boom"},
        {Suite: "mix", Name: "PassB", Status: ir.StatusPassed},
    }

    // Act
    md := BodyMarkdown(results)

    // Assert — FailX must render before PassA/PassB.
    failIdx := strings.Index(md, "`FailX`")
    passAIdx := strings.Index(md, "`PassA`")
    if failIdx < 0 || passAIdx < 0 {
        t.Fatalf("both tests must render: %q", md)
    }
    if failIdx > passAIdx {
        t.Errorf("failure must render before passes: %q", md)
    }
    if !strings.Contains(md, "<details><summary>2 passed tests</summary>") {
        t.Errorf("passes must be collapsed with count-bearing label: %q", md)
    }
}

func TestBodyMarkdown_MixedSuiteOrdersFailedBeforeErroredBeforeSkipped(t *testing.T) {
    // Arrange
    results := []ir.TestResult{
        {Suite: "s", Name: "Sk", Status: ir.StatusSkipped},
        {Suite: "s", Name: "Er", Status: ir.StatusError},
        {Suite: "s", Name: "Fa", Status: ir.StatusFailed},
    }

    // Act
    md := BodyMarkdown(results)

    // Assert
    fa := strings.Index(md, "`Fa`")
    er := strings.Index(md, "`Er`")
    sk := strings.Index(md, "`Sk`")
    if fa >= er || er >= sk {
        t.Errorf("expected failed < errored < skipped ordering, got fa=%d er=%d sk=%d in %q",
            fa, er, sk, md)
    }
}

func TestSummaryMarkdown_TrimsCommonModulePrefix(t *testing.T) {
    // Arrange — three suites sharing a long common prefix. The rendered
    // table should strip that prefix so the Suite column stays readable.
    results := []ir.TestResult{
        {Suite: "github.com/o/r/cmd/tool", Status: ir.StatusPassed},
        {Suite: "github.com/o/r/internal/a", Status: ir.StatusPassed},
        {Suite: "github.com/o/r/internal/b", Status: ir.StatusPassed},
    }

    // Act
    md := SummaryMarkdown(results)

    // Assert
    if strings.Contains(md, "github.com/o/r/") {
        t.Errorf("common prefix must be stripped: %q", md)
    }
    if !strings.Contains(md, "| cmd/tool |") ||
        !strings.Contains(md, "| internal/a |") ||
        !strings.Contains(md, "| internal/b |") {
        t.Errorf("trimmed suite names missing: %q", md)
    }
}

func TestBodyMarkdown_TrimsCommonModulePrefix(t *testing.T) {
    // Arrange
    results := []ir.TestResult{
        {Suite: "github.com/o/r/cmd/tool", Name: "T1", Status: ir.StatusPassed},
        {Suite: "github.com/o/r/internal/a", Name: "T2", Status: ir.StatusPassed},
    }

    // Act
    md := BodyMarkdown(results)

    // Assert
    if strings.Contains(md, "github.com/o/r/") {
        t.Errorf("common prefix must be stripped from headings: %q", md)
    }
    if !strings.Contains(md, "### cmd/tool ") || !strings.Contains(md, "### internal/a ") {
        t.Errorf("trimmed suite headings missing: %q", md)
    }
}

func TestBodyMarkdown_NestsSubtestsUnderParent(t *testing.T) {
    // Arrange — a mixed suite so the passed block (which contains the
    // subtests) actually renders. All-pass suites collapse and never
    // exercise the nesting path.
    results := []ir.TestResult{
        {Suite: "s", Name: "TestFail", Status: ir.StatusFailed, Message: "x"},
        {Suite: "s", Name: "TestFoo", Status: ir.StatusPassed},
        {Suite: "s", Name: "TestFoo/one", Status: ir.StatusPassed},
        {Suite: "s", Name: "TestFoo/two", Status: ir.StatusPassed},
    }

    // Act
    md := BodyMarkdown(results)

    // Assert — subtests must be indented two spaces beneath their parent bullet.
    if !strings.Contains(md, "  - ✅ `TestFoo/one`") {
        t.Errorf("subtest should be nested under its parent: %q", md)
    }
}

func TestTitle_FailedAndErroredReportedSeparately(t *testing.T) {
    // Arrange
    got := Title(Totals{Passed: 5, Failed: 2, Errored: 3, Total: 10})

    // Assert
    want := "2 failed, 3 errored of 10 tests"
    if got != want {
        t.Errorf("Title = %q, want %q", got, want)
    }
}

func TestTitle_OnlyErrored(t *testing.T) {
    // Arrange
    got := Title(Totals{Errored: 1, Total: 3, Passed: 2})

    // Assert
    if got != "1 errored of 3 tests" {
        t.Errorf("Title = %q, want %q", got, "1 errored of 3 tests")
    }
}

func TestSummaryMarkdown_GlanceBarLeadsSummary(t *testing.T) {
    // Arrange
    results := []ir.TestResult{
        {Suite: "a", Status: ir.StatusPassed},
        {Suite: "a", Status: ir.StatusPassed},
        {Suite: "a", Status: ir.StatusFailed},
    }

    // Act
    md := SummaryMarkdown(results)

    // Assert — glance bar must appear before the table and count both.
    if !strings.Contains(md, "✅ 2") || !strings.Contains(md, "❌ 1") {
        t.Errorf("glance bar must include nonzero counts: %q", md)
    }
    glanceIdx := strings.Index(md, "✅ 2")
    tableIdx := strings.Index(md, "| Suite |")
    if glanceIdx < 0 || tableIdx < 0 || glanceIdx > tableIdx {
        t.Errorf("glance bar must precede the table: %q", md)
    }
}

func TestBodyMarkdown_FailedTestShowsFileLine(t *testing.T) {
    // Arrange
    results := []ir.TestResult{
        {Suite: "s", Name: "TestBad", Status: ir.StatusFailed, File: "internal/x/foo.go", Line: 42, Message: "boom"},
    }

    // Act
    md := BodyMarkdown(results)

    // Assert — location must appear as a suffix so readers can jump.
    if !strings.Contains(md, "internal/x/foo.go:42") {
        t.Errorf("failure line must expose File:Line: %q", md)
    }
}

func TestBodyMarkdown_TruncatesMultilineMessage(t *testing.T) {
    // Arrange — a stack-trace style message must not bleed the whole
    // trace into a single bullet.
    results := []ir.TestResult{
        {Suite: "s", Name: "TestX", Status: ir.StatusFailed, Message: "expected 1 got 2\ntraceback line 1\ntraceback line 2"},
    }

    // Act
    md := BodyMarkdown(results)

    // Assert
    if !strings.Contains(md, "expected 1 got 2") {
        t.Errorf("first line of message must be visible: %q", md)
    }
    if strings.Contains(md, "traceback line 1") {
        t.Errorf("subsequent lines must be truncated from the bullet: %q", md)
    }
}

func TestStatusIcon_AllArms(t *testing.T) {
    // Arrange
    cases := []struct {
        s    ir.Status
        want string
    }{
        {ir.StatusPassed, "✅"},
        {ir.StatusFailed, "❌"},
        {ir.StatusSkipped, "⏭️"},
        {ir.StatusError, "⚠️"},
        {ir.Status(99), "❓"}, // unknown → default arm
    }

    // Act / Assert
    for _, tc := range cases {
        if got := statusIcon(tc.s); got != tc.want {
            t.Errorf("statusIcon(%v) = %q, want %q", tc.s, got, tc.want)
        }
    }
}

func TestAnnotationsFor_EmptyFileSkipped(t *testing.T) {
    // Arrange — even with IncludePassed on, a result with empty File is
    // skipped (Checks API rejects annotations without a path).
    results := []ir.TestResult{
        {Name: "no-file", Status: ir.StatusFailed, Message: "boom"},
    }

    // Act
    got := AnnotationsFor(results, Options{IncludePassed: true, IncludeSkipped: true})

    // Assert
    if len(got) != 0 {
        t.Errorf("annotations = %d, want 0 (empty File must be skipped)", len(got))
    }
}

func TestAnnotationsFor_PassedGetsPassedMessage(t *testing.T) {
    // Arrange
    results := []ir.TestResult{
        {Name: "p", Status: ir.StatusPassed, File: "a.go", Line: 1},
    }

    // Act
    got := AnnotationsFor(results, Options{IncludePassed: true})

    // Assert
    if len(got) != 1 {
        t.Fatalf("annotations = %d, want 1", len(got))
    }
    if got[0].Message != "passed" {
        t.Errorf("Message = %q, want %q", got[0].Message, "passed")
    }
    if got[0].AnnotationLevel != LevelNotice {
        t.Errorf("Level = %q, want notice", got[0].AnnotationLevel)
    }
}

func TestAnnotationsFor_FallbackMessageUsedForEmptyOriginal(t *testing.T) {
    // Arrange — Message is empty on the source result; annotationFor
    // should fall back to the status-word default.
    results := []ir.TestResult{
        {Name: "f", Status: ir.StatusFailed, File: "a.go", Line: 3},   // fallback "failed"
        {Name: "s", Status: ir.StatusSkipped, File: "a.go", Line: 4},  // fallback "skipped"
    }

    // Act
    got := AnnotationsFor(results, Options{IncludeSkipped: true})

    // Assert
    if len(got) != 2 {
        t.Fatalf("annotations = %d, want 2", len(got))
    }
    byName := map[string]Annotation{got[0].Title: got[0], got[1].Title: got[1]}
    if byName["f"].Message != "failed" {
        t.Errorf("failed Message = %q, want fallback %q", byName["f"].Message, "failed")
    }
    if byName["s"].Message != "skipped" {
        t.Errorf("skipped Message = %q, want fallback %q", byName["s"].Message, "skipped")
    }
}

func TestAnnotationFor_LineZeroDefaultsToOne(t *testing.T) {
    // Arrange — Line=0 should be promoted to 1 so the Checks API accepts it.
    got := annotationFor(ir.TestResult{Name: "n", File: "a.go", Line: 0, Status: ir.StatusFailed})

    // Assert
    if got.StartLine != 1 || got.EndLine != 1 {
        t.Errorf("Line=0 should default to 1, got (%d, %d)", got.StartLine, got.EndLine)
    }
}

func TestChunk_ZeroSizeUsesMax(t *testing.T) {
    // Arrange — n <= 0 hits the guard that substitutes MaxAnnotationsPerRequest.
    a := make([]Annotation, 3)

    // Act
    got := Chunk(a, 0)

    // Assert
    if len(got) != 1 || len(got[0]) != 3 {
        t.Errorf("Chunk with n=0 should produce one batch of 3, got %v", got)
    }
}

func TestNewClient_Defaults(t *testing.T) {
    // Arrange / Act
    c := NewClient("token", "owner", "repo")

    // Assert
    if c.Token != "token" || c.Owner != "owner" || c.Repo != "repo" {
        t.Errorf("identity fields = %q/%q/%q", c.Token, c.Owner, c.Repo)
    }
    if c.BaseURL != "https://api.github.com" {
        t.Errorf("BaseURL = %q, want production default", c.BaseURL)
    }
    if c.MaxRetries != 3 {
        t.Errorf("MaxRetries = %d, want 3", c.MaxRetries)
    }
    if c.HTTPClient == nil {
        t.Errorf("HTTPClient must be set")
    }
    if c.Now == nil {
        t.Errorf("Now must be set")
    }
    if c.Sleep == nil {
        t.Errorf("Sleep must be set")
    }
}

func TestShouldRetryTransport(t *testing.T) {
    // Arrange / Act / Assert — pure predicate, two inputs.
    if shouldRetryTransport(nil) {
        t.Errorf("nil error must not be retryable")
    }
    if !shouldRetryTransport(fmt.Errorf("dial timeout")) {
        t.Errorf("non-nil error must be retryable")
    }
}

func TestParseRetryAfter(t *testing.T) {
    // Arrange
    cases := []struct {
        name string
        in   string
        want time.Duration
    }{
        {"empty", "", 0},
        {"seconds", "5", 5 * time.Second},
        {"zero", "0", 0},
        {"negative", "-3", 0},
        {"non-numeric", "later", 0},
    }

    // Act / Assert
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if got := parseRetryAfter(tc.in); got != tc.want {
                t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
            }
        })
    }
}

func TestBackoffDuration_CapsAt10s(t *testing.T) {
    // Arrange / Act — attempt=6 → 500ms << 5 = 16s, should cap at 10s.
    got := backoffDuration(6)

    // Assert
    if got != 10*time.Second {
        t.Errorf("backoffDuration(6) = %v, want 10s cap", got)
    }
}

func TestPublish_HeadSHARequired(t *testing.T) {
    m := newMockServer()
    defer m.Close()
    c := newTestClient(t, m.URL())

    _, err := Publish(context.Background(), c, Config{HeadSHA: ""}, nil)
    if err == nil {
        t.Fatal("expected error for empty HeadSHA")
    }
    if !strings.Contains(err.Error(), "HeadSHA is required") {
        t.Errorf("unexpected error: %v", err)
    }
}

func TestPublish_DefaultsCheckNameWhenEmpty(t *testing.T) {
    m := newMockServer()
    defer m.Close()
    c := newTestClient(t, m.URL())

    _, err := Publish(context.Background(), c, Config{HeadSHA: "sha"}, nil)
    if err != nil {
        t.Fatalf("Publish: %v", err)
    }
    var create CheckRunCreate
    if err := json.Unmarshal(m.Requests[0].Body, &create); err != nil {
        t.Fatalf("decode: %v", err)
    }
    if create.Name != "Test Results" {
        t.Errorf("Name = %q, want default", create.Name)
    }
}

func TestPublish_UpdateCheckRun_ErrorPropagates(t *testing.T) {
    // Arrange — CREATE succeeds, first PATCH returns 400 non-retryable.
    m := newMockServer()
    defer m.Close()
    m.Responses = []mockResponse{
        {StatusCode: 201, Body: CheckRunResponse{ID: 7, HTMLURL: "https://x"}},
        {StatusCode: 400, Body: map[string]string{"message": "bad update"}},
    }
    c := newTestClient(t, m.URL())

    // 60 results — one CREATE (50) + one PATCH (10).
    results := make([]ir.TestResult, 60)
    for i := range results {
        results[i] = ir.TestResult{Suite: "x", Name: "n", Status: ir.StatusPassed, File: "a.go", Line: i + 1}
    }

    // Act
    _, err := Publish(context.Background(), c, Config{
        HeadSHA: "sha", Options: Options{IncludePassed: true},
    }, results)

    // Assert
    if err == nil {
        t.Fatal("expected error from failing PATCH")
    }
    if !strings.Contains(err.Error(), "update check-run batch 2") {
        t.Errorf("unexpected error: %v", err)
    }
}

func TestClient_Do_Non2xxReturnsAPIError(t *testing.T) {
    // Arrange — 404 is not retryable, so we should get an APIError back
    // rather than the exhausted-retries error.
    m := newMockServer()
    defer m.Close()
    m.Responses = []mockResponse{
        {StatusCode: 404, Body: map[string]string{"message": "gone"}},
    }
    c := newTestClient(t, m.URL())

    // Act
    _, err := Publish(context.Background(), c, Config{HeadSHA: "sha"}, nil)

    // Assert
    if err == nil {
        t.Fatal("expected APIError")
    }
    var apiErr *APIError
    if !errors.As(err, &apiErr) {
        t.Errorf("expected APIError, got %T: %v", err, err)
    }
    if apiErr.StatusCode != 404 {
        t.Errorf("StatusCode = %d, want 404", apiErr.StatusCode)
    }
    if !strings.Contains(apiErr.Error(), "404") {
        t.Errorf("Error() should include status: %q", apiErr.Error())
    }
}

func TestClient_Do_Non2xxBodyDecodeError(t *testing.T) {
    // Arrange — 200 with invalid JSON body forces the response decode
    // error path.
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("not json"))
    }))
    defer srv.Close()

    c := &Client{
        HTTPClient: &http.Client{Timeout: 1 * time.Second},
        BaseURL:    srv.URL,
        Token:      "t",
        Owner:      "o",
        Repo:       "r",
        MaxRetries: 0,
        Now:        time.Now,
        Sleep:      func(time.Duration) {},
    }

    // Act
    _, err := Publish(context.Background(), c, Config{HeadSHA: "sha"}, nil)

    // Assert
    if err == nil {
        t.Fatal("expected decode error")
    }
    if !strings.Contains(err.Error(), "decode response") {
        t.Errorf("expected 'decode response' in error, got: %v", err)
    }
}

func TestClient_Do_RetryAfterHeaderHonored(t *testing.T) {
    // Arrange — 429 with Retry-After: 1 exercises the Sleep(wait)
    // branch inside do(). Client.Sleep is stubbed so no real delay.
    var sleeps []time.Duration
    m := newMockServer()
    defer m.Close()
    m.Responses = []mockResponse{
        {StatusCode: 429, Body: map[string]string{"m": "slow"}, Headers: map[string]string{"Retry-After": "1"}},
        {StatusCode: 201, Body: CheckRunResponse{ID: 1, HTMLURL: "https://ok"}},
    }
    c := newTestClient(t, m.URL())
    c.Sleep = func(d time.Duration) { sleeps = append(sleeps, d) }

    // Act
    _, err := Publish(context.Background(), c, Config{HeadSHA: "sha"}, nil)
    if err != nil {
        t.Fatalf("Publish: %v", err)
    }

    // Assert — expect at least one Sleep of 1s (the Retry-After value).
    // There may also be a backoff Sleep between attempts.
    found := false
    for _, d := range sleeps {
        if d == time.Second {
            found = true
            break
        }
    }
    if !found {
        t.Errorf("expected a Sleep(1s) from Retry-After, got %v", sleeps)
    }
}

func TestClient_Do_TransportErrorRetriesThenGivesUp(t *testing.T) {
    // Arrange — the handler hijacks and immediately closes the
    // connection, forcing a transport-level error on the client. This
    // avoids the TOCTOU window of closing a listener and racing another
    // process for the same port.
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        hj, ok := w.(http.Hijacker)
        if !ok {
            t.Fatal("ResponseWriter does not support Hijack")
        }
        conn, _, err := hj.Hijack()
        if err != nil {
            t.Fatalf("Hijack: %v", err)
        }
        _ = conn.Close()
    }))
    defer srv.Close()

    c := &Client{
        HTTPClient: &http.Client{Timeout: 1 * time.Second},
        BaseURL:    srv.URL,
        Token:      "t",
        Owner:      "o",
        Repo:       "r",
        MaxRetries: 1,
        Now:        time.Now,
        Sleep:      func(time.Duration) {},
    }

    // Act
    _, err := Publish(context.Background(), c, Config{HeadSHA: "sha"}, nil)

    // Assert
    if err == nil {
        t.Fatal("expected transport error")
    }
    if !strings.Contains(err.Error(), "giving up after") {
        t.Errorf("expected exhausted-retries error, got: %v", err)
    }
}

func TestSummaryMarkdown_ContainsTotals(t *testing.T) {
    results := []ir.TestResult{
        {Suite: "a", Status: ir.StatusPassed},
        {Suite: "a", Status: ir.StatusPassed},
        {Suite: "b", Status: ir.StatusFailed},
    }
    md := SummaryMarkdown(results)
    if !strings.Contains(md, "**Total**") {
        t.Errorf("missing total row: %q", md)
    }
    if !strings.Contains(md, "| a |") || !strings.Contains(md, "| b |") {
        t.Errorf("missing suite rows: %q", md)
    }
}
