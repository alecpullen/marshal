package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// A scratchpad entry written in turn 1 must appear in the context pack
// of turn 2, and scratchpad.read must return the full content.
func TestScratchpadSurvivesAcrossTurns(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := native.RegisterAll(reg, native.Options{WorkspaceRoot: state.WorkingDir, SessionState: state}); err != nil {
		t.Fatalf("register: %v", err)
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{
			"I'll park this and finish.",
			"I see audit-results in the scratchpad. Reading it now.",
		},
		ToolCalls: [][]schema.ToolCall{
			{{
				ID:   "sp1",
				Name: "scratchpad.write",
				Args: json.RawMessage(`{"key":"audit-results","content":"found 3 issues","format":"json"}`),
			}},
			{{
				ID:   "sp2",
				Name: "scratchpad.read",
				Args: json.RawMessage(`{"key":"audit-results"}`),
			}},
		},
	}

	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))

	_, err := r.RunTask(context.Background(), "audit the codebase")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}

	// Second turn: the projection should mention audit-results.
	pack := state.ContextPack()
	found := false
	for _, sec := range pack.Sections {
		if sec.Kind == "scratchpad" && strings.Contains(sec.Content, "audit-results") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected scratchpad projection in second turn, got sections: %+v", pack.Sections)
	}

	// scratchpad.read must return the full content, not just the projection.
	if len(p.Requests) < 3 {
		t.Fatalf("expected at least 3 provider requests, got %d", len(p.Requests))
	}
	var readContent string
	for _, msg := range p.Requests[2].Messages {
		if msg.Role == schema.RoleTool && msg.ToolCallID == "sp2" {
			readContent = msg.Content
			break
		}
	}
	if !strings.Contains(readContent, "found 3 issues") {
		t.Fatalf("scratchpad.read result missing full content, got %q", readContent)
	}
}
