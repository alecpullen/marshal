// Package edit implements executor output parsers for various edit formats.
// This file covers the M3 whole-file format used when the executor responds
// with complete file contents inside fenced code blocks.
//
// Expected format (filename on the line immediately before the opening fence):
//
//	path/to/file.go
//	```go
//	package main
//	// ...
//	```
//
// Multiple files may appear in a single response.
package edit

import (
	"strings"
)

// FileEdit is a single file write produced by parsing executor output.
type FileEdit struct {
	Path    string
	Content string
}

// ParseWhole extracts FileEdit entries from an executor response that uses the
// whole-file format. It is intentionally lenient: unrecognised content between
// blocks is silently ignored.
func ParseWhole(response string) []FileEdit {
	lines := strings.Split(response, "\n")
	var result []FileEdit
	i := 0
	for i < len(lines) {
		line := lines[i]

		// Look for the start of a fenced code block.
		if !strings.HasPrefix(line, "```") {
			i++
			continue
		}

		// Walk backward over blank lines to find a file-path candidate.
		fname := ""
		for j := i - 1; j >= 0; j-- {
			prev := strings.TrimSpace(lines[j])
			if prev == "" {
				continue
			}
			if looksLikeFilePath(prev) {
				fname = prev
			}
			break
		}

		if fname == "" {
			i++
			continue
		}

		// Collect content until the closing fence.
		i++ // skip opening fence line
		var contentLines []string
		for i < len(lines) {
			if lines[i] == "```" {
				break
			}
			contentLines = append(contentLines, lines[i])
			i++
		}
		i++ // skip closing fence

		content := strings.Join(contentLines, "\n")
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}

		result = append(result, FileEdit{Path: fname, Content: content})
	}
	return result
}

// looksLikeFilePath returns true if s looks like a relative file path:
// no spaces, and either contains a '.' after the last '/' or contains a '/'.
func looksLikeFilePath(s string) bool {
	if s == "" || strings.Contains(s, " ") {
		return false
	}
	// Reject obvious non-paths (markdown headings, sentences, etc.)
	if strings.HasPrefix(s, "#") || strings.HasSuffix(s, ":") || strings.HasSuffix(s, ".") {
		return false
	}
	base := s
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		base = s[idx+1:]
	}
	return strings.Contains(base, ".") || strings.Contains(s, "/")
}
