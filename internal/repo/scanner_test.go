package repo

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestScannerFindsFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme"), 0644)

	scanner, err := NewScanner(Config{Root: dir})
	if err != nil {
		t.Fatalf("NewScanner failed: %v", err)
	}
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
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
	os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("// js"), 0644)

	scanner, err := NewScanner(Config{Root: dir})
	if err != nil {
		t.Fatalf("NewScanner failed: %v", err)
	}
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
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, "foo_test.go"), []byte("package main"), 0644)

	scanner, err := NewScanner(Config{Root: dir, Ignore: []string{"*_test.go"}})
	if err != nil {
		t.Fatalf("NewScanner failed: %v", err)
	}
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected only main.go, got %+v", files)
	}
}

func TestScannerRespectsGitignore(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secret.txt\n"), 0644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret"), 0644)

	scanner, err := NewScanner(Config{Root: dir})
	if err != nil {
		t.Fatalf("NewScanner failed: %v", err)
	}
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected only main.go, got %+v", files)
	}
}

func TestScannerComputesHashesAndLanguages(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	scanner, err := NewScanner(Config{Root: dir})
	if err != nil {
		t.Fatalf("NewScanner failed: %v", err)
	}
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
	expected := fmt.Sprintf("%x", sha256.Sum256([]byte("package main\n")))
	if f.Hash != expected {
		t.Fatalf("expected hash %s, got %s", expected, f.Hash)
	}
	if f.SizeBytes != 13 {
		t.Fatalf("expected size 13, got %d", f.SizeBytes)
	}
}

func TestScannerSkipGitignore(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secret.txt\n"), 0644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret"), 0644)

	scanner, err := NewScanner(Config{Root: dir, SkipGitignore: true})
	if err != nil {
		t.Fatalf("NewScanner failed: %v", err)
	}
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
}
