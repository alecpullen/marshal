package native

import (
	"context"
	"strings"
	"testing"
	"time"
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
	id, err := m.Start(ctx, "true", 0)
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
	id, err := m.Start(ctx, "slow", 0)
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
	id, err := m.Start(ctx, "hang", 0)
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
	id, err := m.Start(ctx, "killme", 0)
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
	idA, err := m.Start(ctx, "a", 0)
	if err != nil {
		t.Fatalf("Start a: %v", err)
	}
	if _, err := m.Start(ctx, "b", 0); err != nil {
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
