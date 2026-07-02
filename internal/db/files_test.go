package db

import (
	"testing"
	"time"
)

func TestSaveAndGetFileIndex(t *testing.T) {
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

	indexedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	files := []FileIndex{
		{
			Path:          "main.go",
			Language:      "go",
			Hash:          "abc123",
			SizeBytes:     1234,
			LastIndexedAt: indexedAt,
		},
		{
			Path:          "README.md",
			Language:      "markdown",
			Hash:          "def456",
			SizeBytes:     567,
			LastIndexedAt: indexedAt,
		},
	}

	if err := db.SaveFileIndex(projectID, files); err != nil {
		t.Fatalf("SaveFileIndex failed: %v", err)
	}

	got, err := db.GetFileIndex(projectID)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}

	if len(got) != len(files) {
		t.Fatalf("expected %d files, got %d", len(files), len(got))
	}

	wantByPath := make(map[string]FileIndex, len(files))
	for _, f := range files {
		wantByPath[f.Path] = f
	}

	for _, gotFile := range got {
		wantFile, ok := wantByPath[gotFile.Path]
		if !ok {
			t.Errorf("unexpected file path: %s", gotFile.Path)
			continue
		}
		if gotFile.Language != wantFile.Language ||
			gotFile.Hash != wantFile.Hash ||
			gotFile.SizeBytes != wantFile.SizeBytes ||
			!gotFile.LastIndexedAt.Equal(wantFile.LastIndexedAt) {
			t.Errorf("file %s mismatch:\n got: %+v\nwant: %+v", gotFile.Path, gotFile, wantFile)
		}
	}
}

func TestSaveFileIndexUpdatesExisting(t *testing.T) {
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

	files := []FileIndex{
		{Path: "main.go", Hash: "v1", SizeBytes: 1, LastIndexedAt: time.Now().UTC()},
	}
	if err := db.SaveFileIndex(projectID, files); err != nil {
		t.Fatalf("SaveFileIndex failed: %v", err)
	}

	updated := []FileIndex{
		{Path: "main.go", Hash: "v2", SizeBytes: 2, LastIndexedAt: time.Now().UTC()},
	}
	if err := db.SaveFileIndex(projectID, updated); err != nil {
		t.Fatalf("SaveFileIndex update failed: %v", err)
	}

	got, err := db.GetFileIndex(projectID)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 file, got %d", len(got))
	}
	if got[0].Hash != "v2" || got[0].SizeBytes != 2 {
		t.Errorf("expected updated hash/size, got %+v", got[0])
	}
}
