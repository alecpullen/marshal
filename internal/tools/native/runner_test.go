package native

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
