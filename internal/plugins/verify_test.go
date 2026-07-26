package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHashDirDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha")
	writeFile(t, filepath.Join(dir, "sub", "b.txt"), "beta")

	h1, err := HashDir(dir)
	if err != nil {
		t.Fatalf("HashDir: %v", err)
	}
	h2, err := HashDir(dir)
	if err != nil {
		t.Fatalf("HashDir: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("hash not deterministic: %q vs %q", h1, h2)
	}
	if !strings.HasPrefix(h1, "sha256:") {
		t.Fatalf("hash %q missing sha256: prefix", h1)
	}
}

func TestHashDirContentChange(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha")
	before, err := HashDir(dir)
	if err != nil {
		t.Fatalf("HashDir: %v", err)
	}

	writeFile(t, filepath.Join(dir, "a.txt"), "tampered")
	after, err := HashDir(dir)
	if err != nil {
		t.Fatalf("HashDir: %v", err)
	}
	if before == after {
		t.Fatal("hash should change when file contents change")
	}
}

func TestHashDirRenameChange(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha")
	before, err := HashDir(dir)
	if err != nil {
		t.Fatalf("HashDir: %v", err)
	}

	if err := os.Rename(filepath.Join(dir, "a.txt"), filepath.Join(dir, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	after, err := HashDir(dir)
	if err != nil {
		t.Fatalf("HashDir: %v", err)
	}
	if before == after {
		t.Fatal("hash should change when a file is renamed")
	}
}

func TestHashDirSkipsNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha")
	if err := os.Symlink(filepath.Join(dir, "a.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := HashDir(dir); err != nil {
		t.Fatalf("HashDir should skip symlinks: %v", err)
	}
}

func TestHashDirMissing(t *testing.T) {
	if _, err := HashDir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("HashDir should error on a missing directory")
	}
}
