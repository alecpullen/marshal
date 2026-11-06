package acp

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fenceTestTimeout bounds every wait in this file so a broken fence fails
// the test instead of hanging the suite.
const fenceTestTimeout = 5 * time.Second

// fenceTestServer records the order in which fences were served. Each
// received request is appended to order and then answered, so a caller that
// blocks on ask observes the service before ask returns.
type fenceTestServer struct {
	mu    sync.Mutex
	order []string
	stop  chan struct{}
	wg    sync.WaitGroup
}

// fenceTestServe starts a forwarder loop for f that records name when it
// serves a request. The returned func stops the loop.
func fenceTestServe(f *fence, name string, rec *fenceTestServer) func() {
	rec.mu.Lock()
	if rec.stop == nil {
		rec.stop = make(chan struct{})
	}
	stop := rec.stop
	rec.mu.Unlock()
	rec.wg.Add(1)
	go func() {
		defer rec.wg.Done()
		for {
			select {
			case req := <-f.requests():
				rec.mu.Lock()
				rec.order = append(rec.order, name)
				rec.mu.Unlock()
				close(req)
			case <-stop:
				return
			}
		}
	}()
	return func() {
		rec.mu.Lock()
		if rec.stop != nil {
			close(rec.stop)
			rec.stop = nil
		}
		rec.mu.Unlock()
		rec.wg.Wait()
	}
}

// recorded returns a copy of the names served so far.
func (rec *fenceTestServer) recorded() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]string, len(rec.order))
	copy(out, rec.order)
	return out
}

func (rec *fenceTestServer) reset() {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.order = nil
}

// TestAFenceWaitsForItsForwarder checks that ask returns only once the
// forwarder has served the request, not when the request is handed over.
func TestAFenceWaitsForItsForwarder(t *testing.T) {
	t.Parallel()
	f := newFence()

	served := make(chan struct{})
	gate := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case req := <-f.requests():
				close(served)
				<-gate
				close(req)
			case <-stop:
				return
			}
		}
	}()

	asked := make(chan struct{})
	go func() {
		f.ask(context.Background())
		close(asked)
	}()

	select {
	case <-served:
	case <-time.After(fenceTestTimeout):
		t.Fatal("forwarder never received the request")
	}
	select {
	case <-asked:
		t.Fatal("ask returned before the forwarder answered")
	default:
	}

	close(gate)
	select {
	case <-asked:
	case <-time.After(fenceTestTimeout):
		t.Fatal("ask did not return after the forwarder answered")
	}
}

// TestAFenceReturnsWhenItsForwarderIsGone checks that a closed fence makes
// pending and later asks return, and that close is idempotent.
func TestAFenceReturnsWhenItsForwarderIsGone(t *testing.T) {
	t.Parallel()

	closed := newFence()
	closed.close()
	ctx, cancel := context.WithTimeout(context.Background(), fenceTestTimeout)
	defer cancel()
	asked := make(chan struct{})
	go func() {
		closed.ask(ctx)
		close(asked)
	}()
	select {
	case <-asked:
	case <-time.After(fenceTestTimeout):
		t.Fatal("ask on a closed fence did not return")
	}
	closed.close() // must not panic

	unserved := newFence()
	blocked := make(chan struct{})
	go func() {
		unserved.ask(context.Background())
		close(blocked)
	}()
	// Give the ask a chance to block on the unserved requests channel; the
	// later close must release it either way.
	select {
	case <-blocked:
		t.Fatal("ask returned while nobody had served or closed the fence")
	default:
	}
	unserved.close()
	select {
	case <-blocked:
	case <-time.After(fenceTestTimeout):
		t.Fatal("ask did not return when the fence was closed")
	}
	unserved.close() // must not panic
}

// TestAFenceReturnsWhenTheContextEnds checks that a fence nobody serves
// releases its asker when the context ends.
func TestAFenceReturnsWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	f := newFence()
	ctx, cancel := context.WithCancel(context.Background())

	asked := make(chan struct{})
	go func() {
		f.ask(ctx)
		close(asked)
	}()
	select {
	case <-asked:
		t.Fatal("ask returned before the context ended")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-asked:
	case <-time.After(fenceTestTimeout):
		t.Fatal("ask did not return when the context ended")
	}
}

// TestAFenceSetAsksEveryFenceInOrder checks that wait serves a session's
// fences in the order they were added, ignores other sessions, and stops
// asking a fence once it is removed.
func TestAFenceSetAsksEveryFenceInOrder(t *testing.T) {
	t.Parallel()
	rec := &fenceTestServer{}
	first := newFence()
	second := newFence()
	other := newFence()
	stopFirst := fenceTestServe(first, "first", rec)
	defer stopFirst()
	stopSecond := fenceTestServe(second, "second", rec)
	defer stopSecond()
	stopOther := fenceTestServe(other, "other", rec)
	defer stopOther()

	var s fenceSet
	s.add("session-1", first)
	s.add("session-1", second)
	s.add("session-2", other)

	ctx, cancel := context.WithTimeout(context.Background(), fenceTestTimeout)
	defer cancel()
	s.wait(ctx, "session-1")

	if got := rec.recorded(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("wait served %v, want [first second]", got)
	}

	// Unknown fences and sessions remove nothing and must not panic.
	s.remove("session-1", newFence())
	s.remove("no-such-session", other)

	rec.reset()
	s.remove("session-1", first)
	s.wait(ctx, "session-1")
	if got := rec.recorded(); len(got) != 1 || got[0] != "second" {
		t.Fatalf("after remove wait served %v, want [second]", got)
	}

	rec.reset()
	s.remove("session-1", second)
	if _, ok := s.fences["session-1"]; ok {
		t.Fatal("removing the last fence left an empty slice behind")
	}
	s.wait(ctx, "session-1")
	if got := rec.recorded(); len(got) != 0 {
		t.Fatalf("wait on a session with no fences served %v, want none", got)
	}
}
