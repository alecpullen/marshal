package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo makes dir look like a git repository.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	return dir
}

func TestEnsureMarshalIgnoredCreatesFile(t *testing.T) {
	dir := gitRepo(t)
	if err := EnsureMarshalIgnored(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), ".marshal/") {
		t.Errorf("got %q, want it to contain .marshal/", data)
	}
}

func TestEnsureMarshalIgnoredAppendsToExisting(t *testing.T) {
	dir := gitRepo(t)
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("node_modules/\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := EnsureMarshalIgnored(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "node_modules/") {
		t.Errorf("clobbered existing content: %q", data)
	}
	if !strings.Contains(string(data), ".marshal/") {
		t.Errorf("did not append: %q", data)
	}
}

func TestEnsureMarshalIgnoredIsIdempotent(t *testing.T) {
	dir := gitRepo(t)
	for i := 0; i < 3; i++ {
		if err := EnsureMarshalIgnored(dir); err != nil {
			t.Fatalf("ensure %d: %v", i, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if n := strings.Count(string(data), ".marshal/"); n != 1 {
		t.Errorf("wrote %d entries, want 1: %q", n, data)
	}
}

func TestEnsureMarshalIgnoredRecognizesExistingVariants(t *testing.T) {
	for _, existing := range []string{".marshal/\n", ".marshal\n", "/.marshal/\n"} {
		dir := gitRepo(t)
		path := filepath.Join(dir, ".gitignore")
		if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := EnsureMarshalIgnored(dir); err != nil {
			t.Fatalf("ensure: %v", err)
		}
		data, _ := os.ReadFile(path)
		if string(data) != existing {
			t.Errorf("seeded %q, file became %q — should have been left alone", existing, data)
		}
	}
}

func TestEnsureMarshalIgnoredAtomicWrite(t *testing.T) {
	dir := gitRepo(t)
	existing := "node_modules/\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(existing), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := EnsureMarshalIgnored(dir); err != nil {
		t.Fatalf("EnsureMarshalIgnored: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.HasSuffix(string(data), ".marshal/\n") {
		t.Errorf("gitignore content: %q", data)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".gitignore") && e.Name() != ".gitignore" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

// TestEnsureMarshalIgnoredPreservesPermissions verifies the atomic rewrite
// does not reset a pre-existing .gitignore's mode to the default 0644.
func TestEnsureMarshalIgnoredPreservesPermissions(t *testing.T) {
	dir := gitRepo(t)
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("node_modules/\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := EnsureMarshalIgnored(dir); err != nil {
		t.Fatalf("EnsureMarshalIgnored: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions: got %o, want 600", perm)
	}
}

func TestEnsureMarshalIgnoredSkipsNonGitDir(t *testing.T) {
	dir := t.TempDir() // no .git
	if err := EnsureMarshalIgnored(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); !os.IsNotExist(err) {
		t.Error("created .gitignore in a non-git directory")
	}
}

func TestEnsureMarshalIgnoredAddsNewlineBeforeAppending(t *testing.T) {
	dir := gitRepo(t)
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("node_modules/"), 0644); err != nil { // no trailing newline
		t.Fatalf("seed: %v", err)
	}
	if err := EnsureMarshalIgnored(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "node_modules/\n") {
		t.Errorf("entries ran together: %q", data)
	}
}
