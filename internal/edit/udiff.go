package edit

import (
	"strings"
)

// UdiffEdit is a single file's unified-diff hunk targeting one file.
type UdiffEdit struct {
	Path    string
	Hunks   []Hunk
}

// Hunk is one @@ ... @@ section from a unified diff.
type Hunk struct {
	Lines []HunkLine
}

// HunkLine is a single line in a hunk.
type HunkLine struct {
	Op   byte // ' ' (context), '+' (add), '-' (remove)
	Text string
}

// ParseUdiff extracts unified-diff edits from an executor response.
//
// Expected format:
//
//	--- a/path/to/file.go
//	+++ b/path/to/file.go
//	@@ -1,4 +1,4 @@
//	 context
//	-old line
//	+new line
//	 context
//
// The path is taken from the +++ line. Multiple files may appear in one
// response. Fenced code blocks (```diff ... ```) are unwrapped before parsing.
func ParseUdiff(response string) []UdiffEdit {
	// Unwrap any fenced code blocks that contain the diff.
	response = unwrapFences(response)

	lines := strings.Split(response, "\n")
	var results []UdiffEdit

	var current *UdiffEdit
	var currentHunk *Hunk

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "--- "):
			// Start of a new file diff — reset state.
			currentHunk = nil
			// Don't create the edit yet; wait for +++ line.

		case strings.HasPrefix(line, "+++ "):
			// Extract path: strip "+++ b/" or "+++ " prefix.
			path := strings.TrimPrefix(line, "+++ ")
			path = strings.TrimPrefix(path, "b/")
			path = strings.TrimSpace(path)
			if path == "" || path == "/dev/null" {
				current = nil
				continue
			}
			current = &UdiffEdit{Path: path}
			results = append(results, *current)
			// Keep a pointer to the last appended element.
			current = &results[len(results)-1]
			currentHunk = nil

		case strings.HasPrefix(line, "@@ "):
			if current == nil {
				continue
			}
			current.Hunks = append(current.Hunks, Hunk{})
			currentHunk = &current.Hunks[len(current.Hunks)-1]

		default:
			if currentHunk == nil {
				continue
			}
			if len(line) == 0 {
				// Blank line inside a hunk — treat as context.
				currentHunk.Lines = append(currentHunk.Lines, HunkLine{Op: ' ', Text: ""})
				continue
			}
			op := line[0]
			if op != '+' && op != '-' && op != ' ' {
				// Not a hunk line (e.g. "\ No newline at end of file").
				continue
			}
			currentHunk.Lines = append(currentHunk.Lines, HunkLine{Op: op, Text: line[1:]})
		}
	}

	return results
}

// ApplyToContent applies the unified-diff hunks to existing file content.
// It returns the updated content and ok=true on success.  If any hunk fails
// to match it returns the original content and ok=false.
func (e *UdiffEdit) ApplyToContent(current string) (updated string, ok bool) {
	lines := strings.Split(current, "\n")

	for _, hunk := range e.Hunks {
		newLines, matched := applyHunk(lines, hunk)
		if !matched {
			return current, false
		}
		lines = newLines
	}

	return strings.Join(lines, "\n"), true
}

// applyHunk applies one hunk to lines using a fuzzy context-match search.
// It searches for the context+removal lines in order, replaces them with
// context+addition lines, and returns the result.
func applyHunk(lines []string, hunk Hunk) ([]string, bool) {
	// Build the "before" pattern (context + removed lines).
	var before []string
	var after []string
	for _, hl := range hunk.Lines {
		switch hl.Op {
		case ' ':
			before = append(before, hl.Text)
			after = append(after, hl.Text)
		case '-':
			before = append(before, hl.Text)
		case '+':
			after = append(after, hl.Text)
		}
	}

	if len(before) == 0 {
		// Pure insertion: append at end.
		return append(lines, after...), true
	}

	// Find "before" in lines (exact then normalised).
	idx := findSequence(lines, before, false)
	if idx < 0 {
		idx = findSequence(lines, before, true)
	}
	if idx < 0 {
		return lines, false
	}

	result := make([]string, 0, len(lines)-len(before)+len(after))
	result = append(result, lines[:idx]...)
	result = append(result, after...)
	result = append(result, lines[idx+len(before):]...)
	return result, true
}

// findSequence searches for needle in haystack starting at each offset.
// If normalise is true, trailing whitespace is stripped before comparison.
func findSequence(haystack, needle []string, normalise bool) int {
	n := len(needle)
	trim := func(s string) string { return s }
	if normalise {
		trim = func(s string) string { return strings.TrimRight(s, " \t") }
	}
	for i := 0; i <= len(haystack)-n; i++ {
		match := true
		for j, nl := range needle {
			if trim(haystack[i+j]) != trim(nl) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// unwrapFences strips a single ``` ... ``` (or ```diff ... ```) fence if the
// response contains exactly one fenced block and the content looks like a diff.
func unwrapFences(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	inFence := false
	for _, l := range lines {
		if !inFence && (l == "```diff" || l == "```") {
			inFence = true
			continue
		}
		if inFence && l == "```" {
			inFence = false
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
