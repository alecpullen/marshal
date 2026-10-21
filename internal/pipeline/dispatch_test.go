package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)

func TestDispatcherImplementParsesReport(t *testing.T) {
	d := Dispatcher{
		Factory: func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
			return nil, errors.New("unused")
		},
		exec: func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
			if role != routing.RoleSDDImplementer {
				t.Errorf("role = %q, want the implementer role", role)
			}
			if scope != swarm.ScopeFull {
				t.Errorf("scope = %v, want ScopeFull", scope)
			}
			return "STATUS: DONE\nTESTS: go test ./... — pass\n", nil
		},
	}
	rep, err := d.Implement(context.Background(), routing.RoleSDDImplementer, "", "prompt")
	if err != nil {
		t.Fatalf("Implement: %v", err)
	}
	if rep.Status != StatusDone {
		t.Errorf("Status = %q", rep.Status)
	}
}

func TestDispatcherReviewRunsArtifactWriter(t *testing.T) {
	d := Dispatcher{
		exec: func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
			if scope != swarm.ScopeArtifactWriter {
				t.Errorf("scope = %v, want ScopeArtifactWriter", scope)
			}
			return "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n", nil
		},
	}
	rev, err := d.Review(context.Background(), routing.RoleSDDReviewer, "", "prompt")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !rev.Clean() {
		t.Errorf("review = %+v, want clean", rev)
	}
}

func TestDispatcherPropagatesUnparseableOutput(t *testing.T) {
	d := Dispatcher{
		exec: func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
			return "I did the thing, boss.", nil
		},
	}
	if _, err := d.Implement(context.Background(), routing.RoleSDDImplementer, "", "p"); err == nil {
		t.Fatal("unparseable implementer output: want error, got nil")
	}
}

// newPipelineTestSession builds a bare session for dispatcher card tests.
func newPipelineTestSession(t *testing.T, depth int) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}, session.WithDepth(depth))
}

// TestRunExecRegistersCardForChildSessionRunner covers the SDD path: a role
// runner streaming into its own child session must leave exactly one
// finished summary card on the parent, registered before the task runs.
func TestRunExecRegistersCardForChildSessionRunner(t *testing.T) {
	parent := newPipelineTestSession(t, 0)
	child := newPipelineTestSession(t, 1)
	d := Dispatcher{
		State: parent,
		Factory: func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
			return &agent.Runner{
				State: child,
				RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
					subs := parent.Subagents()
					if len(subs) != 1 {
						t.Errorf("during run: subagents = %d, want 1 running card", len(subs))
					} else if subs[0].Status != session.SubagentRunning {
						t.Errorf("during run: status = %v, want running", subs[0].Status)
					}
					return &agent.Task{Summary: "STATUS: DONE\nTESTS: go test ./... — pass\n"}, nil
				},
			}, nil
		},
	}
	if _, err := d.Implement(context.Background(), routing.RoleSDDImplementer, "task label", "do the task"); err != nil {
		t.Fatalf("Implement: %v", err)
	}
	subs := parent.Subagents()
	if len(subs) != 1 {
		t.Fatalf("subagents = %d, want 1", len(subs))
	}
	v := subs[0]
	if v.Status != session.SubagentDone {
		t.Errorf("status = %v, want done", v.Status)
	}
	if v.Child != child {
		t.Errorf("card child = %p, want the runner's child session %p", v.Child, child)
	}
	if v.Summary == "" {
		t.Error("summary empty, want the task summary on the finished card")
	}
	if !strings.Contains(v.Label, "task label") {
		t.Errorf("label = %q, want it to name the dispatched prompt", v.Label)
	}
	if strings.Contains(v.Label, "do the task") {
		t.Errorf("label = %q, should not contain the full prompt", v.Label)
	}
}

// TestRunExecSkipsCardForSharedStateRunner covers the swarm-style runner:
// it logs to the parent transcript directly, so no card may be registered.
func TestRunExecSkipsCardForSharedStateRunner(t *testing.T) {
	parent := newPipelineTestSession(t, 0)
	d := Dispatcher{
		State: parent,
		Factory: func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
			return &agent.Runner{
				State: parent,
				RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
					return &agent.Task{Summary: "STATUS: DONE\nTESTS: pass\n"}, nil
				},
			}, nil
		},
	}
	if _, err := d.Implement(context.Background(), routing.RoleSDDImplementer, "", "p"); err != nil {
		t.Fatalf("Implement: %v", err)
	}
	if n := len(parent.Subagents()); n != 0 {
		t.Errorf("subagents = %d, want 0 for a shared-state runner", n)
	}
}

// TestRunExecMarksCardFailedOnError ensures a failed run flips the card to
// the failed state instead of leaving it spinning.
func TestRunExecMarksCardFailedOnError(t *testing.T) {
	parent := newPipelineTestSession(t, 0)
	child := newPipelineTestSession(t, 1)
	d := Dispatcher{
		State: parent,
		Factory: func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
			return &agent.Runner{
				State: child,
				RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
					return nil, errors.New("provider exploded")
				},
			}, nil
		},
	}
	if _, err := d.Implement(context.Background(), routing.RoleSDDImplementer, "", "p"); err == nil {
		t.Fatal("Implement: want error, got nil")
	}
	subs := parent.Subagents()
	if len(subs) != 1 {
		t.Fatalf("subagents = %d, want 1", len(subs))
	}
	if subs[0].Status != session.SubagentFailed {
		t.Errorf("status = %v, want failed", subs[0].Status)
	}
}

// TestRunExecWithoutParentState registers nothing and must not panic when
// the dispatcher has no parent session (State left nil).
func TestRunExecWithoutParentState(t *testing.T) {
	child := newPipelineTestSession(t, 1)
	d := Dispatcher{
		Factory: func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
			return &agent.Runner{
				State: child,
				RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
					return &agent.Task{Summary: "STATUS: DONE\nTESTS: pass\n"}, nil
				},
			}, nil
		},
	}
	if _, err := d.Implement(context.Background(), routing.RoleSDDImplementer, "", "p"); err != nil {
		t.Fatalf("Implement: %v", err)
	}
}

// TestDispatchActorUsesExecutionWorktree pins the actor workspace to the
// dispatch's worktree before the registry factory reads the actor's
// authority, while leaving the actor's project root (and the parent's
// workspace) alone.
func TestDispatchActorUsesExecutionWorktree(t *testing.T) {
	parent := newPipelineTestSession(t, 0)
	child := newPipelineTestSession(t, 1)
	projectRoot := child.Workspace().ProjectRoot
	worktree := filepath.Join(t.TempDir(), "worktree")
	var registryRoot string
	d := Dispatcher{
		State: parent,
		ExecCtx: ExecutionContext{
			RepoRoot:      parent.WorkingDir,
			WorkspaceRoot: worktree,
		},
		Factory: func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
			return &agent.Runner{
				State: child,
				RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
					return &agent.Task{Summary: "STATUS: DONE\nTESTS: pass\n"}, nil
				},
			}, nil
		},
		RegistryFactory: func(ctx ExecutionContext, scope RegistryScope, state *session.State) (*registry.Registry, error) {
			registryRoot = state.InteractionAuthority().Snapshot().WorkspaceRoot
			return registry.New(), nil
		},
	}
	if _, err := d.Implement(context.Background(), routing.RoleSDDImplementer, "", "p"); err != nil {
		t.Fatalf("Implement: %v", err)
	}
	if registryRoot != worktree {
		t.Errorf("registry factory saw workspace root %q, want the dispatch worktree %q", registryRoot, worktree)
	}
	if got := child.InteractionAuthority().Snapshot().WorkspaceRoot; got != worktree {
		t.Errorf("actor workspace root = %q, want %q", got, worktree)
	}
	if got := child.Workspace().ActiveRoot; got != worktree {
		t.Errorf("actor active root = %q, want %q", got, worktree)
	}
	if got := child.Workspace().ProjectRoot; got != projectRoot {
		t.Errorf("actor project root = %q, want it unchanged at %q", got, projectRoot)
	}
	if got := parent.Workspace().ActiveRoot; got != parent.WorkingDir {
		t.Errorf("parent active root = %q, want it unchanged at %q", got, parent.WorkingDir)
	}
}

// TestDispatchActorDoesNotRetargetParentWorkspace covers the shared-session
// (swarm-style) runner: it has no actor workspace of its own, so a dispatch
// must never move the owner's active root onto the pipeline worktree.
func TestDispatchActorDoesNotRetargetParentWorkspace(t *testing.T) {
	parent := newPipelineTestSession(t, 0)
	worktree := filepath.Join(t.TempDir(), "worktree")
	d := Dispatcher{
		State: parent,
		ExecCtx: ExecutionContext{
			RepoRoot:      parent.WorkingDir,
			WorkspaceRoot: worktree,
		},
		Factory: func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
			return &agent.Runner{
				State: parent,
				RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
					return &agent.Task{Summary: "STATUS: DONE\nTESTS: pass\n"}, nil
				},
			}, nil
		},
	}
	if _, err := d.Implement(context.Background(), routing.RoleSDDImplementer, "", "p"); err != nil {
		t.Fatalf("Implement: %v", err)
	}
	if got := parent.Workspace().ActiveRoot; got != parent.WorkingDir {
		t.Errorf("parent active root = %q, want it unchanged at %q", got, parent.WorkingDir)
	}
	if got := parent.InteractionAuthority().Snapshot().WorkspaceRoot; got != parent.WorkingDir {
		t.Errorf("parent authority workspace root = %q, want %q", got, parent.WorkingDir)
	}
	if n := len(parent.Subagents()); n != 0 {
		t.Errorf("subagents = %d, want 0 for a shared-state runner", n)
	}
}

// TestDispatchReadOnlyRoleRemainsNarrowerThanAct proves the registry scope,
// not the actor's interaction tuple, is what enforces a read-only role: an
// actor in Act still receives a registry without write tools.
func TestDispatchReadOnlyRoleRemainsNarrowerThanAct(t *testing.T) {
	parent := newPipelineTestSession(t, 0)
	child := newPipelineTestSession(t, 1)
	mode := interaction.ModeAct
	if _, err := child.ApplyInteraction(interaction.Patch{Mode: &mode}); err != nil {
		t.Fatalf("ApplyInteraction: %v", err)
	}
	worktree := filepath.Join(t.TempDir(), "worktree")
	handler := func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, nil
	}
	schema := json.RawMessage(`{"type":"object"}`)
	var gotScope RegistryScope
	d := Dispatcher{
		State: parent,
		ExecCtx: ExecutionContext{
			RepoRoot:      parent.WorkingDir,
			WorkspaceRoot: worktree,
		},
		Factory: func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
			return &agent.Runner{
				State: child,
				RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
					return &agent.Task{Summary: "STATUS: DONE\nTESTS: pass\n"}, nil
				},
			}, nil
		},
		RegistryFactory: func(ctx ExecutionContext, scope RegistryScope, state *session.State) (*registry.Registry, error) {
			gotScope = scope
			src := registry.New()
			if err := src.Register(registry.Tool{Name: "repo.read", Risk: registry.RiskReadOnly, Schema: schema, Handler: handler}); err != nil {
				return nil, err
			}
			if err := src.Register(registry.Tool{Name: "file.write", Risk: registry.RiskWorkspaceWrite, Schema: schema, Handler: handler}); err != nil {
				return nil, err
			}
			if scope == ScopeReadOnly {
				return registry.ReadOnlyView(src), nil
			}
			return src, nil
		},
	}
	if _, err := d.run(context.Background(), routing.RoleSDDReviewer, swarm.ScopeReadOnly, "", "p"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotScope != ScopeReadOnly {
		t.Errorf("registry scope = %v, want ScopeReadOnly", gotScope)
	}
	if snap := child.InteractionAuthority().Snapshot(); snap.Access.Mode != interaction.ModeAct {
		t.Fatalf("actor access = %+v, want Act so the test proves scope, not mode, narrows the role", snap.Access)
	}
	if got := child.InteractionAuthority().Snapshot().WorkspaceRoot; got != worktree {
		t.Errorf("actor workspace root = %q, want %q", got, worktree)
	}
}
