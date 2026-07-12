package main

import (
    "bytes"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/jedi-knights/publish-test-results/internal/ir"
    "github.com/jedi-knights/publish-test-results/internal/publish"
)

func TestRun_NoArgsPrintsUsage(t *testing.T) {
    var stdout, stderr bytes.Buffer
    code := run(nil, &stdout, &stderr, mapEnv{})
    if code != exitUsage {
        t.Errorf("code = %d, want %d", code, exitUsage)
    }
    if !strings.Contains(stderr.String(), "usage:") {
        t.Errorf("stderr missing usage: %q", stderr.String())
    }
}

func TestRun_Version(t *testing.T) {
    var stdout, stderr bytes.Buffer
    code := run([]string{"--version"}, &stdout, &stderr, mapEnv{})
    if code != exitOK {
        t.Fatalf("code = %d, want %d", code, exitOK)
    }
    if strings.TrimSpace(stdout.String()) != version {
        t.Errorf("stdout = %q, want %q", stdout.String(), version)
    }
}

func TestRun_DryRunOnJUnitFixture(t *testing.T) {
    fixture := filepath.Join("..", "..", "testdata", "junit-surefire", "passing.xml")

    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=" + fixture, "--dry-run"}, &stdout, &stderr, mapEnv{})
    if code != exitOK {
        t.Fatalf("code = %d, stderr = %q", code, stderr.String())
    }
    out := stdout.String()
    if !strings.Contains(out, "Parsed 3 test result(s) via junit×3") {
        t.Errorf("summary line missing/wrong: %q", out)
    }
    if !strings.Contains(out, "passed:  3") {
        t.Errorf("passed count missing: %q", out)
    }
}

func TestRun_DryRunOnRealCtestprobeXML(t *testing.T) {
    fixture := filepath.Join("..", "..", "testdata", "junit-surefire", "real-ctestprobe-rle.xml")

    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=" + fixture, "--dry-run"}, &stdout, &stderr, mapEnv{})
    if code != exitOK {
        t.Fatalf("code = %d, stderr = %q", code, stderr.String())
    }
    if !strings.Contains(stdout.String(), "Parsed 12 test result(s) via junit×12") {
        t.Errorf("summary line missing/wrong: %q", stdout.String())
    }
}

func TestRun_NoMatchIsUsageError(t *testing.T) {
    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=nonexistent-*.xml", "--dry-run"}, &stdout, &stderr, mapEnv{})
    if code != exitUsage {
        t.Errorf("code = %d, want %d", code, exitUsage)
    }
    if !strings.Contains(stderr.String(), "no files matched") {
        t.Errorf("stderr missing 'no files matched': %q", stderr.String())
    }
}

// TestRun_RealPublishMissingCreds walks the three env-var guards.
// Each one should surface a specific error message and exit non-zero.
func TestRun_RealPublishMissingCreds(t *testing.T) {
    fixture := filepath.Join("..", "..", "testdata", "junit-surefire", "passing.xml")

    cases := []struct {
        name string
        envs mapEnv
        want string
    }{
        {"missing token", mapEnv{"GITHUB_SHA": "sha", "GITHUB_REPOSITORY": "o/r"}, "GITHUB_TOKEN not set"},
        {"missing sha", mapEnv{"GITHUB_TOKEN": "t", "GITHUB_REPOSITORY": "o/r"}, "GITHUB_SHA not set"},
        {"missing repo", mapEnv{"GITHUB_TOKEN": "t", "GITHUB_SHA": "sha"}, "GITHUB_REPOSITORY not set"},
        {"malformed repo", mapEnv{"GITHUB_TOKEN": "t", "GITHUB_SHA": "sha", "GITHUB_REPOSITORY": "no-slash"}, "malformed repository slug"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            var stdout, stderr bytes.Buffer
            code := run([]string{"--files=" + fixture}, &stdout, &stderr, tc.envs)
            if code != exitUsage {
                t.Errorf("code = %d, want %d", code, exitUsage)
            }
            if !strings.Contains(stderr.String(), tc.want) {
                t.Errorf("stderr should contain %q, got %q", tc.want, stderr.String())
            }
        })
    }
}

// TestRun_RealPublishSuccess exercises the full flow against a mock
// GitHub API served by httptest, wired in via --api-url.
func TestRun_RealPublishSuccess(t *testing.T) {
    var (
        gotMethod string
        gotPath   string
        gotBody   []byte
    )
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotMethod = r.Method
        gotPath = r.URL.Path
        gotBody, _ = io.ReadAll(r.Body)
        w.WriteHeader(http.StatusCreated)
        _ = json.NewEncoder(w).Encode(map[string]any{
            "id":       int64(42),
            "name":     "Test Results",
            "html_url": "https://github.com/o/r/check-runs/42",
        })
    }))
    defer srv.Close()

    fixture := filepath.Join("..", "..", "testdata", "junit-surefire", "with-location-body.xml")
    envs := mapEnv{
        "GITHUB_TOKEN":      "fake-token",
        "GITHUB_SHA":        "deadbeef",
        "GITHUB_REPOSITORY": "o/r",
        "GITHUB_API_URL":    srv.URL,
    }

    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=" + fixture}, &stdout, &stderr, envs)
    if code != exitOK {
        t.Fatalf("code = %d, stderr = %q", code, stderr.String())
    }

    if gotMethod != http.MethodPost {
        t.Errorf("method = %q, want POST", gotMethod)
    }
    if gotPath != "/repos/o/r/check-runs" {
        t.Errorf("path = %q", gotPath)
    }
    if !strings.Contains(string(gotBody), `"head_sha":"deadbeef"`) {
        t.Errorf("body missing head_sha: %s", gotBody)
    }
    if !strings.Contains(stdout.String(), "check-run: https://github.com/o/r/check-runs/42") {
        t.Errorf("stdout missing published URL: %q", stdout.String())
    }
}

// TestRun_DefaultsAnnotateFailuresOnly locks in the failure-only
// default. Fixture has one passing + one failing test, both with
// file:line — the outbound HTTP body should carry exactly one
// annotation (the failure).
func TestRun_DefaultsAnnotateFailuresOnly(t *testing.T) {
    // Arrange
    var gotBody []byte
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotBody, _ = io.ReadAll(r.Body)
        w.WriteHeader(http.StatusCreated)
        _ = json.NewEncoder(w).Encode(map[string]any{"id": int64(1), "html_url": "https://x"})
    }))
    defer srv.Close()

    fixture := filepath.Join("..", "..", "testdata", "junit-surefire", "with-location-attr.xml")
    envs := mapEnv{
        "GITHUB_TOKEN":      "t",
        "GITHUB_SHA":        "sha",
        "GITHUB_REPOSITORY": "o/r",
        "GITHUB_API_URL":    srv.URL,
    }

    // Act
    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=" + fixture}, &stdout, &stderr, envs)

    // Assert
    if code != exitOK {
        t.Fatalf("code = %d, stderr = %q", code, stderr.String())
    }
    n := strings.Count(string(gotBody), `"annotation_level"`)
    if n != 1 {
        t.Errorf("annotations = %d, want 1 (failure only); body = %s", n, gotBody)
    }
}

// TestRun_IncludePassedFlagOptsIn verifies --include-passed re-enables
// notice pins for the passing test in the same fixture.
func TestRun_IncludePassedFlagOptsIn(t *testing.T) {
    // Arrange
    var gotBody []byte
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotBody, _ = io.ReadAll(r.Body)
        w.WriteHeader(http.StatusCreated)
        _ = json.NewEncoder(w).Encode(map[string]any{"id": int64(1), "html_url": "https://x"})
    }))
    defer srv.Close()

    fixture := filepath.Join("..", "..", "testdata", "junit-surefire", "with-location-attr.xml")
    envs := mapEnv{
        "GITHUB_TOKEN":      "t",
        "GITHUB_SHA":        "sha",
        "GITHUB_REPOSITORY": "o/r",
        "GITHUB_API_URL":    srv.URL,
    }

    // Act
    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=" + fixture, "--include-passed"}, &stdout, &stderr, envs)

    // Assert
    if code != exitOK {
        t.Fatalf("code = %d, stderr = %q", code, stderr.String())
    }
    n := strings.Count(string(gotBody), `"annotation_level"`)
    if n != 2 {
        t.Errorf("annotations = %d, want 2 (pass + fail); body = %s", n, gotBody)
    }
}

func TestExpandGlobs(t *testing.T) {
    matches, err := expandGlobs("../../testdata/junit-surefire/*.xml")
    if err != nil {
        t.Fatalf("expandGlobs: %v", err)
    }
    // Six known fixtures in that directory as of this commit; assert a
    // lower bound so future additions don't break the test.
    if len(matches) < 5 {
        t.Errorf("only %d matches; expected at least 5", len(matches))
    }
}

func TestExpandGlobs_InvalidPattern(t *testing.T) {
    // Malformed glob (unmatched bracket) — filepath.Glob returns an error.
    if _, err := expandGlobs("["); err == nil {
        t.Fatal("expected error on malformed pattern")
    }
}

// writeSummaryTo creates an empty file at a temp path and returns it,
// suitable for GITHUB_STEP_SUMMARY.
func writeSummaryTo(t *testing.T) string {
    t.Helper()
    path := filepath.Join(t.TempDir(), "summary.md")
    if err := os.WriteFile(path, nil, 0o644); err != nil {
        t.Fatalf("prep summary file: %v", err)
    }
    return path
}

func TestWriteStepSummary_WritesSummaryAndLink(t *testing.T) {
    // Arrange
    path := writeSummaryTo(t)
    results := []ir.TestResult{
        {Suite: "s", Status: ir.StatusPassed},
    }
    resp := &publish.CheckRunResponse{HTMLURL: "https://gh/runs/1"}

    // Act
    if err := writeStepSummary(path, results, resp); err != nil {
        t.Fatalf("writeStepSummary: %v", err)
    }

    // Assert
    body, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("read summary: %v", err)
    }
    got := string(body)
    if !strings.Contains(got, "## Test Results") {
        t.Errorf("missing header: %q", got)
    }
    if !strings.Contains(got, "https://gh/runs/1") {
        t.Errorf("missing check-run link: %q", got)
    }
}

func TestWriteStepSummary_NilResponseOmitsLink(t *testing.T) {
    path := writeSummaryTo(t)
    if err := writeStepSummary(path, nil, nil); err != nil {
        t.Fatalf("writeStepSummary: %v", err)
    }
    body, _ := os.ReadFile(path)
    if strings.Contains(string(body), "[Open the full check-run") {
        t.Errorf("nil response should not emit a link, got: %q", string(body))
    }
}

func TestWriteStepSummary_OpenFileFails(t *testing.T) {
    // Writing to a directory path fails at OpenFile.
    if err := writeStepSummary(t.TempDir(), nil, nil); err == nil {
        t.Fatal("expected error when path is a directory")
    }
}

func TestRun_WritesStepSummaryWhenEnvSet(t *testing.T) {
    // Arrange
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.WriteHeader(http.StatusCreated)
        _ = json.NewEncoder(w).Encode(map[string]any{"id": int64(1), "html_url": "https://ok"})
    }))
    defer srv.Close()

    summaryPath := writeSummaryTo(t)
    fixture := filepath.Join("..", "..", "testdata", "junit-surefire", "with-location-attr.xml")
    envs := mapEnv{
        "GITHUB_TOKEN":        "t",
        "GITHUB_SHA":          "sha",
        "GITHUB_REPOSITORY":   "o/r",
        "GITHUB_API_URL":      srv.URL,
        "GITHUB_STEP_SUMMARY": summaryPath,
    }

    // Act
    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=" + fixture}, &stdout, &stderr, envs)

    // Assert
    if code != exitOK {
        t.Fatalf("code = %d, stderr = %q", code, stderr.String())
    }
    body, _ := os.ReadFile(summaryPath)
    if !strings.Contains(string(body), "## Test Results") {
        t.Errorf("summary file not populated: %q", string(body))
    }
}

func TestRun_PublishAPIFailureIsUsageError(t *testing.T) {
    // Arrange — 400 is not retryable; publish surfaces the APIError.
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.WriteHeader(http.StatusBadRequest)
        _ = json.NewEncoder(w).Encode(map[string]string{"message": "bad request"})
    }))
    defer srv.Close()

    fixture := filepath.Join("..", "..", "testdata", "junit-surefire", "with-location-attr.xml")
    envs := mapEnv{
        "GITHUB_TOKEN":      "t",
        "GITHUB_SHA":        "sha",
        "GITHUB_REPOSITORY": "o/r",
        "GITHUB_API_URL":    srv.URL,
    }

    // Act
    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=" + fixture}, &stdout, &stderr, envs)

    // Assert
    if code != exitUsage {
        t.Errorf("code = %d, want exitUsage", code)
    }
    if !strings.Contains(stderr.String(), "publish failed") {
        t.Errorf("stderr missing 'publish failed': %q", stderr.String())
    }
}

func TestRun_ParseErrorIsUsageError(t *testing.T) {
    // Arrange — a file no registered parser recognizes.
    tmp := filepath.Join(t.TempDir(), "junk.xml")
    if err := os.WriteFile(tmp, []byte("plain text, no known format"), 0o644); err != nil {
        t.Fatalf("prep fixture: %v", err)
    }

    // Act
    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=" + tmp, "--dry-run"}, &stdout, &stderr, mapEnv{})

    // Assert
    if code != exitUsage {
        t.Errorf("code = %d, want exitUsage", code)
    }
    if !strings.Contains(stderr.String(), "parse ") {
        t.Errorf("stderr should mention parse: %q", stderr.String())
    }
}

func TestRun_FlagParseErrorIsUsageError(t *testing.T) {
    var stdout, stderr bytes.Buffer
    code := run([]string{"--not-a-real-flag"}, &stdout, &stderr, mapEnv{})
    if code != exitUsage {
        t.Errorf("code = %d, want exitUsage", code)
    }
}

func TestRun_GlobErrorIsUsageError(t *testing.T) {
    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=[", "--dry-run"}, &stdout, &stderr, mapEnv{})
    if code != exitUsage {
        t.Errorf("code = %d, want exitUsage", code)
    }
    if !strings.Contains(stderr.String(), "glob error") {
        t.Errorf("stderr missing 'glob error': %q", stderr.String())
    }
}

func TestRun_LocatorFillsMatchingNames(t *testing.T) {
    // Arrange — build a JUnit fixture whose test names match the
    // locator's own gotest fixture (TestOne, TestTwo, TestThree). This
    // reliably triggers the located>0 branch.
    fixture := filepath.Join(t.TempDir(), "junit.xml")
    body := `<?xml version="1.0"?>
<testsuites>
  <testsuite name="s">
    <testcase name="TestOne" classname="s"/>
    <testcase name="TestTwo" classname="s"/>
    <testcase name="TestThree" classname="s"/>
  </testsuite>
</testsuites>`
    if err := os.WriteFile(fixture, []byte(body), 0o644); err != nil {
        t.Fatalf("write fixture: %v", err)
    }
    sourceRoot := filepath.Join("..", "..", "testdata", "locate", "gotest")

    // Act
    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=" + fixture, "--dry-run", "--source-root=" + sourceRoot},
        &stdout, &stderr, mapEnv{})

    // Assert
    if code != exitOK {
        t.Fatalf("code = %d, stderr = %q", code, stderr.String())
    }
    if !strings.Contains(stdout.String(), "located: 3") {
        t.Errorf("stdout should show located count: %q", stdout.String())
    }
}

func TestRun_LocatedLinePrintedOnPublish(t *testing.T) {
    // Arrange — publish (non-dry-run) with locator matches. The
    // "located X test(s)" print in the publish path only fires when
    // located > 0.
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.WriteHeader(http.StatusCreated)
        _ = json.NewEncoder(w).Encode(map[string]any{"id": int64(1), "html_url": "https://ok"})
    }))
    defer srv.Close()

    fixture := filepath.Join(t.TempDir(), "junit.xml")
    body := `<?xml version="1.0"?>
<testsuites>
  <testsuite name="s">
    <testcase name="TestOne" classname="s"/>
  </testsuite>
</testsuites>`
    if err := os.WriteFile(fixture, []byte(body), 0o644); err != nil {
        t.Fatalf("write fixture: %v", err)
    }
    sourceRoot := filepath.Join("..", "..", "testdata", "locate", "gotest")
    envs := mapEnv{
        "GITHUB_TOKEN":      "t",
        "GITHUB_SHA":        "sha",
        "GITHUB_REPOSITORY": "o/r",
        "GITHUB_API_URL":    srv.URL,
    }

    // Act
    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=" + fixture, "--source-root=" + sourceRoot}, &stdout, &stderr, envs)

    // Assert
    if code != exitOK {
        t.Fatalf("code = %d, stderr = %q", code, stderr.String())
    }
    if !strings.Contains(stdout.String(), "located 1 test(s) via filesystem inference") {
        t.Errorf("stdout should include located line, got: %q", stdout.String())
    }
}

func TestRun_StepSummaryWriteFailureIsWarning(t *testing.T) {
    // Arrange — env points at a directory, so writeStepSummary fails.
    // The warning is emitted but the overall run still succeeds.
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.WriteHeader(http.StatusCreated)
        _ = json.NewEncoder(w).Encode(map[string]any{"id": int64(1), "html_url": "https://ok"})
    }))
    defer srv.Close()

    fixture := filepath.Join("..", "..", "testdata", "junit-surefire", "with-location-attr.xml")
    envs := mapEnv{
        "GITHUB_TOKEN":        "t",
        "GITHUB_SHA":          "sha",
        "GITHUB_REPOSITORY":   "o/r",
        "GITHUB_API_URL":      srv.URL,
        "GITHUB_STEP_SUMMARY": t.TempDir(), // directory, not a file
    }

    // Act
    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=" + fixture}, &stdout, &stderr, envs)

    // Assert
    if code != exitOK {
        t.Fatalf("code = %d, stderr = %q", code, stderr.String())
    }
    if !strings.Contains(stderr.String(), "warning: failed to write step summary") {
        t.Errorf("expected warning in stderr, got: %q", stderr.String())
    }
}

func TestPrintSummary_AllStatuses(t *testing.T) {
    // Arrange
    results := []ir.TestResult{
        {Status: ir.StatusPassed},
        {Status: ir.StatusFailed},
        {Status: ir.StatusSkipped},
        {Status: ir.StatusError},
    }
    parsers := map[string]int{"junit": 4}

    // Act
    var buf bytes.Buffer
    printSummary(&buf, results, parsers, 2)

    // Assert
    got := buf.String()
    for _, want := range []string{"passed:  1", "failed:  1", "skipped: 1", "errored: 1", "located: 2"} {
        if !strings.Contains(got, want) {
            t.Errorf("missing %q in: %s", want, got)
        }
    }
}

func TestSortStrings(t *testing.T) {
    // Arrange
    got := []string{"c", "a", "b", "aa"}

    // Act
    sortStrings(got)

    // Assert
    want := []string{"a", "aa", "b", "c"}
    for i := range want {
        if got[i] != want[i] {
            t.Errorf("sortStrings[%d] = %q, want %q; full = %v", i, got[i], want[i], got)
        }
    }
}

func TestFirstNonEmpty(t *testing.T) {
    cases := []struct {
        args []string
        want string
    }{
        {[]string{"", "", "third"}, "third"},
        {[]string{"first"}, "first"},
        {[]string{}, ""},
        {[]string{""}, ""},
    }
    for _, tc := range cases {
        if got := firstNonEmpty(tc.args...); got != tc.want {
            t.Errorf("firstNonEmpty(%v) = %q, want %q", tc.args, got, tc.want)
        }
    }
}

func TestExpandGlobs_MultipleAndEmpty(t *testing.T) {
    matches, err := expandGlobs("../../testdata/junit-ant/*.xml,,,")
    if err != nil {
        t.Fatalf("expandGlobs: %v", err)
    }
    if len(matches) < 2 {
        t.Errorf("only %d matches; expected at least 2", len(matches))
    }
}

func TestSplitRepoSlug(t *testing.T) {
    cases := []struct {
        in         string
        wantOwner  string
        wantRepo   string
        wantOK     bool
    }{
        {"owner/repo", "owner", "repo", true},
        {"o/r", "o", "r", true},
        {"", "", "", false},
        {"no-slash", "", "", false},
        {"/leading", "", "", false},
        {"trailing/", "", "", false},
        {"too/many/slashes", "", "", false},
    }
    for _, tc := range cases {
        o, r, ok := splitRepoSlug(tc.in)
        if o != tc.wantOwner || r != tc.wantRepo || ok != tc.wantOK {
            t.Errorf("splitRepoSlug(%q) = (%q, %q, %v); want (%q, %q, %v)",
                tc.in, o, r, ok, tc.wantOwner, tc.wantRepo, tc.wantOK)
        }
    }
}
