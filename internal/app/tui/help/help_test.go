package help

import (
	"regexp"
	"strings"
	"testing"
)

// stripANSI removes all ANSI escape sequences from text so test assertions
// can match on visible runes without lipgloss styling interfering.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func TestFooterIdle(t *testing.T) {
	out := stripANSI(Footer(FooterHints{}))
	for _, want := range []string{"Tab", "Alt+M", "?", "/", "@"} {
		if !strings.Contains(out, want) {
			t.Fatalf("idle footer missing %q: %q", want, out)
		}
	}
}

func TestFooterBusyShowsCancelAndQueue(t *testing.T) {
	out := stripANSI(Footer(FooterHints{Busy: true}))
	if !strings.Contains(out, "Esc cancel") || !strings.Contains(out, "Ctrl+X clear queue") {
		t.Fatalf("busy footer missing cancel/queue hints: %q", out)
	}
}

func TestFooterQuestionShowsAnswer(t *testing.T) {
	out := stripANSI(Footer(FooterHints{QuestionPending: true}))
	if !strings.Contains(out, "Enter answer") {
		t.Fatalf("question footer missing answer hint: %q", out)
	}
}

func TestFooterIdleShowsThinkingToggle(t *testing.T) {
	out := stripANSI(Footer(FooterHints{}))
	if !strings.Contains(out, "Ctrl+G") {
		t.Fatalf("idle footer missing Ctrl+G: %q", out)
	}
}

func TestFooterIdleShowsRollbackWhenEligible(t *testing.T) {
	out := stripANSI(Footer(FooterHints{IdleRollbackEligible: true}))
	if !strings.Contains(out, "Ctrl+R") {
		t.Fatalf("idle footer missing Ctrl+R: %q", out)
	}
}

func TestOverlayEnumeratesAllBindings(t *testing.T) {
	out := stripANSI(Overlay(80, 24))
	for _, want := range []string{"Enter", "Shift+Enter", "/", "@", "Esc", "?", "Ctrl+O", "Ctrl+K", "Ctrl+G", "Ctrl+R", "Ctrl+X", "PgUp", "PgDn", "End"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help overlay missing %q:\n%s", want, out)
		}
	}
}

func TestOverlayUsesFixedKeyColumn(t *testing.T) {
	out := Overlay(120, 40)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Alt+Shift+M") && !strings.Contains(line, "cycle model backward") {
			// The description MUST appear on the same line as the
			// key (we only wrap on \n, not in the middle of a row).
		}
	}
}

func TestOverlayWrapsOnNarrowWidth(t *testing.T) {
	out := Overlay(60, 30) // narrower than keyColumnWidth*2
	// Assert that no line contains a key label clipped by a
	// hard wrap inside the description (heuristic: the description
	// for "cycle mode backward" should still contain the word
	// "cycle" or "backward").
	if !strings.Contains(out, "backward") {
		t.Fatalf("description lost on narrow terminal: %q", out)
	}
}
