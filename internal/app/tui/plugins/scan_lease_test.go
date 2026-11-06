package plugins

import (
	"sync"
	"testing"
)

// discardRecorder records every token a lease released.
type discardRecorder struct {
	mu     sync.Mutex
	tokens []string
}

func (r *discardRecorder) discard(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = append(r.tokens, token)
}

func (r *discardRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.tokens...)
}

func (r *discardRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tokens)
}

// TestScanLeaseClosePublishClaimRaces pins the lease's ownership rules against
// every order the panel can produce: a close that lands before the worker
// publishes, a close after it published, a claim followed by a close, and a
// worker that fails after publishing. Whatever the order, each token is either
// claimed once or discarded once, and never both.
func TestScanLeaseClosePublishClaimRaces(t *testing.T) {
	t.Run("close cancels before the worker publishes", func(t *testing.T) {
		rec := &discardRecorder{}
		cancelled := make(chan struct{})
		l := newScanLease(func() { close(cancelled) }, rec.discard)
		l.Close()
		select {
		case <-cancelled:
		default:
			t.Fatal("close did not cancel the scan")
		}

		if l.Publish("scan-1") {
			t.Fatal("a closed lease accepted a token it will never release")
		}
		if got := rec.snapshot(); len(got) != 1 || got[0] != "scan-1" {
			t.Fatalf("discarded = %v, want [scan-1]", got)
		}

		l.Close() // repeated close is inert
		if rec.count() != 1 {
			t.Fatalf("repeated close discarded %d tokens, want 1", rec.count())
		}
	})

	t.Run("publish then close discards the unclaimed token", func(t *testing.T) {
		rec := &discardRecorder{}
		l := newScanLease(nil, rec.discard)
		if !l.Publish("scan-1") {
			t.Fatal("an open lease refused a token")
		}
		l.Close()
		l.Close()
		if got := rec.snapshot(); len(got) != 1 || got[0] != "scan-1" {
			t.Fatalf("discarded = %v, want [scan-1]", got)
		}
	})

	t.Run("claim before close keeps the token", func(t *testing.T) {
		rec := &discardRecorder{}
		l := newScanLease(nil, rec.discard)
		if !l.Publish("scan-1") {
			t.Fatal("an open lease refused a token")
		}
		got, ok := l.Claim()
		if !ok || got != "scan-1" {
			t.Fatalf("Claim() = %q, %v; want %q, true", got, ok, "scan-1")
		}
		if _, second := l.Claim(); second {
			t.Fatal("a lease was claimed twice")
		}
		l.Close()
		// The claimant owns the staged scan now: closing the lease must not
		// discard a token it no longer controls.
		if rec.count() != 0 {
			t.Fatalf("close after a claim discarded %v", rec.snapshot())
		}
	})

	t.Run("failed scan publishes then closes", func(t *testing.T) {
		rec := &discardRecorder{}
		l := newScanLease(nil, rec.discard)
		if !l.Publish("scan-1") {
			t.Fatal("an open lease refused a token")
		}
		l.Close() // the worker's error path
		if got := rec.snapshot(); len(got) != 1 || got[0] != "scan-1" {
			t.Fatalf("discarded = %v, want [scan-1]", got)
		}
	})

	t.Run("claim of an unpublished lease fails", func(t *testing.T) {
		l := newScanLease(nil, nil)
		defer l.Close()
		if token, ok := l.Claim(); ok {
			t.Fatalf("Claim() on an unpublished lease returned %q, true", token)
		}
	})

	// A publish that races a close from another goroutine is sequenced through
	// channels, and both legal orders must end with the token discarded once.
	t.Run("publish racing a close leaks nothing", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			rec := &discardRecorder{}
			l := newScanLease(nil, rec.discard)
			published := make(chan bool, 1)
			start := make(chan struct{})
			go func() {
				<-start
				published <- l.Publish("scan-1")
			}()
			close(start)
			l.Close()
			<-published
			if rec.count() != 1 {
				t.Fatalf("run %d: discarded %v, want exactly one discard", i, rec.snapshot())
			}
		}
	})

	// A claim that races a close must be consistent: a successful claim keeps
	// the token, a failed one means the close discarded it.
	t.Run("claim racing a close stays consistent", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			rec := &discardRecorder{}
			l := newScanLease(nil, rec.discard)
			if !l.Publish("scan-1") {
				t.Fatal("an open lease refused a token")
			}
			done := make(chan struct{})
			var (
				claimed bool
				token   string
			)
			go func() {
				token, claimed = l.Claim()
				close(done)
			}()
			l.Close()
			<-done
			if claimed {
				if token != "scan-1" {
					t.Fatalf("Claim() = %q, want %q", token, "scan-1")
				}
				if rec.count() != 0 {
					t.Fatalf("run %d: a claimed token was also discarded: %v", i, rec.snapshot())
				}
				continue
			}
			if got := rec.snapshot(); len(got) != 1 || got[0] != "scan-1" {
				t.Fatalf("run %d: discarded = %v, want [scan-1]", i, got)
			}
		}
	})
}
