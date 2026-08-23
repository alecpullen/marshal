package repo

import (
	"context"
	"testing"

	"marshal/internal/db"
)

func TestExtractSymbolsFunctions(t *testing.T) {
	source := []byte(`package foo

func NewScanner(root string) *Scanner {
	return &Scanner{root: root}
}
`)
	got, err := ExtractSymbols(context.Background(), "go", "scanner.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{
		FilePath:  "scanner.go",
		Kind:      "function",
		Name:      "NewScanner",
		Signature: "func NewScanner(root string) *Scanner",
		LineStart: 3,
		LineEnd:   5,
	})
}

func TestExtractSymbolsMethods(t *testing.T) {
	source := []byte(`package foo

func (s *Scanner) Scan() ([]string, error) {
	return nil, nil
}

func (s Scanner) Value() int {
	return 0
}
`)
	got, err := ExtractSymbols(context.Background(), "go", "scanner.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{
		FilePath:  "scanner.go",
		Kind:      "method",
		Name:      "Scan",
		Receiver:  "*Scanner",
		Signature: "func (s *Scanner) Scan() ([]string, error)",
		LineStart: 3,
		LineEnd:   5,
	})
	assertHasSymbol(t, got, db.Symbol{
		FilePath:  "scanner.go",
		Kind:      "method",
		Name:      "Value",
		Receiver:  "Scanner",
		Signature: "func (s Scanner) Value() int",
		LineStart: 7,
		LineEnd:   9,
	})
}

func TestExtractSymbolsToleratesSyntaxError(t *testing.T) {
	source := []byte(`package foo

func Broken( {
	// missing closing paren above; deliberately malformed
}

func Valid() int {
	return 1
}
`)
	got, err := ExtractSymbols(context.Background(), "go", "broken.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "broken.go", Kind: "function", Name: "Valid"})
}

func TestExtractSymbolsTypes(t *testing.T) {
	source := []byte(`package foo

type Scanner struct {
	root string
}

type Matcher interface {
	Match(s string) bool
}

type ID int
`)
	got, err := ExtractSymbols(context.Background(), "go", "types.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "types.go", Kind: "type", Name: "Scanner", Signature: "type Scanner struct", LineStart: 3, LineEnd: 5})
	assertHasSymbol(t, got, db.Symbol{FilePath: "types.go", Kind: "type", Name: "Matcher", Signature: "type Matcher interface", LineStart: 7, LineEnd: 9})
	assertHasSymbol(t, got, db.Symbol{FilePath: "types.go", Kind: "type", Name: "ID", Signature: "type ID int", LineStart: 11, LineEnd: 11})
}

func TestExtractSymbolsGroupedTypeBlock(t *testing.T) {
	source := []byte(`package foo

type (
	Foo int
	Bar string
)
`)
	got, err := ExtractSymbols(context.Background(), "go", "types.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "types.go", Kind: "type", Name: "Foo", Signature: "type Foo int", LineStart: 4, LineEnd: 4})
	assertHasSymbol(t, got, db.Symbol{FilePath: "types.go", Kind: "type", Name: "Bar", Signature: "type Bar string", LineStart: 5, LineEnd: 5})
}

func TestExtractSymbolsTypeAliases(t *testing.T) {
	source := []byte(`package foo

type Alias = int

type Another = string
`)
	got, err := ExtractSymbols(context.Background(), "go", "aliases.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "aliases.go", Kind: "type", Name: "Alias", Signature: "type Alias int", LineStart: 3, LineEnd: 3})
	assertHasSymbol(t, got, db.Symbol{FilePath: "aliases.go", Kind: "type", Name: "Another", Signature: "type Another string", LineStart: 5, LineEnd: 5})
}

func TestExtractSymbolsGroupedTypeAliases(t *testing.T) {
	source := []byte(`package foo

type (
	RegularType int
	AliasType = string
)
`)
	got, err := ExtractSymbols(context.Background(), "go", "types.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "types.go", Kind: "type", Name: "RegularType", Signature: "type RegularType int", LineStart: 4, LineEnd: 4})
	assertHasSymbol(t, got, db.Symbol{FilePath: "types.go", Kind: "type", Name: "AliasType", Signature: "type AliasType string", LineStart: 5, LineEnd: 5})
}

func TestExtractSymbolsImportsSingle(t *testing.T) {
	source := []byte(`package foo

import "fmt"
`)
	got, err := ExtractSymbols(context.Background(), "go", "imports.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "imports.go", Kind: "import", Name: "fmt", Signature: `"fmt"`, LineStart: 3, LineEnd: 3})
}

func TestExtractSymbolsImportsGroupedWithAlias(t *testing.T) {
	source := []byte(`package foo

import (
	"fmt"
	bar "example.com/bar"
)
`)
	got, err := ExtractSymbols(context.Background(), "go", "imports.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "imports.go", Kind: "import", Name: "fmt", Signature: `"fmt"`, LineStart: 4, LineEnd: 4})
	assertHasSymbol(t, got, db.Symbol{FilePath: "imports.go", Kind: "import", Name: "example.com/bar", Signature: `bar "example.com/bar"`, LineStart: 5, LineEnd: 5})
}

func TestExtractSymbolsUnsupportedLanguageReturnsNil(t *testing.T) {
	got, err := ExtractSymbols(context.Background(), "ruby", "x.rb", []byte("def foo; end\n"))
	if err != nil || got != nil {
		t.Fatalf("ExtractSymbols(ruby) = %v, %v; want nil, nil", got, err)
	}
	if SupportedLanguage("ruby") {
		t.Fatal("SupportedLanguage(ruby) = true, want false")
	}
	for _, lang := range []string{"go", "javascript", "typescript", "python", "rust"} {
		if !SupportedLanguage(lang) {
			t.Fatalf("SupportedLanguage(%q) = false, want true", lang)
		}
	}
}

// assertHasSymbol fails the test unless got contains a symbol matching
// want's Name and Kind. Fields left at their zero value on want are not
// checked, so callers can assert only the fields relevant to a test.
func assertHasSymbol(t *testing.T, got []db.Symbol, want db.Symbol) {
	t.Helper()
	for _, s := range got {
		if s.Name != want.Name || s.Kind != want.Kind {
			continue
		}
		if want.FilePath != "" && s.FilePath != want.FilePath {
			t.Errorf("symbol %s: FilePath = %q, want %q", s.Name, s.FilePath, want.FilePath)
		}
		if s.Receiver != want.Receiver {
			t.Errorf("symbol %s: Receiver = %q, want %q", s.Name, s.Receiver, want.Receiver)
		}
		if want.Signature != "" && s.Signature != want.Signature {
			t.Errorf("symbol %s: Signature = %q, want %q", s.Name, s.Signature, want.Signature)
		}
		if want.LineStart != 0 && s.LineStart != want.LineStart {
			t.Errorf("symbol %s: LineStart = %d, want %d", s.Name, s.LineStart, want.LineStart)
		}
		if want.LineEnd != 0 && s.LineEnd != want.LineEnd {
			t.Errorf("symbol %s: LineEnd = %d, want %d", s.Name, s.LineEnd, want.LineEnd)
		}
		return
	}
	t.Fatalf("expected symbol %s (%s) not found in %+v", want.Name, want.Kind, got)
}
