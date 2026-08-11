package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"marshal/internal/tools/native"
)

// backendCases returns the restricted and passthrough backends for
// table-driven contract tests.
func backendCases(t *testing.T) []struct {
	name string
	sb   Sandbox
} {
	t.Helper()
	return []struct {
		name string
		sb   Sandbox
	}{
		{"restricted", newTestSandbox(t, Config{Backend: "restricted"})},
		{"passthrough", newTestSandbox(t, Config{Backend: "passthrough", UnsafePassthrough: true})},
	}
}

func TestSandboxRunCallsOnStartOnce(t *testing.T) {
	for _, bc := range backendCases(t) {
		t.Run(bc.name, func(t *testing.T) {
			dir := t.TempDir()
			var calls []int
			res, err := bc.sb.Run(context.Background(), native.CommandRequest{
				Command: "echo hello",
				Dir:     dir,
				Timeout: 5 * time.Second,
				OnStart: func(pid int) {
					calls = append(calls, pid)
				},
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(calls) != 1 {
				t.Fatalf("OnStart called %d times, want 1", len(calls))
			}
			if calls[0] <= 0 {
				t.Fatalf("OnStart pid=%d, want >0", calls[0])
			}
			// Verify the PID in the callback matches the actual process.
			if res.ExitCode != 0 {
				t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
			}
		})
	}
}

func TestSandboxRunBoundsResultAndObserver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell echo differs on windows")
	}
	for _, bc := range backendCases(t) {
		t.Run(bc.name, func(t *testing.T) {
			dir := t.TempDir()
			var stdoutObs, stderrObs bytes.Buffer

			res, err := bc.sb.Run(context.Background(), native.CommandRequest{
				Command:        `echo 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' && echo 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' >&2`,
				Dir:            dir,
				Timeout:        5 * time.Second,
				MaxOutputBytes: 8,
				Stdout:         &stdoutObs,
				Stderr:         &stderrObs,
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			// Both result strings must be at most 8 bytes.
			if len(res.Stdout) > 8 {
				t.Fatalf("result stdout len = %d, want <= 8", len(res.Stdout))
			}
			if len(res.Stderr) > 8 {
				t.Fatalf("result stderr len = %d, want <= 8", len(res.Stderr))
			}
			if !res.Meta.OutputTruncated {
				t.Fatal("OutputTruncated should be true")
			}
			// Observers must also be bounded.
			if stdoutObs.Len() > 8 {
				t.Fatalf("stdout observer len = %d, want <= 8", stdoutObs.Len())
			}
			if stderrObs.Len() > 8 {
				t.Fatalf("stderr observer len = %d, want <= 8", stderrObs.Len())
			}
		})
	}
}

func TestSandboxCancellationReason(t *testing.T) {
	for _, bc := range backendCases(t) {
		t.Run(bc.name, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())

			type result struct {
				res native.CommandResult
				err error
			}
			ch := make(chan result, 1)
			go func() {
				res, err := bc.sb.Run(ctx, native.CommandRequest{
					Command: "sleep 30",
					Dir:     dir,
					// No Timeout — we cancel via context.
				})
				ch <- result{res, err}
			}()

			// Cancel after a short delay so the command starts.
			time.Sleep(100 * time.Millisecond)
			cancel()

			r := <-ch
			if r.err == nil {
				t.Fatal("expected error from cancelled command")
			}
			if r.res.Meta.KilledReason != "cancelled" {
				t.Fatalf("KilledReason = %q, want %q", r.res.Meta.KilledReason, "cancelled")
			}
		})
	}
}

func TestSandboxTimeoutReason(t *testing.T) {
	for _, bc := range backendCases(t) {
		t.Run(bc.name, func(t *testing.T) {
			dir := t.TempDir()
			res, err := bc.sb.Run(context.Background(), native.CommandRequest{
				Command: "sleep 30",
				Dir:     dir,
				Timeout: 100 * time.Millisecond,
			})
			if err == nil {
				t.Fatal("expected error from timed out command")
			}
			if res.Meta.KilledReason != "timeout" {
				t.Fatalf("KilledReason = %q, want %q", res.Meta.KilledReason, "timeout")
			}
		})
	}
}

func TestSandboxCancellationKillsProcessGroupAfterGrace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group kill is unix-only")
	}

	pidDir := t.TempDir()
	pidFile := filepath.Join(pidDir, "pids")
	// The command starts a child that ignores SIGTERM and captures
	// both shell PID and child PID to a file. Both survive the
	// initial SIGTERM, forcing the grace-period wait before SIGKILL.
	cmd := `trap '' TERM; echo $$ > ` + pidFile + `; (trap '' TERM; while true; do :; done) & echo $! >> ` + pidFile + `; wait`

	sb := newTestSandbox(t, Config{Backend: "restricted"})
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel only once both PIDs have been written, which proves the TERM
	// traps are installed. A fixed sleep raced `bash -lc` sourcing the login
	// profile: when SIGTERM landed before the trap was in place the shell
	// died on the spot, the process group vanished long before the grace
	// deadline, and the assertion below failed intermittently.
	started := make(chan time.Time, 1)
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			data, err := os.ReadFile(pidFile)
			if err == nil && len(strings.Fields(string(data))) >= 2 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		// Give the shell a moment to reach `wait` so the trap is armed.
		time.Sleep(50 * time.Millisecond)
		started <- time.Now()
		cancel()
	}()

	_, err := sb.Run(ctx, native.CommandRequest{
		Command: cmd,
		Dir:     t.TempDir(),
	})
	// Measure the grace window from the cancel, not from process start:
	// shell startup cost is not part of the grace period.
	elapsed := time.Since(<-started)

	// Must have waited at least the grace period (the child ignores
	// TERM so SIGKILL only fires after the full grace window).
	if elapsed < terminationGrace-200*time.Millisecond {
		t.Fatalf("Run returned in %v, expected at least ~%v (grace period)", elapsed, terminationGrace)
	}
	// Upper bound: should not hang indefinitely.
	if elapsed > terminationGrace+3*time.Second {
		t.Fatalf("Run took %v, expected at most ~%v", elapsed, terminationGrace+3*time.Second)
	}

	// Read PIDs and verify both processes are gone.
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 PIDs, got %d", len(lines))
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(line, "%d", &pid); err != nil {
			t.Fatalf("parse pid %q: %v", line, err)
		}
		if alive, detail := processAlive(pid); alive {
			t.Fatalf("process %d still exists (%s)", pid, detail)
		}
	}
}

// processAlive reports whether pid names a process that is still running.
//
// A plain kill(pid, 0) is not sufficient: a killed orphan stays in the
// process table as a zombie until its new parent reaps it, and kill(0)
// succeeds for a zombie. Under an init that does not reap orphans — the
// default in most containers, including CI — that made this check fail on
// processes the sandbox had correctly killed. A zombie is dead for our
// purposes, so it is excluded on Linux, where /proc exposes the state.
func processAlive(pid int) (bool, string) {
	if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
		return false, "gone"
	}
	if runtime.GOOS == "linux" {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return false, "gone"
		}
		// Field 3 is the state code; it follows "(comm)", which may itself
		// contain spaces, so split after the final ')'.
		if idx := strings.LastIndex(string(stat), ")"); idx >= 0 {
			if fields := strings.Fields(string(stat)[idx+1:]); len(fields) > 0 && fields[0] == "Z" {
				return false, "zombie"
			}
		}
	}
	return true, "kill 0 succeeded"
}
