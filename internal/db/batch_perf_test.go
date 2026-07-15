package db

import (
	"fmt"
	"testing"
	"time"
)

// maxBatchDuration is the regression-guard threshold for batch inserts of
// 1000 rows on an :memory: database. Under -race and CI this can take
// 140-160ms; 1s is a meaningful regression guard.
const maxBatchDuration = 1 * time.Second

func TestSaveSymbolsBatchPerformance(t *testing.T) {
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

	n := 1000
	symbols := make([]Symbol, n)
	for i := 0; i < n; i++ {
		symbols[i] = Symbol{
			FilePath:  fmt.Sprintf("pkg/file%d.go", i/10),
			Kind:      "function",
			Name:      fmt.Sprintf("Func%d", i),
			Signature: fmt.Sprintf("func Func%d() error", i),
			LineStart: i + 1,
			LineEnd:   i + 2,
		}
	}

	start := time.Now()
	if err := db.SaveSymbols(projectID, symbols); err != nil {
		t.Fatalf("SaveSymbols failed: %v", err)
	}
	elapsed := time.Since(start)

	// Verify correctness: all 1000 rows are retrievable.
	got, err := db.GetSymbols(projectID, 0)
	if err != nil {
		t.Fatalf("GetSymbols failed: %v", err)
	}
	if len(got) != n {
		t.Fatalf("expected %d symbols, got %d", n, len(got))
	}
	if got[0].Name != "Func0" || got[n-1].Name != fmt.Sprintf("Func%d", n-1) {
		t.Errorf("symbol order or naming incorrect: first=%q last=%q", got[0].Name, got[n-1].Name)
	}

	// Assert that batch insertion completes quickly on :memory:.
	// This is a regression guard rather than a precise benchmark.
	if elapsed > maxBatchDuration {
		t.Errorf("SaveSymbols for %d rows took %v, expected under %v", n, elapsed, maxBatchDuration)
	}
}

func TestSaveFileIndexBatchPerformance(t *testing.T) {
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

	n := 1000
	now := time.Now().UTC()
	files := make([]FileIndex, n)
	for i := 0; i < n; i++ {
		files[i] = FileIndex{
			Path:          fmt.Sprintf("pkg/file%d.go", i),
			Language:      "go",
			Hash:          fmt.Sprintf("hash%08x", i),
			SizeBytes:     int64(100 + i),
			LastIndexedAt: now,
		}
	}

	start := time.Now()
	if err := db.SaveFileIndex(projectID, files); err != nil {
		t.Fatalf("SaveFileIndex failed: %v", err)
	}
	elapsed := time.Since(start)

	// Verify correctness: all 1000 rows are retrievable.
	got, err := db.GetFileIndex(projectID, 0)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(got) != n {
		t.Fatalf("expected %d files, got %d", n, len(got))
	}
	if got[0].Path != "pkg/file0.go" || got[n-1].Path != fmt.Sprintf("pkg/file%d.go", n-1) {
		t.Errorf("file order or naming incorrect: first=%q last=%q", got[0].Path, got[n-1].Path)
	}

	// Assert that batch insertion completes quickly on :memory:.
	// This is a regression guard rather than a precise benchmark.
	if elapsed > maxBatchDuration {
		t.Errorf("SaveFileIndex for %d rows took %v, expected under %v", n, elapsed, maxBatchDuration)
	}
}
