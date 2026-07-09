package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestExtractPinnedFilesFindsAndReadsKnownPaths(t *testing.T) {
	dir := t.TempDir()
	// Create a few real files we can index.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}

	database := openTestDB(t)
	projectID := int64(1)
	if err := database.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "a.go", Language: "go"},
		{Path: "b.go", Language: "go"},
	}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}

	state := session.New(config.Config{}, dir, time.Unix(100, 0), session.Persistence{DB: database})
	pinned := extractPinnedFiles("look at @a.go and @b.go", state, projectID)
	if len(pinned) != 2 {
		t.Fatalf("pinned = %d, want 2", len(pinned))
	}
	if pinned[0].Path != "a.go" || !strings.Contains(pinned[0].Content, "package a") {
		t.Fatalf("pinned[0] = %+v", pinned[0])
	}
	if pinned[1].Path != "b.go" || !strings.Contains(pinned[1].Content, "package b") {
		t.Fatalf("pinned[1] = %+v", pinned[1])
	}
}

func TestExtractPinnedFilesIgnoresUnknownPaths(t *testing.T) {
	dir := t.TempDir()
	// Real file on disk for the known path.
	if err := os.WriteFile(filepath.Join(dir, "known.go"), []byte("package k\n"), 0o644); err != nil {
		t.Fatalf("write known.go: %v", err)
	}
	database := openTestDB(t)
	projectID := int64(1)
	if err := database.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "known.go", Language: "go"},
	}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}

	state := session.New(config.Config{}, dir, time.Unix(100, 0), session.Persistence{DB: database})
	pinned := extractPinnedFiles("@known.go @unknown.go @also-unknown.txt", state, projectID)
	if len(pinned) != 1 || pinned[0].Path != "known.go" {
		t.Fatalf("pinned = %+v, want only [known.go]", pinned)
	}
}

func TestExtractPinnedFilesNoTokensReturnsNil(t *testing.T) {
	dir := t.TempDir()
	state := session.New(config.Config{}, dir, time.Unix(100, 0), session.Persistence{})
	if got := extractPinnedFiles("no at-refs here", state, 1); got != nil {
		t.Fatalf("pinned = %+v, want nil", got)
	}
}

func TestExtractPinnedFilesIgnoresAtInsideEmail(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.go"), []byte("package r\n"), 0o644); err != nil {
		t.Fatalf("write real.go: %v", err)
	}
	database := openTestDB(t)
	projectID := int64(1)
	if err := database.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "real.go", Language: "go"},
	}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}
	state := session.New(config.Config{}, dir, time.Unix(100, 0), session.Persistence{DB: database})
	// "user@example.com" is preceded by 'r' (not whitespace), so the
	// @ in the email must not match. Only " @real.go" is a real trigger.
	pinned := extractPinnedFiles("user@example.com and @real.go", state, projectID)
	if len(pinned) != 1 || pinned[0].Path != "real.go" {
		t.Fatalf("pinned = %+v, want only [real.go]", pinned)
	}
}

func TestExtractPinnedFilesDeduplicates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write x.go: %v", err)
	}
	database := openTestDB(t)
	projectID := int64(1)
	if err := database.SaveFileIndex(projectID, []db.FileIndex{{Path: "x.go"}}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}
	state := session.New(config.Config{}, dir, time.Unix(100, 0), session.Persistence{DB: database})
	pinned := extractPinnedFiles("@x.go @x.go @x.go", state, projectID)
	if len(pinned) != 1 {
		t.Fatalf("pinned = %d, want 1 (deduped)", len(pinned))
	}
}

// TestRunTaskPinsAtFileReferences is an end-to-end check that the runner
// invokes extractPinnedFiles during RunTask, before the first model call,
// and that the resulting context pack carries the file content.
func TestRunTaskPinsAtFileReferences(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ref.go"), []byte("package ref\n\n// MARKER: ref-go-file\n"), 0o644); err != nil {
		t.Fatalf("write ref.go: %v", err)
	}
	database := openTestDB(t)
	projectID := int64(1)
	if err := database.SaveFileIndex(projectID, []db.FileIndex{{Path: "ref.go", Language: "go"}}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}
	state := session.New(config.Config{}, dir, time.Unix(100, 0), session.Persistence{DB: database})

	p := &scriptedProvider{
		responses: []string{`{"actions":[],"type":"answer","content":"done"}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.NativeTools = true
	runner.ProjectID = projectID

	_, err := runner.RunTask(context.Background(), "explain @ref.go")
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	pack := state.ContextPack()
	if len(pack.Pinned) != 1 {
		t.Fatalf("pack.Pinned = %+v, want 1 snippet", pack.Pinned)
	}
	if pack.Pinned[0].Path != "ref.go" {
		t.Fatalf("pinned path = %q, want ref.go", pack.Pinned[0].Path)
	}
	if !strings.Contains(pack.Pinned[0].Content, "MARKER: ref-go-file") {
		t.Fatalf("pinned content missing file body: %q", pack.Pinned[0].Content)
	}
	// The pinned section must also be present in the pack's Sections.
	found := false
	for _, sec := range pack.Sections {
		if sec.Kind == "file_snippet" && sec.Source == "ref.go" && sec.Priority == 100 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("pinned file_snippet section with Priority 100 not found in pack sections")
	}
}

// openTestDB opens a fresh SQLite database in a temp file and runs
// migrations (the table schemas required by SaveFileIndex are created
// by Migrate, not Open).
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := d.Migrate(); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
