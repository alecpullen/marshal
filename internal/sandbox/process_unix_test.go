//go:build !windows

package sandbox

import (
	"os"
	"os/exec"
	"syscall"
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

// TestTerminateProcessTree_KillsChild tests that a child process which
// ignores SIGTERM is still killed after the grace interval by the SIGKILL
// fallback (either via PGID kill or direct PID kill).
func TestTerminateProcessTree_KillsChild(t *testing.T) {
	// Start a child that traps SIGTERM (ignores it) and runs indefinitely.
	// The SIGTERM from terminateProcessTree will be ignored, forcing the
	// function to escalate to SIGKILL after the grace interval.
	cmd := exec.Command("/bin/sh", "-c",
		`trap "" TERM; while true; do sleep 1; done`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Ensure cleanup even if the test logic below fails.
	defer func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cmd.Wait()
	}()

	// Let the process start its signal handler.
	time.Sleep(50 * time.Millisecond)

	// Call terminateProcessTree with a short grace so the test doesn't
	// take long. The child traps SIGTERM so the grace will expire and
	// SIGKILL will be sent.
	err := terminateProcessTree(cmd, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("terminateProcessTree: %v", err)
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
		t.Fatal("process not reaped within 3s after terminateProcessTree")
	}
}

// TestTerminateProcessTree_NonExistentPID verifies that an error is
// returned when the underlying syscall fails with something other
// than ESRCH. This exercises the error-propagation path through the
// final SIGKILL step.
//
// Note: on most Unix systems, os.FindProcess never fails, so we
// construct a cmd with a deliberately large PID that corresponds to
// no real process. The PGID kill returns ESRCH (which is swallowed),
// and the direct-PID kill in Step 4 returns ESRCH as well, so the
// final result is nil. This test documents that behaviour.
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
