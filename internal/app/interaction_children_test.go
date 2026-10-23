package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// interactionTestOwner builds a root conversation actor. A fresh session starts
// at the configured Act default with no Auto and no System.
func interactionTestOwner(t *testing.T) (*session.State, config.Config) {
	t.Helper()
	cfg := config.Default()
	return session.New(cfg, t.TempDir(), time.Unix(1, 0), session.Persistence{}), cfg
}

// interactionTestChild builds a distinct child actor bound to owner with the
// given immutable ceiling. It deliberately does not share the owner's access
// state, so every assertion reads the live owner through the authority.
func interactionTestChild(t *testing.T, cfg config.Config, owner *session.State, ceiling interaction.Ceiling) *session.State {
	t.Helper()
	return session.New(cfg, owner.WorkingDir, time.Unix(2, 0), session.Persistence{}, session.WithInteractionOwner(owner, ceiling))
}

// fileWriteAuthorizer wires a real policy authorizer over a registry that
// classifies file.write as a workspace write. The risk classification is what
// makes the Ask ceiling deny it.
func fileWriteAuthorizer(t *testing.T, cfg config.Config, authority interaction.Authority) *policy.Authorizer {
	t.Helper()
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "file.write",
		Risk: registry.RiskWorkspaceWrite,
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, nil
		},
	}); err != nil {
		t.Fatalf("register file.write: %v", err)
	}
	pol := policy.NewEngine(&cfg, nil)
	pol.WithRegistry(reg)
	return policy.NewAuthorizer(pol, authority)
}

func fileWriteCall() policy.Call {
	return policy.Call{ToolName: "file.write", Args: map[string]any{"path": "artifact.txt"}}
}

func TestChildCannotWriteAfterOwnerSwitchesAsk(t *testing.T) {
	owner, cfg := interactionTestOwner(t)
	child := interactionTestChild(t, cfg, owner, interaction.Ceiling{Act: true, Auto: true, System: true})
	az := fileWriteAuthorizer(t, cfg, child.InteractionAuthority())

	if _, ev, err := az.Evaluate(context.Background(), fileWriteCall()); err != nil {
		t.Fatalf("Act evaluate: %v", err)
	} else if ev.Decision == policy.DecisionDeny {
		t.Fatalf("Act child write denied before the owner switched: %+v", ev)
	}

	ask := interaction.ModeAsk
	if _, err := owner.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("owner Ask: %v", err)
	}
	_, ev, err := az.Evaluate(context.Background(), fileWriteCall())
	if err != nil {
		t.Fatalf("Ask evaluate: %v", err)
	}
	if ev.Decision != policy.DecisionDeny || !ev.Required.Act {
		t.Fatalf("child write after owner Ask = %+v, want Deny requiring Act", ev)
	}
}

func TestRoleAndPlanAuthorCannotForceAuto(t *testing.T) {
	owner, cfg := interactionTestOwner(t)
	on := true
	if _, err := owner.ApplyInteraction(interaction.Patch{Auto: &on}); err != nil {
		t.Fatalf("owner Auto: %v", err)
	}

	reader := interactionTestChild(t, cfg, owner, pipelineRoleCeiling(swarm.ScopeReadOnly, nil))
	planAuthor := interactionTestChild(t, cfg, owner, interaction.Ceiling{Act: true})
	full := interactionTestChild(t, cfg, owner, pipelineRoleCeiling(swarm.ScopeFull, nil))

	if snap := reader.InteractionAuthority().Snapshot(); snap.Access.Mode != interaction.ModeAsk || snap.Access.Auto {
		t.Fatalf("read-only role access = %+v, want Ask with no Auto", snap.Access)
	}
	if snap := planAuthor.InteractionAuthority().Snapshot(); snap.Access.Mode != interaction.ModeAct || snap.Access.Auto {
		t.Fatalf("plan author access = %+v, want Act with no Auto", snap.Access)
	}
	if snap := full.InteractionAuthority().Snapshot(); snap.Access.Mode != interaction.ModeAct || !snap.Access.Auto {
		t.Fatalf("pipeline implementer access = %+v, want Act+Auto", snap.Access)
	}

	// A plan-author child cannot widen itself: its own access mutation is not
	// the owner's tuple, and its ceiling still forbids Auto.
	if _, err := planAuthor.ApplyInteraction(interaction.Patch{Auto: &on}); err != nil {
		t.Fatalf("plan author self Auto: %v", err)
	}
	if snap := planAuthor.InteractionAuthority().Snapshot(); snap.Access.Auto {
		t.Fatalf("plan author forced Auto: %+v", snap.Access)
	}

	off := false
	customNoAuto := interactionTestChild(t, cfg, owner, pipelineRoleCeiling(swarm.ScopeFull, &routing.CustomAgent{Auto: &off}))
	if snap := customNoAuto.InteractionAuthority().Snapshot(); snap.Access.Auto {
		t.Fatalf("custom agent Auto=false did not narrow the ceiling: %+v", snap.Access)
	}

	ask := interaction.ModeAsk
	customAsk := interactionTestChild(t, cfg, owner, pipelineRoleCeiling(swarm.ScopeFull, &routing.CustomAgent{Mode: &ask}))
	if snap := customAsk.InteractionAuthority().Snapshot(); snap.Access.Mode != interaction.ModeAsk {
		t.Fatalf("custom agent Mode=ask did not remove Act: %+v", snap.Access)
	}
}

func TestChildSystemRequiresExplicitRequestAndLiveOwnerGrant(t *testing.T) {
	owner, cfg := interactionTestOwner(t)
	owner.SetSystemAccess(true)

	child := interactionTestChild(t, cfg, owner, interaction.Ceiling{Act: true, System: true})
	if snap := child.InteractionAuthority().Snapshot(); snap.Access.System {
		t.Fatalf("child inherited System without an explicit request: %+v", snap.Access)
	}

	child.SetSystemAccess(true)
	if snap := child.InteractionAuthority().Snapshot(); !snap.Access.System {
		t.Fatalf("explicit child System request with a live owner grant = %+v, want System", snap.Access)
	}

	owner.SetSystemAccess(false)
	if snap := child.InteractionAuthority().Snapshot(); snap.Access.System {
		t.Fatalf("child kept System after the owner withdrew it: %+v", snap.Access)
	}

	// A ceiling without System rejects the same explicit request even when the
	// owner holds the grant.
	owner.SetSystemAccess(true)
	noSystem := interactionTestChild(t, cfg, owner, interaction.Ceiling{Act: true})
	noSystem.SetSystemAccess(true)
	if snap := noSystem.InteractionAuthority().Snapshot(); snap.Access.System {
		t.Fatalf("ceiling without System admitted a child request: %+v", snap.Access)
	}
}

func TestPipelineAutoOnlyCoversDispatchWorktree(t *testing.T) {
	owner, cfg := interactionTestOwner(t)
	on := true
	if _, err := owner.ApplyInteraction(interaction.Patch{Auto: &on}); err != nil {
		t.Fatalf("owner Auto: %v", err)
	}

	child := interactionTestChild(t, cfg, owner, pipelineRoleCeiling(swarm.ScopeFull, nil))
	child.SetWorkspace(session.Workspace{ProjectRoot: "/repo", ActiveRoot: "/repo/.worktrees/task"})

	snap := child.InteractionAuthority().Snapshot()
	if snap.WorkspaceRoot != "/repo/.worktrees/task" {
		t.Fatalf("pipeline actor workspace = %q, want the dispatch worktree", snap.WorkspaceRoot)
	}
	if snap.Access.Mode != interaction.ModeAct || !snap.Access.Auto {
		t.Fatalf("pipeline actor access = %+v, want Act+Auto for its dispatch", snap.Access)
	}

	ownerSnap := owner.InteractionAuthority().Snapshot()
	if ownerSnap.WorkspaceRoot == snap.WorkspaceRoot {
		t.Fatalf("pipeline actor retargeted the owner workspace to %q", ownerSnap.WorkspaceRoot)
	}
	if !ownerSnap.Access.Auto {
		t.Fatalf("pipeline actor changed the owner tuple: %+v", ownerSnap.Access)
	}
}

func TestOldChildPolicyCannotAdmitAfterReloadedDeny(t *testing.T) {
	owner, cfg := interactionTestOwner(t)
	child := interactionTestChild(t, cfg, owner, interaction.Ceiling{Act: true, Auto: true, System: true})
	az := fileWriteAuthorizer(t, cfg, child.InteractionAuthority())
	call := fileWriteCall()

	proof, ev, err := az.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("Act evaluate: %v", err)
	}
	if ev.Decision == policy.DecisionDeny {
		t.Fatalf("Act evaluation denied before the owner change: %+v", ev)
	}
	ctx := policy.WithAuthorization(context.Background(), proof)

	ask := interaction.ModeAsk
	if _, err := owner.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("owner Ask: %v", err)
	}
	if _, err := az.BeginTool(ctx, call); !errors.Is(err, policy.ErrApprovalRequired) {
		t.Fatalf("old child admission after owner Ask = %v, want ErrApprovalRequired", err)
	}
}

func TestRejectedReloadDoesNotReplacePolicySource(t *testing.T) {
	rt := startEndRuntime(t)
	before := rt.State.InteractionState()

	bad := rt.Config
	bad.Agent.Mode = interaction.Mode("bogus")
	if err := reloadAgentRuntime(context.Background(), bad, rt); err == nil {
		t.Fatal("invalid InteractionDefaults reload succeeded")
	}
	if got := rt.State.InteractionState(); got != before {
		t.Fatalf("rejected reload changed the owner tuple: %+v -> %+v", before, got)
	}

	// The installed engine still reads the owner snapshot, not the rejected
	// candidate: an Act write is not denied by a config that never installed.
	az := policy.NewAuthorizer(rt.Runner.Policy, rt.State.InteractionAuthority())
	if _, ev, err := az.Evaluate(context.Background(), fileWriteCall()); err != nil {
		t.Fatalf("post-rejection evaluate: %v", err)
	} else if ev.Decision == policy.DecisionDeny {
		t.Fatalf("rejected reload replaced the policy source: %+v", ev)
	}
}
