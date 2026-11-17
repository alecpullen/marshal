package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeHookScript writes body as an executable hook script inside a fresh
// temporary directory and returns its path.
func writeHookScript(t *testing.T, body string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	return script
}

// TestHookSuccessReturnsDecision guards the ordinary path against the
// cancellation-aware drain swallowing or truncating normal output.
func TestHookSuccessReturnsDecision(t *testing.T) {
	script := writeHookScript(t, "#!/bin/sh\nprintf '%s\\n' '{\"decision\":\"block\",\"reason\":\"no patches\"}'\n")
	r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "shell.run", Command: script, TimeoutMS: testHookTimeoutMS}}})
	out, err := r.RunPreToolUse(context.Background(), PreToolUseInput{ToolName: "shell.run", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("RunPreToolUse() error = %v", err)
	}
	if out.Decision != DecisionBlock || out.Reason != "no patches" {
		t.Fatalf("out = %+v, want block/no patches", out)
	}
	if out.FailedOpen {
		t.Fatalf("out = %+v, want FailedOpen=false", out)
	}
}

// TestHookNonzeroExitFailsOpen pins the fail-open policy for a hook that
// exits non-zero: a hook error, not a block.
func TestHookNonzeroExitFailsOpen(t *testing.T) {
	script := writeHookScript(t, "#!/bin/sh\nexit 3\n")
	r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "shell.run", Command: script, TimeoutMS: testHookTimeoutMS}}})
	out, err := r.RunPreToolUse(context.Background(), PreToolUseInput{ToolName: "shell.run", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("RunPreToolUse() error = %v", err)
	}
	if out.Decision != DecisionAllow || !out.FailedOpen {
		t.Fatalf("out = %+v, want allow + FailedOpen", out)
	}
}

// TestHookNonzeroExitBlocksWhenFailClosed is the fail-closed counterpart of
// TestHookNonzeroExitFailsOpen: the same script must block.
func TestHookNonzeroExitBlocksWhenFailClosed(t *testing.T) {
	script := writeHookScript(t, "#!/bin/sh\nexit 3\n")
	r := NewRunner(Config{FailClosed: true, Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "shell.run", Command: script, TimeoutMS: testHookTimeoutMS}}})
	out, err := r.RunPreToolUse(context.Background(), PreToolUseInput{ToolName: "shell.run", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("RunPreToolUse() error = %v", err)
	}
	if out.Decision != DecisionBlock {
		t.Fatalf("out = %+v, want block", out)
	}
	if out.Reason == "" {
		t.Fatal("block decision has an empty Reason")
	}
}
