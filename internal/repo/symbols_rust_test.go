package repo

import (
	"context"
	"testing"

	"marshal/internal/db"
)

const rustSample = `use std::io;

fn top() {}

struct S { x: i32 }

enum E { A, B }

trait T { fn m(&self); }

type Alias = i32;

impl S {
    fn method(&self) {}
}
`

func TestExtractSymbolsRust(t *testing.T) {
	got, err := ExtractSymbols(context.Background(), "rust", "sample.rs", []byte(rustSample))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.rs", Kind: "function", Name: "top", Signature: "fn top()", LineStart: 3})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.rs", Kind: "type", Name: "S", Signature: "struct S", LineStart: 5})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.rs", Kind: "type", Name: "E", Signature: "enum E", LineStart: 7})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.rs", Kind: "type", Name: "T", Signature: "trait T", LineStart: 9})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.rs", Kind: "type", Name: "Alias", Signature: "type Alias = i32;", LineStart: 11})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.rs", Kind: "method", Name: "method", Receiver: "S", Signature: "fn method(&self)", LineStart: 14})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.rs", Kind: "import", Name: "std::io", Signature: "use std::io;", LineStart: 1})
}

func TestExtractSymbolsRustToleratesSyntaxError(t *testing.T) {
	source := []byte("fn broken( {}\nfn valid() {}\n")
	got, err := ExtractSymbols(context.Background(), "rust", "broken.rs", source)
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "broken.rs", Kind: "function", Name: "valid"})
}
