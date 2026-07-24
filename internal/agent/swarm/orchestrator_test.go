// internal/agent/swarm/orchestrator_test.go
package swarm

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/agenttest"
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

type scriptedOrchestrator struct {
	*Orchestrator
	mu    *sync.Mutex
	calls *[]factoryCall
}

func newScriptedOrchestrator(t *testing.T, state *session.State, summaries map[agent.AgentRole][]string) *scriptedOrchestrator {
	t.Helper()
	var mu sync.Mutex
	var calls []factoryCall
	factory := func(role agent.AgentRole, scope RegistryScope) (*agent.Runner, error) {
		mu.Lock()
		calls = append(calls, factoryCall{role: role, scope: scope})
		idx := 0
		for _, c := range calls {
			if c.role == role {
				idx++
			}
		}
		script := summaries[role]
		summary := ""
		if len(script) > 0 {
			if idx <= len(script) {
				summary = script[idx-1]
			} else {
				summary = script[len(script)-1]
			}
		}
		mu.Unlock()

		response := `{"rationale": "done", "action": {"type": "final", "content": "` + strings.ReplaceAll(summary, "\n", "\\n") + `"}}`
		r := agent.NewRunner(&agenttest.ScriptedProvider{Responses: []string{response}}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
		r.Role = role
		r.SetForceClass("question")
		r.MaxRetries = 0
		return r, nil
	}
	return &scriptedOrchestrator{Orchestrator: New(state, factory), mu: &mu, calls: &calls}
}

func (o *scriptedOrchestrator) callCount(role agent.AgentRole) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, c := range *o.calls {
		if c.role == role {
			n++
		}
	}
	return n
}

// newScriptedFactory returns a RunnerFactory whose runners answer with a
// single scripted final action per role, and records every factory call.
func newScriptedFactory(state *session.State, finals map[agent.AgentRole]string, calls *[]factoryCall, mu *sync.Mutex) RunnerFactory {
	return func(role agent.AgentRole, scope RegistryScope) (*agent.Runner, error) {
		mu.Lock()
		*calls = append(*calls, factoryCall{role: role, scope: scope})
		mu.Unlock()
		response := `{"rationale": "done", "action": {"type": "final", "content": "` + finals[role] + `"}}`
		r := agent.NewRunner(&agenttest.ScriptedProvider{Responses: []string{response}}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
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
		agent.RoleTester:      "VERDICT: PASS",
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
		{agent.RoleTester, ScopeTester},
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

	final := swarmSummaryMessage(t, state)
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
			r = agent.NewRunner(&agenttest.ScriptedProvider{Responses: []string{
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
		p := &agenttest.ScriptedProvider{Responses: []string{response}}
		if role == agent.RoleRepoScout {
			mu.Lock()
			scoutCount++
			failing := scoutCount == 1
			mu.Unlock()
			if failing {
				// Malformed forever -> RunTask exhausts iterations and errors.
				p = &agenttest.ScriptedProvider{Responses: []string{"not json at all"}}
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
	final := swarmSummaryMessage(t, state).Content
	if !strings.Contains(final, "scout failed") {
		t.Fatalf("final message should record the failed scout:\n%s", final)
	}
}

func swarmSummaryMessage(t *testing.T, state *session.State) session.Message {
	t.Helper()
	for _, msg := range state.Messages() {
		if strings.Contains(msg.Content, "Swarm complete") {
			return msg
		}
	}
	t.Fatal("missing swarm completion summary message")
	return session.Message{}
}

func TestOrchestratorAbortsWhenPlannerFails(t *testing.T) {
	state := newLockTestState(t)
	var mu sync.Mutex
	var calls []factoryCall
	factory := func(role agent.AgentRole, scope RegistryScope) (*agent.Runner, error) {
		mu.Lock()
		calls = append(calls, factoryCall{role: role, scope: scope})
		mu.Unlock()
		r := agent.NewRunner(&agenttest.ScriptedProvider{Responses: []string{"garbage"}}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
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

func TestSwarmLoopStopsOnPass(t *testing.T) {
	state := newLockTestState(t)
	o := newScriptedOrchestrator(t, state, map[agent.AgentRole][]string{
		agent.RolePlanner:     {"1. do the thing"},
		agent.RoleRepoScout:   {"found files"},
		agent.RoleImplementer: {"made the change"},
		agent.RoleTester:      {"ran tests\nVERDICT: PASS"},
		agent.RoleReviewer:    {"APPROVE looks good"},
	})
	o.MaxFixRounds = 3

	if err := o.Run(context.Background(), "add a test"); err != nil {
		t.Fatal(err)
	}
	if got := o.callCount(agent.RoleImplementer); got != 1 {
		t.Errorf("implementer ran %d times, want 1", got)
	}
	if got := o.callCount(agent.RoleTester); got != 1 {
		t.Errorf("tester ran %d times, want 1", got)
	}
}

func TestSwarmLoopRetriesOnFailThenPasses(t *testing.T) {
	state := newLockTestState(t)
	o := newScriptedOrchestrator(t, state, map[agent.AgentRole][]string{
		agent.RolePlanner:     {"plan"},
		agent.RoleRepoScout:   {"scout"},
		agent.RoleImplementer: {"attempt 1", "attempt 2"},
		agent.RoleTester:      {"fail\nVERDICT: FAIL", "pass\nVERDICT: PASS"},
		agent.RoleReviewer:    {"APPROVE"},
	})
	o.MaxFixRounds = 3

	if err := o.Run(context.Background(), "fix bug"); err != nil {
		t.Fatal(err)
	}
	if got := o.callCount(agent.RoleImplementer); got != 2 {
		t.Errorf("implementer ran %d times, want 2", got)
	}
	if got := o.callCount(agent.RoleTester); got != 2 {
		t.Errorf("tester ran %d times, want 2", got)
	}
}

func TestSwarmLoopStopsAtMaxRounds(t *testing.T) {
	state := newLockTestState(t)
	o := newScriptedOrchestrator(t, state, map[agent.AgentRole][]string{
		agent.RolePlanner:     {"plan"},
		agent.RoleRepoScout:   {"scout"},
		agent.RoleImplementer: {"a", "b", "c", "d"},
		agent.RoleTester:      {"f\nVERDICT: FAIL", "f\nVERDICT: FAIL", "f\nVERDICT: FAIL", "f\nVERDICT: FAIL"},
		agent.RoleReviewer:    {"APPROVE"},
	})
	o.MaxFixRounds = 2

	if err := o.Run(context.Background(), "hopeless"); err != nil {
		t.Fatal(err)
	}
	if got := o.callCount(agent.RoleImplementer); got != 2 {
		t.Errorf("implementer ran %d times, want 2", got)
	}
	if got := o.callCount(agent.RoleReviewer); got != 1 {
		t.Errorf("reviewer ran %d times, want 1", got)
	}
}

func TestSwarmUnparseableVerdictStopsLoop(t *testing.T) {
	state := newLockTestState(t)
	o := newScriptedOrchestrator(t, state, map[agent.AgentRole][]string{
		agent.RolePlanner:     {"plan"},
		agent.RoleRepoScout:   {"scout"},
		agent.RoleImplementer: {"change"},
		agent.RoleTester:      {"I could not tell"},
		agent.RoleReviewer:    {"APPROVE"},
	})
	o.MaxFixRounds = 3

	if err := o.Run(context.Background(), "ambiguous"); err != nil {
		t.Fatal(err)
	}
	if got := o.callCount(agent.RoleImplementer); got != 1 {
		t.Errorf("implementer ran %d times, want 1", got)
	}
}

// TestOverBudgetRechecksAfterRole reproduces F-POL-67: a parallel
// scout completing during the implementer role pushes the meter
// over budget; the next overBudget check must observe the new
// total and stop the loop.
func TestOverBudgetRechecksAfterRole(t *testing.T) {
	o := &Orchestrator{MaxTotalTokens: 100}
	m := NewEstimateMeter()
	// Pre-fill to 90, then run a "role" that adds 20.
	m.Observe(agent.RolePlanner, schema.TokenUsage{CompletionTokens: 90})
	if o.overBudget(m) {
		t.Fatal("pre-fill should not be over budget")
	}
	m.Observe(agent.RoleImplementer, schema.TokenUsage{CompletionTokens: 20})
	if !o.overBudget(m) {
		t.Fatal("post-role observation should put us over budget")
	}
}

func TestSwarmTokenCeilingStopsAfterCurrentRole(t *testing.T) {
	state := newLockTestState(t)
	o := newScriptedOrchestrator(t, state, map[agent.AgentRole][]string{
		agent.RolePlanner:     {strings.Repeat("word ", 500)},
		agent.RoleRepoScout:   {"scout"},
		agent.RoleImplementer: {"change"},
		agent.RoleTester:      {"pass\nVERDICT: PASS"},
		agent.RoleReviewer:    {"APPROVE"},
	})
	o.MaxTotalTokens = 10

	if err := o.Run(context.Background(), "big"); err != nil {
		t.Fatal(err)
	}
	if got := o.callCount(agent.RoleImplementer); got != 0 {
		t.Errorf("implementer ran %d times, want 0", got)
	}
}

func TestSwarmPublishesProgress(t *testing.T) {
	state := newLockTestState(t)
	o := newScriptedOrchestrator(t, state, map[agent.AgentRole][]string{
		agent.RolePlanner:     {"plan"},
		agent.RoleRepoScout:   {"scout"},
		agent.RoleImplementer: {"change"},
		agent.RoleTester:      {"pass\nVERDICT: PASS"},
		agent.RoleReviewer:    {"APPROVE"},
	})
	o.MaxFixRounds = 1

	if err := o.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if state.SwarmProgress().Active {
		t.Error("progress should be cleared after run completes")
	}
}

func TestOrchestratorUsesRealTokenUsage(t *testing.T) {
	state := newLockTestState(t)

	factory := func(role agent.AgentRole, scope RegistryScope) (*agent.Runner, error) {
		response := `{"rationale": "done", "action": {"type": "final", "content": "mock content"}}`
		p := &usageScriptedProvider{
			response: response,
			usage: schema.TokenUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			},
		}
		r := agent.NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
		r.Role = role
		r.SetForceClass("question")
		r.MaxRetries = 0
		return r, nil
	}

	o := New(state, factory)
	o.MaxFixRounds = 1
	o.MaxTotalTokens = 10000

	if err := o.Run(context.Background(), "run with real tokens"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	msgs := state.Messages()
	var found bool
	for _, m := range msgs {
		if strings.Contains(m.Content, "Token budget:") {
			found = true
			if !strings.Contains(m.Content, "~1050 / 10000") {
				t.Fatalf("expected '~1050 / 10000' in final message, got: %q", m.Content)
			}
		}
	}
	if !found {
		t.Fatal("no token budget message found")
	}
}

type usageScriptedProvider struct {
	response string
	usage    schema.TokenUsage
}

func (p *usageScriptedProvider) Name() string { return "usage-scripted" }
func (p *usageScriptedProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}
func (p *usageScriptedProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}
func TestSetRoleAnnouncesTerminalTransitionsOnce(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.SetSwarmProgress(session.SwarmProgress{
		Active: true,
		Roles: []session.SwarmRole{
			{Name: "tester", Status: session.SwarmRolePending},
		},
	})
	o := &Orchestrator{State: state}

	o.setRole(nil, "tester", session.SwarmRoleActive, "round 1/2")
	o.setRole(nil, "tester", session.SwarmRoleDone, "round 1/2")
	o.setRole(nil, "tester", session.SwarmRoleDone, "round 2/2")

	var notices []string
	for _, item := range state.Transcript() {
		if item.Message != nil && item.Message.Role == session.RoleSystem {
			notices = append(notices, item.Message.Content)
		}
	}
	if len(notices) != 1 {
		t.Fatalf("expected exactly one role event, got %d: %v", len(notices), notices)
	}
	if !strings.Contains(notices[0], "tester done") || !strings.Contains(notices[0], "round 1/2") {
		t.Fatalf("event text = %q, want `tester done · round 1/2`", notices[0])
	}
	if got := state.SwarmProgress().Roles[0].Detail; got != "round 2/2" {
		t.Fatalf("roster detail = %q, want the latest value %q", got, "round 2/2")
	}
}

func (p *usageScriptedProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	ch := make(chan schema.ChatEvent, 2)
	ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: p.response}
	ch <- schema.ChatEvent{Type: schema.ChatEventDone, Usage: &p.usage}
	close(ch)
	return ch, nil
}
