package fleet

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/web/bridge/contract"
)

// fakeFleet is the seam's client with a state the test sets and a change
// channel the test fires. The seam is embedded, so an action a test does not
// set up fails loudly; the actions the screen drives are recorded below.
type fakeFleet struct {
	acpclient.Fleet

	state   acpclient.FleetState
	changed chan struct{}

	// Each action keeps its calls' arguments and answers what the test set.
	spawns      []contract.SpawnRequest
	spawnAnswer contract.SpawnResult
	spawnErr    error

	permissions   []permissionAnswer
	permissionErr error

	questions   []questionAnswer
	questionErr error

	cancels   []string
	cancelErr error

	approvals     []string
	approveAnswer contract.ApproveResult
	approveErr    error

	denials []string
	denyErr error

	promptReads []promptRead
	prompts     []string
	promptsErr  error
}

// permissionAnswer is one AnswerPermission call: the request it answered and
// the decision it carried.
type permissionAnswer struct {
	id string
	d  contract.Decision
}

// questionAnswer is one AnswerQuestion call.
type questionAnswer struct {
	id string
	a  contract.Answers
}

// promptRead is one RecentPrompts call.
type promptRead struct {
	project string
	limit   int
}

func newFakeFleet() *fakeFleet {
	return &fakeFleet{changed: make(chan struct{}, 1)}
}

func (f *fakeFleet) State() acpclient.FleetState { return f.state }
func (f *fakeFleet) Changed() <-chan struct{}    { return f.changed }

// Spawn records the request and answers what the test set.
func (f *fakeFleet) Spawn(_ context.Context, req contract.SpawnRequest) (contract.SpawnResult, error) {
	f.spawns = append(f.spawns, req)
	return f.spawnAnswer, f.spawnErr
}

// AnswerPermission records the request and the decision.
func (f *fakeFleet) AnswerPermission(_ context.Context, id string, d contract.Decision) error {
	f.permissions = append(f.permissions, permissionAnswer{id: id, d: d})
	return f.permissionErr
}

// AnswerQuestion records the request and the answers.
func (f *fakeFleet) AnswerQuestion(_ context.Context, id string, a contract.Answers) error {
	f.questions = append(f.questions, questionAnswer{id: id, a: a})
	return f.questionErr
}

// Cancel records the agent whose turn was stopped.
func (f *fakeFleet) Cancel(_ context.Context, agentID string) error {
	f.cancels = append(f.cancels, agentID)
	return f.cancelErr
}

// ApproveIntake records the submission it spawned.
func (f *fakeFleet) ApproveIntake(_ context.Context, id string) (contract.ApproveResult, error) {
	f.approvals = append(f.approvals, id)
	return f.approveAnswer, f.approveErr
}

// DenyIntake records the submission it discarded.
func (f *fakeFleet) DenyIntake(_ context.Context, id string) error {
	f.denials = append(f.denials, id)
	return f.denyErr
}

// RecentPrompts records the read and answers the prompts the test set.
func (f *fakeFleet) RecentPrompts(_ context.Context, project string, limit int) ([]string, error) {
	f.promptReads = append(f.promptReads, promptRead{project: project, limit: limit})
	return f.prompts, f.promptsErr
}

// fire signals a change the way the client does.
func (f *fakeFleet) fire() {
	select {
	case f.changed <- struct{}{}:
	default:
	}
}

// change sets the fake's state and drives the model's change handling once.
func (f *fakeFleet) change(t *testing.T, m Model, state acpclient.FleetState) Model {
	t.Helper()
	f.state = state
	return deliver(t, m)
}

// deliver runs one "the client changed" update, which reads the fake's state.
func deliver(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(changedMsg{})
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want fleet.Model", next)
	}
	return got
}

// press runs one key press.
func press(t *testing.T, m Model, msg tea.KeyPressMsg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want fleet.Model", next)
	}
	return got, cmd
}

// typeText presses every rune of s.
func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m, _ = press(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

// eventTime is the fixed clock the event-line assertions use.
var eventTime = time.Date(2026, 3, 4, 15, 4, 0, 0, time.Local)

func fixedClock() Option { return WithNow(func() time.Time { return eventTime }) }

// loadedState is a loaded, connected state on the local daemon's server.
func loadedState(epoch uint64) acpclient.FleetState {
	return acpclient.FleetState{
		Server: "local",
		Conn:   acpclient.FleetConnected,
		Loaded: true,
		Epoch:  epoch,
	}
}

func TestTheFirstStateAddsNoAgentEvents(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	first := loadedState(1)
	first.Agents = []contract.AgentStatus{
		{ID: "a1", Name: "Alpha", Status: contract.AgentAwaitingApproval, Pending: &contract.PendingRequest{Kind: contract.PendingApproval}},
		{ID: "b1", Name: "Bravo", Status: contract.AgentRunning},
		{ID: "c1", Name: "Charlie", Status: contract.AgentError},
	}
	first.Inbox.Groups.NeedsYou = []string{"a1"}
	first.Inbox.Groups.Ready = []string{"b1"}

	m = f.change(t, m, first)
	// The first read reports the connection and no agent changes: there is
	// nothing to compare it with.
	wantOnly(t, m, []string{"15:04 connected to local"})

	// A new epoch is a fresh full read, not a batch of changes: the agents
	// that entered a group are already there.
	second := first
	second.Epoch = 2
	second.Agents = append(append([]contract.AgentStatus{}, first.Agents...),
		contract.AgentStatus{ID: "d1", Name: "Delta", Status: contract.AgentError})
	second.Inbox.Groups.NeedsYou = []string{"a1", "d1"}
	second.Inbox.Groups.Ready = []string{"b1", "d1"}

	m = f.change(t, m, second)
	wantOnly(t, m, []string{"15:04 connected to local"})
}

func TestTransitionsAddEvents(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	first := loadedState(1)
	first.Agents = []contract.AgentStatus{
		{ID: "aaaaaaa1", Status: contract.AgentRunning},
		{ID: "b1", Name: "Bravo", Status: contract.AgentRunning},
		{ID: "c1", Name: "Charlie", Status: contract.AgentRunning},
	}
	m = f.change(t, m, first)

	second := first
	second.Agents = []contract.AgentStatus{
		{ID: "aaaaaaa1", Status: contract.AgentAwaitingApproval, Pending: &contract.PendingRequest{Kind: contract.PendingQuestion}},
		{ID: "b1", Name: "Bravo", Status: contract.AgentIdle},
		{ID: "c1", Name: "Charlie", Status: contract.AgentError},
	}
	second.Inbox.Groups.NeedsYou = []string{"aaaaaaa1"}
	second.Inbox.Groups.Ready = []string{"b1"}
	second.Inbox.Queue = []contract.InboxItem{
		{Kind: contract.InboxItemAgent, AgentID: "aaaaaaa1"},
		{
			Kind:       contract.InboxItemIntake,
			Submission: &contract.PendingSubmission{ID: "s1", Title: "Ship the release"},
		},
	}

	m = f.change(t, m, second)
	wantOnly(t, m, []string{
		"15:04 connected to local",
		"15:04 aaaaaaa1 needs you: question",
		"15:04 intake: Ship the release",
		"15:04 Bravo is ready",
		"15:04 Charlie failed",
	})
}

func TestConnectionChangesAddEvents(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	connected := loadedState(1)
	m = f.change(t, m, connected)
	wantOnly(t, m, []string{"15:04 connected to local"})

	// The same state again is not a change.
	m = f.change(t, m, connected)
	wantOnly(t, m, []string{"15:04 connected to local"})

	reconnecting := connected
	reconnecting.Conn = acpclient.FleetReconnecting
	m = f.change(t, m, reconnecting)

	down := reconnecting
	down.Conn = acpclient.FleetDown
	down.ConnErr = "dial tcp 127.0.0.1:7700: connect: connection refused"
	m = f.change(t, m, down)

	// While down, a new reason is a change too.
	downAgain := down
	downAgain.ConnErr = "timeout after 30s"
	m = f.change(t, m, downAgain)

	// The same down state with the same reason is not.
	m = f.change(t, m, downAgain)

	rejected := down
	rejected.Conn = acpclient.FleetRejected
	m = f.change(t, m, rejected)

	wantOnly(t, m, []string{
		"15:04 connected to local",
		"15:04 reconnecting to local",
		"15:04 local is down: dial tcp 127.0.0.1:7700: connect: connection refused",
		"15:04 local is down: timeout after 30s",
		"15:04 token rejected for local",
	})

	// A reconnect is a new epoch: the screen says it is connected again, and
	// adds no agent events.
	back := loadedState(2)
	back.Agents = []contract.AgentStatus{{ID: "z1", Name: "Zulu", Status: contract.AgentError}}
	back.Inbox.Groups.NeedsYou = []string{"z1"}
	m = f.change(t, m, back)
	if got := lastEvent(m); got != "15:04 connected to local" {
		t.Fatalf("last event = %q, want the reconnect's connected line", got)
	}
	if got := len(m.events); got != 6 {
		t.Fatalf("events = %d after the reconnect, want 6: %v", got, m.events)
	}
}

func TestTheEventsRingKeeps200(t *testing.T) {
	m := New(newFakeFleet(), fixedClock())
	for i := 0; i < 250; i++ {
		m.addEvent("line " + strconv.Itoa(i))
	}
	if got := len(m.events); got != maxEvents {
		t.Fatalf("events = %d, want %d", got, maxEvents)
	}
	if got, want := m.events[0], "15:04 line 50"; got != want {
		t.Fatalf("oldest event = %q, want %q", got, want)
	}
	if got, want := lastEvent(m), "15:04 line 249"; got != want {
		t.Fatalf("newest event = %q, want %q", got, want)
	}
	// The ring stays newest last, in order.
	for i := 1; i < len(m.events); i++ {
		want := "15:04 line " + strconv.Itoa(50+i)
		if m.events[i] != want {
			t.Fatalf("event %d = %q, want %q", i, m.events[i], want)
		}
	}
}

func TestTabCyclesTheFocus(t *testing.T) {
	m := New(newFakeFleet(), fixedClock())
	for _, want := range []int{focusNeedsYou, focusEvents, focusRight, focusAgents} {
		var cmd tea.Cmd
		m, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
		if cmd != nil {
			t.Fatalf("tab returned a command")
		}
		if m.focus != want {
			t.Fatalf("focus after tab = %d, want %d", m.focus, want)
		}
	}
	// shift-tab walks the same ring backwards.
	var cmd tea.Cmd
	m, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if cmd != nil {
		t.Fatalf("shift-tab returned a command")
	}
	if m.focus != focusRight {
		t.Fatalf("focus after shift-tab = %d, want %d", m.focus, focusRight)
	}
}

func TestSelectionFollowsTheIDAcrossAReorder(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	state := loadedState(1)
	state.Agents = []contract.AgentStatus{
		{ID: "a1", Name: "Alpha"},
		{ID: "b1", Name: "Bravo"},
		{ID: "c1", Name: "Charlie"},
	}
	state.Inbox.Groups.Running = []string{"a1", "b1", "c1"}
	m = f.change(t, m, state)

	m = moveDown(t, m, 2)
	if m.selected != "c1" {
		t.Fatalf("selected = %q, want c1", m.selected)
	}

	reordered := state
	reordered.Inbox.Groups.Running = []string{"c1", "a1", "b1"}
	m = f.change(t, m, reordered)
	if m.selected != "c1" {
		t.Fatalf("selection after reorder = %q, want c1", m.selected)
	}
	if m.agentCursor != 0 {
		t.Fatalf("cursor after reorder = %d, want 0", m.agentCursor)
	}

	// When the selected ID goes, the cursor keeps its index, clamped to the
	// list.
	gone := state
	gone.Inbox.Groups.Running = []string{"a1"}
	m = f.change(t, m, gone)
	if m.agentCursor != 0 || m.selected != "a1" {
		t.Fatalf("cursor, selected after the row went = %d, %q; want 0, a1", m.agentCursor, m.selected)
	}
}

func TestTheCursorSkipsGroupHeaders(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	state := loadedState(1)
	state.Agents = []contract.AgentStatus{
		{ID: "a1", Name: "Alpha", Status: contract.AgentAwaitingApproval},
		{ID: "b1", Name: "Bravo", Status: contract.AgentRunning},
	}
	state.Inbox.Groups.NeedsYou = []string{"a1"}
	state.Inbox.Groups.Running = []string{"b1"}
	m = f.change(t, m, state)

	lines, rows := m.agentLines()
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 4: %+v", len(lines), lines)
	}
	if !lines[0].header || !lines[2].header {
		t.Fatalf("group boundaries are not headers: %+v", lines)
	}
	if len(rows) != 2 {
		t.Fatalf("selectable rows = %d, want 2", len(rows))
	}

	if m.selected != "a1" || m.agentCursor != 0 {
		t.Fatalf("initial selection = %q at %d, want a1 at 0", m.selected, m.agentCursor)
	}
	// Moving over the group boundary lands on the next agent, not its header.
	m = moveDown(t, m, 1)
	if m.selected != "b1" || m.agentCursor != 1 {
		t.Fatalf("after one down: selection = %q at %d, want b1 at 1", m.selected, m.agentCursor)
	}
	// The cursor stops at the last row rather than on a header.
	m = moveDown(t, m, 5)
	if m.selected != "b1" || m.agentCursor != 1 {
		t.Fatalf("after overshooting: selection = %q at %d, want b1 at 1", m.selected, m.agentCursor)
	}
}

func TestTheFilterNarrowsTheAgents(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	state := loadedState(1)
	state.Agents = []contract.AgentStatus{
		{ID: "a1", Name: "Alpha", Project: "one"},
		{ID: "b1", Name: "Bravo", Project: "two"},
		{ID: "c1", Name: "Charlie", Project: "one"},
	}
	state.Inbox.Groups.Running = []string{"a1", "b1"}
	state.Inbox.Groups.Ready = []string{"c1"}
	m = f.change(t, m, state)

	// "/" opens the filter on the Agents panel.
	m, _ = press(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	if !m.filtering || m.focus != focusAgents {
		t.Fatalf("after /: filtering = %v, focus = %d", m.filtering, m.focus)
	}
	// The match is a case-insensitive substring of the name.
	m = typeText(t, m, "BRA")
	if m.filter != "BRA" {
		t.Fatalf("filter = %q, want BRA", m.filter)
	}
	if _, rows := m.agentLines(); len(rows) != 1 || rows[0].id != "b1" {
		t.Fatalf("rows with filter BRA = %+v, want just b1", rows)
	}

	// A group whose every agent is filtered out shows no header and no rows.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if lines, rows := m.agentLines(); len(lines) != 2 || len(rows) != 1 || rows[0].id != "b1" {
		t.Fatalf("with filter BR: lines = %+v, rows = %+v", lines, rows)
	}

	// Enter keeps the filter and leaves the input.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.filtering || m.filter != "BR" {
		t.Fatalf("after enter: filtering = %v, filter = %q; want false, BR", m.filtering, m.filter)
	}

	// Backspace shortens it, and the project matches too.
	m, _ = press(t, m, tea.KeyPressMsg{Code: '/', Text: "/"})
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.filter != "" {
		t.Fatalf("filter after two backspaces = %q, want empty", m.filter)
	}
	m = typeText(t, m, "TWO")
	if _, rows := m.agentLines(); len(rows) != 1 || rows[0].id != "b1" {
		t.Fatalf("rows matching the project TWO = %+v, want just b1", rows)
	}

	// Esc clears the filter and leaves the input.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.filtering || m.filter != "" {
		t.Fatalf("after esc: filtering = %v, filter = %q; want false, \"\"", m.filtering, m.filter)
	}
	if _, rows := m.agentLines(); len(rows) != 3 {
		t.Fatalf("rows after clearing the filter = %d, want 3", len(rows))
	}
}

func TestNeedsYouSelectsItsAgent(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	state := loadedState(1)
	state.Agents = []contract.AgentStatus{
		{ID: "a1", Name: "Alpha"},
		{ID: "b1", Name: "Bravo"},
	}
	state.Inbox.Groups.NeedsYou = []string{"a1", "b1"}
	state.Inbox.Queue = []contract.InboxItem{
		{Kind: contract.InboxItemAgent, AgentID: "a1"},
		{Kind: contract.InboxItemAgent, AgentID: "b1"},
	}
	m = f.change(t, m, state)

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.focus != focusNeedsYou {
		t.Fatalf("focus = %d, want Needs you", m.focus)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.needsCursor != 1 {
		t.Fatalf("needs cursor = %d, want 1", m.needsCursor)
	}
	if m.selected != "b1" {
		t.Fatalf("selected = %q, want b1", m.selected)
	}
	if m.agentCursor != 1 {
		t.Fatalf("agents cursor = %d, want 1", m.agentCursor)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.selected != "a1" {
		t.Fatalf("selected after up = %q, want a1", m.selected)
	}
}

// TestTheNeedsYouPanelKeepsTheCursorVisible checks a queue longer than the
// panel scrolls around the cursor's row: the row the user is on is never
// hidden below the fold.
func TestTheNeedsYouPanelKeepsTheCursorVisible(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	state := loadedState(1)
	for i := 1; i <= 7; i++ {
		state.Inbox.Queue = append(state.Inbox.Queue, contract.InboxItem{
			Kind:       contract.InboxItemIntake,
			Submission: &contract.PendingSubmission{ID: fmt.Sprintf("s%d", i), Title: fmt.Sprintf("item%d", i)},
		})
	}
	m = f.change(t, m, state)

	m.focus = focusNeedsYou
	m.needsCursor = 6
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	got := next.(Model).View().Content

	if !strings.Contains(got, "item7") {
		t.Fatalf("the cursor's row is not on screen:\n%s", got)
	}
	if strings.Contains(got, "item1") {
		t.Fatalf("the panel did not scroll: the oldest row is still on screen:\n%s", got)
	}
}

// TestTheTabKeysSwitchTheRightPanel checks the 1 and 3 keys pick the right
// panel's tab.
func TestTheTabKeysSwitchTheRightPanel(t *testing.T) {
	m := New(newFakeFleet(), fixedClock())

	m, _ = press(t, m, tea.KeyPressMsg{Code: '3', Text: "3"})
	if m.tab != 3 {
		t.Fatalf("tab after 3 = %d, want 3", m.tab)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: '1', Text: "1"})
	if m.tab != 1 {
		t.Fatalf("tab after 1 = %d, want 1", m.tab)
	}
}

// TestTheZoomAndHelpKeysToggle checks z and ? toggle their overlays.
func TestTheZoomAndHelpKeysToggle(t *testing.T) {
	m := New(newFakeFleet(), fixedClock())

	m, _ = press(t, m, tea.KeyPressMsg{Code: 'z', Text: "z"})
	if !m.zoom {
		t.Fatal("z did not zoom the focused panel")
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'z', Text: "z"})
	if m.zoom {
		t.Fatal("z did not unzoom the focused panel")
	}

	m, _ = press(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
	if !m.help {
		t.Fatal("? did not show the help")
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
	if m.help {
		t.Fatal("? did not hide the help")
	}
}

// TestARowWithNoTimeReadsAsADash checks a row whose time is unknown: the age
// reads as –, not as a count of days since the epoch.
func TestARowWithNoTimeReadsAsADash(t *testing.T) {
	m := New(newFakeFleet(), fixedClock())
	if got, want := m.age(time.Time{}), "–"; got != want {
		t.Errorf("the age of no time = %q, want %q", got, want)
	}
}

// TestTheWaitEndsWhenTheClientCloses checks the screen stops waiting when the
// client's change signal closes: a closed client has nothing more to say, so
// the wait must end rather than block forever.
func TestTheWaitEndsWhenTheClientCloses(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	wait := m.waitForChange()
	close(f.changed)

	done := make(chan tea.Msg, 1)
	go func() { done <- wait() }()
	select {
	case msg := <-done:
		if msg != nil {
			t.Fatalf("the wait after the client closed returned %T, want nil", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("the wait command blocked after the client closed")
	}
}

func TestQuitKeysQuit(t *testing.T) {
	cases := map[string]tea.KeyPressMsg{
		"q":      {Code: 'q', Text: "q"},
		"ctrl+c": {Code: 'c', Mod: tea.ModCtrl},
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			m := New(newFakeFleet(), fixedClock())
			next, cmd := m.Update(msg)
			if _, ok := next.(Model); !ok {
				t.Fatalf("Update returned %T, want fleet.Model", next)
			}
			if cmd == nil {
				t.Fatalf("%s returned no command", name)
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatalf("%s returned %T, want tea.QuitMsg", name, cmd())
			}
		})
	}
}

// TestTheWaitCommandDeliversTheChange covers the wait Init arms: it returns on
// the client's signal, and it returns rather than blocking after the program
// quits.
func TestTheWaitCommandDeliversTheChange(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())
	wait := m.waitForChange()
	f.fire()
	if _, ok := wait().(changedMsg); !ok {
		t.Fatalf("the wait command did not return a change")
	}

	stop := New(f, fixedClock())
	stop.done = newQuitGate()
	stop.done.release()
	wait = stop.waitForChange()
	done := make(chan tea.Msg, 1)
	go func() { done <- wait() }()
	select {
	case msg := <-done:
		if msg != nil {
			t.Fatalf("the wait after quit returned %T, want nil", msg)
		}
	case <-time.After(time.Second):
		t.Fatalf("the wait command blocked after the program quit")
	}
}

// TestQuitReleasesTheWait is the regression for a quit that never released the
// wait: without it the armed wait stays blocked on the client's signal, which
// is closed with the client at quit, so one goroutine leaks per program run.
func TestQuitReleasesTheWait(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	// Arm the wait the way Init does, but do not fire the client's change
	// channel: the point is that the quit alone releases the wait.
	wait := m.waitForChange()

	_, cmd := press(t, m, tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatalf("q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q returned %T, want tea.QuitMsg", cmd())
	}

	done := make(chan tea.Msg, 1)
	go func() { done <- wait() }()
	select {
	case msg := <-done:
		if msg != nil {
			t.Fatalf("the wait after quit returned %T, want nil", msg)
		}
	case <-time.After(time.Second):
		t.Fatalf("the quit did not release the wait command")
	}
}

// moveDown presses j n times.
func moveDown(t *testing.T, m Model, n int) Model {
	t.Helper()
	for i := 0; i < n; i++ {
		m, _ = press(t, m, tea.KeyPressMsg{Code: 'j', Text: "j"})
	}
	return m
}

// wantOnly asserts the Events ring is exactly want.
func wantOnly(t *testing.T, m Model, want []string) {
	t.Helper()
	if len(m.events) != len(want) {
		t.Fatalf("events = %v, want %v", m.events, want)
	}
	for i := range want {
		if m.events[i] != want[i] {
			t.Fatalf("event %d = %q, want %q (all: %v)", i, m.events[i], want[i], m.events)
		}
	}
}

// lastEvent is the newest line in the Events ring.
func lastEvent(m Model) string {
	if len(m.events) == 0 {
		return ""
	}
	return m.events[len(m.events)-1]
}
