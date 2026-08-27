package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
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
	return r, c
}

func TestRegistryPendingReportsParkedKind(t *testing.T) {
	r := NewRegistry(&Child{})
	r.track("s1", "/home/u/repo")
	if got := r.Pending("s1"); got != "" {
		t.Fatalf("Pending on idle session = %q, want empty", got)
	}
	r.permMu.Lock()
	r.permissions["tc1"] = make(chan Decision, 1)
	r.permSession["tc1"] = "s1"
	r.permMu.Unlock()
	if got := r.Pending("s1"); got != "approval" {
		t.Fatalf("Pending with pending permission = %q", got)
	}
	r.permMu.Lock()
	delete(r.permissions, "tc1")
	delete(r.permSession, "tc1")
	r.permMu.Unlock()
	r.quesMu.Lock()
	r.questions["q1"] = make(chan Answers, 1)
	r.quesSession["q1"] = "s1"
	r.quesMu.Unlock()
	if got := r.Pending("s1"); got != "question" {
		t.Fatalf("Pending with pending question = %q", got)
	}
	if got := r.Pending("other"); got != "" {
		t.Fatalf("Pending for unrelated session = %q", got)
	}
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
	// session/prompt blocks until the turn completes (the fake child
	// holds its response for 200ms), so run it on a goroutine and
	// observe Busy mid-turn, then cleared once the turn ends.
	promptErr := make(chan error, 1)
	go func() { promptErr <- r.Prompt(ctx, "s-1", "hello") }()
	waitFor(t, 2*time.Second, "session busy during turn", func() bool {
		return r.Sessions()["s-1"].Busy
	})
	if err := <-promptErr; err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if r.Sessions()["s-1"].Busy {
		t.Fatal("session still busy after turn ended")
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
	//
	// Poll rather than reading once: the child writes each line to stderr
	// before it responds, but the parent drains stderr on a separate
	// goroutine, so a received response does NOT imply the matching line
	// has been drained yet. Reading once made this test fail whenever the
	// package ran enough concurrent children to delay the drain.
	want := []string{"session/new", "session/load", "session/prompt", "session/steer", "session/set_mode", "session/delete"}
	waitFor(t, 2*time.Second, "every wrapper method in the child's stderr log", func() bool {
		log := c.StderrLog()
		for _, m := range want {
			if !strings.Contains(log, "helper saw request "+m) {
				return false
			}
		}
		return true
	})
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
	// Trigger the fake child to emit session/request_permission. Use a
	// long context for the trigger because the helper now forwards the
	// bridge's response back as the trigger result.
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel()
	if err := r.Steer(ctx, "s-1", "x"); err != nil { // any request primes the loop
		t.Fatalf("prime: %v", err)
	}
	triggerErr := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx, "test/ask_permission", nil)
		triggerErr <- err
	}()
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
	// The trigger request should complete successfully once resolved.
	select {
	case err := <-triggerErr:
		if err != nil {
			t.Fatalf("trigger returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("trigger did not return after resolve")
	}
	// Duplicate resolve must report ErrGone.
	if err := r.ResolvePermission("tc-1", Decision{Approved: true}); !errors.Is(err, ErrGone) {
		t.Fatalf("duplicate resolve: want ErrGone, got %v", err)
	}
}

func TestRegistryPermissionWaitsForDecision(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel()
	triggerErr := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx, "test/ask_permission", nil)
		triggerErr <- err
	}()
	waitFor(t, 2*time.Second, "pending permission", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return ok
	})
	// The pending permission must survive short sleeps; there is no
	// wall-clock bound, the wait ends on a decision or session
	// cancellation.
	time.Sleep(200 * time.Millisecond)
	r.permMu.Lock()
	_, stillPending := r.permissions["tc-1"]
	r.permMu.Unlock()
	if !stillPending {
		t.Fatal("pending permission was dropped without a decision")
	}
	if err := r.ResolvePermission("tc-1", Decision{Approved: true}); err != nil {
		t.Fatalf("ResolvePermission: %v", err)
	}
	select {
	case err := <-triggerErr:
		if err != nil {
			t.Fatalf("trigger returned error after resolve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("trigger did not return after resolve")
	}
	// A late resolve now reports ErrGone.
	if err := r.ResolvePermission("tc-1", Decision{Approved: true}); !errors.Is(err, ErrGone) {
		t.Fatalf("late resolve: want ErrGone, got %v", err)
	}
}

func TestRegistryPermissionWaitsForeverWhenAbandoned(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}

	// Trigger the permission on a goroutine with a long context so the
	// test can observe that the bridge waits forever (no wall-clock
	// timeout denial) until the session is cancelled.
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer triggerCancel()
	respResult := make(chan json.RawMessage, 1)
	respErr := make(chan error, 1)
	go func() {
		res, err := r.child.Request(triggerCtx, "test/ask_permission", nil)
		if err != nil {
			respErr <- err
			return
		}
		respResult <- res
	}()
	waitFor(t, 2*time.Second, "pending permission", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return ok
	})

	// Abandon the request: do not resolve it and do not cancel the
	// session. With no wall-clock bound, the pending entry must still be
	// present after a short sleep.
	time.Sleep(300 * time.Millisecond)
	r.permMu.Lock()
	_, stillPending := r.permissions["tc-1"]
	r.permMu.Unlock()
	if !stillPending {
		t.Fatal("pending permission was dropped without a decision")
	}

	// Cancelling the issuing session must drain the pending permission.
	if err := r.Cancel(ctx, "s-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitFor(t, 2*time.Second, "permission drained on cancel", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return !ok
	})
	// The child should have received the denied result and returned from
	// its trigger request (the helper echoes the bridge's response).
	select {
	case <-respResult:
		// The trigger's own result is empty; the bridge's denial is sent
		// to the child-initiated request id=9000. The important
		// regression check is that the pending entry was removed and the
		// child request completed.
	case err := <-respErr:
		t.Fatalf("trigger request returned error instead of denied result: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("child did not receive denied result after cancel")
	}
}

func TestRegistryQuestionRoundTrip(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	// Use a long context for the trigger because the helper now forwards
	// the bridge's response back as the trigger result.
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel()
	triggerErr := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx, "test/ask_question", nil)
		triggerErr <- err
	}()
	waitFor(t, 2*time.Second, "pending question", func() bool {
		r.quesMu.Lock()
		defer r.quesMu.Unlock()
		_, ok := r.questions["q-1"]
		return ok
	})
	ans := Answers{Answers: []Answer{{Question: "proceed?", Answer: "yes"}}}
	if err := r.ResolveQuestion("q-1", ans); err != nil {
		t.Fatalf("ResolveQuestion: %v", err)
	}
	// The trigger request should complete successfully once resolved.
	select {
	case err := <-triggerErr:
		if err != nil {
			t.Fatalf("trigger returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("trigger did not return after resolve")
	}
	if err := r.ResolveQuestion("q-1", ans); !errors.Is(err, ErrGone) {
		t.Fatalf("duplicate resolve: want ErrGone, got %v", err)
	}
}

func TestRegistryQuestionWaitsForAnswer(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel()
	triggerErr := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx, "test/ask_question", nil)
		triggerErr <- err
	}()
	waitFor(t, 2*time.Second, "pending question", func() bool {
		r.quesMu.Lock()
		defer r.quesMu.Unlock()
		_, ok := r.questions["q-1"]
		return ok
	})
	// The pending question must survive short sleeps; there is no
	// wall-clock bound, the wait ends on an answer or session
	// cancellation.
	time.Sleep(200 * time.Millisecond)
	r.quesMu.Lock()
	_, stillPending := r.questions["q-1"]
	r.quesMu.Unlock()
	if !stillPending {
		t.Fatal("pending question was dropped without an answer")
	}
	ans := Answers{Answers: []Answer{{Question: "proceed?", Answer: "yes"}}}
	if err := r.ResolveQuestion("q-1", ans); err != nil {
		t.Fatalf("ResolveQuestion: %v", err)
	}
	select {
	case err := <-triggerErr:
		if err != nil {
			t.Fatalf("trigger returned error after resolve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("trigger did not return after resolve")
	}
	if err := r.ResolveQuestion("q-1", ans); !errors.Is(err, ErrGone) {
		t.Fatalf("late resolve: want ErrGone, got %v", err)
	}
}

func TestRegistryQuestionWaitsForeverWhenAbandoned(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}

	// Trigger the question on a goroutine with a long context so the
	// test can observe that the bridge waits forever (no wall-clock
	// timeout decline) until the session is cancelled.
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer triggerCancel()
	respResult := make(chan json.RawMessage, 1)
	respErr := make(chan error, 1)
	go func() {
		res, err := r.child.Request(triggerCtx, "test/ask_question", nil)
		if err != nil {
			respErr <- err
			return
		}
		respResult <- res
	}()
	waitFor(t, 2*time.Second, "pending question", func() bool {
		r.quesMu.Lock()
		defer r.quesMu.Unlock()
		_, ok := r.questions["q-1"]
		return ok
	})

	// Abandon the request: do not answer it and do not cancel the
	// session. With no wall-clock bound, the pending entry must still be
	// present after a short sleep.
	time.Sleep(300 * time.Millisecond)
	r.quesMu.Lock()
	_, stillPending := r.questions["q-1"]
	r.quesMu.Unlock()
	if !stillPending {
		t.Fatal("pending question was dropped without an answer")
	}

	// Cancelling the issuing session must drain the pending question.
	if err := r.Cancel(ctx, "s-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitFor(t, 2*time.Second, "question drained on cancel", func() bool {
		r.quesMu.Lock()
		defer r.quesMu.Unlock()
		_, ok := r.questions["q-1"]
		return !ok
	})
	// The child should have received the declined result and returned from
	// its trigger request (the helper echoes the bridge's response).
	select {
	case <-respResult:
		// The trigger's own result is empty; the bridge's decline is sent
		// to the child-initiated request id=9000. The important
		// regression check is that the pending entry was removed and the
		// child request completed.
	case err := <-respErr:
		t.Fatalf("trigger request returned error instead of declined result: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("child did not receive declined result after cancel")
	}
}

func TestRegistryCancelDrainsPendingPermission(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel()
	triggerErr := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx, "test/ask_permission", nil)
		triggerErr <- err
	}()
	waitFor(t, 2*time.Second, "pending permission", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return ok
	})

	// Cancelling the issuing session must drain the pending permission.
	if err := r.Cancel(ctx, "s-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitFor(t, 2*time.Second, "permission drained on cancel", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return !ok
	})
	if err := r.ResolvePermission("tc-1", Decision{Approved: true}); !errors.Is(err, ErrGone) {
		t.Fatalf("resolve after cancel: want ErrGone, got %v", err)
	}
	select {
	case err := <-triggerErr:
		if err != nil {
			t.Fatalf("trigger returned error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("trigger did not return after cancel")
	}
}

func TestRegistryCancelDrainsPendingQuestion(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel()
	triggerErr := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx, "test/ask_question", nil)
		triggerErr <- err
	}()
	waitFor(t, 2*time.Second, "pending question", func() bool {
		r.quesMu.Lock()
		defer r.quesMu.Unlock()
		_, ok := r.questions["q-1"]
		return ok
	})

	if err := r.Cancel(ctx, "s-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitFor(t, 2*time.Second, "question drained on cancel", func() bool {
		r.quesMu.Lock()
		defer r.quesMu.Unlock()
		_, ok := r.questions["q-1"]
		return !ok
	})
	if err := r.ResolveQuestion("q-1", Answers{Declined: true}); !errors.Is(err, ErrGone) {
		t.Fatalf("resolve after cancel: want ErrGone, got %v", err)
	}
	select {
	case err := <-triggerErr:
		if err != nil {
			t.Fatalf("trigger returned error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("trigger did not return after cancel")
	}
}

func TestRegistryCancelDoesNotPoisonFutureRequests(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	// Trigger a permission, then cancel the session: the pending wait is
	// drained with a deny.
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel()
	triggerErr := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx, "test/ask_permission", nil)
		triggerErr <- err
	}()
	waitFor(t, 2*time.Second, "pending permission", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return ok
	})
	if err := r.Cancel(ctx, "s-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitFor(t, 2*time.Second, "permission drained on cancel", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return !ok
	})

	select {
	case err := <-triggerErr:
		if err != nil {
			t.Fatalf("trigger returned error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("trigger did not return after cancel")
	}

	// A request arriving after cancellation but before the next prompt
	// belongs to the cancelled turn and must be rejected.
	if _, err := r.child.Request(ctx, "test/ask_permission", nil); err != nil {
		t.Fatalf("trigger after cancel: %v", err)
	}
	waitFor(t, 2*time.Second, "permission rejected after cancel", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return !ok
	})

	// Starting a new prompt opens a new generation; requests from that
	// turn are allowed to wait for the HTTP response.
	r.beginTurn("s-1")
	triggerCtx2, triggerCancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel2()
	triggerErr2 := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx2, "test/ask_permission", nil)
		triggerErr2 <- err
	}()
	waitFor(t, 2*time.Second, "pending permission in next turn", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return ok
	})
	if err := r.ResolvePermission("tc-1", Decision{Approved: true}); err != nil {
		t.Fatalf("ResolvePermission after next turn: %v", err)
	}
	select {
	case err := <-triggerErr2:
		if err != nil {
			t.Fatalf("trigger in next turn returned error after resolve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("trigger in next turn did not return after resolve")
	}
}

func TestRegistryLateRequestFromCancelledTurnIsRejected(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	// Force a cancel in the window between a request's generation capture
	// and its registration, so the request belongs to the cancelled turn
	// but registers after the drain has already run.
	// The hook stays set for the lifetime of this test's registry (each
	// test gets a fresh Registry), so there is no concurrent write while
	// the child goroutine reads it.
	r.testHookBeforeRegister = func() {
		r.cancelSession("s-1")
	}

	// The late permission must be denied immediately, not parked forever
	// on the fresh (post-cancel) context.
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel()
	triggerErr := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx, "test/ask_permission", nil)
		triggerErr <- err
	}()
	waitFor(t, 2*time.Second, "late permission drained", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return !ok
	})
	if err := r.ResolvePermission("tc-1", Decision{Approved: true}); !errors.Is(err, ErrGone) {
		t.Fatalf("resolve late permission: want ErrGone, got %v", err)
	}
	select {
	case err := <-triggerErr:
		if err != nil {
			t.Fatalf("late permission trigger returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late permission trigger did not return")
	}

	// Same for a late question.
	triggerCtx2, triggerCancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel2()
	triggerErr2 := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx2, "test/ask_question", nil)
		triggerErr2 <- err
	}()
	waitFor(t, 2*time.Second, "late question drained", func() bool {
		r.quesMu.Lock()
		defer r.quesMu.Unlock()
		_, ok := r.questions["q-1"]
		return !ok
	})
	if err := r.ResolveQuestion("q-1", Answers{Declined: true}); !errors.Is(err, ErrGone) {
		t.Fatalf("resolve late question: want ErrGone, got %v", err)
	}
	select {
	case err := <-triggerErr2:
		if err != nil {
			t.Fatalf("late question trigger returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late question trigger did not return")
	}
}

func TestRegistryUntrackedSessionRequestIsRejected(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	_, cancel := testContext(t)
	defer cancel()

	// A permission keyed to an untracked session must be denied
	// immediately rather than waiting forever with no drain path.
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel()
	triggerErr := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx, "test/ask_permission", nil)
		triggerErr <- err
	}()
	waitFor(t, 2*time.Second, "untracked permission drained", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return !ok
	})
	select {
	case err := <-triggerErr:
		if err != nil {
			t.Fatalf("untracked permission trigger returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("untracked permission trigger did not return")
	}

	// Same for a question.
	triggerCtx2, triggerCancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel2()
	triggerErr2 := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx2, "test/ask_question", nil)
		triggerErr2 <- err
	}()
	waitFor(t, 2*time.Second, "untracked question drained", func() bool {
		r.quesMu.Lock()
		defer r.quesMu.Unlock()
		_, ok := r.questions["q-1"]
		return !ok
	})
	select {
	case err := <-triggerErr2:
		if err != nil {
			t.Fatalf("untracked question trigger returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("untracked question trigger did not return")
	}
}

func TestRegistryDrainSessionUnblocksWaiters(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, "/tmp/work", ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel()
	triggerErrPerm := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx, "test/ask_permission", nil)
		triggerErrPerm <- err
	}()
	waitFor(t, 2*time.Second, "pending permission", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return ok
	})

	triggerCtx2, triggerCancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel2()
	triggerErrQues := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx2, "test/ask_question", nil)
		triggerErrQues <- err
	}()
	waitFor(t, 2*time.Second, "pending question", func() bool {
		r.quesMu.Lock()
		defer r.quesMu.Unlock()
		_, ok := r.questions["q-1"]
		return ok
	})

	// DrainSession (as wired to the SSE OnUnsubscribe hook) must unblock
	// both waiters and remove the entries.
	r.DrainSession("s-1")
	r.permMu.Lock()
	_, permOK := r.permissions["tc-1"]
	r.permMu.Unlock()
	r.quesMu.Lock()
	_, quesOK := r.questions["q-1"]
	r.quesMu.Unlock()
	if permOK || quesOK {
		t.Fatal("DrainSession left stale pending entries")
	}
	if err := r.ResolvePermission("tc-1", Decision{Approved: true}); !errors.Is(err, ErrGone) {
		t.Fatalf("resolve permission after drain: want ErrGone, got %v", err)
	}
	if err := r.ResolveQuestion("q-1", Answers{Declined: true}); !errors.Is(err, ErrGone) {
		t.Fatalf("resolve question after drain: want ErrGone, got %v", err)
	}
	select {
	case err := <-triggerErrPerm:
		if err != nil {
			t.Fatalf("permission trigger returned error after drain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("permission trigger did not return after drain")
	}
	select {
	case err := <-triggerErrQues:
		if err != nil {
			t.Fatalf("question trigger returned error after drain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("question trigger did not return after drain")
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
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel()
	triggerErr := make(chan error, 1)
	go func() {
		_, err := r.child.Request(triggerCtx, "test/ask_permission", nil)
		triggerErr <- err
	}()
	waitFor(t, 2*time.Second, "pending permission", func() bool {
		r.permMu.Lock()
		defer r.permMu.Unlock()
		_, ok := r.permissions["tc-1"]
		return ok
	})

	// Simulate a second restart: clearPending must drain the map and a
	// late resolve must report ErrGone.
	r.clearPending()
	select {
	case err := <-triggerErr:
		if err != nil {
			t.Fatalf("trigger returned error after clear: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("trigger did not return after clear")
	}
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

func TestAnswersWireFormatIsStable(t *testing.T) {
	a := Answers{Answers: []Answer{
		{Question: "Which branch?", Answer: "main"},
	}}
	got, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"answers":[{"question":"Which branch?","answer":"main"}]}`
	if string(got) != want {
		t.Fatalf("wire format changed\n got: %s\nwant: %s", got, want)
	}
}

func TestAnswersDeclinedOmitsAnswers(t *testing.T) {
	got, err := json.Marshal(Answers{Declined: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"declined":true}`
	if string(got) != want {
		t.Fatalf("wire format changed\n got: %s\nwant: %s", got, want)
	}
}

func TestAnswersRoundTrip(t *testing.T) {
	in := `{"answers":[{"question":"q1","answer":"a1"},{"question":"q2","answer":"a2"}]}`
	var a Answers
	if err := json.Unmarshal([]byte(in), &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(a.Answers) != 2 || a.Answers[1].Answer != "a2" {
		t.Fatalf("decoded %+v, want two answers ending a2", a.Answers)
	}
}
