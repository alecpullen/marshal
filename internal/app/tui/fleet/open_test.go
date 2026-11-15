package fleet

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/web/bridge/contract"
)

// enter is the open key: the row under the cursor becomes the target the
// program ends with.
func enter() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter} }

// TestEnterOpensTheSelectedAgent checks ⏎ on an agent row: it quits with that
// agent's ID, name and project root.
func TestEnterOpensTheSelectedAgent(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	state := loadedState(1)
	state.Agents = []contract.AgentStatus{
		{ID: "a1", Name: "Alpha", Project: "/repo/marshal"},
		{ID: "b1", Name: "Bravo", Project: "/repo/notes"},
	}
	state.Inbox.Groups.Running = []string{"a1", "b1"}
	m = f.change(t, m, state)

	m = moveDown(t, m, 1)
	if got := m.Selected(); got != "b1" {
		t.Fatalf("Selected() before ⏎ = %q, want b1", got)
	}

	next, cmd := press(t, m, enter())
	if cmd == nil {
		t.Fatal("⏎ returned no command on an agent row")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("⏎ returned %T, want tea.QuitMsg", cmd())
	}
	want := Target{AgentID: "b1", AgentName: "Bravo", Root: "/repo/notes"}
	if got := next.Open(); got != want {
		t.Fatalf("Open() = %+v, want %+v", got, want)
	}

	// An agent with no name opens under the name the screen shows for it.
	bare := loadedState(1)
	bare.Agents = []contract.AgentStatus{{ID: "aaaaaaa1bcdef", Project: "/repo/infra"}}
	bare.Inbox.Groups.Running = []string{"aaaaaaa1bcdef"}
	m = f.change(t, m, bare)
	next, cmd = press(t, m, enter())
	if cmd == nil {
		t.Fatal("⏎ returned no command on an unnamed agent row")
	}
	want = Target{AgentID: "aaaaaaa1bcdef", AgentName: "aaaaaaa1", Root: "/repo/infra"}
	if got := next.Open(); got != want {
		t.Fatalf("Open() for an unnamed agent = %+v, want %+v", got, want)
	}
}

// TestEnterOpensADaemonSession checks ⏎ on a Sessions row: it quits with the
// child's root and the session's ID, and no agent.
func TestEnterOpensADaemonSession(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	state := loadedState(1)
	state.Daemon = &contract.DaemonInfo{
		Version: "0.1.0",
		Profile: "local",
		Children: []contract.DaemonChild{{
			Root: "/repo/marshal",
			Sessions: []contract.DaemonSession{
				{ID: "3f2a91bc7d"},
				{ID: "9c1d4e5f6a"},
			},
		}},
	}
	m = f.change(t, m, state)
	if got := m.Selected(); got != "3f2a91bc7d" {
		t.Fatalf("Selected() on the Sessions rows = %q, want 3f2a91bc7d", got)
	}

	m = moveDown(t, m, 1)
	next, cmd := press(t, m, enter())
	if cmd == nil {
		t.Fatal("⏎ returned no command on a Sessions row")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("⏎ returned %T, want tea.QuitMsg", cmd())
	}
	want := Target{Root: "/repo/marshal", SessionID: "9c1d4e5f6a"}
	if got := next.Open(); got != want {
		t.Fatalf("Open() = %+v, want %+v", got, want)
	}
}

// TestEnterOnANeedsYouItemOpensItsAgent checks ⏎ in Needs you: the agent item
// under the cursor opens that agent.
func TestEnterOnANeedsYouItemOpensItsAgent(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	state := loadedState(1)
	state.Agents = []contract.AgentStatus{
		{ID: "a1", Name: "Alpha", Project: "/repo/marshal"},
		{ID: "b1", Name: "Bravo", Project: "/repo/notes"},
	}
	state.Inbox.Groups.NeedsYou = []string{"a1", "b1"}
	state.Inbox.Queue = []contract.InboxItem{
		{Kind: contract.InboxItemAgent, AgentID: "a1"},
		{Kind: contract.InboxItemAgent, AgentID: "b1"},
	}
	m = f.change(t, m, state)

	// Tab moves the focus to Needs you; j moves its cursor onto the second
	// item's agent.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.focus != focusNeedsYou {
		t.Fatalf("focus = %d, want Needs you", m.focus)
	}
	m = moveDown(t, m, 1)

	next, cmd := press(t, m, enter())
	if cmd == nil {
		t.Fatal("⏎ returned no command on a Needs you agent item")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("⏎ returned %T, want tea.QuitMsg", cmd())
	}
	want := Target{AgentID: "b1", AgentName: "Bravo", Root: "/repo/notes"}
	if got := next.Open(); got != want {
		t.Fatalf("Open() = %+v, want %+v", got, want)
	}
}

// TestEnterOnAnIntakeItemDoesNothing checks ⏎ on an intake item, and with
// nothing selected at all: the screen stays up, since plan 5 gives those items
// approve and deny.
func TestEnterOnAnIntakeItemDoesNothing(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	state := loadedState(1)
	state.Agents = []contract.AgentStatus{{ID: "a1", Name: "Alpha", Project: "/repo/marshal"}}
	state.Inbox.Groups.NeedsYou = []string{"a1"}
	state.Inbox.Queue = []contract.InboxItem{
		{Kind: contract.InboxItemAgent, AgentID: "a1"},
		{
			Kind:       contract.InboxItemIntake,
			Submission: &contract.PendingSubmission{ID: "s1", Title: "ship the release"},
		},
	}
	m = f.change(t, m, state)

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = moveDown(t, m, 1)
	if m.needsCursor != 1 {
		t.Fatalf("needs cursor = %d, want the intake item at 1", m.needsCursor)
	}

	next, cmd := press(t, m, enter())
	if cmd != nil {
		t.Fatalf("⏎ on an intake item returned a command")
	}
	if got := next.Open(); got != (Target{}) {
		t.Fatalf("Open() on an intake item = %+v, want the zero target", got)
	}

	// Nothing selected: no state has landed, so there is nothing to open.
	empty := New(newFakeFleet(), fixedClock())
	next, cmd = press(t, empty, enter())
	if cmd != nil {
		t.Fatalf("⏎ with nothing selected returned a command")
	}
	if got := next.Open(); got != (Target{}) {
		t.Fatalf("Open() with nothing selected = %+v, want the zero target", got)
	}
}

// TestQuitOpensNothing checks the quit keys still end with the zero target,
// even with a row that ⏎ would open under the cursor.
func TestQuitOpensNothing(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock())

	state := loadedState(1)
	state.Agents = []contract.AgentStatus{{ID: "a1", Name: "Alpha", Project: "/repo/marshal"}}
	state.Inbox.Groups.Running = []string{"a1"}
	m = f.change(t, m, state)

	cases := map[string]tea.KeyPressMsg{
		"q":      {Code: 'q', Text: "q"},
		"ctrl+c": {Code: 'c', Mod: tea.ModCtrl},
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			next, cmd := press(t, m, msg)
			if cmd == nil {
				t.Fatalf("%s returned no command", name)
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatalf("%s returned %T, want tea.QuitMsg", name, cmd())
			}
			if got := next.Open(); got != (Target{}) {
				t.Fatalf("%s opened %+v, want the zero target", name, got)
			}
		})
	}
}

// TestWithSelectedRestoresTheCursor checks WithSelected starts the cursor on
// its row — an agent's or a daemon session's — once a loaded state lists it,
// and only that once.
func TestWithSelectedRestoresTheCursor(t *testing.T) {
	f := newFakeFleet()
	m := New(f, fixedClock(), WithSelected("b1"))

	// The first state does not list b1 yet: the start waits for one that does.
	waiting := loadedState(1)
	waiting.Agents = []contract.AgentStatus{{ID: "a1", Name: "Alpha"}}
	waiting.Inbox.Groups.Running = []string{"a1"}
	m = f.change(t, m, waiting)
	if got := m.Selected(); got != "a1" {
		t.Fatalf("Selected() before the row is listed = %q, want a1", got)
	}

	loaded := loadedState(2)
	loaded.Agents = []contract.AgentStatus{
		{ID: "a1", Name: "Alpha"},
		{ID: "b1", Name: "Bravo"},
		{ID: "c1", Name: "Charlie"},
	}
	loaded.Inbox.Groups.Running = []string{"a1", "b1", "c1"}
	m = f.change(t, m, loaded)
	if got := m.Selected(); got != "b1" {
		t.Fatalf("Selected() = %q, want b1", got)
	}
	if m.agentCursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.agentCursor)
	}

	// The start is applied once: a later reorder leaves the operator's own
	// move alone.
	m = moveDown(t, m, 1)
	if got := m.Selected(); got != "c1" {
		t.Fatalf("Selected() after j = %q, want c1", got)
	}
	reordered := loaded
	reordered.Epoch = 3
	reordered.Inbox.Groups.Running = []string{"c1", "a1", "b1"}
	m = f.change(t, m, reordered)
	if got := m.Selected(); got != "c1" {
		t.Fatalf("Selected() after the reorder = %q, want the cursor's own row c1", got)
	}

	// A daemon session's ID restores its cursor in the Sessions group.
	sf := newFakeFleet()
	sm := New(sf, fixedClock(), WithSelected("9c1d4e5f6a"))
	sessions := loadedState(1)
	sessions.Daemon = &contract.DaemonInfo{
		Children: []contract.DaemonChild{{
			Root: "/repo/marshal",
			Sessions: []contract.DaemonSession{
				{ID: "3f2a91bc7d"},
				{ID: "9c1d4e5f6a"},
			},
		}},
	}
	sm = sf.change(t, sm, sessions)
	if got := sm.Selected(); got != "9c1d4e5f6a" {
		t.Fatalf("Selected() on a session = %q, want 9c1d4e5f6a", got)
	}
	if sm.agentCursor != 1 {
		t.Fatalf("cursor on a session = %d, want 1", sm.agentCursor)
	}
}

// TestWithNoticeAddsAnEvent checks the loop's notice is in the Events ring from
// the start, and that the first read's baseline rule leaves it there: it is not
// a transition.
func TestWithNoticeAddsAnEvent(t *testing.T) {
	m := New(newFakeFleet(), fixedClock(), WithNotice("could not open Alpha: no daemon answers"))
	wantOnly(t, m, []string{"15:04 could not open Alpha: no daemon answers"})

	f := newFakeFleet()
	noticed := New(f, fixedClock(), WithNotice("could not open Alpha: no daemon answers"))
	noticed = f.change(t, noticed, loadedState(1))
	wantOnly(t, noticed, []string{
		"15:04 could not open Alpha: no daemon answers",
		"15:04 connected to local",
	})
}

// TestEnterDuringAnApprovalOpensTheAgent checks ⏎ while an inline approval is
// open. An approval answers y, n and esc only, so ⏎ falls through to the
// screen and opens the row — the same key the screen answers everywhere, and
// the affordance the prompt itself names for an access request.
func TestEnterDuringAnApprovalOpensTheAgent(t *testing.T) {
	f := newFakeFleet()
	m := openApproval(t, answering(t, f, approvalRequest(), clock()))
	if m.answer != "a1" {
		t.Fatalf("answer = %q, want the prompt open on a1", m.answer)
	}

	next, cmd := press(t, m, enter())
	if cmd == nil {
		t.Fatal("⏎ during an approval returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("⏎ during an approval returned %T, want tea.QuitMsg", cmd())
	}
	want := Target{AgentID: "a1", AgentName: "Alpha"}
	if got := next.Open(); got != want {
		t.Fatalf("Open() during an approval = %+v, want %+v", got, want)
	}
}
