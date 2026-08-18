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
// Repair is opt-in: a caller that would silently misread a truncated payload
// (the knowledge extractor turning a cut-off memory list into "no memories")
// must keep getting ErrNotFound. Only callers that explicitly want the
// salvage call ExtractRepairing.
func Extract(raw string) (string, error) {
	text, structural, _, err := extractBalanced(raw)
	if err != nil {
		return "", err
	}
	if structural {
		return "", ErrNotFound
	}
	return text, nil
}

// ExtractRepairing behaves like Extract, but it also returns the balanced
// result (with repaired = true) whenever the input needed any correction:
// closing containers left open at the end of input, or excising stray closing
// punctuation. Only closing punctuation is appended or removed: no content is
// invented, nothing inside a string literal is touched, and an unterminated
// string literal is not repaired (the value would be a guess). Callers that
// must not act on a guess should check repaired.
func ExtractRepairing(raw string) (text string, repaired bool, err error) {
	text, structural, excised, err := extractBalanced(raw)
	if err != nil {
		return "", false, err
	}
	return text, structural || excised, nil
}

// extractBalanced scans raw for the first complete, balanced JSON object.
// It returns the balanced text together with two independent signals:
//
//	structural — true when containers left open at the end of input were
//	    closed (a genuinely unbalanced/truncated payload). Strict callers must
//	    reject these.
//	excised — true when stray closing punctuation (e.g. the "]}" tail) was
//	    removed from an object that was otherwise naturally balanced. This is
//	    cosmetic cleanup, not a structural repair.
//
// It returns ErrNotFound when no balanced object is present.
func extractBalanced(raw string) (text string, structural, excised bool, err error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	// stack holds the closing punctuation owed by each open container, so a
	// truncated payload can be balanced without re-scanning.
	var stack []byte
	// drop holds byte offsets of stray closing punctuation — a ']' where an
	// object is open, the "]}" tail models emit when they confuse the single
	// and parallel action forms. Keeping it would return invalid JSON, so it
	// is excised from the result instead.
	var drop []int
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
			if len(stack) == 0 {
				start = i
			}
			stack = append(stack, '}')
		case '[':
			// Arrays only matter once inside the object; a bare leading '['
			// is not an object start, so it is ignored until then.
			if len(stack) > 0 {
				stack = append(stack, ']')
			}
		case ']':
			switch {
			case len(stack) > 0 && stack[len(stack)-1] == ']':
				stack = stack[:len(stack)-1]
			case len(stack) > 0:
				// An object is open, so this ']' closes nothing.
				drop = append(drop, i)
			}
		case '}':
			if len(stack) == 0 {
				// Stray '}' before the first object begins; ignore it so it
				// cannot drive depth negative and misalign the real object.
				continue
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 && start != -1 {
				// Object closed naturally; only cosmetic excision may remain.
				text, hadDrop, err := excise(trimmed[start:i+1], drop, start)
				if err != nil {
					return "", false, false, err
				}
				return text, false, hadDrop, nil
			}
		}
	}

	// Input ended with containers still open. Close them if the tail is
	// structurally repairable: an object was started, and we are not stranded
	// mid-string with a value we would have to invent.
	if start != -1 && len(stack) > 0 && !inString {
		text, hadDrop, err := excise(trimmed[start:], drop, start)
		if err != nil {
			return "", false, false, err
		}
		var b strings.Builder
		b.WriteString(text)
		for i := len(stack) - 1; i >= 0; i-- {
			b.WriteByte(stack[i])
		}
		return b.String(), true, hadDrop, nil
	}

	return "", false, false, ErrNotFound
}

// excise removes stray closing punctuation from an extracted object. offsets
// are absolute positions in the scanned text; start rebases them onto s.
func excise(s string, offsets []int, start int) (string, bool, error) {
	if len(offsets) == 0 {
		return s, false, nil
	}
	var b strings.Builder
	b.Grow(len(s))
	prev := 0
	for _, off := range offsets {
		rel := off - start
		if rel < prev || rel >= len(s) {
			continue
		}
		b.WriteString(s[prev:rel])
		prev = rel + 1
	}
	b.WriteString(s[prev:])
	return b.String(), true, nil
}
