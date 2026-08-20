package pubsub

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBrokerDeliversToAllSubscribers(t *testing.T) {
	b := NewBroker[string]()
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()
	ch1 := b.Subscribe(ctx1)
	ch2 := b.Subscribe(ctx2)

	b.Publish("greet", "hello")

	select {
	case e := <-ch1:
		if e.Type != "greet" || e.Payload != "hello" {
			t.Fatalf("ch1 got %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("ch1 timed out")
	}
	select {
	case e := <-ch2:
		if e.Type != "greet" || e.Payload != "hello" {
			t.Fatalf("ch2 got %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("ch2 timed out")
	}
}

func TestBrokerSlowSubscriberDropsNonTerminal(t *testing.T) {
	b := NewBroker[string](WithBufferSize[string](1))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx)

	// Fill the 1-slot buffer, then publish two more — the slow subscriber
	// (nobody reading) must drop the overflow, not block the publisher.
	b.Publish("a", "1")
	b.Publish("a", "2")
	done := make(chan struct{})
	go func() {
		b.Publish("a", "3") // would block if buffer were unbounded
		b.Publish("a", "4")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher blocked on slow subscriber's non-terminal event")
	}

	// Drain what survives — at least the first event must arrive.
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("first event never delivered")
	}
}

func TestBrokerTerminalEventBlocksUntilDelivered(t *testing.T) {
	b := NewBroker[string](WithBufferSize[string](0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx, WithTerminal[string]())

	published := make(chan struct{})
	go func() {
		b.Publish("turn_end", "done") // terminal, 0-buffer → must block
		close(published)
	}()

	// Publisher should be blocked because nobody has drained the terminal event.
	select {
	case <-published:
		t.Fatal("terminal event published before subscriber drained")
	case <-time.After(50 * time.Millisecond):
	}

	select {
	case e := <-ch:
		if e.Type != "turn_end" {
			t.Fatalf("got %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal event never delivered")
	}
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("publisher never unblocked after terminal drain")
	}
}

func TestBrokerCancelDropsPendingTerminal(t *testing.T) {
	b := NewBroker[string](WithBufferSize[string](0))
	ctx, cancel := context.WithCancel(context.Background())
	ch := b.Subscribe(ctx, WithTerminal[string]())

	published := make(chan struct{})
	go func() {
		b.Publish("turn_end", "done")
		close(published)
	}()

	cancel() // subscriber ctx cancelled while terminal publish blocks
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock terminal publish")
	}
	// Channel should be closed.
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed after ctx cancel")
	}
}

func TestBrokerCloseClosesAllChannels(t *testing.T) {
	b := NewBroker[int]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch1 := b.Subscribe(ctx)
	ch2 := b.Subscribe(ctx)
	b.Close()
	for i, ch := range []<-chan Event[int]{ch1, ch2} {
		if _, ok := <-ch; ok {
			t.Fatalf("channel %d not closed by Close", i)
		}
	}
}

func TestBrokerSubscribeAfterCloseReturnsClosedChannel(t *testing.T) {
	b := NewBroker[string]()
	b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx)

	if _, ok := <-ch; ok {
		t.Fatal("Subscribe after Close returned an open channel")
	}

	// Subsequent Publish is a no-op and does not panic.
	b.Publish("after-close", "data")
}

func TestBrokerSubscribeGoroutineExitsOnClose(t *testing.T) {
	b := NewBroker[string]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use a long-lived context; the goroutine should still exit on Close.
	_ = b.Subscribe(ctx)
	b.Close()

	// Give the goroutine time to exit, then verify no goroutine is still
	// trying to remove from subs by subscribing again (would race otherwise).
	done := make(chan struct{})
	go func() {
		// The previous unsubscribe goroutine should have exited once Close
		// shut down its subscription. Re-subscribing after Close is safe.
		ch := b.Subscribe(ctx)
		if _, ok := <-ch; ok {
			t.Error("Subscribe after Close returned an open channel")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Subscribe after Close did not complete promptly; goroutine leak suspected")
	}
}

func TestBrokerStressParallel(t *testing.T) {
	b := NewBroker[int]()
	const subs = 8
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chans := make([]<-chan Event[int], subs)
	for i := 0; i < subs; i++ {
		chans[i] = b.Subscribe(ctx, WithBufferSize[int](64))
		wg.Add(1)
		go func(ch <-chan Event[int]) {
			defer wg.Done()
			for range ch {
			}
		}(chans[i])
	}
	for i := 0; i < 1000; i++ {
		b.Publish("n", i)
	}
	b.Close()
	wg.Wait()
}

func TestTerminalSubscriptionBlocksUntilConsumed(t *testing.T) {
	b := NewBroker[string](WithBufferSize[string](0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx, WithTerminal[string]())
	// With a 0-buffer, every publish blocks until the consumer drains.
	done := make(chan struct{})
	go func() {
		b.Publish("jobs", "d")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("terminal publish should block on full buffer")
	case <-time.After(50 * time.Millisecond):
	}
	<-ch
	<-done
}

func TestTerminalSubscriptionDoesNotDropOnTimeout(t *testing.T) {
	b := NewBroker[string](WithBufferSize[string](0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx, WithTerminal[string]())

	// Publish 2 events without draining. Terminal sends must not drop
	// after a 500ms publisher-side timeout, so the publisher stays blocked.
	done := make(chan struct{})
	go func() {
		b.Publish("a", "1")
		b.Publish("a", "2")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("terminal publish completed before drain; event was dropped")
	case <-time.After(200 * time.Millisecond):
	}

	// Drain both events; the publisher unblocks only after delivery.
	for i := 0; i < 2; i++ {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("terminal event %d never delivered", i)
		}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher never unblocked after drain")
	}
}

func TestDefaultSubscriptionBuffer(t *testing.T) {
	if defaultSubscriptionBuffer != 16 {
		t.Fatalf("defaultSubscriptionBuffer = %d, want 16", defaultSubscriptionBuffer)
	}
}

// guard against accidental import of strings without use
var _ = strings.Builder{}

// TestRecoverSendOnClosedRePanicsOnRealDefects pins A-06: the send-path guard
// exists for the send-on-closed-channel race only. A bare recover() there
// swallowed genuine defects, turning a nil dereference in delivery into events
// that silently never arrive.
func TestRecoverSendOnClosedRePanicsOnRealDefects(t *testing.T) {
	t.Run("absorbs the close race", func(t *testing.T) {
		func() {
			defer recoverSendOnClosed("test")
			ch := make(chan int)
			close(ch)
			ch <- 1 // panics: send on closed channel
		}()
		// Reaching here means the panic was absorbed, as intended.
	})

	t.Run("re-panics on anything else", func(t *testing.T) {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("unrelated panic was swallowed by the send guard")
			}
			if got, ok := r.(string); !ok || got != "unrelated defect" {
				t.Fatalf("re-panicked with %v, want the original value", r)
			}
		}()
		func() {
			defer recoverSendOnClosed("test")
			panic("unrelated defect")
		}()
	})
}
