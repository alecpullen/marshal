package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"marshal/internal/app/session"
)

func TestQuestionModelInitReturnsCommand(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{
			{Question: "What is your favorite color?"},
		},
		ResponseChan: make(chan []session.Answer, 1),
	}
	qm := newQuestionModel(q, 80)

	cmd := qm.Init()
	if cmd == nil {
		t.Fatal("Init() should return a non-nil command")
	}

	// Execute the command to produce a message that the Bubble Tea
	// runtime would normally process.
	msg := cmd()
	if msg == nil {
		t.Fatal("executing Init() command should return a non-nil message")
	}

	// Feed the message back to the form to verify it processes
	// without error. The form should remain in normal state.
	_, updateCmd := qm.form.Update(msg)
	if qm.form.State != huh.StateNormal {
		t.Fatal("form should remain in normal state after processing Init() message")
	}
	_ = updateCmd

	// Verify the first field is focused after Init() is performed.
	focused := qm.form.GetFocusedField()
	if focused == nil {
		t.Fatal("GetFocusedField() should return a non-nil field after Init()")
	}
}

func TestQuestionSubFormMarksDoneWithoutDispatching(t *testing.T) {
	ch := make(chan []session.Answer, 1)
	q := &session.PendingQuestion{
		Questions: []session.Question{
			{Question: "What is your favorite color?"},
		},
		ResponseChan: ch,
	}
	qm := newQuestionModel(q, 80)

	// Esc should mark done + aborted without writing to the channel.
	qm, _ = qm.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !qm.IsDone() {
		t.Fatal("esc should set done")
	}
	if !qm.Aborted() {
		t.Fatal("esc should set aborted")
	}
	select {
	case got := <-ch:
		t.Fatalf("sub-form dispatched on esc: %v", got)
	default:
	}

	// Reset and test the completion path.
	// Drive the form to completion by setting the form state directly
	// (huh's internal state machine requires a running tea.Program to
	// finalize via commands; in a unit test we simulate the terminal
	// state that the parent would see after the command executes).
	ch2 := make(chan []session.Answer, 1)
	q2 := &session.PendingQuestion{
		Questions: []session.Question{
			{
				Question: "Pick one:",
				Options:  []string{"red", "green", "blue"},
			},
		},
		ResponseChan: ch2,
	}
	qm2 := newQuestionModel(q2, 80)
	// Set the value through the bound pointer and force the form to
	// StateCompleted, then call Update to verify the model marks done
	// without dispatching.
	*qm2.selects[0] = "red"
	qm2.form.State = huh.StateCompleted
	qm2, _ = qm2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !qm2.IsDone() {
		t.Fatal("completion should set done")
	}
	if qm2.Aborted() {
		t.Fatal("completion should not set aborted")
	}
	answers := qm2.Answers()
	if len(answers) != 1 {
		t.Fatalf("expected 1 answer; got %d", len(answers))
	}
	if answers[0].Answer != "red" {
		t.Fatalf("expected answer 'red'; got %q", answers[0].Answer)
	}
	select {
	case got := <-ch2:
		t.Fatalf("sub-form dispatched on completion: %v", got)
	default:
	}
}

func TestQuestionModelViewUsesGutter(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: "What is your favorite color?", Options: []string{"red", "green", "blue"}}},
	}
	qm := newQuestionModel(q, 80)
	view := stripANSI(qm.View())
	if !strings.HasPrefix(view, " ? ") {
		t.Fatalf("question view missing ? gutter:\n%s", view)
	}
	if strings.Contains(view, "Marshal asks") {
		t.Fatalf("question view must not show old title:\n%s", view)
	}
	if !strings.Contains(view, "red / green / blue") {
		t.Fatalf("question view missing options:\n%s", view)
	}
}
