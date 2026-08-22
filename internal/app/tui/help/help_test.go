package help

import (
	"strings"
	"testing"

	"marshal/internal/app/tui/theme"
)

// stripANSI removes all ANSI escape sequences from text so test assertions
// can match on visible runes without lipgloss styling interfering.
func stripANSI(s string) string { return theme.ANSIRe.ReplaceAllString(s, "") }

func TestFooterIdle(t *testing.T) {
	out := stripANSI(Footer(FooterHints{}))
	for _, want := range []string{"Tab mode", "/ cmd", "? help"} {
		if !strings.Contains(out, want) {
			t.Fatalf("idle footer missing %q: %q", want, out)
		}
	}
	// The idle set is deliberately minimal; the full cheatsheet lives
	// behind ? (/help). These must NOT appear:
	for _, gone := range []string{"Alt+M", "@", "Ctrl+G"} {
		if strings.Contains(out, gone) {
			t.Fatalf("idle footer still shows %q (moved behind ?): %q", gone, out)
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

func TestFooterIdleShowsRollbackWhenEligible(t *testing.T) {
	out := stripANSI(Footer(FooterHints{IdleRollbackEligible: true}))
	if !strings.Contains(out, "Ctrl+R") {
		t.Fatalf("idle footer missing Ctrl+R: %q", out)
	}
}

func TestFooterApprovalWording(t *testing.T) {
	out := stripANSI(Footer(FooterHints{ApprovalPending: true}))
	if strings.Contains(out, "Enter×2") {
		t.Fatalf("stale 'Enter×2' label still present: %q", out)
	}
	// One hint per key: the footer used to emit Enter twice in the same row,
	// labelled "arm" and "submit".
	if strings.Count(out, "Enter") != 1 {
		t.Fatalf("expected exactly one Enter hint, got %q", out)
	}
	if !strings.Contains(out, "Enter confirm") {
		t.Fatalf("expected 'Enter confirm', got %q", out)
	}
}

func TestFooterIdleShowsClearQueue(t *testing.T) {
	out := stripANSI(Footer(FooterHints{QueueNonEmpty: true}))
	if !strings.Contains(out, "Ctrl+X") || !strings.Contains(out, "clear queue") {
		t.Fatalf("footer missing clear-queue hint:\n%s", out)
	}
}

func TestFooterShowsTodoToggleWhenTodosActive(t *testing.T) {
	out := Footer(FooterHints{TodosActive: true})
	if !strings.Contains(out, "Ctrl+T") || !strings.Contains(out, "tasks") {
		t.Fatalf("idle footer should hint the todo toggle:\n%s", out)
	}
	if busy := Footer(FooterHints{Busy: true, TodosActive: true}); strings.Contains(busy, "Ctrl+T") {
		t.Fatalf("busy footer must keep its cancel-focused hints:\n%s", busy)
	}
}
