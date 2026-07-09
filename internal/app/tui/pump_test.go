package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"marshal/internal/pubsub"
	"marshal/internal/tools/native"
)

func TestPumpBridgesJobEventsToMsgs(t *testing.T) {
	b := pubsub.NewBroker[native.JobEvent]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// First call: nothing published. The pump cmd must block until a
	// publish arrives or ctx is cancelled (not return nil immediately).
	cmd := pumpJobEvents(ctx, b)
	first := runCmdOnce(cmd, 20*time.Millisecond)
	if first != nil {
		t.Fatalf("expected pump to block on empty broker, got immediate msg: %#v", first)
	}

	// Second call: publish from another goroutine, then call the pump cmd
	// and expect a jobCountMsg.
	go func() {
		time.Sleep(10 * time.Millisecond)
		b.Publish("jobs", native.JobEvent{Count: 3, Delta: 1})
	}()
	cmd = pumpJobEvents(ctx, b)
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
	cancel()
	cmd := pumpJobEvents(ctx, b)
	if msg := runCmdOnce(cmd, 100*time.Millisecond); msg != nil {
		t.Fatalf("expected nil msg on cancelled ctx, got %#v", msg)
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
