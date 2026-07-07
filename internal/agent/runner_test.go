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

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/skills"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestRunnerDefaultsAreSensible(t *testing.T) {
	if DefaultMaxToolIterations != 16 {
		t.Fatalf("DefaultMaxToolIterations = %d, want 16", DefaultMaxToolIterations)
	}
	if DefaultMaxRetries != 2 {
		t.Fatalf("DefaultMaxRetries = %d, want 2", DefaultMaxRetries)
	}
}

// scriptedProvider returns pre-canned responses in call order. Each call to
// Chat consumes the next entry from responses/errs (whichever is non-empty
// at that index); once the scripts run out, the last response is repeated
// so tests exercising max-iteration limits do not need to script every turn.
type scriptedProvider struct {
	responses    []string
	toolCalls    [][]schema.ToolCall
	thinking     []string
	errs         []error
	usages       []*schema.TokenUsage
	calls        int
	requests     []schema.ChatRequest
	capabilities schema.ProviderCapabilities
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}

func (p *scriptedProvider) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) {
	return schema.EmbedResponse{}, nil
}

func (p *scriptedProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return p.capabilities
}

func (p *scriptedProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	idx := p.calls
	p.requests = append(p.requests, req)
	p.calls++

	ch := make(chan schema.ChatEvent, 3)
	if idx < len(p.thinking) && p.thinking[idx] != "" {
		ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: p.thinking[idx]}
	}

	if idx < len(p.errs) && p.errs[idx] != nil {
		ch <- schema.ChatEvent{Type: schema.ChatEventError, Err: p.errs[idx]}
		close(ch)
		return ch, nil
	}

	content := ""
	switch {
	case idx < len(p.responses):
		content = p.responses[idx]
	case len(p.responses) > 0:
		content = p.responses[len(p.responses)-1]
	}
	if content != "" {
		ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: content}
	}
	done := schema.ChatEvent{Type: schema.ChatEventDone}
	if idx < len(p.usages) {
		done.Usage = p.usages[idx]
	}
	if idx < len(p.toolCalls) {
		done.ToolCalls = p.toolCalls[idx]
	}
	ch <- done
	close(ch)
	return ch, nil
}

type scriptedRouteResolver struct {
	routes    []routing.Route
	providers []provider.Provider
	errs      []error
	tasks     []routing.TaskProfile
}

func (r *scriptedRouteResolver) Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error) {
	r.tasks = append(r.tasks, task)
	if len(r.errs) > 0 {
		err := r.errs[0]
		r.errs = r.errs[1:]
		if err != nil {
			return routing.Route{}, nil, err
		}
	}
	if len(r.routes) == 0 {
		return routing.Route{}, nil, routing.ErrNoRoute
	}
	route := r.routes[0]
	r.routes = r.routes[1:]
	var p provider.Provider
	if len(r.providers) > 0 {
		p = r.providers[0]
		r.providers = r.providers[1:]
	}
	return route, p, nil
}

type fakeMemoryProvider struct {
	memories []contextpack.MemoryNote
	err      error
}

func (f *fakeMemoryProvider) Memories(projectID int64) ([]contextpack.MemoryNote, error) {
	return f.memories, f.err
}

func newTestState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

func TestChatOnceRoutesThinkingDeltasToStateAndReturnsAnswerText(t *testing.T) {
	p := &scriptedProvider{
		thinking:  []string{"considering the question"},
		responses: []string{`{"rationale":"r","action":{"type":"answer","content":"done"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	res, err := runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}})
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
	p := &scriptedProvider{
		thinking: []string{"partial thought"},
		errs:     []error{errors.New("boom")},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	_, err := runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}})
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
	p := &scriptedProvider{responses: []string{
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
	if len(messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2 (user + assistant): %#v", len(messages), messages)
	}
	if messages[0].Role != session.RoleUser || messages[0].Content != "What does this project do?" {
		t.Fatalf("messages[0] = %#v", messages[0])
	}
	if messages[1].Role != session.RoleAssistant || messages[1].Content != "Marshal is a TUI coding agent." {
		t.Fatalf("messages[1] = %#v", messages[1])
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

	p := &scriptedProvider{responses: []string{
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

	p := &scriptedProvider{
		responses: []string{"Reading demo.", "Read demo content successfully."},
		toolCalls: [][]schema.ToolCall{
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
	if len(p.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(p.requests))
	}
	if len(p.requests[0].Tools) != 2 {
		t.Fatalf("len(request tools) = %d, want demo.read + ask_user: %+v", len(p.requests[0].Tools), p.requests[0].Tools)
	}
	foundToolResult := false
	for _, msg := range p.requests[1].Messages {
		if msg.Role == schema.RoleTool && msg.ToolCallID == "call_1" && strings.Contains(msg.Content, "demo content") {
			foundToolResult = true
		}
	}
	if !foundToolResult {
		t.Fatalf("second request missing role:tool result for call_1: %#v", p.requests[1].Messages)
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

	p := &scriptedProvider{
		responses: []string{"Reading both.", "Done."},
		toolCalls: [][]schema.ToolCall{
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
	for _, msg := range p.requests[1].Messages {
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
	p := &scriptedProvider{
		responses: []string{"Need clarification.", "Archived as requested."},
		toolCalls: [][]schema.ToolCall{
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
	for _, msg := range p.requests[1].Messages {
		if msg.Role == schema.RoleTool && msg.ToolCallID == "call_question" && strings.Contains(msg.Content, "archive") {
			found = true
		}
	}
	if !found {
		t.Fatalf("answer not fed back as role:tool: %#v", p.requests[1].Messages)
	}
}

func TestRunNativeUnknownToolAnswersToolCallIDWithError(t *testing.T) {
	p := &scriptedProvider{
		responses: []string{"Trying unknown.", "Recovered."},
		toolCalls: [][]schema.ToolCall{
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
	for _, msg := range p.requests[1].Messages {
		if msg.Role == schema.RoleTool && msg.ToolCallID == "call_bad" && strings.Contains(msg.Content, "unknown tool") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unknown tool error not fed back as role:tool: %#v", p.requests[1].Messages)
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
	r := NewRunner(&scriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.Role = RoleRepoScout

	defs := r.buildToolDefinitions()
	for _, def := range defs {
		if def.Name == "ask_user" {
			t.Fatalf("ask_user should be omitted for swarm role: %+v", defs)
		}
	}
	if len(defs) != 1 || defs[0].Name != "demo.read" {
		t.Fatalf("defs = %+v, want only demo.read", defs)
	}
}

func TestRunRequiresApprovalForShellRunAndRespectsApproval(t *testing.T) {
	reg := registry.New()
	executed := make(chan struct{}, 1)
	if err := reg.Register(registry.Tool{
		Name:        "shell.run",
		Description: "runs a shell command",
		Risk:        registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executed <- struct{}{}
			return registry.ToolResult{Summary: "ran ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// "Run echo hi" matches the "run" command keyword, so Classify returns
	// ClassCommand and Run() issues one extra planning-phase provider call
	// (freeform text, not JSON) before entering the tool-call loop. The
	// first scripted response below satisfies that planning call; the
	// second and third are consumed by the tool_call/final loop iterations.
	p := &scriptedProvider{responses: []string{
		"1. Run the requested command.",
		`{"rationale":"check status","action":{"type":"tool_call","tool":"shell.run","args":{"command":"echo hi"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Command ran."}}`,
	}}
	cfg := config.Default()
	pol := policy.NewEngine(&cfg, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	runErr := make(chan error, 1)
	go func() {
		runErr <- runner.Run(context.Background(), "Run echo hi")
	}()

	var tc *session.PendingToolCall
	deadline := time.After(2 * time.Second)
	for tc == nil {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for pending approval")
		default:
			tc = state.PendingApproval()
		}
	}
	if tc.Name != "shell.run" || tc.Command != "echo hi" {
		t.Fatalf("pending approval = %#v", tc)
	}
	tc.ResponseChan <- session.UserApprovalDecision{Approved: true}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to finish")
	}

	select {
	case <-executed:
	default:
		t.Fatal("tool handler was never executed")
	}
}

func TestRunRetriesOnProviderErrorThenSucceeds(t *testing.T) {
	p := &scriptedProvider{
		errs:      []error{errors.New("connection reset"), nil},
		responses: []string{"", `{"rationale":"ok","action":{"type":"answer","content":"recovered"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "What is this?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if p.calls != 2 {
		t.Fatalf("provider called %d times, want 2 (1 failure + 1 retry)", p.calls)
	}
}

func TestMergeMemoriesRemovesExistingMemorySectionWhenProviderReturnsNone(t *testing.T) {
	state := newTestState(t)
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Content: "Project: marshal", EstimatedTokens: 4},
			{Kind: contextpack.SectionMemory, Title: "Project Memories", Content: "[fact] stale note", EstimatedTokens: 3},
			{Kind: contextpack.SectionPlan, Content: "1. Inspect", EstimatedTokens: 3},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 10},
	})

	runner := NewRunner(&scriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.MemoryProvider = &fakeMemoryProvider{}

	runner.mergeMemories(0)

	for _, section := range state.ContextPack().Sections {
		if section.Kind == contextpack.SectionMemory {
			t.Fatalf("stale memory section remained in context pack: %#v", state.ContextPack().Sections)
		}
	}
}

func TestRunFailsAfterExhaustingRetries(t *testing.T) {
	failure := errors.New("connection reset")
	p := &scriptedProvider{errs: []error{failure, failure, failure}}
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
	p := &scriptedProvider{responses: []string{
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
	prov := &scriptedProvider{responses: []string{
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
	prov := &scriptedProvider{responses: []string{"not json at all"}}
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
	prov := &scriptedProvider{
		responses: []string{
			`{"rationale":"loop","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
			`{"rationale":"loop","action":{"type":"tool_call","tool":"file.read","args":{"path":"b.go"}}}`,
			`{"rationale":"loop","action":{"type":"tool_call","tool":"file.read","args":{"path":"c.go"}}}`,
		},
		errs: []error{nil, nil, nil, errors.New("boom")},
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

func TestRunInjectsStoredContextPack(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale":"simple","action":{"type":"answer","content":"Marshal is indexed."}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	})
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(p.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(p.requests))
	}
	var found bool
	for _, msg := range p.requests[0].Messages {
		if strings.Contains(msg.Content, "Project context pack:") && strings.Contains(msg.Content, "Project: marshal") {
			found = true
		}
	}
	if !found {
		t.Fatalf("request missing context pack: %#v", p.requests[0].Messages)
	}
}

func TestRunOmitsContextPackWhenEmpty(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale":"simple","action":{"type":"answer","content":"No pack."}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	for _, msg := range p.requests[0].Messages {
		if strings.Contains(msg.Content, "Project context pack:") {
			t.Fatalf("empty context pack was injected: %#v", p.requests[0].Messages)
		}
	}
}

func TestRunMergesMemoriesIntoContextPackBeforeFirstMessage(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale":"simple","action":{"type":"answer","content":"done"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.MemoryProvider = &fakeMemoryProvider{memories: []contextpack.MemoryNote{
		{Kind: "fact", Content: "Uses SQLite for persistence"},
	}}
	runner.ProjectID = 7

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(p.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(p.requests))
	}

	var contextMessage string
	for _, msg := range p.requests[0].Messages {
		if strings.Contains(msg.Content, "Project context pack:") {
			contextMessage = msg.Content
			break
		}
	}
	if contextMessage == "" {
		t.Fatalf("first provider request missing context pack: %#v", p.requests[0].Messages)
	}
	if !strings.Contains(contextMessage, "## Project Memories") || !strings.Contains(contextMessage, "Uses SQLite for persistence") {
		t.Fatalf("first provider request missing memory content:\n%s", contextMessage)
	}
	userIdx := -1
	for i, msg := range p.requests[0].Messages {
		if msg.Role == schema.RoleUser && msg.Content == "What does this project do?" {
			userIdx = i
			break
		}
	}
	if userIdx == -1 {
		t.Fatalf("first provider request missing user message: %#v", p.requests[0].Messages)
	}
	contextIdx := -1
	for i, msg := range p.requests[0].Messages {
		if msg.Content == contextMessage {
			contextIdx = i
			break
		}
	}
	if contextIdx == -1 || contextIdx > userIdx {
		t.Fatalf("context pack should precede user message: %#v", p.requests[0].Messages)
	}

	pack := state.ContextPack()
	found := false
	for _, section := range pack.Sections {
		if section.Kind == contextpack.SectionMemory && strings.Contains(section.Content, "Uses SQLite for persistence") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a memory section in context pack, got %#v", pack.Sections)
	}
}

func TestRunWithoutMemoryProviderLeavesContextPackEmpty(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale":"simple","action":{"type":"answer","content":"done"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	pack := state.ContextPack()
	if !pack.IsEmpty() {
		t.Fatalf("expected empty context pack when MemoryProvider is nil, got %#v", pack.Sections)
	}
}

func TestRunSwallowsMemoryProviderErrorsWithoutInjectingMemorySection(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale":"simple","action":{"type":"answer","content":"done"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.MemoryProvider = &fakeMemoryProvider{err: errors.New("memory backend unavailable")}
	runner.ProjectID = 7

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(p.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(p.requests))
	}
	for _, msg := range p.requests[0].Messages {
		if strings.Contains(msg.Content, "Project context pack:") {
			t.Fatalf("unexpected context pack injected after memory provider error: %#v", p.requests[0].Messages)
		}
	}

	pack := state.ContextPack()
	for _, section := range pack.Sections {
		if section.Kind == contextpack.SectionMemory {
			t.Fatalf("unexpected memory section after provider error: %#v", pack.Sections)
		}
	}
	if got := state.Messages(); len(got) != 2 || got[1].Content != "done" {
		t.Fatalf("turn did not complete successfully: %#v", got)
	}
}

func TestRunAddsPlanToContextPackForActionCalls(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		"1. Inspect the repo.\n2. Run the demo tool.",
		`{"rationale":"need data","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
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
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	})
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "Add a test"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(p.requests) < 2 {
		t.Fatalf("provider requests = %d, want at least 2", len(p.requests))
	}
	var foundPlan bool
	for _, msg := range p.requests[1].Messages {
		if strings.Contains(msg.Content, "## Current Plan") && strings.Contains(msg.Content, "Inspect the repo") {
			foundPlan = true
		}
	}
	if !foundPlan {
		t.Fatalf("action request missing plan context: %#v", p.requests[1].Messages)
	}
}

func TestRunAddsPlanToContextPackBeforeSnippetsAndToolOutput(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		"1. Inspect the repo.\n2. Run the demo tool.",
		`{"rationale":"need data","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
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
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
			{Kind: contextpack.SectionFileSnippet, Title: "internal/app/app.go", Content: "package app", EstimatedTokens: 3},
			{Kind: contextpack.SectionToolOutput, Title: "go.test", Content: "ok", EstimatedTokens: 1},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 8},
	})
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "Add a test"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var actionContext string
	for _, msg := range p.requests[1].Messages {
		if strings.Contains(msg.Content, "Project context pack:") {
			actionContext = msg.Content
			break
		}
	}
	if actionContext == "" {
		t.Fatalf("action request missing context pack: %#v", p.requests[1].Messages)
	}

	planIdx := strings.Index(actionContext, "## Current Plan")
	snippetIdx := strings.Index(actionContext, "## internal/app/app.go")
	toolIdx := strings.Index(actionContext, "## go.test")
	if planIdx == -1 || snippetIdx == -1 || toolIdx == -1 {
		t.Fatalf("missing expected sections in action context:\n%s", actionContext)
	}
	if !(planIdx < snippetIdx && snippetIdx < toolIdx) {
		t.Fatalf("section order wrong in action context:\n%s", actionContext)
	}
}

func TestRunPreservesContextPackSectionMetadataWhenAddingPlan(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		"1. Inspect the repo.\n2. Run the demo tool.",
		`{"rationale":"need data","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
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
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{
				Kind:            contextpack.SectionFileSnippet,
				Title:           "internal/app/app.go",
				Content:         "package app",
				Source:          "internal/app/app.go:1-3",
				Priority:        30,
				EstimatedTokens: 3,
			},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 3},
	})
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "Add a test"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	pack := state.ContextPack()
	if len(pack.Sections) != 2 {
		t.Fatalf("len(pack.Sections) = %d, want 2: %#v", len(pack.Sections), pack.Sections)
	}

	var snippet *contextpack.Section
	for i := range pack.Sections {
		if pack.Sections[i].Kind == contextpack.SectionFileSnippet {
			snippet = &pack.Sections[i]
			break
		}
	}
	if snippet == nil {
		t.Fatalf("missing file snippet section: %#v", pack.Sections)
	}
	if snippet.Source != "internal/app/app.go:1-3" {
		t.Fatalf("snippet.Source = %q, want %q", snippet.Source, "internal/app/app.go:1-3")
	}
}

type blockingProvider struct{}

func (p *blockingProvider) Name() string { return "blocking" }

func (p *blockingProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) { return nil, nil }

func (p *blockingProvider) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) {
	return schema.EmbedResponse{}, nil
}

func (p *blockingProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func (p *blockingProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	events := make(chan schema.ChatEvent)
	go func() {
		defer close(events)
		<-ctx.Done()
		events <- schema.ChatEvent{Type: schema.ChatEventError, Err: ctx.Err()}
	}()
	return events, nil
}

func TestChatOnceTimesOutPerRequest(t *testing.T) {
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(&blockingProvider{}, reg, pol, state, "test-model")
	runner.RequestTimeout = 50 * time.Millisecond

	start := time.Now()
	_, err := runner.chatOnce(context.Background(), &blockingProvider{}, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}})
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("chatOnce returned %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("chatOnce took too long to time out: %v", elapsed)
	}
}

func TestRunResolvesQuestionRouteAndUpdatesModel(t *testing.T) {
	p := &scriptedProvider{responses: []string{
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
	if len(resolver.tasks) != 1 || resolver.tasks[0].Class != "question" {
		t.Fatalf("resolved tasks = %#v", resolver.tasks)
	}
	if p.requests[0].Model != "fast-model" {
		t.Fatalf("request model = %q, want fast-model", p.requests[0].Model)
	}
	route := state.ActiveRoute()
	if route.Role != routing.RoleRepoScout || route.Model != "fast-model" || !route.Active {
		t.Fatalf("ActiveRoute = %#v", route)
	}
}

func TestRunAppliesRouteContextBudgetToExistingPack(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		"1. Inspect.\n2. Edit.",
		`{"rationale":"done","action":{"type":"final","content":"ok"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	state.SetContextPack(contextpack.Pack{
		Sections:   []contextpack.Section{{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4}},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	})
	resolver := &scriptedRouteResolver{
		routes: []routing.Route{{
			Role:          routing.RoleImplementer,
			Preset:        routing.ModelPreset{Name: "coder", Provider: "ollama", Model: "coder-model", LocalOnly: true},
			ContextBudget: routing.ContextBudget{MaxRepoContextTokens: 24000},
		}},
		providers: []provider.Provider{p},
	}
	runner := NewRunner(p, reg, pol, state, "fallback-model")
	runner.RouteResolver = resolver

	if err := runner.Run(context.Background(), "Add a test"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	pack := state.ContextPack()
	if pack.TokenUsage.MaxTokens != 24000 {
		t.Fatalf("pack max tokens = %d, want 24000", pack.TokenUsage.MaxTokens)
	}
}

func TestRunAppliesRouteContextBudgetToMemoryOnlyPack(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale":"done","action":{"type":"answer","content":"ok"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	resolver := &scriptedRouteResolver{
		routes: []routing.Route{{
			Role:          routing.RoleImplementer,
			Preset:        routing.ModelPreset{Name: "coder", Provider: "ollama", Model: "coder-model", LocalOnly: true},
			ContextBudget: routing.ContextBudget{MaxRepoContextTokens: 8},
		}},
		providers: []provider.Provider{p},
	}
	runner := NewRunner(p, reg, pol, state, "fallback-model")
	runner.RouteResolver = resolver
	runner.MemoryProvider = &fakeMemoryProvider{memories: []contextpack.MemoryNote{
		{Kind: "fact", Content: strings.Repeat("m", 64)},
	}}
	runner.ProjectID = 7

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	pack := state.ContextPack()
	if pack.TokenUsage.MaxTokens != 8 {
		t.Fatalf("pack max tokens = %d, want 8", pack.TokenUsage.MaxTokens)
	}
	if !pack.TokenUsage.Truncated {
		t.Fatalf("expected memory-only pack to be truncated by route budget: %#v", pack.TokenUsage)
	}
	var memory *contextpack.Section
	for i := range pack.Sections {
		if pack.Sections[i].Kind == contextpack.SectionMemory {
			memory = &pack.Sections[i]
			break
		}
	}
	if memory == nil {
		t.Fatalf("expected memory section in pack: %#v", pack.Sections)
	}
	if !strings.Contains(memory.Content, "...[truncated]") {
		t.Fatalf("expected truncated memory content, got %q", memory.Content)
	}
}

func TestRunFallsBackToOriginalProviderAndModelAfterResolverError(t *testing.T) {
	fallbackProvider := &scriptedProvider{responses: []string{
		`{"rationale":"fallback","action":{"type":"answer","content":"fallback ok"}}`,
	}}
	routedProvider := &scriptedProvider{responses: []string{
		`{"rationale":"routed","action":{"type":"answer","content":"route ok"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	resolverErr := errors.New("resolver unavailable")
	resolver := &scriptedRouteResolver{
		routes: []routing.Route{{
			Role:    routing.RoleRepoScout,
			Profile: "local_balanced",
			Preset:  routing.ModelPreset{Name: "fast", Provider: "ollama", Model: "fast-model", LocalOnly: true},
		}},
		providers: []provider.Provider{routedProvider},
		errs:      []error{nil, resolverErr},
	}
	runner := NewRunner(fallbackProvider, reg, pol, state, "fallback-model")
	runner.RouteResolver = resolver

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("first Run returned error: %v", err)
	}
	if err := runner.Run(context.Background(), "What is the fallback model?"); err != nil {
		t.Fatalf("second Run returned error: %v", err)
	}

	if len(routedProvider.requests) != 1 {
		t.Fatalf("routed provider requests = %d, want 1", len(routedProvider.requests))
	}
	if routedProvider.requests[0].Model != "fast-model" {
		t.Fatalf("routed request model = %q, want fast-model", routedProvider.requests[0].Model)
	}
	if len(fallbackProvider.requests) != 1 {
		t.Fatalf("fallback provider requests = %d, want 1", len(fallbackProvider.requests))
	}
	if fallbackProvider.requests[0].Model != "fallback-model" {
		t.Fatalf("fallback request model = %q, want fallback-model", fallbackProvider.requests[0].Model)
	}
	if got := state.ProviderError(); !errors.Is(got, resolverErr) {
		t.Fatalf("ProviderError = %v, want %v", got, resolverErr)
	}
	if route := state.ActiveRoute(); route.Active {
		t.Fatalf("ActiveRoute = %#v, want inactive after resolver error fallback", route)
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

	p := &scriptedProvider{responses: []string{
		"1. Read the demo value twice.",
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

	p := &scriptedProvider{responses: []string{
		"1. Read both demo values.",
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

	p := &scriptedProvider{responses: []string{
		"1. Read the demo value.",
		`{"rationale":"loop","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
		`{"rationale":"loop","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
		`{"rationale":"loop","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.MaxToolIterations = 5

	// Three identical tool calls in a row is an exact-repeat hard stall (see
	// progressTracker.assess), which now forces an immediate final answer
	// via finalize rather than a soft nudge-and-continue. The 5th scripted
	// response ("Done.") is what finalize's forced call receives.
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

func TestRunSummarizesLargeToolResults(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "demo.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "big file", Content: strings.Repeat("x", DefaultMaxToolResultChars+100)}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &scriptedProvider{responses: []string{
		"1. Read the big file.",
		`{"rationale":"read","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if err := runner.Run(context.Background(), "Read the big file"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	foundTruncated := false
	for _, ev := range state.AuditLog() {
		if strings.Contains(ev.ResultSummary, "[truncated]") {
			foundTruncated = true
			break
		}
	}
	if !foundTruncated {
		t.Fatalf("large tool result was not truncated in audit log: %#v", state.AuditLog())
	}
}

func TestRunnerChatOnceSetsThinkingActivity(t *testing.T) {
	p := &scriptedProvider{
		thinking:  []string{"thinking about it"},
		responses: []string{`{"rationale":"r","action":{"type":"answer","content":"done"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	_, err := runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}})
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
	p := &scriptedProvider{
		responses: []string{
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
	p := &scriptedProvider{
		responses: []string{
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
	p := &scriptedProvider{
		responses: []string{
			"Refactor the layout\nAdd tests\nupdate docs",
			`{"rationale":"done","action":{"type":"answer","content":"done"}}`,
		},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
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

	p := &scriptedProvider{responses: []string{
		"1. Try parallel write.",
		`{"rationale":"bad parallel","actions":[{"type":"tool_call","tool":"demo.write","args":{}}]}`,
		`{"rationale":"corrected","action":{"type":"final","content":"Done."}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if err := runner.Run(context.Background(), "Try parallel write"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	found := false
	for _, req := range p.requests {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "read-only") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("missing correction message in provider requests: %#v", p.requests)
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

	p := &scriptedProvider{
		responses: []string{
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
	p := &scriptedProvider{
		responses: []string{
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

	p := &scriptedProvider{responses: []string{
		"1. Load the debug skill.",
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
		if m.Content == "# Debug\n\nSteps: reproduce, isolate, fix, verify.\n" {
			foundBody = true
			break
		}
	}
	if !foundBody {
		t.Fatalf("skill body not found in messages: %#v", msgs)
	}

	var systemPromptMsgs []string
	for _, req := range p.requests {
		for _, msg := range req.Messages {
			if msg.Role == schema.RoleSystem {
				systemPromptMsgs = append(systemPromptMsgs, msg.Content)
			}
		}
	}
	if len(systemPromptMsgs) < 3 {
		t.Fatalf("expected at least 3 provider requests with system messages, got %d", len(systemPromptMsgs))
	}
	if !strings.Contains(systemPromptMsgs[0], "`debug`") {
		t.Fatal("first system prompt should list debug skill")
	}
	if !strings.Contains(systemPromptMsgs[0], "Debugging workflow") {
		t.Fatal("first system prompt should include skill description")
	}
	if !strings.Contains(systemPromptMsgs[2], "Active Skills") {
		t.Fatal("third system prompt should show Active Skills")
	}
}

func TestRunnerUsesConfiguredRoleInSystemPrompt(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale": "done", "action": {"type": "final", "content": "review complete"}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.Role = RoleReviewer
	runner.SetForceClass("question")

	if err := runner.Run(context.Background(), "review the diff"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.requests) == 0 {
		t.Fatal("provider was never called")
	}
	system := p.requests[0].Messages[0].Content
	if !strings.Contains(system, "You are a reviewer") {
		t.Fatalf("system prompt did not use reviewer role:\n%s", system)
	}
}

func TestRunTaskReturnsCompletedTaskWithSummary(t *testing.T) {
	p := &scriptedProvider{responses: []string{
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

type recordingGate struct {
	mu           sync.Mutex
	acquisitions int
}

func (g *recordingGate) Acquire() (release func()) {
	g.mu.Lock()
	g.acquisitions++
	return g.mu.Unlock
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

	p := &scriptedProvider{responses: []string{
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

func TestRunnerNonShellToolApprovalAndJSONEditing(t *testing.T) {
	reg := registry.New()
	var calledArgs string
	if err := reg.Register(registry.Tool{
		Name: "mcp.github.create_issue", Description: "creates a github issue", Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			calledArgs = string(call.Args)
			return registry.ToolResult{Summary: "created"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	p := &scriptedProvider{responses: []string{
		`{"rationale": "call tool", "action": {"type": "tool_call", "tool": "mcp.github.create_issue", "args": {"title": "old title", "body": "old body"}}}`,
		`{"rationale": "done", "action": {"type": "final", "content": "done"}}`,
	}}

	state := newTestState(t)

	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.SetForceClass("question")

	go func() {
		for state.PendingApproval() == nil {
			time.Sleep(10 * time.Millisecond)
		}
		tc := state.PendingApproval()
		if tc.Name != "mcp.github.create_issue" {
			t.Errorf("tc.Name = %q, want mcp.github.create_issue", tc.Name)
		}
		if tc.Schema != "creates a github issue" {
			t.Errorf("tc.Schema = %q, want 'creates a github issue'", tc.Schema)
		}
		tc.ResponseChan <- session.UserApprovalDecision{
			Approved: true,
			Edited:   `{"title":"new title","body":"new body"}`,
		}
	}()

	if err := runner.Run(context.Background(), "create issue"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantArgs := `{"title":"new title","body":"new body"}`
	if calledArgs != wantArgs {
		t.Errorf("calledArgs = %q, want %q", calledArgs, wantArgs)
	}
}

func TestRunNudgeNamesRepeatedCall(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "package main"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	read := func(path string) string {
		return fmt.Sprintf(`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":%q}}}`, path)
	}
	// Three novel reads, then the same three again: the 6th call makes the
	// trailing three all duplicates -> soft stall -> nudge; the model then
	// answers normally on the 7th response.
	p := &scriptedProvider{responses: []string{
		read("a.go"), read("b.go"), read("c.go"),
		read("a.go"), read("b.go"), read("c.go"),
		`{"rationale":"done","action":{"type":"final","content":"Answer."}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	task, err := r.RunTask(context.Background(), "how does pkg work?")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.SalvagedReason != "" || task.Summary != "Answer." {
		t.Fatalf("task = %+v, want un-salvaged completion with Summary=Answer.", task)
	}
	foundNudge := false
	for _, m := range state.Messages() {
		if m.Role == session.RoleSystem &&
			strings.Contains(m.Content, "file.read") &&
			strings.Contains(m.Content, "c.go") {
			foundNudge = true
		}
	}
	if !foundNudge {
		t.Fatal("expected a soft-stall nudge naming the repeated call (file.read c.go)")
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

	p := &scriptedProvider{responses: responses}
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
	if p.calls != 6 {
		t.Fatalf("provider calls = %d, want 6 (5 reads + 1 final, no finalize calls)", p.calls)
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

	p := &scriptedProvider{responses: []string{
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
	if len(executed) != 5 {
		t.Fatalf("executed %d reads, want 5 (batch of 4 + 1 follow-up)", len(executed))
	}
	if task.SalvagedReason != "" || task.Summary != "REAL ANSWER." {
		t.Fatalf("task = %+v, want un-salvaged completion with the model's answer", task)
	}
	if p.calls != 3 {
		t.Fatalf("provider calls = %d, want 3 (batch, single read, final)", p.calls)
	}
}

func TestParseFailuresDoNotConsumeToolIterations(t *testing.T) {
	p := &scriptedProvider{
		responses: []string{
			"not a json action at all",
			`{"rationale":"done","action":{"type":"final","content":"Answer."}}`,
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
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
	if got.Iterations != 1 {
		t.Fatalf("Iterations = %d, want 1 (parse failure must not consume budget)", got.Iterations)
	}
	if got.ParseFailures != 1 {
		t.Fatalf("ParseFailures = %d, want 1", got.ParseFailures)
	}
}

func TestSecondConsecutiveParseFailureEscalatesToRepair(t *testing.T) {
	p := &scriptedProvider{
		responses: []string{
			"not json 1",
			"not json 2",
			`{"rationale":"recovered","action":{"type":"final","content":"recovered"}}`,
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	if _, err := r.RunTask(context.Background(), "test goal"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}

	foundRepair := false
	for _, m := range state.Messages() {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "two consecutive") {
			foundRepair = true
			break
		}
	}
	if !foundRepair {
		msgs := state.Messages()
		contents := make([]string, len(msgs))
		for i, m := range msgs {
			contents[i] = fmt.Sprintf("[%s] %s", m.Role, m.Content[:min(len(m.Content), 80)])
		}
		t.Fatalf("expected repair message in state.Messages() after 2 consecutive parse failures; got:\n%s", strings.Join(contents, "\n"))
	}
}

func TestSecondConsecutiveParseFailureEnablesJSONMode(t *testing.T) {
	p := &scriptedProvider{
		capabilities: schema.ProviderCapabilities{JSONMode: true},
		responses: []string{
			"not json 1",
			"not json 2",
			`{"rationale":"recovered","action":{"type":"final","content":"recovered"}}`,
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	if _, err := r.RunTask(context.Background(), "test goal"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}

	if len(p.requests) < 3 {
		t.Fatalf("expected at least 3 requests, got %d", len(p.requests))
	}
	req := p.requests[2]
	if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" {
		t.Fatalf("requests[2].ResponseFormat = %v, want {Type:\"json_object\"} after 2 consecutive parse failures", req.ResponseFormat)
	}
}

func TestPersistentMalformedOutputSalvagesWhenWorkExists(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "contents"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	read := func(p string) string {
		return fmt.Sprintf(`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":%q}}}`, p)
	}
	p := &scriptedProvider{responses: []string{
		read("a.go"),
		read("b.go"),
		"garbage 1",
		"garbage 2",
		"garbage 3",
		`{"rationale":"salvaged","action":{"type":"final","content":"Salvaged from partial work."}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 10
	r.MaxRetries = 0

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	task, err := r.RunTask(context.Background(), "inspect files")
	if err != nil {
		t.Fatalf("RunTask err = %v, want nil (salvaged)", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("task.Status = %q, want completed", task.Status)
	}
	if task.SalvagedReason != "malformed" {
		t.Fatalf("SalvagedReason = %q, want %q", task.SalvagedReason, "malformed")
	}
	if task.Summary != "Salvaged from partial work." {
		t.Fatalf("Summary = %q, want salvaged content", task.Summary)
	}
	if got == nil {
		t.Fatal("no TurnMetrics emitted")
	}
	if got.ParseFailures != 3 {
		t.Fatalf("ParseFailures = %d, want 3", got.ParseFailures)
	}
	if got.SalvageReason != "malformed" {
		t.Fatalf("SalvageReason = %q, want malformed", got.SalvageReason)
	}
}

func TestPersistentMalformedOutputFailsFastWithoutWork(t *testing.T) {
	p := &scriptedProvider{responses: []string{"not json at all"}}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 10
	r.MaxRetries = 0

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	_, err := r.RunTask(context.Background(), "test goal")
	if !errors.Is(err, ErrModelOutputMalformed) {
		t.Fatalf("err = %v, want ErrModelOutputMalformed", err)
	}
	if got != nil && got.Iterations != 0 {
		t.Fatalf("Iterations = %d, want 0 (no valid iterations consumed)", got.Iterations)
	}
	if got != nil && got.ParseFailures != 3 {
		t.Fatalf("ParseFailures = %d, want 3 (hit maxConsecutiveParseFailures)", got.ParseFailures)
	}
	if p.calls != 3 {
		t.Fatalf("provider calls = %d, want 3 (fail fast, did not exhaust budget)", p.calls)
	}
}

func answerPendingQuestion(state *session.State, answer string) <-chan string {
	questionCh := make(chan string, 1)
	go func() {
		for {
			if q := state.PendingQuestion(); q != nil {
				questionCh <- q.Question
				q.ResponseChan <- answer
				state.SetPendingQuestion(nil)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return questionCh
}

func TestRunHandlesAskUserAction(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Archive or delete?"}}`,
		`{"rationale":"done","action":{"type":"final","content":"Archived as requested."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
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
	second := p.requests[len(p.requests)-1]
	found := false
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "User answered: archive") {
			found = true
		}
	}
	if !found {
		t.Fatal("answer not fed back to the model")
	}
	var sawQuestion, sawAnswer bool
	for _, m := range state.Messages() {
		if m.Role == session.RoleAssistant && strings.Contains(m.Content, "Archive or delete?") {
			sawQuestion = true
		}
		if m.Role == session.RoleUser && m.Content == "archive" {
			sawAnswer = true
		}
	}
	if !sawQuestion || !sawAnswer {
		t.Fatalf("transcript missing question(%v)/answer(%v)", sawQuestion, sawAnswer)
	}
}

func TestRunAskUserDeclinedContinues(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Archive or delete?"}}`,
		`{"rationale":"done","action":{"type":"final","content":"Proceeded with best judgment."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	_ = answerPendingQuestion(state, "")

	task, err := r.RunTask(context.Background(), "clean up old records")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "Proceeded with best judgment." {
		t.Fatalf("Summary = %q", task.Summary)
	}
	second := p.requests[len(p.requests)-1]
	found := false
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "declined to answer") {
			found = true
		}
	}
	if !found {
		t.Fatal("declined marker not fed back to the model")
	}
}

func TestRunAskUserCancelledByContext(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Archive or delete?"}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for state.PendingQuestion() == nil {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	if _, err := r.RunTask(ctx, "clean up"); err == nil {
		t.Fatal("expected error on cancelled question wait")
	}
	if state.PendingQuestion() != nil {
		t.Fatal("pending question must be cleared on cancellation")
	}
}

func TestSwarmRolesCannotAskUser(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Which file?"}}`,
		`{"rationale":"done","action":{"type":"final","content":"Findings reported."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.Role = RoleRepoScout
	r.SetForceClass(string(ClassQuestion))

	if _, err := r.RunTask(context.Background(), "scout the repo"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	second := p.requests[len(p.requests)-1]
	found := false
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "ask_user is not available") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a correction telling the role ask_user is unavailable")
	}
}
