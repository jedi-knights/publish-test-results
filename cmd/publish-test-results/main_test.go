package main

import (
    "bytes"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "strings"
    "testing"
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
