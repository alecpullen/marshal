package kb

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"io/fs"

	_ "modernc.org/sqlite"
)

// TestLive_IndexMarshalRepo tests indexing the marshal repository itself.
// This verifies the KB works on real production code.
func TestLive_IndexMarshalRepo(t *testing.T) {
	// Get repo root
	repoRoot := findRepoRoot(t)
	
	// Setup
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	
	store, err := NewIndexStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	
	parserReg := NewParserRegistry()
	
	// Create maintainer and bootstrap
	maintainer, err := NewMaintainer(store, parserReg, repoRoot, nil)
	if err != nil {
		t.Fatalf("new maintainer: %v", err)
	}
	
	// Index (limit to internal/kb/ for speed)
	kbDir := filepath.Join(repoRoot, "internal", "kb")
	files, err := findSourceFiles(kbDir, parserReg.SupportedExtensions())
	if err != nil {
		t.Fatalf("find files: %v", err)
	}
	
	t.Logf("Found %d source files in %s", len(files), kbDir)
	
	for _, file := range files {
		if err := maintainer.reindexFile(file); err != nil {
			t.Logf("Failed to index %s: %v", file, err)
		}
	}
	
	// Verify stats
	totalFiles, totalSymbols, _, err := store.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	
	t.Logf("Indexed: %d files, %d symbols", totalFiles, totalSymbols)
	
	if totalFiles == 0 {
		t.Error("expected some files to be indexed")
	}
	
	if totalSymbols == 0 {
		t.Error("expected some symbols to be indexed")
	}
	
	// Test queries
	query := NewQuery(store)
	
	// Look up SymbolIndex (should be in index.go)
	results, err := query.Lookup("SymbolIndex", "")
	if err != nil {
		t.Errorf("lookup SymbolIndex: %v", err)
	} else {
		t.Logf("Found %d results for 'SymbolIndex'", len(results))
		for _, r := range results {
			t.Logf("  - %s in %s line %d", r.Symbol.Name, r.FilePath, r.Symbol.Range.StartLine)
		}
	}
	
	// Look up LanguageParser interface
	results, err = query.Lookup("LanguageParser", "")
	if err != nil {
		t.Errorf("lookup LanguageParser: %v", err)
	} else {
		t.Logf("Found %d results for 'LanguageParser'", len(results))
	}
	
	// Test file symbols
	indexFile := filepath.Join(kbDir, "index.go")
	symbols, err := query.FileSymbols(indexFile)
	if err != nil {
		t.Errorf("file symbols: %v", err)
	} else {
		t.Logf("%s has %d symbols", indexFile, len(symbols))
		for _, sym := range symbols[:min(5, len(symbols))] {
			t.Logf("  - %s (%s) line %d", sym.Name, sym.Kind, sym.Range.StartLine)
		}
	}
	
	// Verify we found expected symbols
	foundSymbolKind := false
	foundIndexStore := false
	
	for _, sym := range symbols {
		if sym.Name == "SymbolKind" {
			foundSymbolKind = true
		}
		if sym.Name == "IndexStore" {
			foundIndexStore = true
		}
	}
	
	if !foundSymbolKind {
		t.Error("expected to find SymbolKind type in index.go")
	}
	if !foundIndexStore {
		t.Error("expected to find IndexStore type in index.go")
	}
}

// findRepoRoot finds the repository root by looking for go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	
	// Walk up until we find go.mod
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root with go.mod")
		}
		dir = parent
	}
}

// findSourceFiles finds all source files with given extensions.
func findSourceFiles(root string, extensions []string) ([]string, error) {
	var files []string
	
	extMap := make(map[string]bool)
	for _, ext := range extensions {
		extMap[ext] = true
	}
	
	err := filepath.Walk(root, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if info.IsDir() {
			return nil
		}
		
		ext := filepath.Ext(path)
		if extMap[ext] {
			files = append(files, path)
		}
		
		return nil
	})
	
	return files, err
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}