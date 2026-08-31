package agent

import (
	"fmt"
	"strings"

	"marshal/internal/tools/registry"
)

// DefaultMaxToolResultChars is a rough 4-char-per-token budget for 2000 tokens.
// It is the fallback cap when no turn threshold is available; the derived cap
// (deriveToolResultChars) scales below it on small-window models.
const DefaultMaxToolResultChars = 8000

// minToolResultChars is the smallest derived cap: below this a tool result
// cannot carry even a useful file excerpt, and the spill preview floor
// (spill.go) would exceed the cap.
const minToolResultChars = 2000

// maxDerivedToolResultChars bounds the derived cap so a single tool
// result cannot swallow a huge window's whole budget.
const maxDerivedToolResultChars = 200000

// deriveToolResultChars sizes one tool result at ~5% of the turn's token
// budget. At the 4-chars-per-token estimate the runner uses throughout
// (see estimateTokens), 5% of `threshold` tokens is threshold/5 chars.
// Clamped to [minToolResultChars, maxDerivedToolResultChars]. The floor is
// deliberately small — a fixed 8000-char floor would hand a 16k-window model
// (~12k-token turn threshold) ~17% of its budget per tool result.
func deriveToolResultChars(threshold int) int {
	chars := threshold / 5
	if chars < minToolResultChars {
		return minToolResultChars
	}
	if chars > maxDerivedToolResultChars {
		return maxDerivedToolResultChars
	}
	return chars
}

// SummarizeToolResult applies per-tool line limits and an optional character
// cap to tool output before it reaches the transcript. It preserves the
// original Summary unless truncation occurs.
//
// maxChars controls the character cap: negative values use the default cap,
// zero skips the cap, and positive values apply that exact cap.
func SummarizeToolResult(toolName string, result registry.ToolResult, maxChars int) registry.ToolResult {
	if maxChars < 0 {
		maxChars = DefaultMaxToolResultChars
	}
	skipCharCap := maxChars == 0

	out := result
	content := result.Content
	if content == "" {
		return out
	}

	switch toolName {
	case "repo.search":
		content = limitLines(content, 50, "more matches omitted")
	case "git.diff":
		content = limitLines(content, 200, "more diff lines omitted")
	}

	if !skipCharCap && len(content) > maxChars {
		content = content[:maxChars] + "\n\n...[truncated]"
	}

	if content != result.Content && !strings.HasSuffix(out.Summary, "[truncated]") {
		out.Summary = out.Summary + " [truncated]"
	}
	out.Content = content
	return out
}

func limitLines(content string, maxLines int, label string) string {
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content
	}
	return strings.Join(lines[:maxLines], "\n") +
		fmt.Sprintf("\n... %d %s", len(lines)-maxLines, label)
}
