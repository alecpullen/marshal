package sandbox

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"

	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
)

func TestExecuteCommandBoundedOutput(t *testing.T) {
	// Emit 64 bytes per stream with an 8-byte limit.
	var stdoutObs, stderrObs bytes.Buffer

	cmd := exec.Command("/bin/sh", "-lc",
		`echo 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' && `+
			`echo 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' >&2`)

	ctx := context.Background()
	req := native.CommandRequest{
		MaxOutputBytes: 8,
		Stdout:         &stdoutObs,
		Stderr:         &stderrObs,
	}

	result, err := executeCommand(ctx, cmd, req, registry.SandboxMeta{})
	if err != nil {
		t.Fatalf("executeCommand: %v", err)
	}
	if len(result.Stdout) > 8 {
		t.Fatalf("stdout len = %d, want <= 8", len(result.Stdout))
	}
	if len(result.Stderr) > 8 {
		t.Fatalf("stderr len = %d, want <= 8", len(result.Stderr))
	}
	if !result.Meta.OutputTruncated {
		t.Fatal("OutputTruncated should be true")
	}
	// Observers must also be bounded to 8 bytes.
	if stdoutObs.Len() > 8 {
		t.Fatalf("stdout observer len = %d, want <= 8", stdoutObs.Len())
	}
	if stderrObs.Len() > 8 {
		t.Fatalf("stderr observer len = %d, want <= 8", stderrObs.Len())
	}
}

func TestExecuteCommandCallsOnStartOnce(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-lc", "echo hello")

	var calls int
	var calledPid int

	ctx := context.Background()
	req := native.CommandRequest{
		OnStart: func(pid int) {
			calls++
			calledPid = pid
		},
	}

	_, err := executeCommand(ctx, cmd, req, registry.SandboxMeta{})
	if err != nil {
		t.Fatalf("executeCommand: %v", err)
	}
	if calls != 1 {
		t.Fatalf("OnStart called %d times, want 1", calls)
	}
	if calledPid <= 0 {
		t.Fatalf("OnStart called with invalid pid %d", calledPid)
	}
}

func TestExecuteCommandDurationSet(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-lc", "echo hello")

	ctx := context.Background()
	req := native.CommandRequest{}

	result, err := executeCommand(ctx, cmd, req, registry.SandboxMeta{})
	if err != nil {
		t.Fatalf("executeCommand: %v", err)
	}
	if result.Meta.DurationMS <= 0 {
		t.Fatalf("DurationMS = %d, want > 0", result.Meta.DurationMS)
	}
}

func TestExecuteCommandExitCode(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-lc", "exit 42")
	ctx := context.Background()
	result, err := executeCommand(ctx, cmd, native.CommandRequest{}, registry.SandboxMeta{})
	// err is non-nil for non-zero exit code from Wait.
	if err == nil {
		t.Fatal("expected error for exit 42")
	}
	if result.ExitCode != 42 {
		t.Fatalf("ExitCode = %d, want 42", result.ExitCode)
	}
}

func TestExecuteCommandCancellationReason(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-lc", "sleep 30")

	ctx, cancel := context.WithCancel(context.Background())
	req := native.CommandRequest{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	result, err := executeCommand(ctx, cmd, req, registry.SandboxMeta{})
	<-done

	if err == nil {
		t.Fatal("expected error from cancelled command")
	}
	if result.Meta.KilledReason != "cancelled" {
		t.Fatalf("KilledReason = %q, want %q", result.Meta.KilledReason, "cancelled")
	}
}

func TestExecuteCommandTimeoutReason(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-lc", "sleep 30")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req := native.CommandRequest{}

	result, err := executeCommand(ctx, cmd, req, registry.SandboxMeta{})
	if err == nil {
		t.Fatal("expected error from timed out command")
	}
	if result.Meta.KilledReason != "timeout" {
		t.Fatalf("KilledReason = %q, want %q", result.Meta.KilledReason, "timeout")
	}
}
