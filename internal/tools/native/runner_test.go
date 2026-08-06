package native

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerBoundsAndObservesOutput(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runner := execRunner{}
	req := CommandRequest{
		Command:        "echo 'hello world' && echo 'error message' >&2",
		MaxOutputBytes: 8,
		Stdout:         &stdout,
		Stderr:         &stderr,
	}
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(result.Stdout) > 8 {
		t.Fatalf("Stdout len = %d, want <= 8", len(result.Stdout))
	}
	if !result.Meta.OutputTruncated {
		t.Fatal("OutputTruncated should be true")
	}
	if stdout.String() != result.Stdout {
		t.Fatalf("observer stdout = %q, want %q", stdout.String(), result.Stdout)
	}
	if stderr.String() != result.Stderr {
		t.Fatalf("observer stderr = %q, want %q", stderr.String(), result.Stderr)
	}
}

func TestExecRunnerCallsOnStartOnce(t *testing.T) {
	var callCount int
	var capturedPID int
	runner := execRunner{}
	req := CommandRequest{
		Command: "echo hello",
		OnStart: func(pid int) {
			callCount++
			capturedPID = pid
		},
	}
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("OnStart called %d times, want 1", callCount)
	}
	if capturedPID <= 0 {
		t.Fatalf("captured PID = %d, want > 0", capturedPID)
	}
	_ = result
}

func TestExecRunnerUsesOutputLimitDefault(t *testing.T) {
	runner := execRunner{}
	req := CommandRequest{
		Command: "echo hello",
	}
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(result.Stdout) == 0 {
		t.Fatal("Stdout should not be empty with default limit")
	}
}

func TestExecRunnerShellFreeCommand(t *testing.T) {
	runner := execRunner{}
	req := CommandRequest{
		Command: "echo hello world",
	}
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "hello world") {
		t.Errorf("stdout = %q, want %q", result.Stdout, "hello world")
	}
}

func TestExecRunnerWithShellMetacharacters(t *testing.T) {
	runner := execRunner{}
	req := CommandRequest{
		Command: "echo hello | grep hello",
	}
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "hello") {
		t.Errorf("stdout = %q, want %q", result.Stdout, "hello")
	}
}

func TestExecRunnerDestructiveGoesThroughShell(t *testing.T) {
	runner := execRunner{}
	// rm -rf on a non-existent path is harmless but is classified as
	// RiskDestructive and should still be routed through /bin/sh -lc.
	req := CommandRequest{
		Command: "rm -rf /tmp/__marshal_test_nonexistent__",
	}
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
}

// TestExecRunnerEnforcesTimeout: req.Timeout must bound execution on the
// default runner too, not just the sandbox runners — otherwise shell.run's
// timeout_seconds is silently ignored.
func TestExecRunnerEnforcesTimeout(t *testing.T) {
	runner := execRunner{}
	req := CommandRequest{
		Command: "sleep 30",
		Timeout: 50 * time.Millisecond,
	}
	start := time.Now()
	_, err := runner.Run(context.Background(), req)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a timeout error from a killed sleep, got nil")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("command ran %v — timeout not enforced", elapsed)
	}
}

// TestExecRunnerPipelineFailureExitCode: a command failing early in a
// pipeline (e.g. `go vet ./... 2>&1 | head -50`) must surface a non-zero
// exit code — reporting head's success hides real build/vet failures from
// the agent (session postmortem 2026-08-06, finding 2).
func TestExecRunnerPipelineFailureExitCode(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available; pipefail path untestable")
	}
	runner := execRunner{}
	req := CommandRequest{
		Command: "ls /nonexistent_dir_marshal_test 2>&1 | head -1",
	}
	result, _ := runner.Run(context.Background(), req)
	if result.ExitCode == 0 {
		t.Fatal("pipeline with failing first command reported exit code 0")
	}
}
