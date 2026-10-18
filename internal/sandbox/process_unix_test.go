//go:build !windows

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"marshal/internal/tools/native"
)

// TestTerminateProcessTree_NilProcess verifies that a nil cmd.Process
// is handled gracefully (returns nil without any syscall).
func TestTerminateProcessTree_NilProcess(t *testing.T) {
	cmd := &exec.Cmd{Process: nil}
	if err := terminateProcessTree(cmd, time.Second); err != nil {
		t.Errorf("nil process: %v", err)
	}
}

// TestTerminateProcessTree_AlreadyExited verifies that calling
// terminateProcessTree on an already-exited process returns nil.
// The ESRCH returned by syscall.Kill is treated as success.
func TestTerminateProcessTree_AlreadyExited(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}

	// Process is reaped; syscall.Kill returns ESRCH on any signal sent
	// to the now-nonexistent process group. terminateProcessTree should
	// treat this as success.
	if err := terminateProcessTree(cmd, time.Millisecond); err != nil {
		t.Errorf("exited process: %v", err)
	}
}

// TestKillDirectPID_KillsRealProcess verifies that killDirectPID
// terminates a real process and that the process is reaped successfully.
// This directly exercises the Step 4 fallback logic — sending SIGKILL
// to a single PID rather than a process group.
func TestKillDirectPID_KillsRealProcess(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 300")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Cleanup in case the test logic below fails.
	defer cmd.Process.Kill()

	// Kill via the direct-PID helper — this is the Step 4 logic.
	if err := killDirectPID(cmd.Process.Pid); err != nil {
		t.Fatalf("killDirectPID: %v", err)
	}

	// cmd.Wait should return promptly because the process was killed.
	waitDone := make(chan struct{})
	go func() {
		cmd.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		// Good — process was reaped.
	case <-time.After(3 * time.Second):
		t.Fatal("process not reaped within 3s after killDirectPID")
	}
}

// TestKillDirectPID_NonExistentPID verifies that killDirectPID returns
// nil when the target PID does not exist (ESRCH is treated as success).
func TestKillDirectPID_NonExistentPID(t *testing.T) {
	if err := killDirectPID(2147483647); err != nil {
		t.Errorf("non-existent PID: %v", err)
	}
}

// TestTerminateProcessTree_NonExistentPID verifies that
// terminateProcessTree returns nil when the target PID does not exist.
// All syscall.Kill calls (PGID SIGTERM, PGID poll, PGID SIGKILL, and
// direct-PID SIGKILL via killDirectPID) return ESRCH, which the
// function treats as success (process already gone). This documents
// that non-existence is handled gracefully and never surfaces as an
// error to the caller.
func TestTerminateProcessTree_NonExistentPID(t *testing.T) {
	// A PID that is extremely unlikely to exist in any real process
	// table. os.FindProcess on Unix always succeeds, so we can build
	// an *exec.Cmd with this PID.
	proc, err := os.FindProcess(2147483647) // max int32-ish
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	cmd := &exec.Cmd{Process: proc}

	// The PGID -2147483647 and PID 2147483647 almost certainly don't
	// exist, so all syscall.Kill attempts return ESRCH. The function
	// should return nil (ESRCH treated as success).
	if err := terminateProcessTree(cmd, time.Millisecond); err != nil {
		t.Errorf("non-existent PID: %v", err)
	}
}

// ── Real-process managed execution: the composition the user sees ───────
//
// The tests below run actual processes through the same managed-execution path
// the product uses (the conversation's command service over a real sandbox
// backend), so handoff, late output, settlement and an explicit tree stop are
// proven against real processes rather than a fake runner. Synchronization is
// by temp-file barriers and bounded polls, never by long sleeps.

// pidRecordingRunner delegates to a real sandbox backend and records how many
// executions actually reached it and which PIDs were published, so "one launch,
// one process" is checked against the runner rather than inferred.
type pidRecordingRunner struct {
	inner native.CommandRunner
	runs  atomic.Int32
	mu    sync.Mutex
	pids  []int
}

func (r *pidRecordingRunner) Run(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
	r.runs.Add(1)
	prev := req.OnStart
	req.OnStart = func(pid int) {
		r.mu.Lock()
		r.pids = append(r.pids, pid)
		r.mu.Unlock()
		if prev != nil {
			prev(pid)
		}
	}
	return r.inner.Run(ctx, req)
}

func (r *pidRecordingRunner) runCount() int { return int(r.runs.Load()) }

func (r *pidRecordingRunner) recordedPIDs() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.pids...)
}

// newManagedRealProcessService wires the managed-execution service onto a real
// sandbox backend, which is the production composition: the manager owns
// admission, attachment and settlement while the backend owns the process.
func newManagedRealProcessService(t *testing.T, sb Sandbox, dir string, maxOutputBytes int) (*native.JobManager, *pidRecordingRunner) {
	t.Helper()
	runner := &pidRecordingRunner{inner: sb}
	jm := native.NewCommandService(context.Background(), native.LaunchPolicy{
		Runner:         runner,
		Dir:            dir,
		MaxOutputBytes: maxOutputBytes,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = jm.Shutdown(ctx)
	})
	return jm, runner
}

// unixTestQuote single-quotes a path for the shell the sandbox command runs in.
func unixTestQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// waitForJobOutput polls until the execution's visible output contains want,
// failing the test on timeout. It is how these tests synchronize on a real
// process having reached a step, instead of sleeping a guessed interval.
func waitForJobOutput(t *testing.T, jm *native.JobManager, id, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for {
		_, output, err := jm.Output(id, 0)
		if err != nil {
			t.Fatalf("Output(%s): %v", id, err)
		}
		last = output
		if strings.Contains(output, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s output never contained %q within %v; last output %q", id, want, timeout, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForFileContent polls until path exists with non-empty content and returns
// it, failing the test on timeout.
func waitForFileContent(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			return strings.TrimSpace(string(data))
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s never appeared with content within %v", path, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForProcessGone polls until pid no longer exists, failing the test on
// timeout. The stop path escalates from SIGTERM to SIGKILL within its grace
// period, so a bounded poll is the deterministic way to observe it.
func waitForProcessGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d was still alive %v after its tree was stopped", pid, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestManagedRealProcessHandsOffKeepsItsLateTailAndSettlesOnce is Task 12's
// real-process case: one process is launched through the managed path, the
// foreground wait expires while it is still working (barrier-file
// synchronization, no sleeping on the test's side), and the command then writes
// a late marker past the output cap before exiting naturally.
//
// It asserts the user-visible contract end to end: exactly one launch and one
// PID for the whole life of the command, the handoff is a continuation rather
// than a failure, the retained output is a RECENT tail (the late marker is
// present, the initial one has been dropped), and the command settles once with
// its own exit code.
func TestManagedRealProcessHandsOffKeepsItsLateTailAndSettlesOnce(t *testing.T) {
	dir := t.TempDir()
	barrier := filepath.Join(dir, "release-barrier")
	// 4096 bytes of filler sits between the two markers, far beyond the 512-byte
	// cap below, so a prefix capture could never reach the late marker.
	script := strings.Join([]string{
		"printf 'initial-marker\\n'",
		"n=0",
		"while [ ! -f " + unixTestQuote(barrier) + " ] && [ \"$n\" -lt 1000 ]; do sleep 0.01; n=$((n+1)); done",
		"head -c 4096 /dev/zero | tr '\\000' 'a'",
		"printf '\\nlate-marker\\n'",
	}, "\n")

	const cap = 512
	jm, runner := newManagedRealProcessService(t, newTestSandbox(t, Config{Backend: "restricted"}), dir, cap)
	ctx := context.Background()

	info, err := jm.Launch(ctx, native.LaunchSpec{
		Command: script,
		Origin:  native.CommandOrigin{ConversationID: "conv-real", Actor: "parent", TurnID: "turn-real"},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	// The process is up and has already produced its first output before the
	// wait starts, so a handoff here is genuinely "still working", not "not
	// started yet".
	waitForJobOutput(t, jm, info.ID, "initial-marker", 10*time.Second)

	res, err := jm.WaitForeground(ctx, info.ID, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if res.Terminal {
		t.Fatalf("handoff result = %+v, want a continuation while the command still works", res)
	}
	if res.WaitEnd != native.WaitEndExpired {
		t.Fatalf("WaitEnd = %q, want expired", res.WaitEnd)
	}
	if res.Info.Attachment != native.AttachmentBackground {
		t.Fatalf("attachment after the handoff = %q, want background", res.Info.Attachment)
	}
	if res.RunError != nil || res.Info.ExitCode != nil {
		t.Fatalf("handoff = %+v, want no exit and no error: a wait expiry is not a failed command", res)
	}
	if !strings.Contains(res.Output, "initial-marker") {
		t.Fatalf("handoff output = %q, want the output written before the wait ended", res.Output)
	}

	// Release the barrier: the command writes its late marker and exits.
	if err := os.WriteFile(barrier, []byte("go\n"), 0o644); err != nil {
		t.Fatalf("write barrier: %v", err)
	}
	settleCtx, cancelSettle := context.WithTimeout(ctx, 20*time.Second)
	defer cancelSettle()
	final, err := jm.Wait(settleCtx, info.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if final.Status != native.StatusCompleted || final.Outcome != native.OutcomeCompleted {
		t.Fatalf("settled = %q/%q, want completed/completed", final.Status, final.Outcome)
	}
	if final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("ExitCode = %v, want the process's own 0", final.ExitCode)
	}
	if !final.OutputTruncated {
		t.Fatal("OutputTruncated = false, want true: the command wrote far past the cap")
	}
	if !strings.Contains(final.OutputTail, "late-marker") {
		t.Fatalf("retained tail = %q, want the late marker written after the handoff", final.OutputTail)
	}
	if strings.Contains(final.OutputTail, "initial-marker") {
		t.Fatalf("retained tail = %q, want a RECENT tail: the initial marker should have been dropped past the cap", final.OutputTail)
	}

	// One launch, one PID, for the whole life of the command — including across
	// the handoff.
	if got := runner.runCount(); got != 1 {
		t.Fatalf("runner runs = %d, want exactly 1 (a handoff must not relaunch)", got)
	}
	pids := runner.recordedPIDs()
	if len(pids) != 1 {
		t.Fatalf("published PIDs = %v, want exactly one process", pids)
	}
	if pids[0] != final.PID || final.PID <= 0 {
		t.Fatalf("published PID %d does not match the settled PID %d", pids[0], final.PID)
	}
	if final.ExecutionID != info.ExecutionID {
		t.Fatalf("execution identity changed across the handoff: %q, want %q", final.ExecutionID, info.ExecutionID)
	}
	waitForProcessGone(t, final.PID, 5*time.Second)
}

// TestManagedRealProcessTreeStopCollectsFinalOutputAndKillsTheTree is the
// second real-process case: a command that starts a grandchild is stopped
// explicitly, and the whole process tree is gone afterwards while the output
// the command produced — including the line its SIGTERM trap wrote on the way
// out — is still available.
func TestManagedRealProcessTreeStopCollectsFinalOutputAndKillsTheTree(t *testing.T) {
	dir := t.TempDir()
	grandchildPIDFile := filepath.Join(dir, "grandchild.pid")
	script := strings.Join([]string{
		// The trap is installed before any work, so the SIGTERM the stop
		// escalates from (and the SIGKILL it falls back to) is delivered to a
		// shell that can report what it was doing.
		"trap 'printf \"final-line\\n\"; exit 143' TERM",
		"printf 'tree-start\\n'",
		"( while :; do sleep 0.05; done ) &",
		"echo $! > " + unixTestQuote(grandchildPIDFile),
		"wait",
	}, "\n")

	jm, runner := newManagedRealProcessService(t, newTestSandbox(t, Config{Backend: "restricted"}), dir, 4096)
	ctx := context.Background()

	info, err := jm.Launch(ctx, native.LaunchSpec{
		Command: script,
		Origin:  native.CommandOrigin{ConversationID: "conv-tree", Actor: "parent", TurnID: "turn-tree"},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	waitForJobOutput(t, jm, info.ID, "tree-start", 10*time.Second)
	grandchildPID := 0
	if _, err := fmt.Sscanf(waitForFileContent(t, grandchildPIDFile, 10*time.Second), "%d", &grandchildPID); err != nil || grandchildPID <= 0 {
		t.Fatalf("grandchild pid file = %v (pid %d), want a live child pid", err, grandchildPID)
	}

	// An explicit stop request on the named execution, exactly as agent.kill or
	// a turn stop delivers one.
	if err := jm.RequestStop(info.ID, "user stopped the turn"); err != nil {
		t.Fatalf("RequestStop: %v", err)
	}
	stopping, _, err := jm.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output after the stop request: %v", err)
	}
	if stopping.Status != native.StatusStopping {
		t.Fatalf("status after the stop request = %q, want stopping", stopping.Status)
	}

	settleCtx, cancelSettle := context.WithTimeout(ctx, 20*time.Second)
	defer cancelSettle()
	final, err := jm.Wait(settleCtx, info.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	// The stop owns the outcome, and the cause is recorded as evidence.
	if final.Outcome != native.OutcomeStopped {
		t.Fatalf("outcome = %q, want stopped", final.Outcome)
	}
	if final.StopCause != "user stopped the turn" {
		t.Fatalf("StopCause = %q, want the explicit stop's cause", final.StopCause)
	}
	if final.Status.Terminal() == false {
		t.Fatalf("status = %q, want a settled (terminal) state", final.Status)
	}
	// The output the command produced is still available after the stop,
	// including the line its own SIGTERM trap wrote while terminating.
	if !strings.Contains(final.OutputTail, "tree-start") {
		t.Fatalf("retained tail = %q, want the output written before the stop", final.OutputTail)
	}
	if !strings.Contains(final.OutputTail, "final-line") {
		t.Fatalf("retained tail = %q, want the final line the command wrote while terminating", final.OutputTail)
	}

	// The whole tree is gone: not just the direct child, but the grandchild the
	// command started.
	if got := runner.runCount(); got != 1 {
		t.Fatalf("runner runs = %d, want exactly 1", got)
	}
	pids := runner.recordedPIDs()
	if len(pids) != 1 || pids[0] != final.PID {
		t.Fatalf("published PIDs = %v, want the settled process %d", pids, final.PID)
	}
	waitForProcessGone(t, final.PID, 5*time.Second)
	waitForProcessGone(t, grandchildPID, 5*time.Second)
}
