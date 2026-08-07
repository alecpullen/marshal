package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/skills"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestRunnerDefaultsAreSensible(t *testing.T) {
	if DefaultMaxToolIterations != 100 {
		t.Fatalf("DefaultMaxToolIterations = %d, want 100", DefaultMaxToolIterations)
	}
	if DefaultMaxRetries != 2 {
		t.Fatalf("DefaultMaxRetries = %d, want 2", DefaultMaxRetries)
	}
}

func TestLengthTruncatedToolCallsAreFailedNotExecuted(t *testing.T) {
	executed := false
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"", "all done"},
		ToolCalls:     [][]schema.ToolCall{{{ID: "tc1", Name: "risky.tool", Args: json.RawMessage(`{"partial":`)}}, nil},
		FinishReasons: []string{"length", "stop"},
		ProviderCaps:  schema.ProviderCapabilities{},
	}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "risky.tool", Description: "must not run", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executed = true
			return registry.ToolResult{Summary: "ran"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if executed {
		t.Fatal("tool call from a length-truncated response was executed")
	}
	// The model must have been told to re-issue: the second request carries a
	// role:tool message for tc1 mentioning truncation.
	second := p.Requests[1]
	found := false
	for _, m := range second.Messages {
		if m.Role == schema.RoleTool && m.ToolCallID == "tc1" && strings.Contains(m.Content, "truncated") {
			found = true
		}
	}
	if !found {
		t.Fatal("no corrective tool result for the truncated tool call")
	}
}

func TestChatOnceRoutesThinkingDeltasToStateAndReturnsAnswerText(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Thinking:  []string{"considering the question"},
		Responses: []string{`{"rationale":"r","action":{"type":"answer","content":"done"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	res, err := runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	if err != nil {
		t.Fatalf("chatOnce returned error: %v", err)
	}
	wantText := `{"rationale":"r","action":{"type":"answer","content":"done"}}`
	if res.Text != wantText {
		t.Fatalf("chatOnce returned %q, want %q", res.Text, wantText)
	}
	if got := state.InProgress().Reasoning; got != "considering the question" {
		t.Fatalf("InProgress().Reasoning = %q, want %q", got, "considering the question")
	}
	if state.InProgress().Active {
		t.Fatal("InProgress().Active = true, want false after chatOnce returns")
	}
}

func TestChatOnceEndsStreamingEvenOnProviderError(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Thinking: []string{"partial thought"},
		Errs:     []error{errors.New("boom")},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	_, err := runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	if err == nil {
		t.Fatal("chatOnce returned nil error, want the provider error")
	}
	if got := state.InProgress().Reasoning; got != "partial thought" {
		t.Fatalf("InProgress().Reasoning = %q, want %q (thinking captured before the error must survive EndStreaming)", got, "partial thought")
	}
	if state.InProgress().Active {
		t.Fatal("InProgress().Active = true after error, want false (EndStreaming must still run)")
	}
}

func TestRunAnswersQuestionWithoutToolCalls(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"simple","action":{"type":"answer","content":"Marshal is a TUI coding agent."}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	messages := state.Messages()
	if len(messages) != 3 {
		t.Fatalf("len(messages) = %d, want 3 (user + fallback + assistant): %#v", len(messages), messages)
	}
	if messages[0].Role != session.RoleUser || messages[0].Content != "What does this project do?" {
		t.Fatalf("messages[0] = %#v", messages[0])
	}
	if messages[2].Role != session.RoleAssistant || messages[2].Content != "Marshal is a TUI coding agent." {
		t.Fatalf("messages[2] = %#v", messages[2])
	}
}

func TestRunExecutesAllowedToolCallThenAnswers(t *testing.T) {
	var gotArgs json.RawMessage
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:        "demo.read",
		Description: "reads a demo value",
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			gotArgs = call.Args
			return registry.ToolResult{Summary: "read ok", Content: "demo content"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"need data","action":{"type":"tool_call","tool":"demo.read","args":{"key":"value"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Read demo content successfully."}}`,
	}}
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "Read the demo value"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if string(gotArgs) != `{"key":"value"}` {
		t.Fatalf("tool handler args = %s, want {\"key\":\"value\"}", gotArgs)
	}

	auditLog := state.AuditLog()
	if len(auditLog) != 1 {
		t.Fatalf("len(auditLog) = %d, want 1: %#v", len(auditLog), auditLog)
	}
	if auditLog[0].Approval != registry.ApprovalNotRequired {
		t.Fatalf("auditLog[0].Approval = %q, want %q", auditLog[0].Approval, registry.ApprovalNotRequired)
	}

	messages := state.Messages()
	last := messages[len(messages)-1]
	if last.Role != session.RoleAssistant || last.Content != "Read demo content successfully." {
		t.Fatalf("final message = %#v", last)
	}
}

func TestRunNativeToolCallFeedsRoleToolThenAnswers(t *testing.T) {
	var gotArgs json.RawMessage
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:        "demo.read",
		Description: "reads a demo value",
		Schema:      json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			gotArgs = call.Args
			return registry.ToolResult{Summary: "read ok", Content: "demo content"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{"Reading demo.", "Read demo content successfully."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "call_1", Name: "demo.read", Args: json.RawMessage(`{"key":"value"}`)}},
			nil,
		},
	}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	var got *TurnMetrics
	runner.MetricsObserver = func(m TurnMetrics) { got = &m }

	task, err := runner.RunTask(context.Background(), "Read the demo value")
	if err != nil {
		t.Fatalf("RunTask returned error: %v", err)
	}
	if task.Summary != "Read demo content successfully." {
		t.Fatalf("Summary = %q", task.Summary)
	}
	if string(gotArgs) != `{"key":"value"}` {
		t.Fatalf("tool handler args = %s", gotArgs)
	}
	if got == nil || got.ParseFailures != 0 || got.ToolCalls != 1 {
		t.Fatalf("metrics = %+v, want ParseFailures=0 ToolCalls=1", got)
	}
	if len(p.Requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(p.Requests))
	}
	if len(p.Requests[0].Tools) != 2 {
		t.Fatalf("len(request tools) = %d, want demo.read + ask_user: %+v", len(p.Requests[0].Tools), p.Requests[0].Tools)
	}
	foundToolResult := false
	for _, msg := range p.Requests[1].Messages {
		if msg.Role == schema.RoleTool && msg.ToolCallID == "call_1" && strings.Contains(msg.Content, "demo content") {
			foundToolResult = true
		}
	}
	if !foundToolResult {
		t.Fatalf("second request missing role:tool result for call_1: %#v", p.Requests[1].Messages)
	}
}

func TestRunNativeMultiCallBatchFeedsEachRoleToolInOrder(t *testing.T) {
	var order []string
	reg := registry.New()
	for _, name := range []string{"demo.a", "demo.b"} {
		toolName := name
		if err := reg.Register(registry.Tool{
			Name: toolName,
			Risk: registry.RiskReadOnly,
			Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
				order = append(order, call.Name)
				return registry.ToolResult{Summary: call.Name + " ok", Content: call.Name + " content"}, nil
			},
		}); err != nil {
			t.Fatalf("Register %s: %v", toolName, err)
		}
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{"Reading both.", "Done."},
		ToolCalls: [][]schema.ToolCall{
			{
				{ID: "call_a", Name: "demo.a", Args: json.RawMessage(`{}`)},
				{ID: "call_b", Name: "demo.b", Args: json.RawMessage(`{}`)},
			},
		},
	}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	if _, err := runner.RunTask(context.Background(), "Read both"); err != nil {
		t.Fatalf("RunTask returned error: %v", err)
	}
	if strings.Join(order, ",") != "demo.a,demo.b" {
		t.Fatalf("execution order = %v, want demo.a,demo.b", order)
	}
	var ids []string
	for _, msg := range p.Requests[1].Messages {
		if msg.Role == schema.RoleTool {
			ids = append(ids, msg.ToolCallID)
		}
	}
	if strings.Join(ids, ",") != "call_a,call_b" {
		t.Fatalf("role:tool ids = %v, want call_a,call_b", ids)
	}
}

func TestRunNativeAskUserFeedsAnswerAsRoleTool(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Need clarification.", "Archived as requested."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "call_question", Name: "ask_user", Args: json.RawMessage(`{"question":"Archive or delete?"}`)}},
		},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	questionCh := answerPendingQuestion(state, "archive")

	task, err := r.RunTask(context.Background(), "clean up old records")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if got := <-questionCh; got != "Archive or delete?" {
		t.Fatalf("question = %q", got)
	}
	if task.Summary != "Archived as requested." {
		t.Fatalf("Summary = %q", task.Summary)
	}
	found := false
	for _, msg := range p.Requests[1].Messages {
		if msg.Role == schema.RoleTool && msg.ToolCallID == "call_question" && strings.Contains(msg.Content, "archive") {
			found = true
		}
	}
	if !found {
		t.Fatalf("answer not fed back as role:tool: %#v", p.Requests[1].Messages)
	}
}

func TestRunNativeTruncatedToolNameResolves(t *testing.T) {
	var gotArgs json.RawMessage
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:        "file.read",
		Description: "reads a file",
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			gotArgs = call.Args
			return registry.ToolResult{Summary: "read ok", Content: "file content"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{"Reading.", "Done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "call_1", Name: "read", Args: json.RawMessage(`{"path":"main.go"}`)}},
			nil,
		},
	}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	task, err := runner.RunTask(context.Background(), "Read main.go")
	if err != nil {
		t.Fatalf("RunTask returned error: %v", err)
	}
	if task.Summary != "Done." {
		t.Fatalf("Summary = %q, want Done.", task.Summary)
	}
	if string(gotArgs) != `{"path":"main.go"}` {
		t.Fatalf("tool handler args = %s", gotArgs)
	}
	// Verify the tool result was fed back under the resolved name.
	found := false
	for _, msg := range p.Requests[1].Messages {
		if msg.Role == schema.RoleTool && msg.ToolCallID == "call_1" && strings.Contains(msg.Content, "file content") {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool result not fed back for truncated name: %#v", p.Requests[1].Messages)
	}
}

func TestRunNativeTruncatedToolNameAmbiguousFails(t *testing.T) {
	reg := registry.New()
	for _, name := range []string{"file.read", "db.read"} {
		toolName := name
		if err := reg.Register(registry.Tool{
			Name: toolName,
			Risk: registry.RiskReadOnly,
			Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
				return registry.ToolResult{Summary: toolName + " ok"}, nil
			},
		}); err != nil {
			t.Fatalf("Register %s: %v", toolName, err)
		}
	}

	p := &agenttest.ScriptedProvider{
		Responses: []string{"Trying read.", "Recovered."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "call_ambig", Name: "read", Args: json.RawMessage(`{}`)}},
		},
	}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	if _, err := runner.RunTask(context.Background(), "try ambiguous read"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	// Ambiguous suffix must pass through unchanged -> "unknown tool" error.
	found := false
	for _, msg := range p.Requests[1].Messages {
		if msg.Role == schema.RoleTool && msg.ToolCallID == "call_ambig" && strings.Contains(msg.Content, "unknown tool") {
			found = true
		}
	}
	if !found {
		t.Fatalf("ambiguous truncated name did not produce unknown tool error: %#v", p.Requests[1].Messages)
	}
}

func TestRunNativeUnknownToolAnswersToolCallIDWithError(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Trying unknown.", "Recovered."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "call_bad", Name: "missing.tool", Args: json.RawMessage(`{}`)}},
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	if _, err := r.RunTask(context.Background(), "try a tool"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	found := false
	for _, msg := range p.Requests[1].Messages {
		if msg.Role == schema.RoleTool && msg.ToolCallID == "call_bad" && strings.Contains(msg.Content, "unknown tool") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unknown tool error not fed back as role:tool: %#v", p.Requests[1].Messages)
	}
}

func TestBuildToolDefinitionsOmitsAskUserForSwarmRoles(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:        "demo.read",
		Description: "read",
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.Role = RoleRepoScout

	defs := r.buildToolDefinitions()
	for _, def := range defs {
		if def.Name == "ask_user" {
			t.Fatalf("ask_user should be omitted for swarm role: %+v", defs)
		}
	}
	if len(defs) != 1 || defs[0].Name != "demo_read" {
		t.Fatalf("defs = %+v, want only demo_read (the dotless alias for demo.read)", defs)
	}
}

func TestRunRetriesOnProviderErrorThenSucceeds(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Errs:      []error{errors.New("connection reset"), nil},
		Responses: []string{"", `{"rationale":"ok","action":{"type":"answer","content":"recovered"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "What is this?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if p.Calls != 2 {
		t.Fatalf("provider called %d times, want 2 (1 failure + 1 retry)", p.Calls)
	}
}

func TestRunFailsAfterExhaustingRetries(t *testing.T) {
	failure := errors.New("connection reset")
	p := &agenttest.ScriptedProvider{Errs: []error{failure, failure, failure}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.MaxRetries = 2

	err := runner.Run(context.Background(), "What is this?")
	if err == nil {
		t.Fatal("expected an error after exhausting retries, got nil")
	}
	if state.ProviderError() == nil {
		t.Fatal("expected ProviderError to be set")
	}
}

func TestRunStopsAfterMaxToolIterationsWithoutFinalAnswer(t *testing.T) {
	// The model always tool-calls and never answers, exhausting the tool
	// budget. Since finalize's own model call can still be reached (the
	// provider does not error), the loop salvages a completed task instead
	// of failing outright. See TestExhaustionSalvageFailureReturnsError for
	// the case where the salvage call itself errors.
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
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"loop","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
	}}
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.MaxToolIterations = 2

	task, err := runner.RunTask(context.Background(), "Loop forever")
	if err != nil {
		t.Fatalf("RunTask err = %v, want nil (salvaged)", err)
	}
	if task.Status != TaskStatusCompleted || task.SalvagedReason == "" {
		t.Fatalf("task = %+v, want completed+salvaged", task)
	}
	if len(state.AuditLog()) != 2 {
		t.Fatalf("len(auditLog) = %d, want 2 (bounded by MaxToolIterations)", len(state.AuditLog()))
	}
}

func TestExhaustionSalvagesInsteadOfFailing(t *testing.T) {
	// Model always calls a tool with distinct args (never repeating, so the
	// hard-stall path never fires), and never answers -> the loop runs to
	// exhaustion. finalize (scripted to answer on the next call) must then
	// salvage the turn instead of failing it.
	state := newTestState(t)
	prov := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"loop","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
		`{"rationale":"loop","action":{"type":"tool_call","tool":"file.read","args":{"path":"b.go"}}}`,
		`{"rationale":"loop","action":{"type":"tool_call","tool":"file.read","args":{"path":"c.go"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Salvaged answer."}}`,
	}}
	r := NewRunner(prov, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxToolIterations = 3
	r.MaxRetries = 0
	r.SetForceClass(string(ClassQuestion))

	task, err := r.RunTask(context.Background(), "inspect a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v, want nil (salvaged)", err)
	}
	if task.Status != TaskStatusCompleted || task.SalvagedReason == "" {
		t.Fatalf("task = %+v, want completed+salvaged", task)
	}
}

func TestExhaustionWithoutValidActionFailsHard(t *testing.T) {
	// A model that never emits a parseable action produced nothing to
	// salvage. After maxConsecutiveParseFailures consecutive unparseable
	// responses the loop exits via ErrModelOutputMalformed rather than
	// ErrMaxIterationsExceeded (the model is broken, not merely slow).
	state := newTestState(t)
	prov := &agenttest.ScriptedProvider{Responses: []string{"not json at all"}}
	r := NewRunner(prov, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxToolIterations = 2
	r.MaxRetries = 0
	r.SetForceClass(string(ClassQuestion))

	_, err := r.RunTask(context.Background(), "inspect a.go")
	if !errors.Is(err, ErrModelOutputMalformed) {
		t.Fatalf("err = %v, want ErrModelOutputMalformed", err)
	}
}

func TestExhaustionSalvageFailureReturnsError(t *testing.T) {
	// Same setup as above, but the finalize model call itself errors ->
	// original ErrMaxIterationsExceeded semantics must be preserved.
	state := newTestState(t)
	prov := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"loop","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
			`{"rationale":"loop","action":{"type":"tool_call","tool":"file.read","args":{"path":"b.go"}}}`,
			`{"rationale":"loop","action":{"type":"tool_call","tool":"file.read","args":{"path":"c.go"}}}`,
		},
		Errs: []error{nil, nil, nil, errors.New("boom")},
	}
	r := NewRunner(prov, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxToolIterations = 3
	r.MaxRetries = 0
	r.SetForceClass(string(ClassQuestion))

	_, err := r.RunTask(context.Background(), "inspect a.go")
	if !errors.Is(err, ErrMaxIterationsExceeded) {
		t.Fatalf("err = %v, want ErrMaxIterationsExceeded", err)
	}
}

func TestChatOnceTimesOutPerRequest(t *testing.T) {
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(&blockingProvider{}, reg, pol, state, "test-model")
	runner.ChatTimeout = 50 * time.Millisecond

	start := time.Now()
	_, err := runner.chatOnce(context.Background(), &blockingProvider{}, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("chatOnce returned %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("chatOnce took too long to time out: %v", elapsed)
	}
}

// TestChatOnceNotBoundByApprovalTimeout is the regression test for the
// timeout conflation bug: a short ApprovalTimeout (the TUI approval/question
// wait ceiling) must not cut off a slower-but-working chat completion.
// ChatTimeout is the only field that should bound chatOnce.
func TestChatOnceNotBoundByApprovalTimeout(t *testing.T) {
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	prov := &delayedProvider{delay: 100 * time.Millisecond}
	runner := NewRunner(prov, reg, pol, state, "test-model")
	runner.ApprovalTimeout = 10 * time.Millisecond
	runner.ChatTimeout = 500 * time.Millisecond

	_, err := runner.chatOnce(context.Background(), prov, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	if err != nil {
		t.Fatalf("chatOnce err = %v, want success despite tiny ApprovalTimeout", err)
	}
}

// TestChatOnceDefaultsChatTimeoutWhenUnset covers the ad-hoc subagent path
// (internal/app/app.go's agent.run child runner), which never sets a timeout
// at all. chatOnce must still apply a bounded default rather than blocking
// forever on a stuck connection.
func TestChatOnceDefaultsChatTimeoutWhenUnset(t *testing.T) {
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(&blockingProvider{}, reg, pol, state, "test-model")

	if got := runner.effectiveChatTimeout(); got <= 0 {
		t.Fatalf("effectiveChatTimeout() = %v, want a positive default when ChatTimeout is unset", got)
	}
}

func TestRunResolvesQuestionRouteAndUpdatesModel(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"simple","action":{"type":"answer","content":"ok"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	resolver := &scriptedRouteResolver{
		routes: []routing.Route{{
			Role:    routing.RoleRepoScout,
			Profile: "local_balanced",
			Preset:  routing.ModelPreset{Name: "fast", Provider: "ollama", Model: "fast-model", LocalOnly: true},
		}},
		providers: []provider.Provider{p},
	}
	runner := NewRunner(p, reg, pol, state, "fallback-model")
	runner.RouteResolver = resolver

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(resolver.tasks) != 1 || resolver.tasks[0] != "question" {
		t.Fatalf("resolved tasks = %#v", resolver.tasks)
	}
	if p.Requests[0].Model != "fast-model" {
		t.Fatalf("request model = %q, want fast-model", p.Requests[0].Model)
	}
	route := state.ActiveRoute()
	if route.Role != routing.RoleRepoScout || route.Model != "fast-model" || !route.Active {
		t.Fatalf("ActiveRoute = %#v", route)
	}
}

func TestNormalizeArgsIsStableAcrossKeyOrder(t *testing.T) {
	a, err := normalizeArgs(json.RawMessage(`{"b":1,"a":2}`))
	if err != nil {
		t.Fatalf("normalizeArgs error: %v", err)
	}
	b, err := normalizeArgs(json.RawMessage(`{"a":2,"b":1}`))
	if err != nil {
		t.Fatalf("normalizeArgs error: %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("keys ordered differently produced different normalization: %q vs %q", a, b)
	}
	if string(a) != `{"a":2,"b":1}` {
		t.Fatalf("unexpected normalized form: %q", a)
	}
}

func TestRunnerChatOnceSetsThinkingActivity(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Thinking:  []string{"thinking about it"},
		Responses: []string{`{"rationale":"r","action":{"type":"answer","content":"done"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	_, err := runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil, false)
	if err != nil {
		t.Fatalf("chatOnce returned error: %v", err)
	}

	act := state.Activity()
	if act.Kind != session.ActivityIdle {
		t.Fatalf("activity after chatOnce = %q, want idle", act.Kind)
	}

	if got := state.InProgress().Reasoning; got == "" {
		t.Fatalf("thinking was not captured")
	}
}

func TestRunnerSetsActivityDuringToolExecute(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"let me check","action":{"type":"tool_call","tool":"file.read","args":{"path":"main.go"}}}`,
			`{"rationale":"done","action":{"type":"answer","content":"done"}}`,
		},
	}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.MaxToolIterations = 2

	err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	act := state.Activity()
	if act.Kind != session.ActivityIdle {
		t.Fatalf("activity after run = %q, want idle", act.Kind)
	}
}

func TestRunnerSetsActivityDuringApproval(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"need to run","action":{"type":"tool_call","tool":"shell.run","args":{"command":"go test"}}}`,
			`{"rationale":"done","action":{"type":"answer","content":"done"}}`,
		},
	}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "shell.run",
		Risk: registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.MaxToolIterations = 2

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		err := runner.Run(ctx, "hi")
		if err != nil {
			t.Logf("Run returned: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	act := state.Activity()
	if act.Kind == session.ActivityApproval {
		t.Logf("activity is approval: %v", act.Label)
	}

	tc := state.PendingApproval()
	if tc == nil {
		t.Fatalf("expected pending approval")
	}

	tc.ResponseChan <- session.UserApprovalDecision{Approved: false}

	time.Sleep(100 * time.Millisecond)

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}

	act = state.Activity()
	if act.Kind != session.ActivityIdle {
		t.Fatalf("activity after approval = %q, want idle", act.Kind)
	}
}

func TestRunnerSetsPlanAfterPlanningPhase(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			"Refactor the layout\nAdd tests\nupdate docs",
			`{"rationale":"done","action":{"type":"answer","content":"done"}}`,
		},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.PlanFirst = true
	runner.MaxToolIterations = 2

	err := runner.Run(context.Background(), "build a feature")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	plan := state.Plan()
	if len(plan) != 3 {
		t.Fatalf("Plan() length = %d, want 3: %v", len(plan), plan)
	}
	if plan[0] != "Refactor the layout" || plan[1] != "Add tests" || plan[2] != "update docs" {
		t.Fatalf("Plan() = %v", plan)
	}
}

func TestPlanningStepSkippedByDefault(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"done","action":{"type":"final","content":"edited"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassEdit)) // non-question class used to force planning

	if err := runner.Run(context.Background(), "rename the function"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if p.Calls != 1 {
		t.Fatalf("provider called %d times, want 1 (no separate planning round-trip)", p.Calls)
	}
	if len(state.Plan()) != 0 {
		t.Fatalf("plan was set without PlanFirst: %v", state.Plan())
	}
}

func TestPlanningStepRunsWhenPlanFirstEnabled(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
		"1. Read the file\n2. Edit it",
		`{"rationale":"done","action":{"type":"final","content":"edited"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.PlanFirst = true
	runner.SetForceClass(string(ClassEdit))

	if err := runner.Run(context.Background(), "rename the function"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if p.Calls != 2 {
		t.Fatalf("provider called %d times, want 2 (plan + answer)", p.Calls)
	}
	if len(state.Plan()) == 0 {
		t.Fatal("PlanFirst=true did not set a plan")
	}
}

func TestActionsReadOnlyViolationAdvancesIteration(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "file.write_patch", Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ran"}, nil
		},
	})
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"bad","actions":[{"type":"tool_call","tool":"file.write_patch","args":{}}]}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	r := NewRunner(p, reg, pol, state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	task, err := r.RunTask(context.Background(), "test goal")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("task.Status = %q, want completed", task.Status)
	}
	if got == nil {
		t.Fatal("no TurnMetrics emitted")
	}
	// Without the F-SEC-11 fix, the violation branch does not increment
	// iteration, so Iterations would be 2 (initial parse + final answer).
	// With the fix, the violation branch increments iteration, making it 3.
	if got.Iterations < 3 {
		t.Fatalf("Iterations = %d, want at least 3 (read-only violation must advance iteration budget)", got.Iterations)
	}
	if got.ParseFailures != 1 {
		t.Fatalf("ParseFailures = %d, want 1 (read-only violation counts as parse failure)", got.ParseFailures)
	}
}

func TestRunRejectsNonReadOnlyActions(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "demo.write",
		Risk: registry.RiskCommand, // not read-only
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "written", Content: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"bad parallel","actions":[{"type":"tool_call","tool":"demo.write","args":{}}]}`,
		`{"rationale":"corrected","action":{"type":"final","content":"Done."}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if err := runner.Run(context.Background(), "Try parallel write"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	found := false
	for _, req := range p.Requests {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "read-only") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("missing correction message in provider requests: %#v", p.Requests)
	}
}

func TestRunnerSetsAndClearsActiveToolCall(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			atc, ok := state.ActiveToolCall()
			if !ok {
				t.Error("ActiveToolCall not set during tool handler execution")
			}
			if atc.Name != "file.read" {
				t.Errorf("ActiveToolCall.Name = %q, want file.read", atc.Name)
			}
			return registry.ToolResult{Summary: "read ok", Content: "file contents"}, nil
		},
	})

	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"need file","action":{"type":"tool_call","tool":"file.read","args":{"path":"/repo/main.go"}}}`,
			`{"rationale":"done","action":{"type":"answer","content":"done"}}`,
		},
	}
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if err := runner.Run(context.Background(), "read the file"); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	_, ok := state.ActiveToolCall()
	if ok {
		t.Error("ActiveToolCall still set after Run completed, want cleared")
	}
}

func TestRunnerMarksFinalAnswer(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"simple","action":{"type":"answer","content":"here is the answer"}}`,
		},
	}
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if err := runner.Run(context.Background(), "what is 2+2?"); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	msgs := state.Messages()
	var answer session.Message
	found := false
	for _, m := range msgs {
		if m.Role == session.RoleAssistant && m.Content == "here is the answer" {
			answer = m
			found = true
		}
	}
	if !found {
		t.Fatal("final answer message not found in state")
	}
	if !answer.Final {
		t.Fatal("answer message Final = false, want true")
	}
}

func TestRunLoadsSkillViaToolCall(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("debug", skills.Skill{
		Name:        "debug",
		Description: "Debugging workflow",
		Body:        "# Debug\n\nSteps: reproduce, isolate, fix, verify.\n",
	})

	reg := registry.New()
	state := newTestState(t)

	pol := policy.NewEngine(&config.Config{}, nil)
	skills.RegisterTool(reg, idx, state)

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"need debugging workflow","action":{"type":"tool_call","tool":"skill.load","args":{"name":"debug"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Debug skill loaded and used."}}`,
	}}
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SkillIndex = idx

	if err := runner.Run(context.Background(), "Debug this"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if !state.HasActiveSkill("debug") {
		t.Fatal("HasActiveSkill(debug) = false, want true")
	}

	msgs := state.Messages()
	foundBody := false
	for _, m := range msgs {
		if strings.Contains(m.Content, "# Debug\n\nSteps: reproduce, isolate, fix, verify.\n") {
			foundBody = true
			break
		}
	}
	if !foundBody {
		t.Fatalf("skill body not found in messages: %#v", msgs)
	}

	var systemPromptMsgs []string
	for _, req := range p.Requests {
		for _, msg := range req.Messages {
			if msg.Role == schema.RoleSystem {
				systemPromptMsgs = append(systemPromptMsgs, msg.Content)
			}
		}
	}
	if len(systemPromptMsgs) < 2 {
		t.Fatalf("expected at least 2 provider requests with system messages, got %d", len(systemPromptMsgs))
	}
	if !strings.Contains(systemPromptMsgs[0], "`debug`") {
		t.Fatal("first system prompt should list debug skill")
	}
	if !strings.Contains(systemPromptMsgs[0], "Debugging workflow") {
		t.Fatal("first system prompt should include skill description")
	}
	if !strings.Contains(systemPromptMsgs[1], "ACTIVE") {
		t.Fatal("second system prompt should mark the debug skill active")
	}
}

// The system prompt marks a loaded skill "ACTIVE — body already in
// context", so the body must actually be on the wire. The session-state
// copy is transcript-only and history replay drops system messages, so
// without appendSkillBodies the model was told to follow a skill it had
// never been shown.
func TestRunSendsSkillBodyToProviderAfterLoad(t *testing.T) {
	const body = "# Debug\n\nSteps: reproduce, isolate, fix, verify.\n"

	idx := skills.NewIndex()
	idx.Set("debug", skills.Skill{
		Name:        "debug",
		Description: "Debugging workflow",
		Body:        body,
	})

	reg := registry.New()
	state := newTestState(t)
	pol := policy.NewEngine(&config.Config{}, nil)
	skills.RegisterTool(reg, idx, state)

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"need debugging workflow","action":{"type":"tool_call","tool":"skill.load","args":{"name":"debug"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Debug skill loaded and used."}}`,
	}}
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SkillIndex = idx

	if err := runner.Run(context.Background(), "Debug this"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(p.Requests) < 2 {
		t.Fatalf("expected at least 2 provider requests, got %d", len(p.Requests))
	}

	countBodies := func(req schema.ChatRequest) int {
		n := 0
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, body) {
				n++
			}
		}
		return n
	}

	if n := countBodies(p.Requests[0]); n != 0 {
		t.Fatalf("skill body on the wire before skill.load: %d occurrences", n)
	}
	if n := countBodies(p.Requests[1]); n != 1 {
		t.Fatalf("skill body occurrences on the request after load = %d, want 1", n)
	}
}

func TestRunnerUsesConfiguredRoleInSystemPrompt(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale": "done", "action": {"type": "final", "content": "review complete"}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.Role = RoleReviewer
	runner.SetForceClass("question")

	if err := runner.Run(context.Background(), "review the diff"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.Requests) == 0 {
		t.Fatal("provider was never called")
	}
	system := p.Requests[0].Messages[0].Content
	if !strings.Contains(system, "You are a reviewer") {
		t.Fatalf("system prompt did not use reviewer role:\n%s", system)
	}
}

func TestMaxTurnContextTokensUsesSmallerOfConfiguredAndDerived(t *testing.T) {
	// D1: r.MaxTurnContextTokens is now a hard ceiling, no longer mutated
	// per-call. The derived value flows through effectiveTurnThreshold.
	state := newTestState(t)
	r := NewRunner(nil, nil, nil, state, "test-model")
	r.MaxTurnContextTokens = 100_000 // generous user config
	r.RouteResolver = &staticResolver{route: routing.Route{
		Preset: routing.ModelPreset{Name: "test", Model: "tiny", ContextWindow: 32_000, MaxOutputTokens: 4096},
	}}

	_, _, route := r.resolveRoute(&Task{Class: ClassQuestion})
	got, _, _ := r.effectiveTurnThreshold(route.Window, route.MaxOutput, r.MaxTurnContextTokens)
	// 0.85 * 32000 - 4096 = 23104; the configured ceiling (100000) wins
	// because configured > 0 is a hard ceiling that never exceeds user config.
	if got != 100_000 {
		t.Fatalf("effectiveTurnThreshold = %d, want 100000 (configured ceiling)", got)
	}
}

func TestMaxTurnContextTokensUsesConfiguredWhenLarger(t *testing.T) {
	state := newTestState(t)
	r := NewRunner(nil, nil, nil, state, "test-model")
	r.MaxTurnContextTokens = 100_000
	r.RouteResolver = &staticResolver{route: routing.Route{
		Preset: routing.ModelPreset{Name: "test", Model: "huge", ContextWindow: 200_000, MaxOutputTokens: 8192},
	}}

	_, _, route := r.resolveRoute(&Task{Class: ClassQuestion})
	got, _, _ := r.effectiveTurnThreshold(route.Window, route.MaxOutput, r.MaxTurnContextTokens)
	// 0.85 * 200000 - 8192 = 161808; ceiling keeps the user's 100000.
	if got != 100_000 {
		t.Fatalf("effectiveTurnThreshold = %d, want 100000 (configured ceiling)", got)
	}
}

// TestThinkingLoggedWhenProviderStreamsNoReasoning pins the signal that the
// turn spinner's removal of the "thinking…" label depends on: a model that
// exposes no reasoning must still leave a thinking marker in the transcript.
// The test uses a tool_call action (not answer/final) so it exercises the
// native-tool-call path (first guard at runner.go:671) which logs thinking
// for every model call regardless of action type.
func TestThinkingLoggedWhenProviderStreamsNoReasoning(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"test.txt"}}}`,
		},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "call1", Name: "file.read", Args: json.RawMessage(`{"path":"test.txt"}`)}},
		},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.NativeTools = true

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var thinking []session.TranscriptItem
	for _, item := range state.Transcript() {
		if item.Kind == session.KindThinking {
			thinking = append(thinking, item)
		}
	}
	if len(thinking) == 0 {
		t.Fatal("no KindThinking transcript item logged for a turn with no reasoning text")
	}
	if got := thinking[0].Thinking.Text; got != "" {
		t.Errorf("Thinking.Text = %q, want empty", got)
	}
	if thinking[0].Thinking.StartedAt.IsZero() {
		t.Error("Thinking.StartedAt is zero; duration would be measured from 1970")
	}
}

func TestLengthTruncatedJSONActionIsRefusedNotExecuted(t *testing.T) {
	executed := false
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"r","action":{"type":"tool_call","tool":"risky.tool","args":{}}}`,
			`{"rationale":"r","action":{"type":"answer","content":"done"}}`,
		},
		FinishReasons: []string{"length", "stop"},
	}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "risky.tool", Description: "must not run", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executed = true
			return registry.ToolResult{Summary: "ran"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if executed {
		t.Fatal("tool call from a length-truncated response was executed")
	}
	found := false
	for _, m := range p.Requests[1].Messages {
		if m.Role == schema.RoleUser && strings.Contains(m.Content, "truncated") {
			found = true
		}
	}
	if !found {
		t.Fatal("no corrective user message for the truncated action")
	}
}

// A truncated batch is refused whole: allReadOnly already restricts batches
// to read-only tools, but truncated file.read args are still wrong data.
func TestLengthTruncatedBatchIsRefusedWhole(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"r","actions":[{"type":"tool_call","tool":"file.read","args":{"path":"a.txt"}},{"type":"tool_call","tool":"file.read","args":{"path":"b.txt"}}]}`,
			`{"rationale":"r","action":{"type":"answer","content":"done"}}`,
		},
		FinishReasons: []string{"length", "stop"},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "read two files"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if events := state.AuditLog(); len(events) != 0 {
		t.Fatalf("got %d audit events, want 0 — the whole batch must be refused", len(events))
	}
	found := false
	for _, m := range p.Requests[1].Messages {
		if m.Role == schema.RoleUser && strings.Contains(m.Content, "truncated") && strings.Contains(m.Content, "file.read") {
			found = true
		}
	}
	if !found {
		t.Fatal("no corrective user message naming the refused batch")
	}
}

func TestRunTaskReturnsCompletedTaskWithSummary(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale": "done", "action": {"type": "final", "content": "all findings recorded"}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.SetForceClass("question")

	task, err := runner.RunTask(context.Background(), "scout the repo")
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("task.Status = %q, want %q", task.Status, TaskStatusCompleted)
	}
	if task.Summary != "all findings recorded" {
		t.Fatalf("task.Summary = %q, want final content", task.Summary)
	}
}

// A file read in one turn must not be re-executed when the next turn
// asks for the same thing: the cached content goes back on the wire
// instead. The cache was cleared at the top of every RunTask, so the
// agent paid a tool call to re-read what it had already read.
func TestToolCacheSurvivesAcrossTurns(t *testing.T) {
	calls := 0
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "demo.read", Description: "reads", Risk: registry.RiskReadOnly, Cacheable: true,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			calls++
			return registry.ToolResult{Summary: "read ok", Content: "demo content"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"read","action":{"type":"tool_call","tool":"demo.read","args":{"key":"v"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"turn one"}}`,
		`{"rationale":"read again","action":{"type":"tool_call","tool":"demo.read","args":{"key":"v"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"turn two"}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if err := runner.Run(context.Background(), "read it"); err != nil {
		t.Fatalf("turn one: %v", err)
	}
	if err := runner.Run(context.Background(), "read it again"); err != nil {
		t.Fatalf("turn two: %v", err)
	}

	if calls != 1 {
		t.Fatalf("tool handler ran %d times, want 1 (second turn must hit the cache)", calls)
	}

	last := p.Requests[len(p.Requests)-1]
	found := false
	for _, m := range last.Messages {
		if strings.Contains(m.Content, "demo content") && strings.Contains(m.Content, "(cached)") {
			found = true
		}
	}
	if !found {
		t.Fatal("cached content did not reach the wire on the second turn")
	}
}

// A write between two identical reads must invalidate: serving the
// pre-write content as current is a correctness bug.
func TestWriteInvalidatesToolCache(t *testing.T) {
	reads := 0
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "demo.read", Description: "reads", Risk: registry.RiskReadOnly, Cacheable: true,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			reads++
			return registry.ToolResult{Summary: "read ok", Content: "demo content"}, nil
		},
	}); err != nil {
		t.Fatalf("Register read: %v", err)
	}
	if err := reg.Register(registry.Tool{
		Name: "demo.write", Description: "writes", Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "wrote"}, nil
		},
	}); err != nil {
		t.Fatalf("Register write: %v", err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"read","action":{"type":"tool_call","tool":"demo.read","args":{"key":"v"}}}`,
		`{"rationale":"write","action":{"type":"tool_call","tool":"demo.write","args":{}}}`,
		`{"rationale":"read again","action":{"type":"tool_call","tool":"demo.read","args":{"key":"v"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"done"}}`,
	}}
	state := newTestState(t)
	pol := policy.NewEngine(&config.Config{}, nil)
	pol.SetApprovalMode(policy.ModeAuto)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "read, write, read"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reads != 2 {
		t.Fatalf("read handler ran %d times, want 2 (the write must invalidate the cache)", reads)
	}
}
