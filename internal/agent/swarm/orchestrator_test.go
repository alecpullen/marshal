// internal/agent/swarm/orchestrator_test.go
package swarm

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

type factoryCall struct {
	role  agent.AgentRole
	scope RegistryScope
}

// newScriptedFactory returns a RunnerFactory whose runners answer with a
// single scripted final action per role, and records every factory call.
func newScriptedFactory(state *session.State, finals map[agent.AgentRole]string, calls *[]factoryCall, mu *sync.Mutex) RunnerFactory {
	return func(role agent.AgentRole, scope RegistryScope) (*agent.Runner, error) {
		mu.Lock()
		*calls = append(*calls, factoryCall{role: role, scope: scope})
		mu.Unlock()
		response := `{"rationale": "done", "action": {"type": "final", "content": "` + finals[role] + `"}}`
		r := agent.NewRunner(&scriptedProvider{responses: []string{response}}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
		r.Role = role
		r.SetForceClass("question")
		return r, nil
	}
}

func TestOrchestratorRunsRolesInSequenceAndPublishesTaskState(t *testing.T) {
	state := newLockTestState(t)
	var mu sync.Mutex
	var calls []factoryCall
	finals := map[agent.AgentRole]string{
		agent.RolePlanner:     "1. reproduce\\n2. fix",
		agent.RoleRepoScout:   "parser.go is the hot spot",
		agent.RoleImplementer: "patched parser.go",
		agent.RoleReviewer:    "APPROVE: change is minimal",
	}
	o := New(state, newScriptedFactory(state, finals, &calls, &mu))

	if err := o.Run(context.Background(), "fix the parser"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantOrder := []factoryCall{
		{agent.RolePlanner, ScopeReadOnly},
		{agent.RoleRepoScout, ScopeReadOnly},
		{agent.RoleRepoScout, ScopeReadOnly},
		{agent.RoleRepoScout, ScopeReadOnly},
		{agent.RoleImplementer, ScopeFull},
		{agent.RoleReviewer, ScopeReadOnly},
	}
	if len(calls) != len(wantOrder) {
		t.Fatalf("factory called %d times, want %d: %+v", len(calls), len(wantOrder), calls)
	}
	for i, want := range wantOrder {
		if calls[i] != want {
			t.Fatalf("factory call %d = %+v, want %+v", i, calls[i], want)
		}
	}

	messages := state.Messages()
	final := messages[len(messages)-1]
	for _, want := range []string{"Swarm complete", "1. reproduce", "parser.go is the hot spot", "patched parser.go", "APPROVE"} {
		if !strings.Contains(final.Content, want) {
			t.Fatalf("final swarm message missing %q:\n%s", want, final.Content)
		}
	}
}

// barrierProvider blocks every Chat call until `parties` calls have
// arrived, proving the callers run concurrently. If they run sequentially
// the first call never unblocks and the test times out.
type barrierProvider struct {
	mu      sync.Mutex
	arrived int
	parties int
	release chan struct{}
	final   string
}

func (p *barrierProvider) Name() string                                           { return "barrier" }
func (p *barrierProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) { return nil, nil }
func (p *barrierProvider) Embed(ctx context.Context, req schema.EmbedRequest) (schema.EmbedResponse, error) {
	return schema.EmbedResponse{}, nil
}
func (p *barrierProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}
func (p *barrierProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	p.mu.Lock()
	p.arrived++
	if p.arrived == p.parties {
		close(p.release)
	}
	p.mu.Unlock()

	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	ch := make(chan schema.ChatEvent, 2)
	ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: p.final}
	ch <- schema.ChatEvent{Type: schema.ChatEventDone}
	close(ch)
	return ch, nil
}

func TestOrchestratorRunsScoutsInParallel(t *testing.T) {
	state := newLockTestState(t)
	scoutBarrier := &barrierProvider{
		parties: len(DefaultScoutFocuses),
		release: make(chan struct{}),
		final:   `{"rationale": "done", "action": {"type": "final", "content": "found"}}`,
	}
	factory := func(role agent.AgentRole, scope RegistryScope) (*agent.Runner, error) {
		var r *agent.Runner
		if role == agent.RoleRepoScout {
			r = agent.NewRunner(scoutBarrier, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
		} else {
			r = agent.NewRunner(&scriptedProvider{responses: []string{
				`{"rationale": "done", "action": {"type": "final", "content": "ok"}}`,
			}}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
		}
		r.Role = role
		r.SetForceClass("question")
		r.MaxRetries = 0
		return r, nil
	}
	o := New(state, factory)

	done := make(chan error, 1)
	go func() { done <- o.Run(context.Background(), "goal") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("orchestrator deadlocked: scouts did not run in parallel")
	}
}

func TestOrchestratorContinuesWhenAScoutFails(t *testing.T) {
	state := newLockTestState(t)
	scoutCount := 0
	var mu sync.Mutex
	factory := func(role agent.AgentRole, scope RegistryScope) (*agent.Runner, error) {
		response := `{"rationale": "done", "action": {"type": "final", "content": "ok"}}`
		p := &scriptedProvider{responses: []string{response}}
		if role == agent.RoleRepoScout {
			mu.Lock()
			scoutCount++
			failing := scoutCount == 1
			mu.Unlock()
			if failing {
				// Malformed forever -> RunTask exhausts iterations and errors.
				p = &scriptedProvider{responses: []string{"not json at all"}}
			}
		}
		r := agent.NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
		r.Role = role
		r.SetForceClass("question")
		r.MaxToolIterations = 2
		r.MaxRetries = 0
		return r, nil
	}
	o := New(state, factory)

	if err := o.Run(context.Background(), "goal"); err != nil {
		t.Fatalf("Run should tolerate scout failure, got: %v", err)
	}
	messages := state.Messages()
	final := messages[len(messages)-1].Content
	if !strings.Contains(final, "scout failed") {
		t.Fatalf("final message should record the failed scout:\n%s", final)
	}
}

func TestOrchestratorAbortsWhenPlannerFails(t *testing.T) {
	state := newLockTestState(t)
	var mu sync.Mutex
	var calls []factoryCall
	factory := func(role agent.AgentRole, scope RegistryScope) (*agent.Runner, error) {
		mu.Lock()
		calls = append(calls, factoryCall{role: role, scope: scope})
		mu.Unlock()
		r := agent.NewRunner(&scriptedProvider{responses: []string{"garbage"}}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
		r.Role = role
		r.SetForceClass("question")
		r.MaxToolIterations = 2
		r.MaxRetries = 0
		return r, nil
	}
	o := New(state, factory)

	if err := o.Run(context.Background(), "goal"); err == nil {
		t.Fatal("Run should fail when the planner fails")
	}
	for _, c := range calls {
		if c.role != agent.RolePlanner {
			t.Fatalf("no role beyond planner should run, but factory built %q", c.role)
		}
	}
}
