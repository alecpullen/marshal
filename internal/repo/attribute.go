package repo

import (
	"context"
	"sort"

	sitter "github.com/smacker/go-tree-sitter"

	"marshal/internal/db"
)

// SymbolHit is a matched symbol plus the position of its *name*.
//
// db.Symbol stores no column (internal/db/migrations.go), so it cannot on
// its own supply what an LSP references query needs. NameLine/NameCol are
// carried here rather than added to the DB schema: they are only ever
// needed in-process, at the moment of attribution.
type SymbolHit struct {
	db.Symbol
	NameLine int // 1-based, matching db.Symbol.LineStart; 0 when unresolved
	NameCol  int // 0-based column within NameLine
}

// Resolved reports whether the name position is usable for a position-based
// query. An unresolved hit still names the symbol; callers must not query a
// guessed position, because a query at the wrong place returns confidently
// wrong results.
func (h SymbolHit) Resolved() bool { return h.NameLine > 0 }

// ChangedSymbols returns the symbols in source whose line spans intersect
// any of ranges, innermost first. An unsupported language returns nil, nil:
// "this language has no grammar" is not a failure.
//
// source must be the file's POST-edit content. The SQLite symbol index is a
// startup snapshot on projects without embeddings (see the index watcher
// gate in internal/app/app.go), and attribution is by line range — so using
// the index here would silently name the wrong symbol once the agent has
// edited a file. Re-parsing costs one tree-sitter parse per changed file.
func ChangedSymbols(ctx context.Context, lang, path string, source []byte, ranges []LineRange) ([]SymbolHit, error) {
	if len(ranges) == 0 || !SupportedLanguage(lang) {
		return nil, nil
	}
	syms, err := ExtractSymbols(ctx, lang, path, source)
	if err != nil {
		return nil, err
	}
	var hits []SymbolHit
	for _, s := range syms {
		// An import is not a meaningful subject for "what changed here".
		if s.Kind == "import" {
			continue
		}
		if !intersectsAny(ranges, s.LineStart, s.LineEnd) {
			continue
		}
		hits = append(hits, SymbolHit{Symbol: s})
	}
	if len(hits) == 0 {
		return nil, nil
	}
	// Innermost first: a change inside a method attributes to the method
	// rather than to an enclosing or adjacent type, so narrower spans win.
	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].LineEnd-hits[i].LineStart < hits[j].LineEnd-hits[j].LineStart
	})
	resolveNamePositions(ctx, lang, path, source, hits)
	return hits, nil
}

// intersectsAny reports whether the inclusive span [start, end] overlaps any
// half-open range in ranges.
func intersectsAny(ranges []LineRange, start, end int) bool {
	for _, r := range ranges {
		if start < r.End && end >= r.Start {
			return true
		}
	}
	return false
}

// nameKey identifies a declaration by its first line and declared name,
// which is enough to pair a db.Symbol with its name node.
type nameKey struct {
	line int
	name string
}

// resolveNamePositions fills in NameLine/NameCol for each hit.
//
// It walks the tree once and records every node that has a "name" field —
// which is exactly how all four per-language extractors read names
// (symbols.go, symbols_typescript.go, symbols_python.go, symbols_rust.go),
// so one generic walk covers every grammar without touching any of them.
//
// A hit that cannot be paired keeps NameLine == 0. That is deliberate: a
// guessed column would produce a reference query against the wrong
// position, and wrong callers are worse than no callers.
func resolveNamePositions(ctx context.Context, lang, path string, source []byte, hits []SymbolHit) {
	language := languageFor(lang, path)
	if language == nil {
		return
	}
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(language)

	tree, err := parser.ParseCtx(ctx, nil, source)
	if err != nil {
		return
	}
	defer tree.Close()

	positions := map[nameKey]sitter.Point{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if nameNode := n.ChildByFieldName("name"); nameNode != nil {
			k := nameKey{line: int(n.StartPoint().Row) + 1, name: nameNode.Content(source)}
			// First writer wins: the outermost node starting on that line
			// is the declaration, and any nested node with the same name on
			// the same line is a use, not the declaration.
			if _, seen := positions[k]; !seen {
				positions[k] = nameNode.StartPoint()
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())

	for i := range hits {
		if p, ok := positions[nameKey{line: hits[i].LineStart, name: hits[i].Name}]; ok {
			hits[i].NameLine = int(p.Row) + 1
			hits[i].NameCol = int(p.Column)
		}
	}
}
