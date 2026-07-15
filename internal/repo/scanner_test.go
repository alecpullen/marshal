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

func TestNewScannerContinuesOnBadGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("[\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := NewScanner(Config{Root: dir})
	if s.loadErr == nil {
		t.Fatal("expected loadErr to be set for malformed gitignore")
	}
	if s.gitignore != nil {
		t.Fatal("expected gitignore to be nil when parse fails")
	}
	// Scan must still succeed (the gitignore rules are simply not applied).
	if _, err := s.Scan(); err != nil {
		t.Errorf("Scan should succeed when gitignore fails to parse: %v", err)
	}
	// Warnings should contain the gitignore error.
	if len(s.Warnings()) == 0 {
		t.Error("expected at least one warning after gitignore parse failure")
	}
}

func TestScannerWarningsResetBetweenScanCalls(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("[\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := NewScanner(Config{Root: dir})
	if s.loadErr == nil {
		t.Fatal("expected loadErr to be set for malformed gitignore")
	}
	// First Scan.
	if _, err := s.Scan(); err != nil {
		t.Fatalf("first Scan failed: %v", err)
	}
	// Second Scan — without the fix, warnings would duplicate.
	if _, err := s.Scan(); err != nil {
		t.Fatalf("second Scan failed: %v", err)
	}
	if n := len(s.Warnings()); n != 1 {
		t.Fatalf("expected exactly 1 warning after two Scan calls, got %d", n)
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

func TestScanDetailedReturnsContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write hello.go: %v", err)
	}

	scanner := NewScanner(Config{Root: dir})
	scanned, err := scanner.ScanDetailed()
	if err != nil {
		t.Fatalf("ScanDetailed failed: %v", err)
	}
	if len(scanned) != 1 {
		t.Fatalf("expected 1 file, got %d", len(scanned))
	}
	sf := scanned[0]
	if sf.Path != "hello.go" {
		t.Fatalf("expected hello.go, got %s", sf.Path)
	}
	if sf.Content == nil {
		t.Fatal("expected non-nil Content")
	}
	if string(sf.Content) != "package main\n" {
		t.Fatalf("unexpected content: %q", string(sf.Content))
	}
	if sf.ReadErr != nil {
		t.Fatalf("unexpected ReadErr: %v", sf.ReadErr)
	}
	if sf.Hash == "" {
		t.Fatal("expected non-empty hash")
	}
}

func TestScanDetailedContinuesOnReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0000 semantics differ on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod 0000 has no effect when running as root")
	}

	dir := t.TempDir()

	// Create a readable file.
	if err := os.WriteFile(filepath.Join(dir, "good.go"), []byte("package good\n"), 0644); err != nil {
		t.Fatalf("write good.go: %v", err)
	}
	// Create a file that will be made unreadable.
	if err := os.WriteFile(filepath.Join(dir, "bad.go"), []byte("package bad\n"), 0000); err != nil {
		t.Fatalf("write bad.go: %v", err)
	}
	// Make it truly unreadable (WriteFile with 0000 may not work on all
	// systems depending on umask, so chmod explicitly).
	if err := os.Chmod(filepath.Join(dir, "bad.go"), 0000); err != nil {
		t.Fatalf("chmod bad.go: %v", err)
	}
	// Restore permissions on cleanup so t.TempDir can remove the tree.
	defer os.Chmod(filepath.Join(dir, "bad.go"), 0644)

	scanner := NewScanner(Config{Root: dir})
	scanned, err := scanner.ScanDetailed()
	if err != nil {
		t.Fatalf("ScanDetailed should not abort on read errors: %v", err)
	}

	// We should still get results for both files.
	if len(scanned) != 2 {
		t.Fatalf("expected 2 results (one with ReadErr), got %d", len(scanned))
	}

	var good, bad *ScannedFile
	for i := range scanned {
		sf := &scanned[i]
		switch sf.Path {
		case "good.go":
			good = sf
		case "bad.go":
			bad = sf
		}
	}
	if good == nil {
		t.Fatal("expected result for good.go")
	}
	if bad == nil {
		t.Fatal("expected result for bad.go")
	}
	if good.ReadErr != nil {
		t.Fatalf("good.go should not have ReadErr: %v", good.ReadErr)
	}
	if good.Content == nil {
		t.Fatal("good.go should have non-nil Content")
	}
	if bad.ReadErr == nil {
		t.Fatal("bad.go should have a non-nil ReadErr")
	}
	if bad.Content != nil {
		t.Fatal("bad.go should have nil Content")
	}
	if bad.Path != "bad.go" {
		t.Fatalf("bad.go path should be preserved, got %s", bad.Path)
	}

	// Warnings should include the read error.
	warnings := scanner.Warnings()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "bad.go") && strings.Contains(w, "read ") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected warning about bad.go read error, got %v", warnings)
	}
}

func TestScannerSkipsOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "huge.bin")
	if err := os.WriteFile(big, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewScanner(Config{Root: dir, MaxIndexableFileBytes: 512, SkipGitignore: true})
	files, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
	if len(s.Skipped()) == 0 {
		t.Errorf("expected a skip entry for the oversized file")
	}
}

func TestScanContinuesOnHashError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0000 semantics differ on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod 0000 has no effect when running as root")
	}

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "good.go"), []byte("package good\n"), 0644); err != nil {
		t.Fatalf("write good.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.go"), []byte("package bad\n"), 0000); err != nil {
		t.Fatalf("write bad.go: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "bad.go"), 0000); err != nil {
		t.Fatalf("chmod bad.go: %v", err)
	}
	defer os.Chmod(filepath.Join(dir, "bad.go"), 0644)

	scanner := NewScanner(Config{Root: dir})

	// Scan (which reads file contents via hashFile) should also continue.
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan should not abort on hash errors: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files from Scan, got %d", len(files))
	}

	// The readable file should have a hash, the unreadable one should have an
	// empty hash (because hashFile failed).
	var goodHash, badHash string
	for _, f := range files {
		switch f.Path {
		case "good.go":
			goodHash = f.Hash
		case "bad.go":
			badHash = f.Hash
		}
	}
	if goodHash == "" {
		t.Fatal("good.go should have a non-empty hash")
	}
	if badHash != "" {
		t.Fatal("bad.go should have an empty hash (read failed)")
	}
}
