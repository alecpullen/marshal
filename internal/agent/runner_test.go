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
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// scriptedProvider returns pre-canned responses in call order. Each call to
// Chat consumes the next entry from responses/errs (whichever is non-empty
// at that index); once the scripts run out, the last response is repeated
// so tests exercising max-iteration limits do not need to script every turn.
type scriptedProvider struct {
	responses []string
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

	ch := make(chan schema.ChatEvent, 2)
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
