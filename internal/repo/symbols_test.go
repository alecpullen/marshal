package repo

import (
	"testing"

	"marshal/internal/db"
)

func TestExtractSymbolsFunctions(t *testing.T) {
	source := []byte(`package foo

func NewScanner(root string) *Scanner {
	return &Scanner{root: root}
}
`)
	got, err := ExtractSymbols("scanner.go", source)
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
	got, err := ExtractSymbols("scanner.go", source)
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
	got, err := ExtractSymbols("broken.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "broken.go", Kind: "function", Name: "Valid"})
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
