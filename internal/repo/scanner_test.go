package repo

import (
	"os"
	"path/filepath"
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
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
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
	for _, f := range files {
		if f.Path == "ignored_dir/file.txt" || f.Path == "ignored_dir" {
			t.Fatalf("expected ignored_dir to be excluded, got %+v", files)
		}
	}
}
