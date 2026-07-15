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

func TestFindSymbolsFiltersByNameAndKind(t *testing.T) {
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
		{FilePath: "scanner.go", Kind: "function", Name: "NewScanner", LineStart: 3, LineEnd: 5, Signature: "func NewScanner() *Scanner"},
		{FilePath: "scanner.go", Kind: "method", Name: "Scan", Receiver: "*Scanner", LineStart: 7, LineEnd: 9, Signature: "func (s *Scanner) Scan()"},
		{FilePath: "scanner.go", Kind: "type", Name: "Scanner", LineStart: 1, LineEnd: 1, Signature: "type Scanner struct"},
		{FilePath: "card.go", Kind: "function", Name: "RenderCard", LineStart: 1, LineEnd: 3, Signature: "func RenderCard() string"},
	}); err != nil {
		t.Fatalf("SaveSymbols failed: %v", err)
	}

	// "scan" matches NewScanner, Scan, and Scanner (all contain "scan" as a
	// case-insensitive substring); RenderCard does not.
	got, err := db.FindSymbols(projectID, "scan", "", 0)
	if err != nil {
		t.Fatalf("FindSymbols failed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 name matches, got %d: %+v", len(got), got)
	}

	got, err = db.FindSymbols(projectID, "", "type", 0)
	if err != nil {
		t.Fatalf("FindSymbols kind failed: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Scanner" {
		t.Fatalf("expected 1 type match, got %+v", got)
	}

	got, err = db.FindSymbols(projectID, "", "", 0)
	if err != nil {
		t.Fatalf("FindSymbols no filter failed: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected all 4 symbols with no filter, got %d", len(got))
	}
}

func TestFindSymbolsEscapesWildcards(t *testing.T) {
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
		{FilePath: "a.go", Kind: "function", Name: "foo_bar", Signature: "func foo_bar()", LineStart: 1, LineEnd: 1},
		{FilePath: "b.go", Kind: "function", Name: "fooXbar", Signature: "func fooXbar()", LineStart: 1, LineEnd: 1},
	}); err != nil {
		t.Fatalf("SaveSymbols failed: %v", err)
	}

	// Without wildcard escaping, "_" matches any single character so both
	// "foo_bar" and "fooXbar" would match the pattern "%foo_bar%". With
	// escapeLike the underscore is escaped, so only "foo_bar" matches.
	got, err := db.FindSymbols(projectID, "foo_bar", "", 10)
	if err != nil {
		t.Fatalf("FindSymbols failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 symbol matching literal 'foo_bar', got %d: %+v", len(got), got)
	}
	if got[0].Name != "foo_bar" {
		t.Errorf("expected foo_bar, got %q", got[0].Name)
	}
}

func TestFindSymbolsLimitDefaultsAndClamps(t *testing.T) {
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

	symbols := make([]Symbol, 0, 60)
	for i := 0; i < 60; i++ {
		symbols = append(symbols, Symbol{
			FilePath: "a.go", Kind: "function", Name: "F", LineStart: i + 1, LineEnd: i + 1, Signature: "func F()",
		})
	}
	if err := db.SaveSymbols(projectID, symbols); err != nil {
		t.Fatalf("SaveSymbols failed: %v", err)
	}

	got, err := db.FindSymbols(projectID, "", "", 0)
	if err != nil {
		t.Fatalf("FindSymbols default limit failed: %v", err)
	}
	if len(got) != 50 {
		t.Fatalf("expected default limit of 50, got %d", len(got))
	}

	got, err = db.FindSymbols(projectID, "", "", 1000)
	if err != nil {
		t.Fatalf("FindSymbols clamp failed: %v", err)
	}
	if len(got) != 60 {
		t.Fatalf("expected all 60 symbols under clamp of 200, got %d", len(got))
	}
}
