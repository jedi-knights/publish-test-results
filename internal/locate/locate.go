// Package locate infers source file:line for tests whose input
// dialect doesn't carry that information. Every dialect-agnostic
// producer we control eventually should carry file:line itself; the
// locator exists so drill-down works today for producers we don't
// control (pytest, Jest, gotestsum, etc.).
//
// The first supported language is Go: any `func TestFoo(t *testing.T)`
// discovered under one of the configured roots is indexed by name.
// Other languages land as follow-ups by adding an indexer function
// per file extension.
package locate

import (
    "bufio"
    "io/fs"
    "os"
    "path/filepath"
    "regexp"
    "strings"
    "sync"

    "github.com/jedi-knights/publish-test-results/internal/ir"
)

// Location is a file:line pair.
type Location struct {
    File string
    Line int
}

// Locator maps test names to source locations. Built lazily on first
// Locate call; safe for concurrent use after Locate returns once.
type Locator struct {
    roots []string
    once  sync.Once
    index map[string]Location
}

// New constructs a Locator that walks each root looking for test-
// shaped functions. An empty roots list defaults to the current
// working directory.
func New(roots ...string) *Locator {
    if len(roots) == 0 {
        roots = []string{"."}
    }
    // Copy so callers can't mutate our root list after construction.
    cp := make([]string, len(roots))
    copy(cp, roots)
    return &Locator{roots: cp}
}

// Locate returns the source location of a test by name. The class
// hint is used as a fallback key ("class.name") but Go — the only
// language supported today — identifies tests by bare function name,
// so the class hint is usually redundant.
func (l *Locator) Locate(class, name string) (Location, bool) {
    l.once.Do(l.build)
    for _, key := range candidateKeys(class, name) {
        if loc, ok := l.index[key]; ok {
            return loc, true
        }
    }
    return Location{}, false
}

// Fill walks the results slice and stamps File/Line on entries that
// don't already have them, using the locator's index. Modifies the
// slice in place; entries with existing File and Line are left
// untouched (the producer knows best).
func (l *Locator) Fill(results []ir.TestResult) {
    for i, r := range results {
        if r.File != "" && r.Line != 0 {
            continue
        }
        loc, ok := l.Locate(r.Class, r.Name)
        if !ok {
            continue
        }
        if r.File == "" {
            results[i].File = loc.File
        }
        if r.Line == 0 {
            results[i].Line = loc.Line
        }
    }
}

// Indexed reports how many entries the locator's index contains.
// Useful for logging and tests.
func (l *Locator) Indexed() int {
    l.once.Do(l.build)
    return len(l.index)
}

// candidateKeys returns the lookup keys to try in priority order.
func candidateKeys(class, name string) []string {
    if name == "" {
        return nil
    }
    if class == "" {
        return []string{name}
    }
    return []string{name, class + "." + name}
}

// build walks every configured root once, dispatching each file to
// the extension-appropriate indexer. Errors from unreadable files
// are swallowed silently — a locator is a best-effort helper, not a
// hard dependency of the publish path.
func (l *Locator) build() {
    l.index = map[string]Location{}
    for _, root := range l.roots {
        _ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
            if err != nil {
                return nil
            }
            if d.IsDir() {
                if shouldSkipDir(d.Name()) {
                    return filepath.SkipDir
                }
                return nil
            }
            if strings.HasSuffix(path, "_test.go") {
                indexGoTestFile(path, l.index)
            }
            return nil
        })
    }
}

// shouldSkipDir returns true for directories that are noisy or
// irrelevant to source discovery. Called on the base name of each
// directory, not the full path.
func shouldSkipDir(name string) bool {
    if name == "." || name == ".." {
        return false
    }
    if strings.HasPrefix(name, ".") {
        return true
    }
    switch name {
    case "node_modules", "vendor", "dist", "build", "bin", "target", "testdata":
        return true
    }
    return false
}

// goTestFuncRE matches a top-level Go test / benchmark / example
// function declaration. Bound to line start to avoid matching inside
// strings or comments; the parameter list must open on the same line.
var goTestFuncRE = regexp.MustCompile(`^func\s+((?:Test|Benchmark|Example|Fuzz)\w+)\s*\(`)

// indexGoTestFile scans a *_test.go file line by line, records
// (funcName → path:lineno) for every match, and returns silently
// on any I/O error.
func indexGoTestFile(path string, index map[string]Location) {
    f, err := os.Open(path)
    if err != nil {
        return
    }
    defer f.Close()
    scanner := bufio.NewScanner(f)
    // Bump the buffer size so pathologically long lines don't error.
    scanner.Buffer(make([]byte, 64*1024), 1024*1024)
    lineno := 0
    for scanner.Scan() {
        lineno++
        m := goTestFuncRE.FindStringSubmatch(scanner.Text())
        if m == nil {
            continue
        }
        // First match wins: if the same name appears twice, index
        // the first (typically the primary definition, not a helper).
        if _, exists := index[m[1]]; !exists {
            index[m[1]] = Location{File: path, Line: lineno}
        }
    }
}
