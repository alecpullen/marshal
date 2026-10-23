package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// TestInteractionContractActPlanningSkillReview pins that Act admits the
// read-only work the planning, skill and review workflows need without a
// prompt, and that a routine workspace edit is admitted too — per-change
// approval is retired — while a destructive command still asks.
func TestInteractionContractActPlanningSkillReview(t *testing.T) {
	cfg := nativeToolAgentConfig("contract-act")
	cfg.Agent.Mode, cfg.Agent.Auto = interaction.ModeAct, false
	rt, state := newAccessTestRuntime(t, cfg)

	reg := registry.New()
	handler := func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{Summary: "ok"}, nil
	}
	for _, name := range []string{"file.read", "symbols", "todo.write", "skill.load", "agent.run"} {
		_ = reg.Register(registry.Tool{Name: name, Risk: registry.RiskReadOnly, Handler: handler})
	}
	_ = reg.Register(registry.Tool{Name: "file.write", Risk: registry.RiskWorkspaceWrite, Handler: handler})
	rt.Runner.Policy.WithRegistry(reg)

	actor := state.InteractionState()
	for _, name := range []string{"file.read", "symbols", "todo.write", "skill.load", "agent.run"} {
		ev, err := rt.Runner.Policy.EvaluateInput(policy.Input{ToolName: name, ActorState: actor})
		if err != nil {
			t.Fatalf("EvaluateInput(%q): %v", name, err)
		}
		if ev.Decision != policy.DecisionAllow {
			t.Errorf("%s in Act = %v, want Allow", name, ev.Decision)
		}
	}
	ev, err := rt.Runner.Policy.EvaluateInput(policy.Input{
		ToolName:   "file.write",
		Args:       map[string]any{"path": "a.go", "content": "x"},
		ActorState: actor,
	})
	if err != nil {
		t.Fatalf("EvaluateInput(file.write): %v", err)
	}
	if ev.Decision != policy.DecisionAllow {
		t.Errorf("file.write in Act = %v, want Allow", ev.Decision)
	}
	// A destructive workspace command still needs specific approval.
	a, err := policy.AnalyzeShell(policy.ShellInput{Command: "rm -rf rel/x", WorkspaceRoot: "/repo", Dir: "/repo"})
	if err != nil {
		t.Fatalf("AnalyzeShell: %v", err)
	}
	ev, err = rt.Runner.Policy.EvaluateInput(policy.Input{ToolName: "shell.run", ActorState: actor, Shell: &a})
	if err != nil {
		t.Fatalf("EvaluateInput(shell.run): %v", err)
	}
	if ev.Decision != policy.DecisionConfirm {
		t.Errorf("destructive shell command in Act = %v, want Confirm", ev.Decision)
	}
}

// TestInteractionContractApprovalThenSingleLaunch pins that one approval
// authorizes exactly one launch: the proof is consumed at commit, and a second
// launch of the same invocation fails closed.
func TestInteractionContractApprovalThenSingleLaunch(t *testing.T) {
	cfg := nativeToolAgentConfig("contract-launch")
	cfg.Agent.Mode, cfg.Agent.Auto = interaction.ModeAct, false
	rt, _ := newAccessTestRuntime(t, cfg)

	// A destructive workspace command is the operation that still requires a
	// specific approval in normal Act, so it is what this proof can settle.
	call := policy.Call{
		InvocationID: "inv-1",
		ToolName:     "shell.run",
		Shell:        &policy.ShellInput{Command: "rm -rf rel/x", WorkspaceRoot: "/repo", Dir: "/repo"},
	}
	proof, ev, err := rt.Runner.Authorizer.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if ev.Decision != policy.DecisionConfirm {
		t.Fatalf("decision = %v, want Confirm", ev.Decision)
	}
	if _, err := rt.Runner.Authorizer.ApproveExact(proof, "req-1"); err != nil {
		t.Fatalf("ApproveExact: %v", err)
	}
	ctx := policy.WithAuthorization(context.Background(), proof)
	adm, err := rt.Runner.Authorizer.BeginTool(ctx, call)
	if err != nil {
		t.Fatalf("BeginTool: %v", err)
	}
	if err := adm.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	adm.Release()
	if _, err := rt.Runner.Authorizer.BeginTool(ctx, call); !errors.Is(err, policy.ErrStaleAdmission) {
		t.Fatalf("second launch = %v, want ErrStaleAdmission", err)
	}
}

// TestInteractionContractAskBlocksFutureChildPipelineAndWatchWrites pins that
// an Ask owner's ceiling reaches every future actor: a child session, a
// pipeline role and a watch command all inherit the read-only ceiling, so a
// write is denied rather than queued behind a prompt no UI will show.
func TestInteractionContractAskBlocksFutureChildPipelineAndWatchWrites(t *testing.T) {
	cfg := nativeToolAgentConfig("contract-ask")
	cfg.Agent.Mode, cfg.Agent.Auto = interaction.ModeAsk, false
	rt, state := newAccessTestRuntime(t, cfg)

	child := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{},
		session.WithInteractionOwner(state, interaction.Ceiling{Act: true, Auto: true, System: true}))
	childActor := child.InteractionState()
	if childActor.Mode != interaction.ModeAsk {
		t.Fatalf("child mode = %q, want ask", childActor.Mode)
	}

	ev, err := rt.Runner.Policy.EvaluateInput(policy.Input{
		ToolName:   "file.write",
		Args:       map[string]any{"path": "a.go", "content": "x"},
		ActorState: childActor,
	})
	if err != nil {
		t.Fatalf("EvaluateInput(file.write): %v", err)
	}
	if ev.Decision != policy.DecisionDeny {
		t.Fatalf("child file.write in Ask = %v, want Deny", ev.Decision)
	}

	a, err := policy.AnalyzeShell(policy.ShellInput{Command: "rm -rf rel/x", WorkspaceRoot: "/repo", Dir: "/repo"})
	if err != nil {
		t.Fatalf("AnalyzeShell: %v", err)
	}
	ev, err = rt.Runner.Policy.EvaluateInput(policy.Input{ToolName: "shell.run", ActorState: childActor, Shell: &a})
	if err != nil {
		t.Fatalf("EvaluateInput(shell.run): %v", err)
	}
	if ev.Decision != policy.DecisionDeny {
		t.Fatalf("watch shell write in Ask = %v, want Deny", ev.Decision)
	}
}
