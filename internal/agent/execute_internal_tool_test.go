package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// internalPatchTool registers a file.write_patch that records whether its
// handler ran. Internal is set exactly as the production registration does, so
// the guard is exercised against the real flag rather than a stand-in.
func internalPatchTool(t *testing.T, reg *registry.Registry, ran *bool) {
	t.Helper()
	tool := registry.Tool{
		Name:     "file.write_patch",
		Risk:     registry.RiskWorkspaceWrite,
		Internal: true,
		Schema:   json.RawMessage(`{"type":"object"}`),
	}
	tool.Handler = func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		*ran = true
		return registry.ToolResult{Summary: "applied", Content: "applied"}, nil
	}
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register file.write_patch: %v", err)
	}
}

// TestDirectInternalToolCallIsRejected pins the defect this guard closes:
// Internal suppressed advertisement but not execution, so a model that named
// an internal tool got it run even though it was never offered. The call must
// now be refused before the handler, with a redirect to the public tool.
func TestDirectInternalToolCallIsRejected(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	var ran bool
	internalPatchTool(t, reg, &ran)
	r := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	msgs, err := r.executeToolCall(context.Background(), ModelAction{
		Type: ActionToolCall,
		Tool: "file.write_patch",
		Args: json.RawMessage(`{"patch":"File: a.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}`),
	})
	if err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if ran {
		t.Fatal("internal tool handler ran on a direct call; the guard did not fire")
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "file.edit") {
		t.Fatalf("refusal did not redirect to file.edit:\n%s", msgs[0].Content)
	}
}

// TestEnvelopePatchStillReachesInternalPatchTool is the other half of the
// contract: the envelope patch action is the runtime's own spelling of
// file.write_patch and must keep working. The guard exempts ActionPatch, so
// the handler runs even though the tool is Internal.
func TestEnvelopePatchStillReachesInternalPatchTool(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	var ran bool
	internalPatchTool(t, reg, &ran)
	r := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	msgs, err := r.executeToolCall(context.Background(), ModelAction{
		Type:    ActionPatch,
		Content: "File: a.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE",
	})
	if err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}
	if !ran {
		t.Fatalf("envelope patch did not reach the handler; messages: %+v", msgs)
	}
}

// TestInternalToolRedirectNamesThePublicAlternative pins the redirect text for
// each internal tool, so a model that guessed a name is told what to use
// instead rather than only that it was refused.
func TestInternalToolRedirectNamesThePublicAlternative(t *testing.T) {
	cases := map[string]string{
		"file.write_patch": "file.edit",
		"test.run":         "shell.run",
		"hidden.tool":      "internal tool",
	}
	for name, want := range cases {
		if got := internalToolRedirect(name); !strings.Contains(got, want) {
			t.Errorf("internalToolRedirect(%q) = %q, want it to mention %q", name, got, want)
		}
	}
}
