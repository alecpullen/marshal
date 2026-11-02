package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/help"
)

func TestQuestionModelInitReturnsCommand(t *testing.T) {
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{
			{Question: "What is your favorite color?"},
		},
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
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{
			{Question: "What is your favorite color?"},
		},
	}
	qm := newQuestionModel(q, 80)

	// Esc should mark done + aborted.
	qm, _ = qm.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !qm.IsDone() {
		t.Fatal("esc should set done")
	}
	if !qm.Aborted() {
		t.Fatal("esc should set aborted")
	}

	// Reset and test the completion path.
	// Drive the form to completion by setting the form state directly
	// (huh's internal state machine requires a running tea.Program to
	// finalize via commands; in a unit test we simulate the terminal
	// state that the parent would see after the command executes).
	q2 := acpwire.QuestionRequest{
		Questions: []acpwire.Question{
			{
				Question: "Pick one:",
				Options:  []acpwire.QuestionOption{{Label: "red"}, {Label: "green"}, {Label: "blue"}},
			},
		},
	}
	qm2 := newQuestionModel(q2, 80)
	// Set the value through the bound pointer and force the form to
	// StateCompleted, then call Update to verify the model marks done.
	// The bound value is the generated option identity, not the label.
	*qm2.selects[0] = questionOptionValue(0)
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
}

// TestQuestionFinalizeSingleChoice verifies a listed single-choice option
// submits directly.
func TestQuestionFinalizeSingleChoice(t *testing.T) {
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: "Pick one:", Options: []acpwire.QuestionOption{{Label: "red"}, {Label: "green"}, {Label: "blue"}}}},
	}
	qm := newQuestionModel(q, 80)
	*qm.selects[0] = questionOptionValue(1)
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
	if got := buildQuestionOptions([]acpwire.QuestionOption{{Label: "red"}}); len(got) != 2 {
		t.Fatalf("option count = %d, want 2", len(got))
	}
	if got := buildQuestionOptions([]acpwire.QuestionOption{{Label: "red"}}); got[len(got)-1].Value != questionOtherSentinel {
		t.Fatalf("last option value should be the Other sentinel, got %q", got[len(got)-1].Value)
	}
}

// TestBuildQuestionOptionsFoldsDescription verifies an option's description
// is rendered in the visible key while the value stays the bare label, so a
// recorded answer never carries the description.
func TestBuildQuestionOptionsFoldsDescription(t *testing.T) {
	opts := buildQuestionOptions([]acpwire.QuestionOption{
		{Label: "red", Description: "the warm one"},
		{Label: "green"},
	})
	if len(opts) != 3 {
		t.Fatalf("option count = %d, want 3", len(opts))
	}
	if opts[0].Key != "red — the warm one" {
		t.Fatalf("key = %q, want %q", opts[0].Key, "red — the warm one")
	}
	if opts[0].Value != questionOptionValue(0) {
		t.Fatalf("value = %q, want the generated identity %q", opts[0].Value, questionOptionValue(0))
	}
	if opts[1].Key != "green" {
		t.Fatalf("key = %q, want bare label %q (no separator)", opts[1].Key, "green")
	}
	if opts[1].Value != questionOptionValue(1) {
		t.Fatalf("value = %q, want %q", opts[1].Value, questionOptionValue(1))
	}
}

// TestQuestionModelViewShowsOptionDescriptions verifies the description is
// visible in the rendered question view.
func TestQuestionModelViewShowsOptionDescriptions(t *testing.T) {
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: "Pick one:", Options: []acpwire.QuestionOption{
			{Label: "red", Description: "the warm one"},
			{Label: "green"},
		}}},
	}
	qm := newQuestionModel(q, 80)
	view := stripANSI(qm.View())
	if !strings.Contains(view, "the warm one") {
		t.Fatalf("question view missing option description:\n%s", view)
	}
	if !strings.Contains(view, "red") {
		t.Fatalf("question view missing option label:\n%s", view)
	}
}

// TestQuestionFinalizeMultiChoice verifies a multi-select submits the joined
// selected values.
func TestQuestionFinalizeMultiChoice(t *testing.T) {
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: "Pick some:", Options: []acpwire.QuestionOption{{Label: "a"}, {Label: "b"}, {Label: "c"}}, Multi: true}},
	}
	qm := newQuestionModel(q, 80)
	*qm.multis[0] = []string{questionOptionValue(0), questionOptionValue(2)}
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
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: "Pick one:", Options: []acpwire.QuestionOption{{Label: "red"}, {Label: "green"}, {Label: "blue"}}}},
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
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: "Pick one:", Options: []acpwire.QuestionOption{{Label: "red"}, {Label: "green"}, {Label: "blue"}}}},
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
	if answers[0].Answer != acpwire.AnswerUnanswered {
		t.Fatalf("blank Other should leave the answer unanswered, got %q", answers[0].Answer)
	}
}

func TestQuestionModelViewUsesGutter(t *testing.T) {
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: "What is your favorite color?", Options: []acpwire.QuestionOption{{Label: "red"}, {Label: "green"}, {Label: "blue"}}}},
	}
	qm := newQuestionModel(q, 80)
	view := stripANSI(qm.View())
	if !strings.HasPrefix(view, " ? ") {
		t.Fatalf("question view missing the ? gutter:\n%s", view)
	}
	if strings.Contains(view, "Marshal asks") {
		t.Fatalf("question view must not show old title:\n%s", view)
	}
}

func TestQuestionModelViewHasNoRail(t *testing.T) {
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{
			{Question: "Pick one:", Options: []acpwire.QuestionOption{{Label: "red"}, {Label: "green"}}},
			{Question: "Why?"},
		},
	}
	qm := newQuestionModel(q, 60)
	if msg := qm.Init()(); msg != nil {
		qm, _ = qm.Update(msg)
	}
	view := stripANSI(qm.View())
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("question view too short:\n%s", view)
	}
	for i, line := range lines {
		if strings.HasPrefix(line, "▍") {
			t.Errorf("line %d still wears the ▍ rail: %q\nfull view:\n%s", i, line, view)
		}
	}
}

func TestQuestionModelViewWrapsLongQuestion(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("wrapme ", 30))
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: long}},
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
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: "**Pick** one approach:"}},
	}
	qm := newQuestionModel(q, 80)
	if strings.Contains(qm.View(), "**") {
		t.Fatalf("markdown markers must not render literally:\n%s", qm.View())
	}
}

func TestQuestionModelViewEchoesTypedInput(t *testing.T) {
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: "What is your name?"}},
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
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: "Pick one:", Options: []acpwire.QuestionOption{{Label: "red"}, {Label: "green"}, {Label: "blue"}}}},
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
	m.questionModel = newQuestionModel(acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: "What is your name?"}},
	}, 76)
	out := stripANSI(m.renderInputArea())
	// The dead textarea's tell is its placeholder; the ▍ rail is legitimate
	// chrome on the question panel itself.
	if strings.Contains(out, "Ask Marshal...") {
		t.Fatalf("main textarea must not render while a question is pending (its keys go to the question form):\n%s", out)
	}
}

func TestQuestionMultiSelectOffersOther(t *testing.T) {
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: "Pick some:", Options: []acpwire.QuestionOption{{Label: "a"}, {Label: "b"}}, Multi: true}},
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

// TestQuestionSingleChoiceNavigation drives, through the real form, the two
// single-choice paths whose interaction coverage used to be simulated: a
// listed option submits directly and never focuses the custom field, while the
// appended Other… row reveals the custom field and its trimmed text becomes the
// answer. Every command the form returns is executed (timer-only commands such
// as the cursor blink are skipped), so this fails if the form's own navigation
// stops reaching either path.
func TestQuestionSingleChoiceNavigation(t *testing.T) {
	options := []acpwire.QuestionOption{{Label: "red"}, {Label: "green"}}

	t.Run("listed", func(t *testing.T) {
		q := acpwire.QuestionRequest{
			Questions: []acpwire.Question{{Question: "Pick one:", Options: options}},
		}
		qm := questionStart(t, newQuestionModel(q, 80))
		// Row 0 is red, row 1 is green, and the appended Other… row is 2.
		qm = questionForm(t, qm, questionMoveDown(1)...)
		if got := qm.form.GetFocusedField(); got != qm.fields[0][0] {
			t.Fatal("focus left the option list while navigating a listed pick")
		}
		qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter})
		if !qm.IsDone() {
			t.Fatalf("a listed option must submit the form; state = %v", qm.form.State)
		}
		if got := qm.Answers()[0].Answer; got != "green" {
			t.Fatalf("answer = %q, want green", got)
		}
		if got := qm.form.GetFocusedField(); got == qm.fields[0][1] {
			t.Fatal("a listed pick must never focus the custom field")
		}
		if got := *qm.others[0]; got != "" {
			t.Fatalf("custom input received %q for a listed pick", got)
		}
	})

	t.Run("custom", func(t *testing.T) {
		q := acpwire.QuestionRequest{
			Questions: []acpwire.Question{{Question: "Pick one:", Options: options}},
		}
		qm := questionStart(t, newQuestionModel(q, 80))
		qm = questionForm(t, qm, questionMoveDown(len(options))...)
		qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter})
		if qm.IsDone() {
			t.Fatal("picking the Other… row must reveal the custom input, not complete the form")
		}
		if got := qm.form.GetFocusedField(); got != qm.fields[0][1] {
			t.Fatal("custom input is not focused after picking the Other… row")
		}
		qm = questionType(t, qm, "  magenta  ")
		qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter})
		if got := qm.Answers()[0].Answer; got != "magenta" {
			t.Fatalf("answer = %q, want magenta (trimmed)", got)
		}
	})
}

// questionForm drives the real huh form through questionModel.Update: every
// command the form returns is executed and its message fed back in, which is
// what the Bubble Tea runtime does. Poking the bound values instead would pass
// even when the form's own navigation no longer reaches them.
func questionForm(t *testing.T, qm *questionModel, msgs ...tea.Msg) *questionModel {
	t.Helper()
	for _, msg := range msgs {
		var cmd tea.Cmd
		qm, cmd = qm.Update(msg)
		qm = questionRunCmd(t, qm, cmd, 0)
	}
	return qm
}

// questionStart runs the form's Init, which focuses its first field.
func questionStart(t *testing.T, qm *questionModel) *questionModel {
	t.Helper()
	return questionRunCmd(t, qm, qm.Init(), 0)
}

func questionRunCmd(t *testing.T, qm *questionModel, cmd tea.Cmd, depth int) *questionModel {
	t.Helper()
	if qm == nil || cmd == nil || qm.IsDone() || depth > 256 {
		return qm
	}
	msg, ok := questionExecCmd(cmd)
	if !ok {
		// A timer-driven command (the input cursor's blink, a spinner tick):
		// it carries no form state and would otherwise stall the test.
		return qm
	}
	if msg == nil {
		return qm
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			qm = questionRunCmd(t, qm, c, depth+1)
		}
		return qm
	}
	var next tea.Cmd
	qm, next = qm.Update(msg)
	return questionRunCmd(t, qm, next, depth+1)
}

// questionExecCmd runs a command, giving up on it after a short deadline. huh
// returns timer commands alongside its state changes; those block for their
// interval and would make driving the form slow, so they are dropped.
func questionExecCmd(cmd tea.Cmd) (tea.Msg, bool) {
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg, true
	case <-time.After(100 * time.Millisecond):
		return nil, false
	}
}

// questionMoveDown returns the key messages that walk a list down n rows.
func questionMoveDown(n int) []tea.Msg {
	msgs := make([]tea.Msg, 0, n)
	for i := 0; i < n; i++ {
		msgs = append(msgs, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	return msgs
}

// questionToggle is the multi-select toggle key.
func questionToggle() tea.Msg {
	return tea.KeyPressMsg{Code: 'x', Text: "x"}
}

// questionType feeds text into the focused field one rune at a time.
func questionType(t *testing.T, qm *questionModel, text string) *questionModel {
	t.Helper()
	for _, r := range text {
		qm = questionForm(t, qm, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return qm
}

// TestQuestionListedOtherDoesNotOpenCustomInput is the regression guard for a
// supplied option whose label collides with the custom-answer sentinel: the
// label must stay a listed choice, submit directly, and never open or record
// the custom-answer input.
func TestQuestionListedOtherDoesNotOpenCustomInput(t *testing.T) {
	options := []acpwire.QuestionOption{
		{Label: "other"},
		{Label: "Other…"},
		{Label: "custom"},
		{Label: "option:0"},
	}
	for _, row := range []int{0, 1, 2, 3} {
		t.Run(options[row].Label, func(t *testing.T) {
			q := acpwire.QuestionRequest{
				Questions: []acpwire.Question{{Question: "Pick one:", Options: options}},
			}
			qm := questionStart(t, newQuestionModel(q, 80))
			qm = questionForm(t, qm, questionMoveDown(row)...)
			if got := qm.form.GetFocusedField(); got != qm.fields[0][0] {
				t.Fatalf("focus left the option list on row %d", row)
			}
			qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter})
			if !qm.IsDone() {
				t.Fatalf("a listed option must submit the form; state = %v", qm.form.State)
			}
			if got, want := qm.Answers()[0].Answer, options[row].Label; got != want {
				t.Fatalf("answer = %q, want the listed label %q", got, want)
			}
			if got := *qm.others[0]; got != "" {
				t.Fatalf("custom input received %q for a listed option", got)
			}
		})
	}
}

// TestQuestionMultiOtherAndCustomAreDistinct covers the multi-select half of
// the same collision: listed labels that look like the sentinel, duplicate
// labels, and the appended custom row all stay separate choices, and only the
// custom row reveals the input.
func TestQuestionMultiOtherAndCustomAreDistinct(t *testing.T) {
	options := []acpwire.QuestionOption{
		{Label: "other"},
		{Label: "custom", Description: "a listed choice"},
		{Label: "dup"},
		{Label: "dup"},
	}
	// Rendered keys are unique, so huh cannot toggle two rows together.
	keys := questionOptionKeys(options)
	seen := map[string]bool{questionCustomLabel: true}
	for i, k := range keys {
		if seen[k] {
			t.Fatalf("rendered keys are not unique: key %d = %q (%v)", i, k, keys)
		}
		seen[k] = true
	}
	if got, want := keys[2], "dup (option 3)"; got != want {
		t.Fatalf("first duplicate key = %q, want %q", got, want)
	}
	if got, want := keys[3], "dup (option 4)"; got != want {
		t.Fatalf("second duplicate key = %q, want %q", got, want)
	}

	customRow := len(options)
	newMulti := func(t *testing.T) *questionModel {
		t.Helper()
		q := acpwire.QuestionRequest{
			Questions: []acpwire.Question{{Question: "Pick some:", Options: options, Multi: true}},
		}
		return questionStart(t, newQuestionModel(q, 80))
	}

	t.Run("listed other submits its label", func(t *testing.T) {
		qm := questionForm(t, newMulti(t), questionToggle())
		qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter})
		if !qm.IsDone() {
			t.Fatalf("listed other must submit without the custom input; state = %v", qm.form.State)
		}
		if got := qm.Answers()[0].Answer; got != "other" {
			t.Fatalf("answer = %q, want %q", got, "other")
		}
	})

	t.Run("listed custom label is not the custom answer", func(t *testing.T) {
		qm := questionForm(t, newMulti(t), questionMoveDown(1)...)
		qm = questionForm(t, qm, questionToggle())
		qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter})
		if !qm.IsDone() {
			t.Fatalf("a listed %q option must submit without the custom input; state = %v", "custom", qm.form.State)
		}
		if got := qm.Answers()[0].Answer; got != "custom" {
			t.Fatalf("answer = %q, want the listed label %q", got, "custom")
		}
	})

	t.Run("duplicate labels toggle independently", func(t *testing.T) {
		qm := questionForm(t, newMulti(t), questionMoveDown(2)...)
		qm = questionForm(t, qm, questionToggle())
		if got := len(*qm.multis[0]); got != 1 {
			t.Fatalf("toggling one duplicate row selected %d rows, want 1", got)
		}
		qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter})
		if got := qm.Answers()[0].Answer; got != "dup" {
			t.Fatalf("answer = %q, want %q", got, "dup")
		}
	})

	t.Run("custom row reveals its input and its text is the answer", func(t *testing.T) {
		qm := questionForm(t, newMulti(t), questionMoveDown(customRow)...)
		qm = questionForm(t, qm, questionToggle())
		qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter})
		if qm.IsDone() {
			t.Fatal("picking the custom row must reveal the custom input, not complete the form")
		}
		if got := qm.form.GetFocusedField(); got != qm.fields[0][1] {
			t.Fatalf("custom input is not focused after picking the custom row")
		}
		qm = questionType(t, qm, "bespoke")
		qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter})
		if got := qm.Answers()[0].Answer; got != "bespoke" {
			t.Fatalf("answer = %q, want the custom text %q", got, "bespoke")
		}
	})

	t.Run("listed other and custom text stay separate answers", func(t *testing.T) {
		qm := questionForm(t, newMulti(t), questionToggle())
		qm = questionForm(t, qm, questionMoveDown(customRow)...)
		qm = questionForm(t, qm, questionToggle())
		qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter})
		qm = questionType(t, qm, "bespoke")
		qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter})
		if got, want := qm.Answers()[0].Answer, "other, bespoke"; got != want {
			t.Fatalf("answer = %q, want %q", got, want)
		}
	})

	t.Run("blank custom text is omitted", func(t *testing.T) {
		qm := questionForm(t, newMulti(t), questionMoveDown(customRow)...)
		qm = questionForm(t, qm, questionToggle())
		qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter})
		qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter})
		if got := qm.Answers()[0].Answer; got != acpwire.AnswerUnanswered {
			t.Fatalf("answer = %q, want %q", got, acpwire.AnswerUnanswered)
		}
	})
}

// TestQuestionInvalidOptionIdentityIsNotAnAnswer pins that only a canonical,
// in-range generated identity names a supplied option: a malformed or stale
// value is never recorded as if the user had typed it.
func TestQuestionInvalidOptionIdentityIsNotAnAnswer(t *testing.T) {
	options := []acpwire.QuestionOption{{Label: "red"}, {Label: "green"}}
	for _, value := range []string{
		"option:2",   // out of range
		"option:-1",  // out of range
		"option:+1",  // not canonical
		"option:01",  // not canonical
		"option:",    // no index
		"option:red", // not an index
		"Option:0",   // wrong prefix
		"red",        // a bare label is no longer a bound value
		"",           // untouched
	} {
		for _, multi := range []bool{false, true} {
			name := value
			if multi {
				name += "/multi"
			}
			t.Run(name, func(t *testing.T) {
				q := acpwire.QuestionRequest{
					Questions: []acpwire.Question{{Question: "Pick:", Options: options, Multi: multi}},
				}
				qm := newQuestionModel(q, 80)
				if multi {
					*qm.multis[0] = []string{value}
				} else {
					*qm.selects[0] = value
				}
				qm.form.State = huh.StateCompleted
				qm, _ = qm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				if got := qm.Answers()[0].Answer; got != acpwire.AnswerUnanswered {
					t.Fatalf("identity %q produced answer %q, want %q", value, got, acpwire.AnswerUnanswered)
				}
			})
		}
	}
}

// TestQuestionEscapeKeepsCompletedPrefix pins the confirmed skip contract: a
// question the form has already advanced past keeps the answer the user
// submitted, and the focused question plus every later one are recorded as
// unanswered. Live input on the focused question is never copied.
func TestQuestionEscapeKeepsCompletedPrefix(t *testing.T) {
	stops := []acpwire.QuestionOption{{Label: "red"}, {Label: "green"}, {Label: "blue"}}
	three := func(first acpwire.Question) []acpwire.Question {
		return []acpwire.Question{first, {Question: "Second?"}, {Question: "Third?"}}
	}
	escape := tea.KeyPressMsg{Code: tea.KeyEscape}
	submit := tea.KeyPressMsg{Code: tea.KeyEnter}
	unanswered := acpwire.AnswerUnanswered

	for _, tc := range []struct {
		name      string
		questions []acpwire.Question
		drive     func(t *testing.T, qm *questionModel) *questionModel
		want      []string
	}{
		{
			name:      "free text then skip",
			questions: three(acpwire.Question{Question: "Name?"}),
			drive: func(t *testing.T, qm *questionModel) *questionModel {
				qm = questionType(t, qm, "alice")
				qm = questionForm(t, qm, submit)
				return questionForm(t, qm, escape)
			},
			want: []string{"alice", unanswered, unanswered},
		},
		{
			name:      "single choice then skip",
			questions: three(acpwire.Question{Question: "Pick one:", Options: stops}),
			drive: func(t *testing.T, qm *questionModel) *questionModel {
				qm = questionForm(t, qm, questionMoveDown(1)...)
				qm = questionForm(t, qm, submit)
				return questionForm(t, qm, escape)
			},
			want: []string{"green", unanswered, unanswered},
		},
		{
			name:      "multi choice then skip",
			questions: three(acpwire.Question{Question: "Pick some:", Options: []acpwire.QuestionOption{{Label: "a"}, {Label: "b"}, {Label: "c"}}, Multi: true}),
			drive: func(t *testing.T, qm *questionModel) *questionModel {
				qm = questionForm(t, qm, questionToggle())
				qm = questionForm(t, qm, questionMoveDown(2)...)
				qm = questionForm(t, qm, questionToggle())
				qm = questionForm(t, qm, submit)
				return questionForm(t, qm, escape)
			},
			want: []string{"a, c", unanswered, unanswered},
		},
		{
			name:      "custom answer then skip",
			questions: three(acpwire.Question{Question: "Pick one:", Options: stops}),
			drive: func(t *testing.T, qm *questionModel) *questionModel {
				// The custom row is appended after the listed options.
				qm = questionForm(t, qm, questionMoveDown(len(stops))...)
				qm = questionForm(t, qm, submit)
				qm = questionType(t, qm, "magenta")
				qm = questionForm(t, qm, submit)
				return questionForm(t, qm, escape)
			},
			want: []string{"magenta", unanswered, unanswered},
		},
		{
			name:      "untouched default is not an answer",
			questions: three(acpwire.Question{Question: "Pick one:", Options: stops}),
			drive: func(t *testing.T, qm *questionModel) *questionModel {
				// The list highlights its first row, but nothing was submitted.
				return questionForm(t, qm, escape)
			},
			want: []string{unanswered, unanswered, unanswered},
		},
		{
			name:      "escape at the first question skips everything",
			questions: three(acpwire.Question{Question: "Name?"}),
			drive: func(t *testing.T, qm *questionModel) *questionModel {
				return questionForm(t, qm, escape)
			},
			want: []string{unanswered, unanswered, unanswered},
		},
		{
			name:      "unsubmitted multi choices are not an answer",
			questions: three(acpwire.Question{Question: "Pick some:", Options: []acpwire.QuestionOption{{Label: "a"}, {Label: "b"}}, Multi: true}),
			drive: func(t *testing.T, qm *questionModel) *questionModel {
				qm = questionForm(t, qm, questionToggle())
				return questionForm(t, qm, escape)
			},
			want: []string{unanswered, unanswered, unanswered},
		},
		{
			name:      "partial custom text is not an answer",
			questions: three(acpwire.Question{Question: "Pick one:", Options: stops}),
			drive: func(t *testing.T, qm *questionModel) *questionModel {
				qm = questionForm(t, qm, questionMoveDown(len(stops))...)
				qm = questionForm(t, qm, submit)
				qm = questionType(t, qm, "mag")
				return questionForm(t, qm, escape)
			},
			want: []string{unanswered, unanswered, unanswered},
		},
		{
			name:      "queued advancement skips the focused question",
			questions: three(acpwire.Question{Question: "Name?"}),
			drive: func(t *testing.T, qm *questionModel) *questionModel {
				qm = questionType(t, qm, "alice")
				// Submit without running the returned command: the form has
				// not advanced yet, so the focused question is still this one.
				qm, _ = qm.Update(submit)
				return questionForm(t, qm, escape)
			},
			want: []string{unanswered, unanswered, unanswered},
		},
		{
			name:      "unmapped focus falls back to all unanswered",
			questions: three(acpwire.Question{Question: "Name?"}),
			drive: func(t *testing.T, qm *questionModel) *questionModel {
				qm = questionType(t, qm, "alice")
				qm = questionForm(t, qm, submit)
				// Drop the fields mapping: no focused field can be placed.
				qm.fields = make([][]huh.Field, len(qm.q.Questions))
				return questionForm(t, qm, escape)
			},
			want: []string{unanswered, unanswered, unanswered},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := acpwire.QuestionRequest{
				Questions: tc.questions,
			}
			qm := questionStart(t, newQuestionModel(q, 80))
			qm = tc.drive(t, qm)

			if !qm.IsDone() || !qm.Aborted() {
				t.Fatalf("escape should finish the form: done=%v aborted=%v", qm.IsDone(), qm.Aborted())
			}
			answers := qm.Answers()
			if len(answers) != len(tc.want) {
				t.Fatalf("got %d answers, want %d: %+v", len(answers), len(tc.want), answers)
			}
			for i, want := range tc.want {
				if answers[i].Question != tc.questions[i].Question {
					t.Fatalf("answer %d is for %q, want %q", i, answers[i].Question, tc.questions[i].Question)
				}
				if answers[i].Answer != want {
					t.Fatalf("answer %d = %q, want %q (all: %+v)", i, answers[i].Answer, want, answers)
				}
			}
		})
	}
}

// TestQuestionEscapeAfterNavigatingBack pins the prefix rule for a user who
// returns to an earlier question: the focused question and every later one are
// skipped, even when they were visited before.
func TestQuestionEscapeAfterNavigatingBack(t *testing.T) {
	stops := []acpwire.QuestionOption{{Label: "red"}, {Label: "green"}}
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{
			{Question: "Name?"},
			{Question: "Pick one:", Options: stops},
			{Question: "Why?"},
		},
	}
	qm := questionStart(t, newQuestionModel(q, 80))
	qm = questionType(t, qm, "alice")
	qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter}) // → question 2
	qm = questionForm(t, qm, questionMoveDown(1)...)              // green
	qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter}) // → question 3
	back := tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	qm = questionForm(t, qm, back) // → back to question 2
	if idx, ok := qm.focusedQuestionIndex(); !ok || idx != 1 {
		t.Fatalf("focused question after navigating back = %d (ok=%v), want 1", idx, ok)
	}
	qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEscape})

	answers := qm.Answers()
	want := []string{"alice", acpwire.AnswerUnanswered, acpwire.AnswerUnanswered}
	if len(answers) != len(want) {
		t.Fatalf("got %d answers, want %d: %+v", len(answers), len(want), answers)
	}
	for i, w := range want {
		if answers[i].Answer != w {
			t.Fatalf("answer %d = %q, want %q (all: %+v)", i, answers[i].Answer, w, answers)
		}
	}
}

// TestQuestionCompletedFormStillFinalizesAll keeps the ordinary path: a form
// the user completes keeps every question's answer and is not an abort.
func TestQuestionCompletedFormStillFinalizesAll(t *testing.T) {
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{
			{Question: "Name?"},
			{Question: "Pick one:", Options: []acpwire.QuestionOption{{Label: "red"}, {Label: "green"}}},
			{Question: "Pick some:", Options: []acpwire.QuestionOption{{Label: "a"}, {Label: "b"}}, Multi: true},
		},
	}
	qm := questionStart(t, newQuestionModel(q, 80))
	qm = questionType(t, qm, "alice")
	qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter}) // → question 2
	qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter}) // red, → question 3
	qm = questionForm(t, qm, questionToggle())                    // a
	qm = questionForm(t, qm, tea.KeyPressMsg{Code: tea.KeyEnter}) // submit

	if !qm.IsDone() {
		t.Fatalf("completed form should be done")
	}
	if qm.Aborted() {
		t.Fatalf("completed form must not report an abort")
	}
	want := []string{"alice", "red", "a"}
	answers := qm.Answers()
	if len(answers) != len(want) {
		t.Fatalf("got %d answers, want %d: %+v", len(answers), len(want), answers)
	}
	for i, w := range want {
		if answers[i].Answer != w {
			t.Fatalf("answer %d = %q, want %q (all: %+v)", i, answers[i].Answer, w, answers)
		}
	}
}

// TestQuestionPromptKeysFitMinimumWidth keeps the Escape hint readable in the
// console header at the narrowest terminal the TUI draws a prompt in.
func TestQuestionPromptKeysFitMinimumWidth(t *testing.T) {
	keys := help.PromptKeys(help.FooterHints{QuestionPending: true})
	row := stripANSI(chrome.BorderLine("├─ ", "? Question", keys, "┤", minTerminalWidth, violetColor))
	if !strings.Contains(row, "skip remaining") {
		t.Fatalf("the Escape hint is lost at width %d: %q", minTerminalWidth, row)
	}
	if strings.Contains(row, "…") {
		t.Fatalf("the prompt header truncates at width %d: %q", minTerminalWidth, row)
	}
}

func TestQuestionFinalizeMultiOtherSubstitutesCustom(t *testing.T) {
	q := acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: "Pick some:", Options: []acpwire.QuestionOption{{Label: "real"}, {Label: "other-choice"}}, Multi: true}},
	}
	qm := newQuestionModel(q, 80)
	*qm.multis[0] = []string{questionOptionValue(0), questionOtherSentinel}
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
	q2 := acpwire.QuestionRequest{
		Questions: []acpwire.Question{{Question: "Pick some:", Options: []acpwire.QuestionOption{{Label: "real"}, {Label: "other-choice"}}, Multi: true}},
	}
	qm2 := newQuestionModel(q2, 80)
	*qm2.multis[0] = []string{questionOptionValue(0), questionOtherSentinel}
	*qm2.others[0] = "   "
	qm2.form.State = huh.StateCompleted
	qm2, _ = qm2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := qm2.Answers()[0].Answer; got != "real" {
		t.Fatalf("blank multi Other should be omitted, got %q", got)
	}
}
