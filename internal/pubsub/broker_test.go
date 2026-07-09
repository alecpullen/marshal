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

// guard against accidental import of strings without use
var _ = strings.Builder{}
