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

	for i := range files {
		if got[i].Path != files[i].Path ||
			got[i].Language != files[i].Language ||
			got[i].Hash != files[i].Hash ||
			got[i].SizeBytes != files[i].SizeBytes ||
			!got[i].LastIndexedAt.Equal(files[i].LastIndexedAt) {
			t.Errorf("file %d mismatch:\n got: %+v\nwant: %+v", i, got[i], files[i])
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
