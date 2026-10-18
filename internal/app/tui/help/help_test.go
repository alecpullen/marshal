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
	if !strings.Contains(out, "Ctrl+C stop") || !strings.Contains(out, "Ctrl+X clear queue") {
		t.Fatalf("busy footer missing cancel/queue hints: %q", out)
	}
	// Enter queues; it does not interrupt. The label must say so, because
	// Alt+Enter is the key that interrupts.
	if !strings.Contains(out, "Enter queue") {
		t.Fatalf("busy footer missing the queue hint: %q", out)
	}
	if strings.Contains(out, "Alt+Enter") {
		t.Fatalf("busy footer offered send now without a supported turn: %q", out)
	}
}

// TestFooterSendNowRequiresCapability pins the gate on the interruption hint:
// a supported busy turn advertises both actions with distinct labels, and a
// turn the runtime cannot interrupt advertises only the queueing one.
func TestFooterSendNowRequiresCapability(t *testing.T) {
	supported := stripANSI(Footer(FooterHints{Busy: true, SendNowAvailable: true}))
	if !strings.Contains(supported, "Enter queue") {
		t.Fatalf("supported busy footer lost the queue hint: %q", supported)
	}
	if !strings.Contains(supported, "Alt+Enter send now") {
		t.Fatalf("supported busy footer missing the send-now hint: %q", supported)
	}

	// Idle: the busy actions do not apply at all.
	idle := stripANSI(Footer(FooterHints{SendNowAvailable: true}))
	if strings.Contains(idle, "send now") {
		t.Fatalf("idle footer advertised send now: %q", idle)
	}

	// Busy but unsupported: the runtime has no interruption handler, so the
	// hint would promise an action that cannot happen.
	unsupported := stripANSI(Footer(FooterHints{Busy: true}))
	if strings.Contains(unsupported, "send now") {
		t.Fatalf("unsupported busy footer advertised send now: %q", unsupported)
	}
}

// TestFooterPromptKeepsAnswerAndSendNowDistinct pins that a pending prompt
// keeps exactly one ordinary answer key and gains a separate interruption hint,
// so the two actions never collapse into one ambiguous label.
func TestFooterPromptKeepsAnswerAndSendNowDistinct(t *testing.T) {
	out := stripANSI(Footer(FooterHints{QuestionPending: true, SendNowAvailable: true}))
	if got := strings.Count(out, "Enter answer"); got != 1 {
		t.Fatalf("question footer has %d answer hints, want exactly 1: %q", got, out)
	}
	if !strings.Contains(out, "Alt+Enter send now") {
		t.Fatalf("question footer missing the separate send-now hint: %q", out)
	}

	// The same distinction for an approval prompt: confirm stays the ordinary
	// answer, and send now is its own key.
	approval := stripANSI(Footer(FooterHints{ApprovalPending: true, SendNowAvailable: true}))
	if got := strings.Count(approval, "Enter confirm"); got != 1 {
		t.Fatalf("approval footer has %d confirm hints, want exactly 1: %q", got, approval)
	}
	if !strings.Contains(approval, "Alt+Enter send now") {
		t.Fatalf("approval footer missing the separate send-now hint: %q", approval)
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
