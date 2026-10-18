package postmortempanel

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// press drives one keypress through the panel. When the panel emits a
// terminal answer it returns that DoneMsg; a key that leaves the question
// open returns nil.
func press(t *testing.T, p *Panel, key tea.KeyPressMsg) *DoneMsg {
	t.Helper()
	cmd := p.Update(key)
	if cmd == nil {
		return nil
	}
	msg := cmd()
	done, ok := msg.(DoneMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want postmortempanel.DoneMsg", msg)
	}
	return &done
}

// advance moves the panel from the first question to the agent question.
func advance(t *testing.T, p *Panel) {
	t.Helper()
	if got := press(t, p, tea.KeyPressMsg{Code: 'y'}); got != nil {
		t.Fatalf("y at stage 0 emitted %+v, want advancement without an answer", got)
	}
	if p.stage != 1 {
		t.Fatalf("stage = %d, want 1 after y", p.stage)
	}
}

func TestStageZeroNoDeclines(t *testing.T) {
	p := New()
	got := press(t, p, tea.KeyPressMsg{Code: 'n'})
	if got == nil || got.Result != ResultDeclined {
		t.Fatalf("n at stage 0 = %+v, want ResultDeclined", got)
	}
}

func TestStageZeroEscDeclines(t *testing.T) {
	p := New()
	got := press(t, p, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got == nil || got.Result != ResultDeclined {
		t.Fatalf("esc at stage 0 = %+v, want ResultDeclined", got)
	}
}

func TestStageZeroYesAdvances(t *testing.T) {
	p := New()
	if got := press(t, p, tea.KeyPressMsg{Code: 'y'}); got != nil {
		t.Fatalf("y at stage 0 emitted %+v, want no answer yet", got)
	}
	if p.stage != 1 {
		t.Fatalf("stage = %d, want 1 after y", p.stage)
	}
}

func TestStageOneYesRunsAgent(t *testing.T) {
	p := New()
	advance(t, p)
	got := press(t, p, tea.KeyPressMsg{Code: 'y'})
	if got == nil || got.Result != ResultWithAgent {
		t.Fatalf("y at stage 1 = %+v, want ResultWithAgent", got)
	}
}

func TestStageOneNoExtractsOnly(t *testing.T) {
	p := New()
	advance(t, p)
	got := press(t, p, tea.KeyPressMsg{Code: 'n'})
	if got == nil || got.Result != ResultExtractOnly {
		t.Fatalf("n at stage 1 = %+v, want ResultExtractOnly", got)
	}
}

func TestStageOneEscExtractsOnly(t *testing.T) {
	p := New()
	advance(t, p)
	got := press(t, p, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got == nil || got.Result != ResultExtractOnly {
		t.Fatalf("esc at stage 1 = %+v, want ResultExtractOnly", got)
	}
}

func TestEnterConfirmsCursor(t *testing.T) {
	// Cursor 0 (Yes) advances from stage 0, then runs the agent pass.
	p := New()
	if got := press(t, p, tea.KeyPressMsg{Code: tea.KeyEnter}); got != nil {
		t.Fatalf("enter on Yes at stage 0 emitted %+v, want advancement", got)
	}
	if p.stage != 1 {
		t.Fatalf("stage = %d, want 1 after enter on Yes", p.stage)
	}
	if got := press(t, p, tea.KeyPressMsg{Code: tea.KeyEnter}); got == nil || got.Result != ResultWithAgent {
		t.Fatalf("enter on Yes at stage 1 = %+v, want ResultWithAgent", got)
	}

	// Cursor 1 (No) takes the extract-only path.
	p = New()
	advance(t, p)
	if got := press(t, p, tea.KeyPressMsg{Code: 'j'}); got != nil {
		t.Fatalf("moving the cursor emitted %+v, want no answer", got)
	}
	got := press(t, p, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got == nil || got.Result != ResultExtractOnly {
		t.Fatalf("enter on No = %+v, want ResultExtractOnly", got)
	}
}

func TestCursorMovesAndClamps(t *testing.T) {
	// Every back key from cursor 0 clamps there rather than wrapping.
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyLeft}, {Code: 'h'}, {Code: tea.KeyUp}, {Code: 'k'},
	} {
		p := New()
		press(t, p, key)
		if p.cursor != 0 {
			t.Fatalf("%q from cursor 0 moved cursor to %d, want clamp at 0", key.String(), p.cursor)
		}
	}

	// Every forward key moves 0 → 1 and then clamps at 1.
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyRight}, {Code: 'l'}, {Code: tea.KeyDown}, {Code: 'j'},
	} {
		p := New()
		press(t, p, key)
		if p.cursor != 1 {
			t.Fatalf("%q from cursor 0 moved cursor to %d, want 1", key.String(), p.cursor)
		}
		press(t, p, key)
		if p.cursor != 1 {
			t.Fatalf("%q past cursor 1 moved cursor to %d, want clamp at 1", key.String(), p.cursor)
		}
	}

	// A back key from cursor 1 returns to 0.
	p := New()
	press(t, p, tea.KeyPressMsg{Code: 'j'})
	press(t, p, tea.KeyPressMsg{Code: tea.KeyLeft})
	if p.cursor != 0 {
		t.Fatalf("left from cursor 1 moved cursor to %d, want 0", p.cursor)
	}
}

func TestNonKeyMessagesAreIgnored(t *testing.T) {
	p := New()
	if cmd := p.Update(struct{}{}); cmd != nil {
		t.Fatalf("non-key message returned a cmd (%T), want nil", cmd)
	}
	if p.stage != 0 || p.cursor != 0 {
		t.Fatalf("non-key message changed state: stage %d cursor %d", p.stage, p.cursor)
	}
}

// TestBothChoicesVisibleAndKeyboardReachableAtRealSizes pins the exit question's
// contract: at the sizes the frame tests sweep, both answers stay visible and
// each is reachable by keyboard at BOTH stages — including the agent stage,
// whose disclosure paragraph is the longest content the panel renders.
func TestBothChoicesVisibleAndKeyboardReachableAtRealSizes(t *testing.T) {
	// answer drives the panel to a terminal result, choosing Yes (forward
	// false) or No (forward true) at whatever stage it starts from.
	answer := func(t *testing.T, startStage int, yes bool) Result {
		t.Helper()
		p := New()
		for p.stage < startStage {
			press(t, p, tea.KeyPressMsg{Code: 'y'})
		}
		if !yes {
			press(t, p, tea.KeyPressMsg{Code: 'j'})
		}
		for {
			got := press(t, p, tea.KeyPressMsg{Code: tea.KeyEnter})
			if got != nil {
				return got.Result
			}
			// Stage 0: the panel advanced rather than answered.
			if !yes {
				press(t, p, tea.KeyPressMsg{Code: 'j'})
			}
		}
	}

	for _, maxHeight := range []int{6, 9, 16, 24} {
		for stage := 0; stage < 2; stage++ {
			p := New()
			if stage == 1 {
				press(t, p, tea.KeyPressMsg{Code: 'y'})
			}
			view := p.View(80, maxHeight)
			for _, want := range []string{"Yes", "No"} {
				if !strings.Contains(view, want) {
					t.Fatalf("stage %d at maxHeight %d: %q missing:\n%s", stage, maxHeight, want, view)
				}
			}

			yes, no := answer(t, stage, true), answer(t, stage, false)
			if yes == no {
				t.Fatalf("stage %d: both answers produced %v; the choices are not distinct", stage, yes)
			}
		}
	}
}
