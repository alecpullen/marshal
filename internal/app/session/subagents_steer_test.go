package session

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/db"
)

// countSteerNotices counts the parent-visible notices a session's transcript
// carries: one per accepted request, and never one for a refused one. A plain
// message survives the database as a NULL content type, which restores as "",
// so both mean the same thing here.
func countSteerNotices(state *State) int {
	n := 0
	for _, m := range state.Messages() {
		if m.Role != RoleSystem {
			continue
		}
		if m.ContentType != "" && m.ContentType != ContentTypePlain {
			continue
		}
		if strings.Contains(m.Content, "steered subagent") {
			n++
		}
	}
	return n
}

// TestUserSubagentSteerAcceptedAndNotifiesParent pins the happy path: a user
// instruction reaches the live child's steering queue and the parent transcript
// records a notice naming the child and what it was told. Acceptance is not
// consumption: nothing drained the queue, so the child's transcript carries no
// steering message yet.
func TestUserSubagentSteerAcceptedAndNotifiesParent(t *testing.T) {
	state := newTestState()
	child := newTestState()
	view := state.RegisterSubagent("refactor the parser", child)

	if err := state.SteerSubagent("req-1", view.ID, "wrap up and report"); err != nil {
		t.Fatalf("SteerSubagent: %v", err)
	}
	if got := child.SteeringQueue(); len(got) != 1 || got[0] != "wrap up and report" {
		t.Fatalf("child steering queue = %v, want the one instruction", got)
	}
	for _, m := range child.Messages() {
		if m.ContentType == ContentTypeSteering {
			t.Fatalf("child transcript already carries %q: acceptance is not consumption", m.Content)
		}
	}

	if n := countSteerNotices(state); n != 1 {
		t.Fatalf("parent notices = %d, want exactly one", n)
	}
	for _, m := range state.Messages() {
		if m.ContentType != ContentTypePlain || !strings.Contains(m.Content, "steered subagent") {
			continue
		}
		if !strings.Contains(m.Content, view.Label) {
			t.Errorf("parent notice %q does not name the child label %q", m.Content, view.Label)
		}
		if !strings.Contains(m.Content, "wrap up and report") {
			t.Errorf("parent notice %q does not carry the instruction", m.Content)
		}
	}
}

// TestUserSubagentSteerReplayAndConflict pins exactly-once delivery per request
// ID: an identical replay is accepted without a second enqueue and without a
// second notice, while a reused request ID naming another instruction or another
// child is refused. Blank input, an unknown child and a finished child are
// refusals too, never silent acceptances.
func TestUserSubagentSteerReplayAndConflict(t *testing.T) {
	state := newTestState()
	first := state.RegisterSubagent("first child", newTestState())
	second := state.RegisterSubagent("second child", newTestState())

	if err := state.SteerSubagent("req-1", first.ID, "wrap up now"); err != nil {
		t.Fatalf("SteerSubagent: %v", err)
	}
	if err := state.SteerSubagent("req-1", first.ID, "wrap up now"); err != nil {
		t.Fatalf("replayed SteerSubagent: %v", err)
	}
	if got := first.Child.SteeringQueue(); len(got) != 1 {
		t.Fatalf("child queue after replay = %v, want one instruction", got)
	}
	if n := countSteerNotices(state); n != 1 {
		t.Fatalf("parent notices after replay = %d, want one", n)
	}

	// The same request ID may not name a different instruction ...
	if err := state.SteerSubagent("req-1", first.ID, "something else"); err == nil {
		t.Fatal("SteerSubagent accepted a reused request ID with a different message")
	}
	// ... nor a different child.
	if err := state.SteerSubagent("req-1", second.ID, "wrap up now"); err == nil {
		t.Fatal("SteerSubagent accepted a reused request ID naming another child")
	}
	if got := second.Child.SteeringQueue(); len(got) != 0 {
		t.Fatalf("second child's queue = %v, want nothing", got)
	}

	if err := state.SteerSubagent("req-2", first.ID, "   "); err == nil {
		t.Fatal("SteerSubagent accepted a blank message")
	}
	if err := state.SteerSubagent("  ", first.ID, "an instruction"); err == nil {
		t.Fatal("SteerSubagent accepted a blank request ID")
	}
	if err := state.SteerSubagent("req-3", 999, "an instruction"); err == nil {
		t.Fatal("SteerSubagent accepted an unknown child")
	}
	if got := first.Child.SteeringQueue(); len(got) != 1 {
		t.Fatalf("child queue after refusals = %v, want the one accepted instruction", got)
	}
	if n := countSteerNotices(state); n != 1 {
		t.Fatalf("parent notices after refusals = %d, want one", n)
	}

	state.FinishSubagent(first.ID, "done", nil)
	if err := state.SteerSubagent("req-4", first.ID, "one more thing"); err == nil {
		t.Fatal("SteerSubagent accepted an instruction for a finished child")
	}
}

// TestUserSubagentSteerFinishRace pins the settlement coordination: FinishSubagent
// and a steer cannot interleave, so the instructions the child's queue holds are
// exactly the steers that were accepted, and a steer that lands after the
// settlement is refused rather than accepted into a queue nothing will drain.
func TestUserSubagentSteerFinishRace(t *testing.T) {
	state := newTestState()
	child := newTestState()
	view := state.RegisterSubagent("task", child)

	const steers = 8
	var accepted atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < steers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			message := fmt.Sprintf("instruction %d", i)
			if err := state.SteerSubagent(fmt.Sprintf("req-%d", i), view.ID, message); err == nil {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		state.FinishSubagent(view.ID, "report", nil)
	}()
	close(start)
	wg.Wait()

	queued := child.SteeringQueue()
	if int64(len(queued)) != accepted.Load() {
		t.Fatalf("child queue holds %d instructions but %d steers were accepted", len(queued), accepted.Load())
	}
	if err := state.SteerSubagent("req-late", view.ID, "too late"); err == nil {
		t.Fatal("SteerSubagent accepted an instruction after the child settled")
	}
	if got := len(child.SteeringQueue()); got != len(queued) {
		t.Fatalf("child queue grew to %d after a refused steer, want %d", got, len(queued))
	}
}

// TestUserSubagentSteerSessionIsolation pins that the ledger is the session's:
// the same request ID is one request per session, not one per process, and a
// session cannot steer another session's child.
func TestUserSubagentSteerSessionIsolation(t *testing.T) {
	one := newTestState()
	two := newTestState()
	first := one.RegisterSubagent("first child", newTestState())
	second := two.RegisterSubagent("second child", newTestState())

	if err := one.SteerSubagent("req-1", first.ID, "first instruction"); err != nil {
		t.Fatalf("SteerSubagent on the first session: %v", err)
	}
	if err := two.SteerSubagent("req-1", second.ID, "second instruction"); err != nil {
		t.Fatalf("SteerSubagent on the second session reused the first session's request ID: %v", err)
	}
	if got := first.Child.SteeringQueue(); len(got) != 1 || got[0] != "first instruction" {
		t.Fatalf("first child queue = %v", got)
	}
	if got := second.Child.SteeringQueue(); len(got) != 1 || got[0] != "second instruction" {
		t.Fatalf("second child queue = %v", got)
	}

	// The other session's child is not this session's to steer.
	if err := one.SteerSubagent("req-2", second.ID, "cross-session"); err == nil {
		t.Fatal("SteerSubagent reached another session's child")
	}
	if got := second.Child.SteeringQueue(); len(got) != 1 {
		t.Fatalf("second child queue = %v, want only its own instruction", got)
	}
	if n := countSteerNotices(one); n != 1 {
		t.Fatalf("first session's notices = %d, want one", n)
	}
}

// TestUserSubagentSteerReplayAfterReconnect pins the durable receipt: a session
// that restored its transcript answers a retried request from the receipt the
// transcript carries, so a retry after a lost response and a reconnect cannot
// enqueue twice, and a conflicting reuse is still refused.
func TestUserSubagentSteerReplayAfterReconnect(t *testing.T) {
	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}
	sessionID := "subagent-steer-reconnect"
	if err := dbConn.CreateSession(sessionID, projectID, "steer", time.Now().UTC()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	before := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})
	child := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	view := before.RegisterSubagent("task", child)
	if err := before.SteerSubagent("req-1", view.ID, "wrap up and report"); err != nil {
		t.Fatalf("SteerSubagent: %v", err)
	}

	// The reconnect: a fresh State over the same session, with no live child.
	after := New(config.Default(), "/repo", time.Unix(200, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})
	if err := after.SteerSubagent("req-1", view.ID, "wrap up and report"); err != nil {
		t.Fatalf("replayed SteerSubagent after reconnect: %v", err)
	}
	if n := countSteerNotices(after); n != 1 {
		t.Fatalf("restored notices = %d, want the one accepted request", n)
	}
	if err := after.SteerSubagent("req-1", view.ID, "a different instruction"); err == nil {
		t.Fatal("a restored session accepted a reused request ID with a different message")
	}
	if err := after.SteerSubagent("req-2", view.ID, "a new instruction"); err == nil {
		t.Fatal("a restored session accepted an instruction for a child it has no record of")
	}
}
