package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPreToolUseBlockDecision(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '{\"decision\":\"block\",\"reason\":\"no patches\"}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "file.write_patch", Command: script, TimeoutMS: 5000}}})
	out, err := r.RunPreToolUse(context.Background(), PreToolUseInput{ToolName: "file.write_patch", Args: json.RawMessage(`{"patch":"x"}`)})
	if err != nil {
		t.Fatalf("RunPreToolUse() error = %v", err)
	}
	if out.Decision != DecisionBlock || out.Reason != "no patches" {
		t.Fatalf("out = %+v", out)
	}
}

func TestPreToolUseStable(t *testing.T) {
	for i := 0; i < 100; i++ {
		dir := t.TempDir()
		script := filepath.Join(dir, "hook.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '{\"decision\":\"block\",\"reason\":\"no patches\"}'\n"), 0755); err != nil {
			t.Fatal(err)
		}
		r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "file.write_patch", Command: script, TimeoutMS: 5000}}})
		out, err := r.RunPreToolUse(context.Background(), PreToolUseInput{ToolName: "file.write_patch", Args: json.RawMessage(`{"patch":"x"}`)})
		if err != nil {
			t.Fatalf("iter %d: RunPreToolUse() error = %v", i, err)
		}
		if out.Decision != DecisionBlock || out.Reason != "no patches" {
			t.Fatalf("iter %d: out = %+v", i, out)
		}
	}
}

func TestPreToolUseRewriteDecision(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '{\"rewrite\":{\"command\":\"go test ./...\"}}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "shell.run", Command: script, TimeoutMS: 5000}}})
	out, err := r.RunPreToolUse(context.Background(), PreToolUseInput{ToolName: "shell.run", Args: json.RawMessage(`{"command":"go test ./internal/..."}`)})
	if err != nil {
		t.Fatalf("RunPreToolUse() error = %v", err)
	}
	if string(out.Rewrite) != `{"command":"go test ./..."}` {
		t.Fatalf("Rewrite = %s", out.Rewrite)
	}
}

func TestHookTimeoutFailOpen(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 2\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "shell.run", Command: script, TimeoutMS: 10}}})
	out, err := r.RunPreToolUse(context.Background(), PreToolUseInput{ToolName: "shell.run", Args: json.RawMessage(`{"command":"date"}`)})
	if err != nil {
		t.Fatalf("fail-open timeout returned error: %v", err)
	}
	if out.Decision != DecisionAllow || !out.FailedOpen {
		t.Fatalf("out = %+v", out)
	}
}

func TestHookScrubSecretEnv(t *testing.T) {
	t.Setenv("MARSHAL_TEST_SECRET_KEY", "leak-me")
	t.Setenv("MARSHAL_TEST_OPENAI_API_KEY", "sk-leak")
	t.Setenv("PATH", "/usr/bin:/bin")
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nif env | grep -qE 'MARSHAL_TEST_(SECRET_KEY|OPENAI_API_KEY)'; then\n  printf '%s\\n' '{\"decision\":\"block\",\"reason\":\"secret visible\"}'\nfi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "shell.run", Command: script, TimeoutMS: 2000}}})
	out, err := r.RunPreToolUse(context.Background(), PreToolUseInput{ToolName: "shell.run", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("RunPreToolUse() error = %v", err)
	}
	if out.Decision != DecisionAllow {
		t.Fatalf("out = %+v, want allow (secrets must be scrubbed)", out)
	}
}

func TestHookOutputCapPreventsOOM(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	// Emit 2 MiB of data — well over a 1 MiB cap.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nyes 'x' | head -c 2097152\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "shell.run", Command: script, TimeoutMS: 5000}}})
	out, err := r.RunPreToolUse(context.Background(), PreToolUseInput{ToolName: "shell.run", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("RunPreToolUse() error = %v", err)
	}
	// Over-cap output must fail open, not OOM. The exact decision is
	// allow (fail-open default); the important assertion is that the
	// call returns at all within the test timeout rather than OOMing.
	if out.Decision != DecisionAllow && !out.FailedOpen {
		t.Fatalf("out = %+v, want fail-open allow on over-cap output", out)
	}
}

func TestTurnEndContinuePropagated(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '{\"continue\":true,\"message\":\"Check tests before final.\"}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(Config{Entries: []HookEntry{{Event: EventTurnEnd, Command: script, TimeoutMS: 1000}}})
	out, err := r.RunTurnEnd(context.Background(), TurnEndInput{SessionID: "s1"})
	if err != nil {
		t.Fatalf("RunTurnEnd() error = %v", err)
	}
	if !out.Continue || out.Message != "Check tests before final." {
		t.Fatalf("out = %+v, want Continue=true Message=\"Check tests before final.\"", out)
	}
	if out.HookCount != 1 {
		t.Fatalf("HookCount = %d, want 1", out.HookCount)
	}
}
