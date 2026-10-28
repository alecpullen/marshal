package sddreview

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
	"marshal/internal/pipeline"
	"marshal/internal/sddauthor"
)

var testTheme = theme.LoadFor(false, "xterm-256color")

func init() {
	theme.Reload(testTheme)
}

func inspectionFixture() *pipeline.Inspection {
	return &pipeline.Inspection{
		Plan: &pipeline.Plan{Path: "/repo/.marshal/plans/feature.md", Slug: "feature"},
		IR: &pipeline.PlanIR{
			Tasks: []pipeline.TaskIR{
				{TaskN: 1, Title: "Add file", Operations: []pipeline.Operation{&pipeline.FileOp{Path: "created.txt"}}},
			},
		},
		Report: pipeline.CompileReport{
			Tasks: []pipeline.TaskReport{{TaskN: 1, Title: "Add file", DetOps: 1, EstCalls: 0}},
			Total: struct {
				DetOps   int
				AgentOps int
				EstCalls int
			}{DetOps: 1, AgentOps: 0, EstCalls: 0},
		},
		EffectiveStrategy: pipeline.StrategyAdaptive,
		HasMarshalBlocks:  true,
	}
}

func cleanResult() sddauthor.Result {
	return sddauthor.Result{
		Request:    sddauthor.Request{PlanPath: "/repo/.marshal/plans/feature.md"},
		Inspection: inspectionFixture(),
	}
}

func blockedResult() sddauthor.Result {
	insp := inspectionFixture()
	insp.Diagnostics = []pipeline.Diagnostic{{TaskN: 0, Severity: pipeline.DiagError, Message: "blocked"}}
	return sddauthor.Result{
		Request:    sddauthor.Request{PlanPath: "/repo/.marshal/plans/feature.md"},
		Inspection: insp,
	}
}

// rendered returns a panel that has been drawn once at a supported geometry,
// which is what earns the right to act on the discard key.
func rendered(t *testing.T, result sddauthor.Result) *Panel {
	t.Helper()
	p := New(result)
	p.View(80, 20)
	return p
}

func TestPanelRendersCandidateAndMetadata(t *testing.T) {
	p := rendered(t, cleanResult())
	out := ansi.Strip(p.View(80, 20))
	for _, want := range []string{"feature.md", "1 task", "deterministic", "1", "adaptive", "esc keep · d discard"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}

func TestEnterEmitsAcceptWhenNoErrors(t *testing.T) {
	p := New(cleanResult())
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should emit a command when inspection has no errors")
	}
	msg := cmd()
	accept, ok := msg.(AcceptMsg)
	if !ok {
		t.Fatalf("msg = %T, want AcceptMsg", msg)
	}
	if accept.Panel != p {
		t.Fatalf("AcceptMsg.Panel = %p, want the originating panel %p", accept.Panel, p)
	}
}

func TestEnterEmitsNoCommandWhenErrorsExist(t *testing.T) {
	p := New(blockedResult())
	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("Enter must not emit a command when inspection has errors")
	}
}

func TestEscEmitsKeep(t *testing.T) {
	p := rendered(t, cleanResult())
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc should emit a command")
	}
	keep, ok := cmd().(KeepMsg)
	if !ok {
		t.Fatalf("msg = %T, want KeepMsg", cmd())
	}
	if keep.Panel != p {
		t.Fatalf("KeepMsg.Panel = %p, want the originating panel %p", keep.Panel, p)
	}
}

// TestEscKeepsBlockedCandidate pins the recovery path: a candidate that cannot
// be accepted must still be keepable, because the user's next move is to edit
// it and run /sdd.
func TestEscKeepsBlockedCandidate(t *testing.T) {
	p := rendered(t, blockedResult())
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc must keep a blocked candidate")
	}
	if _, ok := cmd().(KeepMsg); !ok {
		t.Fatalf("msg = %T, want KeepMsg", cmd())
	}
}

// TestEscKeepsNilInspectionCandidate covers a result with no inspection at all:
// blocked for acceptance, still recoverable.
func TestEscKeepsNilInspectionCandidate(t *testing.T) {
	p := rendered(t, sddauthor.Result{Request: sddauthor.Request{PlanPath: "/repo/.marshal/plans/feature.md"}})
	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("a nil inspection must not be acceptable")
	}
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("a nil inspection must still be keepable")
	}
	if _, ok := cmd().(KeepMsg); !ok {
		t.Fatalf("msg = %T, want KeepMsg", cmd())
	}
}

func TestDiscardRequiresTwoConsecutiveKeys(t *testing.T) {
	p := rendered(t, cleanResult())

	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("first d must only arm the confirmation, got %T", cmd())
	}
	out := ansi.Strip(p.View(80, 20))
	if !strings.Contains(out, "d again to discard") {
		t.Fatalf("armed panel must state the confirmation:\n%s", out)
	}
	if !strings.Contains(out, "feature.md") {
		t.Fatalf("armed panel must name the candidate it would delete:\n%s", out)
	}

	cmd := p.Update(tea.KeyPressMsg{Code: 'd'})
	if cmd == nil {
		t.Fatal("second consecutive d must confirm")
	}
	discard, ok := cmd().(DiscardMsg)
	if !ok {
		t.Fatalf("msg = %T, want DiscardMsg", cmd())
	}
	if discard.Panel != p {
		t.Fatalf("DiscardMsg.Panel = %p, want the originating panel %p", discard.Panel, p)
	}
}

func TestDiscardAnyOtherKeyDisarms(t *testing.T) {
	for _, key := range []string{"j", "k", "enter", "x", "up"} {
		t.Run(key, func(t *testing.T) {
			p := rendered(t, cleanResult())
			if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
				t.Fatalf("first d must only arm, got %T", cmd())
			}
			// Any other key breaks the "two consecutive" pair. Enter may emit
			// AcceptMsg on a clean candidate, which is its own answer.
			p.Update(press(key))
			if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
				t.Fatalf("%q did not disarm the confirmation; next d emitted %T", key, cmd())
			}
		})
	}
}

// press maps a key name to the keypress the tests send.
func press(name string) tea.KeyPressMsg {
	codes := map[string]rune{
		"enter": tea.KeyEnter, "esc": tea.KeyEscape, "up": tea.KeyUp,
		"down": tea.KeyDown, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown,
	}
	if code, ok := codes[name]; ok {
		return tea.KeyPressMsg{Code: code}
	}
	return tea.KeyPressMsg{Code: rune(name[0])}
}

func TestEscWhileArmedOnlyDisarms(t *testing.T) {
	p := rendered(t, cleanResult())
	p.Update(tea.KeyPressMsg{Code: 'd'})
	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil {
		t.Fatalf("Esc must disarm the pending discard and stay, got %T", cmd())
	}
	// The panel stayed open and a further Esc keeps the candidate.
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("the second Esc must keep the candidate")
	}
	if _, ok := cmd().(KeepMsg); !ok {
		t.Fatalf("msg = %T, want KeepMsg", cmd())
	}
}

// TestDiscardRefusedBeforeAnyRender pins the visibility rule at the panel
// level: a panel that never drew its candidate cannot act on `d`.
func TestDiscardRefusedBeforeAnyRender(t *testing.T) {
	p := New(cleanResult())
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("unrendered panel must not arm, got %T", cmd())
	}
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("unrendered panel must not confirm, got %T", cmd())
	}
}

// TestDiscardRefusedBelowMinimumGeometry pins the undersized rule: a render
// that cannot show the candidate and the action does not earn the permission.
func TestDiscardRefusedBelowMinimumGeometry(t *testing.T) {
	p := rendered(t, cleanResult())
	p.View(80, 2) // below the header + body + action minimum
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("undersized render must not arm, got %T", cmd())
	}
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("undersized render must not confirm, got %T", cmd())
	}
}

// TestInvalidateVisibilityDisarms pins the resize rule: an arm does not
// survive a geometry change, and the next render re-earns it.
func TestInvalidateVisibilityDisarms(t *testing.T) {
	p := rendered(t, cleanResult())
	p.Update(tea.KeyPressMsg{Code: 'd'})
	p.InvalidateVisibility()
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("a disarmed panel must not confirm, got %T", cmd())
	}

	p.View(120, 40) // the fresh render re-earns the permission
	p.Update(tea.KeyPressMsg{Code: 'd'})
	cmd := p.Update(tea.KeyPressMsg{Code: 'd'})
	if cmd == nil {
		t.Fatal("a freshly rendered panel must be able to arm and confirm again")
	}
	if _, ok := cmd().(DiscardMsg); !ok {
		t.Fatalf("msg = %T, want DiscardMsg", cmd())
	}
}

func TestSetErrorDisplaysAndDisarms(t *testing.T) {
	p := rendered(t, cleanResult())
	p.Update(tea.KeyPressMsg{Code: 'd'})
	p.SetError(errors.New("sddauthor: candidate is not a regular file"))

	out := ansi.Strip(p.View(80, 20))
	if !strings.Contains(out, "discard failed") {
		t.Fatalf("failed discard must be reported in the panel:\n%s", out)
	}
	if !strings.Contains(out, "not a regular file") {
		t.Fatalf("failed discard must name the error:\n%s", out)
	}
	// Disarmed: the next d arms again rather than deleting on a stale arm.
	if cmd := p.Update(tea.KeyPressMsg{Code: 'd'}); cmd != nil {
		t.Fatalf("a failed discard must disarm, got %T", cmd())
	}
}

// TestSetErrorEscapesControlCharacters keeps a hostile error or path from
// reaching the terminal as an escape sequence.
func TestSetErrorEscapesControlCharacters(t *testing.T) {
	p := rendered(t, cleanResult())
	p.SetError(errors.New("bad\x1b[2Kpath"))
	out := p.View(80, 20)
	if strings.Contains(out, "\x1b[2K") {
		t.Fatalf("raw escape sequence reached the view:\n%q", out)
	}
}

func TestPanelNeverStartsRunner(t *testing.T) {
	// The panel has no reference to a runner; it only emits Accept/Keep/Discard.
	p := New(cleanResult())
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should emit a command")
	}
	if _, ok := cmd().(AcceptMsg); !ok {
		t.Fatalf("panel must emit AcceptMsg, not start a runner")
	}
}
