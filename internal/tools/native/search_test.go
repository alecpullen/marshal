package native

import (
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

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
