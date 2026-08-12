package tui

import "strings"

// suggestionCap is the maximum rune length of a generated suggestion.
// Longer either/or options are skipped rather than truncated mid-word.
const suggestionCap = 60

// extractSuggestion derives a suggested next prompt from the final
// assistant message of a turn. Returns ok=false when no heuristic matches.
//
// Rule order (phase 1, deterministic):
//  1. Message ends with "?" → the model asked a question:
//     - contains "y/n", "yes/no", "should I", "shall I" → "yes"
//     - offers an either/or ("X or Y?") → the first option, verbatim
//  2. Message proposes an action ("I can...", "Want me to...", "Shall I...")
//     → "yes, go ahead"
//  3. No match → ok=false (phase 2 LLM fallback).
func extractSuggestion(lastAssistantMsg string) (suggestion string, ok bool) {
	msg := strings.TrimSpace(lastAssistantMsg)
	if msg == "" {
		return "", false
	}

	// The final sentence is the last non-empty line. Questions and
	// proposals that warrant a suggestion typically end the message.
	last := lastNonEmptyLine(msg)
	if last == "" {
		return "", false
	}
	// Never suggest from text inside a code block (e.g. a line ending in
	// "?" within a ``` fence).
	if inCodeBlock(msg, last) {
		return "", false
	}

	lower := strings.ToLower(last)
	endsWithQuestion := strings.HasSuffix(last, "?")

	// Rule 1: yes/no question.
	if endsWithQuestion {
		if containsAny(lower, "y/n", "yes/no", "should i", "shall i") {
			return "yes", true
		}
		// Rule 1b: either/or question → first option.
		if opt, ok := eitherOrOption(last); ok {
			return opt, true
		}
	}

	// Rule 2: action proposal.
	if containsAny(lower, "want me to", "shall i", "i can ") {
		return "yes, go ahead", true
	}

	return "", false
}

// lastNonEmptyLine returns the last non-empty line of s, trimmed.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// inCodeBlock reports whether the given line (a trimmed line of msg) sits
// inside a ```-fenced code block.
func inCodeBlock(msg, line string) bool {
	inFence := false
	for _, l := range strings.Split(msg, "\n") {
		trimmed := strings.TrimSpace(l)
		if trimmed == line {
			return inFence
		}
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
		}
	}
	return false
}

// eitherOrOption extracts the first option of an "X or Y?" question — the
// word immediately before the last " or ". Returns ok=false when there is no
// " or " or the option exceeds suggestionCap runes.
func eitherOrOption(s string) (string, bool) {
	idx := strings.LastIndex(strings.ToLower(s), " or ")
	if idx < 0 {
		return "", false
	}
	before := strings.TrimRight(s[:idx], " ")
	start := strings.LastIndexAny(before, " \t\n")
	opt := strings.TrimSpace(before[start+1:])
	if opt == "" {
		return "", false
	}
	if len([]rune(opt)) > suggestionCap {
		return "", false
	}
	return opt, true
}

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
