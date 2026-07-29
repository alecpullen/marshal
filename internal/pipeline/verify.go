package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/google/shlex"
)

// CommandRunner is the seam for running verify commands. The real
// implementation shells out; tests use FakeCommandRunner.
type CommandRunner interface {
	// Run executes a shell-style command string in dir and returns its
	// combined output. A non-zero exit is returned as an error along with
	// whatever output was produced.
	Run(ctx context.Context, dir, command string) (string, error)
}

// CLICommandRunner executes commands with exec.CommandContext after
// splitting the command string with shlex. No shell is involved, so
// pipelines and redirection in a configured command will not work — the
// command is an argv, not a script.
type CLICommandRunner struct{}

func (CLICommandRunner) Run(ctx context.Context, dir, command string) (string, error) {
	argv, err := shlex.Split(command)
	if err != nil || len(argv) == 0 {
		return "", fmt.Errorf("pipeline verify: cannot parse command %q: %w", command, err)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	return out.String(), err
}

// VerifyResult is the outcome of one gate run. Skipped means no build or
// test command was available: the gate did not pass, it did not run, and
// the caller must say so rather than treat it as a pass.
type VerifyResult struct {
	Skipped       bool
	OK            bool
	FailedCommand string
	Output        string
}

// Verifier runs the build and test commands that gate every task. An empty
// Build or Test string skips that half.
type Verifier struct {
	Build   string
	Test    string
	Timeout time.Duration
	Runner  CommandRunner
}

// Run executes build then test in dir. A failing command stops the gate —
// there is no point running tests against a tree that does not compile. A
// command failure is reported in the result, not as an error; an error
// return means the gate itself could not run.
func (v Verifier) Run(ctx context.Context, dir string) (VerifyResult, error) {
	commands := make([]string, 0, 2)
	for _, c := range []string{v.Build, v.Test} {
		if c != "" {
			commands = append(commands, c)
		}
	}
	if len(commands) == 0 {
		return VerifyResult{Skipped: true, OK: true}, nil
	}
	if v.Runner == nil {
		return VerifyResult{}, fmt.Errorf("pipeline verify: no CommandRunner configured")
	}
	runCtx := ctx
	if v.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, v.Timeout)
		defer cancel()
	}
	for _, c := range commands {
		out, err := v.Runner.Run(runCtx, dir, c)
		if err != nil {
			return VerifyResult{OK: false, FailedCommand: c, Output: out}, nil
		}
	}
	return VerifyResult{OK: true}, nil
}

// DefaultVerifier resolves the gate commands: configured values win, and
// an unconfigured Go repository (one with a go.mod at its root) falls back
// to the standard Go commands. Anything else yields empty commands, which
// Run reports as Skipped.
func DefaultVerifier(repoRoot, build, test string, timeout time.Duration) Verifier {
	v := Verifier{Build: build, Test: test, Timeout: timeout, Runner: CLICommandRunner{}}
	if v.Build == "" && v.Test == "" {
		if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err == nil {
			v.Build = "go build ./..."
			v.Test = "go test ./..."
		}
	}
	return v
}

// FakeCommandRunner is the in-memory CommandRunner used by tests. Calls
// records every command in order.
type FakeCommandRunner struct {
	Calls   []string
	outputs map[string]string
	errs    map[string]error
}

func NewFakeCommandRunner() *FakeCommandRunner {
	return &FakeCommandRunner{outputs: map[string]string{}, errs: map[string]error{}}
}

// SetOutput makes the next run of command succeed with the given output.
func (f *FakeCommandRunner) SetOutput(command, output string) {
	f.outputs[command] = output
}

// SetError makes the next run of command fail with the given output and error.
func (f *FakeCommandRunner) SetError(command, output string, err error) {
	f.outputs[command] = output
	f.errs[command] = err
}

func (f *FakeCommandRunner) Run(ctx context.Context, dir, command string) (string, error) {
	f.Calls = append(f.Calls, command)
	return f.outputs[command], f.errs[command]
}
