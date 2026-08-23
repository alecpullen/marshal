package repo

import (
	"context"
	"testing"

	"marshal/internal/db"
)

const tsSample = `import { foo } from './foo';
function add(a: number, b: number): number { return a + b; }
const mul = (a: number, b: number) => a * b;
export function shout(s: string): string { return s; }
class Greeter {
	greet(name: string): string { return name; }
}
interface Shape { area(): number; }
type Alias = string | number;
enum Color { Red, Green }
`

func TestExtractSymbolsTypeScript(t *testing.T) {
	got, err := ExtractSymbols(context.Background(), "typescript", "sample.ts", []byte(tsSample))
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.ts", Kind: "function", Name: "add", Signature: "function add(a: number, b: number): number", LineStart: 2, LineEnd: 2})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.ts", Kind: "function", Name: "mul", Signature: "mul = (a: number, b: number) => a * b", LineStart: 3, LineEnd: 3})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.ts", Kind: "function", Name: "shout", LineStart: 4, LineEnd: 4})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.ts", Kind: "type", Name: "Greeter", Signature: "class Greeter", LineStart: 5})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.ts", Kind: "method", Name: "greet", Receiver: "Greeter", Signature: "greet(name: string): string", LineStart: 6})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.ts", Kind: "type", Name: "Shape", Signature: "interface Shape"})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.ts", Kind: "type", Name: "Alias", Signature: "type Alias = string | number;"})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.ts", Kind: "type", Name: "Color", Signature: "enum Color"})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.ts", Kind: "import", Name: "./foo", Signature: "import { foo } from './foo';", LineStart: 1})
}

func TestExtractSymbolsJavaScript(t *testing.T) {
	source := []byte("import { x } from './x';\nfunction top(a) { return a; }\nconst arrow = (a) => a * 2;\nclass C { m() { return 1; } }\n")
	got, err := ExtractSymbols(context.Background(), "javascript", "sample.js", source)
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.js", Kind: "function", Name: "top", Signature: "function top(a)", LineStart: 2})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.js", Kind: "function", Name: "arrow", Signature: "arrow = (a) => a * 2", LineStart: 3})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.js", Kind: "type", Name: "C", Signature: "class C"})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.js", Kind: "method", Name: "m", Receiver: "C"})
	assertHasSymbol(t, got, db.Symbol{FilePath: "sample.js", Kind: "import", Name: "./x", LineStart: 1})
}

func TestExtractSymbolsTSXUsesTSXGrammar(t *testing.T) {
	// DetectLanguage maps .tsx to "typescript"; the tsx grammar is what
	// tolerates the JSX element in the body.
	source := []byte("import React from 'react';\nexport function App() { return <div/>; }\n")
	got, err := ExtractSymbols(context.Background(), "typescript", "app.tsx", source)
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "app.tsx", Kind: "function", Name: "App", LineStart: 2})
	assertHasSymbol(t, got, db.Symbol{FilePath: "app.tsx", Kind: "import", Name: "react"})
}

func TestExtractSymbolsTypeScriptToleratesSyntaxError(t *testing.T) {
	// The vendored TS grammar's error recovery swallows the remainder of
	// the file into an ERROR node when a malformed declaration appears
	// first, so the valid symbol must precede the malformed one for it to
	// survive as a top-level declaration.
	source := []byte("function valid() { return 1; }\nfunction broken( {\n}\n")
	got, err := ExtractSymbols(context.Background(), "typescript", "broken.ts", source)
	if err != nil {
		t.Fatalf("ExtractSymbols: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "broken.ts", Kind: "function", Name: "valid"})
}
