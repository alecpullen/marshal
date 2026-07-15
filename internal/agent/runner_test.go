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
	"marshal/internal/hooks"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/permissions"
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

// scriptedProvider returns pre-canned responses in call order. Each call to
// Chat consumes the next entry from responses/errs (whichever is non-empty
// at that index); once the scripts run out, the last response is repeated
// so tests exercising max-iteration limits do not need to script every turn.
type scriptedProvider struct {
	responses     []string
	toolCalls     [][]schema.ToolCall
	finishReasons []string
	thinking      []string
	errs          []error
	usages        []*schema.TokenUsage
	calls         int
	requests      []schema.ChatRequest
	capabilities  schema.ProviderCapabilities
	onChat        func(idx int, req schema.ChatRequest)
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
	if p.onChat != nil {
		p.onChat(idx, req)
	}

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
	if idx < len(p.finishReasons) {
		done.FinishReason = p.finishReasons[idx]
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

func TestLengthTruncatedToolCallsAreFailedNotExecuted(t *testing.T) {
	executed := false
	p := &scriptedProvider{
		responses:     []string{"", "all done"},
		toolCalls:     [][]schema.ToolCall{{{ID: "tc1", Name: "risky.tool", Args: json.RawMessage(`{"partial":`)}}, nil},
		finishReasons: []string{"length", "stop"},
		capabilities:  schema.ProviderCapabilities{},
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
	second := p.requests[1]
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
	p := &scriptedProvider{
		thinking:  []string{"considering the question"},
		responses: []string{`{"rationale":"r","action":{"type":"answer","content":"done"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	res, err := runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
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

	_, err := runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
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

func TestNativeQuestionAskDeclined(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{
		responses: []string{"Need to ask.", "Done."},
		toolCalls: [][]schema.ToolCall{
			{{ID: "call_q", Name: "question.ask", Args: json.RawMessage(`{"questions":[{"question":"Keep legacy?"}]}`)}},
			nil,
		},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	// Simulate what the TUI actually does: send AnswerUnanswered when user declines.
	_ = answerPendingQuestion(state, session.AnswerUnanswered)

	task, err := r.RunTask(context.Background(), "ask something")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "Done." {
		t.Fatalf("Summary = %q, want Done.", task.Summary)
	}
	// Verify the runner used the correct sentinel constant by checking state messages.
	foundUnanswered := false
	for _, a := range state.Messages() {
		if strings.Contains(a.Content, "Unanswered") {
			foundUnanswered = true
			break
		}
	}
	if !foundUnanswered {
		t.Fatal("expected state to contain AnswerUnanswered sentinel in content after declined question.ask")
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

func TestNativeAskUserCountsAgainstIterationBudget(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{
		responses: []string{"Ask1.", "Ask2."},
		toolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "ask_user", Args: json.RawMessage(`{"question":"Q1?"}`)}},
			{{ID: "c2", Name: "ask_user", Args: json.RawMessage(`{"question":"Q2?"}`)}},
		},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	done := make(chan struct{})
	go func() {
		for i := 0; i < 2; i++ {
			answerPendingQuestion(state, "yes")
		}
		close(done)
	}()

	task, err := r.RunTask(context.Background(), "ask repeatedly")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "Ask2." {
		t.Fatalf("Summary = %q, want Ask2.", task.Summary)
	}
	if got == nil || got.Iterations < 4 {
		// Each ask_user batch consumes 1 iteration from line 564 +
		// 1 iteration from executeNativeAskUser = 2 per call × 2 calls = 4
		t.Fatalf("Iterations = %d, want at least 4", got.Iterations)
	}
	if r.iterationBudget != nil {
		t.Fatalf("iterationBudget should be nil after RunTask")
	}
}

func TestNativeQuestionAskCountsAgainstIterationBudget(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{
		responses: []string{"Ask1.", "Ask2."},
		toolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "question.ask", Args: json.RawMessage(`{"questions":[{"question":"Q1?"}]}`)}},
			{{ID: "c2", Name: "question.ask", Args: json.RawMessage(`{"questions":[{"question":"Q2?"}]}`)}},
		},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	done := make(chan struct{})
	go func() {
		for i := 0; i < 2; i++ {
			answerPendingQuestion(state, "yes")
		}
		close(done)
	}()

	task, err := r.RunTask(context.Background(), "ask repeatedly")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if got == nil || got.Iterations < 4 {
		t.Fatalf("Iterations = %d, want at least 4", got.Iterations)
	}
	_ = task
}

func TestRequestApprovalTimeout(t *testing.T) {
	state := newTestState(t)
	r := NewRunner(&scriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.RequestTimeout = 10 * time.Millisecond

	ctx := context.Background()
	// Tool that will never get a response — the channel has a buffer of 1 but
	// nobody sends on it. The timeout arm should fire.
	_, _, err := r.requestApproval(ctx, registry.Tool{Name: "test", Risk: registry.RiskReadOnly, Description: "test"}, "test", nil, map[string]interface{}{}, "test reason")
	if !errors.Is(err, ErrRequestTimedOut) {
		t.Fatalf("requestApproval err = %v, want ErrRequestTimedOut", err)
	}
	if state.PendingApproval() != nil {
		t.Fatal("PendingApproval should be nil after timeout")
	}
}

func TestRequestQuestionsTimeout(t *testing.T) {
	state := newTestState(t)
	r := NewRunner(&scriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.RequestTimeout = 10 * time.Millisecond

	ctx := context.Background()
	_, err := r.requestQuestions(ctx, []session.Question{{Question: "Q?"}})
	if !errors.Is(err, ErrRequestTimedOut) {
		t.Fatalf("requestQuestions err = %v, want ErrRequestTimedOut", err)
	}
	if state.PendingQuestion() != nil {
		t.Fatal("PendingQuestion should be nil after timeout")
	}
}

func TestBuildQuestionLabel(t *testing.T) {
	tests := []struct {
		name      string
		questions []session.Question
		want      string
	}{
		{
			name:      "empty questions falls back",
			questions: nil,
			want:      "waiting for your answer",
		},
		{
			name:      "single short question",
			questions: []session.Question{{Question: "Archive or delete?"}},
			want:      "waiting for your answer: Archive or delete?",
		},
		{
			name:      "single long question truncated",
			questions: []session.Question{{Question: "Should the cache be per-session or global? This is a very long question that exceeds forty characters"}},
			want:      "waiting for your answer: Should the cache be per-session or globa…",
		},
		{
			name:      "multiple questions",
			questions: []session.Question{{Question: "Auth?"}, {Question: "Keep legacy?"}},
			want:      "waiting for your answer (Q1/2): Auth?",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildQuestionLabel(tt.questions)
			if got != tt.want {
				t.Fatalf("buildQuestionLabel = %q, want %q", got, tt.want)
			}
		})
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

	p := &scriptedProvider{responses: []string{
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

func TestRunnerShellEditNormalizesSuccessfully(t *testing.T) {
	reg := registry.New()
	var calledArgs string
	if err := reg.Register(registry.Tool{
		Name: "shell.run", Risk: registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			calledArgs = string(call.Args)
			return registry.ToolResult{Summary: "ran"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	p := &scriptedProvider{responses: []string{
		`{"rationale":"run","action":{"type":"tool_call","tool":"shell.run","args":{"command":"echo hello"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"done"}}`,
	}}
	state := newTestState(t)
	pol := policy.NewEngine(&config.Config{}, nil)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassQuestion))

	go func() {
		for state.PendingApproval() == nil {
			time.Sleep(10 * time.Millisecond)
		}
		tc := state.PendingApproval()
		tc.ResponseChan <- session.UserApprovalDecision{
			Approved: true,
			Edited:   "echo edited",
		}
	}()

	if err := runner.Run(context.Background(), "run"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(calledArgs, "echo edited") {
		t.Errorf("calledArgs = %q, want to contain 'echo edited'", calledArgs)
	}
}

func TestRunnerReevaluatesPolicyAfterEditedArgs(t *testing.T) {
	// Verifies that a non-shell tool whose args edit is syntactically invalid
	// JSON causes the runner to abort with an error rather than silently
	// proceeding with the original args (the bug: the else branch at
	// runner.go:1125 silently swallowed invalid JSON).
	//
	// We use web.fetch because the policy engine unconditionally returns
	// DecisionConfirm for it, and it is NOT shell.run, so edits go through
	// the else branch.
	reg := registry.New()
	executed := make(chan struct{}, 1)
	if err := reg.Register(registry.Tool{
		Name: "web.fetch",
		Risk: registry.RiskNetwork,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			select {
			case executed <- struct{}{}:
			default:
			}
			return registry.ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Two responses: the tool call (triggers approval) and a final message so
	// the loop terminates even if the buggy code ignores the edit.
	p := &scriptedProvider{responses: []string{
		`{"rationale":"fetch","action":{"type":"tool_call","tool":"web.fetch","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"done"}}`,
	}}
	cfg := config.Default()
	pol := policy.NewEngine(&cfg, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	runErr := make(chan error, 1)
	go func() {
		runErr <- runner.Run(context.Background(), "Fetch")
	}()

	var tc *session.PendingToolCall
	deadline := time.After(5 * time.Second)
	for tc == nil {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for pending approval")
		default:
			tc = state.PendingApproval()
		}
	}

	// Send a syntactically invalid JSON edit — the runner must reject it.
	tc.ResponseChan <- session.UserApprovalDecision{
		Approved: true,
		Edited:   "not json",
	}

	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("expected error from invalid JSON edit, got nil")
		}
		if !strings.Contains(err.Error(), "not valid JSON") {
			t.Fatalf("error does not mention invalid JSON: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Run to finish")
	}

	select {
	case <-executed:
		t.Fatal("tool handler was executed despite invalid JSON edit")
	default:
	}
}

func TestRunnerReevaluatesDenyAfterValidEdit(t *testing.T) {
	// Verifies that when the user edits args with valid JSON and the
	// re-evaluated policy returns DecisionDeny, the runner returns a
	// tool-error message (not a hard error) and does NOT execute the tool
	// handler.
	//
	// We use web.fetch because the policy engine unconditionally returns
	// DecisionConfirm for it on first evaluation, letting us reach the
	// approval dialog. After the approval is pending we inject a deny rule
	// via SetRules so the re-evaluation on the edited args returns Deny.
	reg := registry.New()
	executed := make(chan struct{}, 1)
	if err := reg.Register(registry.Tool{
		Name: "web.fetch",
		Risk: registry.RiskNetwork,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			select {
			case executed <- struct{}{}:
			default:
			}
			return registry.ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &scriptedProvider{responses: []string{
		`{"rationale":"fetch","action":{"type":"tool_call","tool":"web.fetch","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"done"}}`,
	}}
	cfg := config.Default()
	pol := policy.NewEngine(&cfg, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	runErr := make(chan error, 1)
	go func() {
		runErr <- runner.Run(context.Background(), "Fetch")
	}()

	var tc *session.PendingToolCall
	deadline := time.After(5 * time.Second)
	for tc == nil {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for pending approval")
		default:
			tc = state.PendingApproval()
		}
	}

	// First Evaluate returned DecisionConfirm (no deny rules for web.fetch).
	// Install a deny rule so the re-evaluation after edit sees DecisionDeny.
	pol.SetRules([]permissions.Rule{
		{Permission: "web.fetch", Pattern: "web.fetch", Action: permissions.ActionDeny},
	})

	// Send a valid JSON edit — the re-evaluation must deny it.
	tc.ResponseChan <- session.UserApprovalDecision{
		Approved: true,
		Edited:   `{"url":"https://evil.example/path"}`,
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Run to finish")
	}

	select {
	case <-executed:
		t.Fatal("tool handler was executed despite policy deny after edit")
	default:
	}

	// Verify the provider received a message mentioning the denial.
	if len(p.requests) < 2 {
		t.Fatalf("expected at least 2 provider requests, got %d", len(p.requests))
	}
	lastReq := p.requests[len(p.requests)-1]
	found := false
	for _, msg := range lastReq.Messages {
		if strings.Contains(msg.Content, "denied") || strings.Contains(msg.Content, "deny") {
			found = true
			break
		}
	}
	if !found {
		for i, msg := range lastReq.Messages {
			t.Logf("msg[%d] role=%s content=%q", i, msg.Role, msg.Content)
		}
		t.Fatal("expected a message mentioning denial in the last provider request")
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
	runner.PlanFirst = true

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
	runner.PlanFirst = true

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
	runner.PlanFirst = true

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
	_, err := runner.chatOnce(context.Background(), &blockingProvider{}, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
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
	runner.PlanFirst = true

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
	p := &scriptedProvider{responses: responses}
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
		`{"rationale":"read","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if err := runner.Run(context.Background(), "Read the big file"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	foundSpill := false
	for _, ev := range state.AuditLog() {
		if strings.Contains(ev.ResultSummary, "[output spilled to file]") {
			foundSpill = true
			break
		}
	}
	if !foundSpill {
		t.Fatalf("large tool result was not spilled in audit log: %#v", state.AuditLog())
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

	_, err := runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, nil)
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
	p := &scriptedProvider{responses: []string{
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
	if p.calls != 1 {
		t.Fatalf("provider called %d times, want 1 (no separate planning round-trip)", p.calls)
	}
	if len(state.Plan()) != 0 {
		t.Fatalf("plan was set without PlanFirst: %v", state.Plan())
	}
}

func TestPlanningStepRunsWhenPlanFirstEnabled(t *testing.T) {
	p := &scriptedProvider{responses: []string{
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
	if p.calls != 2 {
		t.Fatalf("provider called %d times, want 2 (plan + answer)", p.calls)
	}
	if len(state.Plan()) == 0 {
		t.Fatal("PlanFirst=true did not set a plan")
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
	if len(systemPromptMsgs) < 2 {
		t.Fatalf("expected at least 2 provider requests with system messages, got %d", len(systemPromptMsgs))
	}
	if !strings.Contains(systemPromptMsgs[0], "`debug`") {
		t.Fatal("first system prompt should list debug skill")
	}
	if !strings.Contains(systemPromptMsgs[0], "Debugging workflow") {
		t.Fatal("first system prompt should include skill description")
	}
	if !strings.Contains(systemPromptMsgs[1], "Active Skills") {
		t.Fatal("second system prompt should show Active Skills")
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
	executedMu.Lock()
	executedCount := len(executed)
	executedMu.Unlock()
	if executedCount != 5 {
		t.Fatalf("executed %d reads, want 5 (batch of 4 + 1 follow-up)", executedCount)
	}
	if task.SalvagedReason != "" || task.Summary != "REAL ANSWER." {
		t.Fatalf("task = %+v, want un-salvaged completion with the model's answer", task)
	}
	if p.calls != 3 {
		t.Fatalf("provider calls = %d, want 3 (batch, single read, final)", p.calls)
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
	r := NewRunner(&scriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
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

	p := &scriptedProvider{responses: []string{
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

// TestResponseFormatResetsAcrossRunTaskCalls ensures that when a Runner
// triggers the JSON-mode response format escalation inside one RunTask,
// the next RunTask on the same *Runner starts with a clean response format
// (the original seed value, typically nil).
func TestResponseFormatResetsAcrossRunTaskCalls(t *testing.T) {
	p := &scriptedProvider{
		capabilities: schema.ProviderCapabilities{JSONMode: true},
		responses: []string{
			// First RunTask: 2 parse failures → JSON mode, then recover
			"not json 1",
			"not json 2",
			`{"rationale":"recovered","action":{"type":"final","content":"first done"}}`,
			// Second RunTask: no JSON mode should leak across
			"not json 3",
			`{"rationale":"done","action":{"type":"final","content":"second done"}}`,
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	// First RunTask triggers JSON-mode escalation on the 3rd request
	if _, err := r.RunTask(context.Background(), "first goal"); err != nil {
		t.Fatalf("first RunTask err = %v", err)
	}

	firstRunRequestCount := len(p.requests)

	// Second RunTask on the same *Runner
	if _, err := r.RunTask(context.Background(), "second goal"); err != nil {
		t.Fatalf("second RunTask err = %v", err)
	}

	// The first request of the second run must NOT inherit JSON mode
	req := p.requests[firstRunRequestCount]
	if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_object" {
		t.Fatalf("second RunTask's first request has ResponseFormat = %v, want nil or non-json_object", req.ResponseFormat)
	}
}

// TestRunnerSequentialReuse verifies that the same *Runner can be called
// with RunTask twice in sequence (different goals, different ForceClass
// values) and both calls complete successfully.
func TestRunnerSequentialReuse(t *testing.T) {
	p := &scriptedProvider{
		capabilities: schema.ProviderCapabilities{JSONMode: true},
		responses: []string{
			// First RunTask
			`{"rationale":"first","action":{"type":"final","content":"first done"}}`,
			// Second RunTask
			`{"rationale":"second","action":{"type":"final","content":"second done"}}`,
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	r.SetForceClass("question")
	if _, err := r.RunTask(context.Background(), "first goal"); err != nil {
		t.Fatalf("first RunTask err = %v", err)
	}

	r.SetForceClass("edit")
	if _, err := r.RunTask(context.Background(), "second goal"); err != nil {
		t.Fatalf("second RunTask err = %v", err)
	}

	// Both completed without error — the second call did not inherit
	// per-run state from the first.
	if len(p.requests) < 2 {
		t.Fatalf("expected at least 2 chat requests, got %d", len(p.requests))
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

// scriptRepeats returns n copies of the same tool_call action envelope.
func scriptRepeats(n int, resp string) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, resp)
	}
	return out
}

func TestHardStallAsksUserAndContinuesOnGuidance(t *testing.T) {
	toolResp := `{"rationale":"look","action":{"type":"tool_call","tool":"echo.tool","args":{}}}`
	responses := scriptRepeats(repeatHardStall, toolResp)
	responses = append(responses, `{"rationale":"done","action":{"type":"final","content":"finished after guidance"}}`)
	p := &scriptedProvider{responses: responses}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "echo.tool", Description: "static output", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "same output"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassQuestion))

	go func() {
		for {
			if q := state.PendingQuestion(); q != nil && len(q.Questions) > 0 {
				canonical := q.Questions[0].Question
				q.ResponseChan <- []session.Answer{{Question: canonical, Answer: "try reading the config file instead"}}
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	task, err := runner.RunTask(context.Background(), "investigate")
	if err != nil {
		t.Fatalf("RunTask returned error: %v", err)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("task was salvaged (%s); guidance should have continued the loop", task.SalvagedReason)
	}
	if task.Summary != "finished after guidance" {
		t.Fatalf("task.Summary = %q", task.Summary)
	}
	// The guidance must reach the model as a user message.
	last := p.requests[len(p.requests)-1]
	found := false
	for _, m := range last.Messages {
		if m.Role == schema.RoleUser && strings.Contains(m.Content, "try reading the config file instead") {
			found = true
		}
	}
	if !found {
		t.Fatal("user guidance was not injected into the conversation")
	}
}

func TestHardStallFinalizesWhenUserDeclines(t *testing.T) {
	toolResp := `{"rationale":"look","action":{"type":"tool_call","tool":"echo.tool","args":{}}}`
	responses := scriptRepeats(repeatHardStall, toolResp)
	responses = append(responses, `{"rationale":"stopping","action":{"type":"final","content":"summary of progress"}}`)
	p := &scriptedProvider{responses: responses}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "echo.tool", Description: "static output", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "same output"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassQuestion))

	go func() {
		for {
			if q := state.PendingQuestion(); q != nil && len(q.Questions) > 0 {
				canonical := q.Questions[0].Question
				q.ResponseChan <- []session.Answer{{Question: canonical, Answer: ""}} // decline
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	task, err := runner.RunTask(context.Background(), "investigate")
	if err != nil {
		t.Fatalf("RunTask returned error: %v", err)
	}
	if task.SalvagedReason != string(reasonStalled) {
		t.Fatalf("SalvagedReason = %q, want %q", task.SalvagedReason, reasonStalled)
	}
}

func TestHardStallAutoFinalizesForNonGeneralRole(t *testing.T) {
	toolResp := `{"rationale":"look","action":{"type":"tool_call","tool":"echo.tool","args":{}}}`
	responses := scriptRepeats(repeatHardStall, toolResp)
	responses = append(responses, `{"rationale":"stopping","action":{"type":"final","content":"scout findings"}}`)
	p := &scriptedProvider{responses: responses}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "echo.tool", Description: "static output", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "same output"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.Role = RoleRepoScout
	runner.SetForceClass(string(ClassQuestion))

	task, err := runner.RunTask(context.Background(), "scout the repo")
	if err != nil {
		t.Fatalf("RunTask returned error: %v", err)
	}
	if task.SalvagedReason != string(reasonStalled) {
		t.Fatalf("SalvagedReason = %q, want stalled auto-finalize for swarm role", task.SalvagedReason)
	}
}

func answerPendingQuestion(state *session.State, answer string) <-chan string {
	questionCh := make(chan string, 1)
	go func() {
		for {
			if q := state.PendingQuestion(); q != nil && len(q.Questions) > 0 {
				canonical := q.Questions[0].Question
				questionCh <- canonical
				q.ResponseChan <- []session.Answer{{Question: canonical, Answer: answer}}
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

// TestRunNativeEmptyResponsesFinalizeWithReasonEmpty is the regression test
// for the infinite empty-response loop: in native mode, a model that returns
// no tool calls and empty text used to continue without incrementing
// iteration or invoking the stall detector, looping forever. After the fix,
// two consecutive empty responses short-circuit to finalize with reasonEmpty
// and the task completes (salvaged) instead of hanging.
func TestRunNativeEmptyResponsesFinalizeWithReasonEmpty(t *testing.T) {
	state := newTestState(t)
	// Two empty native responses (no text, no tool calls), then finalize's
	// own forced call produces a real prose answer.
	p := &scriptedProvider{
		responses: []string{"", "", "Here is the salvaged answer."},
		toolCalls: [][]schema.ToolCall{nil, nil, nil},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 16

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	task, err := r.RunTask(context.Background(), "vague goal")
	if err != nil {
		t.Fatalf("RunTask err = %v, want nil (salvaged)", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("task.Status = %q, want completed", task.Status)
	}
	if task.SalvagedReason != "empty" {
		t.Fatalf("SalvagedReason = %q, want %q", task.SalvagedReason, "empty")
	}
	if task.Summary != "Here is the salvaged answer." {
		t.Fatalf("Summary = %q, want the finalize prose answer", task.Summary)
	}
	// Two empty turns consumed the budget before finalize fired.
	if got == nil || got.Iterations != 2 {
		t.Fatalf("Iterations = %v, want 2 (empty turns must consume budget)", got)
	}
}

// TestRunNativeEmptyThenAnswerWins verifies that a single empty response
// followed by a real final answer is honored — the loop must not over-eagerly
// finalize after one silence.
func TestRunNativeEmptyThenAnswerWins(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{
		responses: []string{"", "The real answer after a moment of silence."},
		toolCalls: [][]schema.ToolCall{nil, nil},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	task, err := r.RunTask(context.Background(), "what is 2+2?")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty (normal completion)", task.SalvagedReason)
	}
	if task.Summary != "The real answer after a moment of silence." {
		t.Fatalf("Summary = %q", task.Summary)
	}
}

// TestRunAskUserDeclinedCountsAgainstBudget ensures a declined ask_user
// consumes a budget slot so repeated ask→decline→ask cannot loop unbounded.
func TestRunAskUserDeclinedCountsAgainstBudget(t *testing.T) {
	state := newTestState(t)
	// Repeated ask_user, always declined, until the budget runs out. With a
	// small budget the loop must terminate (via finalize/salvage or budget
	// exhaustion) rather than looping ask→decline forever.
	p := &scriptedProvider{responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Which one?"}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 3
	r.MaxRetries = 0

	// Drain every pending question with an empty (declined) answer.
	go func() {
		for {
			if q := state.PendingQuestion(); q != nil && len(q.Questions) > 0 {
				canonical := q.Questions[0].Question
				q.ResponseChan <- []session.Answer{{Question: canonical, Answer: ""}}
				state.SetPendingQuestion(nil)
			} else {
				time.Sleep(time.Millisecond)
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.RunTask(context.Background(), "decide something")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunTask hung: declined ask_user did not consume the budget")
	}
}

func TestRepeatedToolCallGetsReminderInResult(t *testing.T) {
	toolResp := `{"rationale":"look","action":{"type":"tool_call","tool":"echo.tool","args":{}}}`
	p := &scriptedProvider{responses: []string{
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
	last := p.requests[len(p.requests)-1]
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

func TestLoopCompactsViaSummaryWhenOverBudget(t *testing.T) {
	toolResp := `{"rationale":"look","action":{"type":"tool_call","tool":"big.tool","args":{}}}`
	p := &scriptedProvider{responses: []string{
		toolResp,
		"## Current State\nread the big file; ready to answer.", // handoff summary call
		`{"rationale":"done","action":{"type":"final","content":"answer from summary"}}`,
	}}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "big.tool", Description: "big output", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			// Keep the output inline (under the 8000-char spill limit) while still
			// exceeding the 1500-token context budget when included in the transcript.
			return registry.ToolResult{Summary: "ok", Content: strings.Repeat("word ", 1400)}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassQuestion))
	runner.MaxTurnContextTokens = 1500

	task, err := runner.RunTask(context.Background(), "summarize the big file")
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if task.Summary != "answer from summary" {
		t.Fatalf("task.Summary = %q", task.Summary)
	}
	// Request 2 is the summarization call; request 3 must be the rebuilt
	// transcript containing the summary but not the huge tool output.
	final := p.requests[len(p.requests)-1]
	for _, m := range final.Messages {
		if strings.Count(m.Content, "word ") > 100 {
			t.Fatal("rebuilt transcript still contains the oversized tool output")
		}
	}
}

func TestSecondTurnSeesFirstTurnHistory(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale":"a","action":{"type":"answer","content":"the parser lives in protocol.go"}}`,
		`{"rationale":"b","action":{"type":"answer","content":"expanded answer"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "where is the parser?"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if err := runner.Run(context.Background(), "tell me more about it"); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	second := p.requests[len(p.requests)-1]
	sawPriorQuestion, sawPriorAnswer := false, false
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "where is the parser?") {
			sawPriorQuestion = true
		}
		if strings.Contains(m.Content, "the parser lives in protocol.go") {
			sawPriorAnswer = true
		}
	}
	if !sawPriorQuestion || !sawPriorAnswer {
		t.Fatalf("second turn missing history: question=%v answer=%v", sawPriorQuestion, sawPriorAnswer)
	}
}

type staticResolver struct {
	route    routing.Route
	provider provider.Provider
}

func (s *staticResolver) Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error) {
	return s.route, s.provider, nil
}

func TestResolveRouteRaisesBudgetFromKnownWindow(t *testing.T) {
	p := &scriptedProvider{responses: []string{`{"rationale":"done","action":{"type":"final","content":"ok"}}`}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "qwen2.5-coder:14b")
	// A resolver that returns a route whose preset has no explicit window —
	// forces the catalog lookup path.
	runner.RouteResolver = &staticResolver{
		route: routing.Route{
			Role:    routing.RoleImplementer,
			Profile: "p",
			Preset:  routing.ModelPreset{Name: "fast", Provider: "ollama", Model: "qwen2.5-coder:14b", LocalOnly: true},
		},
		provider: p,
	}
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 0.85 * 32768 - 8192 = ~19698; must be > the 60000 default floor? No — effective
	// is ~19698 which is LESS than the 60000 default floor, so the floor wins.
	// Assert the catalog path ran by checking the window was recorded on state.
	if _, window := state.TurnUsage(); window != 32768 {
		t.Fatalf("state window = %d, want 32768 (catalog resolved)", window)
	}
}

func TestResolveRouteConfigWindowRaisesBudget(t *testing.T) {
	runner := NewRunner(&scriptedProvider{responses: []string{`{"rationale":"done","action":{"type":"final","content":"ok"}}`}},
		registry.New(), policy.NewEngine(&config.Config{}, nil), newTestState(t), "big-model")
	runner.MaxTurnContextTokens = 1000 // small floor
	runner.RouteResolver = &staticResolver{
		route:    routing.Route{Preset: routing.ModelPreset{Model: "big", ContextWindow: 200000, MaxOutputTokens: 8192}},
		provider: runner.Provider,
	}
	runner.SetForceClass(string(ClassQuestion))
	if err := runner.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 0.85*200000 - 8192 = 161808, far above the 1000 floor.
	if runner.MaxTurnContextTokens != 161808 {
		t.Fatalf("effective budget = %d, want 161808", runner.MaxTurnContextTokens)
	}
}

func TestHistoryAfterRewindExcludesAbandonedBranch(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale":"a","action":{"type":"answer","content":"first answer"}}`,
		`{"rationale":"b","action":{"type":"answer","content":"second answer"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "question one"); err != nil {
		t.Fatalf("run1: %v", err)
	}
	msgs := state.Messages()
	state.Rewind(msgs[0].ID)
	if err := runner.Run(context.Background(), "different question"); err != nil {
		t.Fatalf("run2: %v", err)
	}

	second := p.requests[len(p.requests)-1]
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "first answer") {
			t.Fatal("abandoned branch's answer leaked into the new branch's history")
		}
	}
}

type fakeHookRunner struct {
	preOut  hooks.Output
	turnOut hooks.Output
	preErr  error
}

func (f fakeHookRunner) RunPreToolUse(ctx context.Context, in hooks.PreToolUseInput) (hooks.Output, error) {
	return f.preOut, f.preErr
}

func (f fakeHookRunner) RunTurnEnd(ctx context.Context, in hooks.TurnEndInput) (hooks.Output, error) {
	return f.turnOut, nil
}

// onceRewriteHookRunner rewrites args exactly once, then passes through
// on subsequent calls. Used by TestAuditEventRecordsOriginalArgs.
type onceRewriteHookRunner struct {
	hasRewritten bool
}

func (r *onceRewriteHookRunner) RunPreToolUse(ctx context.Context, in hooks.PreToolUseInput) (hooks.Output, error) {
	if !r.hasRewritten {
		r.hasRewritten = true
		return hooks.Output{Rewrite: json.RawMessage(`{"command":"git --no-pager log"}`)}, nil
	}
	return hooks.Output{}, nil
}

func (r *onceRewriteHookRunner) RunTurnEnd(ctx context.Context, in hooks.TurnEndInput) (hooks.Output, error) {
	return hooks.Output{}, nil
}

func TestAuditEventRecordsOriginalArgs(t *testing.T) {
	// Simulate a pre_tool_use hook rewriting "git status" to
	// "git --no-pager log". "git status" is auto-approved by the
	// allow list, so no first approval is needed. After the rewrite
	// the user is re-prompted to approve "git --no-pager log".
	// Verify the audit event preserves the original approved args.
	reg := registry.New()
	executed := false
	if err := reg.Register(registry.Tool{
		Name: "shell.run", Risk: registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executed = true
			return registry.ToolResult{Summary: "ran"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Hook rewrites once, then stops on subsequent calls.
	hook := &onceRewriteHookRunner{}
	p := &scriptedProvider{responses: []string{
		`{"rationale":"r","action":{"type":"tool_call","tool":"shell.run","args":{"command":"git status"}}}`,
		`{"rationale":"r","action":{"type":"answer","content":"done"}}`,
	}}
	cfg := config.Default()
	pol := policy.NewEngine(&cfg, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.HookRunner = hook
	runner.SetForceClass(string(ClassQuestion))

	runErr := make(chan error, 1)
	go func() {
		runErr <- runner.Run(context.Background(), "check status")
	}()

	// "git status" is auto-approved (allow list). The hook rewrites it
	// to "git --no-pager log", which requires approval. Wait for that
	// single approval dialog.
	var tc *session.PendingToolCall
	deadline := time.After(5 * time.Second)
	for tc == nil {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for approval after rewrite")
		default:
			tc = state.PendingApproval()
		}
	}
	tc.ResponseChan <- session.UserApprovalDecision{Approved: true}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Run to finish")
	}

	if !executed {
		t.Fatal("tool was not executed")
	}

	log := state.AuditLog()
	// Find the execution event (the last successful shell.run event).
	var execEvent *registry.AuditEvent
	for i := len(log) - 1; i >= 0; i-- {
		if log[i].Error == "" && log[i].ToolName == "shell.run" {
			execEvent = &log[i]
			break
		}
	}
	if execEvent == nil {
		t.Fatal("no successful shell.run audit event found")
	}
	if !execEvent.Rewritten {
		t.Fatal("expected Rewritten=true on the audit event")
	}
	if execEvent.OriginalArgs == nil {
		t.Fatal("expected OriginalArgs to be set")
	}
	if !strings.Contains(string(execEvent.OriginalArgs), "git status") {
		t.Fatalf("OriginalArgs = %s, want to contain 'git status'", string(execEvent.OriginalArgs))
	}
}

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
	p := &scriptedProvider{responses: []string{`{"rationale":"r","action":{"type":"patch","content":"*** Begin Patch\n*** End Patch"}}`, `{"rationale":"r","action":{"type":"answer","content":"stopped"}}`}}
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
	p := &scriptedProvider{responses: []string{`{"rationale":"r","action":{"type":"tool_call","tool":"shell.run","args":{"command":"date"}}}`, `{"rationale":"r","action":{"type":"answer","content":"done"}}`}}
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
	p := &scriptedProvider{responses: []string{`{"rationale":"r","action":{"type":"tool_call","tool":"shell.run","args":{"command":"date"}}}`, `{"rationale":"r","action":{"type":"answer","content":"done"}}`}}
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
	p := &scriptedProvider{responses: []string{
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
	p := &scriptedProvider{responses: []string{
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

func TestTurnEndHookContinuesNativePath(t *testing.T) {
	// Drives the native-tools final-return site (runner.go: NativeTools &&
	// len(res.ToolCalls) == 0 && strings.TrimSpace(res.Text) != ""). The
	// existing two turn-end tests both use the JSON ActionAnswer/ActionFinal
	// site, so this test guards the other hook wiring against regressions.
	p := &scriptedProvider{
		responses: []string{"first native answer", "final native answer"},
		toolCalls: [][]schema.ToolCall{nil, nil},
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
