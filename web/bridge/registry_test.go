package bridge

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/session"
)

// newTestRegistry starts a fake child in "registry" mode and returns
// the wired Registry.
func newTestRegistry(t *testing.T, mode string) (*Registry, *Child) {
	t.Helper()
	c := newTestChild(t, mode)
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(c.Stop)
	r := NewRegistry(c)
	r.PendingTimeout = 300 * time.Millisecond
	return r, c
}

func TestRegistryNew(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	id, err := r.New(ctx, "/tmp/work", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if id != "s-1" {
		t.Fatalf("New returned %q, want s-1", id)
	}
	info, ok := r.Sessions()[id]
	if !ok || info.Cwd != "/tmp/work" {
		t.Fatalf("session not tracked: %+v", r.Sessions())
	}
}

func TestRegistryWrappers(t *testing.T) {
	r, c := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Load(ctx, "/tmp/work", "s-2"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := r.Prompt(ctx, "s-1", "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !r.Sessions()["s-1"].Busy {
		t.Fatal("Prompt did not mark session busy")
	}
	if err := r.Steer(ctx, "s-1", "actually"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if err := r.SetMode(ctx, "s-1", "auto"); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if err := r.Cancel(ctx, "s-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := r.Delete(ctx, "s-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := r.Sessions()["s-1"]; ok {
		t.Fatal("Delete did not untrack session")
	}

	// The fake child logs every request to stderr; confirm the wrapper
	// methods reached it with the right ACP method strings.
	log := c.StderrLog()
	for _, m := range []string{"session/new", "session/load", "session/prompt", "session/steer", "session/set_mode", "session/delete"} {
		if !strings.Contains(log, "helper saw request "+m) {
			t.Errorf("child never saw %s; log:\n%s", m, log)
		}
	}
}

func TestRegistryUnknownSession(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	for _, fn := range map[string]func() error{
		"Prompt":  func() error { return r.Prompt(ctx, "nope", "x") },
		"Cancel":  func() error { return r.Cancel(ctx, "nope") },
		"Steer":   func() error { return r.Steer(ctx, "nope", "x") },
		"SetMode": func() error { return r.SetMode(ctx, "nope", "auto") },
		"Delete":  func() error { return r.Delete(ctx, "nope") },
	} {
		if err := fn(); !errors.Is(err, ErrUnknownSession) {
			t.Errorf("want ErrUnknownSession, got %v", err)
		}
	}
}

func TestRegistryPermissionRoundTrip(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	// Trigger the fake child to emit session/request_permission.
	if err := r.Steer(ctx, "s-1", "x"); err != nil { // any request primes the loop
		t.Fatalf("prime: %v", err)
	}
	if _, err := r.child.Request(ctx, "test/ask_permission", nil); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	// The registry should now have a pending permission for tc-1.
	waitFor(t, 2*time.Second, "pending permission", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return ok
	})
	if err := r.ResolvePermission("tc-1", Decision{Approved: true}); err != nil {
		t.Fatalf("ResolvePermission: %v", err)
	}
	// Duplicate resolve must report ErrGone.
	if err := r.ResolvePermission("tc-1", Decision{Approved: true}); !errors.Is(err, ErrGone) {
		t.Fatalf("duplicate resolve: want ErrGone, got %v", err)
	}
}

func TestRegistryPermissionTimeout(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.child.Request(ctx, "test/ask_permission", nil); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	waitFor(t, 2*time.Second, "pending permission", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return ok
	})
	// No resolve: the 300ms injected timeout must fire and clean up.
	waitFor(t, 2*time.Second, "timeout cleanup", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return !ok
	})
	// A late resolve now reports ErrGone.
	if err := r.ResolvePermission("tc-1", Decision{Approved: true}); !errors.Is(err, ErrGone) {
		t.Fatalf("late resolve: want ErrGone, got %v", err)
	}
}

func TestRegistryQuestionRoundTrip(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.child.Request(ctx, "test/ask_question", nil); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	waitFor(t, 2*time.Second, "pending question", func() bool {
		r.quesMu.Lock()
		defer r.quesMu.Unlock()
		_, ok := r.questions["q-1"]
		return ok
	})
	ans := Answers{Answers: []session.Answer{{Question: "proceed?", Answer: "yes"}}}
	if err := r.ResolveQuestion("q-1", ans); err != nil {
		t.Fatalf("ResolveQuestion: %v", err)
	}
	if err := r.ResolveQuestion("q-1", ans); !errors.Is(err, ErrGone) {
		t.Fatalf("duplicate resolve: want ErrGone, got %v", err)
	}
}

func TestRegistryQuestionTimeout(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.child.Request(ctx, "test/ask_question", nil); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	waitFor(t, 2*time.Second, "pending question", func() bool {
		r.quesMu.Lock()
		defer r.quesMu.Unlock()
		_, ok := r.questions["q-1"]
		return ok
	})
	waitFor(t, 2*time.Second, "timeout cleanup", func() bool {
		r.quesMu.Lock()
		defer r.quesMu.Unlock()
		_, ok := r.questions["q-1"]
		return !ok
	})
	if err := r.ResolveQuestion("q-1", Answers{Declined: true}); !errors.Is(err, ErrGone) {
		t.Fatalf("late resolve: want ErrGone, got %v", err)
	}
}

func TestRegistryRestartResumesSessions(t *testing.T) {
	r, c := newTestRegistry(t, "registry-die")
	ctx, cancel := testContext(t)
	defer cancel()

	var mu sync.Mutex
	var events []string
	r.OnEvent = func(sessionId string, payload any) {
		mu.Lock()
		defer mu.Unlock()
		if m, ok := payload.(map[string]any); ok {
			if typ, _ := m["type"].(string); typ == "bridge_restarted" {
				events = append(events, sessionId)
			}
		}
	}

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	// registry-die mode exits after the first request (the session/new
	// above), so the child has already died; the supervisor respawns it
	// and the OnRestart hook must resume the tracked session.
	waitFor(t, 3*time.Second, "session/resume after restart", func() bool {
		return strings.Contains(c.StderrLog(), "helper saw request session/resume")
	})
	waitFor(t, 2*time.Second, "bridge_restarted event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) == 1 && events[0] == "s-1"
	})
	if r.Sessions()["s-1"].Busy {
		t.Fatal("restart should mark in-flight turn interrupted")
	}
}

func TestRegistryRestartClearsPending(t *testing.T) {
	r, _ := newTestRegistry(t, "registry-die")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	// Wait for the restart (triggered by registry-die exiting after
	// session/new), then trigger a permission request on generation 2.
	waitFor(t, 3*time.Second, "restart", func() bool {
		return strings.Contains(c0Stderr(r), "session/resume")
	})
	if _, err := r.child.Request(ctx, "test/ask_permission", nil); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	waitFor(t, 2*time.Second, "pending permission", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return ok
	})

	// Simulate a second restart: clearPending must drain the map and a
	// late resolve must report ErrGone.
	r.clearPending()
	r.permMu.Lock()
	_, ok := r.permissions["tc-1"]
	r.permMu.Unlock()
	if ok {
		t.Fatal("clearPending left stale permission entry")
	}
	if err := r.ResolvePermission("tc-1", Decision{Approved: true}); !errors.Is(err, ErrGone) {
		t.Fatalf("resolve after clear: want ErrGone, got %v", err)
	}
}

// c0Stderr reaches the child through the registry for stderr asserts.
func c0Stderr(r *Registry) string { return r.child.StderrLog() }
