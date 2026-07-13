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
	if !strings.Contains(out, "Tab") || !strings.Contains(out, "?") || !strings.Contains(out, "/") {
		t.Fatalf("idle footer missing core hints: %q", out)
	}
	if strings.Contains(out, "cancel queued") {
		t.Fatalf("idle footer should not show busy-only hints: %q", out)
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

func TestOverlayEnumeratesAllBindings(t *testing.T) {
	out := stripANSI(Overlay(80, 24))
	for _, want := range []string{"Enter", "Shift+Enter", "/", "@", "Esc", "?", "Ctrl+O", "Ctrl+K", "Ctrl+G", "Ctrl+R", "Ctrl+X", "PgUp", "PgDn", "End"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help overlay missing %q:\n%s", want, out)
		}
	}
}
