package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// This file covers the user-visible side of snapshot capture. The failure
// modes are the interesting ones: a capture that fails, is skipped, or is
// captured but not recorded must leave a warning in the transcript while
// changing NOTHING about whether the tool runs.

// fakeWriteSnapshotter is the Snapshotter double for these tests: it returns
// a scripted (hash, error) pair and records how many times it was asked.
type fakeWriteSnapshotter struct {
	hash  string
	err   error
	calls int
	// block makes Track wait for the caller's context to end and return its
	// error, which is what a capture that outruns snapshotCaptureDeadline looks
	// like from the runner's side.
	block bool
	// lastDeadline records the deadline Track observed, so a test can pin which
	// phase gets the short pre-write bound.
	lastDeadline time.Time
	hadDeadline  bool
}

func (f *fakeWriteSnapshotter) Track(ctx context.Context) (string, error) {
	f.calls++
	f.lastDeadline, f.hadDeadline = ctx.Deadline()
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return f.hash, f.err
}

func (f *fakeWriteSnapshotter) Diff(ctx context.Context, hash string) (string, error) { return "", nil }

func (f *fakeWriteSnapshotter) Restore(ctx context.Context, hash string) error { return nil }

// fakeSnapshotRecorder records every SaveSnapshot call so a test can assert a
// row was NOT written for a failed or skipped capture.
type fakeSnapshotRecorder struct {
	mu    sync.Mutex
	saved []recordedSnapshot
	err   error
}

type recordedSnapshot struct {
	hash  string
	files []string
}

func (f *fakeSnapshotRecorder) SaveSnapshot(sessionID string, turnIndex int, hash string, files []string, at time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, recordedSnapshot{hash: hash, files: append([]string(nil), files...)})
	return int64(len(f.saved)), f.err
}

func (f *fakeSnapshotRecorder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.saved)
}

// snapshotWarningTranscript returns the plain system messages that carry the
// snapshot warning — the transcript projection the TUI and ACP both render.
func snapshotWarningTranscript(st *session.State) []string {
	var out []string
	for _, m := range st.Messages() {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "snapshot") {
			out = append(out, m.Content)
		}
	}
	return out
}

// writeToolRunner builds a runner with a registered non-read-only tool whose
// handler records that it ran.
func writeToolRunner(t *testing.T, state *session.State, snap Snapshotter, rec SnapshotRecorder) (*Runner, *bool) {
	t.Helper()
	executed := false
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:   "file.write",
		Risk:   registry.RiskWorkspaceWrite,
		Schema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executed = true
			return registry.ToolResult{Summary: "wrote"}, nil
		},
	}); err != nil {
		t.Fatalf("register file.write: %v", err)
	}
	runner := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.Snapshotter = snap
	runner.SnapshotRecorder = rec
	return runner, &executed
}

func writeFileAction() ModelAction {
	return ModelAction{
		Type: ActionToolCall,
		Tool: "file.write",
		Args: json.RawMessage(`{"path":"target.go"}`),
	}
}

// TestPreWriteCaptureFailureStillRunsToolAndWarns is the core preservation
// property: a failed capture changes nothing about execution, and the user is
// told rollback protection is missing.
func TestPreWriteCaptureFailureStillRunsToolAndWarns(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{err: errors.New("budget_exhausted: workspace budget exceeded")}
	rec := &fakeSnapshotRecorder{}
	runner, executed := writeToolRunner(t, state, snap, rec)

	if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if !*executed {
		t.Fatal("tool did not run after a failed snapshot capture; continuation policy changed")
	}
	if rec.count() != 0 {
		t.Fatalf("recorded %d snapshot rows for a failed capture, want 0", rec.count())
	}
	warnings := snapshotWarningTranscript(state)
	if len(warnings) != 1 {
		t.Fatalf("transcript has %d snapshot warnings, want 1: %v", len(warnings), warnings)
	}
	msg := warnings[0]
	for _, want := range []string{
		state.Workspace().ActiveRoot, // the workspace
		"budget_exhausted",           // the structured reason
		"WITHOUT a rollback snapshot",
		"marshal snapshots status",
		"marshal snapshots cleanup",
		"2.0 GB", // the applicable workspace limit
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning missing %q:\n%s", want, msg)
		}
	}
}

// TestPreWriteCaptureTimeoutIsReportedNotSwallowed pins the bound this task
// adds: a capture that outruns snapshotCaptureDeadline is a named capture
// outcome ("capture_timeout"), not an invisible cancellation. The old
// cancellation filters matched any error equal to ctx.Err(), so a capture that
// timed out on its own bound would have been logged at Debug and shown to
// nobody — while the tool call proceeded with no rollback snapshot at all.
func TestPreWriteCaptureTimeoutIsReportedNotSwallowed(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{block: true}
	rec := &fakeSnapshotRecorder{}
	runner, executed := writeToolRunner(t, state, snap, rec)
	// Drive the bound from a short injected value rather than the real five
	// seconds: the test pins the behaviour, not the wall clock.
	runner.SnapshotCaptureDeadline = 20 * time.Millisecond

	start := time.Now()
	if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("capture took %s, want it bounded near the injected %s", elapsed, runner.SnapshotCaptureDeadline)
	}
	if !*executed {
		t.Fatal("tool did not run after a timed-out snapshot capture; continuation policy changed")
	}
	if rec.count() != 0 {
		t.Fatalf("recorded %d snapshot rows for a timed-out capture, want 0", rec.count())
	}
	warnings := snapshotWarningTranscript(state)
	if len(warnings) != 1 {
		t.Fatalf("transcript has %d snapshot warnings, want 1: %v", len(warnings), warnings)
	}
	msg := warnings[0]
	for _, want := range []string{
		"capture_timeout",                // the structured reason
		"WITHOUT a rollback snapshot",    // the tool may proceed
		state.Workspace().ActiveRoot,     // the workspace
		"could not be enumerated within", // the timeout detail
		"marshal snapshots status",       // recovery starts with status
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning missing %q:\n%s", want, msg)
		}
	}
	// A timed-out capture is not a store that needs cleaning.
	if strings.Contains(msg, "marshal snapshots cleanup") {
		t.Errorf("timeout warning advises a store cleanup it cannot help:\n%s", msg)
	}
}

// TestPreWriteCaptureDeadlineIsFiveSeconds pins the number itself: the bound
// is a product decision, so a test must fail if someone changes it quietly.
func TestPreWriteCaptureDeadlineIsFiveSeconds(t *testing.T) {
	if snapshotCaptureDeadline != 5*time.Second {
		t.Fatalf("snapshotCaptureDeadline = %s, want 5s", snapshotCaptureDeadline)
	}
}

// TestTurnStartCaptureKeepsTheManagersDeadline pins the phase gate: the short
// bound belongs to the pre-write path only. A turn-start capture keeps the
// manager's own command deadline, so a workspace that legitimately takes longer
// than five seconds to enumerate does not lose the turn's baseline rollback
// point.
func TestTurnStartCaptureKeepsTheManagersDeadline(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{hash: "0123456789abcdef"}
	runner, _ := writeToolRunner(t, state, snap, &fakeSnapshotRecorder{})
	runner.SnapshotCaptureDeadline = 20 * time.Millisecond

	runner.snapshotCapture(context.Background(), SnapshotPhaseTurnStart, nil)
	if snap.hadDeadline {
		t.Fatalf("turn-start capture carried a deadline %s away; it must inherit the manager's",
			time.Until(snap.lastDeadline))
	}

	runner.snapshotCapture(context.Background(), SnapshotPhasePreWrite, nil)
	if !snap.hadDeadline {
		t.Fatal("pre-write capture carried no deadline; the short bound was not applied")
	}
	if remaining := time.Until(snap.lastDeadline); remaining > runner.SnapshotCaptureDeadline {
		t.Fatalf("pre-write deadline is %s away, want at most the injected %s",
			remaining, runner.SnapshotCaptureDeadline)
	}
}

// TestPreWriteCaptureSkipIsVisible is the regression the plan calls out: the
// old hook matched neither branch when Track returned ("", nil), so a skip was
// completely silent.
func TestPreWriteCaptureSkipIsVisible(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{hash: "", err: nil}
	rec := &fakeSnapshotRecorder{}
	runner, executed := writeToolRunner(t, state, snap, rec)

	if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if !*executed {
		t.Fatal("tool did not run after a skipped snapshot capture")
	}
	if rec.count() != 0 {
		t.Fatalf("recorded %d rows for a skipped capture, want 0", rec.count())
	}
	warnings := snapshotWarningTranscript(state)
	if len(warnings) != 1 {
		t.Fatalf("transcript has %d snapshot warnings for a skip, want 1", len(warnings))
	}
	if !strings.Contains(warnings[0], "capture_skipped") {
		t.Errorf("skip warning missing the capture_skipped reason:\n%s", warnings[0])
	}
	if !strings.Contains(warnings[0], "WITHOUT a rollback snapshot") {
		t.Errorf("skip warning must state the tool may proceed without a rollback snapshot:\n%s", warnings[0])
	}
}

// TestRepeatedIdenticalFailuresWarnOnce covers the anti-spam requirement.
func TestRepeatedIdenticalFailuresWarnOnce(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{err: errors.New("insufficient_free_space: not enough room")}
	rec := &fakeSnapshotRecorder{}
	runner, _ := writeToolRunner(t, state, snap, rec)

	for i := 0; i < 5; i++ {
		if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
			t.Fatalf("executeToolCall %d: %v", i, err)
		}
	}
	if snap.calls != 5 {
		t.Fatalf("Track called %d times, want 5 (every capture is attempted)", snap.calls)
	}
	if got := len(snapshotWarningTranscript(state)); got != 1 {
		t.Fatalf("transcript has %d warnings after 5 identical failures, want 1", got)
	}
}

// TestNewReasonWarnsAgain: a different reason is a meaningful status change.
func TestNewReasonWarnsAgain(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{err: errors.New("insufficient_free_space: no room")}
	rec := &fakeSnapshotRecorder{}
	runner, _ := writeToolRunner(t, state, snap, rec)

	if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if got := len(snapshotWarningTranscript(state)); got != 1 {
		t.Fatalf("warnings after first failure = %d, want 1", got)
	}

	snap.err = errors.New("legacy_recovery_required: old store must be recovered offline")
	if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	warnings := snapshotWarningTranscript(state)
	if len(warnings) != 2 {
		t.Fatalf("warnings after a new reason = %d, want 2", len(warnings))
	}
	if !strings.Contains(warnings[1], "legacy_recovery_required") ||
		!strings.Contains(warnings[1], "marshal snapshots migrate") {
		t.Errorf("new-reason warning lacks the legacy recovery wording:\n%s", warnings[1])
	}
}

// TestDifferentWorkspaceWarnsAgain: rebinding to a worktree makes the same
// failure a new event, because the user is now protecting a different tree.
func TestDifferentWorkspaceWarnsAgain(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{err: errors.New("budget_exhausted: over budget")}
	rec := &fakeSnapshotRecorder{}
	runner, _ := writeToolRunner(t, state, snap, rec)

	if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if got := len(snapshotWarningTranscript(state)); got != 1 {
		t.Fatalf("warnings = %d, want 1", got)
	}

	state.SetWorkspace(session.Workspace{
		ProjectRoot: state.WorkingDir,
		ActiveRoot:  state.WorkingDir + "/.marshal/worktrees/branch",
	})
	if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	warnings := snapshotWarningTranscript(state)
	if len(warnings) != 2 {
		t.Fatalf("warnings after a workspace change = %d, want 2", len(warnings))
	}
	if !strings.Contains(warnings[1], "worktrees/branch") {
		t.Errorf("new-workspace warning must name the worktree:\n%s", warnings[1])
	}
}

// TestSuccessfulCaptureResetsSuppression: rollback protection returning is a
// status change, so the NEXT failure must be visible again.
func TestSuccessfulCaptureResetsSuppression(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{err: errors.New("budget_exhausted: over budget")}
	rec := &fakeSnapshotRecorder{}
	runner, _ := writeToolRunner(t, state, snap, rec)

	if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if got := len(snapshotWarningTranscript(state)); got != 1 {
		t.Fatalf("warnings after first failure = %d, want 1", got)
	}

	// A successful capture resets suppression and writes exactly one row.
	snap.err = nil
	snap.hash = "abc123published"
	if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("recorded %d rows for a published capture, want 1", rec.count())
	}
	if rec.saved[0].hash != "abc123published" {
		t.Fatalf("recorded hash = %q, want the published hash", rec.saved[0].hash)
	}
	if got := len(snapshotWarningTranscript(state)); got != 1 {
		t.Fatalf("a successful capture added a warning; warnings = %d, want 1", got)
	}

	// Relapse of the SAME failure is visible again.
	snap.err = errors.New("budget_exhausted: over budget")
	snap.hash = ""
	if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if got := len(snapshotWarningTranscript(state)); got != 2 {
		t.Fatalf("warnings after a relapse = %d, want 2 (suppression must clear on success)", got)
	}
}

// TestCapturedButUnrecordedDistinguishesFromCaptureFailure pins requirement 3:
// a published capture whose DB row failed must NOT be reported as a missing
// snapshot.
func TestCapturedButUnrecordedDistinguishesFromCaptureFailure(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{hash: "deadbeefdeadbeefdeadbeef"}
	rec := &fakeSnapshotRecorder{err: errors.New("disk I/O error")}
	runner, executed := writeToolRunner(t, state, snap, rec)

	if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if !*executed {
		t.Fatal("tool did not run after a record failure")
	}
	warnings := snapshotWarningTranscript(state)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want 1: %v", len(warnings), warnings)
	}
	msg := warnings[0]
	if !strings.Contains(msg, "capture_unrecorded") {
		t.Errorf("warning must use the capture_unrecorded reason:\n%s", msg)
	}
	if !strings.Contains(msg, "A snapshot WAS captured") {
		t.Errorf("warning must not claim the snapshot is missing:\n%s", msg)
	}
	if strings.Contains(msg, "unavailable in") {
		t.Errorf("a recorded-failure warning must not say the snapshot is unavailable:\n%s", msg)
	}
	if !strings.Contains(msg, "deadbeefdead") {
		t.Errorf("warning should name the captured hash:\n%s", msg)
	}
}

// TestReadOnlyToolsDoNotCapture: read-only tools must stay snapshot-free, as
// before. Registering a read-only tool and calling it must not warn or record.
func TestReadOnlyToolsDoNotCapture(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{err: errors.New("budget_exhausted: over budget")}
	rec := &fakeSnapshotRecorder{}

	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:   "demo.read",
		Risk:   registry.RiskReadOnly,
		Schema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "read"}, nil
		},
	}); err != nil {
		t.Fatalf("register demo.read: %v", err)
	}
	runner := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.Snapshotter = snap
	runner.SnapshotRecorder = rec

	if _, err := runner.executeToolCall(context.Background(), ModelAction{
		Type: ActionToolCall,
		Tool: "demo.read",
		Args: json.RawMessage(`{"path":"x.go"}`),
	}); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if snap.calls != 0 {
		t.Fatalf("Track called %d times for a read-only tool, want 0", snap.calls)
	}
	if got := len(snapshotWarningTranscript(state)); got != 0 {
		t.Fatalf("read-only tool produced %d snapshot warnings, want 0", got)
	}
}

// TestCancelledCaptureIsNotRecoverySpam pins requirement 5: an expected
// shutdown is a debug log, never a user-visible "recover now".
func TestCancelledCaptureIsNotRecoverySpam(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{err: context.Canceled}
	rec := &fakeSnapshotRecorder{}
	runner, executed := writeToolRunner(t, state, snap, rec)

	if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if !*executed {
		t.Fatal("tool did not run after a cancelled capture")
	}
	if got := len(snapshotWarningTranscript(state)); got != 0 {
		t.Fatalf("a cancelled capture produced %d visible warnings, want 0: %v",
			got, snapshotWarningTranscript(state))
	}
	if rec.count() != 0 {
		t.Fatalf("cancelled capture recorded %d rows, want 0", rec.count())
	}
}

// TestContextCancellationErrorIsSuppressed covers the second cancellation
// spelling: Track returning the caller's ctx.Err().
func TestContextCancellationErrorIsSuppressed(t *testing.T) {
	state := newTestState(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snap := &fakeWriteSnapshotter{err: context.Canceled}
	rec := &fakeSnapshotRecorder{}
	runner, _ := writeToolRunner(t, state, snap, rec)

	if _, err := runner.executeToolCall(ctx, writeFileAction()); err != nil {
		// The tool itself may legitimately fail on a cancelled context; that is
		// not what this test is about.
		_ = err
	}
	if got := len(snapshotWarningTranscript(state)); got != 0 {
		t.Fatalf("cancelled context produced %d visible warnings, want 0", got)
	}
}

// TestTurnStartCaptureSkipIsVisible covers the second hook: a turn-start skip
// must warn, and must not write a DB row (turn-start never did).
func TestTurnStartCaptureSkipIsVisible(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{hash: "", err: nil}
	rec := &fakeSnapshotRecorder{}
	runner, _ := writeToolRunner(t, state, snap, rec)

	runner.snapshotCapture(context.Background(), SnapshotPhaseTurnStart, nil)
	if snap.calls != 1 {
		t.Fatalf("Track called %d times, want 1", snap.calls)
	}
	if rec.count() != 0 {
		t.Fatalf("turn-start capture recorded %d rows, want 0", rec.count())
	}
	warnings := snapshotWarningTranscript(state)
	if len(warnings) != 1 {
		t.Fatalf("turn-start skip produced %d warnings, want 1", len(warnings))
	}
	if !strings.Contains(warnings[0], "Turn-start snapshot") {
		t.Errorf("turn-start warning does not name its phase:\n%s", warnings[0])
	}
}

// TestTurnStartFailureIsVisibleAndDeduplicatesAcrossPhases checks that the
// turn-start and pre-write hooks share one suppression policy: the same
// workspace and reason is ONE user-visible event, not two.
func TestTurnStartFailureIsVisibleAndDeduplicatesAcrossPhases(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{err: errors.New("budget_exhausted: over budget")}
	rec := &fakeSnapshotRecorder{}
	runner, _ := writeToolRunner(t, state, snap, rec)

	runner.snapshotCapture(context.Background(), SnapshotPhaseTurnStart, nil)
	if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if got := len(snapshotWarningTranscript(state)); got != 1 {
		t.Fatalf("turn-start + pre-write same failure produced %d warnings, want 1", got)
	}
}

// TestSnapshotWarningNeverPanicsWithoutWiring guards the defensive paths: a
// runner with no Snapshotter, no warning state, and no session must be inert.
func TestSnapshotWarningNeverPanicsWithoutWiring(t *testing.T) {
	// No snapshotter at all.
	bare := &Runner{}
	bare.snapshotCapture(context.Background(), SnapshotPhaseTurnStart, nil)

	// A snapshotter but no session and no suppression state.
	bare2 := &Runner{Snapshotter: &fakeWriteSnapshotter{err: errors.New("boom")}}
	bare2.snapshotCapture(context.Background(), SnapshotPhasePreWrite, nil)

	// A state but a nil warning state.
	state := newTestState(t)
	bare3 := &Runner{State: state, snapshotWarnings: nil,
		Snapshotter: &fakeWriteSnapshotter{err: errors.New("boom")}}
	bare3.snapshotCapture(context.Background(), SnapshotPhaseTurnStart, nil)
	if got := len(snapshotWarningTranscript(state)); got != 1 {
		t.Fatalf("nil warning state produced %d warnings, want 1 (fail open, not silent)", got)
	}
}

// TestSnapshotWarningLogsEveryOccurrence pins the other half of the
// suppression rule: the transcript is deduplicated, the LOG is not.
func TestSnapshotWarningLogsEveryOccurrence(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{Logger: logger})

	snap := &fakeWriteSnapshotter{err: errors.New("budget_exhausted: over budget")}
	rec := &fakeSnapshotRecorder{}
	runner, _ := writeToolRunner(t, state, snap, rec)

	for i := 0; i < 3; i++ {
		if _, err := runner.executeToolCall(context.Background(), writeFileAction()); err != nil {
			t.Fatalf("executeToolCall %d: %v", i, err)
		}
	}
	if got := len(snapshotWarningTranscript(state)); got != 1 {
		t.Fatalf("transcript warnings = %d, want 1", got)
	}
	if got := strings.Count(buf.String(), "snapshot capture unavailable"); got != 3 {
		t.Fatalf("log entries = %d, want 3 (every occurrence is logged):\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "visible=false") {
		t.Errorf("suppressed occurrences should be logged as visible=false:\n%s", buf.String())
	}
}

// TestSnapshotWarningStateKeyIsWorkspaceReasonRecovery pins the suppression
// key directly, so a future edit cannot silently widen or narrow it.
func TestSnapshotWarningStateKeyIsWorkspaceReasonRecovery(t *testing.T) {
	state := newTestState(t)
	w := NewSnapshotWarningState()
	base := snapshotProblem{
		phase:     SnapshotPhasePreWrite,
		workspace: "/ws",
		reason:    reasonBudgetExhausted,
		detail:    snapshotReasonDetail(reasonBudgetExhausted),
		recovery:  snapshotRecovery(reasonBudgetExhausted),
	}

	w.report(state, base)
	w.report(state, base)
	if got := len(snapshotWarningTranscript(state)); got != 1 {
		t.Fatalf("identical problems produced %d warnings, want 1", got)
	}
	sameKey := "/ws\x00" + string(reasonBudgetExhausted) + "\x00" + base.recovery
	if w.shouldReport(sameKey) {
		t.Fatalf("identical (workspace, reason, recovery) must be suppressed; keys: %q vs %q",
			w.key, sameKey)
	}
	// Prove every component of the key is load bearing: perturbing any one of
	// them is a new event.
	w.report(state, base)
	if !w.shouldReport("/other-ws\x00" + string(reasonBudgetExhausted) + "\x00" + base.recovery) {
		t.Fatal("a different workspace must be visible")
	}
	w.report(state, base)
	if !w.shouldReport("/ws\x00" + string(reasonLegacyRecoveryRequired) + "\x00" + base.recovery) {
		t.Fatal("a different reason must be visible")
	}
	w.report(state, base)
	if !w.shouldReport(sameKey + "-changed-recovery") {
		t.Fatal("a different recovery command must be visible")
	}
}

// TestMaintenanceCancellationIsNotVisible covers the runtime-facing entry
// point: a shutdown cancellation logs at Debug and adds nothing.
func TestMaintenanceCancellationIsNotVisible(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{Logger: logger})
	w := NewSnapshotWarningState()

	w.ReportMaintenance(state, SnapshotPhaseMaintenance, context.Canceled, false)
	if got := len(snapshotWarningTranscript(state)); got != 0 {
		t.Fatalf("cancelled maintenance produced %d visible warnings, want 0", got)
	}
	if !strings.Contains(buf.String(), "snapshot maintenance cancelled") {
		t.Errorf("cancelled maintenance should still be logged at Debug:\n%s", buf.String())
	}

	// A real fault IS visible.
	w.ReportMaintenance(state, SnapshotPhaseMaintenance,
		fmt.Errorf("unreadable_file: legacy store unreadable"), false)
	if got := len(snapshotWarningTranscript(state)); got != 1 {
		t.Fatalf("real maintenance fault produced %d visible warnings, want 1", got)
	}
}

// TestStructuredReasonInterfaceIsPreferred pins the extraction mechanism the
// plan asked to be documented: a typed error that can name its reason is asked
// directly, without this package importing internal/snapshot.
func TestStructuredReasonInterfaceIsPreferred(t *testing.T) {
	if got := snapshotReasonFor(storeErrorStub{reason: "budget_exhausted"}); got != reasonBudgetExhausted {
		t.Fatalf("reason = %q, want budget_exhausted", got)
	}
	// A wrapped store error is found through the chain.
	wrapped := fmt.Errorf("pre-write snapshot: %w", storeErrorStub{reason: "legacy_recovery_required"})
	if got := snapshotReasonFor(wrapped); got != reasonLegacyRecoveryRequired {
		t.Fatalf("wrapped reason = %q, want legacy_recovery_required", got)
	}
	// The string form the store actually produces today is recognised too.
	if got := snapshotReasonFor(errors.New("insufficient_free_space workspace=abc: reserve too small")); got != reasonInsufficientFreeSpace {
		t.Fatalf("string-form reason = %q, want insufficient_free_space", got)
	}
	// An unrecognised error degrades to capture_failed, never to a guess.
	if got := snapshotReasonFor(errors.New("something odd happened")); got != reasonCaptureFailed {
		t.Fatalf("unknown error reason = %q, want capture_failed", got)
	}
}

// storeErrorStub is a stand-in for *snapshot.StoreError with the optional
// structural accessor attached.
type storeErrorStub struct{ reason string }

func (e storeErrorStub) Error() string { return e.reason }

func (e storeErrorStub) SnapshotReason() string { return e.reason }

// TestCommandTimeoutIsReportedAsATimeoutNotAStorageFault pins the wording of a
// capture that a time bound ended.
//
// It is its own kind of failure: nothing in the store is wrong, nothing in it
// needs repairing, and the capture can simply be retried. Reporting it as a
// generic capture failure sends the user to `marshal snapshots cleanup`, which
// cannot make a slow command fast and, worse, invites them to delete rollback
// history over a transient condition.
func TestCommandTimeoutIsReportedAsATimeoutNotAStorageFault(t *testing.T) {
	// The store prints its reason first; this is the exact shape a classified
	// managed-command timeout produces.
	err := errors.New("command_timeout path=/ws/.local/share/marshal/snapshots/v2: git ls-files timed out after 30s: context deadline exceeded")
	reason := snapshotReasonFor(err)
	if reason != reasonCommandTimeout {
		t.Fatalf("reason = %q, want command_timeout", reason)
	}
	detail := snapshotReasonDetail(reason)
	if !strings.Contains(detail, "time") {
		t.Errorf("detail does not say the capture timed out: %q", detail)
	}
	if strings.Contains(detail, "budget") || strings.Contains(detail, "free space") {
		t.Errorf("detail attributes a timeout to a storage limit: %q", detail)
	}
	recovery := snapshotRecovery(reason)
	if strings.Contains(recovery, "cleanup") || strings.Contains(recovery, "migrate") {
		t.Errorf("recovery sends the user to a storage repair for a timeout: %q", recovery)
	}
	if !strings.Contains(recovery, "status") {
		t.Errorf("recovery dropped the status command: %q", recovery)
	}
	// A timeout is not a limit failure, so no budget line is rendered.
	if line := snapshotLimitLine(nil, reason); line != "" {
		t.Errorf("a timeout rendered a limit line: %q", line)
	}
}

// TestSnapshotWarningRendersWorkspaceReasonLimitAndRecovery asserts the
// message contract itself, independent of the hook that emits it.
func TestSnapshotWarningRendersWorkspaceReasonLimitAndRecovery(t *testing.T) {
	state := newTestState(t)
	state.Config.Snapshots.WorkspaceMaxBytes = 1024 * 1024 * 512
	state.Config.Snapshots.GlobalMaxBytes = 1024 * 1024 * 1024 * 4

	msg := snapshotWarningMessage(snapshotProblem{
		phase:     SnapshotPhasePreWrite,
		workspace: "/ws/root",
		reason:    reasonBudgetExhausted,
		detail:    snapshotReasonDetail(reasonBudgetExhausted),
		limit:     snapshotLimitLine(state, reasonBudgetExhausted),
		recovery:  snapshotRecovery(reasonBudgetExhausted),
		err:       errors.New("budget_exhausted: over"),
	})
	for _, want := range []string{
		"Pre-write snapshot unavailable in /ws/root (budget_exhausted)",
		"WITHOUT a rollback snapshot",
		"Cause: budget_exhausted: over",
		"Limit: workspace budget 512.0 MB, global budget 4.0 GB",
		"Recover: marshal snapshots status, then marshal snapshots cleanup.",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
}

// TestSnapshotWarningUsesSessionConfigLimits pins that the numbers are read
// from the session's merged config, not hard-coded.
func TestSnapshotWarningUsesSessionConfigLimits(t *testing.T) {
	state := newTestState(t)
	state.Config.Snapshots.WorkspaceMaxBytes = 7 * 1024 * 1024
	line := snapshotLimitLine(state, reasonBudgetExhausted)
	if !strings.Contains(line, "7.0 MB") {
		t.Fatalf("limit line did not use the session config: %q", line)
	}
	// Free space is not a configured budget, and the wording must not pretend
	// it is one.
	fs := snapshotLimitLine(state, reasonInsufficientFreeSpace)
	if !strings.Contains(fs, "filesystem free space") || strings.Contains(fs, "Limit: workspace budget") {
		t.Fatalf("free-space limit line misstates the limit: %q", fs)
	}
}

// TestSnapshotWarningDoesNotAlterApprovalOrPolicy is a guardrail test: the
// approval state of a tool call is unchanged by a snapshot failure. The tool
// still executes, and no approval event is recorded as denied.
func TestSnapshotWarningDoesNotAlterApprovalOrPolicy(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{err: errors.New("budget_exhausted: over budget")}
	rec := &fakeSnapshotRecorder{}
	runner, executed := writeToolRunner(t, state, snap, rec)

	msgs, err := runner.executeToolCall(context.Background(), writeFileAction())
	if err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if !*executed {
		t.Fatal("tool did not run")
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "wrote") {
		t.Fatalf("tool result messages = %+v, want one successful tool result", msgs)
	}
	for _, ev := range state.AuditLog() {
		if ev.Approval == registry.ApprovalDenied {
			t.Fatalf("snapshot failure recorded a denied approval event: %+v", ev)
		}
	}
}

// TestSnapshotWarningViaRunTaskAlsoRunsTool drives the same property through
// the public turn loop, so the hook's placement inside executeToolCall is
// covered end-to-end rather than only via executeToolCall directly.
func TestSnapshotWarningViaRunTaskAlsoRunsTool(t *testing.T) {
	state := newTestState(t)
	snap := &fakeWriteSnapshotter{err: errors.New("budget_exhausted: over budget")}
	rec := &fakeSnapshotRecorder{}
	runner, executed := writeToolRunner(t, state, snap, rec)

	p := &agenttest.ScriptedProvider{ToolCalls: [][]schema.ToolCall{
		{{ID: "tc1", Name: "file.write", Args: json.RawMessage(`{"path":"target.go"}`)}},
		nil,
	}, Responses: []string{"", "done"}}
	runner.Provider = p
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	if _, err := runner.RunTask(context.Background(), "write the file"); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if !*executed {
		t.Fatal("tool did not run through the turn loop after a capture failure")
	}
	if got := len(snapshotWarningTranscript(state)); got != 1 {
		t.Fatalf("turn loop produced %d snapshot warnings, want 1: %v",
			got, snapshotWarningTranscript(state))
	}
}
