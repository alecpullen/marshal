package postmortempanel

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
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

// renderAgentStage advances to the agent question and renders it at a geometry
// where the complete disclosure and both choices fit, which is what an
// affirmative answer requires. No and Esc need no render.
func renderAgentStage(t *testing.T, p *Panel) {
	t.Helper()
	advance(t, p)
	if view := p.View(80, 24); !strings.Contains(view, "editing approval") {
		t.Fatalf("agent stage did not render its disclosure at 80x24:\n%s", view)
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
	renderAgentStage(t, p)
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
	// The affirmative answer needs the disclosure on screen at this geometry.
	p.View(80, 24)
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
			// The affirmative choice at the agent stage requires its complete
			// content to have been rendered at the current geometry.
			p.View(80, 24)
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

// TestAgentDisclosureAndChoicesFit pins the agent stage's rendering contract:
// wherever the disclosure is shown it is complete — both mandatory lines, the
// question and both choices — and the panel never exceeds the geometry it was
// given, in colour or in NO_COLOR.
func TestAgentDisclosureAndChoicesFit(t *testing.T) {
	prev := theme.Current()
	t.Cleanup(func() { theme.Reload(prev) })

	for _, noColor := range []bool{false, true} {
		t.Run(fmt.Sprintf("NO_COLOR=%v", noColor), func(t *testing.T) {
			if noColor {
				theme.Reload(theme.LoadFor(true, ""))
			} else {
				theme.Reload(prev)
			}
			for _, w := range []int{80, 120, 200} {
				for _, h := range []int{6, 9, 16, 24} {
					p := New()
					advance(t, p)
					view := p.View(w, h)
					plain := ansi.Strip(view)
					for _, want := range []string{
						"Include agent analysis?",
						"Agent analysis has system access and editing approval.",
						"It can access and edit files outside this workspace.",
						"Yes",
						"No",
					} {
						if !strings.Contains(plain, want) {
							t.Fatalf("%dx%d: %q missing from the agent stage:\n%s", w, h, want, plain)
						}
					}
					lines := strings.Split(view, "\n")
					if len(lines) > h {
						t.Errorf("%dx%d: panel is %d rows, want <= %d", w, h, len(lines), h)
					}
					for i, line := range lines {
						if got := ansi.StringWidth(line); got > w {
							t.Errorf("%dx%d: row %d is %d cells wide, want <= %d: %q",
								w, h, i, got, w, line)
						}
					}
				}
			}
		})
	}
}

// TestAgentConsentRequiresCurrentVisibleDisclosure pins the gate: the
// affirmative answer is refused until the complete agent-stage content has been
// rendered at the current geometry, and No/Esc stay available while it is not.
func TestAgentConsentRequiresCurrentVisibleDisclosure(t *testing.T) {
	// An unrendered agent stage cannot be consented to.
	for _, key := range []tea.KeyPressMsg{{Code: 'y'}, {Code: 'Y'}, {Code: tea.KeyEnter}} {
		p := New()
		advance(t, p)
		if got := press(t, p, key); got != nil {
			t.Fatalf("%q without a render emitted %+v, want refusal", key.String(), got)
		}
		if p.stage != 1 {
			t.Fatalf("%q without a render left the agent stage (stage %d)", key.String(), p.stage)
		}
		// Both safe exits still answer from the same state.
		for _, safe := range []tea.KeyPressMsg{{Code: 'n'}, {Code: tea.KeyEscape}} {
			q := New()
			advance(t, q)
			if got := press(t, q, safe); got == nil || got.Result != ResultExtractOnly {
				t.Fatalf("%q without a render = %+v, want ResultExtractOnly", safe.String(), got)
			}
		}
	}

	// A geometry too small for the disclosure cannot enable consent, and says
	// so instead of dropping it silently.
	for _, size := range []struct{ w, h int }{{40, 12}, {80, 5}, {80, 4}} {
		p := New()
		advance(t, p)
		view := ansi.Strip(p.View(size.w, size.h))
		if strings.Contains(view, "It can access and edit files outside this workspace.") {
			t.Fatalf("%dx%d rendered a disclosure it cannot fit:\n%s", size.w, size.h, view)
		}
		if !strings.Contains(view, "Resize") {
			t.Fatalf("%dx%d dropped the disclosure without a resize instruction:\n%s", size.w, size.h, view)
		}
		for _, key := range []tea.KeyPressMsg{{Code: 'y'}, {Code: tea.KeyEnter}} {
			if got := press(t, p, key); got != nil {
				t.Fatalf("%dx%d accepted %q = %+v, want refusal", size.w, size.h, key.String(), got)
			}
		}
		if got := press(t, p, tea.KeyPressMsg{Code: tea.KeyEscape}); got == nil || got.Result != ResultExtractOnly {
			t.Fatalf("%dx%d esc = %+v, want ResultExtractOnly", size.w, size.h, got)
		}
	}

	// InvalidateVisibility drops a rendering that was complete a moment ago.
	p := New()
	renderAgentStage(t, p)
	p.InvalidateVisibility()
	if got := press(t, p, tea.KeyPressMsg{Code: 'y'}); got != nil {
		t.Fatalf("y after InvalidateVisibility emitted %+v, want refusal", got)
	}
	// Navigation neither reveals the disclosure nor loses the selection.
	if got := press(t, p, tea.KeyPressMsg{Code: 'j'}); got != nil {
		t.Fatalf("cursor move emitted %+v, want nothing", got)
	}
	if got := press(t, p, tea.KeyPressMsg{Code: 'h'}); got != nil {
		t.Fatalf("cursor move emitted %+v, want nothing", got)
	}
	if got := press(t, p, tea.KeyPressMsg{Code: tea.KeyEnter}); got != nil {
		t.Fatalf("enter after only navigation emitted %+v, want refusal", got)
	}
	// A subsequent sufficient render restores consent, once.
	p.View(120, 40)
	if got := press(t, p, tea.KeyPressMsg{Code: tea.KeyEnter}); got == nil || got.Result != ResultWithAgent {
		t.Fatalf("enter after a sufficient render = %+v, want ResultWithAgent", got)
	}
}
