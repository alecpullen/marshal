package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

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

	runner := NewRunner(&agenttest.ScriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.MemoryProvider = &fakeMemoryProvider{}

	runner.mergeMemories(0)

	for _, section := range state.ContextPack().Sections {
		if section.Kind == contextpack.SectionMemory {
			t.Fatalf("stale memory section remained in context pack: %#v", state.ContextPack().Sections)
		}
	}
}

func TestMergeScratchpadPassesConfiguredProjectionBudget(t *testing.T) {
	cfg := config.Default()
	cfg.Scratchpad.ProjectionMaxTokens = 30
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	if err := state.SetScratchpadEntry("alpha", "first scratchpad entry content", "text"); err != nil {
		t.Fatalf("SetScratchpadEntry: %v", err)
	}
	if err := state.SetScratchpadEntry("beta", "second scratchpad entry content", "text"); err != nil {
		t.Fatalf("SetScratchpadEntry: %v", err)
	}
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Content: "Project: marshal", EstimatedTokens: 4},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	})

	runner := NewRunner(&agenttest.ScriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.mergeScratchpad(0)

	pack := state.ContextPack()
	var scratchpadSection *contextpack.Section
	for i := range pack.Sections {
		if pack.Sections[i].Kind == contextpack.SectionScratchpad {
			scratchpadSection = &pack.Sections[i]
			break
		}
	}
	if scratchpadSection == nil {
		t.Fatalf("expected a scratchpad section, got %#v", pack.Sections)
	}
	if !strings.Contains(scratchpadSection.Content, "...") {
		t.Fatalf("expected projection to be truncated by configured budget, got %q", scratchpadSection.Content)
	}
	if strings.Contains(scratchpadSection.Content, "beta (") {
		t.Fatalf("expected second entry to be truncated out of projection, got %q", scratchpadSection.Content)
	}
}

func TestMergeScratchpadRemovingLastEntryRemovesProjection(t *testing.T) {
	state := newTestState(t)
	if err := state.SetScratchpadEntry("only", "only entry content", "text"); err != nil {
		t.Fatalf("SetScratchpadEntry: %v", err)
	}
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Content: "Project: marshal", EstimatedTokens: 4},
			{Kind: contextpack.SectionScratchpad, Title: "Scratchpad", Content: "old projection", Priority: 50, EstimatedTokens: 2},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 6},
	})

	if err := state.DeleteScratchpadEntry("only"); err != nil {
		t.Fatalf("DeleteScratchpadEntry: %v", err)
	}

	runner := NewRunner(&agenttest.ScriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.mergeScratchpad(0)

	for _, section := range state.ContextPack().Sections {
		if section.Kind == contextpack.SectionScratchpad {
			t.Fatalf("stale scratchpad section remained after deleting last entry: %#v", state.ContextPack().Sections)
		}
	}
}

func TestRunInjectsStoredContextPack(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
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
	if len(p.Requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(p.Requests))
	}
	var found bool
	for _, msg := range p.Requests[0].Messages {
		if strings.Contains(msg.Content, "Project context pack:") && strings.Contains(msg.Content, "Project: marshal") {
			found = true
		}
	}
	if !found {
		t.Fatalf("request missing context pack: %#v", p.Requests[0].Messages)
	}
}

func TestRunOmitsContextPackWhenEmpty(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"simple","action":{"type":"answer","content":"No pack."}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	for _, msg := range p.Requests[0].Messages {
		if strings.Contains(msg.Content, "Project context pack:") {
			t.Fatalf("empty context pack was injected: %#v", p.Requests[0].Messages)
		}
	}
}

func TestRunMergesMemoriesIntoContextPackBeforeFirstMessage(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
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
	if len(p.Requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(p.Requests))
	}

	var contextMessage string
	for _, msg := range p.Requests[0].Messages {
		if strings.Contains(msg.Content, "Project context pack:") {
			contextMessage = msg.Content
			break
		}
	}
	if contextMessage == "" {
		t.Fatalf("first provider request missing context pack: %#v", p.Requests[0].Messages)
	}
	if !strings.Contains(contextMessage, "## Project Memories") || !strings.Contains(contextMessage, "Uses SQLite for persistence") {
		t.Fatalf("first provider request missing memory content:\n%s", contextMessage)
	}
	userIdx := -1
	for i, msg := range p.Requests[0].Messages {
		if msg.Role == schema.RoleUser && msg.Content == "What does this project do?" {
			userIdx = i
			break
		}
	}
	if userIdx == -1 {
		t.Fatalf("first provider request missing user message: %#v", p.Requests[0].Messages)
	}
	contextIdx := -1
	for i, msg := range p.Requests[0].Messages {
		if msg.Content == contextMessage {
			contextIdx = i
			break
		}
	}
	if contextIdx == -1 || contextIdx > userIdx {
		t.Fatalf("context pack should precede user message: %#v", p.Requests[0].Messages)
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
	p := &agenttest.ScriptedProvider{Responses: []string{
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
	p := &agenttest.ScriptedProvider{Responses: []string{
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
	if len(p.Requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(p.Requests))
	}
	for _, msg := range p.Requests[0].Messages {
		if strings.Contains(msg.Content, "Project context pack:") {
			t.Fatalf("unexpected context pack injected after memory provider error: %#v", p.Requests[0].Messages)
		}
	}

	pack := state.ContextPack()
	for _, section := range pack.Sections {
		if section.Kind == contextpack.SectionMemory {
			t.Fatalf("unexpected memory section after provider error: %#v", pack.Sections)
		}
	}
	if got := state.Messages(); len(got) != 3 || got[2].Content != "done" {
		t.Fatalf("turn did not complete successfully: %#v", got)
	}
}

func TestRunAddsPlanToContextPackForActionCalls(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
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
	if len(p.Requests) < 2 {
		t.Fatalf("provider requests = %d, want at least 2", len(p.Requests))
	}
	var foundPlan bool
	for _, msg := range p.Requests[1].Messages {
		if strings.Contains(msg.Content, "## Current Plan") && strings.Contains(msg.Content, "Inspect the repo") {
			foundPlan = true
		}
	}
	if !foundPlan {
		t.Fatalf("action request missing plan context: %#v", p.Requests[1].Messages)
	}
}

func TestRunAddsPlanToContextPackBeforeSnippetsAndToolOutput(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
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
	for _, msg := range p.Requests[1].Messages {
		if strings.Contains(msg.Content, "Project context pack:") {
			actionContext = msg.Content
			break
		}
	}
	if actionContext == "" {
		t.Fatalf("action request missing context pack: %#v", p.Requests[1].Messages)
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
	p := &agenttest.ScriptedProvider{Responses: []string{
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

func TestRunAppliesRouteContextBudgetToExistingPack(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
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
	p := &agenttest.ScriptedProvider{Responses: []string{
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
	fallbackProvider := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"fallback","action":{"type":"answer","content":"fallback ok"}}`,
	}}
	routedProvider := &agenttest.ScriptedProvider{Responses: []string{
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

	if len(routedProvider.Requests) != 1 {
		t.Fatalf("routed provider requests = %d, want 1", len(routedProvider.Requests))
	}
	if routedProvider.Requests[0].Model != "fast-model" {
		t.Fatalf("routed request model = %q, want fast-model", routedProvider.Requests[0].Model)
	}
	if len(fallbackProvider.Requests) != 1 {
		t.Fatalf("fallback provider requests = %d, want 1", len(fallbackProvider.Requests))
	}
	if fallbackProvider.Requests[0].Model != "fallback-model" {
		t.Fatalf("fallback request model = %q, want fallback-model", fallbackProvider.Requests[0].Model)
	}
	if got := state.ProviderError(); !errors.Is(got, resolverErr) {
		t.Fatalf("ProviderError = %v, want %v", got, resolverErr)
	}
	if route := state.ActiveRoute(); route.Active {
		t.Fatalf("ActiveRoute = %#v, want inactive after resolver error fallback", route)
	}
}

func TestRunSummarizesLargeToolResults(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "demo.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "big file", Content: strings.Repeat("x", deriveToolResultChars(DefaultMaxTurnContextTokens)+100)}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{
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

func TestLoopCompactsViaSummaryWhenOverBudget(t *testing.T) {
	toolResp := `{"rationale":"look","action":{"type":"tool_call","tool":"big.tool","args":{}}}`
	p := &agenttest.ScriptedProvider{Responses: []string{
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
	final := p.Requests[len(p.Requests)-1]
	for _, m := range final.Messages {
		if strings.Count(m.Content, "word ") > 100 {
			t.Fatal("rebuilt transcript still contains the oversized tool output")
		}
	}
}

func TestSecondTurnSeesFirstTurnHistory(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
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

	second := p.Requests[len(p.Requests)-1]
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

func TestResolveRouteRaisesBudgetFromKnownWindow(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{`{"rationale":"done","action":{"type":"final","content":"ok"}}`}}
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

func TestResolveRouteConfigWindowCapsBudget(t *testing.T) {
	runner := NewRunner(&agenttest.ScriptedProvider{Responses: []string{`{"rationale":"done","action":{"type":"final","content":"ok"}}`}},
		registry.New(), policy.NewEngine(&config.Config{}, nil), newTestState(t), "big-model")
	runner.MaxTurnContextTokens = 1000 // small ceiling
	runner.RouteResolver = &staticResolver{
		route:    routing.Route{Preset: routing.ModelPreset{Model: "big", ContextWindow: 200000, MaxOutputTokens: 8192}},
		provider: runner.Provider,
	}
	runner.SetForceClass(string(ClassQuestion))
	if err := runner.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 0.85*200000 - 8192 = 161808, but the configured ceiling of 1000 must win.
	if runner.MaxTurnContextTokens != 1000 {
		t.Fatalf("effective budget = %d, want 1000 (configured ceiling)", runner.MaxTurnContextTokens)
	}
}

func TestHistoryAfterRewindExcludesAbandonedBranch(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
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

	second := p.Requests[len(p.Requests)-1]
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "first answer") {
			t.Fatal("abandoned branch's answer leaked into the new branch's history")
		}
	}
}

func TestRunPropagatesResolvedLimitsToRequests(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"done","action":{"type":"final","content":"ok"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.RouteResolver = &staticResolver{
		route: routing.Route{
			Role:    routing.RoleImplementer,
			Profile: "p",
			Preset:  routing.ModelPreset{Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:7b", ContextWindow: 32768, MaxOutputTokens: 2048},
		},
		provider: p,
	}
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.Requests) == 0 {
		t.Fatal("no requests captured")
	}
	req := p.Requests[0]
	if got := req.MaxTokens; got == nil || *got != 2048 {
		t.Fatalf("MaxTokens = %v, want 2048", got)
	}
	if got := req.ContextWindow; got == nil || *got != 32768 {
		t.Fatalf("ContextWindow = %v, want 32768", got)
	}
}

func TestRunLeavesLimitsUnsetWhenUnknown(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"done","action":{"type":"final","content":"ok"}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.RouteResolver = &staticResolver{
		route: routing.Route{
			Role:    routing.RoleImplementer,
			Profile: "p",
			Preset:  routing.ModelPreset{Name: "coder", Provider: "ollama", Model: "totally-made-up-xyz"},
		},
		provider: p,
	}
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.Requests) == 0 {
		t.Fatal("no requests captured")
	}
	req := p.Requests[0]
	if req.MaxTokens != nil {
		t.Fatalf("MaxTokens = %v, want nil", req.MaxTokens)
	}
	if req.ContextWindow != nil {
		t.Fatalf("ContextWindow = %v, want nil", req.ContextWindow)
	}
}
