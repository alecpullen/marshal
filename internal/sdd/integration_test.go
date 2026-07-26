package sdd_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestIntegrationEscalation(t *testing.T) {
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws.Ensure()
	git := sdd.NewFakeGitOps()
	// Seed the pipeline branch + T1 task branch.
	git.SetRef("sdd/feature", "base123")
	git.SetRef("sdd/T1", "base123")
	git.SetBranch("sdd/T1")
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{
		{ID: "T1", Title: "Foundation", Status: sdd.TaskPending},
	}}
	rs := &sdd.RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "base123", Merged: []string{}}
	progress := &sdd.Progress{}
	ss := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})

	var callCount int
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		callCount++
		switch role {
		case routing.RoleSDDOrchestrator:
			// Return BLOCKED with DIRTY_MAIN_REPO (no deterministic fix).
			return &agent.Runner{RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
				return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: BLOCKED\nbatch_id: 1\ndispatched: [T1]\nmerged: []\nblocked_tasks:\n  - task: T1\n    reason: DIRTY_MAIN_REPO\nhealth_alerts: []\nedit_guard: clean\nnext_action: retry\n"}, nil
			}}, nil
		case routing.RoleSDDRescue:
			// Rescue returns NEEDS_HUMAN.
			return &agent.Runner{RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
				os.MkdirAll(filepath.Join(ws.Dir(), "reports"), 0755)
				os.WriteFile(filepath.Join(ws.Dir(), "reports", "orchestrator-rescue.md"), []byte("status: NEEDS_HUMAN\n\nMODEL_ESCALATION recommended\n"), 0644)
				return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "rescue done"}, nil
			}}, nil
		}
		return &agent.Runner{RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: DONE"}, nil
		}}, nil
	}

	// Seed an approved spec so the controller skips the spec gate.
	os.WriteFile(filepath.Join(ws.Dir(), "spec.md"), []byte("---\nstatus: approved\n---\n```yaml\ntasks:\n  - id: T1\n    title: Foundation\n    deps: []\n```\n"), 0644)

	c := sdd.NewController(ws, git, dag, rs, progress, factory, routing.Config{}, ss, "sdd/feature", "main")
	c.State = sdd.StateDecompose

	err = c.Run(context.Background())
	if !errors.Is(err, sdd.ErrHumanGateRequired) {
		t.Fatalf("Run returned %v, want ErrHumanGateRequired", err)
	}
	if c.State != sdd.StateBlocked {
		t.Errorf("c.State = %v, want StateBlocked", c.State)
	}
	gate := ss.SDDGate()
	if gate.Kind != "escalation" {
		t.Errorf("SDDGate.Kind = %q, want \"escalation\"", gate.Kind)
	}
	if len(c.BlockedTasks) != 1 || c.BlockedTasks[0].Reason != "DIRTY_MAIN_REPO" {
		t.Errorf("BlockedTasks = %v, want [{Task:T1 Reason:DIRTY_MAIN_REPO}]", c.BlockedTasks)
	}
}

func TestIntegrationRetryFlow(t *testing.T) {
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws.Ensure()
	git := sdd.NewFakeGitOps()
	// Seed the pipeline branch + T1 task branch.
	git.SetRef("sdd/feature", "base123")
	git.SetRef("sdd/T1", "base123")
	git.SetBranch("sdd/T1")
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{
		{ID: "T1", Title: "Foundation", Status: sdd.TaskPending},
	}}
	rs := &sdd.RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "base123", Merged: []string{}}
	progress := &sdd.Progress{}
	ss := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})

	var callCount int
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		return &agent.Runner{RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
			callCount++
			switch role {
			case routing.RoleSDDOrchestrator:
				if callCount == 1 {
					// First call: return BLOCKED with STALE_HEAD on T1.
					return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: BLOCKED\nbatch_id: 1\ndispatched: [T1]\nmerged: []\nblocked_tasks:\n  - task: T1\n    reason: STALE_HEAD\nhealth_alerts: []\nedit_guard: clean\nnext_action: retry\n"}, nil
				}
				// Second call: return DONE with T1 merged.
				dag.Tasks[0].Status = sdd.TaskMerged
				rs.MarkMerged("T1")
				return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: DONE\nbatch_id: 2\ndispatched: [T1]\nmerged: [T1]\nblocked_tasks: []\nhealth_alerts: []\nedit_guard: clean\nnext_action: branch_review\n"}, nil
			case routing.RoleSDDBranchReviewer:
				os.MkdirAll(filepath.Join(ws.Dir(), "reports"), 0755)
				os.WriteFile(filepath.Join(ws.Dir(), "reports", "branch.md"), []byte("status: PASS\nverdict: approve\n"), 0644)
				return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: PASS"}, nil
			}
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: DONE"}, nil
		}}, nil
	}

	// Seed an approved spec so the controller skips the spec gate.
	os.WriteFile(filepath.Join(ws.Dir(), "spec.md"), []byte("---\nstatus: approved\n---\n```yaml\ntasks:\n  - id: T1\n    title: Foundation\n    deps: []\n```\n"), 0644)

	c := sdd.NewController(ws, git, dag, rs, progress, factory, routing.Config{}, ss, "sdd/feature", "main")
	c.State = sdd.StateDecompose

	err = c.Run(context.Background())
	if err != nil && !errors.Is(err, sdd.ErrHumanGateRequired) {
		t.Fatalf("Run: %v", err)
	}
	// After retry flow: T1 should be merged, blocked tasks cleared, state past StateBlocked.
	if len(rs.Merged) != 1 {
		t.Errorf("merged = %v, want 1 task", rs.Merged)
	}
	if len(c.BlockedTasks) != 0 {
		t.Errorf("blocked tasks = %v, want empty", c.BlockedTasks)
	}
	if c.State != sdd.StateFinalMergeGate && c.State != sdd.StateFinalize {
		t.Errorf("final state = %v, want StateFinalMergeGate or StateFinalize", c.State)
	}
}

func TestIntegrationResume(t *testing.T) {
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

	// Seed an approved spec so the controller skips the spec gate.
	os.WriteFile(filepath.Join(ws.Dir(), "spec.md"), []byte("---\nstatus: approved\n---\n```yaml\ntasks:\n  - id: T1\n    title: Foundation\n    deps: []\n  - id: T2\n    title: Build\n    deps: [T1]\n  - id: T3\n    title: Verify\n    deps: [T2]\n```\n"), 0644)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var callCount int
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		return &agent.Runner{RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
			callCount++
			switch role {
			case routing.RoleSDDOrchestrator:
				if callCount == 1 {
					// First call: merge T1, return DONE with next_action: drain
					// so the controller loops back to drain.
					dag.Tasks[0].Status = sdd.TaskMerged
					rs.MarkMerged("T1")
					return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: DONE\nbatch_id: 1\ndispatched: [T1]\nmerged: [T1]\nblocked_tasks: []\nhealth_alerts: []\nedit_guard: clean\nnext_action: drain\n"}, nil
				}
				if callCount == 2 {
					// Second call: cancel context to interrupt mid-drain.
					cancel()
					return nil, ctx.Err()
				}
				// Subsequent calls (from second controller): merge remaining tasks.
				var dispatched []string
				for i := range dag.Tasks {
					if dag.Tasks[i].Status != sdd.TaskMerged {
						dag.Tasks[i].Status = sdd.TaskMerged
						rs.MarkMerged(dag.Tasks[i].ID)
						dispatched = append(dispatched, dag.Tasks[i].ID)
					}
				}
				merged := rs.Merged
				return &agent.Task{Status: agent.TaskStatusCompleted, Summary: fmt.Sprintf("status: DONE\nbatch_id: %d\ndispatched: [%s]\nmerged: [%s]\nblocked_tasks: []\nhealth_alerts: []\nedit_guard: clean\nnext_action: branch_review\n", callCount, strings.Join(dispatched, ","), strings.Join(merged, ","))}, nil
			case routing.RoleSDDBranchReviewer:
				os.MkdirAll(filepath.Join(ws.Dir(), "reports"), 0755)
				os.WriteFile(filepath.Join(ws.Dir(), "reports", "branch.md"), []byte("status: PASS\nverdict: approve\n"), 0644)
				return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: PASS"}, nil
			}
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: DONE"}, nil
		}}, nil
	}

	// First controller: run, gets interrupted mid-drain after merging T1.
	c1 := sdd.NewController(ws, git, dag, rs, progress, factory, routing.Config{}, ss, "sdd/feature", "main")
	c1.State = sdd.StateDecompose

	err = c1.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run returned %v, want context.Canceled", err)
	}
	if len(rs.Merged) != 1 || rs.Merged[0] != "T1" {
		t.Errorf("after first run: merged = %v, want [T1]", rs.Merged)
	}

	// Second controller: same ws/dag/rs, resumes from Decompose.
	c2 := sdd.NewController(ws, git, dag, rs, progress, factory, routing.Config{}, ss, "sdd/feature", "main")
	c2.State = sdd.StateDecompose

	// Use a fresh context (the old one is cancelled).
	err = c2.Run(context.Background())
	if err != nil && !errors.Is(err, sdd.ErrHumanGateRequired) {
		t.Fatalf("second Run: %v", err)
	}
	want := []string{"T1", "T2", "T3"}
	if !reflect.DeepEqual(rs.Merged, want) {
		t.Errorf("after second run: merged = %v, want %v", rs.Merged, want)
	}
	if c2.State != sdd.StateFinalMergeGate && c2.State != sdd.StateFinalize {
		t.Errorf("final state = %v, want StateFinalMergeGate or StateFinalize", c2.State)
	}
}
