package session

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
)

func TestWaitSubagentBlocksUntilFinish(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	v := s.RegisterSubagent("task", nil)

	type outcome struct {
		view SubagentView
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		got, err := s.WaitSubagent(context.Background(), v.ID)
		done <- outcome{got, err}
	}()

	select {
	case <-done:
		t.Fatal("WaitSubagent returned while the subagent was still running")
	case <-time.After(20 * time.Millisecond):
	}

	s.FinishSubagent(v.ID, "report", nil)
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("WaitSubagent: %v", out.err)
		}
		if out.view.Status != SubagentDone {
			t.Fatalf("status = %v, want SubagentDone", out.view.Status)
		}
		if out.view.Summary != "report" {
			t.Fatalf("summary = %q, want %q", out.view.Summary, "report")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitSubagent did not return after FinishSubagent")
	}
}

func TestWaitSubagentReturnsFinishedViewImmediately(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	v := s.RegisterSubagent("task", nil)
	s.FinishSubagent(v.ID, "report", errors.New("boom"))

	got, err := s.WaitSubagent(context.Background(), v.ID)
	if err != nil {
		t.Fatalf("WaitSubagent: %v", err)
	}
	if got.Status != SubagentFailed {
		t.Fatalf("status = %v, want SubagentFailed", got.Status)
	}
	if got.Error != "boom" {
		t.Fatalf("Error = %q, want %q", got.Error, "boom")
	}
}

func TestWaitSubagentUnknownID(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	_, err := s.WaitSubagent(context.Background(), 999)
	if err == nil || !strings.Contains(err.Error(), "unknown subagent id") {
		t.Fatalf("err = %v, want unknown-id error", err)
	}
}

func TestWaitSubagentContextCancel(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	v := s.RegisterSubagent("task", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.WaitSubagent(ctx, v.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestContextCancelledByShutdown(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	ctx := s.Context()
	if ctx == nil {
		t.Fatal("Context() returned nil")
	}
	select {
	case <-ctx.Done():
		t.Fatal("session context cancelled before Shutdown")
	default:
	}
	s.Shutdown()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session context not cancelled by Shutdown")
	}
}

func TestWaitAnyReturnsFirstFinisher(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	a := s.RegisterSubagent("a", nil)
	b := s.RegisterSubagent("b", nil)
	c := s.RegisterSubagent("c", nil)

	type outcome struct {
		view SubagentView
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		got, err := s.WaitAnySubagent(context.Background(), []int64{a.ID, b.ID, c.ID})
		done <- outcome{got, err}
	}()

	select {
	case <-done:
		t.Fatal("WaitAnySubagent returned while all three were still running")
	case <-time.After(20 * time.Millisecond):
	}

	s.FinishSubagent(b.ID, "b report", nil)
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("WaitAnySubagent: %v", out.err)
		}
		if out.view.ID != b.ID {
			t.Fatalf("returned #%d, want the first finisher #%d", out.view.ID, b.ID)
		}
		if out.view.Summary != "b report" {
			t.Fatalf("summary = %q, want %q", out.view.Summary, "b report")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitAnySubagent did not return after a child finished")
	}
}

func TestWaitAnyReturnsAlreadyFinishedImmediately(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	a := s.RegisterSubagent("a", nil)
	b := s.RegisterSubagent("b", nil)
	s.FinishSubagent(a.ID, "done", nil)

	got, err := s.WaitAnySubagent(context.Background(), []int64{a.ID, b.ID})
	if err != nil {
		t.Fatalf("WaitAnySubagent: %v", err)
	}
	if got.ID != a.ID {
		t.Fatalf("returned #%d, want the already-finished #%d", got.ID, a.ID)
	}
}

func TestWaitAnyRespectsContext(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	a := s.RegisterSubagent("a", nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := s.WaitAnySubagent(ctx, []int64{a.ID}); err == nil {
		t.Fatal("want a context error, got nil")
	}
}

func TestWaitAnyRejectsEmptyIDs(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	if _, err := s.WaitAnySubagent(context.Background(), nil); err == nil {
		t.Fatal("want an error for an empty id set, not an infinite block")
	}
}

// The fan-in spawns one goroutine per pending child; the losers must all be
// released when the first winner is chosen.
func TestWaitAnyLeaksNoGoroutines(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	a := s.RegisterSubagent("a", nil)
	b := s.RegisterSubagent("b", nil)
	c := s.RegisterSubagent("c", nil)

	before := runtime.NumGoroutine()
	go func() {
		time.Sleep(10 * time.Millisecond)
		s.FinishSubagent(c.ID, "done", nil)
	}()
	if _, err := s.WaitAnySubagent(context.Background(), []int64{a.ID, b.ID, c.ID}); err != nil {
		t.Fatalf("WaitAnySubagent: %v", err)
	}
	// Give the released goroutines a moment to unwind.
	for i := 0; i < 50 && runtime.NumGoroutine() > before; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before {
		t.Errorf("goroutines: before %d, after %d — fan-in leaked", before, got)
	}
}

// --- Task 5: explicit-stop registration for background work --------------

// TestSubagentWorkStopCompletionRace pins the stop/settlement ordering: an
// accepted stop records StopRequested before the child is cancelled and settles
// as "stopped" only when the child observed the cancellation; a request that
// arrives after a successful settlement, an unrelated failure, or a panic cannot
// relabel the result, and a stopped/finished child is never a stop target.
func TestSubagentWorkStopCompletionRace(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})

	// A late stop cannot relabel a successful result.
	done := s.RegisterSubagent("done", nil)
	s.SetSubagentCancel(done.ID, func() {})
	s.FinishSubagent(done.ID, "reported", nil)
	if s.CancelSubagent(done.ID) {
		t.Fatal("a settled child was reported as stopped")
	}
	got, _ := s.Subagent(done.ID)
	if got.Outcome != "completed" || got.StopRequested {
		t.Fatalf("settled view = outcome %q stopRequested %v, want completed/false", got.Outcome, got.StopRequested)
	}

	// An accepted stop that the child observed settles as "stopped".
	stopped := s.RegisterSubagent("stopped", nil)
	cancelled := false
	s.SetSubagentCancel(stopped.ID, func() { cancelled = true })
	if !s.CancelSubagent(stopped.ID) {
		t.Fatal("CancelSubagent did not accept a running child's stop")
	}
	if !cancelled {
		t.Fatal("CancelSubagent did not cancel the child's context")
	}
	if mid, _ := s.Subagent(stopped.ID); !mid.StopRequested {
		t.Fatal("an accepted stop was not recorded on the view")
	}
	s.FinishSubagent(stopped.ID, "", context.Canceled)
	gotStop, _ := s.Subagent(stopped.ID)
	if gotStop.Outcome != "stopped" {
		t.Fatalf("stopped child outcome = %q, want stopped", gotStop.Outcome)
	}
	// The existing three-valued status stays compatible: a non-nil error is
	// still a SubagentFailed status.
	if gotStop.Status != SubagentFailed {
		t.Fatalf("stopped child status = %v, want SubagentFailed (status compatibility)", gotStop.Status)
	}

	// Without an accepted stop, an unrelated failure is "failed".
	failed := s.RegisterSubagent("failed", nil)
	s.FinishSubagent(failed.ID, "", errors.New("boom"))
	if gotF, _ := s.Subagent(failed.ID); gotF.Outcome != "failed" {
		t.Fatalf("unrelated failure outcome = %q, want failed", gotF.Outcome)
	}

	// A stop accepted before a successful completion cannot relabel it.
	late := s.RegisterSubagent("late", nil)
	s.SetSubagentCancel(late.ID, func() {})
	if !s.CancelSubagent(late.ID) {
		t.Fatal("CancelSubagent did not accept the running child's stop")
	}
	s.FinishSubagent(late.ID, "still fine", nil)
	if gotL, _ := s.Subagent(late.ID); gotL.Outcome != "completed" {
		t.Fatalf("late stop relabelled a successful result: %q", gotL.Outcome)
	}
}

// fakeTurnStopService records the stop calls the session makes on the
// conversation's command service.
type fakeTurnStopService struct {
	turnIDs []string
	causes  []string
	targets []string
}

func (f *fakeTurnStopService) StopAttached(turnID, cause string) ([]string, error) {
	f.turnIDs = append(f.turnIDs, turnID)
	f.causes = append(f.causes, cause)
	return []string{"job-1"}, nil
}

func (f *fakeTurnStopService) RequestStop(executionID, cause string) error {
	f.targets = append(f.targets, executionID)
	return nil
}

// TestTurnStopRegistrationRetainsEarlyStop pins the retained-stop behavior: a
// stop that lands before the turn registers is not dropped. The registration
// adopts it, marks the group stopped, and asks the service — so a turn cannot
// start doing work the user already asked to stop.
func TestTurnStopRegistrationRetainsEarlyStop(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	svc := &fakeTurnStopService{}
	s.SetTurnStopService(svc)

	// No registration yet: the request is retained.
	if ids, err := s.RequestTurnStop("stop before start"); err != nil || ids != nil {
		t.Fatalf("early RequestTurnStop = (%v, %v), want (nil, nil)", ids, err)
	}

	token, pending := s.BeginTurnStop("")
	if pending != "stop before start" {
		t.Fatalf("pending cause = %q, want the retained stop", pending)
	}
	if !token.Stopped() {
		t.Fatal("the registration did not adopt the retained stop")
	}
	if len(svc.turnIDs) != 1 || svc.turnIDs[0] != token.TurnID() {
		t.Fatalf("service stops = %v, want the adopted registration %q", svc.turnIDs, token.TurnID())
	}
	if token.StopCause() != "stop before start" {
		t.Fatalf("stop cause = %q", token.StopCause())
	}
	// The retained cause is consumed: the next registration starts clean.
	next, pending := s.BeginTurnStop("")
	if pending != "" {
		t.Fatalf("retained cause leaked into the next registration: %q", pending)
	}
	if next.Stopped() {
		t.Fatal("a fresh registration inherited the previous stop")
	}
}

// TestOrdinaryCleanupDoesNotRequestExecutionStop pins the other direction:
// unregistering a turn that simply finished must not ask the service to stop
// anything, because a turn ending is not a command being stopped.
func TestOrdinaryCleanupDoesNotRequestExecutionStop(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	svc := &fakeTurnStopService{}
	s.SetTurnStopService(svc)

	token, _ := s.BeginTurnStop("")
	if !s.EndTurnStop(token) {
		t.Fatal("EndTurnStop did not clear its own registration")
	}
	if len(svc.turnIDs) != 0 || len(svc.targets) != 0 {
		t.Fatalf("ordinary cleanup requested a stop: turns=%v targets=%v", svc.turnIDs, svc.targets)
	}
	if s.CurrentTurnStop() != nil {
		t.Fatal("registration survived ordinary cleanup")
	}
}

// TestCancelSubagentTargetsOnlyTheChild pins parent/child targeting: stopping one
// child asks the service to stop THAT child's group, leaves the parent's turn
// alone, and never marks the parent's group stopped.
func TestCancelSubagentTargetsOnlyTheChild(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	svc := &fakeTurnStopService{}
	s.SetTurnStopService(svc)

	parent, _ := s.BeginTurnStop("")
	child := NewTurnStopToken(svc, "subagent-1")

	view := s.RegisterSubagent("child", nil)
	s.RegisterSubagentStopGroup(view.ID, child)
	cancelled := false
	s.SetSubagentCancel(view.ID, func() { cancelled = true })

	if !s.CancelSubagent(view.ID) {
		t.Fatal("CancelSubagent did not stop a running child")
	}
	if !cancelled {
		t.Fatal("CancelSubagent did not cancel the child's context")
	}
	if len(svc.turnIDs) != 1 || svc.turnIDs[0] != child.TurnID() {
		t.Fatalf("targeted stop reached %v, want the child's group %q", svc.turnIDs, child.TurnID())
	}
	if parent.Stopped() {
		t.Fatal("a targeted child stop marked the parent's turn stopped")
	}
	if s.CurrentTurnStop() != parent {
		t.Fatal("a targeted child stop replaced the parent's registration")
	}
}

// TestCancelSubagentIgnoresFinishedChild pins that normal completion is never an
// explicit stop: a finished child's group is cleared and a stop request for it
// does nothing.
func TestCancelSubagentIgnoresFinishedChild(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	svc := &fakeTurnStopService{}
	s.SetTurnStopService(svc)

	child := NewTurnStopToken(svc, "subagent-1")
	view := s.RegisterSubagent("child", nil)
	s.RegisterSubagentStopGroup(view.ID, child)
	s.FinishSubagent(view.ID, "done", nil)
	s.ClearSubagentStopGroup(view.ID)

	if s.CancelSubagent(view.ID) {
		t.Fatal("CancelSubagent reported stopping an already-finished child")
	}
	if len(svc.turnIDs) != 0 {
		t.Fatalf("a finished child's stop reached the service: %v", svc.turnIDs)
	}
	if child.Stopped() {
		t.Fatal("a finished child's group was marked stopped")
	}
}

// TestRequestTurnStopWithoutServiceReportsRatherThanPretending pins the
// no-service case: a session with no command service reports that nothing was
// stopped instead of reporting a successful stop.
func TestRequestTurnStopWithoutServiceReportsRatherThanPretending(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	token, _ := s.BeginTurnStop("")
	if _, err := s.RequestTurnStop("stop"); !errors.Is(err, ErrNoTurnStopService) {
		t.Fatalf("RequestTurnStop without a service = %v, want ErrNoTurnStopService", err)
	}
	if !token.Stopped() {
		t.Fatal("the group did not record the (refused) stop decision")
	}
	if token.StopCause() != "stop" {
		t.Fatalf("stop cause = %q, want the requested cause recorded for diagnosis", token.StopCause())
	}
}

// TestChildLifecycleKeepsOneWorkItemPerChild pins Task 8's child registration for
// the card path: a registered child is exactly one item of the conversation's
// membership, its card does not add a second, and finishing it releases exactly
// that item.
func TestChildLifecycleKeepsOneWorkItemPerChild(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})

	view := s.RegisterSubagent("child", nil)
	if got := s.WorkMembership().Items; len(got) != 1 || got[0].Kind != WorkKindChild {
		t.Fatalf("membership after registration = %+v, want one child item", got)
	}
	if got := s.WorkMembership().Items[0].SubagentID; got != view.ID {
		t.Fatalf("child item subagent id = %d, want the card's %d", got, view.ID)
	}

	s.FinishSubagent(view.ID, "done", nil)
	if got := s.WorkMembership().Items; len(got) != 0 {
		t.Fatalf("membership after the child finished = %+v, want empty", got)
	}
}

// TestEnterSubagentPairsItsWorkItemWithTheCard pins the identity binding: the
// creation's item is the card's item — one child, one ID — and the paired exit
// releases it even though the card appeared after it.
func TestEnterSubagentPairsItsWorkItemWithTheCard(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})

	work, err := s.EnterSubagentWork()
	if err != nil {
		t.Fatalf("EnterSubagentWork: %v", err)
	}
	if work == "" {
		t.Fatal("EnterSubagentWork returned no work item id")
	}
	// The card names the SAME item, so the child is not registered twice.
	view := s.RegisterSubagentWithMetaAndWork(work, "delegate", nil, SubagentMeta{})
	items := s.WorkMembership().Items
	if len(items) != 1 {
		t.Fatalf("membership = %+v, want exactly one item for one child", items)
	}
	if items[0].ID != work || items[0].SubagentID != view.ID {
		t.Fatalf("child item = %+v, want id %q bound to card %d", items[0], work, view.ID)
	}

	// The card finishing does not release the creation's item: the child is still
	// live until its own exit.
	s.FinishSubagent(view.ID, "done", nil)
	if got := s.WorkMembership().Items; len(got) != 1 {
		t.Fatalf("membership after the card finished = %+v, want the creation's item still open", got)
	}
	// The paired exit releases the creation's item, and nothing else.
	s.ExitSubagent()
	if got := s.WorkMembership().Items; len(got) != 0 {
		t.Fatalf("membership after ExitSubagent = %+v, want empty", got)
	}
}

// TestExitSubagentWorkReleasesTheNamedChild pins the identity-keyed release: with
// two children in flight, exiting the FIRST must release that child's own item.
// A FIFO release would drop the most recently registered item — the child that is
// still running — and leave the exited child's item looking active, so the
// membership an end warns about would name the wrong child.
func TestExitSubagentWorkReleasesTheNamedChild(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})

	first, err := s.EnterSubagentWork()
	if err != nil {
		t.Fatalf("EnterSubagentWork (first): %v", err)
	}
	second, err := s.EnterSubagentWork()
	if err != nil {
		t.Fatalf("EnterSubagentWork (second): %v", err)
	}
	if first == second {
		t.Fatal("two children registered the same work item id")
	}
	firstView := s.RegisterSubagentWithMetaAndWork(first, "first", nil, SubagentMeta{})
	secondView := s.RegisterSubagentWithMetaAndWork(second, "second", nil, SubagentMeta{})

	// The first child finishes while the second is still running.
	s.ExitSubagentWork(first)

	items := s.WorkMembership().Items
	if len(items) != 1 {
		t.Fatalf("membership after the first child exited = %+v, want only the second child", items)
	}
	if items[0].ID != second || items[0].SubagentID != secondView.ID {
		t.Fatalf("surviving item = %+v, want the still-running child %q (card %d)", items[0], second, secondView.ID)
	}
	for _, item := range items {
		if item.ID == first || item.SubagentID == firstView.ID {
			t.Fatalf("the exited child is still in the membership: %+v", item)
		}
	}

	// The second child's own exit releases its item, leaving nothing behind.
	s.ExitSubagentWork(second)
	if got := s.WorkMembership().Items; len(got) != 0 {
		t.Fatalf("membership after both children exited = %+v, want empty", got)
	}
}
