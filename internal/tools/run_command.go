// Package tools implements the tool-use executor interface for Marshal.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/alec/marshal/internal/sandbox"
)

// RunCommandInput is the JSON input schema for the run_command tool.
type RunCommandInput struct {
	Command string `json:"command"`             // Shell command to execute
	Timeout int    `json:"timeout,omitempty"`   // Timeout in seconds (default: 30)
	WorkDir string `json:"work_dir,omitempty"`  // Working directory (relative to repo root)
}

// RunCommandResult is the JSON output from the run_command tool.
type RunCommandResult struct {
	Command  string `json:"command"`              // Executed command
	ExitCode int    `json:"exit_code"`            // Exit code (0 = success)
	Stdout   string `json:"stdout"`               // Standard output
	Stderr   string `json:"stderr"`               // Standard error
	Duration int    `json:"duration_ms"`          // Execution time in milliseconds
	Error    string `json:"error,omitempty"`      // Error message if any
}

// DefaultTimeout is the default command timeout in seconds.
const DefaultTimeout = 30

// MaxOutputSize is the maximum bytes to capture from stdout/stderr.
const MaxOutputSize = 64 * 1024 // 64KB

// RunCommand executes a shell command within the repository with sandboxing.
// It validates the command against an allowlist, sets a timeout, and captures output.
func RunCommand(ctx context.Context, sb *sandbox.Sandbox, input json.RawMessage) (*RunCommandResult, error) {
	var params RunCommandInput
	if err := json.Unmarshal(input, &params); err != nil {
		return &RunCommandResult{
			Error: fmt.Sprintf("invalid input: %v", err),
		}, nil
	}

	// Validate command is provided
	if strings.TrimSpace(params.Command) == "" {
		return &RunCommandResult{
			Error: "command is required",
		}, nil
	}

	// Validate command against allowlist
	if err := sb.ValidateCommand(params.Command); err != nil {
		return &RunCommandResult{
			Command: params.Command,
			Error:   err.Error(),
		}, nil
	}

	// Determine working directory
	workDir := sb.Config().RepoRoot
	if params.WorkDir != "" {
		// Validate work_dir is within repo
		if err := sb.ValidatePath(params.WorkDir); err != nil {
			return &RunCommandResult{
				Command: params.Command,
				Error:   fmt.Sprintf("invalid work_dir: %v", err),
			}, nil
		}
		workDir = filepath.Join(sb.Config().RepoRoot, filepath.Clean(params.WorkDir))
	}

	// Set timeout
	timeout := params.Timeout
	if timeout <= 0 || timeout > 300 { // Max 5 minutes
		timeout = DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// Execute command
	start := time.Now()
	cmd := exec.CommandContext(ctx, "sh", "-c", params.Command)
	cmd.Dir = workDir
	cmd.Env = os.Environ()

	// Capture output with size limits
	stdout := &limitedWriter{limit: MaxOutputSize}
	stderr := &limitedWriter{limit: MaxOutputSize}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	duration := time.Since(start).Milliseconds()

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			exitCode = -1
			return &RunCommandResult{
				Command:  params.Command,
				ExitCode: exitCode,
				Stdout:   stdout.String(),
				Stderr:   stderr.String(),
				Duration: int(duration),
				Error:    fmt.Sprintf("command timed out after %d seconds", timeout),
			}, nil
		} else {
			return &RunCommandResult{
				Command:  params.Command,
				ExitCode: -1,
				Stdout:   stdout.String(),
				Stderr:   stderr.String(),
				Duration: int(duration),
				Error:    fmt.Sprintf("execution failed: %v", runErr),
			}, nil
		}
	}

	return &RunCommandResult{
		Command:  params.Command,
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: int(duration),
	}, nil
}

// limitedWriter captures output up to a limit and truncates the rest.
type limitedWriter struct {
	buf   strings.Builder
	limit int
	total int
}

func (w *limitedWriter) Write(p []byte) (n int, err error) {
	w.total += len(p)
	if w.buf.Len() >= w.limit {
		return len(p), nil // Silently drop excess
	}

	remaining := w.limit - w.buf.Len()
	if len(p) > remaining {
		w.buf.Write(p[:remaining])
		w.buf.WriteString("\n[output truncated...]")
	} else {
		w.buf.Write(p)
	}
	return len(p), nil
}

func (w *limitedWriter) String() string {
	return w.buf.String()
}

// IsCommandAllowed checks if a command is in the allowlist.
func IsCommandAllowed(sb *sandbox.Sandbox, command string) bool {
	return sb.IsCommandAllowed(command)
}

// ValidateCommand validates a command against the allowlist.
func ValidateCommand(sb *sandbox.Sandbox, command string) error {
	return sb.ValidateCommand(command)
}
