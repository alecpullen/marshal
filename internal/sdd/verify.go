package sdd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CommandRunner is the seam for running build/lint/test commands with a
// context (for timeout). The real impl shells out; the fake returns canned
// results. P3 unit tests use the fake; the real commands are exercised by
// P5's integration test.
type CommandRunner interface {
	Run(ctx context.Context, dir, command string, args ...string) (string, error)
}

// CLICommandRunner shells out via exec.CommandContext.
type CLICommandRunner struct{}

func (CLICommandRunner) Run(ctx context.Context, dir, command string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// FakeCommandRunner is an in-memory CommandRunner for tests. SetResult makes
// the next Run of that command (joined args) return (output, nil); SetError
// makes it return (output, error). Unset commands return ("", nil).
type FakeCommandRunner struct {
	results map[string]string
	errs    map[string]error
	outputs map[string]string
}

func NewFakeCommandRunner() *FakeCommandRunner {
	return &FakeCommandRunner{
		results: map[string]string{},
		errs:    map[string]error{},
		outputs: map[string]string{},
	}
}

func keyFor(command string, args []string) string {
	return command + " " + strings.Join(args, " ")
}

// SetResult makes Run(command, args...) return (output, nil).
func (f *FakeCommandRunner) SetResult(joinedKey, output string) {
	f.results[joinedKey] = output
}

// SetError makes Run(command, args...) return (output, err).
func (f *FakeCommandRunner) SetError(joinedKey string, errStr, output string) {
	f.errs[joinedKey] = fmt.Errorf("%s", errStr)
	f.outputs[joinedKey] = output
}

func (f *FakeCommandRunner) Run(ctx context.Context, dir, command string, args ...string) (string, error) {
	key := keyFor(command, args)
	if err, ok := f.errs[key]; ok {
		return f.outputs[key], err
	}
	return f.results[key], nil
}

// VerifyTask runs build, vet, and test in worktreeDir with a timeout of
// timeoutMS milliseconds (0 = no timeout). The allowed-files check is a
// separate concern (the caller runs AllowedFilesCheck first and
// short-circuits); VerifyTask focuses on the build/lint/test trio.
func VerifyTask(ctx context.Context, runner CommandRunner, worktreeDir string, timeoutMS int) VerifyResult {
	if timeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
		defer cancel()
	}
	var combined strings.Builder
	steps := []struct {
		name string
		args []string
	}{
		{"build", []string{"./..."}},
		{"vet", []string{"./..."}},
		{"test", []string{"./..."}},
	}
	for _, s := range steps {
		out, err := runner.Run(ctx, worktreeDir, "go", append([]string{s.name}, s.args...)...)
		combined.WriteString(out)
		if err != nil {
			return VerifyResult{Pass: false, Event: "VERIFY_FAIL", Output: combined.String()}
		}
	}
	return VerifyResult{Pass: true, Event: "VERIFY_PASS", Output: combined.String()}
}
