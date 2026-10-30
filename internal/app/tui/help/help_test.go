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

// TestFooterStopOnly pins the narrow-terminal fallback: while busy, the stop
// hint is the only pair the footer keeps, and the flag does nothing when idle.
func TestFooterStopOnly(t *testing.T) {
	want := pair("Ctrl+C", "stop")
	if got := Footer(FooterHints{Busy: true, StopOnly: true}); got != want {
		t.Fatalf("busy stop-only footer = %q, want exactly %q", got, want)
	}
	if got, plain := Footer(FooterHints{StopOnly: true}), Footer(FooterHints{}); got != plain {
		t.Fatalf("StopOnly without Busy changed the footer: %q, want %q", got, plain)
	}
}

func TestFooterBusyShowsCancelAndQueue(t *testing.T) {
	out := stripANSI(Footer(FooterHints{Busy: true, CtrlXHint: CtrlXHintClearQueue}))
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
	if got := strings.Count(approval, "Enter twice confirm"); got != 1 {
		t.Fatalf("approval footer has %d confirm hints, want exactly 1: %q", got, approval)
	}
	if !strings.Contains(approval, "Alt+Enter send now") {
		t.Fatalf("approval footer missing the separate send-now hint: %q", approval)
	}
}

// TestFooterQuestionShowsAnswer pins the question prompt's key pairs: Enter
// answers, and Escape's label says what it actually does — skip the remaining
// questions, keeping the ones already answered.
func TestFooterQuestionShowsAnswer(t *testing.T) {
	out := stripANSI(Footer(FooterHints{QuestionPending: true}))
	if !strings.Contains(out, "Enter answer") {
		t.Fatalf("question footer missing answer hint: %q", out)
	}
	if !strings.Contains(out, "Esc skip remaining") {
		t.Fatalf("question footer missing the skip-remaining hint: %q", out)
	}
	if strings.Contains(out, "skip ·") || strings.HasSuffix(out, "Esc skip") {
		t.Fatalf("question footer still advertises a bare skip: %q", out)
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
	// Both gestures are two presses. A single-press label made the first press
	// of an armed control look like a key that did nothing.
	if !strings.Contains(out, "Enter twice confirm") {
		t.Fatalf("expected 'Enter twice confirm', got %q", out)
	}
	if !strings.Contains(out, "Esc twice deny") {
		t.Fatalf("expected 'Esc twice deny', got %q", out)
	}
	// Navigation is unchanged: arrows still choose among the allowed actions.
	if !strings.Contains(out, "←→ choose") {
		t.Fatalf("approval footer lost the navigation hint: %q", out)
	}
}

// TestFooterIdleNeverShowsClearQueue pins that idle never advertises Ctrl+X:
// the key does nothing while no turn runs, so a hint would be a dead key.
func TestFooterIdleNeverShowsClearQueue(t *testing.T) {
	out := stripANSI(Footer(FooterHints{CtrlXHint: CtrlXHintClearQueue}))
	if strings.Contains(out, "Ctrl+X") {
		t.Fatalf("idle footer advertised Ctrl+X:\n%s", out)
	}
	if strings.Contains(out, "clear queue") {
		t.Fatalf("idle footer advertised a queue action:\n%s", out)
	}
}

// TestFooterCtrlXMatchesAction pins the presentation rule: the footer shows at
// most one Ctrl+X pair, it names the caller's single decision, and it appears
// only in the branches where the global handler still receives the key.
func TestFooterCtrlXMatchesAction(t *testing.T) {
	cases := []struct {
		name string
		h    FooterHints
		// want is the label the Ctrl+X pair must carry, or "" for no pair.
		want string
		// exact, when set, is the whole expected footer.
		exact string
	}{
		{name: "idle none", h: FooterHints{}},
		{name: "idle clear queue", h: FooterHints{CtrlXHint: CtrlXHintClearQueue}},
		{name: "idle stop agent", h: FooterHints{CtrlXHint: CtrlXHintStopAgent}},
		{name: "busy none", h: FooterHints{Busy: true}},
		{name: "busy stop agent", h: FooterHints{Busy: true, CtrlXHint: CtrlXHintStopAgent}, want: "stop agent"},
		{name: "busy clear queue", h: FooterHints{Busy: true, CtrlXHint: CtrlXHintClearQueue}, want: "clear queue"},
		{
			name: "busy keeps send now and inspect agent",
			h:    FooterHints{Busy: true, CtrlXHint: CtrlXHintStopAgent, SendNowAvailable: true, RunActive: true},
			want: "stop agent",
		},
		{name: "browse none", h: FooterHints{Browsing: true}},
		{name: "browse stop agent", h: FooterHints{Browsing: true, CtrlXHint: CtrlXHintStopAgent}, want: "stop agent"},
		{name: "browse clear queue", h: FooterHints{Browsing: true, CtrlXHint: CtrlXHintClearQueue}, want: "clear queue"},
		{name: "question prompt", h: FooterHints{Busy: true, QuestionPending: true, CtrlXHint: CtrlXHintClearQueue}},
		{name: "skill gate", h: FooterHints{Busy: true, SkillGatePending: true, CtrlXHint: CtrlXHintClearQueue}},
		{name: "approval prompt", h: FooterHints{Busy: true, ApprovalPending: true, CtrlXHint: CtrlXHintStopAgent}},
		{
			name: "command editor",
			h:    FooterHints{Busy: true, ApprovalPending: true, EditingCommand: true, CtrlXHint: CtrlXHintClearQueue},
		},
		{name: "completion popup", h: FooterHints{Busy: true, PopupOpen: true, CtrlXHint: CtrlXHintClearQueue}},
		{
			name:  "keys in frame",
			h:     FooterHints{Busy: true, QuestionPending: true, KeysInFrame: true, CtrlXHint: CtrlXHintClearQueue},
			exact: pair("Ctrl+C", "stop"),
		},
		{
			name:  "stop only",
			h:     FooterHints{Busy: true, StopOnly: true, CtrlXHint: CtrlXHintClearQueue},
			exact: pair("Ctrl+C", "stop"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := stripANSI(Footer(tc.h))
			if tc.exact != "" {
				if want := stripANSI(tc.exact); out != want {
					t.Fatalf("footer = %q, want exactly %q", out, want)
				}
			}
			// At most one Ctrl+X key, whatever the state.
			if got := strings.Count(out, "Ctrl+X"); got > 1 {
				t.Fatalf("footer shows %d Ctrl+X keys, want at most 1: %q", got, out)
			}
			// And never the label that is not the current action.
			for _, label := range []string{"stop agent", "clear queue"} {
				if label != tc.want && strings.Contains(out, label) {
					t.Fatalf("footer shows the %q label, want only %q: %q", label, tc.want, out)
				}
			}
			if tc.want == "" {
				if strings.Contains(out, "Ctrl+X") {
					t.Fatalf("footer advertised Ctrl+X with no action: %q", out)
				}
				return
			}
			if got := strings.Count(out, "Ctrl+X "+tc.want); got != 1 {
				t.Fatalf("footer has %d complete %q pairs, want exactly 1: %q", got, "Ctrl+X "+tc.want, out)
			}
		})
	}
}

func TestFooterHasNoCtrlT(t *testing.T) {
	states := []FooterHints{
		{},
		{Busy: true},
		{CtrlXHint: CtrlXHintClearQueue},
		{IdleRollbackEligible: true},
		{Browsing: true},
		{RunActive: true},
	}
	for _, h := range states {
		if out := Footer(h); strings.Contains(out, "Ctrl+T") {
			t.Fatalf("no footer state may advertise Ctrl+T: %+v → %q", h, out)
		}
	}
}

func TestFooterMouseHintNamesDragWhileCaptured(t *testing.T) {
	captured := stripANSI(Footer(FooterHints{}))
	if !strings.Contains(captured, "drag copy") || strings.Contains(captured, "Ctrl+S select") {
		t.Fatalf("captured footer should offer drag-to-copy: %q", captured)
	}
	released := stripANSI(Footer(FooterHints{MouseReleased: true}))
	if !strings.Contains(released, "Ctrl+S scroll") {
		t.Fatalf("released footer should offer Ctrl+S to scroll: %q", released)
	}
}

// TestPromptKeysMatchTheFooter: the console draws the same key pairs in the
// prompt section's header that the footer used to show, from one source.
func TestPromptKeysMatchTheFooter(t *testing.T) {
	for _, h := range []FooterHints{
		{QuestionPending: true},
		{QuestionPending: true, SendNowAvailable: true},
		{SkillGatePending: true},
		{SkillGatePending: true, SendNowAvailable: true},
		{ApprovalPending: true},
		{ApprovalPending: true, SendNowAvailable: true},
	} {
		if got, want := PromptKeys(h), Footer(h); got != want {
			t.Fatalf("PromptKeys(%+v) = %q, want the footer's %q", h, got, want)
		}
	}
	if got := PromptKeys(FooterHints{Busy: true}); got != "" {
		t.Fatalf("PromptKeys with no prompt = %q, want empty", got)
	}
	if got := PromptKeys(FooterHints{ApprovalPending: true, EditingCommand: true}); got != "" {
		t.Fatalf("PromptKeys while editing a command = %q, want empty", got)
	}
}

// TestFooterKeysInFrame: once a pending prompt's keys ride in its console
// header, the footer keeps only the stop key while busy and nothing while idle.
func TestFooterKeysInFrame(t *testing.T) {
	busy := FooterHints{QuestionPending: true, KeysInFrame: true, Busy: true}
	if got, want := Footer(busy), pair("Ctrl+C", "stop"); got != want {
		t.Fatalf("busy keys-in-frame footer = %q, want %q", got, want)
	}
	idle := FooterHints{QuestionPending: true, KeysInFrame: true}
	if got := Footer(idle); got != "" {
		t.Fatalf("idle keys-in-frame footer = %q, want empty", got)
	}
	// Prompt-free states are unchanged by the flag.
	for _, h := range []FooterHints{{}, {Busy: true}, {PopupOpen: true}} {
		withFlag := h
		withFlag.KeysInFrame = true
		if got, want := Footer(withFlag), Footer(h); got != want {
			t.Fatalf("KeysInFrame changed a prompt-free footer: got %q, want %q", got, want)
		}
	}
}
