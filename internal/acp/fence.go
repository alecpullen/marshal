package acp

import (
	"context"
	"sync"
)

// fence lets one forwarder goroutine confirm that it has written the
// updates for every event published before it was asked (P3 R2).
//
// How exact that is depends on the subscription the forwarder drains. The
// turn forwarder's terminal subscription makes Publish return only after
// delivery, so a publish that returned before the ask is already in the
// channel the answer drains. The stack, work and watch forwarders drain a
// buffered subscription, so an answer covers the events buffered when it was
// served; an event whose delivery lands after that snapshot is applied by the
// next loop pass, not by this answer. Those subscriptions are best-effort by
// construction, and a fence does not make them stronger than Publish is.
type fence struct {
	req  chan chan struct{}
	done chan struct{}
	once sync.Once
}

// newFence returns a fence whose forwarder is not yet serving it.
func newFence() *fence {
	return &fence{
		req:  make(chan chan struct{}),
		done: make(chan struct{}),
	}
}

// requests is the channel the forwarder's loop serves. Each request is a
// channel the forwarder closes once it has drained, flushed and answered.
func (f *fence) requests() <-chan chan struct{} { return f.req }

// ask blocks until the forwarder answers, the forwarder closes the fence,
// or ctx ends. Once the request has been handed over, the wait is still
// bounded by both ctx and the fence closing, so a serving loop that
// disappears mid-drain cannot hang the caller.
func (f *fence) ask(ctx context.Context) {
	answer := make(chan struct{})
	select {
	case f.req <- answer:
	case <-f.done:
		return
	case <-ctx.Done():
		return
	}
	select {
	case <-answer:
	case <-f.done:
	case <-ctx.Done():
	}
}

// close marks the forwarder gone: pending and later asks return at once.
// It is idempotent.
func (f *fence) close() {
	f.once.Do(func() { close(f.done) })
}

// fenceSet holds the live fences of one manager's forwarders, by session.
type fenceSet struct {
	mu     sync.Mutex
	fences map[string][]*fence
}

// add registers f for sessionID, keeping the order fences were added in.
func (s *fenceSet) add(sessionID string, f *fence) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fences == nil {
		s.fences = make(map[string][]*fence)
	}
	s.fences[sessionID] = append(s.fences[sessionID], f)
}

// remove drops f from sessionID's fences by identity. An unknown fence or
// session is a no-op.
func (s *fenceSet) remove(sessionID string, f *fence) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.fences[sessionID]
	for i, candidate := range list {
		if candidate == f {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(s.fences, sessionID)
		return
	}
	s.fences[sessionID] = list
}

// wait asks every fence registered for sessionID, one after another, in
// the order they were added. It asks a snapshot so a fence added or removed
// while waiting cannot change the sequence under it.
func (s *fenceSet) wait(ctx context.Context, sessionID string) {
	s.mu.Lock()
	list := make([]*fence, len(s.fences[sessionID]))
	copy(list, s.fences[sessionID])
	s.mu.Unlock()
	for _, f := range list {
		f.ask(ctx)
	}
}
