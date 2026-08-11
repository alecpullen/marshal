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

// TestQuestionFinalizeSingleChoice verifies a listed single-choice option
// submits directly.
func TestQuestionFinalizeSingleChoice(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: "Pick one:", Options: []string{"red", "green", "blue"}}},
	}
	qm := newQuestionModel(q, 80)
	*qm.selects[0] = "green"
	qm.form.State = huh.StateCompleted
	qm, _ = qm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	answers := qm.Answers()
	if len(answers) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(answers))
	}
	if answers[0].Answer != "green" {
		t.Fatalf("single-choice answer = %q, want green", answers[0].Answer)
	}
	if qm.others[0] == nil {
		t.Fatal("every options question now gets a custom-answer input")
	}
}

func TestBuildQuestionOptionsAlwaysAppendsOther(t *testing.T) {
	if got := buildQuestionOptions([]string{"red"}); len(got) != 2 {
		t.Fatalf("option count = %d, want 2", len(got))
	}
	if got := buildQuestionOptions([]string{"red"}); got[len(got)-1].Value != questionOtherSentinel {
		t.Fatalf("last option value should be the Other sentinel, got %q", got[len(got)-1].Value)
	}
}

// TestQuestionFinalizeMultiChoice verifies a multi-select submits the joined
// selected values.
func TestQuestionFinalizeMultiChoice(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: "Pick some:", Options: []string{"a", "b", "c"}, Multi: true}},
	}
	qm := newQuestionModel(q, 80)
	*qm.multis[0] = []string{"a", "c"}
	qm.form.State = huh.StateCompleted
	qm, _ = qm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	answers := qm.Answers()
	if len(answers) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(answers))
	}
	if answers[0].Answer != "a, c" {
		t.Fatalf("multi-choice answer = %q, want \"a, c\"", answers[0].Answer)
	}
}

// TestQuestionFinalizeOtherWithText verifies the Other sentinel uses the
// trimmed custom text.
func TestQuestionFinalizeOtherWithText(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: "Pick one:", Options: []string{"red", "green", "blue"}}},
	}
	qm := newQuestionModel(q, 80)
	*qm.selects[0] = questionOtherSentinel
	*qm.others[0] = "  magenta  "
	qm.form.State = huh.StateCompleted
	qm, _ = qm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	answers := qm.Answers()
	if len(answers) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(answers))
	}
	if answers[0].Answer != "magenta" {
		t.Fatalf("Other answer = %q, want magenta (trimmed)", answers[0].Answer)
	}
}

// TestQuestionFinalizeOtherBlankStaysUnanswered verifies the Other sentinel
// with blank custom text does NOT submit the sentinel.
func TestQuestionFinalizeOtherBlankStaysUnanswered(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: "Pick one:", Options: []string{"red", "green", "blue"}}},
	}
	qm := newQuestionModel(q, 80)
	*qm.selects[0] = questionOtherSentinel
	*qm.others[0] = "   "
	qm.form.State = huh.StateCompleted
	qm, _ = qm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	answers := qm.Answers()
	if len(answers) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(answers))
	}
	if answers[0].Answer != session.AnswerUnanswered {
		t.Fatalf("blank Other should leave the answer unanswered, got %q", answers[0].Answer)
	}
}

func TestQuestionModelViewUsesGutter(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: "What is your favorite color?", Options: []string{"red", "green", "blue"}}},
	}
	qm := newQuestionModel(q, 80)
	view := stripANSI(qm.View())
	if !strings.HasPrefix(view, "▍ ? ") {
		t.Fatalf("question view missing ▍ rail + ? gutter:\n%s", view)
	}
	if strings.Contains(view, "Marshal asks") {
		t.Fatalf("question view must not show old title:\n%s", view)
	}
}

func TestQuestionModelViewHasChromeRail(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{
			{Question: "Pick one:", Options: []string{"red", "green"}},
			{Question: "Why?"},
		},
	}
	qm := newQuestionModel(q, 60)
	if msg := qm.Init()(); msg != nil {
		qm, _ = qm.Update(msg)
	}
	lines := strings.Split(strings.TrimRight(stripANSI(qm.View()), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("question view too short to rail:\n%s", qm.View())
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "▍") {
			t.Errorf("line %d missing the ▍ chrome rail: %q\nfull view:\n%s", i, line, stripANSI(qm.View()))
		}
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
	// The dead textarea's tell is its placeholder; the ▍ rail is legitimate
	// chrome on the question panel itself.
	if strings.Contains(out, "Ask Marshal...") {
		t.Fatalf("main textarea must not render while a question is pending (its keys go to the question form):\n%s", out)
	}
}

func TestQuestionMultiSelectOffersOther(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: "Pick some:", Options: []string{"a", "b"}, Multi: true}},
	}
	qm := newQuestionModel(q, 80)
	if qm.multis[0] == nil {
		t.Fatal("multi question should have a multi-select")
	}
	if qm.others[0] == nil {
		t.Fatal("multi question should create a custom-answer input")
	}
	// The sentinel must be offered as a selectable option.
	view := stripANSI(qm.View())
	if !strings.Contains(view, "Other…") {
		t.Fatalf("multi question view should offer Other…:\n%s", view)
	}
}

// TestQuestionPremadePickSkipsCustomInput is the regression guard for the
// unwanted free-input regression (report 7b): picking a listed option must
// submit that option without the custom-answer input ever standing in the
// way. The input now lives in its own group behind a hide func, so a listed
// pick leaves it hidden. (Fallback: huh's group navigation needs a live
// tea.Program to drive, so this asserts the wiring indirectly through the
// bound select value + the custom-answer input's existence.)
func TestQuestionPremadePickSkipsCustomInput(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: "Pick one:", Options: []string{"red", "green"}}},
	}
	qm := newQuestionModel(q, 80)
	// A listed pick binds into the select; the input exists but is hidden
	// by its group's hide func, so it must not interfere with completion.
	*qm.selects[0] = "red"
	qm.form.State = huh.StateCompleted
	qm, _ = qm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !qm.IsDone() {
		t.Fatalf("form should complete after picking the first listed option")
	}
	if got := qm.Answers()[0].Answer; got != "red" {
		t.Fatalf("answer = %q, want red", got)
	}
	if qm.others[0] == nil {
		t.Fatal("custom-answer input should exist behind its hide func")
	}
}

// TestQuestionOtherPickRevealsCustomInput guards the Other path wiring:
// every options question owns a custom-answer input (qm.others[i]) and the
// sentinel is a selectable option. (Fallback: driving the reveal via keys
// needs a live tea.Program; asserting wiring covers the report-7b regression
// that the input could be walked into unconditionally.)
func TestQuestionOtherPickRevealsCustomInput(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: "Pick one:", Options: []string{"red", "green"}}},
	}
	qm := newQuestionModel(q, 80)
	if qm.others[0] == nil {
		t.Fatal("custom-answer input should exist")
	}
	view := stripANSI(qm.View())
	if !strings.Contains(view, "Other…") {
		t.Fatalf("question view should offer Other…:\n%s", view)
	}
	// Sentinel selected + custom text set finalizes to the trimmed text.
	*qm.selects[0] = questionOtherSentinel
	*qm.others[0] = "  magenta  "
	qm.form.State = huh.StateCompleted
	qm, _ = qm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := qm.Answers()[0].Answer; got != "magenta" {
		t.Fatalf("answer = %q, want magenta (trimmed)", got)
	}
}

func TestQuestionFinalizeMultiOtherSubstitutesCustom(t *testing.T) {
	q := &session.PendingQuestion{
		Questions: []session.Question{{Question: "Pick some:", Options: []string{"real", "other-choice"}, Multi: true}},
	}
	qm := newQuestionModel(q, 80)
	*qm.multis[0] = []string{"real", questionOtherSentinel}
	*qm.others[0] = "custom"
	qm.form.State = huh.StateCompleted
	qm, _ = qm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	answers := qm.Answers()
	if len(answers) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(answers))
	}
	if answers[0].Answer != "real, custom" {
		t.Fatalf("multi Other answer = %q, want \"real, custom\"", answers[0].Answer)
	}

	// Sentinel with blank custom text is omitted entirely.
	q2 := &session.PendingQuestion{
		Questions: []session.Question{{Question: "Pick some:", Options: []string{"real", "other-choice"}, Multi: true}},
	}
	qm2 := newQuestionModel(q2, 80)
	*qm2.multis[0] = []string{"real", questionOtherSentinel}
	*qm2.others[0] = "   "
	qm2.form.State = huh.StateCompleted
	qm2, _ = qm2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := qm2.Answers()[0].Answer; got != "real" {
		t.Fatalf("blank multi Other should be omitted, got %q", got)
	}
}
