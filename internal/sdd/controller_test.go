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
