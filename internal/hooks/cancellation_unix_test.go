//go:build !windows

package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// hookBound is the upper bound asserted for a hook whose timeout_ms is 10.
// It is deliberately generous (roughly 500x the configured timeout) so a
// loaded CI machine cannot flake it, while staying far below the 30 s a
// descendant would otherwise hold the hook open for (AUD-05).
const hookBound = 5 * time.Second

// readPidFile polls for the pid file a hook writes right after
// backgrounding its descendant. The hook writes it within milliseconds of
// starting, well inside its timeout, but a slow machine can still race the
// first poll.
func readPidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if convErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant pid file %s was never written", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// processGone reports whether pid names a process that is no longer running.
//
// A plain kill(pid, 0) is not sufficient: a killed orphan stays in the
// process table as a zombie until its new parent reaps it, and kill(0)
// succeeds for a zombie. Under an init that does not reap orphans — the
// default in most containers, including CI — that would make this check
// fail on a descendant the hook correctly killed. A zombie is dead for our
// purposes, so it is excluded on Linux, where /proc exposes the state.
// Mirrors internal/sandbox/restricted_test.go's processAlive without
// importing it.
func processGone(pid int) bool {
	if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
		return true
	}
	if runtime.GOOS == "linux" {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return true
		}
		// Field 3 is the state code; it follows "(comm)", which may itself
		// contain spaces, so split after the final ')'.
		if idx := strings.LastIndex(string(stat), ")"); idx >= 0 {
			if fields := strings.Fields(string(stat)[idx+1:]); len(fields) > 0 && fields[0] == "Z" {
				return true
			}
		}
	}
	return false
}

// descendantHookScript backgrounds a long-lived descendant that inherits
// the hook's stdout — the thing that holds the pipe open — records its pid,
// and keeps the shell alive so there is a live group leader to kill.
func descendantHookScript(pidFile string) string {
	return fmt.Sprintf("#!/bin/sh\nsleep 30 &\necho $! > '%s'\nwait\n", pidFile)
}

// TestHookTimeoutBoundsDescendantHoldingStdout is the AUD-05 regression: a
// hook that exceeds its 10 ms timeout while a descendant inherits stdout
// must return promptly, not after the descendant's 30 s lifetime. The
// fail-open policy is unchanged, so the call still allows the tool.
func TestHookTimeoutBoundsDescendantHoldingStdout(t *testing.T) {
	pidFile := fmt.Sprintf("%s/descendant.pid", t.TempDir())
	script := writeHookScript(t, descendantHookScript(pidFile))
	r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "shell.run", Command: script, TimeoutMS: 10}}})

	start := time.Now()
	out, err := r.RunPreToolUse(context.Background(), PreToolUseInput{ToolName: "shell.run", Args: json.RawMessage(`{"command":"date"}`)})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("fail-open timeout returned error: %v", err)
	}
	if out.Decision != DecisionAllow || !out.FailedOpen {
		t.Fatalf("out = %+v, want allow + FailedOpen", out)
	}
	if elapsed >= hookBound {
		t.Fatalf("hook returned after %v, want < %v: the timeout did not bound the process tree", elapsed, hookBound)
	}
}

// TestHookTimeoutTerminatesDescendant proves the whole process tree is
// terminated, not just the immediate shell: after the bounded return, the
// backgrounded descendant must be gone rather than still running (or still
// holding the pipe). If it survived, the drain would have stayed blocked
// and the elapsed assertion above would already have failed.
func TestHookTimeoutTerminatesDescendant(t *testing.T) {
	if testing.Short() {
		t.Skip("process-group test requires real process execution")
	}
	pidFile := fmt.Sprintf("%s/descendant.pid", t.TempDir())
	script := writeHookScript(t, descendantHookScript(pidFile))
	r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "shell.run", Command: script, TimeoutMS: 10}}})

	start := time.Now()
	out, err := r.RunPreToolUse(context.Background(), PreToolUseInput{ToolName: "shell.run", Args: json.RawMessage(`{}`)})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("fail-open timeout returned error: %v", err)
	}
	if out.Decision != DecisionAllow || !out.FailedOpen {
		t.Fatalf("out = %+v, want allow + FailedOpen", out)
	}
	if elapsed >= hookBound {
		t.Fatalf("hook returned after %v, want < %v: a surviving descendant held it open", elapsed, hookBound)
	}

	pid := readPidFile(t, pidFile)
	// Reaping is asynchronous: the killed descendant is reparented and may
	// stay a zombie briefly. Poll for a bounded period instead of asserting
	// instantly.
	deadline := time.Now().Add(2 * time.Second)
	for !processGone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("descendant pid %d still alive after the hook returned: the process tree was not terminated", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestHookCancelBeforeStartFailsOpen pins cancellation before the hook has
// started: the call fails open promptly and the script never runs.
// exec.Cmd.Start checks ctx.Done() before os.StartProcess, so the process is
// never created.
func TestHookCancelBeforeStartFailsOpen(t *testing.T) {
	marker := fmt.Sprintf("%s/marker", t.TempDir())
	script := writeHookScript(t, fmt.Sprintf("#!/bin/sh\ntouch '%s'\n", marker))
	r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "shell.run", Command: script, TimeoutMS: 10}}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	out, err := r.RunPreToolUse(ctx, PreToolUseInput{ToolName: "shell.run", Args: json.RawMessage(`{}`)})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RunPreToolUse() error = %v", err)
	}
	if out.Decision != DecisionAllow || !out.FailedOpen {
		t.Fatalf("out = %+v, want allow + FailedOpen", out)
	}
	if elapsed >= time.Second {
		t.Fatalf("cancel before start returned after %v, want < 1s", elapsed)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("hook ran despite a context cancelled before it started")
	}
}
