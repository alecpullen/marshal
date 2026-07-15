package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/hooks"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestPreToolUseHookBlocksPatch(t *testing.T) {
	executed := false
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "file.write_patch", Description: "patch", Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executed = true
			return registry.ToolResult{Summary: "patched"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	p := &agenttest.ScriptedProvider{Responses: []string{`{"rationale":"r","action":{"type":"patch","content":"*** Begin Patch\n*** End Patch"}}`, `{"rationale":"r","action":{"type":"answer","content":"stopped"}}`}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.HookRunner = fakeHookRunner{preOut: hooks.Output{Decision: hooks.DecisionBlock, Reason: "patch blocked"}}
	runner.SetForceClass(string(ClassQuestion))

	if _, err := runner.RunTask(context.Background(), "patch"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if executed {
		t.Fatal("patch handler executed despite hook block")
	}
	log := state.AuditLog()
	if len(log) == 0 {
		t.Fatalf("audit log empty; want at least one block event")
	}
	if !strings.Contains(log[0].Error, "patch blocked") {
		t.Fatalf("audit log[0].Error = %q, want contains %q", log[0].Error, "patch blocked")
	}
}

func TestPreToolUseRewriteReentersPolicy(t *testing.T) {
	executed := false
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "shell.run", Description: "shell", Risk: registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executed = true
			return registry.ToolResult{Summary: "ran"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Permissions.Rules = []config.PermissionRule{{Permission: "shell.run", Pattern: "rm*", Action: "deny"}}
	p := &agenttest.ScriptedProvider{Responses: []string{`{"rationale":"r","action":{"type":"tool_call","tool":"shell.run","args":{"command":"date"}}}`, `{"rationale":"r","action":{"type":"answer","content":"done"}}`}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&cfg, nil), state, "test-model")
	runner.HookRunner = fakeHookRunner{preOut: hooks.Output{Rewrite: json.RawMessage(`{"command":"rm -rf ."}`)}}
	runner.SetForceClass(string(ClassQuestion))

	if _, err := runner.RunTask(context.Background(), "run date"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if executed {
		t.Fatal("shell handler executed after rewritten args were denied")
	}
}

func TestPreToolUseHookErrorBlocksWhenFailClosed(t *testing.T) {
	executed := false
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "shell.run", Description: "shell", Risk: registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executed = true
			return registry.ToolResult{Summary: "ran"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	p := &agenttest.ScriptedProvider{Responses: []string{`{"rationale":"r","action":{"type":"tool_call","tool":"shell.run","args":{"command":"date"}}}`, `{"rationale":"r","action":{"type":"answer","content":"done"}}`}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.HookRunner = fakeHookRunner{preOut: hooks.Output{Decision: hooks.DecisionBlock, Reason: "boom"}, preErr: fmt.Errorf("boom")}
	runner.SetForceClass(string(ClassQuestion))

	if _, err := runner.RunTask(context.Background(), "run"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if executed {
		t.Fatal("shell handler executed when hook returned an error")
	}
}

func TestTurnEndHookContinuesExactlyOnce(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"r","action":{"type":"answer","content":"done"}}`,
		`{"rationale":"r","action":{"type":"answer","content":"checked"}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.HookRunner = fakeHookRunner{turnOut: hooks.Output{Continue: true, Message: "Check tests before final."}}
	runner.SetForceClass(string(ClassQuestion))

	if _, err := runner.RunTask(context.Background(), "finish"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	msgs := state.Messages()
	var injected, final bool
	for _, msg := range msgs {
		if msg.Role == session.RoleUser && msg.Content == "Check tests before final." {
			injected = true
		}
		if msg.Role == session.RoleAssistant && msg.Final && msg.Content == "checked" {
			final = true
		}
	}
	if !injected || !final {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestTurnEndHookDoesNotContinueTwice(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"r","action":{"type":"answer","content":"one"}}`,
		`{"rationale":"r","action":{"type":"answer","content":"two"}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.HookRunner = fakeHookRunner{turnOut: hooks.Output{Continue: true, Message: "continue once"}}
	runner.SetForceClass(string(ClassQuestion))

	if _, err := runner.RunTask(context.Background(), "finish"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	count := 0
	for _, msg := range state.Messages() {
		if msg.Role == session.RoleUser && msg.Content == "continue once" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("injected continuation count = %d, want 1", count)
	}
}

func TestSummarizeAndContinueFailureSkipsLossyFallback(t *testing.T) {
	// When context exceeds the budget and summarization fails, the runner
	// must terminate the turn with a clear error rather than falling back
	// to lossy compaction (which can cause transcript duplication).
	//
	// The system prompt alone is ~3700 chars (~925 tokens). We set
	// MaxTurnContextTokens=1000 so the initial check passes. The tool
	// returns 4000+ chars, pushing the total past 1000 on iteration 2,
	// which triggers the summarization attempt.
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"need big data","action":{"type":"tool_call","tool":"big.tool","args":{}}}`, // idx=0: main loop first chat
			"", // idx=1: summarizeAndContinue call — overridden by errs[1]
			`{"rationale":"done","action":{"type":"final","content":"done"}}`, // idx=2: post-compaction main loop (old code only)
		},
		Errs: []error{nil, errors.New("simulated summarization failure")},
	}
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "big.tool", Description: "big output", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "big", Content: strings.Repeat("big data ", 500)}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassQuestion))
	// Must be > ~925 (system prompt) but still allow tool result to trigger overflow.
	runner.MaxTurnContextTokens = 1000
	runner.MaxRetries = 0 // no retries for deterministic index mapping

	task, err := runner.RunTask(context.Background(), "process the big data")
	if err == nil {
		t.Fatal("expected RunTask to return error on summarization failure, got nil")
	}
	if !strings.Contains(err.Error(), "summarization") {
		t.Fatalf("error should mention summarization failure, got: %v", err)
	}
	if task.Status != TaskStatusFailed {
		t.Fatalf("task status = %v, want TaskStatusFailed", task.Status)
	}
}

func TestTurnEndHookContinuesNativePath(t *testing.T) {
	// Drives the native-tools final-return site (runner.go: NativeTools &&
	// len(res.ToolCalls) == 0 && strings.TrimSpace(res.Text) != ""). The
	// existing two turn-end tests both use the JSON ActionAnswer/ActionFinal
	// site, so this test guards the other hook wiring against regressions.
	p := &agenttest.ScriptedProvider{
		Responses: []string{"first native answer", "final native answer"},
		ToolCalls: [][]schema.ToolCall{nil, nil},
	}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.NativeTools = true
	runner.HookRunner = fakeHookRunner{turnOut: hooks.Output{Continue: true, Message: "Verify native path hook fired."}}
	runner.SetForceClass(string(ClassQuestion))

	if _, err := runner.RunTask(context.Background(), "finish"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	msgs := state.Messages()
	var injected, final bool
	for _, msg := range msgs {
		if msg.Role == session.RoleUser && msg.Content == "Verify native path hook fired." {
			injected = true
		}
		if msg.Role == session.RoleAssistant && msg.Final && msg.Content == "final native answer" {
			final = true
		}
	}
	if !injected {
		t.Fatalf("turn-end hook message was not injected on the native-tools final-return path: %+v", msgs)
	}
	if !final {
		t.Fatalf("second native answer never reached Final state: %+v", msgs)
	}
}
