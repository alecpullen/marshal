package sdd_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	sdd "marshal/internal/sdd"
)

func TestIntegrationFullPipeline(t *testing.T) {
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws.Ensure()
	git := sdd.NewFakeGitOps()
	// Seed the pipeline branch + 3 task branches.
	git.SetRef("sdd/feature", "base123")
	for _, id := range []string{"T1", "T2", "T3"} {
		git.SetRef("sdd/"+id, "base123")
		git.SetBranch("sdd/" + id)
	}
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{
		{ID: "T1", Title: "Foundation", Status: sdd.TaskPending},
		{ID: "T2", Title: "Build", Status: sdd.TaskPending, Deps: []string{"T1"}},
		{ID: "T3", Title: "Verify", Status: sdd.TaskPending, Deps: []string{"T2"}},
	}}
	rs := &sdd.RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "base123", Merged: []string{}}
	progress := &sdd.Progress{}
	ss := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})

	// Fake factory: orchestrator returns DONE + merged T1/T2/T3 across
	// iterations; branch reviewer returns PASS.
	// The fake orchestrator also updates the DAG and RepoState to mark tasks
	// as merged, matching what the real orchestrator does via the merge tool.
	var callCount int
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		return &agent.Runner{RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
			callCount++
			switch role {
			case routing.RoleSDDOrchestrator:
				// Mark all unmerged tasks as merged in the DAG and RepoState,
				// simulating what the real orchestrator does via the merge tool.
				for i := range dag.Tasks {
					if dag.Tasks[i].Status != sdd.TaskMerged {
						dag.Tasks[i].Status = sdd.TaskMerged
						rs.MarkMerged(dag.Tasks[i].ID)
					}
				}
				merged := rs.Merged
				return &agent.Task{Status: agent.TaskStatusCompleted, Summary: fmt.Sprintf("status: DONE\nbatch_id: %d\ndispatched: []\nmerged: [%s]\nblocked_tasks: []\nhealth_alerts: []\nedit_guard: clean\nnext_action: branch_review\n", callCount, strings.Join(merged, ","))}, nil
			case routing.RoleSDDBranchReviewer:
				os.MkdirAll(filepath.Join(ws.Dir(), "reports"), 0755)
				os.WriteFile(filepath.Join(ws.Dir(), "reports", "branch.md"), []byte("status: PASS\nverdict: approve\n"), 0644)
				return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: PASS"}, nil
			}
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: DONE"}, nil
		}}, nil
	}

	// Seed an approved spec so the controller skips the spec gate.
	os.WriteFile(filepath.Join(ws.Dir(), "spec.md"), []byte("---\nstatus: approved\n---\n```yaml\ntasks:\n  - id: T1\n    title: Foundation\n    deps: []\n  - id: T2\n    title: Build\n    deps: [T1]\n  - id: T3\n    title: Verify\n    deps: [T2]\n```\n"), 0644)

	c := sdd.NewController(ws, git, dag, rs, progress, factory, routing.Config{}, ss, "sdd/feature", "main")
	// Start at Decompose to skip workspace reset (which would archive spec.md).
	c.State = sdd.StateDecompose

	err = c.Run(context.Background())
	if err != nil && !errors.Is(err, sdd.ErrHumanGateRequired) {
		t.Fatalf("Run: %v", err)
	}
	// The controller should reach StateFinalMergeGate (human gate) with all 3 merged.
	if len(rs.Merged) != 3 {
		t.Errorf("merged = %v, want 3 tasks", rs.Merged)
	}
	if c.State != sdd.StateFinalMergeGate && c.State != sdd.StateFinalize {
		t.Errorf("final state = %v, want StateFinalMergeGate or StateFinalize", c.State)
	}
}
