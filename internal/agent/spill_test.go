package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestSpillToolResultPassesThroughSmallOutput(t *testing.T) {
	dir := t.TempDir()
	in := registry.ToolResult{Summary: "ok", Content: "short output"}
	out := spillToolResult(dir, "shell.run", in, 8000)
	if out.Content != "short output" {
		t.Fatalf("small output modified: %q", out.Content)
	}
}

func TestSpillToolResultWritesFileAndReturnsPreview(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("line of output\n", 2000) // ~30k chars
	in := registry.ToolResult{Summary: "ok", Content: big}

	out := spillToolResult(dir, "shell.run", in, 8000)

	if len(out.Content) >= len(big) {
		t.Fatal("oversized output was not reduced")
	}
	if !strings.Contains(out.Content, "full_output_path:") {
		t.Fatalf("spilled result missing path pointer:\n%s", out.Content)
	}
	if !strings.Contains(out.Content, "file.read") || !strings.Contains(out.Content, "start_line") || !strings.Contains(out.Content, "file.page") {
		t.Fatalf("spilled result missing paging instruction:\n%s", out.Content)
	}
	if !strings.HasPrefix(out.Content, big[:spillPreviewChars]) {
		t.Fatal("spilled result does not start with the preview of the original output")
	}

	// The full content must be on disk under .marshal/tool-results/.
	entries, err := os.ReadDir(filepath.Join(dir, ".marshal", "tool-results"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one spill file, got %v (err %v)", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".marshal", "tool-results", entries[0].Name()))
	if err != nil {
		t.Fatalf("reading spill file: %v", err)
	}
	if string(data) != big {
		t.Fatal("spill file does not contain the full original output")
	}
}

func TestSpillToolResultFallsBackToTruncationOnBadDir(t *testing.T) {
	// A file where the directory should be forces the spill write to fail.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".marshal"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 20000)
	out := spillToolResult(dir, "shell.run", registry.ToolResult{Summary: "ok", Content: big}, 8000)
	if !strings.Contains(out.Content, "[truncated]") {
		t.Fatalf("fallback truncation missing marker:\n%.200s", out.Content)
	}
	if len(out.Content) > 8000+100 {
		t.Fatalf("fallback did not truncate: %d chars", len(out.Content))
	}
}
