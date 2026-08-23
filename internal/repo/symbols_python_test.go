package repo

import (
	"context"
	"testing"

	"marshal/internal/db"
)

const pySample = `import os
from pkg import thing

def top(x):
    return x

class Foo:
    def method(self):
        pass

    def _helper(self):
        pass
`

func TestExtractSymbolsPython(t *testing.T) {
	got, err := ExtractSymbols(context.Background(), "python", "sample.py", []byte(pySample))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.py", Kind: "function", Name: "top", Signature: "def top(x):", LineStart: 4, LineEnd: 5})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.py", Kind: "type", Name: "Foo", Signature: "class Foo:", LineStart: 7})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.py", Kind: "method", Name: "method", Receiver: "Foo", Signature: "def method(self):", LineStart: 8})
	// Extraction keeps underscore-prefixed names; repo.map filters them.
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.py", Kind: "method", Name: "_helper", Receiver: "Foo"})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.py", Kind: "import", Name: "os", Signature: "import os", LineStart: 1})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.py", Kind: "import", Name: "pkg", Signature: "from pkg import thing", LineStart: 2})
}

func TestExtractSymbolsPythonDecorated(t *testing.T) {
	source := []byte("import functools\n\n@functools.cache\ndef cached(x):\n    return x\n")
	got, err := ExtractSymbols(context.Background(), "python", "deco.py", source)
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "deco.py", Kind: "function", Name: "cached", LineStart: 4})
}

func TestExtractSymbolsPythonSkipsNestedFunctions(t *testing.T) {
	source := []byte("def outer(x):\n    def inner(y):\n        return y\n    return inner(x)\n")
	got, err := ExtractSymbols(context.Background(), "python", "nested.py", source)
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "nested.py", Kind: "function", Name: "outer"})
	for _, s := range got {
		if s.Name == "inner" {
			t.Fatalf("nested function should not be extracted: %+v", s)
		}
	}
}

func TestExtractSymbolsPythonToleratesSyntaxError(t *testing.T) {
	source := []byte("def broken(:\n    pass\ndef valid():\n    return 1\n")
	got, err := ExtractSymbols(context.Background(), "python", "broken.py", source)
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "broken.py", Kind: "function", Name: "valid"})
}
