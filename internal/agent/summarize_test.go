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

func TestDeriveToolResultChars(t *testing.T) {
	cases := []struct {
		name      string
		threshold int
		want      int
	}{
		{"60k fallback threshold -> 12000", 60000, 12000},
		{"minimax-m3 103219 -> 20643", 103219, 20643},
		{"kimi k3-256k 185600 -> 37120", 185600, 37120},
		{"deepseek 725000 -> 145000", 725000, 145000},
		{"very large threshold clamps to 200000", 1_200_000, 200000},
		{"16k-model threshold derives below the old 8000 floor", 11878, 2375},
		{"tiny threshold clamps to the 2000 floor", 5000, minToolResultChars},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveToolResultChars(tc.threshold); got != tc.want {
				t.Fatalf("deriveToolResultChars(%d) = %d, want %d", tc.threshold, got, tc.want)
			}
		})
	}
}

func TestToolResultCharsPrefersExplicitOverDerived(t *testing.T) {
	r := NewRunner(nil, nil, nil, newTestState(t), "m")

	if got := r.toolResultChars(); got != DefaultMaxToolResultChars {
		t.Fatalf("with nothing set, toolResultChars() = %d, want %d", got, DefaultMaxToolResultChars)
	}

	r.turnToolResultChars = 40000
	if got := r.toolResultChars(); got != 40000 {
		t.Fatalf("with a derived value, toolResultChars() = %d, want 40000", got)
	}

	r.MaxToolResultChars = 5000
	if got := r.toolResultChars(); got != 5000 {
		t.Fatalf("explicit config must win, toolResultChars() = %d, want 5000", got)
	}
}

func TestNewRunnerLeavesToolResultCapUnset(t *testing.T) {
	r := NewRunner(nil, nil, nil, newTestState(t), "m")
	if r.MaxToolResultChars != 0 {
		t.Fatalf("NewRunner seeded MaxToolResultChars = %d, want 0 (0 = derive)", r.MaxToolResultChars)
	}
}
