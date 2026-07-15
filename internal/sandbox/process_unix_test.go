//go:build !windows

package sandbox

import (
	"os"
	"os/exec"
	"testing"
	"time"
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
