package acp

import (
	"sync"
	"testing"
)

func TestNotifySinkDropsWhenDetached(t *testing.T) {
	var s notifySink
	if err := s.Notify("session/update", map[string]any{"a": 1}); err != nil {
		t.Fatalf("Notify while detached = %v, want nil (dropped)", err)
	}
}

func TestNotifySinkForwardsToAttached(t *testing.T) {
	var s notifySink
	var mu sync.Mutex
	var got []string
	s.Set(func(method string, params any) error {
		mu.Lock()
		got = append(got, method)
		mu.Unlock()
		return nil
	})
	if err := s.Notify("session/update", nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	s.Clear()
	if err := s.Notify("session/update", nil); err != nil {
		t.Fatalf("Notify after Clear = %v, want nil", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "session/update" {
		t.Fatalf("got %v, want exactly one session/update", got)
	}
}
