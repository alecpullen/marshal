//go:build !windows

package sandbox

import (
	"os/exec"
	"testing"
)

// TestShellCommandPipelineFailureExitCode: the restricted backend's shell
// wrapper must report a non-zero exit code when a command early in a
// pipeline fails, not the last stage's success (session postmortem
// 2026-08-06, finding 2).
func TestShellCommandPipelineFailureExitCode(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available; pipefail path untestable")
	}
	cmd := shellCommand("exit 3 | head -1")
	if err := cmd.Run(); err == nil {
		t.Fatal("pipeline with failing first command exited 0")
	}
	if cmd.ProcessState.ExitCode() != 3 {
		t.Fatalf("exit code = %d, want 3", cmd.ProcessState.ExitCode())
	}
}
