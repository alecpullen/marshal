package native

import (
	"fmt"
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

func TestGitLogDefaultAndClampedLimit(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: "abc1234 2026-08-20 alec subject one\ndef5678 2026-08-19 alec subject two\n", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "git.log", `{}`)
	if err != nil {
		t.Fatalf("git.log: %v", err)
	}
	want := "git log --format='%h %ad %an %s' --date=short -n 20"
	if runner.requests[0].Command != want {
		t.Fatalf("command = %q, want %q", runner.requests[0].Command, want)
	}
	if result.Summary != "2 commits" {
		t.Fatalf("Summary = %q, want %q", result.Summary, "2 commits")
	}

	if _, err := invokeTool(t, reg, "git.log", `{"limit":500}`); err != nil {
		t.Fatalf("git.log clamp: %v", err)
	}
	wantClamped := "git log --format='%h %ad %an %s' --date=short -n 100"
	if runner.requests[1].Command != wantClamped {
		t.Fatalf("clamped command = %q, want %q", runner.requests[1].Command, wantClamped)
	}
}

func TestGitLogScopesToPath(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: "abc1234 2026-08-20 alec touched it\n", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "git.log", `{"path":"sub/file.go"}`); err != nil {
		t.Fatalf("git.log: %v", err)
	}
	want := "git log --format='%h %ad %an %s' --date=short -n 20 -- 'sub/file.go'"
	if runner.requests[0].Command != want {
		t.Fatalf("command = %q, want %q", runner.requests[0].Command, want)
	}
}

func TestGitLogEmptyAndTraversal(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: "", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "git.log", `{}`)
	if err != nil {
		t.Fatalf("git.log: %v", err)
	}
	if result.Summary != "no commits" {
		t.Fatalf("Summary = %q, want %q", result.Summary, "no commits")
	}
	if _, err := invokeTool(t, reg, "git.log", `{"path":"../outside"}`); err == nil {
		t.Fatal("git.log traversal returned nil error")
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner called %d times, want 1 (traversal must not reach the runner)", len(runner.requests))
	}
}

func TestGitBlameWithRange(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: "abc1234 (alec 2026-08-20 10) line\n", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "git.blame", `{"path":"foo.go","start_line":10,"end_line":20}`)
	if err != nil {
		t.Fatalf("git.blame: %v", err)
	}
	want := "git blame -L 10,20 --date=short -- 'foo.go'"
	if runner.requests[0].Command != want {
		t.Fatalf("command = %q, want %q", runner.requests[0].Command, want)
	}
	if result.Summary != "blame foo.go lines 10-20" {
		t.Fatalf("Summary = %q", result.Summary)
	}
}

func TestGitBlameRangeValidation(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	for _, args := range []string{
		`{"path":"foo.go","start_line":10}`,               // one-sided
		`{"path":"foo.go","end_line":20}`,                 // one-sided
		`{"path":"foo.go","start_line":20,"end_line":10}`, // inverted
		`{"path":"foo.go","start_line":0,"end_line":10}`,  // zero start
		`{"path":"../outside"}`,                           // traversal
	} {
		if _, err := invokeTool(t, reg, "git.blame", args); err == nil {
			t.Fatalf("git.blame %s returned nil error", args)
		}
	}
	if len(runner.requests) != 0 {
		t.Fatalf("runner called %d times, want 0", len(runner.requests))
	}
}

func TestGitBlameWholeFileCapsAt200Lines(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(&sb, "abc1234 (alec 2026-08-20 %d) line %d\n", i, i)
	}
	runner := &fakeRunner{result: CommandResult{Stdout: sb.String(), ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "git.blame", `{"path":"foo.go"}`)
	if err != nil {
		t.Fatalf("git.blame: %v", err)
	}
	want := "git blame --date=short -- 'foo.go'"
	if runner.requests[0].Command != want {
		t.Fatalf("command = %q, want %q", runner.requests[0].Command, want)
	}
	if result.Summary != "blame foo.go" {
		t.Fatalf("Summary = %q", result.Summary)
	}
	if !strings.Contains(result.Content, "line 200") {
		t.Fatal("content should include line 200")
	}
	if strings.Contains(result.Content, "line 201") {
		t.Fatal("content should be capped before line 201")
	}
	if !strings.Contains(result.Content, "first 200 lines shown") {
		t.Fatalf("content missing cap note:\n%s", result.Content)
	}
}
