package lsp

import "marshal/internal/db"

// lspKind maps LSP SymbolKind numbers to Marshal's symbol kinds.
func lspKind(k int) string {
	switch k {
	case 12: // Function
		return "function"
	case 6: // Method
		return "method"
	case 5, 23: // Class, Struct
		return "type"
	case 11, 10: // Interface, Enum
		return "type"
	default:
		return "symbol"
	}
}

// MapSymbols flattens an LSP DocumentSymbol tree into db.Symbols tagged
// source="lsp". LSP positions are 0-based; db lines are 1-based.
func MapSymbols(filePath string, docSyms []DocumentSymbol) []db.Symbol {
	var out []db.Symbol
	var walk func(ds DocumentSymbol)
	walk = func(ds DocumentSymbol) {
		out = append(out, db.Symbol{
			FilePath:  filePath,
			Kind:      lspKind(ds.Kind),
			Name:      ds.Name,
			Signature: ds.Detail,
			LineStart: ds.Range.Start.Line + 1,
			LineEnd:   ds.Range.End.Line + 1,
			Source:    "lsp",
		})
		for _, c := range ds.Children {
			walk(c)
		}
	}
	for _, ds := range docSyms {
		walk(ds)
	}
	return out
}
