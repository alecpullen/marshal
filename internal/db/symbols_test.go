package db

import "testing"

func TestSaveAndGetSymbols(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	symbols := []Symbol{
		{FilePath: "scanner.go", Kind: "function", Name: "NewScanner", Signature: "func NewScanner(root string) *Scanner", LineStart: 3, LineEnd: 5},
		{FilePath: "scanner.go", Kind: "method", Name: "Scan", Receiver: "*Scanner", Signature: "func (s *Scanner) Scan() ([]string, error)", LineStart: 7, LineEnd: 9},
	}
	if err := db.SaveSymbols(projectID, symbols); err != nil {
		t.Fatalf("SaveSymbols failed: %v", err)
	}

	got, err := db.GetSymbols(projectID)
	if err != nil {
		t.Fatalf("GetSymbols failed: %v", err)
	}
	if len(got) != len(symbols) {
		t.Fatalf("expected %d symbols, got %d", len(symbols), len(got))
	}
	if got[0].Name != "NewScanner" || got[1].Name != "Scan" {
		t.Fatalf("expected symbols ordered by line_start, got %+v", got)
	}
	if got[1].Receiver != "*Scanner" {
		t.Errorf("expected receiver *Scanner, got %q", got[1].Receiver)
	}
}

func TestSaveSymbolsReplacesExisting(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	if err := db.SaveSymbols(projectID, []Symbol{
		{FilePath: "a.go", Kind: "function", Name: "Old", Signature: "func Old()", LineStart: 1, LineEnd: 1},
	}); err != nil {
		t.Fatalf("SaveSymbols failed: %v", err)
	}
	if err := db.SaveSymbols(projectID, []Symbol{
		{FilePath: "b.go", Kind: "function", Name: "New", Signature: "func New()", LineStart: 1, LineEnd: 1},
	}); err != nil {
		t.Fatalf("SaveSymbols replace failed: %v", err)
	}

	got, err := db.GetSymbols(projectID)
	if err != nil {
		t.Fatalf("GetSymbols failed: %v", err)
	}
	if len(got) != 1 || got[0].Name != "New" {
		t.Fatalf("expected only New symbol after replace, got %+v", got)
	}
}
