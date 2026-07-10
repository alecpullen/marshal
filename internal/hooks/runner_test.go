package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPreToolUseBlockDecision(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '{\"decision\":\"block\",\"reason\":\"no patches\"}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "file.write_patch", Command: script, TimeoutMS: 1000}}})
	out, err := r.RunPreToolUse(context.Background(), PreToolUseInput{ToolName: "file.write_patch", Args: json.RawMessage(`{"patch":"x"}`)})
	if err != nil {
		t.Fatalf("RunPreToolUse() error = %v", err)
	}
	if out.Decision != DecisionBlock || out.Reason != "no patches" {
		t.Fatalf("out = %+v", out)
	}
}

func TestPreToolUseRewriteDecision(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '{\"rewrite\":{\"command\":\"go test ./...\"}}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "shell.run", Command: script, TimeoutMS: 1000}}})
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
	_ = time.Second
}
