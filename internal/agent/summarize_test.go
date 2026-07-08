package agent

import (
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestSummarizeToolResultTruncatesGenericContent(t *testing.T) {
	long := strings.Repeat("x", DefaultMaxToolResultChars+100)
	result := SummarizeToolResult("file.read", registry.ToolResult{Summary: "read ok", Content: long}, -1)

	if len(result.Content) >= len(long) {
		t.Fatalf("content was not truncated")
	}
	if !strings.HasSuffix(result.Content, "[truncated]") {
		t.Fatalf("missing truncation marker: %q", result.Content)
	}
	if !strings.Contains(result.Summary, "[truncated]") {
		t.Fatalf("summary should note truncation: %q", result.Summary)
	}
}

func TestSummarizeToolResultZeroMaxCharsSkipsCharCap(t *testing.T) {
	big := strings.Repeat("x", DefaultMaxToolResultChars+1000)
	out := SummarizeToolResult("shell.run", registry.ToolResult{Summary: "s", Content: big}, 0)
	if len(out.Content) != len(big) {
		t.Fatalf("maxChars=0 must skip the char cap: got %d chars, want %d", len(out.Content), len(big))
	}
}

func TestSummarizeToolResultLimitsRepoSearchLines(t *testing.T) {
	content := strings.Repeat("match\n", 60)
	result := SummarizeToolResult("repo.search", registry.ToolResult{Summary: "found 60", Content: content}, 0)

	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	if len(lines) != 51 { // 50 matches + omission notice
		t.Fatalf("got %d lines, want 51", len(lines))
	}
	if !strings.Contains(result.Content, "more matches omitted") {
		t.Fatalf("missing omission notice: %q", result.Content)
	}
}

func TestSummarizeToolResultLeavesSmallResultsUnchanged(t *testing.T) {
	result := SummarizeToolResult("file.read", registry.ToolResult{Summary: "ok", Content: "hello"}, 0)
	if result.Content != "hello" {
		t.Fatalf("content changed unexpectedly: %q", result.Content)
	}
	if result.Summary != "ok" {
		t.Fatalf("summary changed unexpectedly: %q", result.Summary)
	}
}
