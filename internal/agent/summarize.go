package agent

import (
	"fmt"
	"strings"

	"marshal/internal/tools/registry"
)

// DefaultMaxToolResultChars is a rough 4-char-per-token budget for 2000 tokens.
const DefaultMaxToolResultChars = 8000

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
