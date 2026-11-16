package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/fleet"
	"marshal/internal/app/tui/theme"
	"marshal/web/bridge/contract"
)

// needsYouFleet is the fleet the session screen reads: the state a test sets,
// and the change signal it fires. The actions belong to the fleet screen, which
// this screen never calls, so the interface is embedded.
type needsYouFleet struct {
	acpclient.Fleet

	state   acpclient.FleetState
	changed chan struct{}
}

func newNeedsYouFleet(state acpclient.FleetState) *needsYouFleet {
	return &needsYouFleet{state: state, changed: make(chan struct{}, 1)}
}

func (f *needsYouFleet) State() acpclient.FleetState { return f.state }
func (f *needsYouFleet) Changed() <-chan struct{}    { return f.changed }

// fire signals a change the way the client does: coalescing, never blocking.
func (f *needsYouFleet) fire() {
	select {
	case f.changed <- struct{}{}:
	default:
	}
}

// agentItem is a needs-you item for an agent.
func agentItem(id string) contract.InboxItem {
	return contract.InboxItem{Kind: contract.InboxItemAgent, AgentID: id}
}

// intakeItem is a needs-you item for a submission, which belongs to no agent.
func intakeItem(id, title string) contract.InboxItem {
	return contract.InboxItem{
		Kind:       contract.InboxItemIntake,
		Submission: &contract.PendingSubmission{ID: id, Title: title},
	}
}

// loadedFleet is a connected, loaded state with queue waiting, at epoch 1.
func loadedFleet(queue ...contract.InboxItem) acpclient.FleetState {
	return acpclient.FleetState{
		Server: "local",
		Conn:   acpclient.FleetConnected,
		Loaded: true,
		Epoch:  1,
		Inbox:  contract.Inbox{Queue: queue},
	}
}

// parkedFleet is loadedFleet after agentID parked on an approval: the same
// epoch, so the watch reports the change rather than taking a new baseline.
func parkedFleet(agentID string) acpclient.FleetState {
	st := loadedFleet(agentItem(agentID))
	st.Agents = []contract.AgentStatus{{
		ID:      agentID,
		Name:    "Alpha",
		Status:  contract.AgentAwaitingApproval,
		Pending: &contract.PendingRequest{Kind: contract.PendingApproval},
	}}
	st.Inbox.Groups.NeedsYou = []string{agentID}
	return st
}

// needsYouModel is a session screen over f with the loop's watch, which may be
// nil: exactly what a daemon session screen is built with (decision 8).
func needsYouModel(t *testing.T, f acpclient.Fleet, w *fleet.Watch, opts ...Option) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	m := testModel(t, state, append([]Option{WithFleet(f, w)}, opts...)...)
	m.resize(100, 30)
	return m
}

// deliverFleetChange hands m the fleet's change signal and returns the model
// after it.
func deliverFleetChange(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.Update(fleetChangedMsg{ch: m.fleet.Changed()})
	return asModel(t, updated)
}

// TestTheCountChangesWhenAnotherAgentParks: another agent parking in the queue
// is what the count is for, and a second one raises it. A submission belongs to
// no agent, so it counts too.
func TestTheCountChangesWhenAnotherAgentParks(t *testing.T) {
	f := newNeedsYouFleet(loadedFleet(agentItem("a2")))
	m := needsYouModel(t, f, nil)

	if got := stripANSI(m.renderStatusLine(100)); !strings.Contains(got, "needs you 1") {
		t.Fatalf("status line = %q, want the count of one parked agent", got)
	}

	f.state = loadedFleet(agentItem("a2"), agentItem("a3"))
	m = deliverFleetChange(t, m)
	if got := stripANSI(m.renderStatusLine(100)); !strings.Contains(got, "needs you 2") {
		t.Fatalf("status line = %q, want the count of two parked agents", got)
	}

	f.state = loadedFleet(agentItem("a2"), agentItem("a3"), intakeItem("s1", "Tidy the docs"))
	m = deliverFleetChange(t, m)
	if got := stripANSI(m.renderStatusLine(100)); !strings.Contains(got, "needs you 3") {
		t.Fatalf("status line = %q, want the submission counted too", got)
	}
}

// TestTheCountLeavesOutTheShownAgent: the shown agent's own item is this
// screen's business, not another agent's, so it is not counted.
func TestTheCountLeavesOutTheShownAgent(t *testing.T) {
	f := newNeedsYouFleet(loadedFleet(agentItem("a1"), agentItem("a2")))
	m := needsYouModel(t, f, nil, WithAgentSession("a1", "Alpha"))

	got := stripANSI(m.renderStatusLine(100))
	if !strings.Contains(got, "needs you 1") {
		t.Fatalf("status line = %q, want only the other agent counted", got)
	}
	if strings.Contains(got, "needs you 2") {
		t.Fatalf("status line counted the shown agent's own item: %q", got)
	}
}

// TestNoCountAtZero: a queue with nothing in it is not a segment.
func TestNoCountAtZero(t *testing.T) {
	f := newNeedsYouFleet(loadedFleet())
	m := needsYouModel(t, f, nil)

	if got := stripANSI(m.renderStatusLine(100)); strings.Contains(got, "needs you") {
		t.Fatalf("status line = %q, want no count at zero", got)
	}
}

// TestNoCountWithoutAFleet: embedded mode builds the model without WithFleet,
// and has no fleet to count.
func TestNoCountWithoutAFleet(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	m := testModel(t, state)
	m.resize(100, 30)

	if m.fleet != nil {
		t.Fatal("a model built without WithFleet holds a fleet")
	}
	if got := stripANSI(m.renderStatusLine(100)); strings.Contains(got, "needs you") {
		t.Fatalf("status line = %q, want no count without a fleet", got)
	}
	if cmd := m.pumpFleet(); cmd != nil {
		t.Fatal("a model built without a fleet armed the fleet's wait")
	}
}

// TestTheSessionScreenNotifiesWithAWatch: with the loop's watch, a change that
// parks another agent rings the bell, as it does on the fleet screen.
func TestTheSessionScreenNotifiesWithAWatch(t *testing.T) {
	f := newNeedsYouFleet(loadedFleet())
	clock := func() time.Time { return time.Unix(1000, 0) }
	m := needsYouModel(t, f, fleet.NewWatch(nil, clock))

	// The first state the watch sees is a baseline, not a batch of changes.
	m = deliverFleetChange(t, m)

	f.state = parkedFleet("a1")
	updated, cmd := m.Update(fleetChangedMsg{ch: f.Changed()})
	_ = asModel(t, updated)
	if !ringsBell(t, f, cmd) {
		t.Fatalf("the update returned %v, want a command ringing the bell", fleetMsgs(t, f, cmd))
	}
}

// TestNoBellWithoutAWatch: a plain `marshal` that never opens the fleet has no
// watch, so it counts but never notifies (decision 8).
func TestNoBellWithoutAWatch(t *testing.T) {
	f := newNeedsYouFleet(loadedFleet())
	m := needsYouModel(t, f, nil)
	m = deliverFleetChange(t, m)

	f.state = parkedFleet("a1")
	updated, cmd := m.Update(fleetChangedMsg{ch: f.Changed()})
	_ = asModel(t, updated)

	msgs := fleetMsgs(t, f, cmd)
	for _, msg := range msgs {
		if raw, ok := msg.(tea.RawMsg); ok {
			t.Fatalf("the session screen rang %q without a watch", raw.Msg)
		}
	}
	// The wait is still re-armed: the count is the fleet's business, not the
	// watch's.
	rearmed := false
	for _, msg := range msgs {
		if _, ok := msg.(fleetChangedMsg); ok {
			rearmed = true
		}
	}
	if !rearmed {
		t.Fatalf("the update returned %v, want the fleet's wait re-armed", msgs)
	}
}

// TestTheFleetWaitEndsOnQuit is the regression for a quit that never released
// the fleet's wait: without it the armed wait stays blocked on the client's
// signal, which outlives this program, so one goroutine leaks per run.
func TestTheFleetWaitEndsOnQuit(t *testing.T) {
	f := newNeedsYouFleet(loadedFleet())
	m := needsYouModel(t, f, nil)

	// Arm the wait the way Init does, but do not fire the client's change
	// channel: the point is that the quit alone releases the wait.
	wait := m.pumpFleet()
	if wait == nil {
		t.Fatal("the model did not arm the fleet's wait")
	}

	quit := m.finishQuit()
	if quit == nil {
		t.Fatal("finishQuit returned no command")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("finishQuit returned %T, want tea.QuitMsg", quit())
	}

	done := make(chan tea.Msg, 1)
	go func() { done <- wait() }()
	select {
	case msg := <-done:
		if msg != nil {
			t.Fatalf("the wait after quit returned %T, want nil", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("the quit did not release the fleet's wait")
	}
}

// TestTheStatusLineGoldenShowsTheCount pins the segment's place on the status
// line at 100 columns, beside the count's own goldens.
func TestTheStatusLineGoldenShowsTheCount(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	prev := theme.Current()
	t.Cleanup(func() { theme.Reload(prev) })
	theme.Reload(theme.LoadWithConfig("", "", theme.DepthFlat, nil))

	render := func() string {
		// The working directory is a temp dir, so the line's own segment is
		// pinned to a fixed path, and the project is trusted so the hints
		// survive beside the count: the golden compares the whole row.
		state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
		state.SetTrusted(true)
		stub := &stubSession{patch: func(st *acpwire.SessionStatus) { st.WorkingDir = "/home/marshal/project" }}
		f := newNeedsYouFleet(loadedFleet(agentItem("a2"), agentItem("a3")))
		m := testModelStub(t, state, stub, WithFleet(f, nil))
		m.resize(100, 30)
		return stripANSI(m.renderStatusLine(100))
	}

	first := render()
	if second := render(); first != second {
		t.Fatalf("the status line is not deterministic:\nfirst:  %q\nsecond: %q", first, second)
	}
	if !strings.Contains(first, "needs you 2") {
		t.Fatalf("the golden's row has no count: %q", first)
	}
	checkStatusGolden(t, "status-needs-you-100", first)
}

// checkStatusGolden compares got with testdata/<name>.golden, or rewrites it
// under -update, as the transcript goldens do. It sits directly under testdata/
// like its neighbours in the fleet package.
func checkStatusGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update to write the goldens)", path, err)
	}
	if string(want) != got+"\n" {
		t.Fatalf("%s: line differs:\nwant: %q\n got: %q", path, strings.TrimRight(string(want), "\n"), got)
	}
}

// ringsBell reports whether cmd carries the bell: the messages it produces, its
// batch flattened, hold a RawMsg with the bell character.
func ringsBell(t *testing.T, f *needsYouFleet, cmd tea.Cmd) bool {
	t.Helper()
	for _, msg := range fleetMsgs(t, f, cmd) {
		if raw, ok := msg.(tea.RawMsg); ok && raw.Msg == bell {
			return true
		}
	}
	return false
}

// fleetMsgs runs cmd and, when it is a batch, each of its commands. The wait
// for the client's signal ends on the signal the fake fires, so nothing blocks.
func fleetMsgs(t *testing.T, f *needsYouFleet, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	f.fire()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		if c == nil {
			continue
		}
		f.fire()
		out = append(out, c())
	}
	return out
}
