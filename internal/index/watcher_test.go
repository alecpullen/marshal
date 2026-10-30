package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func startWatcher(t *testing.T, root string, runs *int32) func() {
	t.Helper()
	w := NewWatcher(root, 40*time.Millisecond, func(ctx context.Context) error {
		atomic.AddInt32(runs, 1)
		return nil
	}, nil)
	stop, _ := startWatcherWith(t, w)
	return stop
}

// startWatcherWith starts w and returns only once the watcher has installed its
// watch set and is about to enter its event loop. The ready hook is the
// production seam the watcher calls at exactly that point, so nothing sleeps
// waiting for a watch to appear.
//
// The returned stop cancels the watcher and joins Run; it is idempotent and
// also registered as a cleanup, so an early t.Fatal cannot leak the goroutine.
// exited is closed when Run returns, which lets a test prove the watcher was
// still alive at a given moment.
func startWatcherWith(t *testing.T, w *Watcher) (stop func(), exited <-chan struct{}) {
	t.Helper()
	ready := make(chan struct{})
	w.ready = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	exitedCh := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
		close(exitedCh)
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("watcher did not install its watch set before the deadline")
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(stop)
	return stop, exitedCh
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

func TestWatcherTriggersOnChange(t *testing.T) {
	root := t.TempDir()
	var runs int32
	stop := startWatcher(t, root, &runs)
	defer stop()

	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&runs) >= 1 })
}

func TestWatcherCoalescesBurst(t *testing.T) {
	root := t.TempDir()
	var runs int32
	stop := startWatcher(t, root, &runs)
	defer stop()

	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(root, "a.go"), []byte{byte(i)}, 0o644); err != nil {
			t.Fatalf("write burst file %d: %v", i, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&runs) >= 1 })
	time.Sleep(150 * time.Millisecond)
	if n := atomic.LoadInt32(&runs); n > 2 {
		t.Fatalf("burst produced %d runs, expected coalescing to ~1", n)
	}
}

// TestWatcherContinuesAfterRunError proves a failed indexing pass does not end
// the watch: a second callback must arrive for a second file, after the first
// callback returned an error.
func TestWatcherContinuesAfterRunError(t *testing.T) {
	root := t.TempDir()
	invocations := make(chan int, 4)
	var runs int32
	w := NewWatcher(root, 40*time.Millisecond, func(context.Context) error {
		invocations <- int(atomic.AddInt32(&runs, 1))
		return errors.New("probe failed")
	}, nil)
	stop, exited := startWatcherWith(t, w)
	defer stop()

	waitInvocation := func(want int) {
		t.Helper()
		select {
		case got := <-invocations:
			if got != want {
				t.Fatalf("callback invocation %d, want %d", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("watcher never ran callback invocation %d", want)
		}
	}

	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitInvocation(1)

	// The first pass failed; the watcher must still be in its event loop.
	select {
	case <-exited:
		t.Fatal("watcher exited after the first callback returned an error")
	default:
	}

	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitInvocation(2)

	stop()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not terminate after cancellation")
	}
}

func TestWatcherStopsOnCancel(t *testing.T) {
	root := t.TempDir()
	var runs int32
	w := NewWatcher(root, 40*time.Millisecond, func(context.Context) error { atomic.AddInt32(&runs, 1); return nil }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}
