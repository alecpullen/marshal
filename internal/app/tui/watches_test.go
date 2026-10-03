package tui

import (
	"context"
	"testing"
	"time"

	"marshal/internal/pubsub"
	"marshal/internal/watch"
)

func watchEvent(id, name string, kind watch.Kind, state watch.State) watch.Event {
	return watch.Event{WatchID: id, Name: name, Kind: kind, State: state}
}

// resumeWatchEvent builds the report-bearing event shape the auto-resume
// wake sites act on: a fired command watch opted into resume, carrying its
// mode so the TUI can add the repeat stop hint to the wrapper goal.
func resumeWatchEvent(id, name string, mode watch.Mode) watch.Event {
	return watch.Event{WatchID: id, Name: name, Kind: watch.KindCommand, State: watch.StateFired, Mode: mode, Resume: true}
}

func TestPumpBridgesWatchEventsToMsgs(t *testing.T) {
	// First call: nothing published. The pump cmd must block until a
	// publish arrives or ctx is cancelled (not return nil immediately).
	blockingBroker := pubsub.NewBroker[watch.Event]()
	blockingCtx, blockingCancel := context.WithCancel(context.Background())
	cmd := pumpWatchEvents(blockingBroker.Subscribe(blockingCtx))
	first := runCmdOnce(cmd, 20*time.Millisecond)
	blockingCancel()
	if first != nil {
		t.Fatalf("expected pump to block on empty broker, got immediate msg: %#v", first)
	}

	// Second call: publish from another goroutine, then call the pump cmd
	// and expect a watchMsg.
	b := pubsub.NewBroker[watch.Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx)
	go func() {
		time.Sleep(10 * time.Millisecond)
		b.Publish("watch", watchEvent("w1", "build", watch.KindCommand, watch.StateWatching))
	}()
	cmd = pumpWatchEvents(ch)
	msg := runCmdOnce(cmd, time.Second)
	if msg == nil {
		t.Fatal("pump did not bridge the event")
	}
	wm, ok := msg.(watchMsg)
	if !ok {
		t.Fatalf("got %T, want watchMsg", msg)
	}
	if wm.event.WatchID != "w1" || wm.event.Name != "build" {
		t.Fatalf("event = %+v, want w1/build", wm.event)
	}
}

// handleWatchMsg updates the cached snapshot and re-arms the pump.
func TestHandleWatchMsgUpdatesSnapshot(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.handleWatchMsg(watchMsg{event: watchEvent("w1", "build", watch.KindCommand, watch.StateWatching)})
	mm := asModel(t, m2)
	if len(mm.watches) != 1 {
		t.Fatalf("watches = %d, want 1", len(mm.watches))
	}
	if mm.watches[0].Name != "build" {
		t.Fatalf("watch name = %q, want build", mm.watches[0].Name)
	}

	// A second event for the same watch replaces the entry.
	m3, _ := mm.handleWatchMsg(watchMsg{event: watchEvent("w1", "build", watch.KindCommand, watch.StateFired)})
	mm3 := asModel(t, m3)
	if len(mm3.watches) != 1 {
		t.Fatalf("watches = %d, want 1 after update", len(mm3.watches))
	}
	if mm3.watches[0].State != watch.StateFired {
		t.Fatalf("watch state = %q, want fired", mm3.watches[0].State)
	}
}
