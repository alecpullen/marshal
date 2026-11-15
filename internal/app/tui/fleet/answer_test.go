package fleet

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/web/bridge/contract"
)

// approvalRequest is one pending approval: a shell command with a risk and a
// reason.
func approvalRequest() *contract.PendingRequest {
	return &contract.PendingRequest{
		Kind:   contract.PendingApproval,
		ID:     "p1",
		Params: json.RawMessage(`{"toolName":"shell","command":"go test ./...","risk":"high","reason":"runs the suite"}`),
	}
}

// questionRequest is one pending question request with the payload the ACP
// bridge sends: the questions, in order.
func questionRequest(params string) *contract.PendingRequest {
	return &contract.PendingRequest{
		Kind:   contract.PendingQuestion,
		ID:     "q1",
		Params: json.RawMessage(params),
	}
}

// parkedState is a loaded, connected fleet of one agent, Alpha, parked on p.
func parkedState(p *contract.PendingRequest) acpclient.FleetState {
	state := loadedState(1)
	status := contract.AgentAwaitingApproval
	if p.Kind == contract.PendingQuestion {
		status = contract.AgentAwaitingQuestion
	}
	state.Agents = []contract.AgentStatus{{
		ID:      "a1",
		Name:    "Alpha",
		Status:  status,
		Pending: p,
	}}
	state.Inbox.Groups.NeedsYou = []string{"a1"}
	return state
}

// sized is the model at 100×30, so the screen renders.
func sized(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return next.(Model)
}

// answering is the fleet of parkedState at 100×30, on now.
func answering(t *testing.T, f *fakeFleet, p *contract.PendingRequest, now func() time.Time) Model {
	t.Helper()
	m := New(f, WithNow(now))
	return sized(t, f.change(t, m, parkedState(p)))
}

// clock returns a fixed clock at eventTime.
func clock() func() time.Time { return func() time.Time { return eventTime } }

// answer delivers the command's answer to the model.
func answer(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("the key returned no command")
	}
	next, _ := m.Update(cmd())
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want fleet.Model", next)
	}
	return got
}

// openApproval presses a on the parked agent.
func openApproval(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd != nil {
		t.Fatalf("a returned a command")
	}
	return m
}

func TestYesApprovesTheAgentsRequest(t *testing.T) {
	f := newFakeFleet()
	m := openApproval(t, answering(t, f, approvalRequest(), clock()))

	if m.answer != "a1" {
		t.Fatalf("answer = %q, want the agent a1", m.answer)
	}
	if m.tab != 1 {
		t.Fatalf("tab = %d, want the Activity tab", m.tab)
	}
	view := m.View().Content
	for _, want := range []string{"go test ./...", "risk high", "reason runs the suite", "[y]es [n]o"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the approval's prompt does not show %q:\n%s", want, view)
		}
	}

	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = answer(t, m, cmd)

	if len(f.permissions) != 1 {
		t.Fatalf("permission answers = %+v, want one", f.permissions)
	}
	got := f.permissions[0]
	if got.id != "p1" {
		t.Fatalf("answered %q, want the pending request p1", got.id)
	}
	if !got.d.Approved {
		t.Fatalf("decision = %+v, want approved", got.d)
	}
	if m.answer != "" {
		t.Fatalf("the prompt is still open after the answer: %q", m.answer)
	}
}

func TestNoDeniesIt(t *testing.T) {
	f := newFakeFleet()
	m := openApproval(t, answering(t, f, approvalRequest(), clock()))

	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = answer(t, m, cmd)

	if len(f.permissions) != 1 {
		t.Fatalf("permission answers = %+v, want one", f.permissions)
	}
	got := f.permissions[0]
	if got.id != "p1" {
		t.Fatalf("answered %q, want the pending request p1", got.id)
	}
	if got.d.Approved {
		t.Fatalf("decision = %+v, want denied", got.d)
	}
	if m.answer != "" {
		t.Fatalf("the prompt is still open after the answer: %q", m.answer)
	}
}

// TestAnAccessRequestIsNotAnsweredHere covers decision 4's non-goal: an
// approval carrying modeAccess is shown, says to open the agent, and y answers
// nothing.
func TestAnAccessRequestIsNotAnsweredHere(t *testing.T) {
	f := newFakeFleet()
	access := &contract.PendingRequest{
		Kind:   contract.PendingApproval,
		ID:     "p1",
		Params: json.RawMessage(`{"toolName":"mode","modeAccess":{"requestId":"p1","sessionId":"s1","reason":"needs the shell"}}`),
	}
	m := openApproval(t, answering(t, f, access, clock()))

	if view := m.View().Content; !strings.Contains(view, "open the agent to answer") {
		t.Fatalf("the access request does not say to open the agent:\n%s", view)
	}
	if strings.Contains(m.View().Content, "[y]es [n]o") {
		t.Fatalf("the access request offers an answer here:\n%s", m.View().Content)
	}

	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd != nil {
		t.Fatalf("y answered an access request")
	}
	if len(f.permissions) != 0 {
		t.Fatalf("permission answers = %+v, want none", f.permissions)
	}
}

func TestTheQuestionModalAnswersInOrder(t *testing.T) {
	f := newFakeFleet()
	params := `{"questions":[
		{"question":"Which one?","options":[{"label":"one"},{"label":"two"}]},
		{"question":"Which many?","options":[{"label":"a","description":"first"},{"label":"b"}],"multi":true}]}`
	m := openApproval(t, answering(t, f, questionRequest(params), clock()))

	if m.modal == nil {
		t.Fatal("a did not open the question's modal")
	}
	if view := m.View().Content; !strings.Contains(view, "Which one?") || !strings.Contains(view, "two") {
		t.Fatalf("the modal does not draw the first question:\n%s", view)
	}

	// The single question: the second option, then enter.
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'j', Text: "j"})
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	q, ok := m.modal.(*questionModal)
	if !ok {
		t.Fatalf("the modal is %T, want *questionModal", m.modal)
	}
	if q.at != 1 {
		t.Fatalf("question = %d, want the second", q.at)
	}
	if view := m.View().Content; !strings.Contains(view, "Which many?") {
		t.Fatalf("the modal did not move to the second question:\n%s", view)
	}

	// The multi question: both options toggled, then enter submits.
	for i := 0; i < 2; i++ {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
		m, _ = press(t, m, tea.KeyPressMsg{Code: 'j', Text: "j"})
	}
	// j moved past the last option; come back onto it.
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'k', Text: "k"})
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = answer(t, m, cmd)

	if m.modal != nil {
		t.Fatal("the modal stayed open after the last question")
	}
	if len(f.questions) != 1 {
		t.Fatalf("question answers = %+v, want one", f.questions)
	}
	got := f.questions[0]
	if got.id != "q1" {
		t.Fatalf("answered %q, want the pending question q1", got.id)
	}
	want := []contract.Answer{
		{Question: "Which one?", Answer: "two"},
		{Question: "Which many?", Answer: "a, b"},
	}
	if len(got.a.Answers) != len(want) {
		t.Fatalf("answers = %+v, want %+v", got.a.Answers, want)
	}
	for i := range want {
		if got.a.Answers[i] != want[i] {
			t.Fatalf("answer %d = %+v, want %+v", i, got.a.Answers[i], want[i])
		}
	}
	if got.a.Declined {
		t.Fatalf("the answers are declined: %+v", got.a)
	}
}

func TestAQuestionWithoutOptionsTakesText(t *testing.T) {
	f := newFakeFleet()
	m := openApproval(t, answering(t, f, questionRequest(`{"questions":[{"question":"Name it"}]}`), clock()))

	if m.modal == nil {
		t.Fatal("a did not open the question's modal")
	}
	m = typeText(t, m, "blue")
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = answer(t, m, cmd)

	if len(f.questions) != 1 {
		t.Fatalf("question answers = %+v, want one", f.questions)
	}
	want := []contract.Answer{{Question: "Name it", Answer: "blue"}}
	got := f.questions[0].a.Answers
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("answers = %+v, want %+v", got, want)
	}
}

func TestDDeclinesTheQuestion(t *testing.T) {
	f := newFakeFleet()
	m := openApproval(t, answering(t, f, questionRequest(`{"questions":[{"question":"Which one?","options":[{"label":"one"}]}]}`), clock()))

	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = answer(t, m, cmd)

	if m.modal != nil {
		t.Fatal("the modal stayed open after the decline")
	}
	if len(f.questions) != 1 {
		t.Fatalf("question answers = %+v, want one", f.questions)
	}
	if !f.questions[0].a.Declined {
		t.Fatalf("answers = %+v, want declined", f.questions[0].a)
	}
}

func TestAFailedAnswerShowsForFourSeconds(t *testing.T) {
	now := eventTime
	f := newFakeFleet()
	m := openApproval(t, answering(t, f, approvalRequest(), func() time.Time { return now }))

	f.permissionErr = &acpclient.FleetError{Status: 429, Message: "Budget reached (daily)"}
	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = answer(t, m, cmd)

	const want = "answer Alpha failed: Budget reached (daily)"
	if got := m.statusText(); got != want {
		t.Fatalf("status line = %q, want %q", got, want)
	}
	if got := lastEvent(m); got != "15:04 "+want {
		t.Fatalf("last event = %q, want %q", got, "15:04 "+want)
	}
	if !strings.Contains(m.footer(), want) {
		t.Fatalf("the footer does not draw the failure: %q", m.footer())
	}

	// Three seconds on, the line is still there; past four it is gone. The
	// Events panel keeps its own copy either way, so the footer is what the
	// two checks read.
	now = now.Add(3 * time.Second)
	if !strings.Contains(m.footer(), want) {
		t.Fatalf("the status line cleared before its four seconds: %q", m.footer())
	}
	now = now.Add(2 * time.Second)
	if got := m.statusText(); got != "" {
		t.Fatalf("status line after four seconds = %q, want none", got)
	}
	if strings.Contains(m.footer(), want) {
		t.Fatalf("the footer still draws the cleared status line: %q", m.footer())
	}
}

func TestAnAnswerSettledElsewhereSaysSo(t *testing.T) {
	f := newFakeFleet()
	m := openApproval(t, answering(t, f, approvalRequest(), clock()))

	f.permissionErr = acpclient.ErrSettled
	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = answer(t, m, cmd)

	if got, want := lastEvent(m), "15:04 Alpha: already answered"; got != want {
		t.Fatalf("last event = %q, want %q", got, want)
	}
	if got := m.statusText(); got != "" {
		t.Fatalf("status line = %q, want none for an answer settled elsewhere", got)
	}
	if view := m.View().Content; strings.Contains(view, "failed") {
		t.Fatalf("the footer reports a failure:\n%s", view)
	}
}

// TestActionsAreRefusedWhileDown covers decision 9: with the connection
// anything but connected, the action key sets the status line and calls
// nothing.
func TestActionsAreRefusedWhileDown(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	down := parkedState(approvalRequest())
	down.Conn = acpclient.FleetDown
	down.ConnErr = "dial tcp 127.0.0.1:7700: connect: connection refused"
	m = sized(t, f.change(t, m, down))

	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd != nil {
		t.Fatalf("a returned a command while down")
	}
	if got, want := m.statusText(), "not connected to local"; got != want {
		t.Fatalf("status line = %q, want %q", got, want)
	}
	if m.answer != "" {
		t.Fatalf("the approval opened while down: %q", m.answer)
	}
	if m.modal != nil {
		t.Fatal("a modal opened while down")
	}
	if len(f.permissions) != 0 || len(f.questions) != 0 {
		t.Fatalf("answers while down: permissions %+v, questions %+v", f.permissions, f.questions)
	}
}
