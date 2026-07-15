// Package jsonextract provides a small utility for pulling the first
// complete, balanced JSON object out of a model response. It is used by
// the agent runtime (ParseAction) and by the knowledge agent
// (ParseExtraction), both of which need to tolerate prose or markdown
// fences around the JSON payload that local models frequently emit.
package jsonextract

import (
	"errors"
	"strings"
)

// ErrNotFound is returned when raw contains no complete JSON object. The
// caller is expected to wrap this with package-specific context (e.g.
// "knowledge: …" or "agent: …") so the error message matches its
// existing surface.
var ErrNotFound = errors.New("jsonextract: no JSON object found in input")

// Extract returns the first complete, balanced JSON object in raw. It
// tolerates a leading/trailing ``` or ```json markdown fence (local
// models frequently wrap JSON in code fences even when told not to).
// Brace depth is tracked via a stack-based scanner that respects string
// boundaries and escape sequences, so braces inside string literals do
// not confuse the parse.
//
// Returns ErrNotFound if no balanced object is present.
func Extract(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	depth := 0
	start := -1
	inString := false
	escaped := false

	for i, ch := range trimmed {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		switch ch {
		case '"':
			inString = true
			escaped = false
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start != -1 {
				return trimmed[start : i+1], nil
			}
		}
	}

	return "", ErrNotFound
}
