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
	"sync"
	"time"
)

// Option configures a subscription.
type Option[T any] func(*subscription[T])

// WithBufferSize sets the per-subscriber buffer for non-terminal events.
// Default is 16. A full buffer drops the oldest non-terminal event and
// continues (drop-head, never block the publisher).
func WithBufferSize[T any](n int) Option[T] {
	return func(s *subscription[T]) {
		if n >= 0 {
			s.buffer = n
		}
	}
}

// WithTerminal marks the subscriber as a must-receive receiver. Every event
// (whether published via Publish or PublishTerminal) is delivered without
// dropping — a slow reader blocks the publisher until the subscriber
// delivers or its context is cancelled (the channel is closed, unblocking
// any blocked send). This is the F19 R1 "must-deliver terminal events"
// rule keyed on the subscriber rather than the event type: types stay
// opaque strings; the subscriber opts into "I can't miss anything".
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

	mu       sync.Mutex
	closed   bool
	inflight int
	buffer   int
	terminal bool
}

func newSubscription[T any](buffer int) *subscription[T] {
	if buffer < 0 {
		buffer = 0
	}
	return &subscription[T]{
		ch:     make(chan Event[T], buffer),
		stopCh: make(chan struct{}),
		buffer: buffer,
	}
}

// sendBlocking is for terminal subscribers: must receive. Blocks until
// delivered, ctx-cancelled, or sub closed.
func sendBlocking[T any](s *subscription[T], ev Event[T]) {
	if !s.enter() {
		return
	}
	defer s.exit()
	defer func() { recover() }()
	select {
	case s.ch <- ev:
	case <-s.stopCh:
	}
}

// sendBestEffort is for non-terminal subscribers: drop on overflow.
func sendBestEffort[T any](s *subscription[T], ev Event[T]) {
	if !s.enter() {
		return
	}
	defer s.exit()
	defer func() { recover() }()
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
func (b *Broker[T]) Subscribe(ctx context.Context, opts ...Option[T]) <-chan Event[T] {
	b.mu.RLock()
	defaults := b.defaultOpts
	b.mu.RUnlock()
	s := newSubscription[T](16)
	for _, opt := range defaults {
		opt(s)
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.buffer != 16 {
		s.ch = make(chan Event[T], s.buffer)
	}

	b.mu.Lock()
	b.subs = append(b.subs, s)
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
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

// Publish broadcasts a non-terminal event. A full subscriber buffer drops
// the oldest event (drop-head) and continues; the publisher never blocks.
// Terminal subscribers must receive their copy.
func (b *Broker[T]) Publish(typ string, payload T) {
	b.publish(typ, payload, false)
}

// PublishTerminal broadcasts an event the publisher has marked critical.
// Delivery semantics are identical to Publish: terminal subscribers must
// receive and block until delivered; non-terminal subscribers receive
// best-effort. Event types are agreed out-of-band in the owning service.
func (b *Broker[T]) PublishTerminal(typ string, payload T) {
	b.publish(typ, payload, true)
}

func (b *Broker[T]) publish(typ string, payload T, terminal bool) {
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
