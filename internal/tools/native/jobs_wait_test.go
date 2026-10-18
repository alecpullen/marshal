package native

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/progress"
	"marshal/internal/tools/registry"
)

// waitTestRunner finishes jobs after a configurable delay so Wait tests get
// deterministic running→terminal transitions (fakeRunner returns
// DeadlineExceeded when req.Timeout is unset, which always yields a failed
// job).
type waitTestRunner struct{ delay time.Duration }

func (r *waitTestRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	select {
	case <-time.After(r.delay):
		return CommandResult{ExitCode: 0, Stdout: "done"}, nil
	case <-ctx.Done():
		return CommandResult{}, ctx.Err()
	}
}

// blockingTestRunner streams a marker, then blocks until its release channel
// closes or its context is cancelled. It records the process through OnStart so
// an accepted stop is evidence-backed, and reports a terminated process (no
// natural exit code) once cancelled. It models a long foreground command whose
// outcome the service owns.
type blockingTestRunner struct {
	release chan struct{}
	live    string
}

func (r *blockingTestRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	if req.OnStart != nil {
		req.OnStart(7001)
	}
	// Output the command produced before it was interrupted is part of the
	// result, so it must reach the observer the way it would live.
	if r.live != "" && req.Stdout != nil {
		_, _ = io.WriteString(req.Stdout, r.live)
	}
	select {
	case <-r.release:
		return CommandResult{ExitCode: 0}, nil
	case <-ctx.Done():
		return CommandResult{ExitCode: -1}, nil
	}
}

// TestShellSteeringWaitReturnsActualStoppedOutput pins the wait an interrupted
// shell.run call uses. The wait must be tied to the turn, not to the operation
// context a steering interruption cancels, so when the service's stop settles
// the process the handler reports that real outcome — the actual killed status,
// the steering stop cause and the output captured before the process died —
// rather than mislabelling the command as a detached job that is still running.
func TestShellSteeringWaitReturnsActualStoppedOutput(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	runner := &blockingTestRunner{
		release: make(chan struct{}),
		live:    "partial output\n",
	}
	manager := NewCommandService(context.Background(), LaunchPolicy{
		Runner:         runner,
		Dir:            root,
		MaxOutputBytes: 1 << 20,
	})
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(shutCtx)
	})
	manager.stopSettleTimeout = 5 * time.Second
	manager.SetOnChange(state.SetRunningJobsCount)

	// The interruption reaches the command service exactly as a conversation's
	// wiring does: the operation handler stops what is still attached to it.
	stopped := make(chan struct{}, 1)
	opCtx, turn, ref := steeringToolContext(t, state, func(ctx context.Context, ref session.OperationRef) (session.OperationStop, error) {
		select {
		case stopped <- struct{}{}:
		default:
		}
		return manager.InterruptOperation(ctx, ref)
	})

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  runner,
		SessionState:   state,
		JobManager:     manager,
		Guardrail:      func(string, bool) error { return nil },
		MaxOutputBytes: 1 << 20,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, ok := reg.Lookup("shell.run")
	if !ok {
		t.Fatal("shell.run not registered")
	}

	// Launch the command with a long wait: it stays attached and running until
	// the interruption stops it.
	done := make(chan toolOutcome, 1)
	go func() {
		res, err := tool.Handler(opCtx, registry.ToolCall{ID: "call-shell", Name: "shell.run",
			Args: []byte(`{"command":"sleep 30","timeout_seconds":300}`)})
		done <- toolOutcome{res: res, err: err}
	}()

	// The command is really running before the interruption is submitted.
	deadline := time.Now().Add(5 * time.Second)
	for manager.ActiveCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("shell.run never launched its command")
		}
		time.Sleep(2 * time.Millisecond)
	}
	steerNow(t, state, turn, "r1")
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the interruption never reached the command service")
	}

	out := awaitOutcome(t, done)
	if out.err == nil {
		t.Fatal("an interrupted command was reported as a successful result")
	}
	if strings.Contains(out.res.Content, "still running in the background") ||
		strings.Contains(out.res.Summary, "still running in the background") {
		t.Fatalf("an interrupted command was mislabelled as detached work: %+v", out.res)
	}
	if !strings.Contains(out.res.Summary, "stopped") {
		t.Fatalf("summary = %q, want the command's actual stopped outcome", out.res.Summary)
	}
	if !strings.Contains(out.res.Content, "partial output") {
		t.Fatalf("content = %q, want the output captured before the process died", out.res.Content)
	}
	if out.res.CommandExitCode == nil || *out.res.CommandExitCode != -1 {
		t.Fatalf("exit code = %v, want the observed -1", out.res.CommandExitCode)
	}

	// The execution settled with the steering stop cause and is nobody's
	// outstanding background work.
	executions := manager.List()
	if len(executions) != 1 {
		t.Fatalf("executions = %+v, want exactly the one command", executions)
	}
	if executions[0].Outcome != OutcomeStopped || executions[0].StopCause != steeringStopCause {
		t.Fatalf("settled execution = %q/%q, want stopped with the %q cause [op %s]",
			executions[0].Outcome, executions[0].StopCause, steeringStopCause, ref.ID)
	}
	if got := manager.Outstanding(); len(got) != 0 {
		t.Fatalf("Outstanding = %+v, want none: the stopped command is not background work", got)
	}
	select {
	case <-runner.release:
		t.Fatal("the runner was released: the command was not actually stopped")
	default:
	}
}

func TestJobManagerWaitUnknownID(t *testing.T) {
	m := NewJobManager(context.Background(), &waitTestRunner{}, t.TempDir(), 5, time.Hour, 1<<20)
	_, err := m.Wait(context.Background(), "job-999")
	if err == nil || !strings.Contains(err.Error(), "unknown job") {
		t.Fatalf("Wait unknown = %v, want unknown-job error", err)
	}
}

func TestJobManagerWaitReturnsTerminalJobImmediately(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &waitTestRunner{}, t.TempDir(), 5, time.Hour, 1<<20)
	id, err := launchBackground(ctx, m, "true")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	info, err := m.Wait(ctx, id)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if info.Status != StatusCompleted || info.ExitCode == nil || *info.ExitCode != 0 {
		t.Fatalf("Wait = %+v, want completed/exit 0", info)
	}
}

func TestJobManagerWaitBlocksUntilDone(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &waitTestRunner{delay: 150 * time.Millisecond}, t.TempDir(), 5, time.Hour, 1<<20)
	id, err := launchBackground(ctx, m, "slow")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	start := time.Now()
	info, err := m.Wait(ctx, id)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("Wait returned after %s; it did not block", elapsed)
	}
	if info.Status != StatusCompleted {
		t.Fatalf("status = %s, want completed", info.Status)
	}
}

// TestJobManagerWaitHonorsContextCancel pins the non-destructive wait contract:
// an ending context reports the context error and leaves the process running.
func TestJobManagerWaitHonorsContextCancel(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &waitTestRunner{delay: time.Hour}, t.TempDir(), 5, time.Hour, 1<<20)
	id, err := launchBackground(ctx, m, "hang")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := m.Wait(waitCtx, id); err == nil || err != context.DeadlineExceeded {
		t.Fatalf("Wait = %v, want DeadlineExceeded", err)
	}
	// The wait ended; the command did not.
	info, _, err := m.Output(id, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if info.Status != StatusRunning {
		t.Fatalf("status after a cancelled Wait = %q, want running", info.Status)
	}
	if info.StopCause != "" {
		t.Fatalf("StopCause = %q, want none: a cancelled wait must not stop the command", info.StopCause)
	}
}

func TestJobManagerWaitAfterKill(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &waitTestRunner{delay: time.Hour}, t.TempDir(), 5, time.Hour, 1<<20)
	id, err := launchBackground(ctx, m, "killme")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Kill(id); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	info, err := m.Wait(ctx, id)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if info.Status != StatusKilled {
		t.Fatalf("status = %s, want killed", info.Status)
	}
	if info.Outcome != OutcomeStopped {
		t.Fatalf("outcome = %s, want stopped", info.Outcome)
	}
}

// TestJobManagerOutstandingListsOnlyDetachedRunning pins what the await scan
// sees: detached running/stopping work only. An attached foreground command is
// not background work, and a settled job is not outstanding.
func TestJobManagerOutstandingListsOnlyDetachedRunning(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &waitTestRunner{delay: time.Hour}, t.TempDir(), 5, time.Hour, 1<<20)
	idA, err := launchBackground(ctx, m, "a")
	if err != nil {
		t.Fatalf("Start a: %v", err)
	}
	if _, err := launchBackground(ctx, m, "b"); err != nil {
		t.Fatalf("Start b: %v", err)
	}
	if got := len(m.Outstanding()); got != 2 {
		t.Fatalf("Outstanding = %d, want 2", got)
	}
	if err := m.Kill(idA); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if got := len(m.Outstanding()); got != 1 {
		t.Fatalf("Outstanding after kill = %d, want 1", got)
	}
	out := m.Outstanding()
	if out[0].Command != "b" {
		t.Fatalf("Outstanding[0] = %s, want job b", out[0].Command)
	}
}

// TestJobOutstandingExcludesAttachedCommands pins the attachment split: the
// authoritative snapshot counts both attachments, while the await/background
// count sees only detached work.
func TestJobOutstandingExcludesAttachedCommands(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &waitTestRunner{delay: time.Hour}, t.TempDir(), 5, time.Hour, 1<<20)

	attached, err := m.Launch(ctx, LaunchSpec{Command: "attached"})
	if err != nil {
		t.Fatalf("Launch attached: %v", err)
	}
	if _, err := m.Launch(ctx, LaunchSpec{Command: "detached", Detached: true}); err != nil {
		t.Fatalf("Launch detached: %v", err)
	}

	if got := len(m.Outstanding()); got != 1 {
		t.Fatalf("Outstanding = %d, want 1 (the detached command only)", got)
	}
	if got := m.ActiveCount(); got != 2 {
		t.Fatalf("ActiveCount = %d, want 2 (both attachments)", got)
	}
	if got := len(m.ActiveExecutions()); got != 2 {
		t.Fatalf("ActiveExecutions = %d, want 2", got)
	}

	// After the handoff both are detached work.
	if _, err := m.WaitForeground(ctx, attached.ID, 0); err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if got := len(m.Outstanding()); got != 2 {
		t.Fatalf("Outstanding after handoff = %d, want 2", got)
	}
}

// TestJobWaitForegroundExpiryLeavesProcessRunning pins the central handoff
// contract: the expired wait returns continuation metadata, the process keeps
// running with the same identity and PID, and it runs exactly once.
func TestJobWaitForegroundExpiryLeavesProcessRunning(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &waitTestRunner{delay: time.Hour}, t.TempDir(), 5, time.Hour, 1<<20)

	info, err := m.Launch(ctx, LaunchSpec{Command: "long", Origin: CommandOrigin{TurnID: "turn-1"}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	res, err := m.WaitForeground(ctx, info.ID, 40*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if res.Terminal {
		t.Fatalf("result = %+v, want a continuation", res)
	}
	if res.WaitEnd != WaitEndExpired {
		t.Fatalf("WaitEnd = %q, want expired", res.WaitEnd)
	}
	if res.Info.ID != info.ID {
		t.Fatalf("identity = %q, want the same %q", res.Info.ID, info.ID)
	}
	if res.Info.Status != StatusRunning {
		t.Fatalf("status = %q, want running: an expired wait must not end the process", res.Info.Status)
	}
	if res.RunError != nil {
		t.Fatalf("RunError = %v, want nil: a wait expiry is not a failed command", res.RunError)
	}
	if res.Info.ExitCode != nil {
		t.Fatalf("ExitCode = %v, want none for a continuation", res.Info.ExitCode)
	}

	// The command itself is still there, with no stop recorded.
	alive, _, err := m.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if alive.Status != StatusRunning || alive.StopCause != "" {
		t.Fatalf("live = %q / %q, want running with no stop cause", alive.Status, alive.StopCause)
	}
}

// TestJobWaitForegroundSettlesWhileAttached pins the terminal shape of a
// foreground wait: the actual exit code and captured output come back as a
// terminal result, not as a handle.
func TestJobWaitForegroundSettlesWhileAttached(t *testing.T) {
	ctx := context.Background()
	m := NewJobManager(ctx, &waitTestRunner{delay: 20 * time.Millisecond}, t.TempDir(), 5, time.Hour, 1<<20)

	info, err := m.Launch(ctx, LaunchSpec{Command: "quick"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	res, err := m.WaitForeground(ctx, info.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if !res.Terminal {
		t.Fatalf("result = %+v, want a terminal result", res)
	}
	if res.Info.Status != StatusCompleted || res.Info.Outcome != OutcomeCompleted {
		t.Fatalf("info = %q/%q, want completed/completed", res.Info.Status, res.Info.Outcome)
	}
	if res.Info.ExitCode == nil || *res.Info.ExitCode != 0 {
		t.Fatalf("ExitCode = %v, want 0", res.Info.ExitCode)
	}
	if !strings.Contains(res.Output, "done") {
		t.Fatalf("Output = %q, want the captured stdout", res.Output)
	}
	// A command that settles while still attached stays attached: the caller
	// waited for it, so there is nothing to detach.
	if res.Info.Attachment != AttachmentForeground {
		t.Fatalf("attachment = %q, want foreground for a completed wait", res.Info.Attachment)
	}
}

// TestCommandSnapshotGatesInFlightSettlementAndRetainsSaveError pins the settling
// boundary: while a detached completion's first durable-result sink attempt is in
// flight, the source snapshot names the execution as settling and reports no
// save error yet; once the attempt has returned having failed, the settling flag
// is cleared and the bounded error is reported by execution identity, with the
// completion retained as pending rather than lost.
func TestCommandSnapshotGatesInFlightSettlementAndRetainsSaveError(t *testing.T) {
	ctx := context.Background()
	runner := &waitTestRunner{delay: 10 * time.Millisecond}
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	entered := make(chan struct{})
	releaseSink := make(chan struct{})
	saveErr := errors.New("store unavailable")
	var once sync.Once
	jm.SetOnExit(func(info JobInfo, ran time.Duration, output string) error {
		once.Do(func() { close(entered) })
		<-releaseSink
		return saveErr
	})

	info, err := jm.Launch(ctx, LaunchSpec{Command: "build", Detached: true, Progress: eventsProgressConfig()})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the exit sink was never offered the completion")
	}

	// The sink is in flight: the terminal fact is held back, and no failure has
	// been recorded yet.
	inFlight := jm.Snapshot()
	if !slices.Contains(inFlight.Settling, info.ExecutionID) {
		t.Fatalf("Settling = %v, want the in-flight execution %q", inFlight.Settling, info.ExecutionID)
	}
	if len(inFlight.SaveErrors) != 0 {
		t.Fatalf("SaveErrors = %v before the sink returned, want none", inFlight.SaveErrors)
	}

	close(releaseSink)

	// The attempt failed: settling clears and the bounded error is retained.
	deadline := time.Now().Add(3 * time.Second)
	var after JobSnapshot
	for {
		after = jm.Snapshot()
		if len(after.Settling) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Settling never cleared after the sink returned: %v", after.Settling)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := after.SaveErrors[info.ExecutionID]; !strings.Contains(got, "store unavailable") {
		t.Fatalf("SaveErrors = %v, want the recorded failure for %q", after.SaveErrors, info.ExecutionID)
	}
	if pending := jm.PendingSettlements(); len(pending) != 1 || pending[0].Info.ID != info.ID {
		t.Fatalf("PendingSettlements = %+v, want the failed completion retained", pending)
	}
}

// TestCommandProgressCompletionStopRaceKeepsActualOutcome pins that a stop racing
// a completion cannot corrupt the result: whichever the manager's lock arbitrates
// first wins, the reported outcome is truthful for that ordering, exactly one
// detached completion is reported, and the progress the process wrote is retained
// in every case.
func TestCommandProgressCompletionStopRaceKeepsActualOutcome(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	final := append(progressLine(t, 3, 12, floatPtr(12)), '\n')
	runner := &progressStreamRunner{
		pid:     9207,
		started: started,
		release: release,
		writes:  []progressWrite{{stream: progress.Stdout, data: final}},
	}
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	jm.stopSettleTimeout = 2 * time.Second

	var (
		mu          sync.Mutex
		completions int
	)
	jm.SetOnExit(func(info JobInfo, ran time.Duration, output string) error {
		mu.Lock()
		completions++
		mu.Unlock()
		return nil
	})

	info, err := jm.Launch(ctx, LaunchSpec{
		Command:  "build",
		Detached: true,
		Progress: eventsProgressConfig(),
		Origin:   CommandOrigin{TurnID: "turn-1"},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-started

	// Release the stop and the process together: the manager's lock decides which
	// wins, and the result must be truthful either way.
	start := make(chan struct{})
	stopDone := make(chan error, 1)
	go func() {
		<-start
		stopDone <- jm.RequestStop(info.ID, "user stopped it")
	}()
	go func() {
		<-start
		close(release)
	}()
	close(start)

	if err := <-stopDone; err != nil {
		t.Fatalf("RequestStop: %v", err)
	}

	settled, err := jm.Wait(ctx, info.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !settled.Status.Terminal() {
		t.Fatalf("settled status = %q, want a terminal state", settled.Status)
	}
	switch settled.Outcome {
	case OutcomeStopped:
		if settled.Status != StatusKilled {
			t.Fatalf("stopped outcome carried status %q, want killed", settled.Status)
		}
		if settled.StopCause == "" || settled.StopRequestedAt.IsZero() {
			t.Fatalf("stopped evidence = %q/%v, want the accepted request recorded", settled.StopCause, settled.StopRequestedAt)
		}
	case OutcomeCompleted:
		if settled.Status != StatusCompleted {
			t.Fatalf("completed outcome carried status %q, want completed", settled.Status)
		}
	default:
		t.Fatalf("outcome = %q, want the actual completed or stopped outcome", settled.Outcome)
	}
	if settled.StopCause != "" && settled.StopRequestedAt.IsZero() {
		t.Fatalf("StopCause = %q with no request time: evidence must be complete", settled.StopCause)
	}
	if settled.Progress == nil || settled.Progress.Sequence != 3 {
		t.Fatalf("final progress = %#v, want the last record the process wrote", settled.Progress)
	}

	// Exactly one detached completion is reported, whichever way the race fell.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := completions
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	n := completions
	mu.Unlock()
	if n != 1 {
		t.Fatalf("completions = %d, want exactly one for the settled detached execution", n)
	}
}
