package tui

import "strings"

// Confidence grades a deterministic suggestion. It exists so the Phase 2
// LLM fallback can be gated on the rules' *quality* rather than on their
// silence: a low-confidence heuristic hit still gets a second opinion.
type Confidence int

const (
	// ConfidenceNone means no rule matched; there is no suggestion.
	ConfidenceNone Confidence = iota
	// ConfidenceLow means a heuristic over free text matched. Its text is
	// shown immediately, and in "llm" mode the model is asked to replace it.
	ConfidenceLow
	// ConfidenceHigh means the message is unambiguously a yes/no question.
	// There is nothing a model would improve, so no fallback runs.
	ConfidenceHigh
)

// extractSuggestion derives a suggested next prompt from the final
// assistant message of a turn, along with how much to trust it.
//
// Rule order (phase 1, deterministic). Both rules require the last line to
// be a question:
//  1. Contains "y/n", "yes/no", "should I" or "shall I"
//     → "yes", ConfidenceHigh.
//  2. Proposes an action ("Want me to ...")
//     → "yes, go ahead", ConfidenceLow.
//  3. Otherwise → "", ConfidenceNone, and the LLM fallback takes over.
//
// There is deliberately no either/or rule. Taking "the word before the
// last ' or '" produced ungrammatical single words — "...use the accessor
// approach or blank the slots?" yielded "approach" — and clause-boundary
// detection by string matching is not reliable enough to replace it.
// Either/or questions fall through to the LLM, which reads the whole
// question.
func extractSuggestion(lastAssistantMsg string) (string, Confidence) {
	msg := strings.TrimSpace(lastAssistantMsg)
	if msg == "" {
		return "", ConfidenceNone
	}

	// The final sentence is the last non-empty line. Questions and
	// proposals that warrant a suggestion typically end the message.
	last := lastNonEmptyLine(msg)
	if last == "" {
		return "", ConfidenceNone
	}
	// Never suggest from text inside a code block (e.g. a line ending in
	// "?" within a ``` fence).
	if inCodeBlock(msg, last) {
		return "", ConfidenceNone
	}
	// Both rules require a question mark. Without it, rule 2's matchers
	// fired on plain declarative prose ("I can see why that test failed.")
	// — and, worse, suppressed the LLM fallback by reporting success.
	if !strings.HasSuffix(last, "?") {
		return "", ConfidenceNone
	}

	lower := strings.ToLower(last)

	// Rule 1: yes/no question.
	if containsAny(lower, "y/n", "yes/no", "should i", "shall i") {
		return "yes", ConfidenceHigh
	}

	// Rule 2: action proposal. "shall i" is not repeated here — rule 1
	// already claims every question containing it. "i can " is dropped
	// outright: it is the only matcher that hits non-proposals even with a
	// "?" present ("Do you know if I can retry?").
	if containsAny(lower, "want me to") {
		return "yes, go ahead", ConfidenceLow
	}

	return "", ConfidenceNone
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

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
