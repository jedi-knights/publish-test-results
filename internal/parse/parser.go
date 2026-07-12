// Package parse is the multi-dialect input-parser bank. Every dialect
// implements the Parser interface and registers itself via Register at
// package init time; Detect walks the registry in insertion order and
// routes an input to the first parser whose CanParse returns true.
package parse

import (
    "bytes"
    "errors"
    "fmt"
    "io"

    "github.com/jedi-knights/publish-test-results/internal/ir"
)

// Parser translates one input dialect into an ir.Report.
type Parser interface {
    // Name returns a stable identifier used in metadata and diagnostics.
    Name() string
    // CanParse returns true if this parser can handle the given preview
    // bytes (up to the first PreviewSize bytes of the input). Detect
    // uses this to route.
    CanParse(preview []byte) bool
    // Parse consumes the full input reader.
    Parse(r io.Reader) (ir.Report, error)
}

// PreviewSize is the number of leading bytes Detect reads for sniffing.
// Kept small enough to fit any format's header signature in a single
// buffered read.
const PreviewSize = 256

// registry holds every registered parser. Insertion order matters:
// Detect returns the first match, so more-specific parsers should
// register before more-general ones.
var registry []Parser

// Register adds a parser to the registry. Called from each parser's
// init() so import side-effects wire everything up automatically.
func Register(p Parser) {
    registry = append(registry, p)
}

// Registered returns the currently registered parsers in insertion
// order. Provided for diagnostics; not intended for mutation.
func Registered() []Parser {
    out := make([]Parser, len(registry))
    copy(out, registry)
    return out
}

// ErrNoParserMatched is returned when no registered parser recognizes
// the input.
var ErrNoParserMatched = errors.New("no parser matched the input")

// Detect reads a preview from r, picks a Parser via CanParse, and
// parses the full input. The preview bytes are stitched back in front
// of the remaining stream so parsers see the whole input.
//
// Returns the parsed Report and the matching parser's Name (useful for
// metadata), or ErrNoParserMatched if nothing recognized the input.
func Detect(r io.Reader) (ir.Report, string, error) {
    preview := make([]byte, PreviewSize)
    n, err := io.ReadFull(r, preview)
    if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
        return ir.Report{}, "", fmt.Errorf("preview read: %w", err)
    }
    preview = preview[:n]

    for _, p := range registry {
        if !p.CanParse(preview) {
            continue
        }
        combined := io.MultiReader(bytes.NewReader(preview), r)
        report, err := p.Parse(combined)
        if err != nil {
            return ir.Report{}, p.Name(), fmt.Errorf("%s: %w", p.Name(), err)
        }
        return report, p.Name(), nil
    }
    return ir.Report{}, "", ErrNoParserMatched
}
