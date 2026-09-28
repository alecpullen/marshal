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
	out := stripANSI(Footer(FooterHints{
		Busy: true,
		Actions: []Hint{
			{Key: "Esc", Label: "cancel", Priority: PriorityEssential},
			{Key: "Ctrl+X", Label: "clear queue", Priority: PriorityEssential},
		},
	}))
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
	out := stripANSI(Footer(FooterHints{
		Actions: []Hint{{Key: "Ctrl+R", Label: "rollback", Priority: PriorityOptional}},
	}))
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
	out := stripANSI(Footer(FooterHints{
		Busy:    true,
		Actions: []Hint{{Key: "Ctrl+X", Label: "clear queue", Priority: PriorityEssential}},
	}))
	if !strings.Contains(out, "Ctrl+X") || !strings.Contains(out, "clear queue") {
		t.Fatalf("footer missing clear-queue hint:\n%s", out)
	}
}

func TestFooterShowsTodoToggleWhenTodosActive(t *testing.T) {
	todoHint := Hint{Key: "Ctrl+T", Label: "tasks", Priority: PriorityOptional}
	out := Footer(FooterHints{Actions: []Hint{todoHint}})
	if !strings.Contains(out, "Ctrl+T") || !strings.Contains(out, "tasks") {
		t.Fatalf("idle footer should hint the todo toggle:\n%s", out)
	}
	// A form owns the keys while an approval is pending, so the chat-action
	// hints (including the todo toggle) are not listed.
	busy := Footer(FooterHints{ApprovalPending: true, Actions: []Hint{todoHint}})
	if strings.Contains(busy, "Ctrl+T") {
		t.Fatalf("form footer must show the form's own keys:\n%s", busy)
	}
}

// The Ctrl+X hint must come from the single resolved action, so a state with
// two candidate verbs prints one of them. This is the duplicate-hint bug that
// the action catalog exists to prevent.
func TestFooterPrintsOneCtrlXHint(t *testing.T) {
	// The caller resolves the action; the footer renders what it is given. A
	// caller that passed both would be wrong, so this pins the rendered
	// contract for the single-hint case.
	out := stripANSI(Footer(FooterHints{
		Busy: true,
		Actions: []Hint{
			{Key: "Enter", Label: "send", Priority: PriorityEssential},
			{Key: "Esc", Label: "cancel", Priority: PriorityEssential},
			{Key: "Ctrl+X", Label: "stop agent", Priority: PriorityEssential},
		},
	}))
	if strings.Count(out, "Ctrl+X") != 1 {
		t.Fatalf("expected exactly one Ctrl+X hint, got %q", out)
	}
	if strings.Contains(out, "clear queue") {
		t.Fatalf("footer advertises a second Ctrl+X verb: %q", out)
	}
}

// FooterParts carries priorities so the status line can shed one hint at a
// time. Losing the whole cluster at 80 columns dropped essential actions.
func TestFooterPartsAssignsPriorities(t *testing.T) {
	parts := FooterParts(FooterHints{Busy: true})
	if len(parts) == 0 {
		t.Fatal("busy footer produced no parts")
	}
	var sawOptional, sawEssential bool
	for _, p := range parts {
		switch p.Priority {
		case PriorityOptional:
			sawOptional = true
		case PriorityEssential:
			sawEssential = true
		}
	}
	if !sawEssential {
		t.Fatalf("busy footer has no essential part: %+v", parts)
	}
	if !sawOptional {
		t.Fatalf("busy footer has no optional part to shed first: %+v", parts)
	}
	// The help hint is last so it is the rightmost thing on the row.
	last := parts[len(parts)-1]
	if last.Key != "?" {
		t.Fatalf("last busy-footer part = %q, want the help hint", last.Key)
	}
}
