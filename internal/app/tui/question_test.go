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
}

func TestQuestionModelViewWrapsLongQuestion(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("wrapme ", 30))
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: long}},
	}
	qm := newQuestionModel(q, 40)
	view := stripANSI(qm.View())
	for _, line := range strings.Split(view, "\n") {
		if w := len([]rune(line)); w > 40 {
			t.Fatalf("question view line exceeds width 40 (%d cells): %q\nfull view:\n%s", w, line, view)
		}
	}
}

func TestQuestionModelViewRendersMarkdown(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: "**Pick** one approach:"}},
	}
	qm := newQuestionModel(q, 80)
	if strings.Contains(qm.View(), "**") {
		t.Fatalf("markdown markers must not render literally:\n%s", qm.View())
	}
}

func TestQuestionModelViewEchoesTypedInput(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: "What is your name?"}},
	}
	qm := newQuestionModel(q, 80)
	if msg := qm.Init()(); msg != nil {
		qm, _ = qm.Update(msg)
	}
	for _, r := range "hello" {
		qm, _ = qm.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	view := stripANSI(qm.View())
	if !strings.Contains(view, "hello") {
		t.Fatalf("question view must echo the in-progress typed answer:\n%s", view)
	}
}

func TestQuestionModelViewShowsInteractiveOptions(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: "Pick one:", Options: []string{"red", "green", "blue"}}},
	}
	qm := newQuestionModel(q, 80)
	view := stripANSI(qm.View())
	if strings.Contains(view, "red / green / blue") {
		t.Fatalf("static option summary must be replaced by an interactive list:\n%s", view)
	}
	for _, opt := range []string{"red", "green", "blue"} {
		if !strings.Contains(view, opt) {
			t.Fatalf("question view missing option %q:\n%s", opt, view)
		}
	}
}

func TestRenderInputAreaHidesTextareaWhileQuestionPending(t *testing.T) {
	m := newTestModel(t)
	q := &session.PendingQuestion{
		Questions:    []session.Question{{Question: "What is your name?"}},
		ResponseChan: make(chan []session.Answer, 1),
	}
	m.state.SetPendingQuestion(q)
	m.questionModel = newQuestionModel(q, 76)
	out := stripANSI(m.renderInputArea())
	if strings.Contains(out, "▍") {
		t.Fatalf("main textarea must not render while a question is pending (its keys go to the question form):\n%s", out)
	}
}
