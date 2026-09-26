package app

import (
	"sync"
	"testing"

	"marshal/internal/watch"
)

func TestWatchResumeHookSetInvokeClear(t *testing.T) {
	var h WatchResumeHook
	h.Invoke(watch.Report{Name: "x"}) // zero-value cell, no fn set — must be a no-op, not a panic
	var mu sync.Mutex
	var got []string
	h.Set(func(r watch.Report) {
		mu.Lock()
		got = append(got, r.Name)
		mu.Unlock()
	})
	h.Invoke(watch.Report{Name: "build"})
	mu.Lock()
	if len(got) != 1 || got[0] != "build" {
		t.Fatalf("invoke got %v", got)
	}
	mu.Unlock()
	h.Set(nil)
	h.Invoke(watch.Report{Name: "again"})
	mu.Lock()
	if len(got) != 1 {
		t.Fatalf("invoke after clear got %v", got)
	}
	mu.Unlock()
}

func TestWatchResumeHookNilReceiverSafe(t *testing.T) {
	var h *WatchResumeHook
	h.Set(nil)               // must not panic
	h.Invoke(watch.Report{}) // must not panic
}
