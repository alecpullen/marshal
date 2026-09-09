package acp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"marshal/internal/app"
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

// TestChainedCancellerReachesOrphanedTurns pins follow-ups doc item #5:
// the manager's turn canceller used to be overwritten by each
// connection, so a turn that outlived its connection became
// uncancellable through the manager. The host-level canceller is now
// registered once in newAgentHost and fans out to every connection
// canceller, so registering a second connection must not strand the
// first's active turn.
func TestChainedCancellerReachesOrphanedTurns(t *testing.T) {
	h, err := newAgentHost(runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return nil, errors.New("no runtime in tests")
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("newAgentHost: %v", err)
	}

	var mu sync.Mutex
	cancelled := map[string]bool{}

	// Connection 1 registers a canceller with an "active turn" for s1.
	h.addTurnCanceller(func(ctx context.Context, sessionID string) error {
		mu.Lock()
		cancelled[sessionID] = true
		mu.Unlock()
		return nil
	})

	// Connection 2 attaches: with the old overwrite design this
	// replaced the manager's canceller entirely, stranding s1.
	h.addTurnCanceller(func(ctx context.Context, sessionID string) error {
		return nil
	})

	// The manager's canceller must still reach connection 1's entry
	// even though connection 2 has since registered.
	if err := h.chainedCancel(context.Background(), "s1"); err != nil {
		t.Fatalf("chainedCancel: %v", err)
	}
	mu.Lock()
	got := cancelled["s1"]
	mu.Unlock()
	if !got {
		t.Fatal("second connection's registration stranded the first connection's turn: chainedCancel never reached it")
	}
}
