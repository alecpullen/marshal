package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupSpillFilesRemovesAllTxtFiles(t *testing.T) {
	dir := t.TempDir()
	// Create some spill files.
	for i := 0; i < 3; i++ {
		path := filepath.Join(dir, "shell-run-"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(path, []byte("spill content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Create a non-txt file that should survive.
	if err := os.WriteFile(filepath.Join(dir, "other.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CleanupSpillFiles(dir); err != nil {
		t.Fatalf("CleanupSpillFiles: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 remaining file, got %d: %+v", len(entries), entries)
	}
	if entries[0].Name() != "other.json" {
		t.Fatalf("unexpected remaining file: %s", entries[0].Name())
	}
}

func TestCleanupSpillFilesEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := CleanupSpillFiles(dir); err != nil {
		t.Fatalf("CleanupSpillFiles on empty dir: %v", err)
	}
}

func TestCleanupSpillFilesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")
	if err := CleanupSpillFiles(dir); err != nil {
		t.Fatalf("CleanupSpillFiles on missing dir should not error: %v", err)
	}
}
