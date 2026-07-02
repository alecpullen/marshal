package native

import (
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestGitStatusInvokesRunner(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: " M file.go\n", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "git.status", `{}`)
	if err != nil {
		t.Fatalf("git.status returned error: %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner requests = %d, want 1", len(runner.requests))
	}
	if runner.requests[0].Command != "git status --short" {
		t.Fatalf("command = %q", runner.requests[0].Command)
	}
	if runner.requests[0].Dir != root {
		t.Fatalf("dir = %q, want root", runner.requests[0].Dir)
	}
	if result.CommandExitCode == nil || *result.CommandExitCode != 0 {
		t.Fatalf("CommandExitCode = %#v", result.CommandExitCode)
	}
	if !strings.Contains(result.Content, "M file.go") {
		t.Fatalf("Content = %q", result.Content)
	}
}

func TestGitDiffInvokesRunnerWithOptionalPath(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: "diff --git a/file.go b/file.go\n", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "git.diff", `{"path":"file.go"}`)
	if err != nil {
		t.Fatalf("git.diff returned error: %v", err)
	}
	if runner.requests[0].Command != "git diff -- 'file.go'" {
		t.Fatalf("command = %q", runner.requests[0].Command)
	}
	if !strings.Contains(result.Summary, "diff present") {
		t.Fatalf("Summary = %q", result.Summary)
	}
}

func TestGitDiffRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "git.diff", `{"path":"../outside"}`); err == nil {
		t.Fatal("git.diff traversal returned nil error")
	}
	if len(runner.requests) != 0 {
		t.Fatalf("runner called %d times, want 0", len(runner.requests))
	}
}
