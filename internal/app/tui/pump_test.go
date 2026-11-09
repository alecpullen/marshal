package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"marshal/internal/acp/acptest"
	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
	"marshal/internal/viewmodel"
)

// TestPumpChangedCarriesItsChannel pins that the pump reports the channel it
// read, so the handler can tell a replaced session's pump from the current one.
func TestPumpChangedCarriesItsChannel(t *testing.T) {
	ch := make(chan struct{}, 1)
	ch <- struct{}{}
	msg := runCmdOnce(pumpChanged(ch), time.Second)
	got, ok := msg.(changedMsg)
	if !ok {
		t.Fatalf("got %T, want changedMsg", msg)
	}
	if got.ch != ch {
		t.Fatal("changedMsg did not carry the channel the pump read")
	}
}

// TestPumpChangedEndsOnClose pins that a closed channel ends the pump.
func TestPumpChangedEndsOnClose(t *testing.T) {
	ch := make(chan struct{})
	close(ch)
	msg := runCmdOnce(pumpChanged(ch), time.Second)
	// A nil success value makes a blocking regression indistinguishable from
	// the expected result, so the deadline is checked as its own outcome.
	if _, timedOut := msg.(cmdTimeout); timedOut {
		t.Fatal("the pump blocked on a closed channel instead of ending")
	}
	if msg != nil {
		t.Fatalf("got %#v, want nil on a closed channel", msg)
	}
}

// TestHandleChangedIgnoresAReplacedSession pins that a message from a pump
// belonging to a replaced session is dropped and not re-armed.
func TestHandleChangedIgnoresAReplacedSession(t *testing.T) {
	m := newTestModel(t)
	m.refreshViewport()
	before := m.lastTranscriptHash
	// A transcript change makes the next refresh produce a different hash.
	stateOf(m).AddMessage(session.RoleSystem, "changed", session.ContentTypePlain)

	other := make(chan struct{}, 1)
	updated, cmd := m.Update(changedMsg{ch: other})
	mm := updated.(Model)
	if cmd != nil {
		t.Fatalf("a replaced session's message returned a cmd (%T), want nil", cmd)
	}
	if mm.lastTranscriptHash != before {
		t.Fatal("a replaced session's message refreshed the transcript")
	}

	updated, cmd = mm.Update(changedMsg{ch: mm.sess.Changed()})
	mm = updated.(Model)
	if cmd == nil {
		t.Fatal("the current session's message returned no cmd")
	}
	if mm.lastTranscriptHash == before {
		t.Fatal("the current session's message did not refresh the transcript")
	}
}

// cmdTimeout is the message runCmdOnce returns when a cmd blocked past its
// deadline. It is a distinct value, not nil, because several cmds legitimately
// return nil: a test whose success value is nil would otherwise pass when the
// cmd never returned at all. The cmd is left to finish in the background, which
// is acceptable for the blocking-pump tests.
type cmdTimeout struct{}

func (cmdTimeout) String() string { return "cmd timed out" }

// runCmdOnce runs the cmd once with a hard timeout. Returns the produced msg,
// or cmdTimeout if the cmd blocked past the deadline.
func runCmdOnce(cmd tea.Cmd, timeout time.Duration) tea.Msg {
	if cmd == nil {
		return nil
	}
	out := make(chan tea.Msg, 1)
	go func() { out <- cmd() }()
	select {
	case msg := <-out:
		return msg
	case <-time.After(timeout):
		return cmdTimeout{}
	}
}

// fakeWorkService is a scripted viewmodel.WorkService for the Task 18 update
// and action tests. It records calls and lets a test publish snapshots on the
// open subscription.
type fakeWorkService struct {
	mu   sync.Mutex
	snap viewmodel.WorkSnapshot
	subs int
	open []chan pubsub.Event[viewmodel.WorkSnapshot]

	listCalls int
	lastQuery viewmodel.WorkQuery
	page      viewmodel.WorkPage
	pageErr   error

	inspectCalls []viewmodel.WorkTarget
	detail       viewmodel.WorkDetail
	detailErr    error

	stopCalls  []viewmodel.WorkTarget
	stopResult viewmodel.WorkStopResult
	stopErr    error
	ackCalls   []viewmodel.WorkAck
	ackErr     error
}

func (f *fakeWorkService) WorkSnapshot() viewmodel.WorkSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func (f *fakeWorkService) SubscribeWork(ctx context.Context) <-chan pubsub.Event[viewmodel.WorkSnapshot] {
	f.mu.Lock()
	f.subs++
	ch := make(chan pubsub.Event[viewmodel.WorkSnapshot], 16)
	f.open = append(f.open, ch)
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		for i, c := range f.open {
			if c == ch {
				f.open = append(f.open[:i], f.open[i+1:]...)
				break
			}
		}
		close(ch)
		f.mu.Unlock()
	}()
	return ch
}

// publish delivers a snapshot on the newest open subscription without blocking.
// It holds the mutex across the send so it can never race a cancel's close.
func (f *fakeWorkService) publish(snap viewmodel.WorkSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.open) == 0 {
		return
	}
	ch := f.open[len(f.open)-1]
	select {
	case ch <- pubsub.Event[viewmodel.WorkSnapshot]{Type: "work", Payload: snap}:
	default:
	}
}

func (f *fakeWorkService) ListWork(_ context.Context, q viewmodel.WorkQuery) (viewmodel.WorkPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	f.lastQuery = q
	return f.page, f.pageErr
}

func (f *fakeWorkService) InspectWork(_ context.Context, target viewmodel.WorkTarget, _ bool) (viewmodel.WorkDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspectCalls = append(f.inspectCalls, target)
	return f.detail, f.detailErr
}

func (f *fakeWorkService) StopWork(_ context.Context, target viewmodel.WorkTarget, _ string) (viewmodel.WorkStopResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls = append(f.stopCalls, target)
	return f.stopResult, f.stopErr
}

func (f *fakeWorkService) AcknowledgeWork(_ context.Context, ack viewmodel.WorkAck) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ackCalls = append(f.ackCalls, ack)
	return f.ackErr
}

func (f *fakeWorkService) subscriptionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subs
}

// newWorkModel builds a model whose session serves the fake's work, as the
// runtime's session serves the runtime's.
func newWorkModel(t *testing.T, svc viewmodel.WorkService) Model {
	t.Helper()
	m := newTestModel(t)
	_, sess := acptest.Open(t, service.Deps{State: stateOf(m), Work: svc})
	m.sess = sess
	m.resetWork()
	m.refreshViewport()
	return m
}

// vmWorkTask is a running command item as the runtime's service reports it.
func vmWorkTask(key, label string) viewmodel.WorkItem {
	return viewmodel.WorkItem{
		TaskKey:   key,
		Kind:      "command",
		Label:     label,
		SourceID:  key,
		State:     "running",
		StartedAt: workT0,
	}
}

// deliverWork publishes snap on svc and applies the change the session
// signals, as the change pump delivers it. snap must be one the adapter keeps.
func deliverWork(t *testing.T, m Model, svc *fakeWorkService, snap viewmodel.WorkSnapshot) Model {
	t.Helper()
	svc.publish(snap)
	// The connection signals a change in any mirror, and attach-time updates can
	// arrive first, so wait until the work itself has arrived; the deadline is
	// what bounds the wait (P3 plan 8, Task 5 case 4).
	deadline := time.Now().Add(5 * time.Second)
	for {
		// Check the deadline before the next wait, or a pass that starts after
		// it expired would get another 5s inside runCmdOnce.
		if time.Now().After(deadline) {
			t.Fatalf("the work never arrived: revision %d, want %d", m.workSnapshot.Revision, snap.Revision)
		}
		msg := runCmdOnce(pumpChanged(m.sess.Changed()), 5*time.Second)
		if _, ok := msg.(changedMsg); !ok {
			t.Fatalf("the session did not signal the work change: got %#v", msg)
		}
		updated, _ := m.Update(msg)
		m = updated.(Model)
		if m.workSnapshot.Revision >= snap.Revision {
			return m
		}
	}
}

// TestWorkArrivesOnChanged pins that a snapshot the session keeps reaches the
// model and the box through the session's own change signal.
func TestWorkArrivesOnChanged(t *testing.T) {
	svc := &fakeWorkService{snap: viewmodel.WorkSnapshot{ConversationID: "conv", RuntimeEpoch: "epoch", Revision: 1}}
	m := newWorkModel(t, svc)
	if !m.workAvailable() {
		t.Fatal("the session does not serve work")
	}

	m = deliverWork(t, m, svc, viewmodel.WorkSnapshot{
		ConversationID: "conv", RuntimeEpoch: "epoch", Revision: 2,
		Items: []viewmodel.WorkItem{vmWorkTask("work:a", "Alpha")},
	})
	if m.workSnapshot.Revision != 2 {
		t.Fatalf("snapshot revision = %d, want 2", m.workSnapshot.Revision)
	}
	if view := m.viewString(); !strings.Contains(view, "Alpha") {
		t.Fatalf("the box did not show the item's label:\n%s", view)
	}
}

// TestResetWorkStartsTheViewOver pins that a new conversation's work view
// starts from its own snapshot: no previous observations, no expanded card.
func TestResetWorkStartsTheViewOver(t *testing.T) {
	svcA := &fakeWorkService{snap: viewmodel.WorkSnapshot{ConversationID: "A", Revision: 1}}
	m := newWorkModel(t, svcA)
	m.workExpanded = "k"
	m.workPrevious = map[string]*acpwire.WorkProgress{
		"k": {Version: 1, Sequence: 1, ObservedAt: acpwire.Millis(workT0)},
	}

	svcB := &fakeWorkService{snap: viewmodel.WorkSnapshot{ConversationID: "B", Revision: 1}}
	_, sessB := acptest.Open(t, service.Deps{State: stateOf(m), Work: svcB})
	m.sess = sessB
	m.resetWork()

	if m.workSnapshot.ConversationID != "B" {
		t.Fatalf("snapshot conversation = %q, want B", m.workSnapshot.ConversationID)
	}
	if len(m.workPrevious) != 0 {
		t.Fatalf("previous observations = %v, want none", m.workPrevious)
	}
	if m.workExpanded != "" {
		t.Fatalf("expanded card = %q, want none", m.workExpanded)
	}
}

// TestASessionWithoutWorkLeavesTheSnapshotAlone pins that a session serving no
// work is not read as an empty work view.
func TestASessionWithoutWorkLeavesTheSnapshotAlone(t *testing.T) {
	m := newTestModel(t)
	m.workSnapshot = workSnapOf(workTask("work:a", "Alpha"))
	before := m.workSnapshot

	updated, _ := m.Update(changedMsg{ch: m.sess.Changed()})
	m = updated.(Model)

	if m.workSnapshot.ConversationID != before.ConversationID || len(m.workSnapshot.Items) != len(before.Items) {
		t.Fatalf("a session without work changed the snapshot: %+v", m.workSnapshot)
	}
	if !m.workLaneActive() {
		t.Fatal("the work lane went inactive")
	}
}

// TestWorkUpdateAcceptsNewRevisionWhilePromptOpen pins that a work update lands
// even while a prompt and a dock panel are up, without blocking or being
// swallowed by the prompt routing.
func TestWorkUpdateAcceptsNewRevisionWhilePromptOpen(t *testing.T) {
	svc := &fakeWorkService{snap: viewmodel.WorkSnapshot{ConversationID: "conv", RuntimeEpoch: "epoch", Revision: 1}}
	m := newWorkModel(t, svc)
	m.workSnapshot = acpwire.WorkSnapshot{ConversationID: "conv", Revision: 1, Items: []acpwire.WorkItem{workTask("work:a", "Alpha")}}
	m = toggleTasks(m)
	if !m.tasksOpen() {
		t.Fatal("the Tasks panel did not open")
	}
	// A question prompt is pending while the panel is open.
	stateOf(m).SetPendingQuestion(&session.PendingQuestion{
		Key:       session.PromptKey{ID: "q1", Kind: session.PromptQuestion},
		Questions: []session.Question{{Question: "what?"}},
	})

	m = deliverWork(t, m, svc, viewmodel.WorkSnapshot{ConversationID: "conv", RuntimeEpoch: "epoch", Revision: 7,
		Items: []viewmodel.WorkItem{vmWorkTask("work:b", "Beta")}})

	if m.workSnapshot.Revision != 7 {
		t.Fatalf("revision while prompt open = %d, want 7", m.workSnapshot.Revision)
	}
	if m.tasksPanel == nil || m.tasksPanel.snapshot.Revision != 7 {
		t.Fatal("the open panel did not receive the new projection")
	}
	if stateOf(m).PendingQuestion() == nil {
		t.Fatal("the pending prompt was cleared by a background work update")
	}
}

// workProgressItem is a running command carrying one progress observation.
func workProgressItem(key string, observed time.Time) acpwire.WorkItem {
	it := workRunningCard(key, time.Unix(10_000, 0))
	total := 2.0
	it.Progress = &acpwire.WorkProgress{
		Version:    1,
		Sequence:   1,
		Metrics:    []acpwire.WorkMetric{{ID: "bytes", Unit: "bytes", Current: 1, Total: &total}},
		ObservedAt: acpwire.Millis(observed),
	}
	return it
}

// A rate and an ETA need two observations: the snapshot being applied keeps the
// one it replaces.
func TestWorkProgressKeepsThePreviousObservation(t *testing.T) {
	m := newTestModel(t)
	first := time.Unix(10_000, 0)
	m.workSnapshot = workSnapOf(workProgressItem("k", first))
	m.notePreviousProgress(workSnapOf(workProgressItem("k", first.Add(2*time.Second))))
	got := m.workPrevious["k"]
	if got == nil || got.ObservedAt != acpwire.Millis(first) {
		t.Fatalf("previous observation = %v, want %v", got, acpwire.Millis(first))
	}
}

// A settled item has no rate to show, so its entry goes with it.
func TestWorkProgressPrunesSettledItems(t *testing.T) {
	m := newTestModel(t)
	first := time.Unix(10_000, 0)
	m.workSnapshot = workSnapOf(workProgressItem("k", first))
	m.notePreviousProgress(workSnapOf(workProgressItem("k", first.Add(2*time.Second))))
	if m.workPrevious["k"] == nil {
		t.Fatal("setup: no previous observation recorded")
	}
	m.notePreviousProgress(workSnapOf())
	if _, ok := m.workPrevious["k"]; ok {
		t.Fatal("a settled item kept its previous observation")
	}
}

// A repeated observation is not a new measurement, so the earlier previous
// stays: measuring against itself would invent a rate.
func TestWorkProgressIgnoresARepeatedObservation(t *testing.T) {
	m := newTestModel(t)
	first := time.Unix(10_000, 0)
	second := first.Add(2 * time.Second)
	m.workSnapshot = workSnapOf(workProgressItem("k", first))
	m.notePreviousProgress(workSnapOf(workProgressItem("k", second)))
	m.workSnapshot = workSnapOf(workProgressItem("k", second))
	m.notePreviousProgress(workSnapOf(workProgressItem("k", second)))
	got := m.workPrevious["k"]
	if got == nil || got.ObservedAt != acpwire.Millis(first) {
		t.Fatalf("a repeated observation replaced the earlier previous: %v", got)
	}
}
