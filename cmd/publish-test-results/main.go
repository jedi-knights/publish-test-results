// Command publish-test-results is the GitHub Action entrypoint. It reads
// action inputs (from CLI flags in --dry-run mode, from the standard
// GitHub Actions environment variables when publishing for real),
// parses one or more test-report files via the dialect-agnostic parser
// bank in internal/parse, and posts a Check Run to the GitHub API via
// internal/publish.
package main

import (
    "context"
    "flag"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "strings"

    "github.com/jedi-knights/publish-test-results/internal/ir"
    "github.com/jedi-knights/publish-test-results/internal/parse"
    "github.com/jedi-knights/publish-test-results/internal/publish"
)

// version is stamped at release time via -ldflags. The default is
// suitable for local development.
var version = "0.1.0-dev"

// exit codes matching test-runner convention:
//   0 — parse and publish succeeded
//   1 — reserved for future "some tests failed and --fail-on-error was set"
//   2 — usage / IO / parse / publish error
const (
    exitOK    = 0
    exitUsage = 2
)

func main() {
    os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, osEnv{}))
}

// env abstracts environment access so tests can supply their own map.
type env interface {
    Get(key string) string
}

type osEnv struct{}

func (osEnv) Get(key string) string { return os.Getenv(key) }

type mapEnv map[string]string

func (m mapEnv) Get(key string) string { return m[key] }

func run(args []string, stdout, stderr io.Writer, envs env) int {
    var (
        files       string
        checkName   string
        githubToken string
        headSHA     string
        repoSlug    string
        apiURL      string
        dryRun      bool
        showVer     bool
    )
    fs := flag.NewFlagSet("publish-test-results", flag.ContinueOnError)
    fs.SetOutput(stderr)
    fs.StringVar(&files, "files", "", "Comma-separated globs of test-report files to parse")
    fs.StringVar(&checkName, "check-name", "Test Results", "Name shown in the GitHub Checks tab")
    fs.StringVar(&githubToken, "github-token", "", "GitHub API token (falls back to GITHUB_TOKEN env)")
    fs.StringVar(&headSHA, "head-sha", "", "Commit SHA the check-run is attached to (falls back to GITHUB_SHA env)")
    fs.StringVar(&repoSlug, "repository", "", "owner/repo slug (falls back to GITHUB_REPOSITORY env)")
    fs.StringVar(&apiURL, "api-url", "", "GitHub API base URL (falls back to GITHUB_API_URL env, then api.github.com)")
    fs.BoolVar(&dryRun, "dry-run", false, "Parse the files and print a summary without posting to the GitHub API")
    fs.BoolVar(&showVer, "version", false, "Print version and exit")

    if err := fs.Parse(args); err != nil {
        return exitUsage
    }

    if showVer {
        fmt.Fprintln(stdout, version)
        return exitOK
    }

    if files == "" {
        fmt.Fprintf(stderr, "publish-test-results %s\n", version)
        fmt.Fprintf(stderr, "registered parsers: %d\n", len(parse.Registered()))
        fmt.Fprintln(stderr, "usage: publish-test-results --files=GLOB[,GLOB...] [--dry-run]")
        return exitUsage
    }

    matches, err := expandGlobs(files)
    if err != nil {
        fmt.Fprintf(stderr, "glob error: %v\n", err)
        return exitUsage
    }
    if len(matches) == 0 {
        fmt.Fprintf(stderr, "no files matched: %s\n", files)
        return exitUsage
    }

    var (
        all        []ir.TestResult
        parserSeen = map[string]int{}
    )
    for _, path := range matches {
        f, err := os.Open(path)
        if err != nil {
            fmt.Fprintf(stderr, "open %s: %v\n", path, err)
            return exitUsage
        }
        report, name, err := parse.Detect(f)
        _ = f.Close()
        if err != nil {
            fmt.Fprintf(stderr, "parse %s: %v\n", path, err)
            return exitUsage
        }
        parserSeen[name] += len(report.Results)
        all = append(all, report.Results...)
    }

    if dryRun {
        printSummary(stdout, all, parserSeen)
        return exitOK
    }

    // Real publish: resolve credentials + target from flags or env,
    // then hand off to internal/publish.
    token := firstNonEmpty(githubToken, envs.Get("GITHUB_TOKEN"))
    if token == "" {
        fmt.Fprintln(stderr, "GITHUB_TOKEN not set (pass --github-token or set the env var)")
        return exitUsage
    }
    sha := firstNonEmpty(headSHA, envs.Get("GITHUB_SHA"))
    if sha == "" {
        fmt.Fprintln(stderr, "GITHUB_SHA not set (pass --head-sha or set the env var)")
        return exitUsage
    }
    slug := firstNonEmpty(repoSlug, envs.Get("GITHUB_REPOSITORY"))
    if slug == "" {
        fmt.Fprintln(stderr, "GITHUB_REPOSITORY not set (pass --repository or set the env var)")
        return exitUsage
    }
    owner, repo, ok := splitRepoSlug(slug)
    if !ok {
        fmt.Fprintf(stderr, "malformed repository slug %q; expected owner/repo\n", slug)
        return exitUsage
    }
    baseURL := firstNonEmpty(apiURL, envs.Get("GITHUB_API_URL"), "https://api.github.com")

    client := publish.NewClient(token, owner, repo)
    client.BaseURL = baseURL

    resp, err := publish.Publish(context.Background(), client, publish.Config{
        CheckName: checkName,
        HeadSHA:   sha,
        Options:   publish.DefaultOptions(),
    }, all)
    if err != nil {
        fmt.Fprintf(stderr, "publish failed: %v\n", err)
        return exitUsage
    }

    fmt.Fprintf(stdout, "published %d test result(s) via %s\n",
        len(all), joinParsers(parserSeen))
    fmt.Fprintf(stdout, "check-run: %s\n", resp.HTMLURL)
    return exitOK
}

// expandGlobs turns "a.xml,build/**/*.xml" into a flat list of matched
// filesystem paths. filepath.Glob only handles a single pattern, so we
// split on commas ourselves.
func expandGlobs(spec string) ([]string, error) {
    var out []string
    for _, pattern := range strings.Split(spec, ",") {
        pattern = strings.TrimSpace(pattern)
        if pattern == "" {
            continue
        }
        matches, err := filepath.Glob(pattern)
        if err != nil {
            return nil, fmt.Errorf("%s: %w", pattern, err)
        }
        out = append(out, matches...)
    }
    return out, nil
}

// printSummary writes a compact multi-line summary of the parsed
// results. Intended for humans reading a CI log.
func printSummary(w io.Writer, results []ir.TestResult, parsers map[string]int) {
    var passed, failed, skipped, errored int
    for _, r := range results {
        switch r.Status {
        case ir.StatusPassed:
            passed++
        case ir.StatusFailed:
            failed++
        case ir.StatusSkipped:
            skipped++
        case ir.StatusError:
            errored++
        }
    }
    fmt.Fprintf(w, "Parsed %d test result(s) via %s\n",
        len(results), joinParsers(parsers))
    fmt.Fprintf(w, "  passed:  %d\n", passed)
    fmt.Fprintf(w, "  failed:  %d\n", failed)
    fmt.Fprintf(w, "  errored: %d\n", errored)
    fmt.Fprintf(w, "  skipped: %d\n", skipped)
}

// joinParsers stringifies the per-parser test count as "name×N, name×N".
// Map iteration order is unstable; sort so log lines are reproducible.
func joinParsers(parsers map[string]int) string {
    names := make([]string, 0, len(parsers))
    for name := range parsers {
        names = append(names, name)
    }
    sortStrings(names)
    parts := make([]string, 0, len(names))
    for _, name := range names {
        parts = append(parts, fmt.Sprintf("%s×%d", name, parsers[name]))
    }
    return strings.Join(parts, ", ")
}

// sortStrings avoids pulling in the sort package for one call site;
// n is always small (≤ number of registered parsers) so an insertion
// sort is fine.
func sortStrings(s []string) {
    for i := 1; i < len(s); i++ {
        for j := i; j > 0 && s[j-1] > s[j]; j-- {
            s[j-1], s[j] = s[j], s[j-1]
        }
    }
}

func firstNonEmpty(values ...string) string {
    for _, v := range values {
        if v != "" {
            return v
        }
    }
    return ""
}

// splitRepoSlug parses "owner/repo" into its parts. Rejects anything
// that doesn't look like exactly one slash-separated pair.
func splitRepoSlug(slug string) (owner, repo string, ok bool) {
    slash := strings.Index(slug, "/")
    if slash <= 0 || slash == len(slug)-1 {
        return "", "", false
    }
    if strings.Contains(slug[slash+1:], "/") {
        return "", "", false
    }
    return slug[:slash], slug[slash+1:], true
}
