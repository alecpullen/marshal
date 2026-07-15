package repo

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestScannerFindsFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	scanner := NewScanner(Config{Root: dir})
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.Path] = true
	}
	if !paths["main.go"] || !paths["README.md"] {
		t.Fatalf("missing expected files: %+v", files)
	}
}

func TestScannerSkipsIgnoredDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0755); err != nil {
		t.Fatalf("create node_modules/pkg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("// js"), 0644); err != nil {
		t.Fatalf("write index.js: %v", err)
	}

	scanner := NewScanner(Config{Root: dir})
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected only main.go, got %+v", files)
	}
}

func TestScannerAppliesConfigIgnore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foo_test.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("write foo_test.go: %v", err)
	}

	scanner := NewScanner(Config{Root: dir, Ignore: []string{"*_test.go"}})
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected only main.go, got %+v", files)
	}
}

func TestScannerReturnsErrorForMissingRoot(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "does-not-exist")
	scanner := NewScanner(Config{Root: missingDir})
	_, err := scanner.Scan()
	if err == nil {
		t.Fatalf("expected Scan to return an error for missing root, got nil")
	}
}

func TestScannerInvalidIgnorePattern(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	scanner := NewScanner(Config{Root: dir, Ignore: []string{"["}})
	_, err := scanner.Scan()
	if err == nil {
		t.Fatalf("expected Scan to return an error for invalid ignore pattern, got nil")
	}
}

func TestScannerIgnoresDirectoryPattern(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "ignored_dir"), 0755); err != nil {
		t.Fatalf("create ignored_dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored_dir", "file.txt"), []byte("ignored"), 0644); err != nil {
		t.Fatalf("write ignored_dir/file.txt: %v", err)
	}

	scanner := NewScanner(Config{Root: dir, Ignore: []string{"ignored_dir"}})
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected only main.go, got %+v", files)
	}
	// ignored_dir was skipped as a directory, so its contents are not returned.
}

func TestScannerRespectsGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secret.txt\n"), 0644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatalf("write secret.txt: %v", err)
	}

	scanner := NewScanner(Config{Root: dir})
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected only main.go, got %+v", files)
	}
}

func TestScannerIncludesGitignoredFilesWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secret.txt\n"), 0644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatalf("write secret.txt: %v", err)
	}

	scanner := NewScanner(Config{Root: dir, SkipGitignore: true})
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.Path] = true
	}
	if !paths["main.go"] || !paths["secret.txt"] {
		t.Fatalf("expected main.go and secret.txt, got %+v", files)
	}
	if paths[".gitignore"] {
		t.Fatalf("expected .gitignore itself to be skipped, got %+v", files)
	}
}

func TestScanner_SkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(Config{Root: root})
	files, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.Contains(f.Path, "link") || strings.Contains(f.Path, "secret") {
			t.Errorf("scanner followed symlink: %s", f.Path)
		}
	}
}

func TestScannerComputesHashesAndLanguages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	scanner := NewScanner(Config{Root: dir})
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Path != "main.go" {
		t.Fatalf("expected main.go, got %s", f.Path)
	}
	if f.Language != "go" {
		t.Fatalf("expected language go, got %s", f.Language)
	}
	if f.Hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if f.SizeBytes != 13 {
		t.Fatalf("expected size 13, got %d", f.SizeBytes)
	}
}
