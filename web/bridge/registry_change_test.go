package bridge

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// changeCall is one OnChange report: the session and what changed.
type changeCall struct {
	sessionID string
	change    registryChange
}

// TestTheRegistryReportsEachChange drives each site that changes a tracked
// session and asserts the exact reports it made.
func TestTheRegistryReportsEachChange(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	// A followed_run update arrives on the child's own read goroutine, so the
	// reports are appended off the test's goroutine: the slice is read and
	// written under a lock of its own.
	var (
		callsMu sync.Mutex
		calls   []changeCall
	)
	reportCount := func() int {
		callsMu.Lock()
		defer callsMu.Unlock()
		return len(calls)
	}
	reportsFrom := func(before int) []changeCall {
		callsMu.Lock()
		defer callsMu.Unlock()
		return append([]changeCall(nil), calls[before:]...)
	}
	r.OnChange = func(sessionID string, change registryChange) {
		callsMu.Lock()
		calls = append(calls, changeCall{sessionID: sessionID, change: change})
		callsMu.Unlock()
	}
	cwd := AgentPath("/tmp/work")
	steps := []struct {
		name string
		run  func(t *testing.T)
		want []changeCall
	}{
		{
			name: "New tracks a session",
			run: func(t *testing.T) {
				if _, err := r.New(ctx, cwd, ""); err != nil {
					t.Fatalf("New: %v", err)
				}
			},
			want: []changeCall{{"s-1", changeTracked}},
		},
		{
			name: "a second track of the same id reports nothing",
			run: func(t *testing.T) {
				r.track("s-1", cwd)
			},
		},
		{
			name: "attachViewer",
			run: func(t *testing.T) {
				if !r.attachViewer("s-1") {
					t.Fatal("attachViewer on a tracked session = false")
				}
			},
			want: []changeCall{{"s-1", changeViewers}},
		},
		{
			name: "detachViewer",
			run: func(t *testing.T) {
				r.detachViewer("s-1")
			},
			want: []changeCall{{"s-1", changeViewers}},
		},
		{
			name: "beginTurn",
			run: func(t *testing.T) {
				r.beginTurn("s-1")
			},
			want: []changeCall{{"s-1", changeTurnStarted}},
		},
		{
			name: "endTurn",
			run: func(t *testing.T) {
				r.endTurn("s-1")
			},
			want: []changeCall{{"s-1", changeTurnEnded}},
		},
		{
			name: "a followed run starts",
			run: func(t *testing.T) {
				if _, err := r.child.Request(ctx, "test/followed_run", map[string]any{
					"sessionId": "s-1", "state": "started", "runId": "run-1",
				}); err != nil {
					t.Fatalf("followed_run started: %v", err)
				}
			},
			want: []changeCall{{"s-1", changeTurnStarted}},
		},
		{
			name: "the followed run ends",
			run: func(t *testing.T) {
				if _, err := r.child.Request(ctx, "test/followed_run", map[string]any{
					"sessionId": "s-1", "state": "ended", "runId": "run-1", "outcome": "done",
				}); err != nil {
					t.Fatalf("followed_run ended: %v", err)
				}
			},
			want: []changeCall{{"s-1", changeTurnEnded}},
		},
		{
			name: "Close drops the session",
			run: func(t *testing.T) {
				if err := r.Close(ctx, "s-1"); err != nil {
					t.Fatalf("Close: %v", err)
				}
			},
			want: []changeCall{{"s-1", changeDropped}},
		},
	}
	for _, step := range steps {
		before := reportCount()
		step.run(t)
		if len(step.want) > 0 {
			// A followed_run update arrives on the child's read goroutine,
			// so wait for the report it makes.
			waitFor(t, 2*time.Second, step.name+" report", func() bool {
				return reportCount()-before >= len(step.want)
			})
		}
		got := reportsFrom(before)
		if len(got) != len(step.want) {
			t.Fatalf("%s: reports = %+v, want %+v", step.name, got, step.want)
		}
		for i := range step.want {
			if got[i] != step.want[i] {
				t.Fatalf("%s: report %d = %+v, want %+v", step.name, i, got[i], step.want[i])
			}
		}
	}
}

// TestOnChangeRunsWithoutTheLock: an OnChange that reads the registry must not
// deadlock against the change that reported it.
func TestOnChangeRunsWithoutTheLock(t *testing.T) {
	r := NewRegistry(&Child{})
	reported := make(chan changeCall, 1)
	r.OnChange = func(sessionID string, change registryChange) {
		if _, ok := r.lookup(sessionID); ok {
			reported <- changeCall{sessionID: sessionID, change: change}
		}
	}
	// The change is reported from the goroutine that made it: if OnChange ran
	// under the registry's lock, its lookup would block forever.
	go r.track("s-1", AgentPath("/tmp/work"))
	select {
	case got := <-reported:
		if got.sessionID != "s-1" || got.change != changeTracked {
			t.Fatalf("report = %+v, want s-1 tracked", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OnChange deadlocked: it ran under the registry's lock")
	}
}

// TestAParkedRequestKeepsItsPayload: PendingRequest reports each parked
// request with the payload it arrived with, and nothing once it is resolved.
func TestAParkedRequestKeepsItsPayload(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := r.New(ctx, AgentPath("/tmp/work"), ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer triggerCancel()

	go func() { _, _ = r.child.Request(triggerCtx, "test/ask_permission", nil) }()
	var parked contract.PendingRequest
	waitFor(t, 5*time.Second, "a parked permission", func() bool {
		var ok bool
		parked, ok = r.PendingRequest("s-1")
		return ok
	})
	if parked.Kind != contract.PendingApproval || parked.ID != "tc-1" {
		t.Fatalf("PendingRequest = %+v, want approval tc-1", parked)
	}
	var params map[string]any
	if err := json.Unmarshal(parked.Params, &params); err != nil {
		t.Fatalf("PendingRequest params %s: %v", parked.Params, err)
	}
	if params["toolCallId"] != "tc-1" {
		t.Fatalf("PendingRequest params = %s, want the request's own payload", parked.Params)
	}
	if err := r.ResolvePermission("tc-1", Decision{Approved: true}); err != nil {
		t.Fatalf("ResolvePermission: %v", err)
	}
	waitFor(t, 5*time.Second, "the resolved permission to be cleared", func() bool {
		_, ok := r.PendingRequest("s-1")
		return !ok
	})

	go func() { _, _ = r.child.Request(triggerCtx, "test/ask_question", nil) }()
	waitFor(t, 5*time.Second, "a parked question", func() bool {
		var ok bool
		parked, ok = r.PendingRequest("s-1")
		return ok
	})
	if parked.Kind != contract.PendingQuestion || parked.ID != "q-1" {
		t.Fatalf("PendingRequest = %+v, want question q-1", parked)
	}
	if err := json.Unmarshal(parked.Params, &params); err != nil {
		t.Fatalf("PendingRequest params %s: %v", parked.Params, err)
	}
	if params["questionId"] != "q-1" {
		t.Fatalf("PendingRequest params = %s, want the request's own payload", parked.Params)
	}
	if err := r.ResolveQuestion("q-1", Answers{Declined: true}); err != nil {
		t.Fatalf("ResolveQuestion: %v", err)
	}
	waitFor(t, 5*time.Second, "the resolved question to be cleared", func() bool {
		_, ok := r.PendingRequest("s-1")
		return !ok
	})
}

// TestClientSessionsLeaveTheFleetsOut: a fleet agent's session is not a
// client's, and the rest keep summaries' order.
func TestClientSessionsLeaveTheFleetsOut(t *testing.T) {
	r := NewRegistry(&Child{})
	r.track("s-1", AgentPath("/tmp/a"))
	r.TrackFleet("f-1", AgentPath("/tmp/b"))
	r.track("s-2", AgentPath("/tmp/c"))
	r.attachViewer("s-1")
	r.beginTurn("s-2")

	got := r.clientSessions()
	if len(got) != 2 {
		t.Fatalf("clientSessions = %+v, want the two client sessions and no fleet", got)
	}
	// s-2 was tracked after s-1, so it is most recently active.
	if got[0].ID != "s-2" || !got[0].Busy || got[0].Viewers != 0 {
		t.Fatalf("clientSessions[0] = %+v, want s-2 busy with no viewer", got[0])
	}
	if got[1].ID != "s-1" || got[1].Busy || got[1].Viewers != 1 {
		t.Fatalf("clientSessions[1] = %+v, want s-1 idle with one viewer", got[1])
	}
}

// TestDeleteReportsItsDrop: Delete untracks a session, so it reports the
// same changeDropped Close does. Without it the index keeps a deleted
// session as open until something else happens to announce the project.
func TestDeleteReportsItsDrop(t *testing.T) {
	r, _ := newTestRegistry(t, "registry")
	ctx, cancel := testContext(t)
	defer cancel()

	var calls []changeCall
	r.OnChange = func(sessionID string, change registryChange) {
		calls = append(calls, changeCall{sessionID: sessionID, change: change})
	}
	cwd := AgentPath("/tmp/work")
	if _, err := r.New(ctx, cwd, ""); err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Delete(ctx, "s-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	want := []changeCall{{"s-1", changeTracked}, {"s-1", changeDropped}}
	if len(calls) != len(want) {
		t.Fatalf("reports = %+v, want %+v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("report %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
}
