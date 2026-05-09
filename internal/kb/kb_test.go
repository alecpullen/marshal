package kb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupTestDB creates a temporary SQLite database for testing.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	return db
}

// setupTestStore creates an IndexStore with a test database.
func setupTestStore(t *testing.T) *IndexStore {
	t.Helper()
	db := setupTestDB(t)
	store, err := NewIndexStore(db)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	return store
}

// TestIndexStore_PutAndGet tests storing and retrieving index entries.
func TestIndexStore_PutAndGet(t *testing.T) {
	store := setupTestStore(t)

	entry := &IndexEntry{
		FilePath:    "test.go",
		ContentHash: "abc123",
		Parser:      "go",
		Symbols: []Symbol{
			{
				Name:     "Hello",
				Kind:     SymbolFunction,
				Range:    LineRange{StartLine: 10, StartCol: 0, EndLine: 10, EndCol: 5},
				Exported: true,
				Language: "go",
			},
		},
		Imports:   []Import{{Path: "fmt", Line: 3}},
		IndexedAt: time.Now(),
	}

	// Store entry
	if err := store.Put(entry); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Retrieve entry
	retrieved, err := store.Get("test.go")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("expected entry, got nil")
	}

	if retrieved.FilePath != entry.FilePath {
		t.Errorf("FilePath: got %q, want %q", retrieved.FilePath, entry.FilePath)
	}

	if retrieved.ContentHash != entry.ContentHash {
		t.Errorf("ContentHash: got %q, want %q", retrieved.ContentHash, entry.ContentHash)
	}

	if len(retrieved.Symbols) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(retrieved.Symbols))
	}

	if retrieved.Symbols[0].Name != "Hello" {
		t.Errorf("Symbol name: got %q, want %q", retrieved.Symbols[0].Name, "Hello")
	}
}

// TestIndexStore_Remove tests removing entries.
func TestIndexStore_Remove(t *testing.T) {
	store := setupTestStore(t)

	entry := &IndexEntry{
		FilePath:    "test.go",
		ContentHash: "abc123",
		Parser:      "go",
		Symbols:     []Symbol{{Name: "Test", Kind: SymbolFunction, Language: "go"}},
		IndexedAt:   time.Now(),
	}

	if err := store.Put(entry); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if err := store.Remove("test.go"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	retrieved, err := store.Get("test.go")
	if err != nil {
		t.Fatalf("Get after remove failed: %v", err)
	}

	if retrieved != nil {
		t.Error("expected nil after remove")
	}
}

// TestIndexStore_Stats tests statistics retrieval.
func TestIndexStore_Stats(t *testing.T) {
	store := setupTestStore(t)

	// Add some entries
	for i := 0; i < 3; i++ {
		entry := &IndexEntry{
			FilePath:    fmt.Sprintf("test%d.go", i),
			ContentHash: fmt.Sprintf("hash%d", i),
			Parser:      "go",
			Symbols: []Symbol{
				{Name: "Func1", Kind: SymbolFunction, Language: "go"},
				{Name: "Func2", Kind: SymbolFunction, Language: "go"},
			},
			IndexedAt: time.Now(),
		}
		if err := store.Put(entry); err != nil {
			t.Fatalf("Put failed: %v", err)
		}
	}

	totalFiles, totalSymbols, _, err := store.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	if totalFiles != 3 {
		t.Errorf("TotalFiles: got %d, want 3", totalFiles)
	}

	if totalSymbols != 6 {
		t.Errorf("TotalSymbols: got %d, want 6", totalSymbols)
	}
}

// TestGoParser_Parse tests Go file parsing.
func TestGoParser_Parse(t *testing.T) {
	parser := &GoParser{}
	
	content := []byte(`package main

import (
	"fmt"
	"strings"
)

// Hello greets someone
func Hello(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}

type Person struct {
	Name string
	Age  int
}

func (p *Person) Greet() string {
	return Hello(p.Name)
}

func main() {
	p := &Person{Name: "World"}
	fmt.Println(p.Greet())
}
`)

	parsed, err := parser.Parse(content, "test.go")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Check we found the function
	foundHello := false
	foundGreet := false
	foundPerson := false

	for _, sym := range parsed.Symbols {
		switch sym.Name {
		case "Hello":
			foundHello = true
			if sym.Kind != SymbolFunction {
				t.Errorf("Hello kind: got %s, want function", sym.Kind)
			}
			if !sym.Exported {
				t.Error("Hello should be exported")
			}
		case "Greet":
			foundGreet = true
			if sym.Kind != SymbolMethod {
				t.Errorf("Greet kind: got %s, want method", sym.Kind)
			}
			// Note: Parent relationship detection is best-effort in Phase 3.75
			// Full parent resolution requires cross-file type analysis
		case "Person":
			foundPerson = true
			if sym.Kind != SymbolType && sym.Kind != SymbolStruct {
				t.Errorf("Person kind: got %s, want type/struct", sym.Kind)
			}
		}
	}

	if !foundHello {
		t.Error("did not find Hello function")
	}
	if !foundGreet {
		t.Error("did not find Greet method")
	}
	if !foundPerson {
		t.Error("did not find Person type")
	}

	// Check imports
	if len(parsed.Imports) < 1 {
		t.Errorf("expected at least 1 import, got %d", len(parsed.Imports))
	}
}

// TestQuery_Lookup tests symbol lookup.
func TestQuery_Lookup(t *testing.T) {
	store := setupTestStore(t)
	query := NewQuery(store)

	// Add entries
	entry1 := &IndexEntry{
		FilePath:    "api.go",
		ContentHash: "hash1",
		Parser:      "go",
		Symbols: []Symbol{
			{Name: "Middleware", Kind: SymbolFunction, Exported: true, Language: "go", Range: LineRange{StartLine: 10}},
			{Name: "Handle", Kind: SymbolFunction, Exported: true, Language: "go", Range: LineRange{StartLine: 20}},
		},
		IndexedAt: time.Now(),
	}
	entry2 := &IndexEntry{
		FilePath:    "utils.go",
		ContentHash: "hash2",
		Parser:      "go",
		Symbols: []Symbol{
			{Name: "Middleware", Kind: SymbolType, Exported: true, Language: "go", Range: LineRange{StartLine: 5}},
		},
		IndexedAt: time.Now(),
	}

	if err := store.Put(entry1); err != nil {
		t.Fatalf("Put entry1 failed: %v", err)
	}
	if err := store.Put(entry2); err != nil {
		t.Fatalf("Put entry2 failed: %v", err)
	}

	// Test exact lookup
	results, err := query.Lookup("Middleware", "")
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}

	// Test scoped lookup
	results, err = query.Lookup("Middleware", "api")
	if err != nil {
		t.Fatalf("Lookup with scope failed: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("expected 1 result for scoped lookup, got %d", len(results))
	}

	if results[0].FilePath != "api.go" {
		t.Errorf("scoped lookup returned wrong file: %s", results[0].FilePath)
	}
}

// TestQuery_FileSymbols tests retrieving symbols from a file.
func TestQuery_FileSymbols(t *testing.T) {
	store := setupTestStore(t)
	query := NewQuery(store)

	entry := &IndexEntry{
		FilePath:    "test.go",
		ContentHash: "hash1",
		Parser:      "go",
		Symbols: []Symbol{
			{Name: "Func1", Kind: SymbolFunction, Language: "go"},
			{Name: "Func2", Kind: SymbolFunction, Language: "go"},
			{Name: "MyType", Kind: SymbolType, Language: "go"},
		},
		IndexedAt: time.Now(),
	}

	if err := store.Put(entry); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	symbols, err := query.FileSymbols("test.go")
	if err != nil {
		t.Fatalf("FileSymbols failed: %v", err)
	}

	if len(symbols) != 3 {
		t.Errorf("expected 3 symbols, got %d", len(symbols))
	}
}

// TestQuery_PackageExports tests listing package exports.
func TestQuery_PackageExports(t *testing.T) {
	store := setupTestStore(t)
	query := NewQuery(store)

	// Add entries in the same package
	entry1 := &IndexEntry{
		FilePath:    "kb/api.go",
		ContentHash: "hash1",
		Parser:      "go",
		Symbols: []Symbol{
			{Name: "Lookup", Kind: SymbolFunction, Exported: true, Language: "go"},
			{Name: "internalHelper", Kind: SymbolFunction, Exported: false, Language: "go"},
		},
		IndexedAt: time.Now(),
	}
	entry2 := &IndexEntry{
		FilePath:    "kb/store.go",
		ContentHash: "hash2",
		Parser:      "go",
		Symbols: []Symbol{
			{Name: "IndexStore", Kind: SymbolType, Exported: true, Language: "go"},
		},
		IndexedAt: time.Now(),
	}

	if err := store.Put(entry1); err != nil {
		t.Fatalf("Put entry1 failed: %v", err)
	}
	if err := store.Put(entry2); err != nil {
		t.Fatalf("Put entry2 failed: %v", err)
	}

	exports, err := query.PackageExports("kb/")
	if err != nil {
		t.Fatalf("PackageExports failed: %v", err)
	}

	if len(exports) != 2 {
		t.Errorf("expected 2 exports, got %d", len(exports))
	}

	// Verify we got the right symbols
	foundLookup := false
	foundStore := false
	for _, sym := range exports {
		if sym.Name == "Lookup" {
			foundLookup = true
		}
		if sym.Name == "IndexStore" {
			foundStore = true
		}
		if sym.Name == "internalHelper" {
			t.Error("internalHelper should not be exported")
		}
	}

	if !foundLookup {
		t.Error("did not find Lookup in exports")
	}
	if !foundStore {
		t.Error("did not find IndexStore in exports")
	}
}

// TestSymbolLookupTool_Invoke tests the kb_symbol_lookup tool.
func TestSymbolLookupTool_Invoke(t *testing.T) {
	store := setupTestStore(t)
	query := NewQuery(store)
	tool := NewSymbolLookupTool(query)

	// Add an entry
	entry := &IndexEntry{
		FilePath:    "api.go",
		ContentHash: "hash1",
		Parser:      "go",
		Symbols: []Symbol{
			{Name: "Middleware", Kind: SymbolFunction, Exported: true, Language: "go", Range: LineRange{StartLine: 42}},
		},
		IndexedAt: time.Now(),
	}
	if err := store.Put(entry); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Invoke tool
	args, _ := json.Marshal(map[string]string{
		"name": "Middleware",
	})

	result, err := tool.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("Invoke failed: %v", err)
	}

	if result == nil {
		t.Fatal("expected result, got nil")
	}

	// Check result contains expected content
	if !contains(result.Content, "Middleware") {
		t.Error("result should mention Middleware")
	}
	if !contains(result.Content, "api.go") {
		t.Error("result should mention api.go")
	}
	if !contains(result.Content, "Line: 42") {
		t.Error("result should mention Line: 42")
	}
}

// TestSymbolLookupTool_Invoke_NotFound tests lookup for non-existent symbol.
func TestSymbolLookupTool_Invoke_NotFound(t *testing.T) {
	store := setupTestStore(t)
	query := NewQuery(store)
	tool := NewSymbolLookupTool(query)

	args, _ := json.Marshal(map[string]string{
		"name": "NonExistent",
	})

	result, err := tool.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("Invoke failed: %v", err)
	}

	if result == nil {
		t.Fatal("expected result, got nil")
	}

	if !contains(result.Content, "No symbol") {
		t.Error("result should indicate no symbol found")
	}
}

// TestFileSymbolsTool_Invoke tests the kb_file_symbols tool.
func TestFileSymbolsTool_Invoke(t *testing.T) {
	store := setupTestStore(t)
	query := NewQuery(store)
	tool := NewFileSymbolsTool(query)

	entry := &IndexEntry{
		FilePath:    "test.go",
		ContentHash: "hash1",
		Parser:      "go",
		Symbols: []Symbol{
			{Name: "Func1", Kind: SymbolFunction, Language: "go", Range: LineRange{StartLine: 10}},
			{Name: "MyType", Kind: SymbolType, Language: "go", Range: LineRange{StartLine: 20}},
		},
		IndexedAt: time.Now(),
	}
	if err := store.Put(entry); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	args, _ := json.Marshal(map[string]string{
		"path": "test.go",
	})

	result, err := tool.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("Invoke failed: %v", err)
	}

	if !contains(result.Content, "Func1") {
		t.Error("result should mention Func1")
	}
	if !contains(result.Content, "MyType") {
		t.Error("result should mention MyType")
	}
}

// TestMaintainer_ReindexFile tests the file reindexing logic.
func TestMaintainer_ReindexFile(t *testing.T) {
	db := setupTestDB(t)
	store, err := NewIndexStore(db)
	if err != nil {
		t.Fatalf("NewIndexStore failed: %v", err)
	}

	parserReg := NewParserRegistry()
	
	// Create temp directory with a Go file
	tmpDir := t.TempDir()
	goFile := filepath.Join(tmpDir, "test.go")
	
	content := []byte(`package main

func Hello() string {
	return "Hello"
}
`)
	if err := os.WriteFile(goFile, content, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	maintainer, err := NewMaintainer(store, parserReg, tmpDir, nil)
	if err != nil {
		t.Fatalf("NewMaintainer failed: %v", err)
	}

	// Reindex the file
	if err := maintainer.reindexFile(goFile); err != nil {
		t.Fatalf("reindexFile failed: %v", err)
	}

	// Verify it was indexed
	entry, err := store.Get(goFile)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if entry == nil {
		t.Fatal("expected entry after reindex")
	}

	if entry.Parser != "go" {
		t.Errorf("parser: got %q, want go", entry.Parser)
	}

	// Check symbols
	foundHello := false
	for _, sym := range entry.Symbols {
		if sym.Name == "Hello" {
			foundHello = true
			if sym.Kind != SymbolFunction {
				t.Errorf("Hello kind: got %s, want function", sym.Kind)
			}
		}
	}

	if !foundHello {
		t.Error("did not find Hello function in indexed symbols")
	}
}

// TestMaintainer_SkipsUnchangedFile tests that unchanged files are skipped.
func TestMaintainer_SkipsUnchangedFile(t *testing.T) {
	db := setupTestDB(t)
	store, err := NewIndexStore(db)
	if err != nil {
		t.Fatalf("NewIndexStore failed: %v", err)
	}

	parserReg := NewParserRegistry()
	
	tmpDir := t.TempDir()
	goFile := filepath.Join(tmpDir, "test.go")
	
	content := []byte(`package main
func Hello() string { return "Hello" }
`)
	if err := os.WriteFile(goFile, content, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	maintainer, err := NewMaintainer(store, parserReg, tmpDir, nil)
	if err != nil {
		t.Fatalf("NewMaintainer failed: %v", err)
	}

	// First index
	if err := maintainer.reindexFile(goFile); err != nil {
		t.Fatalf("first reindexFile failed: %v", err)
	}

	firstEntry, _ := store.Get(goFile)
	if firstEntry == nil {
		t.Fatal("expected entry after first index")
	}
	firstHash := firstEntry.ContentHash

	// Second index (unchanged content)
	if err := maintainer.reindexFile(goFile); err != nil {
		t.Fatalf("second reindexFile failed: %v", err)
	}

	secondEntry, _ := store.Get(goFile)
	if secondEntry == nil {
		t.Fatal("expected entry after second index")
	}

	// Hash should be the same
	if secondEntry.ContentHash != firstHash {
		t.Error("hash changed for unchanged file - should have been skipped")
	}
}

// TestParserRegistry tests the language parser registry.
func TestParserRegistry(t *testing.T) {
	reg := NewParserRegistry()

	// Test Go
	goParser := reg.GetParser("test.go")
	if goParser == nil {
		t.Error("expected Go parser for .go file")
	} else if goParser.Name() != "go" {
		t.Errorf("Go parser name: got %q, want go", goParser.Name())
	}

	// Test TypeScript
	tsParser := reg.GetParser("test.ts")
	if tsParser == nil {
		t.Error("expected TypeScript parser for .ts file")
	}

	// Test unsupported
	noParser := reg.GetParser("test.unknown")
	if noParser != nil {
		t.Error("expected nil parser for unknown extension")
	}

	// Test language names
	if reg.GetLanguage("test.go") != "go" {
		t.Error("GetLanguage for .go should return 'go'")
	}
}

// TestSymbolKind_IsValid tests symbol kind validation.
func TestSymbolKind_IsValid(t *testing.T) {
	tests := []struct {
		kind  SymbolKind
		valid bool
	}{
		{SymbolFunction, true},
		{SymbolMethod, true},
		{SymbolType, true},
		{SymbolKind("unknown"), false},
		{SymbolKind(""), false},
	}

	for _, tt := range tests {
		if got := tt.kind.IsValid(); got != tt.valid {
			t.Errorf("IsValid(%q): got %v, want %v", tt.kind, got, tt.valid)
		}
	}
}

// TestAllTools tests that AllTools returns the expected tools.
func TestAllTools(t *testing.T) {
	store := setupTestStore(t)
	query := NewQuery(store)

	tools := AllTools(query)

	if len(tools) != 5 {
		t.Errorf("expected 5 tools, got %d", len(tools))
	}

	// Check tool names
	expectedNames := map[string]bool{
		"kb_symbol_lookup":     false,
		"kb_symbol_references": false,
		"kb_file_symbols":      false,
		"kb_package_exports":   false,
		"kb_project_map":       false,
	}

	for _, tool := range tools {
		name := tool.Name()
		if _, ok := expectedNames[name]; !ok {
			t.Errorf("unexpected tool: %s", name)
		}
		expectedNames[name] = true
	}

	for name, found := range expectedNames {
		if !found {
			t.Errorf("missing tool: %s", name)
		}
	}
}

// Helper functions

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && 
		(s == substr || len(s) > len(substr) && 
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
		 findInString(s, substr)))
}

func findInString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Benchmarks

// BenchmarkQuery_Lookup benchmarks symbol lookup performance.
func BenchmarkQuery_Lookup(b *testing.B) {
	db, _ := sql.Open("sqlite", ":memory:")
	store, _ := NewIndexStore(db)
	query := NewQuery(store)

	// Populate with test data
	for i := 0; i < 100; i++ {
		entry := &IndexEntry{
			FilePath:    fmt.Sprintf("file%d.go", i),
			ContentHash: fmt.Sprintf("hash%d", i),
			Parser:      "go",
			Symbols: []Symbol{
				{Name: fmt.Sprintf("Func%d", i), Kind: SymbolFunction, Language: "go"},
			},
			IndexedAt: time.Now(),
		}
		store.Put(entry)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		query.Lookup("Func50", "")
	}
}