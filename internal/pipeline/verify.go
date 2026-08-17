package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/shlex"

	"marshal/internal/repo"
)

// CommandRunner is the seam for running verify commands. The real
// implementation shells out; tests use FakeCommandRunner.
type CommandRunner interface {
	// Run executes argv in dir and returns its combined output. A non-zero
	// exit is returned as an error along with whatever output was produced.
	Run(ctx context.Context, dir string, argv []string) (string, error)
}

// CLICommandRunner executes commands with exec.CommandContext. No shell is
// involved, so pipelines and redirection in a configured command will not
// work — the command is an argv, not a script.
type CLICommandRunner struct{}

func (CLICommandRunner) Run(ctx context.Context, dir string, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("pipeline verify: empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
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

// withTimeout returns a context bounded by the verifier's timeout. When
// Timeout is zero, the context is cancellable but not time-bounded.
func (v Verifier) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if v.Timeout > 0 {
		return context.WithTimeout(ctx, v.Timeout)
	}
	return context.WithCancel(ctx)
}

// Run executes build then test in dir. A failing command stops the gate —
// there is no point running tests against a tree that does not compile. A
// command failure is reported in the result, not as an error; an error
// return means the gate itself could not run.
func (v Verifier) Run(ctx context.Context, dir string) (VerifyResult, error) {
	commands := make([][]string, 0, 2)
	for _, c := range []string{v.Build, v.Test} {
		if c != "" {
			argv, err := shlex.Split(c)
			if err != nil || len(argv) == 0 {
				return VerifyResult{}, fmt.Errorf("pipeline verify: cannot parse command %q: %w", c, err)
			}
			commands = append(commands, argv)
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
	for _, argv := range commands {
		out, err := v.Runner.Run(runCtx, dir, argv)
		if err != nil {
			return VerifyResult{OK: false, FailedCommand: strings.Join(argv, " "), Output: out}, nil
		}
	}
	return VerifyResult{OK: true}, nil
}

// ResolveResult is the outcome of resolving gate commands for a repo.
type ResolveResult struct {
	Build string
	Test  string
	// Known is true when at least one command was confidently resolved.
	Known bool
}

// ResolveVerifyCommands resolves the build/test commands for a repo:
// configured values win, then manifest detection, else unknown.
func ResolveVerifyCommands(repoRoot, configuredBuild, configuredTest string) ResolveResult {
	if configuredBuild != "" || configuredTest != "" {
		return ResolveResult{Build: configuredBuild, Test: configuredTest, Known: true}
	}
	m, found := repo.DetectManifest(repoRoot)
	if !found {
		// Manifest-less: no command is confidently known, even if a dominant
		// language exists, because e.g. `go build ./...` needs go.mod. Prompt.
		return ResolveResult{}
	}
	switch m.Language {
	case "go":
		return ResolveResult{Build: "go build ./...", Test: "go test ./...", Known: true}
	case "rust":
		return ResolveResult{Build: "cargo build", Test: "cargo test", Known: true}
	case "java":
		return ResolveResult{Build: "mvn compile", Test: "mvn test", Known: true}
	case "node":
		return resolveNodeManifest(repoRoot)
	case "python":
		return resolvePythonManifest(repoRoot)
	}
	return ResolveResult{}
}

// resolveNodeManifest sets commands only for scripts that exist in package.json.
func resolveNodeManifest(repoRoot string) ResolveResult {
	data, err := os.ReadFile(filepath.Join(repoRoot, "package.json"))
	if err != nil {
		return ResolveResult{}
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return ResolveResult{}
	}
	res := ResolveResult{}
	if _, ok := pkg.Scripts["build"]; ok {
		res.Build = "npm run build"
	}
	if _, ok := pkg.Scripts["test"]; ok {
		res.Test = "npm test"
	}
	res.Known = res.Build != "" || res.Test != ""
	return res
}

// resolvePythonManifest sets test only when pytest is present; build is not
// confidently known from pyproject.toml, so test-only or unknown.
func resolvePythonManifest(repoRoot string) ResolveResult {
	data, err := os.ReadFile(filepath.Join(repoRoot, "pyproject.toml"))
	if err != nil {
		return ResolveResult{}
	}
	s := string(data)
	if strings.Contains(s, "pytest") {
		return ResolveResult{Test: "python -m pytest", Known: true}
	}
	return ResolveResult{}
}

// DefaultVerifier resolves the gate commands via ResolveVerifyCommands and
// wraps them in a Verifier with the standard CLI runner.
func DefaultVerifier(repoRoot, build, test string, timeout time.Duration) Verifier {
	res := ResolveVerifyCommands(repoRoot, build, test)
	return Verifier{Build: res.Build, Test: res.Test, Timeout: timeout, Runner: CLICommandRunner{}}
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

func (f *FakeCommandRunner) Run(ctx context.Context, dir string, argv []string) (string, error) {
	cmdStr := strings.Join(argv, " ")
	f.Calls = append(f.Calls, cmdStr)
	return f.outputs[cmdStr], f.errs[cmdStr]
}
