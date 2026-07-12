package publish

import (
    "context"
    "encoding/json"
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
