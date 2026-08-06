package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"marshal/internal/tools/registry"
)

// spillPreviewChars is how much of an oversized tool output the model sees
// inline before the pointer to the full file.
const spillPreviewChars = 2000

// spillToolResult keeps oversized tool output reachable instead of
// destroying it (kimi-code / opencode pattern): output above maxChars is
// written to .marshal/tool-results/ inside the working directory and the
// model receives a preview plus the path and a paging instruction. On any
// filesystem error it falls back to plain truncation so a bad disk never
// blocks the loop.
func spillToolResult(workDir, toolName string, result registry.ToolResult, maxChars int) registry.ToolResult {
	if maxChars <= 0 {
		maxChars = DefaultMaxToolResultChars
	}
	if len(result.Content) <= maxChars {
		return result
	}

	dir := filepath.Join(workDir, ".marshal", "tool-results")
	path, err := writeSpillFile(dir, toolName, result.Content)
	if err != nil {
		return SummarizeToolResult(toolName, result, maxChars)
	}

	rel := path
	if r, relErr := filepath.Rel(workDir, path); relErr == nil {
		rel = r
	}

	out := result
	out.Summary = result.Summary + " [output spilled to file]"
	out.Content = result.Content[:spillPreviewChars] + fmt.Sprintf(
		"\n\n[output truncated: %d chars total, preview above]\nfull_output_path: %s\nTo continue reading, use file.read on the full_output_path above (or on the original path) with start_line/end_line, or use file.page with the desired page number.",
		len(result.Content), rel,
	)
	return out
}

func writeSpillFile(dir, toolName, content string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	stem := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, toolName)
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.txt", stem, time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
