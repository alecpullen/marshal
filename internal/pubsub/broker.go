// Package pubsub is an in-process typed event broker (F19). Subscribers
// get bounded per-subscriber buffers; a slow subscriber drops non-terminal
// events rather than blocking publishers, but terminal subscribers (those
// opting in via WithTerminal) must receive every event — the publisher
// blocks until delivered or the subscriber's context is cancelled (crush's
// "must-deliver" rule, keyed on the subscriber rather than the event type).
//
// Concurrent close(ch)+chansend races on the runtime's internal channel
// state are avoided by an in-flight tracking mutex on each subscription:
// send goroutines register themselves under the mutex before touching ch
// and unregister after; the close path waits until in-flight == 0 before
// calling close(ch) (single-shot via sync.Once).
//
// This is a clean-room implementation of the public behavior described in
// docs/12 F19; it does not derive from crush's FSL-licensed source.
package pubsub

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// defaultSubscriptionBuffer is the per-subscriber buffer size for
// non-terminal events when WithBufferSize is not specified.
const defaultSubscriptionBuffer = 16

// Option configures a subscription.
type Option[T any] func(*subscription[T])

// WithBufferSize sets the per-subscriber buffer for non-terminal events.
// Default is defaultSubscriptionBuffer. A full buffer drops the oldest
// non-terminal event and continues (drop-head, never block the publisher).
func WithBufferSize[T any](n int) Option[T] {
	return func(s *subscription[T]) {
		if n >= 0 {
			s.buffer = n
		}
	}
}

// WithTerminal marks the subscriber as a must-receive receiver. Every
// event published to this subscriber is delivered without dropping — a
// slow reader blocks the publisher until the subscriber delivers or its
// context is cancelled (the channel is closed, unblocking any blocked
// send). The must-deliver rule is keyed on the subscriber rather than the
// event type: types stay opaque strings; the subscriber opts into "I
// can't miss anything", and any Publish to a terminal subscriber uses
// bounded-blocking semantics regardless of how the publisher was called.
func WithTerminal[T any]() Option[T] {
	return func(s *subscription[T]) {
		s.terminal = true
	}
}

// subscription’s lifetime is gated by an inflight tracking mutex. Every
// send registers under the mutex before calling chansend and unregisters
// after; the close path waits until in-flight == 0 before closeOnce.Do
// closes the channel. This guarantees no concurrent chansend/close and
// keeps the race detector quiet.
type subscription[T any] struct {
	ch        chan Event[T]
	stopCh    chan struct{}
	closeOnce sync.Once
	ctx       context.Context

	mu       sync.Mutex
	closed   bool
	inflight int
	buffer   int
	terminal bool
}

func newSubscription[T any](ctx context.Context, buffer int) *subscription[T] {
	if buffer < 0 {
		buffer = 0
	}
	return &subscription[T]{
		ch:     make(chan Event[T], buffer),
		stopCh: make(chan struct{}),
		ctx:    ctx,
		buffer: buffer,
	}
}

// sendBlocking is for terminal subscribers: must receive. Blocks until
// delivered, ctx-cancelled, or sub closed. Terminal events are never
// dropped by a publisher-side timeout.
func sendBlocking[T any](s *subscription[T], ev Event[T]) {
	if !s.enter() {
		return
	}
	defer s.exit()
	defer recoverSendOnClosed(ev.Type)
	select {
	case s.ch <- ev:
	case <-s.stopCh:
	case <-s.ctx.Done():
	}
}

// recoverSendOnClosed absorbs the send-on-closed-channel panic that the
// enter/exit/closeOnce protocol deliberately races with, and re-panics on
// anything else.
//
// This used to be a bare `defer func() { recover() }()`, which discarded every
// panic in the send path — a nil dereference in delivery would surface only as
// events silently not arriving, with no log line and no crash.
func recoverSendOnClosed(topic string) {
	r := recover()
	if r == nil {
		return
	}
	// Go does not provide a distinct typed panic for "send on closed
	// channel". The runtime panics with a runtime.plainError, which does
	// implement error, but that type is shared across many runtime panics
	// (e.g. "index out of range"), so we cannot distinguish this case by
	// type assertion or errors.As alone. String matching against the
	// panic message is the only option available without parsing the
	// panic stack. This is a known Go limitation; see
	// https://go.dev/ref/spec#Handling_panics.
	if err, ok := r.(error); ok && strings.Contains(err.Error(), "send on closed channel") {
		slog.Default().Debug("publish raced subscription close; event dropped", "topic", topic)
		return
	}
	// Not the race this guard exists for — a real defect. Let it surface.
	panic(r)
}

// sendBestEffort is for non-terminal subscribers: drop on overflow.
//
// The drop-head path has a benign TOCTOU race: between checking the
// channel length (via the non-blocking receive in the second select)
// and the retry send, another goroutine may add or remove an event.
// This means we might drop one extra event or keep one extra event
// under concurrent access. This is acceptable for non-terminal
// events — the contract is best-effort delivery, not exact ordering —
// and the alternative (holding a lock across the drop+send) would
// serialize all publishers for a single slow subscriber.
func sendBestEffort[T any](s *subscription[T], ev Event[T]) {
	if !s.enter() {
		return
	}
	defer s.exit()
	defer recoverSendOnClosed(ev.Type)
	// Try non-blocking first.
	select {
	case s.ch <- ev:
		return
	case <-s.stopCh:
		return
	default:
	}
	// Buffer full: drop-head one slot, then retry once.
	select {
	case <-s.ch:
	case <-s.stopCh:
		return
	default:
	}
	select {
	case s.ch <- ev:
	case <-s.stopCh:
	default:
	}
}

// enter registers this send as in-flight under the lifetime mutex. Returns
// false if the sub is already closed (caller should bail).
func (s *subscription[T]) enter() bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.inflight++
	s.mu.Unlock()
	return true
}

// exit unregisters the in-flight send; if the sub was closed and we are the
// last in-flight send, perform the channel close (idempotent via closeOnce).
func (s *subscription[T]) exit() {
	s.mu.Lock()
	s.inflight--
	mayClose := s.closed && s.inflight == 0
	s.mu.Unlock()
	if mayClose {
		s.closeOnce.Do(func() { close(s.ch) })
	}
}

// shutdown signals lifetime end. Idempotent. Closes the user-facing
// channel via closeOnce after the last in-flight send exits. Send
// goroutines either complete (possible send on open ch) or panic from a
// closed-channel send (recovered). Because close happens AFTER all
// in-flight sends return, there is no concurrent chansend/close.
func (s *subscription[T]) shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.stopCh)
	mayClose := s.inflight == 0
	s.mu.Unlock()
	if mayClose {
		s.closeOnce.Do(func() { close(s.ch) })
	}
}

// Broker is a generic typed pub/sub broker. The zero value is not usable;
// use NewBroker. Options passed to NewBroker apply as defaults for any
// subscription that does not override them via Subscribe's options.
type Broker[T any] struct {
	mu          sync.RWMutex
	subs        []*subscription[T]
	closed      bool
	defaultOpts []Option[T]
}

// NewBroker creates a broker with optional default subscription options.
// Subscribe before publishing.
func NewBroker[T any](opts ...Option[T]) *Broker[T] {
	return &Broker[T]{defaultOpts: opts}
}

// Subscribe returns a receive channel. When ctx is cancelled the channel
// is closed and the subscription is removed. A terminal subscriber also
// unblocks any pending Publish on ctx cancellation.
//
// Subscribe after Close returns an already-closed channel and starts no
// goroutine, so callers ranging over the channel terminate immediately.
func (b *Broker[T]) Subscribe(ctx context.Context, opts ...Option[T]) <-chan Event[T] {
	b.mu.RLock()
	defaults := b.defaultOpts
	b.mu.RUnlock()
	s := newSubscription[T](ctx, defaultSubscriptionBuffer)
	for _, opt := range defaults {
		opt(s)
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.buffer != defaultSubscriptionBuffer {
		s.ch = make(chan Event[T], s.buffer)
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		s.shutdown()
		return s.ch
	}
	b.subs = append(b.subs, s)
	b.mu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
		case <-s.stopCh:
			// Broker closed or subscription already shut down; nothing to do.
			return
		}
		b.mu.Lock()
		for i, sub := range b.subs {
			if sub == s {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		s.shutdown()
	}()
	return s.ch
}

// Publish broadcasts an event to every current subscriber. Delivery
// semantics are determined entirely by the subscriber's subscription
// options, not by how the publisher invokes this method:
//
//   - Subscribers registered without WithTerminal receive events on a
//     best-effort basis: a full subscriber buffer drops the oldest event
//     (drop-head) and the publisher never blocks.
//   - Subscribers registered with WithTerminal receive events with
//     must-deliver (bounded-blocking) semantics: every event is delivered
//     without dropping, and a slow reader blocks the publisher until
//     either the subscriber delivers the event or its context is
//     cancelled (which closes the channel and unblocks the send).
//
// In other words, WithTerminal opts a subscriber into must-receive
// regardless of the publish call site; there is no separate "publish a
// terminal event" method.
func (b *Broker[T]) Publish(typ string, payload T) {
	b.publish(typ, payload)
}

func (b *Broker[T]) publish(typ string, payload T) {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	ev := Event[T]{Type: typ, Payload: payload, PublishedAt: time.Now()}
	subs := append([]*subscription[T](nil), b.subs...)
	b.mu.RUnlock()

	for _, s := range subs {
		if s.terminal {
			sendBlocking(s, ev)
			continue
		}
		sendBestEffort(s, ev)
	}
}

// Close shuts down the broker: all subscriber channels are closed and
// subsequent Publish calls are no-ops. Idempotent with ctx cancellation.
func (b *Broker[T]) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := b.subs
	b.subs = nil
	b.mu.Unlock()
	for _, s := range subs {
		s.shutdown()
	}
}
