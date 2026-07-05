package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	responses []string
	thinking  []string
	errs      []error
	calls     int
	requests  []schema.ChatRequest
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}

func (p *scriptedProvider) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) {
	return schema.EmbedResponse{}, nil
}

func (p *scriptedProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
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
	ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: content}
	ch <- schema.ChatEvent{Type: schema.ChatEventDone}
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

	text, err := runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("chatOnce returned error: %v", err)
	}
	wantText := `{"rationale":"r","action":{"type":"answer","content":"done"}}`
	if text != wantText {
		t.Fatalf("chatOnce returned %q, want %q", text, wantText)
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

	err := runner.Run(context.Background(), "Loop forever")
	if !errors.Is(err, ErrMaxIterationsExceeded) {
		t.Fatalf("err = %v, want ErrMaxIterationsExceeded", err)
	}
	if len(state.AuditLog()) != 2 {
		t.Fatalf("len(auditLog) = %d, want 2 (bounded by MaxToolIterations)", len(state.AuditLog()))
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

	if err := runner.Run(context.Background(), "Read the demo value"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	found := false
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, "repeating the same step") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing loop-detection nudge in transcript: %#v", state.Messages())
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
	if len(systemPromptMsgs) < 2 {
		t.Fatalf("expected at least 2 provider requests with system messages, got %d", len(systemPromptMsgs))
	}
	if !strings.Contains(systemPromptMsgs[0], "`debug`") {
		t.Fatal("first system prompt should list debug skill")
	}
	if !strings.Contains(systemPromptMsgs[0], "Debugging workflow") {
		t.Fatal("first system prompt should include skill description")
	}
	if !strings.Contains(systemPromptMsgs[2], "Active Skills") {
		t.Fatal("second system prompt should show Active Skills")
	}
}
