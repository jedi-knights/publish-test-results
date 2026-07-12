// Command publish-test-results is the GitHub Action entrypoint. It reads
// action inputs from environment variables, parses one or more test-report
// files via the dialect-agnostic parser bank in internal/parse, and posts
// a Check Run to the GitHub API via internal/publish.
//
// This build is a scaffolding stub. The parser bank and publisher land in
// subsequent commits; running the binary today prints a version banner
// and exits successfully so downstream CI plumbing can be validated in
// isolation.
package main

import (
    "fmt"
    "os"

    "github.com/jedi-knights/publish-test-results/internal/parse"
)

// version is stamped at release time via -ldflags. The default is
// suitable for local development.
var version = "0.1.0-dev"

func main() {
    fmt.Fprintf(os.Stderr, "publish-test-results %s\n", version)
    fmt.Fprintf(os.Stderr, "registered parsers: %d\n", len(parse.Registered()))
    fmt.Fprintln(os.Stderr, "note: scaffolding build; parser bank and publisher land in follow-up commits")
}
