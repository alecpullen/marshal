package db

import (
	"testing"
	"time"
)

func TestCountFilesAndSymbols(t *testing.T) {
	d := newTestDB(t)
	projectID := mustCreateProject(t, d, "/repo")

	if err := d.SaveFileIndex(projectID, []FileIndex{
		{Path: "a.go", Language: "go", Hash: "h1"},
		{Path: "b.go", Language: "go", Hash: "h2"},
		{Path: "c.md", Language: "markdown", Hash: "h3"},
	}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}
	if err := d.SaveSymbols(projectID, []Symbol{
		{FilePath: "a.go", Name: "Foo", Kind: "func"},
		{FilePath: "a.go", Name: "Bar", Kind: "func"},
	}); err != nil {
		t.Fatalf("SaveSymbols: %v", err)
	}

	files, err := d.CountFiles(projectID)
	if err != nil {
		t.Fatalf("CountFiles: %v", err)
	}
	if files != 3 {
		t.Errorf("CountFiles = %d, want 3", files)
	}

	syms, err := d.CountSymbols(projectID)
	if err != nil {
		t.Fatalf("CountSymbols: %v", err)
	}
	if syms != 2 {
		t.Errorf("CountSymbols = %d, want 2", syms)
	}
}

func TestCountsEmptyProject(t *testing.T) {
	d := newTestDB(t)
	projectID := mustCreateProject(t, d, "/repo")

	files, err := d.CountFiles(projectID)
	if err != nil || files != 0 {
		t.Errorf("CountFiles = (%d, %v), want (0, nil)", files, err)
	}
	syms, err := d.CountSymbols(projectID)
	if err != nil || syms != 0 {
		t.Errorf("CountSymbols = (%d, %v), want (0, nil)", syms, err)
	}
}

func TestCountEmbeddedChunks(t *testing.T) {
	d := newTestDB(t)
	projectID := mustCreateProject(t, d, "/repo")

	if n, err := d.CountEmbeddedChunks(projectID); err != nil || n != 0 {
		t.Fatalf("empty: CountEmbeddedChunks = %d, %v; want 0, nil", n, err)
	}

	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	// Two chunks; only chunk 1 gets an embedding.
	for _, id := range []int{1, 2} {
		if _, err := d.sqlDB.Exec(
			`INSERT INTO chunks (id, project_id, file_path, file_hash, kind, start_line, end_line, content, token_count, created_at)
			 VALUES (?, ?, 'a.go', 'h1', 'func', 1, 3, 'package a', 2, ?)`, id, projectID, now); err != nil {
			t.Fatalf("insert chunk %d: %v", id, err)
		}
	}
	if _, err := d.sqlDB.Exec(
		`INSERT INTO embeddings (chunk_id, model, dim, vector) VALUES (1, 'test-embed', 4, ?)`, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("insert embedding: %v", err)
	}

	if n, err := d.CountEmbeddedChunks(projectID); err != nil || n != 1 {
		t.Fatalf("CountEmbeddedChunks = %d, %v; want 1, nil", n, err)
	}
}

func TestLatestIndexedAt(t *testing.T) {
	d := newTestDB(t)
	projectID := mustCreateProject(t, d, "/repo")

	if ts, err := d.LatestIndexedAt(projectID); err != nil || !ts.IsZero() {
		t.Fatalf("empty: LatestIndexedAt = %v, %v; want zero time, nil", ts, err)
	}

	indexedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := d.SaveFileIndex(projectID, []FileIndex{
		{Path: "a.go", Language: "go", Hash: "h1", LastIndexedAt: indexedAt},
	}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}
	if ts, err := d.LatestIndexedAt(projectID); err != nil || !ts.Equal(indexedAt) {
		t.Fatalf("LatestIndexedAt = %v, %v; want %v", ts, err, indexedAt)
	}
}
