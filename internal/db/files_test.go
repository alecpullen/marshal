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

func TestSaveFileIndexPreservesSummaryWhenHashUnchanged(t *testing.T) {
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
	if err := db.UpdateFileSummary(projectID, "main.go", "Entry point"); err != nil {
		t.Fatalf("UpdateFileSummary failed: %v", err)
	}

	if err := db.SaveFileIndex(projectID, files); err != nil {
		t.Fatalf("second SaveFileIndex failed: %v", err)
	}

	got, err := db.GetFileIndex(projectID)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(got) != 1 || got[0].Summary != "Entry point" {
		t.Fatalf("expected summary preserved, got %#v", got)
	}
}

func TestSaveFileIndexClearsSummaryWhenHashChanges(t *testing.T) {
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

	if err := db.SaveFileIndex(projectID, []FileIndex{
		{Path: "main.go", Hash: "v1", SizeBytes: 1, LastIndexedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("SaveFileIndex failed: %v", err)
	}
	if err := db.UpdateFileSummary(projectID, "main.go", "Entry point"); err != nil {
		t.Fatalf("UpdateFileSummary failed: %v", err)
	}

	if err := db.SaveFileIndex(projectID, []FileIndex{
		{Path: "main.go", Hash: "v2", SizeBytes: 2, LastIndexedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("second SaveFileIndex failed: %v", err)
	}

	got, err := db.GetFileIndex(projectID)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(got) != 1 || got[0].Summary != "" {
		t.Fatalf("expected summary cleared after hash change, got %#v", got)
	}
}

func TestFilesMatchingBasename(t *testing.T) {
	database, projectID := openMetricsTestDB(t)

	indexedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	files := []FileIndex{
		{Path: "src/main.go", Language: "go", Hash: "a", SizeBytes: 1, LastIndexedAt: indexedAt},
		{Path: "src/db/main_test.go", Language: "go", Hash: "b", SizeBytes: 2, LastIndexedAt: indexedAt},
		{Path: "README.md", Language: "markdown", Hash: "c", SizeBytes: 3, LastIndexedAt: indexedAt},
		{Path: "internal/db/files.go", Language: "go", Hash: "d", SizeBytes: 4, LastIndexedAt: indexedAt},
		{Path: "cmd/server/main.go", Language: "go", Hash: "e", SizeBytes: 5, LastIndexedAt: indexedAt},
	}
	if err := database.SaveFileIndex(projectID, files); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}

	got, err := database.FilesMatchingBasename(projectID, "main.go", 5)
	if err != nil {
		t.Fatalf("FilesMatchingBasename: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 matches, got %d: %v", len(got), got)
	}

	if got[0] != "src/main.go" {
		t.Errorf("first result should be shortest match first, got %q", got[0])
	}
	if got[1] != "cmd/server/main.go" {
		t.Errorf("second result = %q, want cmd/server/main.go", got[1])
	}
}

func TestFilesMatchingBasenameNoResults(t *testing.T) {
	database, projectID := openMetricsTestDB(t)

	indexedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	if err := database.SaveFileIndex(projectID, []FileIndex{
		{Path: "README.md", Language: "markdown", Hash: "a", SizeBytes: 1, LastIndexedAt: indexedAt},
	}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}

	got, err := database.FilesMatchingBasename(projectID, "nonexistent.go", 5)
	if err != nil {
		t.Fatalf("FilesMatchingBasename: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 matches, got %d: %v", len(got), got)
	}
}

func TestFilesMatchingBasenameAnchored(t *testing.T) {
	database, projectID := openMetricsTestDB(t)

	indexedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	files := []FileIndex{
		{Path: "src/main.go", Language: "go", Hash: "a", SizeBytes: 1, LastIndexedAt: indexedAt},
		{Path: "cmd/remains.go", Language: "go", Hash: "b", SizeBytes: 2, LastIndexedAt: indexedAt},
		{Path: "cmd/server/main.go", Language: "go", Hash: "c", SizeBytes: 3, LastIndexedAt: indexedAt},
	}
	if err := database.SaveFileIndex(projectID, files); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}

	// "main.go" appears as a substring in "cmd/remains.go" via the substring
	// "main" within "remains", but only "src/main.go" and "cmd/server/main.go"
	// have "main.go" at a path-separator boundary.
	got, err := database.FilesMatchingBasename(projectID, "main.go", 10)
	if err != nil {
		t.Fatalf("FilesMatchingBasename: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 matches (anchored to path separator), got %d: %v", len(got), got)
	}
}

func TestFilesMatchingBasenameEscapesWildcards(t *testing.T) {
	database, projectID := openMetricsTestDB(t)

	indexedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	files := []FileIndex{
		{Path: "src/test_helper.go", Language: "go", Hash: "a", SizeBytes: 1, LastIndexedAt: indexedAt},
		{Path: "src/Xhelper.go", Language: "go", Hash: "b", SizeBytes: 2, LastIndexedAt: indexedAt},
	}
	if err := database.SaveFileIndex(projectID, files); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}

	// Searching for "test_helper.go" must match the literal underscore,
	// not treat "_" as a single-character wildcard that would also match
	// "testXhelper.go" or similar. The underscore in the search term is
	// escaped so only the exact basename matches.
	got, err := database.FilesMatchingBasename(projectID, "test_helper.go", 10)
	if err != nil {
		t.Fatalf("FilesMatchingBasename: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 match (wildcard escaped), got %d: %v", len(got), got)
	}
	if got[0] != "src/test_helper.go" {
		t.Errorf("expected src/test_helper.go, got %q", got[0])
	}
}

func TestUpdateFileSummaryNoOpForMissingPath(t *testing.T) {
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

	if err := db.UpdateFileSummary(projectID, "does-not-exist.go", "should not error"); err != nil {
		t.Fatalf("UpdateFileSummary returned error for missing path: %v", err)
	}
}
