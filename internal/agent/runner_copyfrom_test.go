package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// TestCopyFromRefreshesHooks guards the reload path: app.reloadAgentRuntime
// mutates a live runner in place via CopyFrom, so the rebuilt runner's
// session-scoped hooks must be adopted or a config reload silently keeps the
// stale TitleManager/Classifier closures bound to the old route.
func TestCopyFromRefreshesHooks(t *testing.T) {
	classifierCalled := false
	classifier := func(ctx context.Context, goal string) (TaskClass, error) {
		classifierCalled = true
		return ClassEdit, nil
	}

	source := &Runner{
		HookRunner:   fakeHookRunner{},
		TitleManager: fakeTitleManager{},
		Classifier:   classifier,
	}
	target := &Runner{}
	target.CopyFrom(source)

	if target.Classifier == nil {
		t.Fatal("CopyFrom did not transfer Classifier")
	}
	if _, err := target.Classifier(context.Background(), "goal"); err != nil {
		t.Fatalf("transferred Classifier errored: %v", err)
	}
	if !classifierCalled {
		t.Fatal("transferred Classifier closure was not the source's")
	}
	if target.TitleManager == nil {
		t.Fatal("CopyFrom did not transfer TitleManager")
	}
	if target.HookRunner == nil {
		t.Fatal("CopyFrom did not transfer HookRunner")
	}

	// A target that had prior hooks must have them replaced, not preserved.
	source2 := &Runner{}
	target2 := &Runner{Classifier: classifier}
	target2.CopyFrom(source2)
	if target2.Classifier != nil {
		t.Fatal("CopyFrom preserved a stale Classifier when source had nil")
	}
}

// TestCopyFromTransfersVerificationGate guards the reload path for the
// verification-gate fallback: app.reloadAgentRuntime rebuilds the runner and
// mutates the live one in place via CopyFrom, so the global gate flag must
// transfer, and a gate-off source must clear a stale on-value.
func TestCopyFromTransfersVerificationGate(t *testing.T) {
	target := &Runner{}
	target.CopyFrom(&Runner{VerificationGate: true})
	if !target.VerificationGate {
		t.Fatal("CopyFrom did not transfer VerificationGate")
	}

	target2 := &Runner{VerificationGate: true}
	target2.CopyFrom(&Runner{})
	if target2.VerificationGate {
		t.Fatal("CopyFrom preserved a stale VerificationGate when source had false")
	}
}

type fakeTitleManager struct{}

func (fakeTitleManager) OnUserTurn(context.Context, string) {}

// seqCounter hands out monotonically increasing sequence numbers so a test
// can assert that one event happened before another without relying on
// timing or goroutine scheduling.
type seqCounter struct {
	mu sync.Mutex
	n  int
}

func (c *seqCounter) next() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n
}

// recordingTitleManager records the sequence number and goal of every
// OnUserTurn call.
type recordingTitleManager struct {
	seq *seqCounter

	mu     sync.Mutex
	goals  []string
	orders []int
}

func (m *recordingTitleManager) OnUserTurn(_ context.Context, goal string) {
	order := m.seq.next()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.goals = append(m.goals, goal)
	m.orders = append(m.orders, order)
}

func (m *recordingTitleManager) snapshot() ([]string, []int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.goals...), append([]int(nil), m.orders...)
}

// seqProvider wraps a scripted provider and records the sequence number of
// every Chat call, so a test can prove the title manager ran before the
// turn's first provider request.
type seqProvider struct {
	*agenttest.ScriptedProvider
	seq *seqCounter

	mu     sync.Mutex
	orders []int
}

func (p *seqProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	order := p.seq.next()
	p.mu.Lock()
	p.orders = append(p.orders, order)
	p.mu.Unlock()
	return p.ScriptedProvider.Chat(ctx, req)
}

// TestRunnerInvokesTitleManagerOnTurnStart proves the runner drives the
// turn-start titler on a real RunTask turn: the manager is invoked with the
// user's goal before the turn's first provider Chat, and again on a second
// turn (the manager itself decides generate-vs-drift; the runner just calls
// it at every turn start).
func TestRunnerInvokesTitleManagerOnTurnStart(t *testing.T) {
	state := newTestState(t)
	seq := &seqCounter{}
	p := &seqProvider{
		ScriptedProvider: &agenttest.ScriptedProvider{
			Responses: scriptRepeats(2, `{"rationale":"done","action":{"type":"final","content":"ok"}}`),
		},
		seq: seq,
	}
	mgr := &recordingTitleManager{seq: seq}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.TitleManager = mgr

	if _, err := r.RunTask(context.Background(), "first goal"); err != nil {
		t.Fatalf("first RunTask err = %v", err)
	}
	goals, orders := mgr.snapshot()
	if len(goals) != 1 {
		t.Fatalf("title manager invoked %d times after first turn, want 1", len(goals))
	}
	if goals[0] != "first goal" {
		t.Fatalf("title manager goal = %q, want %q", goals[0], "first goal")
	}
	if len(p.orders) == 0 {
		t.Fatal("main provider Chat was never called")
	}
	if orders[0] > p.orders[0] {
		t.Fatalf("title manager ran after the turn's first provider Chat (manager seq %d, chat seq %d)", orders[0], p.orders[0])
	}

	// Second turn: invoked again, still before that turn's first Chat.
	if _, err := r.RunTask(context.Background(), "second goal"); err != nil {
		t.Fatalf("second RunTask err = %v", err)
	}
	goals, orders = mgr.snapshot()
	if len(goals) != 2 {
		t.Fatalf("title manager invoked %d times after second turn, want 2", len(goals))
	}
	if goals[1] != "second goal" {
		t.Fatalf("second title manager goal = %q, want %q", goals[1], "second goal")
	}
	if orders[1] > p.orders[1] {
		t.Fatalf("title manager ran after the second turn's provider Chat (manager seq %d, chat seq %d)", orders[1], p.orders[1])
	}
}

// TestRunnerSkipsTitleManagerForSubagent proves the depth-0 gate on the
// turn-start titler: a nested subagent session (depth > 0) must not drive the
// title manager at all, because titling is a top-level-session concern. The
// chat itself must still proceed normally.
func TestRunnerSkipsTitleManagerForSubagent(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{}, session.WithDepth(1))
	seq := &seqCounter{}
	p := &seqProvider{
		ScriptedProvider: &agenttest.ScriptedProvider{
			Responses: scriptRepeats(1, `{"rationale":"done","action":{"type":"final","content":"ok"}}`),
		},
		seq: seq,
	}
	mgr := &recordingTitleManager{seq: seq}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.TitleManager = mgr

	if _, err := r.RunTask(context.Background(), "subagent goal"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}

	if goals, _ := mgr.snapshot(); len(goals) != 0 {
		t.Fatalf("title manager invoked %d times for a depth-%d session, want 0", len(goals), state.SubagentDepth())
	}
	if len(p.orders) == 0 {
		t.Fatal("main provider Chat was never called; the turn did not proceed")
	}
}
