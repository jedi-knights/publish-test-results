package main

import (
    "bytes"
    "path/filepath"
    "strings"
    "testing"
)

func TestRun_NoArgsPrintsUsage(t *testing.T) {
    var stdout, stderr bytes.Buffer
    code := run(nil, &stdout, &stderr)
    if code != exitUsage {
        t.Errorf("code = %d, want %d", code, exitUsage)
    }
    if !strings.Contains(stderr.String(), "usage:") {
        t.Errorf("stderr missing usage: %q", stderr.String())
    }
}

func TestRun_Version(t *testing.T) {
    var stdout, stderr bytes.Buffer
    code := run([]string{"--version"}, &stdout, &stderr)
    if code != exitOK {
        t.Fatalf("code = %d, want %d", code, exitOK)
    }
    if strings.TrimSpace(stdout.String()) != version {
        t.Errorf("stdout = %q, want %q", stdout.String(), version)
    }
}

func TestRun_DryRunOnJUnitFixture(t *testing.T) {
    // Point --files at the JUnit passing fixture in testdata. Since
    // cmd/ runs from its own package directory during `go test`, the
    // relative path back to the repo root is one level up.
    fixture := filepath.Join("..", "..", "testdata", "junit-surefire", "passing.xml")

    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=" + fixture, "--dry-run"}, &stdout, &stderr)
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
    code := run([]string{"--files=" + fixture, "--dry-run"}, &stdout, &stderr)
    if code != exitOK {
        t.Fatalf("code = %d, stderr = %q", code, stderr.String())
    }
    if !strings.Contains(stdout.String(), "Parsed 12 test result(s) via junit×12") {
        t.Errorf("summary line missing/wrong: %q", stdout.String())
    }
}

func TestRun_NoMatchIsUsageError(t *testing.T) {
    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=nonexistent-*.xml", "--dry-run"}, &stdout, &stderr)
    if code != exitUsage {
        t.Errorf("code = %d, want %d", code, exitUsage)
    }
    if !strings.Contains(stderr.String(), "no files matched") {
        t.Errorf("stderr missing 'no files matched': %q", stderr.String())
    }
}

func TestRun_WithoutDryRunIsNotYetSupported(t *testing.T) {
    fixture := filepath.Join("..", "..", "testdata", "junit-surefire", "passing.xml")
    var stdout, stderr bytes.Buffer
    code := run([]string{"--files=" + fixture}, &stdout, &stderr)
    if code != exitUsage {
        t.Errorf("code = %d, want %d", code, exitUsage)
    }
    if !strings.Contains(stderr.String(), "publisher not implemented yet") {
        t.Errorf("stderr should mention missing publisher: %q", stderr.String())
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
    // Comma-separated with an empty entry that should be skipped.
    matches, err := expandGlobs("../../testdata/junit-ant/*.xml,,,")
    if err != nil {
        t.Fatalf("expandGlobs: %v", err)
    }
    if len(matches) < 2 {
        t.Errorf("only %d matches; expected at least 2", len(matches))
    }
}
