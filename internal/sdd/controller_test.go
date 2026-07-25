package sdd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

func TestControllerStateConstants(t *testing.T) {
	cases := []struct {
		name string
		want ControllerState
	}{
		{"idle", StateIdle},
		{"workspace_reset", StateWorkspaceReset},
		{"spec_gate", StateSpecGate},
		{"decompose", StateDecompose},
		{"drain_iteration", StateDrainIteration},
		{"dispatch_workers", StateDispatchWorkers},
		{"verify_audit", StateVerifyAudit},
		{"review_merge", StateReviewMerge},
		{"branch_review", StateBranchReview},
		{"final_merge_gate", StateFinalMergeGate},
		{"finalize", StateFinalize},
		{"blocked", StateBlocked},
	}
	for _, c := range cases {
		if string(c.want) != c.name {
			t.Fatalf("%s = %q, want %q", c.name, c.want, c.name)
		}
	}
}

func TestControllerDecomposeFromPlan(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	// Write a plan file.
	planPath := filepath.Join(dir, "plan.md")
	writeFile(planPath, "# Plan\n\n### Task 1: Foundation\n\ncontent\n\n### Task 2: Git ops\n\ncontent\n")
	git := NewFakeGitOps()
	git.SetRef("main", "main123")
	git.SetBranch("sdd/feature")
	git.SetRef("sdd/feature", "main123")
	dag := &DAG{}
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "main123"}
	var p Progress
	ss := session.New(config.Default(), dir, time.Now(), session.Persistence{})
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		return &agent.Runner{}, nil
	}
	c := NewController(ws, git, dag, rs, &p, factory, routing.Config{}, ss, "sdd/feature", "main")
	c.PlanPath = planPath
	// Skip workspace reset (already done) and go straight to decompose.
	c.State = StateDecompose
	err := c.Run(context.Background())
	// Should reach the spec gate (draft spec emitted, needs approval).
	if err != ErrHumanGateRequired {
		t.Fatalf("expected ErrHumanGateRequired, got %v", err)
	}
	if c.State != StateSpecGate {
		t.Errorf("State = %q, want spec_gate", c.State)
	}
	// A draft spec.md should exist.
	specPath := filepath.Join(ws.Dir(), "spec.md")
	if _, err := os.Stat(specPath); err != nil {
		t.Fatalf("draft spec.md not created: %v", err)
	}
}

func TestControllerRunReachesSpecGate(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	git := NewFakeGitOps()
	git.SetRef("main", "main123")
	git.SetBranch("sdd/feature")
	git.SetRef("sdd/feature", "main123")
	dag := &DAG{}
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "main123"}
	var p Progress
	ss := session.New(config.Default(), dir, time.Now(), session.Persistence{})

	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		r := &agent.Runner{}
		r.RunTaskFunc = func(ctx context.Context, goal string) (*agent.Task, error) {
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: DONE\nbatch_id: 0\ndispatched: []\nmerged: []\nblocked_tasks: []\nhealth_alerts: []\nedit_guard: clean\nnext_action: drain\n"}, nil
		}
		return r, nil
	}

	c := NewController(ws, git, dag, rs, &p, factory, routing.Config{}, ss, "sdd/feature", "main")
	// The controller should reach the spec gate (no spec.md exists yet) and
	// surface a human gate. Since the human-gate blocking is stubbed to return
	// ErrHumanGateRequired, Run should return that error.
	err := c.Run(context.Background())
	if err == nil {
		t.Fatal("expected ErrHumanGateRequired at spec gate (no spec.md)")
	}
	if c.State != StateSpecGate {
		t.Errorf("State = %q, want spec_gate", c.State)
	}
}

func TestControllerBranchReviewPass(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	git := NewFakeGitOps()
	git.SetRef("sdd/feature", "pipe123")
	git.SetRef("main", "main123")
	git.SetAncestor("main123", "pipe123")
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Status: TaskMerged}}}
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "pipe123", Merged: []string{"T1"}}
	var p Progress
	ss := session.New(config.Default(), dir, time.Now(), session.Persistence{})
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		r := &agent.Runner{}
		r.RunTaskFunc = func(ctx context.Context, goal string) (*agent.Task, error) {
			// Write a passing branch review report.
			writeFile(filepath.Join(ws.Dir(), "reports", "branch.md"), "status: PASS\n\nbranch looks good\n")
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "branch review done"}, nil
		}
		return r, nil
	}
	c := NewController(ws, git, dag, rs, &p, factory, routing.Config{}, ss, "sdd/feature", "main")
	c.State = StateBranchReview
	err := c.Run(context.Background())
	if err != ErrHumanGateRequired {
		t.Fatalf("expected ErrHumanGateRequired at final merge gate, got %v", err)
	}
	if c.State != StateFinalMergeGate {
		t.Errorf("State = %q, want final_merge_gate", c.State)
	}
}

func TestControllerDrainIterationWiresBlockedTasks(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	git := NewFakeGitOps()
	git.SetRef("main", "main123")
	git.SetBranch("sdd/feature")
	git.SetRef("sdd/feature", "main123")
	git.SetBranch("sdd/T1")
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Title: "x", Status: TaskInProgress, Branch: "sdd/T1"}}}
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "main123", Merged: []string{}}
	var p Progress
	ss := session.New(config.Default(), dir, time.Now(), session.Persistence{})

	callCount := 0
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		r := &agent.Runner{}
		r.RunTaskFunc = func(ctx context.Context, goal string) (*agent.Task, error) {
			callCount++
			if callCount == 1 {
				// First call: orchestrator returns BLOCKED with blocked_tasks.
				return &agent.Task{
					Status:  agent.TaskStatusCompleted,
					Summary: "status: BLOCKED\nbatch_id: 1\ndispatched: []\nmerged: []\nblocked_tasks:\n  - task: T1\n    reason: STALE_HEAD\n    detail: sdd/T1 is behind pipeline\n    files_in_conflict: []\nhealth_alerts: []\nedit_guard: clean\nnext_action: surface_blockers\n",
				}, nil
			}
			if callCount == 2 {
				// Second call: orchestrator returns DONE (after deterministic fix).
				return &agent.Task{
					Status:  agent.TaskStatusCompleted,
					Summary: "status: DONE\nbatch_id: 2\ndispatched: []\nmerged: []\nblocked_tasks: []\nhealth_alerts: []\nedit_guard: clean\nnext_action: branch_review\n",
				}, nil
			}
			// Third call: branch reviewer — write a PASS report.
			writeFile(filepath.Join(ws.Dir(), "reports", "branch.md"), "status: PASS\n\nbranch looks good\n")
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "branch review done"}, nil
		}
		return r, nil
	}

	c := NewController(ws, git, dag, rs, &p, factory, routing.Config{}, ss, "sdd/feature", "main")
	c.State = StateDrainIteration
	err := c.Run(context.Background())
	// The controller should have:
	//   1. Received BLOCKED from orchestrator → wired BlockedTasks → entered StateBlocked
	//   2. Applied deterministic fix (STALE_HEAD → Repair) → re-entered drain
	//   3. Received DONE → entered branch review → PASS → reached final merge gate
	// If BlockedTasks was NOT wired, the controller would have taken the
	// "no blocked tasks" branch and returned ErrHumanGateRequired with
	// "blocked with no tasks" — not reached final merge gate.
	if err != ErrHumanGateRequired {
		t.Fatalf("expected ErrHumanGateRequired at final merge gate, got %v", err)
	}
	if c.State != StateFinalMergeGate {
		t.Errorf("State = %q, want final_merge_gate", c.State)
	}
}

func TestControllerRecordUsageUpdatesSession(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	ss := session.New(config.Default(), dir, time.Now(), session.Persistence{})
	var observed int
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		r := &agent.Runner{}
		r.UsageObserver = func(usage schema.TokenUsage) { observed += usage.TotalTokens }
		r.RunTaskFunc = func(ctx context.Context, goal string) (*agent.Task, error) {
			// Simulate real runner behavior: fire UsageObserver.
			if r.UsageObserver != nil {
				r.UsageObserver(schema.TokenUsage{TotalTokens: 42})
			}
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: DONE\nbatch_id: 0\ndispatched: []\nmerged: []\nblocked_tasks: []\nhealth_alerts: []\nedit_guard: clean\nnext_action: branch_review\n"}, nil
		}
		return r, nil
	}
	c := NewController(ws, NewFakeGitOps(), &DAG{}, &RepoState{}, &Progress{}, factory, routing.Config{}, ss, "sdd/feature", "main")
	// Seed an approved spec so Run reaches StateDrainIteration.
	os.WriteFile(filepath.Join(ws.Dir(), "spec.md"), []byte("---\nstatus: approved\n---\n```yaml\ntasks:\n  - id: T1\n    title: X\n    deps: []\n    files: []\n    acceptance: []\n```\n"), 0644)
	// Start at Decompose to skip workspace reset (which would archive spec.md).
	c.State = StateDecompose
	_ = c.Run(context.Background())
	if observed == 0 {
		t.Error("UsageObserver never fired; recordUsage did not wire the observer")
	}
}

func TestControllerSwapOrchestratorModelRebuildsFactory(t *testing.T) {
	var lastPreset string
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		return &agent.Runner{RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: DONE\nbatch_id: 0\ndispatched: []\nmerged: []\nblocked_tasks: []\nhealth_alerts: []\nedit_guard: clean\nnext_action: branch_review\n"}, nil
		}}, nil
	}
	cfg := routing.Config{DefaultProfile: "default", Profiles: map[string]routing.AgentProfile{"default": {Roles: map[routing.AgentRole]routing.RoleBinding{routing.RoleSDDOrchestrator: {Preset: "fast"}}}}}
	rebuildCalled := false
	ws2, _ := NewWorkspace(t.TempDir())
	ws2.Ensure()
	c := NewController(ws2, NewFakeGitOps(), &DAG{}, &RepoState{}, &Progress{}, factory, cfg, nil, "sdd/feature", "main")
	c.RebuildFactory = func(newCfg routing.Config) swarm.RunnerFactory {
		rebuildCalled = true
		lastPreset = newCfg.Profiles[newCfg.DefaultProfile].Roles[routing.RoleSDDOrchestrator].Preset
		return factory
	}
	c.swapOrchestratorModel("strong")
	if !rebuildCalled {
		t.Error("RebuildFactory not called")
	}
	if lastPreset != "strong" {
		t.Errorf("rebuilt preset = %q, want strong", lastPreset)
	}
}

func TestControllerBlockedDeterministicFix(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	git := NewFakeGitOps()
	git.SetRef("sdd/feature", "pipe123")
	git.SetRef("main", "main123")
	git.SetAncestor("main123", "pipe123")
	git.SetBranch("sdd/T1")
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Title: "x", Status: TaskInProgress, Branch: "sdd/T1"}}}
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "pipe123", Merged: []string{}}
	var p Progress
	ss := session.New(config.Default(), dir, time.Now(), session.Persistence{})
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		r := &agent.Runner{}
		r.RunTaskFunc = func(ctx context.Context, goal string) (*agent.Task, error) {
			// Write a passing branch review report so the controller can read it.
			writeFile(filepath.Join(ws.Dir(), "reports", "branch.md"), "status: PASS\n\nbranch looks good\n")
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: DONE\nbatch_id: 1\ndispatched: []\nmerged: [T1]\nblocked_tasks: []\nhealth_alerts: []\nedit_guard: clean\nnext_action: drain\n"}, nil
		}
		return r, nil
	}
	c := NewController(ws, git, dag, rs, &p, factory, routing.Config{}, ss, "sdd/feature", "main")
	c.State = StateBlocked
	c.BlockedTasks = []BlockedTask{{Task: "T1", Reason: "STALE_HEAD"}}
	err := c.Run(context.Background())
	// After the deterministic fix (State.Repair), the controller re-dispatches.
	// The fake orchestrator returns DONE with T1 merged, so the controller
	// should reach branch review (no ready tasks) or drain iteration.
	if err != nil {
		// It may reach a human gate (final merge) — that's OK.
		if err != ErrHumanGateRequired {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}
