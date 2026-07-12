// Command publish-test-results is the GitHub Action entrypoint. It reads
// action inputs (from CLI flags in dry-run mode, from INPUT_* environment
// variables when invoked as the composite action), parses one or more
// test-report files via the dialect-agnostic parser bank in internal/parse,
// and — once the publisher lands — posts a Check Run to the GitHub API
// via internal/publish.
//
// Today only --dry-run is wired up. It parses every matching file and
// prints a summary. That's enough to dogfood the parser bank against the
// action's own test suite in CI without the publisher being ready.
package main

import (
    "flag"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "strings"

    "github.com/jedi-knights/publish-test-results/internal/ir"
    "github.com/jedi-knights/publish-test-results/internal/parse"
)

// version is stamped at release time via -ldflags. The default is
// suitable for local development.
var version = "0.1.0-dev"

// exit codes matching test-runner convention:
//   0 — parse succeeded
//   1 — reserved for future "some tests failed and --fail-on-error was set"
//   2 — usage / IO / parse error
const (
    exitOK    = 0
    exitUsage = 2
)

func main() {
    os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
    var (
        files   string
        dryRun  bool
        showVer bool
    )
    fs := flag.NewFlagSet("publish-test-results", flag.ContinueOnError)
    fs.SetOutput(stderr)
    fs.StringVar(&files, "files", "", "Comma-separated globs of test-report files to parse")
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
        all         []ir.TestResult
        parserSeen  = map[string]int{}
        totalErrors int
    )
    for _, path := range matches {
        f, err := os.Open(path)
        if err != nil {
            fmt.Fprintf(stderr, "open %s: %v\n", path, err)
            totalErrors++
            continue
        }
        report, name, err := parse.Detect(f)
        _ = f.Close()
        if err != nil {
            fmt.Fprintf(stderr, "parse %s: %v\n", path, err)
            totalErrors++
            continue
        }
        parserSeen[name] += len(report.Results)
        all = append(all, report.Results...)
    }
    if totalErrors > 0 {
        return exitUsage
    }

    if dryRun {
        printSummary(stdout, all, parserSeen)
        return exitOK
    }

    fmt.Fprintln(stderr, "note: publisher not implemented yet; re-run with --dry-run for parse validation")
    return exitUsage
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

// printSummary writes a compact multi-line summary of the parsed results.
// The format is intended for humans reading a CI log — not for downstream
// tooling.
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

// joinParsers stringifies the per-parser test count as "name×N, name×N"
// in whatever order Go's map iteration hands them out — stable enough
// for a CI log line, not intended for parsing.
func joinParsers(parsers map[string]int) string {
    var parts []string
    for name, n := range parsers {
        parts = append(parts, fmt.Sprintf("%s×%d", name, n))
    }
    return strings.Join(parts, ", ")
}
