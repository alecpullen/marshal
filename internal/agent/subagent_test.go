package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/pricing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

// TestSubagentDepthLimit verifies that a session whose own nesting depth
// has already hit the cap is rejected by the depth guard before any child
// runner is constructed. We construct a depth-2 session directly (the
// factory never gets a chance to "build" it into a parent).
func TestSubagentDepthLimit(t *testing.T) {
	called := false
	factory := func(_ SubagentRequest) (*Runner, *session.State, error) {
		called = true
		return &Runner{}, nil, nil
	}
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}, session.WithDepth(2))

	tool := NewSubagentTool(factory, nil, registry.New(), state)
	if tool.Name != "agent.run" {
		t.Fatalf("Name = %q, want %q", tool.Name, "agent.run")
	}

	_, err := tool.Handler(t.Context(), registry.ToolCall{Args: []byte(`{"prompt":"x","description":"y"}`)})
	if err == nil {
		t.Fatal("expected depth-limit error, got nil")
	}
	if !errors.Is(err, session.ErrSubagentDepthLimit) {
		t.Fatalf("error = %v, want session.ErrSubagentDepthLimit", err)
	}
	if called {
		t.Fatal("factory must not be invoked when depth guard rejects")
	}
}

// TestSubagentConcurrencyLimit exercises the concurrency guard end-to-end:
// a parent at depth=0 admits two in-flight subagents, then rejects a third
// with ErrSubagentConcurrencyLimit (not ErrSubagentDepthLimit). We spawn
// the first two via direct EnterSubagent/ExitSubagent calls in goroutines
// blocked on a release channel, then attempt a third admission via the
// normal agent.run handler.
func TestSubagentConcurrencyLimit(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	if got := state.SubagentDepth(); got != 0 {
		t.Fatalf("initial depth = %d, want 0", got)
	}

	release := make(chan struct{})
	entered := make(chan struct{}, 2)

	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			if err := state.EnterSubagent(); err != nil {
				t.Errorf("first/second EnterSubagent returned %v, want nil", err)
				return
			}
			defer state.ExitSubagent()
			entered <- struct{}{}
			<-release
		}()
	}

	<-entered
	<-entered
	if got := state.SubagentConcurrency(); got != 2 {
		t.Fatalf("concurrency after 2 admissions = %d, want 2", got)
	}

	factoryCalls := 0
	factory := func(_ SubagentRequest) (*Runner, *session.State, error) {
		factoryCalls++
		return &Runner{}, nil, nil
	}
	tool := NewSubagentTool(factory, nil, registry.New(), state)
	_, err := tool.Handler(t.Context(), registry.ToolCall{Args: []byte(`{"prompt":"x","description":"y"}`)})
	if err == nil {
		t.Fatal("expected concurrency-limit error, got nil")
	}
	if !errors.Is(err, session.ErrSubagentConcurrencyLimit) {
		t.Fatalf("error = %v, want session.ErrSubagentConcurrencyLimit", err)
	}
	if errors.Is(err, session.ErrSubagentDepthLimit) {
		t.Fatalf("error = %v leaked depth-limit sentinel inside a concurrency-limit test", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory invocations = %d, want 0", factoryCalls)
	}

	close(release)
	wg.Wait()
	if got := state.SubagentConcurrency(); got != 0 {
		t.Fatalf("concurrency after release = %d, want 0", got)
	}
}

func TestSubagentCancelPropagatesToExec(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	tool := NewSubagentTool(
		func(req SubagentRequest) (*Runner, *session.State, error) {
			return &Runner{}, state, nil
		},
		nil,
		registry.New(),
		state,
		WithSubagentExec(func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
			<-ctx.Done()
			return "", "", ctx.Err()
		}),
	)

	handlerErr := make(chan error, 1)
	go func() {
		_, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"prompt":"x","description":"y"}`)})
		handlerErr <- err
	}()

	// Wait for the subagent to register so we have an ID to cancel.
	var viewID int64
	for {
		views := state.Subagents()
		if len(views) == 1 {
			viewID = views[0].ID
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !state.CancelSubagent(viewID) {
		t.Fatal("CancelSubagent returned false")
	}

	err := <-handlerErr
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	views := state.Subagents()
	if len(views) != 1 {
		t.Fatalf("expected one subagent view, got %d", len(views))
	}
	if views[0].Status != session.SubagentFailed {
		t.Fatalf("status = %v, want SubagentFailed", views[0].Status)
	}
}

func TestSubagentUsageObserverComposesAndUpdatesCard(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	parentUsage := 0
	child := NewRunner(nil, registry.New(), nil, session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}), "test")
	child.UsageObserver = func(u schema.TokenUsage) {
		parentUsage += u.PromptTokens + u.CompletionTokens
	}
	tool := NewSubagentTool(
		func(req SubagentRequest) (*Runner, *session.State, error) {
			return child, child.State, nil
		},
		nil,
		registry.New(),
		state,
		WithSubagentExec(func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
			if child.UsageObserver != nil {
				child.UsageObserver(schema.TokenUsage{PromptTokens: 10, CompletionTokens: 5})
			}
			return "done", "", nil
		}),
	)
	_, err := tool.Handler(t.Context(), registry.ToolCall{Args: []byte(`{"prompt":"x","description":"y"}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if parentUsage != 15 {
		t.Fatalf("parent usage observer got %d, want 15", parentUsage)
	}
	views := state.Subagents()
	if len(views) != 1 {
		t.Fatalf("expected one subagent view, got %d", len(views))
	}
	if views[0].TokensUsed != 15 {
		t.Fatalf("card TokensUsed = %d, want 15", views[0].TokensUsed)
	}
}

func TestSubagentSalvageSurfacesInResultAndCard(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	tool := NewSubagentTool(
		func(req SubagentRequest) (*Runner, *session.State, error) {
			return &Runner{}, state, nil
		},
		nil,
		registry.New(),
		state,
		WithSubagentExec(func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
			return "partial report", "exhausted", nil
		}),
	)
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"prompt":"x","description":"y"}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Summary, "salvaged: exhausted") {
		t.Fatalf("summary = %q, want salvaged marker", res.Summary)
	}
	if !strings.Contains(res.Content, "partial report") {
		t.Fatalf("content missing report: %q", res.Content)
	}
	if !strings.Contains(res.Content, " Raise [agent] subtask_iterations") {
		t.Fatalf("content missing remedy hint: %q", res.Content)
	}
	views := state.Subagents()
	if len(views) != 1 {
		t.Fatalf("expected one subagent view, got %d", len(views))
	}
	if views[0].SalvagedReason != "exhausted" {
		t.Fatalf("SalvagedReason = %q, want exhausted", views[0].SalvagedReason)
	}
}

func TestSubagentGuardCountersRoundTrip(t *testing.T) {
	top := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}, session.WithDepth(0))
	if got := top.SubagentDepth(); got != 0 {
		t.Fatalf("top depth = %d, want 0", got)
	}
	if got := top.SubagentConcurrency(); got != 0 {
		t.Fatalf("top concurrency = %d, want 0", got)
	}
	top.SetSubagentConcurrency(1)
	if got := top.SubagentConcurrency(); got != 1 {
		t.Fatalf("after SetSubagentConcurrency(1) = %d, want 1", got)
	}

	child := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}, session.WithDepth(1))
	if got := child.SubagentDepth(); got != 1 {
		t.Fatalf("child depth = %d, want 1", got)
	}
	if got := child.SubagentConcurrency(); got != 0 {
		t.Fatalf("child concurrency = %d, want 0", got)
	}

	if err := child.EnterSubagent(); !errors.Is(err, session.ErrSubagentDepthLimit) {
		t.Fatalf("child EnterSubagent = %v, want ErrSubagentDepthLimit (depth guard, not concurrency)", err)
	}
	if got := child.SubagentConcurrency(); got != 0 {
		t.Fatalf("child concurrency after rejected EnterSubagent = %d, want 0", got)
	}
}

func TestSubtaskScopeViewFiltersTools(t *testing.T) {
	reg := registry.New()
	mustReg := func(tool *registry.Tool) {
		t.Helper()
		if err := reg.Register(*tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}
	mustReg(&registry.Tool{Name: "file.read", Description: "read", Schema: []byte(`{}`), Risk: registry.RiskReadOnly, Handler: stubAgentRunHandler})
	mustReg(&registry.Tool{Name: "agent.run", Description: "delegate", Schema: []byte(`{}`), Risk: registry.RiskReadOnly, Handler: stubAgentRunHandler})
	mustReg(&registry.Tool{Name: "web.fetch", Description: "fetch", Schema: []byte(`{}`), Risk: registry.RiskNetwork, Handler: stubAgentRunHandler})
	mustReg(&registry.Tool{Name: "shell.run", Description: "shell", Schema: []byte(`{}`), Risk: registry.RiskCommand, Handler: stubAgentRunHandler})
	mustReg(&registry.Tool{Name: "diagnostics.check", Description: "diag", Schema: []byte(`{}`), Risk: registry.RiskReadOnly, Deferred: true, Handler: stubAgentRunHandler})
	mustReg(&registry.Tool{Name: "question.ask", Description: "ask", Schema: []byte(`{}`), Risk: registry.RiskReadOnly, Handler: stubAgentRunHandler})
	mustReg(&registry.Tool{Name: "ask_user", Description: "ask alias", Schema: []byte(`{}`), Risk: registry.RiskReadOnly, Handler: stubAgentRunHandler})

	view := SubtaskScopeView(reg)
	names := make(map[string]bool, len(view.List()))
	for _, tool := range view.List() {
		names[tool.Name] = true
	}
	if !names["file.read"] {
		t.Fatal("subtask view missing file.read")
	}
	if !names["web.fetch"] {
		t.Fatal("subtask view missing web.fetch (network must be allowed)")
	}
	if names["agent.run"] {
		t.Fatal("subtask view must NOT contain agent.run (no nested subagents)")
	}
	if !names["shell.run"] {
		t.Fatal("subtask view missing shell.run (implementation tools must be visible to child)")
	}
	if names["diagnostics.check"] {
		t.Fatal("subtask view must NOT contain deferred tools (no MCP autoloading)")
		// diagnostics.check is RiskReadOnly AND not Deferred for native, but our test
		// set Deferred=true to keep the rule general; a future deferred read tool
		// stays out of the subtask view.
	}
	if _, ok := view.Lookup("agent.run"); ok {
		t.Fatal("Lookup(agent.run) must fail in subtask view")
	}
	// A subtask runs in its own orphaned child session.State that no ACP
	// client (or the TUI) ever sees — there is no live user who could
	// possibly answer a question.ask call. The prompt already tells the
	// model "you cannot prompt the user"; the registry must actually
	// enforce it, or the call would block forever waiting for an answer
	// that structurally cannot arrive.
	if names["question.ask"] {
		t.Fatal("subtask view must NOT contain question.ask (no user to answer it)")
	}
	if _, ok := view.Lookup("question.ask"); ok {
		t.Fatal("Lookup(question.ask) must fail in subtask view")
	}
	// ask_user is the alias for question.ask and routes through the same
	// orphaned child session.State, so it must be excluded for the same
	// reason.
	if names["ask_user"] {
		t.Fatal("subtask view must NOT contain ask_user (no user to answer it)")
	}
	if _, ok := view.Lookup("ask_user"); ok {
		t.Fatal("Lookup(ask_user) must fail in subtask view")
	}
}

func TestNewSubagentToolAgentArgResolves(t *testing.T) {
	called := ""
	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		called = req.Agent
		r := &Runner{RunTaskFunc: func(context.Context, string) (*Task, error) {
			return &Task{Summary: "ok"}, nil
		}}
		return r, nil, nil
	}
	tool := NewSubagentTool(factory, nil, registry.New(), session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}))
	res, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"prompt":"do it","description":"d","agent":"my-scout"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if called != "my-scout" {
		t.Fatalf("factory called with %q, want my-scout", called)
	}
	if !strings.Contains(res.Summary, "subagent completed") {
		t.Fatalf("summary = %q", res.Summary)
	}
}

func TestNewSubagentToolNoAgentArgStillWorks(t *testing.T) {
	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		if req.Agent != "" {
			t.Fatalf("factory called with %q, want empty", req.Agent)
		}
		return &Runner{RunTaskFunc: func(context.Context, string) (*Task, error) {
			return &Task{Summary: "ok"}, nil
		}}, nil, nil
	}
	tool := NewSubagentTool(factory, nil, registry.New(), session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}))
	if _, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"prompt":"do it","description":"d"}`),
	}); err != nil {
		t.Fatalf("handler: %v", err)
	}
}

// TestNewSubagentToolForwardsExplicitModel verifies that an explicit
// "provider/model" pair passed in the JSON payload is forwarded to the
// factory as SubagentRequest.Model alongside the named agent.
func TestNewSubagentToolForwardsExplicitModel(t *testing.T) {
	var got SubagentRequest
	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		got = req
		return &Runner{RunTaskFunc: func(context.Context, string) (*Task, error) {
			return &Task{Summary: "ok"}, nil
		}}, nil, nil
	}
	tool := NewSubagentTool(factory, nil, registry.New(), session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}))
	if _, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"prompt":"do it","description":"d","agent":"my-scout","model":"openai/gpt-4o-mini"}`),
	}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got.Agent != "my-scout" {
		t.Fatalf("req.Agent = %q, want my-scout", got.Agent)
	}
	if got.Model != "openai/gpt-4o-mini" {
		t.Fatalf("req.Model = %q, want openai/gpt-4o-mini", got.Model)
	}
}

// TestNewSubagentToolRecordsMetaFromRunner verifies that the handler records
// the child runner's resolved model and provider onto the registered
// SubagentView via RegisterSubagentWithMeta, so the TUI card can display
// what model actually ran the subagent.
func TestNewSubagentToolRecordsMetaFromRunner(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		r := &Runner{
			Model:    "gpt-4o-mini",
			Provider: &stubProvider{name: "ollama"},
			RunTaskFunc: func(context.Context, string) (*Task, error) {
				return &Task{Summary: "ok"}, nil
			},
		}
		return r, nil, nil
	}
	tool := NewSubagentTool(factory, nil, registry.New(), state)
	if _, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"prompt":"do it","description":"d","model":"ollama/gpt-4o-mini"}`),
	}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	subs := state.Subagents()
	if len(subs) != 1 {
		t.Fatalf("registered subagents = %d, want 1", len(subs))
	}
	if subs[0].Model != "gpt-4o-mini" {
		t.Fatalf("view.Model = %q, want gpt-4o-mini", subs[0].Model)
	}
	if subs[0].Provider != "ollama" {
		t.Fatalf("view.Provider = %q, want ollama", subs[0].Provider)
	}
}

// stubProvider is a minimal provider.Provider implementation for tests that
// need the metadata-recording path to observe a non-nil provider.
type stubProvider struct{ name string }

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Models(context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}
func (s *stubProvider) Chat(context.Context, schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	return nil, nil
}
func (s *stubProvider) Capabilities(context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

// TestNewSubagentToolModelDefaultsWhenOmitted verifies that omitting the
// model in the JSON payload forwards an empty Model to the factory (the
// default model selection), preserving existing ad-hoc behavior.
func TestNewSubagentToolModelDefaultsWhenOmitted(t *testing.T) {
	var got SubagentRequest
	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		got = req
		return &Runner{RunTaskFunc: func(context.Context, string) (*Task, error) {
			return &Task{Summary: "ok"}, nil
		}}, nil, nil
	}
	tool := NewSubagentTool(factory, nil, registry.New(), session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}))
	if _, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"prompt":"do it","description":"d"}`),
	}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got.Agent != "" {
		t.Fatalf("req.Agent = %q, want empty (ad-hoc)", got.Agent)
	}
	if got.Model != "" {
		t.Fatalf("req.Model = %q, want empty (default model)", got.Model)
	}
}

// TestNewSubagentToolFactoryErrorPreventsRegistration verifies that when
// the factory returns an error (e.g. an invalid provider/model pair), the
// handler surfaces that error and does NOT register a subagent view.
func TestNewSubagentToolFactoryErrorPreventsRegistration(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	factory := func(SubagentRequest) (*Runner, *session.State, error) {
		return nil, nil, errors.New("invalid provider/model pair")
	}
	tool := NewSubagentTool(factory, nil, registry.New(), state)
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"prompt":"do it","description":"d","model":"bogus/nope"}`),
	})
	if err == nil {
		t.Fatal("expected factory error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid provider/model pair") {
		t.Fatalf("error = %v, want it to surface the factory error", err)
	}
	if got := len(state.Subagents()); got != 0 {
		t.Fatalf("registered subagent views = %d, want 0", got)
	}
}

func stubAgentRunHandler(_ context.Context, _ registry.ToolCall) (registry.ToolResult, error) {
	return registry.ToolResult{Summary: "stub"}, nil
}

func TestSubagentModelConsentGate(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	factoryCalled := false
	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		factoryCalled = true
		return &Runner{RunTaskFunc: func(context.Context, string) (*Task, error) {
			return &Task{Summary: "ok"}, nil
		}}, nil, nil
	}
	// Resolver returns a paid model; parent is free.
	resolver := func(req SubagentRequest) (SubagentModelPreview, error) {
		return SubagentModelPreview{
			Model:    "gpt-4o",
			Provider: "openai",
			Pricing:  pricing.ModelPricing{InputPerMTokCents: 250, OutputPerMTokCents: 1000},
		}, nil
	}
	tool := NewSubagentTool(factory, resolver, registry.New(), state,
		WithSubagentParentModel("local-model", pricing.ModelPricing{}),
	)
	// Auto-approve the consent in a goroutine.
	go func() {
		time.Sleep(10 * time.Millisecond)
		if tc := state.PendingApproval(); tc != nil {
			tc.Respond(session.UserApprovalDecision{Approved: true})
		}
	}()
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"prompt":"do it","description":"d","model":"openai/gpt-4o"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !factoryCalled {
		t.Fatal("factory should have been called after consent approval")
	}
}

func TestSubagentModelConsentDenied(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	factoryCalled := false
	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		factoryCalled = true
		return &Runner{RunTaskFunc: func(context.Context, string) (*Task, error) {
			return &Task{Summary: "ok"}, nil
		}}, nil, nil
	}
	resolver := func(req SubagentRequest) (SubagentModelPreview, error) {
		return SubagentModelPreview{
			Model:    "gpt-4o",
			Provider: "openai",
			Pricing:  pricing.ModelPricing{InputPerMTokCents: 250, OutputPerMTokCents: 1000},
		}, nil
	}
	tool := NewSubagentTool(factory, resolver, registry.New(), state,
		WithSubagentParentModel("local-model", pricing.ModelPricing{}),
	)
	go func() {
		time.Sleep(10 * time.Millisecond)
		if tc := state.PendingApproval(); tc != nil {
			tc.Respond(session.UserApprovalDecision{Approved: false})
		}
	}()
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"prompt":"do it","description":"d","model":"openai/gpt-4o"}`),
	})
	if err == nil {
		t.Fatal("expected denial error, got nil")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Fatalf("error = %v, want 'denied'", err)
	}
	if factoryCalled {
		t.Fatal("factory must not be called when consent is denied")
	}
}

func TestSubagentModelConsentSkippedForSameModel(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	factoryCalled := false
	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		factoryCalled = true
		return &Runner{RunTaskFunc: func(context.Context, string) (*Task, error) {
			return &Task{Summary: "ok"}, nil
		}}, nil, nil
	}
	resolver := func(req SubagentRequest) (SubagentModelPreview, error) {
		return SubagentModelPreview{
			Model:    "local-model",
			Provider: "ollama",
			Pricing:  pricing.ModelPricing{},
		}, nil
	}
	tool := NewSubagentTool(factory, resolver, registry.New(), state,
		WithSubagentParentModel("local-model", pricing.ModelPricing{}),
	)
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"prompt":"do it","description":"d"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !factoryCalled {
		t.Fatal("factory should have been called (same model, no consent)")
	}
	if state.PendingApproval() != nil {
		t.Fatal("pending approval should not have been set for same model")
	}
}
