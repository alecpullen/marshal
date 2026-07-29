package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/hooks"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

const strictToolSchema = `{"type":"object","properties":{"path":{"type":"string","minLength":1}},` +
	`"required":["path"],"additionalProperties":false}`

// strictRegistry returns a registry holding one strict-schema tool plus a
// counter that reports whether its handler ever ran.
func strictRegistry(t *testing.T) (*registry.Registry, *atomic.Int64) {
	t.Helper()
	var ran atomic.Int64
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:        "strict.read",
		Description: "reads a path",
		Schema:      json.RawMessage(strictToolSchema),
		Risk:        registry.RiskWorkspaceWrite,
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			ran.Add(1)
			return registry.ToolResult{Summary: "ok", Content: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return reg, &ran
}

// A malformed call must be rejected before the user is asked to approve it.
func TestInvalidArgsRejectedBeforeApprovalPrompt(t *testing.T) {
	state := newTestState(t)
	reg, ran := strictRegistry(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"calling", "Done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "strict.read", Args: json.RawMessage(`{"wrong":"key"}`)}},
			nil,
		},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	r.MaxToolIterations = 5

	if _, err := r.RunTask(context.Background(), "read something"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if ran.Load() != 0 {
		t.Fatal("handler ran despite arguments that violate the schema")
	}
	if q := state.PendingApproval(); q != nil {
		t.Fatal("an approval was requested for a call that could never run")
	}
}

// The rejection is a tool message the model can correct, not a turn failure.
func TestInvalidArgsReturnsCorrectableToolMessage(t *testing.T) {
	state := newTestState(t)
	reg, _ := strictRegistry(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"calling", "Done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "strict.read", Args: json.RawMessage(`{"wrong":"key"}`)}},
			nil,
		},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	r.MaxToolIterations = 5

	task, err := r.RunTask(context.Background(), "read something")
	if err != nil {
		t.Fatalf("RunTask err = %v, want nil (a bad call is correctable, not fatal)", err)
	}
	if task.Summary != "Done." {
		t.Fatalf("Summary = %q, want the model to have recovered", task.Summary)
	}
	// The tool error is fed back in the next ChatRequest's messages.
	if len(p.Requests) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(p.Requests))
	}
	var found string
	for _, m := range p.Requests[1].Messages {
		if m.Role == schema.RoleTool && strings.Contains(m.Content, "strict.read") && strings.Contains(m.Content, "wrong") {
			found = m.Content
		}
	}
	if found == "" {
		t.Fatal("no tool message named the tool and the offending field; the model cannot self-correct")
	}
}

// A pre_tool_use hook may rewrite arguments after approval; the rewritten
// shape must be validated too.
func TestHookRewriteIntoInvalidArgsIsRejected(t *testing.T) {
	state := newTestState(t)
	reg, ran := strictRegistry(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"calling", "Done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "strict.read", Args: json.RawMessage(`{"path":"a.go"}`)}},
			nil,
		},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	r.MaxToolIterations = 5
	// The hook strips the required field after the call was approved.
	r.HookRunner = fakeHookRunner{preOut: hooks.Output{Rewrite: json.RawMessage(`{"path":""}`)}}

	if _, err := r.RunTask(context.Background(), "read something"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if ran.Load() != 0 {
		t.Fatal("handler ran on hook-rewritten arguments that violate the schema")
	}
}
