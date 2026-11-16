package fleet

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/web/bridge/contract"
)

// recordingNotifier is the watch's notifier, keeping what it was told. It does
// not block: the assertions read what it collected.
type recordingNotifier struct{ notes []Notification }

func (r *recordingNotifier) Notify(n Notification) { r.notes = append(r.notes, n) }

// testClock is the burst's clock: the test moves it a step at a time, so the
// 2s window is decided by the test and never by the wall clock.
type testClock struct{ t time.Time }

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 3, 4, 15, 0, 0, 0, time.Local)}
}

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// needing is a loaded epoch-1 state whose one agent, Alpha, is parked on kind:
// it entered groups.needsYou, which is what notifies.
func needing(kind contract.PendingKind) acpclient.FleetState {
	s := loadedState(1)
	s.Agents = []contract.AgentStatus{{
		ID:      "a1",
		Name:    "Alpha",
		Status:  contract.AgentAwaitingApproval,
		Pending: &contract.PendingRequest{Kind: kind},
	}}
	s.Inbox.Groups.NeedsYou = []string{"a1"}
	return s
}

func TestTheFirstStateNotifiesNothing(t *testing.T) {
	n := &recordingNotifier{}
	w := NewWatch(n, newTestClock().now)

	if got := w.Observe(needing(contract.PendingApproval)); len(got) != 0 {
		t.Fatalf("the first state notified %v, want none: it is the baseline", got)
	}
	if len(n.notes) != 0 {
		t.Fatalf("the notifier got %v, want none", n.notes)
	}
}

func TestANewEpochNotifiesNothing(t *testing.T) {
	n := &recordingNotifier{}
	w := NewWatch(n, newTestClock().now)
	w.Observe(loadedState(1))

	// A fresh full read lists the parked agent already: nothing entered.
	fresh := needing(contract.PendingApproval)
	fresh.Epoch = 2
	if got := w.Observe(fresh); len(got) != 0 {
		t.Fatalf("a new epoch notified %v, want none: it is a fresh baseline", got)
	}
	if len(n.notes) != 0 {
		t.Fatalf("the notifier got %v, want none", n.notes)
	}
}

func TestAnApprovalNotifies(t *testing.T) {
	n := &recordingNotifier{}
	w := NewWatch(n, newTestClock().now)
	w.Observe(loadedState(1))

	got := w.Observe(needing(contract.PendingApproval))
	want := Notification{Key: "a1", Text: "Alpha needs you: approval"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("notified %v, want [%v]", got, want)
	}
	if len(n.notes) != 1 || n.notes[0] != want {
		t.Fatalf("the notifier got %v, want [%v]", n.notes, want)
	}
}

func TestAQuestionNotifies(t *testing.T) {
	n := &recordingNotifier{}
	w := NewWatch(n, newTestClock().now)
	w.Observe(loadedState(1))

	s := needing(contract.PendingQuestion)
	s.Agents[0].Status = contract.AgentAwaitingQuestion
	got := w.Observe(s)
	want := Notification{Key: "a1", Text: "Alpha needs you: question"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("notified %v, want [%v]", got, want)
	}
	if len(n.notes) != 1 || n.notes[0] != want {
		t.Fatalf("the notifier got %v, want [%v]", n.notes, want)
	}
}

func TestASubmissionNotifies(t *testing.T) {
	n := &recordingNotifier{}
	w := NewWatch(n, newTestClock().now)
	w.Observe(loadedState(1))

	s := loadedState(1)
	s.Inbox.Queue = []contract.InboxItem{{
		Kind:       contract.InboxItemIntake,
		Submission: &contract.PendingSubmission{ID: "s1", Title: "Ship the parser"},
	}}
	got := w.Observe(s)
	want := Notification{Key: "s1", Text: "Ship the parser needs you: intake"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("notified %v, want [%v]", got, want)
	}
	if len(n.notes) != 1 || n.notes[0] != want {
		t.Fatalf("the notifier got %v, want [%v]", n.notes, want)
	}
}

func TestReadyNotifies(t *testing.T) {
	n := &recordingNotifier{}
	w := NewWatch(n, newTestClock().now)
	w.Observe(loadedState(1))

	s := loadedState(1)
	s.Agents = []contract.AgentStatus{{ID: "a1", Name: "Alpha", Status: contract.AgentIdle}}
	s.Inbox.Groups.Ready = []string{"a1"}
	got := w.Observe(s)
	want := Notification{Key: "a1", Text: "Alpha is ready"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("notified %v, want [%v]", got, want)
	}
	if len(n.notes) != 1 || n.notes[0] != want {
		t.Fatalf("the notifier got %v, want [%v]", n.notes, want)
	}
}

func TestAFailureDoesNotNotify(t *testing.T) {
	n := &recordingNotifier{}
	w := NewWatch(n, newTestClock().now)
	w.Observe(loadedState(1))

	s := loadedState(1)
	s.Agents = []contract.AgentStatus{{ID: "a1", Name: "Alpha", Status: contract.AgentError}}
	if got := w.Observe(s); len(got) != 0 {
		t.Fatalf("a failed agent notified %v, want none: it is an Events line", got)
	}
	if len(n.notes) != 0 {
		t.Fatalf("the notifier got %v, want none", n.notes)
	}
}

func TestABurstCollapses(t *testing.T) {
	n := &recordingNotifier{}
	clock := newTestClock()
	w := NewWatch(n, clock.now)
	w.Observe(loadedState(1))

	if got := w.Observe(needing(contract.PendingApproval)); len(got) != 1 {
		t.Fatalf("the first approval notified %d, want 1", len(got))
	}

	// The agent settles and is parked again 1s later: inside the 2s window,
	// so the second notification is dropped.
	clock.advance(time.Second)
	w.Observe(loadedState(1))
	if got := w.Observe(needing(contract.PendingApproval)); len(got) != 0 {
		t.Fatalf("a second approval 1s later notified %v, want none: the burst drops it", got)
	}

	// 3s after the first, the window has passed: it notifies again.
	clock.advance(2 * time.Second)
	w.Observe(loadedState(1))
	got := w.Observe(needing(contract.PendingApproval))
	if len(got) != 1 || got[0].Key != "a1" {
		t.Fatalf("a third approval 3s after the first notified %v, want one for a1", got)
	}
	if len(n.notes) != 2 {
		t.Fatalf("the notifier got %d notifications, want 2: one per burst", len(n.notes))
	}
}

func TestTheBurstIsPerAgent(t *testing.T) {
	n := &recordingNotifier{}
	clock := newTestClock()
	w := NewWatch(n, clock.now)
	w.Observe(loadedState(1))

	if got := w.Observe(needing(contract.PendingApproval)); len(got) != 1 {
		t.Fatalf("Alpha's approval notified %d, want 1", len(got))
	}

	// Bravo is parked 1s later: another agent, another burst.
	clock.advance(time.Second)
	both := needing(contract.PendingApproval)
	both.Agents = append(both.Agents, contract.AgentStatus{
		ID:      "b1",
		Name:    "Bravo",
		Status:  contract.AgentAwaitingQuestion,
		Pending: &contract.PendingRequest{Kind: contract.PendingQuestion},
	})
	both.Inbox.Groups.NeedsYou = []string{"a1", "b1"}

	got := w.Observe(both)
	if len(got) != 1 || got[0].Key != "b1" {
		t.Fatalf("notified %v, want one for b1", got)
	}
	if len(n.notes) != 2 {
		t.Fatalf("the notifier got %v, want both agents' notifications", n.notes)
	}
}

func TestTheWatchSpansPrograms(t *testing.T) {
	f := newFakeFleet()
	n := &recordingNotifier{}
	clock := newTestClock()
	w := NewWatch(n, clock.now)

	// The fleet program reads its baseline.
	first := New(f, WithWatch(w), WithNow(clock.now))
	f.change(t, first, loadedState(1))

	// The loop switches to a session screen: the change lands now, and the
	// watch still compares it with what the fleet program read.
	second := New(f, WithWatch(w), WithNow(clock.now))
	clock.advance(time.Second)
	f.state = needing(contract.PendingApproval)
	deliver(t, second)

	if len(n.notes) != 1 || n.notes[0].Text != "Alpha needs you: approval" {
		t.Fatalf("the notifier got %v, want the change between the programs", n.notes)
	}
}

func TestTheFleetModelRingsTheBell(t *testing.T) {
	f := newFakeFleet()
	n := &recordingNotifier{}
	clock := newTestClock()
	m := New(f, WithWatch(NewWatch(n, clock.now)), WithNow(clock.now))
	m = f.change(t, m, loadedState(1))

	f.state = needing(contract.PendingApproval)
	next, cmd := m.Update(changedMsg{})
	if _, ok := next.(Model); !ok {
		t.Fatalf("Update returned %T, want fleet.Model", next)
	}
	if !ringsBell(t, f, cmd) {
		t.Fatalf("the update returned %v, want a command ringing the bell", msgsOf(t, f, cmd))
	}
	if len(n.notes) != 1 {
		t.Fatalf("the notifier got %v, want the approval", n.notes)
	}
}

// ringsBell reports whether cmd carries the bell: the messages it produces,
// its batch flattened, hold a RawMsg with the bell character.
func ringsBell(t *testing.T, f *fakeFleet, cmd tea.Cmd) bool {
	t.Helper()
	for _, msg := range msgsOf(t, f, cmd) {
		if raw, ok := msg.(tea.RawMsg); ok && raw.Msg == bell {
			return true
		}
	}
	return false
}

// msgsOf runs cmd and, when it is a batch, each of its commands. The wait for
// the client's signal ends on the signal the fake fires, so nothing blocks.
func msgsOf(t *testing.T, f *fakeFleet, cmd tea.Cmd) []tea.Msg {
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
