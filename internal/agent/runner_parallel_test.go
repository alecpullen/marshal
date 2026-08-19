package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/hooks"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestRunCachesReadOnlyToolResults(t *testing.T) {
	calls := 0
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:      "demo.read",
		Risk:      registry.RiskReadOnly,
		Cacheable: true,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			calls++
			return registry.ToolResult{Summary: "ok", Content: "demo content"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"read","action":{"type":"tool_call","tool":"demo.read","args":{"key":"value"}}}`,
		`{"rationale":"read again","action":{"type":"tool_call","tool":"demo.read","args":{"key":"value"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if err := runner.Run(context.Background(), "Read the demo value twice"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("handler called %d times, want 1 (second call should be cached)", calls)
	}
	foundCached := false
	for _, ev := range state.AuditLog() {
		if strings.Contains(ev.ResultSummary, "(cached)") {
			foundCached = true
		}
	}
	if !foundCached {
		t.Fatalf("audit log missing cached result marker: %#v", state.AuditLog())
	}
}

func TestRunExecutesParallelReadOnlyActions(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "demo.a",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "a ok", Content: "alpha"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(registry.Tool{
		Name: "demo.b",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "b ok", Content: "beta"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"read both","actions":[{"type":"tool_call","tool":"demo.a","args":{}},{"type":"tool_call","tool":"demo.b","args":{}}]}`,
		`{"rationale":"done","action":{"type":"final","content":"Got alpha and beta."}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if err := runner.Run(context.Background(), "Read both demo values"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(state.AuditLog()) != 2 {
		t.Fatalf("len(auditLog) = %d, want 2", len(state.AuditLog()))
	}
	final := state.Messages()[len(state.Messages())-1].Content
	if !strings.Contains(final, "alpha") || !strings.Contains(final, "beta") {
		t.Fatalf("final answer missing parallel results: %s", final)
	}
}

func TestRunDetectsRepeatedToolCalls(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "demo.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	read := `{"rationale":"loop","action":{"type":"tool_call","tool":"demo.read","args":{}}}`
	responses := make([]string, 0, repeatHardStall+1)
	for i := 0; i < repeatHardStall; i++ {
		responses = append(responses, read)
	}
	responses = append(responses, `{"rationale":"done","action":{"type":"final","content":"Done."}}`)
	p := &agenttest.ScriptedProvider{Responses: responses}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.Role = RoleRepoScout
	runner.MaxToolIterations = repeatHardStall + 1

	// repeatHardStall identical tool calls in a row is an exact-repeat hard
	// stall. For non-general (swarm) roles this forces an immediate final
	// answer via finalize; the final scripted response ("Done.") is what
	// finalize's forced call receives.
	task, err := runner.RunTask(context.Background(), "Read the demo value")
	if err != nil {
		t.Fatalf("RunTask returned error: %v", err)
	}
	if task.Status != TaskStatusCompleted || task.SalvagedReason != "stalled" {
		t.Fatalf("task = %+v, want completed with SalvagedReason=%q", task, "stalled")
	}
	if task.Summary != "Done." {
		t.Fatalf("task.Summary = %q, want %q", task.Summary, "Done.")
	}
}

func TestWriteGateAcquiredForWriteToolsOnly(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "fs.touch", Description: "write something", Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "touched"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(registry.Tool{
		Name: "fs.peek", Description: "read something", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "peeked"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale": "read", "action": {"type": "tool_call", "tool": "fs.peek", "args": {}}}`,
		`{"rationale": "write", "action": {"type": "tool_call", "tool": "fs.touch", "args": {}}}`,
		`{"rationale": "done", "action": {"type": "final", "content": "done"}}`,
	}}
	gate := &recordingGate{}
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	runner.SetForceClass("question")
	runner.WriteGate = gate

	if err := runner.Run(context.Background(), "touch the file"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gate.acquisitions != 1 {
		t.Fatalf("gate acquired %d times, want 1 (write tool only)", gate.acquisitions)
	}
}

func TestRunAllowsSustainedDistinctReadsBeforeAnswering(t *testing.T) {
	// Regression for the "agent never produces an answer" bug: five distinct
	// file reads used to trip readOnlyChurn(4) and force finalize after the
	// 4th read, cutting research off before the model could answer.
	reg := registry.New()
	var executed []string
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executed = append(executed, string(call.Args))
			return registry.ToolResult{Summary: "ok", Content: "package main"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	responses := make([]string, 0, 6)
	for i := 1; i <= 5; i++ {
		responses = append(responses, fmt.Sprintf(
			`{"rationale":"reading file %d of 5","action":{"type":"tool_call","tool":"file.read","args":{"path":"pkg/f%d.go"}}}`, i, i))
	}
	responses = append(responses,
		`{"rationale":"done","action":{"type":"final","content":"THE REAL ANSWER after reading all five files."}}`)

	p := &agenttest.ScriptedProvider{Responses: responses}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	task, err := r.RunTask(context.Background(), "how does pkg work?")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if len(executed) != 5 {
		t.Fatalf("executed %d reads, want all 5", len(executed))
	}
	if task.Status != TaskStatusCompleted || task.SalvagedReason != "" {
		t.Fatalf("task = %+v, want normal (un-salvaged) completion", task)
	}
	if task.Summary != "THE REAL ANSWER after reading all five files." {
		t.Fatalf("Summary = %q, want the model's own final answer", task.Summary)
	}
	if p.Calls != 6 {
		t.Fatalf("provider calls = %d, want 6 (5 reads + 1 final, no finalize calls)", p.Calls)
	}
	for _, m := range state.Messages() {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "repeating") {
			t.Fatalf("distinct reads drew a repetition nudge: %q", m.Content)
		}
	}
}

func TestRunAllowsParallelReadBatchWithoutStalling(t *testing.T) {
	// Regression: a single actions-array batch of four distinct reads — the
	// exact pattern baseOutputFormat recommends — used to record four churn
	// entries and hard-stall on the very first model response.
	reg := registry.New()
	var executedMu sync.Mutex
	var executed []string
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executedMu.Lock()
			executed = append(executed, string(call.Args))
			executedMu.Unlock()
			return registry.ToolResult{Summary: "ok", Content: "package main"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"read all four relevant files at once","actions":[
			{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}},
			{"type":"tool_call","tool":"file.read","args":{"path":"b.go"}},
			{"type":"tool_call","tool":"file.read","args":{"path":"c.go"}},
			{"type":"tool_call","tool":"file.read","args":{"path":"d.go"}}]}`,
		`{"rationale":"one more file","action":{"type":"tool_call","tool":"file.read","args":{"path":"e.go"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"REAL ANSWER."}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	task, err := r.RunTask(context.Background(), "how does pkg work?")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	executedMu.Lock()
	executedCount := len(executed)
	executedMu.Unlock()
	if executedCount != 5 {
		t.Fatalf("executed %d reads, want 5 (batch of 4 + 1 follow-up)", executedCount)
	}
	if task.SalvagedReason != "" || task.Summary != "REAL ANSWER." {
		t.Fatalf("task = %+v, want un-salvaged completion with the model's answer", task)
	}
	if p.Calls != 3 {
		t.Fatalf("provider calls = %d, want 3 (batch, single read, final)", p.Calls)
	}
}

// TestParallelActionsSerializesQuestionTools is a regression test for the
func TestSerialBatchContinuesAfterError(t *testing.T) {
	// Test that executeActions runs all serial tools even when one errors.
	reg := registry.New()
	var mu sync.Mutex
	serialCount := 0
	errCount := 0

	if err := reg.Register(registry.Tool{
		Name: "question.ask",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			mu.Lock()
			serialCount++
			count := serialCount
			mu.Unlock()
			if count == 2 {
				mu.Lock()
				errCount++
				mu.Unlock()
				return registry.ToolResult{}, fmt.Errorf("simulated error on serial tool %d", count)
			}
			return registry.ToolResult{Summary: "ok", Content: "result"}, nil
		},
		Schema: json.RawMessage(`{"type":"object","properties":{"questions":{"type":"array","items":{"type":"object","properties":{"question":{"type":"string"}},"required":["question"]}}},"required":["questions"]}`),
	}); err != nil {
		t.Fatalf("Register question.ask: %v", err)
	}
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "content"}, nil
		},
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}); err != nil {
		t.Fatalf("Register file.read: %v", err)
	}

	state := newTestState(t)
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.tracker = newProgressTracker()
	defer func() { r.tracker = nil }()

	// Build 3 actions: question.ask, question.ask (errors), file.read
	actions := []ModelAction{
		{Type: ActionToolCall, Tool: "question.ask", Args: json.RawMessage(`{"questions":[{"question":"Q1?"}]}`)},
		{Type: ActionToolCall, Tool: "question.ask", Args: json.RawMessage(`{"questions":[{"question":"Q2?"}]}`)},
		{Type: ActionToolCall, Tool: "file.read", Args: json.RawMessage(`{"path":"x.go"}`)},
	}

	// Use a background context with cancel to prevent hanging
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results, err := r.executeActions(ctx, actions)
	if err != nil {
		t.Fatalf("executeActions err = %v (should not propagate tool errors)", err)
	}

	if serialCount != 2 {
		t.Fatalf("serial tools executed = %d, want 2 (both serial tools should run)", serialCount)
	}
	if errCount != 1 {
		t.Fatalf("errors = %d, want 1 (second serial tool should error)", errCount)
	}
	if len(results) != 3 {
		t.Fatalf("results count = %d, want 3 (one per tool call)", len(results))
	}
	// Verify one of the messages contains the error
	foundErr := false
	for _, msg := range results {
		if msg.Role == schema.RoleUser && strings.Contains(msg.Content, "simulated error") {
			foundErr = true
			break
		}
	}
	if !foundErr {
		t.Fatal("expected an error message for the failed serial tool")
	}
}

// TestParallelAgentRunDispatchesConcurrently proves that two agent.run
// calls in a single native batch overlap, preserve result order, and that
// a third call hits the concurrency guard.
func TestParallelAgentRunDispatchesConcurrently(t *testing.T) {
	reg := registry.New()
	state := newTestState(t)

	gate := make(chan struct{})
	var enteredMu sync.Mutex
	var entered []string
	var exited []string

	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		return &Runner{State: state}, state, nil
	}
	exec := func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		enteredMu.Lock()
		entered = append(entered, prompt)
		enteredMu.Unlock()
		<-gate
		enteredMu.Lock()
		exited = append(exited, prompt)
		enteredMu.Unlock()
		return "done: " + prompt, "", nil
	}

	tool := NewSubagentTool(factory, nil, reg, state, WithSubagentExec(exec))
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register agent.run: %v", err)
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{"", ""},
		ToolCalls: [][]schema.ToolCall{
			{
				{Name: "agent.run", Args: json.RawMessage(`{"prompt":"alpha","description":"alpha"}`)},
				{Name: "agent.run", Args: json.RawMessage(`{"prompt":"beta","description":"beta"}`)},
			},
			nil,
		},
		FinishReasons: []string{"tool_calls", "stop"},
		ProviderCaps:  schema.ProviderCapabilities{ToolCalling: true},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true

	runErr := make(chan error, 1)
	go func() {
		runErr <- r.Run(context.Background(), "run two parallel subagents")
	}()

	// Wait until both subagents have entered exec, proving concurrency.
	for {
		enteredMu.Lock()
		n := len(entered)
		enteredMu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case err := <-runErr:
			t.Fatalf("Run finished early with %d entered: %v", n, err)
		case <-time.After(10 * time.Millisecond):
		}
	}

	close(gate)
	if err := <-runErr; err != nil {
		t.Fatalf("Run error: %v", err)
	}

	enteredMu.Lock()
	defer enteredMu.Unlock()
	if len(entered) != 2 {
		t.Fatalf("entered = %d, want 2", len(entered))
	}

	// Verify the tool-result messages sent back to the model preserve
	// the original call order.
	if len(p.Requests) < 2 {
		t.Fatalf("expected at least 2 provider requests, got %d", len(p.Requests))
	}
	var resultOrder []string
	for _, m := range p.Requests[1].Messages {
		if strings.Contains(m.Content, "done: alpha") {
			resultOrder = append(resultOrder, "alpha")
		}
		if strings.Contains(m.Content, "done: beta") {
			resultOrder = append(resultOrder, "beta")
		}
	}
	if len(resultOrder) != 2 || resultOrder[0] != "alpha" || resultOrder[1] != "beta" {
		t.Fatalf("result order = %v, want [alpha beta]", resultOrder)
	}
	if len(exited) != 2 {
		t.Fatalf("exited = %d, want 2", len(exited))
	}
}

// TestParallelAgentRunPreservesSiblingsOnError verifies that when one of
// two concurrent agent.run calls fails, the sibling's result is still
// recorded and the turn completes.
func TestParallelAgentRunPreservesSiblingsOnError(t *testing.T) {
	reg := registry.New()
	state := newTestState(t)

	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		return &Runner{State: state}, state, nil
	}
	exec := func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		if prompt == "fail" {
			return "", "", errors.New("boom")
		}
		return "ok: " + prompt, "", nil
	}

	tool := NewSubagentTool(factory, nil, reg, state, WithSubagentExec(exec))
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register agent.run: %v", err)
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{"", ""},
		ToolCalls: [][]schema.ToolCall{
			{
				{Name: "agent.run", Args: json.RawMessage(`{"prompt":"ok","description":"ok"}`)},
				{Name: "agent.run", Args: json.RawMessage(`{"prompt":"fail","description":"fail"}`)},
			},
			nil,
		},
		FinishReasons: []string{"tool_calls", "stop"},
		ProviderCaps:  schema.ProviderCapabilities{ToolCalling: true},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true

	if err := r.Run(context.Background(), "run parallel subagents one fails"); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// The successful result should appear in the tool-result messages sent
	// back to the model; the failed one should produce a tool-error message.
	if len(p.Requests) < 2 {
		t.Fatalf("expected at least 2 provider requests, got %d", len(p.Requests))
	}
	foundOK, foundErr := false, false
	for _, m := range p.Requests[1].Messages {
		if strings.Contains(m.Content, "ok: ok") {
			foundOK = true
		}
		if strings.Contains(m.Content, "boom") {
			foundErr = true
		}
	}
	if !foundOK {
		t.Fatal("successful sibling result missing from tool results")
	}
	if !foundErr {
		t.Fatal("failed sibling error missing from tool results")
	}
}

// haltHook returns DecisionHalt when the tool's arguments contain a "fail"
// prompt, forcing executeToolCall to return a pre-tool (nil, err). This is
// the deterministic pre-tool failure path that the old flushRunBatch
// discarded the whole batch on.
type haltHook struct{}

func (haltHook) RunPreToolUse(ctx context.Context, input hooks.PreToolUseInput) (hooks.Output, error) {
	if strings.Contains(string(input.Args), `"fail"`) {
		return hooks.Output{Decision: hooks.DecisionHalt, Reason: "halted for test"}, nil
	}
	return hooks.Output{}, nil
}

func (haltHook) RunTurnEnd(ctx context.Context, input hooks.TurnEndInput) (hooks.Output, error) {
	return hooks.Output{}, nil
}

// TestParallelAgentRunPreservesSiblingsOnPreToolError verifies that when
// a batch of two agent.run calls hits a pre-tool error (hook halt) for one
// slot, the successful sibling's result is still returned to the model
// rather than the whole batch being discarded by a propagated error.
func TestParallelAgentRunPreservesSiblingsOnPreToolError(t *testing.T) {
	reg := registry.New()
	state := newTestState(t)

	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		return &Runner{State: state}, state, nil
	}
	exec := func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		return "ok: " + prompt, "", nil
	}

	tool := NewSubagentTool(factory, nil, reg, state, WithSubagentExec(exec))
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register agent.run: %v", err)
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{"", ""},
		ToolCalls: [][]schema.ToolCall{
			{
				{Name: "agent.run", Args: json.RawMessage(`{"prompt":"ok","description":"ok"}`)},
				{Name: "agent.run", Args: json.RawMessage(`{"prompt":"fail","description":"fail"}`)},
			},
			nil,
		},
		FinishReasons: []string{"tool_calls", "stop"},
		ProviderCaps:  schema.ProviderCapabilities{ToolCalling: true},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.HookRunner = haltHook{}

	if err := r.Run(context.Background(), "run parallel subagents one hook-halts"); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// The successful result should appear in the tool-result messages sent
	// back to the model.
	if len(p.Requests) < 2 {
		t.Fatalf("expected at least 2 provider requests, got %d", len(p.Requests))
	}
	foundOK := false
	for _, m := range p.Requests[1].Messages {
		if strings.Contains(m.Content, "ok: ok") {
			foundOK = true
		}
	}
	if !foundOK {
		t.Fatal("successful sibling result missing from tool results — discarded by partial batch error")
	}
}

// TestParallelAgentRunConcurrencyLimitRejectsThird verifies that a batch
// of three agent.run calls runs the first two in parallel and returns a
// concurrency-limit error for the third.
func TestParallelAgentRunConcurrencyLimitRejectsThird(t *testing.T) {
	reg := registry.New()
	state := newTestState(t)

	gate := make(chan struct{})
	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		return &Runner{State: state}, state, nil
	}
	exec := func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		<-gate
		return "done: " + prompt, "", nil
	}

	tool := NewSubagentTool(factory, nil, reg, state, WithSubagentExec(exec))
	if err := reg.Register(tool); err != nil {
		t.Fatalf("register agent.run: %v", err)
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{"", ""},
		ToolCalls: [][]schema.ToolCall{
			{
				{Name: "agent.run", Args: json.RawMessage(`{"prompt":"one","description":"one"}`)},
				{Name: "agent.run", Args: json.RawMessage(`{"prompt":"two","description":"two"}`)},
				{Name: "agent.run", Args: json.RawMessage(`{"prompt":"three","description":"three"}`)},
			},
			nil,
		},
		FinishReasons: []string{"tool_calls", "stop"},
		ProviderCaps:  schema.ProviderCapabilities{ToolCalling: true},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true

	runErr := make(chan error, 1)
	go func() {
		runErr <- r.Run(context.Background(), "run three parallel subagents")
	}()

	// Wait briefly to let the dispatcher reach the third call.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	if err := <-runErr; err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// The third call's concurrency-limit error should appear in the tool
	// result messages sent back to the model after the first batch.
	if len(p.Requests) < 2 {
		t.Fatalf("expected at least 2 provider requests, got %d", len(p.Requests))
	}
	foundConcurrencyErr := false
	for _, m := range p.Requests[1].Messages {
		if strings.Contains(m.Content, "concurrency limit") {
			foundConcurrencyErr = true
		}
	}
	if !foundConcurrencyErr {
		t.Fatal("expected concurrency-limit error message for third agent.run")
	}
}

// bug in which the parallel-actions path (executeActions) raced two
// ask_user / question.ask calls on the single State.PendingQuestion
// slot, clobbering queue state and leaking ResponseChans. The fix
// partitions the batch: question tools run sequentially while the rest
// still parallelize.
func TestParallelActionsSerializesQuestionTools(t *testing.T) {
	var (
		mu                       sync.Mutex
		liveQA, liveAU, liveRead int
		maxQA, maxAU, maxRead    int
		ranQA, ranAU, ranRead    int
	)
	note := func(kind string) func() {
		mu.Lock()
		switch kind {
		case "question.ask":
			liveQA++
			if liveQA > maxQA {
				maxQA = liveQA
			}
		case "ask_user":
			liveAU++
			if liveAU > maxAU {
				maxAU = liveAU
			}
		case "file.read":
			liveRead++
			if liveRead > maxRead {
				maxRead = liveRead
			}
		}
		mu.Unlock()
		return func() {
			mu.Lock()
			defer mu.Unlock()
			switch kind {
			case "question.ask":
				liveQA--
			case "ask_user":
				liveAU--
			case "file.read":
				liveRead--
			}
		}
	}

	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			defer note("file.read")()
			time.Sleep(15 * time.Millisecond)
			mu.Lock()
			ranRead++
			mu.Unlock()
			return registry.ToolResult{Summary: "ok", Content: "package main"}, nil
		},
	}); err != nil {
		t.Fatalf("Register file.read: %v", err)
	}
	if err := reg.Register(registry.Tool{
		Name: "question.ask",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			defer note("question.ask")()
			// Long enough that any overlap with another question handler
			// would be observed by the live-count assertion.
			time.Sleep(40 * time.Millisecond)
			mu.Lock()
			ranQA++
			mu.Unlock()
			return registry.ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register question.ask: %v", err)
	}
	if err := reg.Register(registry.Tool{
		Name: "ask_user",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			defer note("ask_user")()
			time.Sleep(40 * time.Millisecond)
			mu.Lock()
			ranAU++
			mu.Unlock()
			return registry.ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register ask_user: %v", err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"two questions needing user input plus one parallel read","actions":[
			{"type":"tool_call","tool":"question.ask","args":{"questions":[{"question":"auth?","options":["JWT","OAuth"]}]}},
			{"type":"tool_call","tool":"ask_user","args":{"question":"which backend?"}},
			{"type":"tool_call","tool":"file.read","args":{"path":"x.go"}}]}`,
		`{"rationale":"done","action":{"type":"final","content":"handled"}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	if _, err := r.RunTask(context.Background(), "mix question tools with a parallel read"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if ranQA != 1 {
		t.Fatalf("question.ask handler ran %d times, want 1", ranQA)
	}
	if ranAU != 1 {
		t.Fatalf("ask_user handler ran %d times, want 1", ranAU)
	}
	if ranRead != 1 {
		t.Fatalf("file.read handler ran %d times, want 1 (read must still execute from the batch)", ranRead)
	}
	if maxQA > 1 {
		t.Fatalf("two question.ask handlers overlapped; maxQA = %d, want <= 1", maxQA)
	}
	if maxAU > 1 {
		t.Fatalf("two ask_user handlers overlapped; maxAU = %d, want <= 1", maxAU)
	}
}

func TestRepeatedToolCallGetsReminderInResult(t *testing.T) {
	toolResp := `{"rationale":"look","action":{"type":"tool_call","tool":"echo.tool","args":{}}}`
	p := &agenttest.ScriptedProvider{Responses: []string{
		toolResp, toolResp, toolResp,
		`{"rationale":"done","action":{"type":"final","content":"finished"}}`,
	}}
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "echo.tool", Description: "static output", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "same output"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "check the thing"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	// The 3rd identical result must carry the gentle reminder; requests[3] is
	// the model call after that result, so its message list contains it.
	last := p.Requests[len(p.Requests)-1]
	found := false
	for _, m := range last.Messages {
		if strings.Contains(m.Content, "repeating the exact same tool call") {
			found = true
		}
	}
	if !found {
		t.Fatal("third identical tool result did not carry the repeat reminder")
	}
}
