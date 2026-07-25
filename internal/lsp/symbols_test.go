package lsp

import (
	"testing"
)

func TestMapSymbolsFlattens(t *testing.T) {
	docs := []DocumentSymbol{{
		Name: "T", Kind: 23, // Struct
		Range: Range{Start: Position{Line: 0}, End: Position{Line: 4}},
		Children: []DocumentSymbol{{
			Name: "M", Kind: 6, // Method
			Range: Range{Start: Position{Line: 1}, End: Position{Line: 3}},
		}},
	}}
	got := MapSymbols("a.go", docs)
	if len(got) != 2 {
		t.Fatalf("got %d symbols", len(got))
	}
	for _, s := range got {
		if s.Source != "lsp" || s.FilePath != "a.go" {
			t.Fatalf("symbol = %#v", s)
		}
		if s.Name == "T" && s.Kind != "type" {
			t.Fatalf("T kind = %q", s.Kind)
		}
		if s.Name == "M" && s.Kind != "method" {
			t.Fatalf("M kind = %q", s.Kind)
		}
		if s.LineStart == 0 {
			t.Fatalf("1-based line expected, got %#v", s)
		}
	}
}
