package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func startWatcher(t *testing.T, root string, runs *int32) (context.CancelFunc, chan error) {
	t.Helper()
	w := NewWatcher(root, 40*time.Millisecond, func(ctx context.Context) error {
		atomic.AddInt32(runs, 1)
		return nil
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(60 * time.Millisecond) // let the watch set up
	return cancel, done
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
	cancel, done := startWatcher(t, root, &runs)
	defer func() { cancel(); <-done }()

	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&runs) >= 1 })
}

func TestWatcherCoalescesBurst(t *testing.T) {
	root := t.TempDir()
	var runs int32
	cancel, done := startWatcher(t, root, &runs)
	defer func() { cancel(); <-done }()

	for i := 0; i < 5; i++ {
		_ = os.WriteFile(filepath.Join(root, "a.go"), []byte{byte(i)}, 0o644)
		time.Sleep(5 * time.Millisecond)
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&runs) >= 1 })
	time.Sleep(150 * time.Millisecond)
	if n := atomic.LoadInt32(&runs); n > 2 {
		t.Fatalf("burst produced %d runs, expected coalescing to ~1", n)
	}
}

func TestWatcherContinuesAfterRunError(t *testing.T) {
	root := t.TempDir()
	var runs int32
	w := NewWatcher(root, 40*time.Millisecond, func(context.Context) error {
		atomic.AddInt32(&runs, 1)
		return errors.New("probe failed")
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(60 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&runs) >= 1 })
	cancel()
	<-done
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
