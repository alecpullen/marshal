package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewPatchDiffGeneratesUnifiedDiff(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}

	patchText := "File: main.go\n<<<<<<< SEARCH\nfunc main() {}\n=======\nfunc main() {\n\tprintln(\"hi\")\n}\n>>>>>>> REPLACE\n"

	diff, err := PreviewPatchDiff(dir, patchText)
	if err != nil {
		t.Fatalf("PreviewPatchDiff returned error: %v", err)
	}
	if !strings.Contains(diff, "--- a/main.go") || !strings.Contains(diff, "+++ b/main.go") {
		t.Fatalf("diff missing unified diff headers: %s", diff)
	}
	if !strings.Contains(diff, `println("hi")`) {
		t.Fatalf("diff missing added content: %s", diff)
	}
}

func TestPreviewPatchDiffRejectsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	patchText := "File: /etc/passwd\n<<<<<<< SEARCH\nroot\n=======\nroot2\n>>>>>>> REPLACE\n"

	if _, err := PreviewPatchDiff(dir, patchText); err == nil {
		t.Fatal("expected error for absolute path, got nil")
	}
}

func TestPreviewPatchDiffRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	patchText := "File: ../outside.go\n<<<<<<< SEARCH\na\n=======\nb\n>>>>>>> REPLACE\n"

	if _, err := PreviewPatchDiff(dir, patchText); err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestPreviewPatchDiffRejectsSearchBlockNotFound(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	patchText := "File: main.go\n<<<<<<< SEARCH\nnot present\n=======\nreplacement\n>>>>>>> REPLACE\n"

	if _, err := PreviewPatchDiff(dir, patchText); err == nil {
		t.Fatal("expected validation error, got nil")
	}
}
