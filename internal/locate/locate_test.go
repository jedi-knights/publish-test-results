package locate

import (
    "path/filepath"
    "testing"

    "github.com/jedi-knights/publish-test-results/internal/ir"
)

// gotestRoot is the fixture tree under testdata/locate/gotest/.
func gotestRoot(t *testing.T) string {
    t.Helper()
    // internal/locate → repo root → testdata/locate/gotest
    return filepath.Join("..", "..", "testdata", "locate", "gotest")
}

func TestLocator_IndexesGoTestFunctions(t *testing.T) {
    l := New(gotestRoot(t))
    got := l.Indexed()
    // Two _test.go files, 6 test-shaped functions total. The
    // non-test file (not_a_test.go) must be ignored.
    if got != 6 {
        t.Errorf("Indexed = %d, want 6", got)
    }
}

func TestLocator_LocateKnownTest(t *testing.T) {
    l := New(gotestRoot(t))
    loc, ok := l.Locate("", "TestTwo")
    if !ok {
        t.Fatal("TestTwo not found")
    }
    if !filepath.IsAbs(loc.File) && !isSubpath(loc.File, "pkga") {
        t.Errorf("File = %q, expected under pkga", loc.File)
    }
    if loc.Line != 13 {
        // TestTwo has a comment above it; the func line is 13.
        t.Errorf("Line = %d, want 13", loc.Line)
    }
}

func TestLocator_LocateBenchAndFuzz(t *testing.T) {
    l := New(gotestRoot(t))
    for _, tc := range []struct {
        name string
        want int
    }{
        {"BenchmarkThing", 18},
        {"ExampleFoo", 11},
        {"FuzzBar", 16},
    } {
        loc, ok := l.Locate("", tc.name)
        if !ok {
            t.Errorf("%s not found", tc.name)
            continue
        }
        if loc.Line != tc.want {
            t.Errorf("%s: line = %d, want %d", tc.name, loc.Line, tc.want)
        }
    }
}

func TestLocator_LocateMissing(t *testing.T) {
    l := New(gotestRoot(t))
    _, ok := l.Locate("", "TestDoesNotExist")
    if ok {
        t.Error("expected miss for unknown test name")
    }
}

func TestLocator_IgnoresNonTestFiles(t *testing.T) {
    l := New(gotestRoot(t))
    _, ok := l.Locate("", "TestShouldSkip")
    if ok {
        t.Error("TestShouldSkip is defined in a non-_test.go file and should not be indexed")
    }
}

func TestLocator_Fill(t *testing.T) {
    l := New(gotestRoot(t))
    results := []ir.TestResult{
        {Name: "TestOne", Status: ir.StatusPassed},
        {Name: "TestThree", Status: ir.StatusPassed},
        {Name: "UnknownTest", Status: ir.StatusPassed},
        // Producer already knew this one; locator must not overwrite.
        {Name: "TestTwo", Status: ir.StatusPassed, File: "producer/path.go", Line: 99},
    }
    l.Fill(results)

    if results[0].Line == 0 || results[0].File == "" {
        t.Error("TestOne: locator did not fill")
    }
    if results[1].Line == 0 || results[1].File == "" {
        t.Error("TestThree: locator did not fill")
    }
    if results[2].File != "" || results[2].Line != 0 {
        t.Error("UnknownTest: locator should not have filled")
    }
    if results[3].File != "producer/path.go" || results[3].Line != 99 {
        t.Error("TestTwo: locator overwrote producer-supplied location")
    }
}

func TestLocator_FillOnlyEmptyFields(t *testing.T) {
    // If the producer gave us a File but not a Line, the locator
    // should complete only the missing half.
    l := New(gotestRoot(t))
    results := []ir.TestResult{
        {Name: "TestOne", File: "custom.go"},
    }
    l.Fill(results)
    if results[0].File != "custom.go" {
        t.Errorf("File overwritten: %q", results[0].File)
    }
    if results[0].Line == 0 {
        t.Error("Line not filled")
    }
}

func TestShouldSkipDir(t *testing.T) {
    cases := map[string]bool{
        ".":            false,
        "..":           false,
        ".git":         true,
        ".github":      true,
        ".claude":      true,
        "node_modules": true,
        "vendor":       true,
        "build":        true,
        "dist":         true,
        "bin":          true,
        "target":       true,
        "src":          false,
        "internal":     false,
        "testdata":     true,
    }
    for name, want := range cases {
        if got := shouldSkipDir(name); got != want {
            t.Errorf("shouldSkipDir(%q) = %v, want %v", name, got, want)
        }
    }
}

func TestCandidateKeys(t *testing.T) {
    cases := []struct {
        class, name string
        want        []string
    }{
        {"", "TestFoo", []string{"TestFoo"}},
        {"pkg", "TestFoo", []string{"TestFoo", "pkg.TestFoo"}},
        {"", "", nil},
    }
    for _, tc := range cases {
        got := candidateKeys(tc.class, tc.name)
        if !stringSlicesEqual(got, tc.want) {
            t.Errorf("candidateKeys(%q, %q) = %v, want %v",
                tc.class, tc.name, got, tc.want)
        }
    }
}

// isSubpath is a tiny helper — filepath.Rel would work too but this
// is enough for the "is this location under pkga" assertion above.
func isSubpath(p, want string) bool {
    for _, part := range filepath.SplitList(p) {
        if part == want {
            return true
        }
    }
    return filepath.Base(filepath.Dir(p)) == want
}

func stringSlicesEqual(a, b []string) bool {
    if len(a) != len(b) {
        return false
    }
    for i := range a {
        if a[i] != b[i] {
            return false
        }
    }
    return true
}
