package gatepanel

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
)

func testGate() session.SDDGate {
	return session.SDDGate{
		TaskN:     3,
		TaskTitle: "Add the retry helper",
		Question:  "Should this reuse the existing backoff in internal/retry?",
		Report: "STATUS: NEEDS_CONTEXT\n" +
			"TESTS: none yet\n" +
			"QUESTION: Should this reuse the existing backoff in internal/retry?",
	}
}

// key builds a key press for a single rune, mirroring the picker test helper.
func key(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// typeInto feeds each rune of s into the panel as a key press.
func typeInto(t *testing.T, p *Panel, s string) {
	t.Helper()
	for _, r := range s {
		p.Update(key(r))
	}
}

// StripANSI removes ANSI escape sequences so tests can assert on plain text.
func StripANSI(s string) string { return ansi.Strip(s) }

func TestPanelShowsQuestionWithContext(t *testing.T) {
	got := StripANSI(New(testGate()).View(80, 20))
	for _, want := range []string{
		"Task 3", "Add the retry helper",
		"Should this reuse the existing backoff",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("panel missing %q — the user cannot answer a question they cannot see:\n%s", want, got)
		}
	}
}

func TestPanelShowsExplicitKeyHints(t *testing.T) {
	got := StripANSI(New(testGate()).View(80, 20))
	if !strings.Contains(got, "answer") {
		t.Errorf("panel must advertise that enter answers:\n%s", got)
	}
	if !strings.Contains(strings.ToLower(got), "esc") || !strings.Contains(got, "stop") {
		t.Errorf("panel must advertise that esc stops the run:\n%s", got)
	}
}

func TestEnterEmitsAnswer(t *testing.T) {
	p := New(testGate())
	typeInto(t, p, "reuse it")

	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter with a non-empty answer must emit a command")
	}
	msg, ok := cmd().(AnswerMsg)
	if !ok {
		t.Fatalf("got %T, want AnswerMsg", cmd())
	}
	if msg.Text != "reuse it" {
		t.Errorf("AnswerMsg.Text = %q, want %q", msg.Text, "reuse it")
	}
}

func TestEnterOnEmptyAnswerDoesNothing(t *testing.T) {
	p := New(testGate())
	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		if _, ok := cmd().(AnswerMsg); ok {
			t.Error("an empty answer must not resume the run")
		}
	}
}

func TestEscStopsTheRun(t *testing.T) {
	p := New(testGate())
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc must emit a command")
	}
	if _, ok := cmd().(StopMsg); !ok {
		t.Fatalf("got %T, want StopMsg", cmd())
	}
}

func TestReportIsShownButSecondaryToTheQuestion(t *testing.T) {
	got := StripANSI(New(testGate()).View(80, 24))
	qi := strings.Index(got, "Should this reuse")
	ri := strings.Index(got, "STATUS: NEEDS_CONTEXT")
	if qi < 0 {
		t.Fatalf("question missing:\n%s", got)
	}
	if ri >= 0 && ri < qi {
		t.Errorf("the report must not precede the question:\n%s", got)
	}
}

func TestPanelDegradesInASmallHeight(t *testing.T) {
	got := StripANSI(New(testGate()).View(60, 5))
	if got == "" {
		t.Error("panel must render something at height 5")
	}
	if len(strings.Split(strings.TrimRight(got, "\n"), "\n")) > 5 {
		t.Errorf("panel exceeded its height budget:\n%s", got)
	}
}
