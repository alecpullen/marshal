package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
	"marshal/internal/tools/native"
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
