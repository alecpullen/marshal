package native

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestRepoSearchRespectsGitignore(t *testing.T) {
	root := t.TempDir()
	// Root .gitignore: ignore *.log
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	// A .log file at root — should be ignored
	writeFile(t, filepath.Join(root, "debug.log"), "needle\n")
	// A .go file at root — should be found
	writeFile(t, filepath.Join(root, "main.go"), "needle\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if strings.Contains(result.Content, "debug.log") {
		t.Fatalf("Content included gitignored file debug.log:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "main.go:1:needle") {
		t.Fatalf("Content missing main.go match:\n%s", result.Content)
	}
}

func TestRepoSearchFindsSubstringMatches(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha\nneedle here\n")
	writeFile(t, filepath.Join(root, "sub", "b.txt"), "another needle\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	for _, want := range []string{"a.txt:2:needle here", "sub/b.txt:1:another needle"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("Content missing %q:\n%s", want, result.Content)
		}
	}
}

func TestRepoSearchRespectsMaxResults(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "needle 1\nneedle 2\nneedle 3\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle","max_results":2}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if strings.Count(result.Content, "a.txt:") != 2 {
		t.Fatalf("Content = %q, want 2 results", result.Content)
	}
	if !strings.Contains(result.Summary, "capped") {
		t.Fatalf("Summary = %q, want capped", result.Summary)
	}
}

func TestRepoSearchSkipsIgnoredDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "config"), "needle\n")
	writeFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "needle\n")
	writeFile(t, filepath.Join(root, "src", "main.go"), "needle\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if strings.Contains(result.Content, ".git/") || strings.Contains(result.Content, "node_modules/") {
		t.Fatalf("Content included ignored directory:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "src/main.go:1:needle") {
		t.Fatalf("Content missing src match:\n%s", result.Content)
	}
}

func TestRepoSearch_SkipsSymlinksAndReportsErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires elevated privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("needle"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if strings.Contains(result.Content, "secret") {
		t.Errorf("search followed symlink outside root; content:\n%s", result.Content)
	}
}

func TestRepoSearchRejectsEmptyQueryAndTraversal(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "repo.search", `{"query":""}`); err == nil {
		t.Fatal("repo.search empty query returned nil error")
	}
	if _, err := invokeTool(t, reg, "repo.search", `{"query":"x","path":"../outside"}`); err == nil {
		t.Fatal("repo.search traversal returned nil error")
	}
}

func TestRepoSearchRegexMode(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "needle here\nn34dle there\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"n[0-9]+dle","mode":"regex"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if !strings.Contains(result.Content, "a.txt:2:n34dle there") {
		t.Fatalf("Content missing regex match:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "needle here") {
		t.Fatalf("substring-only line should not match regex:\n%s", result.Content)
	}
}

func TestRepoSearchInvalidRegex(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "needle\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "repo.search", `{"query":"n[","mode":"regex"}`)
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("error = %v, want it to say %q so the model can correct itself", err, "invalid regex")
	}
}

func TestRepoSearchRejectsUnknownMode(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "repo.search", `{"query":"x","mode":"fuzzy"}`); err == nil {
		t.Fatal("expected error for unknown mode")
	}
}

func TestRepoSearchIncludeGlob(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "// needle\n")
	writeFile(t, filepath.Join(root, "a.txt"), "needle\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle","include":"*.go"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if !strings.Contains(result.Content, "a.go:1:// needle") {
		t.Fatalf("Content missing a.go match:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "a.txt") {
		t.Fatalf("include glob should have excluded a.txt:\n%s", result.Content)
	}
}

func TestRepoSearchContextLines(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "one\ntwo\nneedle\nfour\nfive\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle","context":1}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	for _, want := range []string{"a.txt-2-two", "a.txt:3:needle", "a.txt-4-four"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("Content missing %q:\n%s", want, result.Content)
		}
	}
	if strings.Contains(result.Content, "one") || strings.Contains(result.Content, "five") {
		t.Fatalf("context=1 should not include lines 1 or 5:\n%s", result.Content)
	}
}
