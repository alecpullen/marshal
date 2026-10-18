package tui

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
	"marshal/internal/tools/native"
	"marshal/internal/viewmodel"
)

func TestPumpBridgesJobEventsToMsgs(t *testing.T) {
	// First call: nothing published. The pump cmd must block until a
	// publish arrives or ctx is cancelled (not return nil immediately).
	blockingBroker := pubsub.NewBroker[native.JobEvent]()
	blockingCtx, blockingCancel := context.WithCancel(context.Background())
	cmd := pumpJobEvents(blockingBroker.Subscribe(blockingCtx))
	first := runCmdOnce(cmd, 20*time.Millisecond)
	blockingCancel()
	if first != nil {
		t.Fatalf("expected pump to block on empty broker, got immediate msg: %#v", first)
	}

	// Second call: publish from another goroutine, then call the pump cmd
	// and expect a jobCountMsg.
	b := pubsub.NewBroker[native.JobEvent]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx)
	go func() {
		time.Sleep(10 * time.Millisecond)
		b.Publish("jobs", native.JobEvent{Count: 3, Delta: 1})
	}()
	cmd = pumpJobEvents(ch)
	msg := runCmdOnce(cmd, time.Second)
	if msg == nil {
		t.Fatal("pump did not bridge the event")
	}
	jc, ok := msg.(jobCountMsg)
	if !ok {
		t.Fatalf("got %T, want jobCountMsg", msg)
	}
	if jc.count != 3 {
		t.Fatalf("count = %d, want 3", jc.count)
	}
}

func TestPumpReturnsNilOnCtxCancel(t *testing.T) {
	b := pubsub.NewBroker[native.JobEvent]()
	ctx, cancel := context.WithCancel(context.Background())
	ch := b.Subscribe(ctx)
	cancel()
	cmd := pumpJobEvents(ch)
	if msg := runCmdOnce(cmd, 100*time.Millisecond); msg != nil {
		t.Fatalf("expected nil msg on cancelled ctx, got %#v", msg)
	}
}

func TestModelJobPumpRearmsWithoutLeakingTerminalSubscriptions(t *testing.T) {
	b := pubsub.NewBroker[native.JobEvent](pubsub.WithBufferSize[native.JobEvent](0), pubsub.WithTerminal[native.JobEvent]())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	m := New(state, WithJobBroker(ctx, b))

	updated, cmd := m.Update(jobCountMsg{count: 1})
	m = updated.(Model)
	msg := publishJobAndRunCmd(t, b, cmd, 2)
	updated, cmd = m.Update(msg)
	m = updated.(Model)
	msg = publishJobAndRunCmd(t, b, cmd, 3)
	jc, ok := msg.(jobCountMsg)
	if !ok {
		t.Fatalf("got %T, want jobCountMsg", msg)
	}
	if jc.count != 3 {
		t.Fatalf("count = %d, want 3", jc.count)
	}
}

func publishJobAndRunCmd(t *testing.T, b *pubsub.Broker[native.JobEvent], cmd tea.Cmd, count int) tea.Msg {
	t.Helper()
	published := make(chan struct{})
	go func() {
		b.Publish("jobs", native.JobEvent{Count: count, Delta: 1})
		close(published)
	}()
	msg := runCmdOnce(cmd, time.Second)
	if msg == nil {
		t.Fatalf("pump did not deliver job count %d", count)
	}
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatalf("publish for job count %d blocked", count)
	}
	return msg
}

// TestPumpSteeringEventsCarriesSnapshotAndReceipt pins the additive Send now
// channels: the same pump keeps the QueueLen compatibility fields and forwards
// the snapshot and receipt the event carried.
func TestPumpSteeringEventsCarriesSnapshotAndReceipt(t *testing.T) {
	b := pubsub.NewBroker[session.SteeringEvent]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx)

	snap := session.SteeringSnapshot{Version: 7, TurnID: "turn", Active: true, Supported: true, PendingCount: 2}
	receipt := session.SteeringReceipt{Version: 3, RequestID: "req-1", TurnID: "turn", Status: session.SteeringInterrupting}
	go func() {
		time.Sleep(10 * time.Millisecond)
		b.Publish("steering", session.SteeringEvent{QueueLen: 2, Message: "queued", Snapshot: &snap, Receipt: &receipt})
	}()

	msg := runCmdOnce(pumpSteeringEvents(ch), time.Second)
	got, ok := msg.(steeringMsg)
	if !ok {
		t.Fatalf("got %T, want steeringMsg", msg)
	}
	if got.queueLen != 2 || got.message != "queued" {
		t.Fatalf("compatibility fields = %d/%q, want 2/queued", got.queueLen, got.message)
	}
	if got.snapshot == nil || *got.snapshot != snap {
		t.Fatalf("snapshot = %+v, want %+v", got.snapshot, snap)
	}
	if got.receipt == nil || got.receipt.RequestID != receipt.RequestID || got.receipt.Status != receipt.Status {
		t.Fatalf("receipt = %+v, want %+v", got.receipt, receipt)
	}
}

// TestModelSteeringMsgWithoutBrokerCachesState is the no-broker counterpart: a
// model with no steering broker re-arms nothing and still caches what a
// directly-delivered message carried.
func TestModelSteeringMsgWithoutBrokerCachesState(t *testing.T) {
	m := newTestModel(t)
	snap := session.SteeringSnapshot{Version: 4, TurnID: "turn", Active: true, Supported: true}
	receipt := session.SteeringReceipt{Version: 2, RequestID: "req", TurnID: "turn", Status: session.SteeringWaitingResume}
	updated, cmd := m.Update(steeringMsg{queueLen: 3, snapshot: &snap, receipt: &receipt})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("a broker-less model returned a cmd (%T), want nil", cmd)
	}
	if m.queuedCount != 3 {
		t.Fatalf("queuedCount = %d, want 3", m.queuedCount)
	}
	if m.steeringSnapshot != snap {
		t.Fatalf("cached snapshot = %+v, want %+v", m.steeringSnapshot, snap)
	}
	if got := m.steeringReceipts["req"].Status; got != session.SteeringWaitingResume {
		t.Fatalf("cached receipt status = %q, want waiting_for_resume", got)
	}
}

// runCmdOnce runs the cmd once with a hard timeout. Returns the produced
// msg, or nil if the cmd blocked past the timeout (the cmd is then left
// to finish in the background — acceptable for blocking-pump tests).
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
		return nil
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

// newWorkModel builds a model with a Work service attached, running the same
// one-time subscription New does for the initial conversation.
func newWorkModel(t *testing.T, svc viewmodel.WorkService) Model {
	t.Helper()
	m := newTestModel(t)
	m.workService = svc
	m.startWorkSubscription()
	m.refreshViewport()
	return m
}

// TestWorkPumpSingleSubscriptionAndSessionRebind pins the subscription
// lifecycle: one subscription for the initial conversation, the pump re-arming
// the same channel, and a rebind cancelling and replacing it so a superseded
// event can never be applied.
func TestWorkPumpSingleSubscriptionAndSessionRebind(t *testing.T) {
	svc := &fakeWorkService{snap: viewmodel.WorkSnapshot{ConversationID: "convA", RuntimeEpoch: "epochA", Revision: 1}}
	m := newWorkModel(t, svc)

	if got := svc.subscriptionCount(); got != 1 {
		t.Fatalf("initial subscriptions = %d, want exactly 1", got)
	}
	if m.workEvents == nil || m.workSubscriptionID == 0 {
		t.Fatal("no work subscription was established")
	}
	if m.workConversation != "convA" || m.workSnapshot.ConversationID != "convA" {
		t.Fatalf("bound conversation = %q, seed = %q, want convA", m.workConversation, m.workSnapshot.ConversationID)
	}

	// A published snapshot bridges through the pump, which captures the
	// subscription ID, and Update re-arms the same channel.
	svc.publish(viewmodel.WorkSnapshot{ConversationID: "convA", RuntimeEpoch: "epochA", Revision: 2})
	wm, ok := runCmdOnce(pumpWorkEvents(m.workEvents, m.workSubscriptionID), time.Second).(workMsg)
	if !ok {
		t.Fatal("the work pump did not bridge a published snapshot")
	}
	if wm.subscriptionID != m.workSubscriptionID {
		t.Fatalf("pump captured subscription %d, want %d", wm.subscriptionID, m.workSubscriptionID)
	}
	updated, rearm := m.Update(wm)
	m = updated.(Model)
	if m.workSnapshot.Revision != 2 {
		t.Fatalf("applied revision = %d, want 2", m.workSnapshot.Revision)
	}
	if rearm == nil {
		t.Fatal("Update did not re-arm the work pump")
	}

	// An event whose subscription ID is no longer current is ignored.
	updated, _ = m.Update(workMsg{
		snapshot:       viewmodel.WorkSnapshot{ConversationID: "convA", Revision: 99},
		subscriptionID: m.workSubscriptionID + 1,
	})
	m = updated.(Model)
	if m.workSnapshot.Revision != 2 {
		t.Fatalf("a superseded subscription event was applied: revision %d", m.workSnapshot.Revision)
	}

	// Session rebind: the old subscription is cancelled, a new one is opened,
	// the ID advances and the new conversation's epoch is seeded.
	staleID := wm.subscriptionID
	svc.mu.Lock()
	svc.snap = viewmodel.WorkSnapshot{ConversationID: "convB", RuntimeEpoch: "epochB", Revision: 1}
	svc.mu.Unlock()
	rebind := m.rebindWorkSession()
	if got := svc.subscriptionCount(); got != 2 {
		t.Fatalf("subscriptions after rebind = %d, want 2", got)
	}
	if m.workSubscriptionID == staleID {
		t.Fatal("workSubscriptionID did not advance on rebind")
	}
	if m.workConversation != "convB" || m.workSnapshot.ConversationID != "convB" {
		t.Fatalf("rebind did not seed convB: bound %q seed %q", m.workConversation, m.workSnapshot.ConversationID)
	}
	if rebind == nil {
		t.Fatal("rebind did not re-arm the pump")
	}
	// A late event from the superseded subscription is ignored after the
	// rebind, even though it names the old conversation.
	updated, _ = m.Update(workMsg{
		snapshot:       viewmodel.WorkSnapshot{ConversationID: "convA", Revision: 500},
		subscriptionID: staleID,
	})
	m = updated.(Model)
	if m.workSnapshot.ConversationID != "convB" || m.workSnapshot.Revision != 1 {
		t.Fatalf("stale event after rebind applied: %+v", m.workSnapshot)
	}
}

// TestWorkUpdateAcceptsNewRevisionWhilePromptOpen pins that a work update lands
// even while a prompt and a dock panel are up, without blocking or being
// swallowed by the prompt routing.
func TestWorkUpdateAcceptsNewRevisionWhilePromptOpen(t *testing.T) {
	svc := &fakeWorkService{snap: viewmodel.WorkSnapshot{ConversationID: "conv", RuntimeEpoch: "epoch", Revision: 1}}
	m := newWorkModel(t, svc)
	m.workSnapshot = viewmodel.WorkSnapshot{ConversationID: "conv", Revision: 1, Items: []viewmodel.WorkItem{workTask("work:a", "Alpha")}}
	m = ctrlT(m)
	if !m.tasksOpen() {
		t.Fatal("the Tasks panel did not open")
	}
	// A question prompt is pending while the panel is open.
	m.state.SetPendingQuestion(&session.PendingQuestion{
		Key:       session.PromptKey{ID: "q1", Kind: session.PromptQuestion},
		Questions: []session.Question{{Question: "what?"}},
	})

	updated, cmd := m.Update(workMsg{
		snapshot: viewmodel.WorkSnapshot{ConversationID: "conv", Revision: 7,
			Items: []viewmodel.WorkItem{workTask("work:b", "Beta")}},
		subscriptionID: m.workSubscriptionID,
	})
	m = updated.(Model)

	if m.workSnapshot.Revision != 7 {
		t.Fatalf("revision while prompt open = %d, want 7", m.workSnapshot.Revision)
	}
	if cmd == nil {
		t.Fatal("a background work update was dropped instead of re-arming the pump")
	}
	if m.tasksPanel == nil || m.tasksPanel.snapshot.Revision != 7 {
		t.Fatal("the open panel did not receive the new projection")
	}
	if m.state.PendingQuestion() == nil {
		t.Fatal("the pending prompt was cleared by a background work update")
	}
}
