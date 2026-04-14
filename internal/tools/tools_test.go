package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeRepo creates a temporary directory with the given files and returns its path.
func makeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		abs := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ── sandboxPath ───────────────────────────────────────────────────────────────

func TestSandboxPath_AcceptsValidPaths(t *testing.T) {
	root := t.TempDir()
	cases := []string{"file.go", "sub/dir/file.txt", "."}
	for _, c := range cases {
		if _, err := sandboxPath(root, c); err != nil {
			t.Errorf("sandboxPath(%q) unexpected error: %v", c, err)
		}
	}
}

func TestSandboxPath_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	cases := []string{"../../etc/passwd", "../secret", "/etc/hosts"}
	for _, c := range cases {
		if _, err := sandboxPath(root, c); err == nil {
			t.Errorf("sandboxPath(%q) should have returned an error", c)
		}
	}
}

// ── readFile ──────────────────────────────────────────────────────────────────

func TestReadFile_ReturnsContent(t *testing.T) {
	root := makeRepo(t, map[string]string{"hello.go": "package main\n"})
	got, err := readFile(root, "hello.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "package main") {
		t.Errorf("unexpected content: %q", got)
	}
}

func TestReadFile_RejectsTraversal(t *testing.T) {
	root := makeRepo(t, nil)
	if _, err := readFile(root, "../../etc/passwd"); err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestReadFile_MissingFile(t *testing.T) {
	root := makeRepo(t, nil)
	if _, err := readFile(root, "nonexistent.go"); err == nil {
		t.Error("expected error for missing file")
	}
}

// ── writeFile ─────────────────────────────────────────────────────────────────

func TestWriteFile_CreatesFile(t *testing.T) {
	root := t.TempDir()
	if err := writeFile(root, "new.go", "package x\n"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "new.go"))
	if string(data) != "package x\n" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestWriteFile_CreatesParentDirs(t *testing.T) {
	root := t.TempDir()
	if err := writeFile(root, "a/b/c/file.go", "x"); err != nil {
		t.Fatal(err)
	}
}

func TestWriteFile_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if err := writeFile(root, "../../evil.txt", "bad"); err == nil {
		t.Error("expected error for path traversal")
	}
}

// ── editFile ──────────────────────────────────────────────────────────────────

func TestEditFile_ReplacesLines(t *testing.T) {
	root := makeRepo(t, map[string]string{"f.txt": "line1\nline2\nline3\nline4\n"})
	if err := editFile(root, "f.txt", 2, 3, "NEW2\nNEW3"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	got := string(data)
	if !strings.Contains(got, "NEW2") || strings.Contains(got, "line2") {
		t.Errorf("unexpected content after edit: %q", got)
	}
}

func TestEditFile_OutOfBoundsRange(t *testing.T) {
	root := makeRepo(t, map[string]string{"f.txt": "a\nb\n"})
	if err := editFile(root, "f.txt", 5, 10, "x"); err == nil {
		t.Error("expected error for out-of-bounds line range")
	}
}

func TestEditFile_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if err := editFile(root, "../../bad.txt", 1, 1, "x"); err == nil {
		t.Error("expected error for path traversal")
	}
}

// ── listDirectory ─────────────────────────────────────────────────────────────

func TestListDirectory_ListsFiles(t *testing.T) {
	root := makeRepo(t, map[string]string{"a.go": "", "b.go": "", "sub/c.go": ""})
	got, err := listDirectory(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "a.go") || !strings.Contains(got, "b.go") {
		t.Errorf("unexpected listing: %q", got)
	}
}

func TestListDirectory_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := listDirectory(root, "../../etc"); err == nil {
		t.Error("expected error for path traversal")
	}
}

// ── searchCode ────────────────────────────────────────────────────────────────

func TestSearchCode_FindsMatches(t *testing.T) {
	root := makeRepo(t, map[string]string{
		"a.go": "func Foo() {}\nfunc Bar() {}\n",
		"b.go": "// no match here\n",
	})
	got, err := searchCode(root, "func Foo", "*.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "func Foo") {
		t.Errorf("expected match not found: %q", got)
	}
	if strings.Contains(got, "func Bar") {
		t.Errorf("unexpected match in result: %q", got)
	}
}

func TestSearchCode_InvalidPattern(t *testing.T) {
	root := t.TempDir()
	if _, err := searchCode(root, "[invalid", ""); err == nil {
		t.Error("expected error for invalid regexp")
	}
}

// ── runCommand ────────────────────────────────────────────────────────────────

func TestRunCommand_AllowedCommand(t *testing.T) {
	root := t.TempDir()
	// "go version" is always available in the test environment
	got, err := runCommand(context.Background(), root, []string{"go", "version"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "go version") {
		t.Errorf("unexpected output: %q", got)
	}
}

func TestRunCommand_RejectsDisallowedCommands(t *testing.T) {
	root := t.TempDir()
	disallowed := [][]string{
		{"rm", "-rf", "/"},
		{"sh", "-c", "echo bad"},
		{"bash", "-c", "echo bad"},
		{"curl", "http://example.com"},
	}
	for _, args := range disallowed {
		if _, err := runCommand(context.Background(), root, args); err == nil {
			t.Errorf("runCommand(%v) should have been rejected", args)
		}
	}
}

func TestRunCommand_EmptyArgs(t *testing.T) {
	root := t.TempDir()
	if _, err := runCommand(context.Background(), root, nil); err == nil {
		t.Error("expected error for empty args")
	}
}

func TestRunCommand_RespectsContextCancellation(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := runCommand(ctx, root, []string{"go", "version"})
	if err == nil {
		t.Error("expected error when context is already cancelled")
	}
}

// ── Execute dispatcher ────────────────────────────────────────────────────────

func TestExecute_UnknownTool(t *testing.T) {
	root := t.TempDir()
	result := Execute(context.Background(), Call{ID: "1", ToolName: "nonexistent"}, root)
	if !result.IsError {
		t.Error("expected IsError=true for unknown tool")
	}
}

func TestExecute_ReadFile(t *testing.T) {
	root := makeRepo(t, map[string]string{"x.txt": "hello"})
	result := Execute(context.Background(), Call{
		ID:        "1",
		ToolName:  "read_file",
		Arguments: map[string]any{"path": "x.txt"},
	}, root)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "hello" {
		t.Errorf("unexpected content: %q", result.Content)
	}
	if result.CallID != "1" {
		t.Errorf("CallID not echoed: %q", result.CallID)
	}
}
