package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestInternalToolsAreNeverOfferedToTheModel(t *testing.T) {
	reg := registry.New()
	ok := func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{Summary: "ok"}, nil
	}
	for _, tl := range []registry.Tool{
		{Name: "file.read", Description: "read", Risk: registry.RiskReadOnly, Schema: json.RawMessage(`{"type":"object"}`), Handler: ok},
		{Name: "hidden.tool", Description: "hidden", Risk: registry.RiskReadOnly, Internal: true, Schema: json.RawMessage(`{"type":"object"}`), Handler: ok},
	} {
		if err := reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "m")
	for _, d := range r.buildToolDefinitions() {
		if strings.Contains(d.Name, "hidden") {
			t.Fatalf("internal tool offered as a definition: %s", d.Name)
		}
	}
	sys := BuildSystemPromptWithSystem(SystemPromptOptions{Role: RoleGeneral, Tools: reg.List(), Mode: policy.ModeEdit})
	if strings.Contains(sys.Content, "hidden.tool") {
		t.Fatalf("internal tool listed in the envelope prompt:\n%s", sys.Content)
	}
	if _, ok := reg.Lookup("hidden.tool"); !ok {
		t.Fatal("internal tool must stay dispatchable")
	}
}
