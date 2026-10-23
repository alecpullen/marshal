package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/hooks"
	"marshal/internal/interaction"
	"marshal/internal/permissions"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// modePtr is the pointer form interaction.Patch needs for a mode change.
func modePtr(m interaction.Mode) *interaction.Mode { return &m }

// answerApproval answers the next pending approval with d, failing the test if
// none appears. It is the single-answer form of the approval fixtures.
func answerApproval(t *testing.T, state *session.State, d session.UserApprovalDecision) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if tc := state.PendingApproval(); tc != nil {
			tc.Respond(d)
			return
		}
		select {
		case <-deadline:
			t.Error("timed out waiting for a pending approval")
			return
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

// onceRewriteTo rewrites the arguments exactly once, then passes through.
type onceRewriteTo struct {
	rewrite json.RawMessage
	done    bool
}

func (h *onceRewriteTo) RunPreToolUse(ctx context.Context, in hooks.PreToolUseInput) (hooks.Output, error) {
	if h.done {
		return hooks.Output{}, nil
	}
	h.done = true
	return hooks.Output{Rewrite: h.rewrite}, nil
}

func (h *onceRewriteTo) RunTurnEnd(ctx context.Context, in hooks.TurnEndInput) (hooks.Output, error) {
	return hooks.Output{}, nil
}

// blockingGate is a legacy WriteGate that parks the first acquisition until
// release is closed, giving a test a deterministic window between approval and
// launch.
type blockingGate struct {
	acquired chan struct{}
	release  chan struct{}
	once     sync.Once
}

func newBlockingGate() *blockingGate {
	return &blockingGate{acquired: make(chan struct{}), release: make(chan struct{})}
}

func (g *blockingGate) Acquire() func() {
	g.once.Do(func() { close(g.acquired) })
	<-g.release
	return func() {}
}

// TestDispatchAskCeilingRunsBeforeCachedRead pins that a cached read cannot
// bypass the current authority: once the session drops to Ask, a cached
// network read is refused instead of served from cache.
func TestDispatchAskCeilingRunsBeforeCachedRead(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	var calls atomic.Int32
	if err := reg.Register(registry.Tool{
		Name:      "net.read",
		Risk:      registry.RiskNetwork,
		Cacheable: true,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			calls.Add(1)
			return registry.ToolResult{Summary: "fetched", Content: "payload"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	pol := policy.NewEngine(&config.Config{}, nil)
	pol.WithRegistry(reg)
	pol.SetRules([]permissions.Rule{{Permission: "net.read", Pattern: "net.read", Action: permissions.ActionAllow}})
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, pol, state, "test-model")

	action := ModelAction{Type: ActionToolCall, Tool: "net.read", Args: json.RawMessage(`{}`), ToolCallID: "c1"}
	if _, err := r.executeToolCall(context.Background(), action); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}

	if _, err := state.ApplyInteraction(interaction.Patch{Mode: modePtr(interaction.ModeAsk)}); err != nil {
		t.Fatalf("ApplyInteraction: %v", err)
	}
	action2 := ModelAction{Type: ActionToolCall, Tool: "net.read", Args: json.RawMessage(`{}`), ToolCallID: "c2"}
	msgs, err := r.executeToolCall(context.Background(), action2)
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1: the cached read must not run under Ask", calls.Load())
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "denied") {
		t.Fatalf("messages = %v, want a denial", msgs)
	}
}

// TestDispatchEditedShellRequiresNewApproval pins that a shell edit is not
// implicitly approved by the decision that approved the original command: the
// edited command raises its own prompt.
func TestDispatchEditedShellRequiresNewApproval(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	var ran atomic.Value
	if err := reg.Register(registry.Tool{
		Name: "shell.run",
		Risk: registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			ran.Store(string(call.Args))
			return registry.ToolResult{Summary: "ran"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{}
	cfg.Tools.Shell.Confirm.Commands = []string{"echo *"}
	pol := policy.NewEngine(&cfg, nil)
	pol.WithRegistry(reg)
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, pol, state, "test-model")

	go func() {
		answerApproval(t, state, session.UserApprovalDecision{Approved: true, Edited: "echo edited"})
		answerApproval(t, state, session.UserApprovalDecision{Approved: true})
	}()

	action := ModelAction{Type: ActionToolCall, Tool: "shell.run", Args: json.RawMessage(`{"command":"echo original"}`), ToolCallID: "c1"}
	if _, err := r.executeToolCall(context.Background(), action); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got, _ := ran.Load().(string); !strings.Contains(got, "echo edited") {
		t.Fatalf("ran = %q, want the edited command", got)
	}
}

// TestDispatchHookRewriteRechecksAllEffects pins that a hook rewrite is
// re-evaluated: a benign original rewritten into a destructive command raises
// the approval the destructive command needs.
func TestDispatchHookRewriteRechecksAllEffects(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	var ran atomic.Value
	if err := reg.Register(registry.Tool{
		Name: "shell.run",
		Risk: registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			ran.Store(string(call.Args))
			return registry.ToolResult{Summary: "ran"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	pol := policy.NewEngine(&config.Config{}, nil)
	pol.WithRegistry(reg)
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, pol, state, "test-model")
	r.HookRunner = &onceRewriteTo{rewrite: json.RawMessage(`{"command":"rm -rf build"}`)}

	go answerApproval(t, state, session.UserApprovalDecision{Approved: true})

	action := ModelAction{Type: ActionToolCall, Tool: "shell.run", Args: json.RawMessage(`{"command":"echo safe"}`), ToolCallID: "c1"}
	if _, err := r.executeToolCall(context.Background(), action); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got, _ := ran.Load().(string); !strings.Contains(got, "rm -rf build") {
		t.Fatalf("ran = %q, want the rewritten command", got)
	}
}

// TestDispatchQueuedWriteRechecksAfterWriteGate pins that a write queued
// behind the write gate is re-checked against the live authority once it holds
// the gate: a mode change while queued refuses the launch.
func TestDispatchQueuedWriteRechecksAfterWriteGate(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	var ran atomic.Bool
	if err := reg.Register(registry.Tool{
		Name: "file.write",
		Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			ran.Store(true)
			return registry.ToolResult{Summary: "wrote"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	pol := policy.NewEngine(&config.Config{}, nil)
	pol.WithRegistry(reg)
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, pol, state, "test-model")
	gate := newBlockingGate()
	r.WriteGate = gate

	go func() {
		answerApproval(t, state, session.UserApprovalDecision{Approved: true})
		<-gate.acquired
		if _, err := state.ApplyInteraction(interaction.Patch{Mode: modePtr(interaction.ModeAsk)}); err != nil {
			t.Errorf("ApplyInteraction: %v", err)
		}
		close(gate.release)
	}()

	// An out-of-workspace write still needs System, so it is the write this
	// gate fixture can park behind a pending approval in normal Act.
	action := ModelAction{Type: ActionToolCall, Tool: "file.write", Args: json.RawMessage(`{"path":"/tmp/outside/a.txt","content":"x"}`), ToolCallID: "c1"}
	msgs, err := r.executeToolCall(context.Background(), action)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if ran.Load() {
		t.Fatal("the write ran after the authority switched to Ask while queued")
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "not started") {
		t.Fatalf("messages = %v, want a refusal", msgs)
	}
}

// TestDispatchExactApprovalCannotCreateStandingRule pins that an exact-only
// approval is marked as such and does not become a standing rule: a second
// identical call prompts again.
func TestDispatchExactApprovalCannotCreateStandingRule(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "shell.run",
		Risk: registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ran"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	pol := policy.NewEngine(&config.Config{}, nil)
	pol.WithRegistry(reg)
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, pol, state, "test-model")

	var exact atomic.Bool
	go func() {
		deadline := time.After(5 * time.Second)
		for {
			if tc := state.PendingApproval(); tc != nil {
				exact.Store(tc.ExactCommandApproval)
				tc.Respond(session.UserApprovalDecision{Approved: true})
				return
			}
			select {
			case <-deadline:
				return
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}()

	action := ModelAction{Type: ActionToolCall, Tool: "shell.run", Args: json.RawMessage(`{"command":"rm -rf build"}`), ToolCallID: "c1"}
	if _, err := r.executeToolCall(context.Background(), action); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !exact.Load() {
		t.Fatal("a destructive command must request exact approval")
	}

	prompted := make(chan struct{}, 1)
	go func() {
		deadline := time.After(5 * time.Second)
		for {
			if tc := state.PendingApproval(); tc != nil {
				prompted <- struct{}{}
				tc.Respond(session.UserApprovalDecision{Approved: true})
				return
			}
			select {
			case <-deadline:
				return
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}()
	action2 := ModelAction{Type: ActionToolCall, Tool: "shell.run", Args: json.RawMessage(`{"command":"rm -rf build"}`), ToolCallID: "c2"}
	if _, err := r.executeToolCall(context.Background(), action2); err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	select {
	case <-prompted:
	default:
		t.Fatal("the second identical call did not prompt: an exact approval created a standing rule")
	}
}

// TestDispatchShellProofConsumedOnlyAtLaunch pins that a shell tool receives
// the proof dispatch evaluated and admits exactly that attempt at launch.
func TestDispatchShellProofConsumedOnlyAtLaunch(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	cfg := config.Config{}
	cfg.Tools.Shell.Confirm.Commands = []string{"echo *"}
	pol := policy.NewEngine(&cfg, nil)
	pol.WithRegistry(reg)
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, pol, state, "test-model")

	var admitted atomic.Int32
	if err := reg.Register(registry.Tool{
		Name: "shell.run",
		Risk: registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			input, ok := policy.ShellInputFromContext(ctx)
			if !ok {
				return registry.ToolResult{}, errors.New("dispatch did not attach the shell proof")
			}
			admission, err := r.Authorizer.BeginCommand(ctx, policy.CommandAttempt{
				InvocationID: policy.InvocationID(ctx),
				ToolName:     "shell.run",
				Input:        input,
			})
			if err != nil {
				return registry.ToolResult{}, err
			}
			if err := admission.Commit(); err != nil {
				admission.Release()
				return registry.ToolResult{}, err
			}
			admission.Release()
			admitted.Add(1)
			return registry.ToolResult{Summary: "ran"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	go answerApproval(t, state, session.UserApprovalDecision{Approved: true})

	action := ModelAction{Type: ActionToolCall, Tool: "shell.run", Args: json.RawMessage(`{"command":"echo hi"}`), ToolCallID: "c1"}
	if _, err := r.executeToolCall(context.Background(), action); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if admitted.Load() != 1 {
		t.Fatalf("admitted = %d, want exactly 1", admitted.Load())
	}
}

// TestDispatchStaleApprovalCannotLaunchAfterAsk pins that an approval granted
// before a mode change cannot launch after it: the launch admission re-reads
// the live authority and refuses.
func TestDispatchStaleApprovalCannotLaunchAfterAsk(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	var ran atomic.Bool
	if err := reg.Register(registry.Tool{
		Name: "file.write",
		Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			ran.Store(true)
			return registry.ToolResult{Summary: "wrote"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	pol := policy.NewEngine(&config.Config{}, nil)
	pol.WithRegistry(reg)
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, pol, state, "test-model")
	gate := newBlockingGate()
	r.WriteGate = gate

	go func() {
		answerApproval(t, state, session.UserApprovalDecision{Approved: true})
		<-gate.acquired
		if _, err := state.ApplyInteraction(interaction.Patch{Mode: modePtr(interaction.ModeAsk)}); err != nil {
			t.Errorf("ApplyInteraction: %v", err)
		}
		close(gate.release)
	}()

	// An out-of-workspace write still needs System, so it is the write this
	// stale-approval fixture can park behind a pending approval in normal Act.
	action := ModelAction{Type: ActionToolCall, Tool: "file.write", Args: json.RawMessage(`{"path":"/tmp/outside/a.txt","content":"x"}`), ToolCallID: "c1"}
	msgs, err := r.executeToolCall(context.Background(), action)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if ran.Load() {
		t.Fatal("a stale approval launched the write after the authority switched to Ask")
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "not started") {
		t.Fatalf("messages = %v, want a refusal", msgs)
	}
}
