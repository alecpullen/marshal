package sdd

import (
	"context"
	"testing"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/llm/routing"
)

func TestDeterministicFixWrongBase(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	git := NewFakeGitOps()
	git.SetRef("sdd/newfeature", "newhead")
	git.SetBranch("sdd/newfeature")
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main"}
	var p Progress
	if err := DeterministicFix("WRONG_BASE", ws, nil, rs, git, &p, "sdd/newfeature"); err != nil {
		t.Fatalf("DeterministicFix: %v", err)
	}
	if rs.Branch != "sdd/newfeature" {
		t.Errorf("rs.Branch = %q, want sdd/newfeature", rs.Branch)
	}
}

func TestDeterministicFixStaleHead(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	git := NewFakeGitOps()
	git.SetRef("sdd/feature", "newhead")
	git.SetBranch("sdd/T1")
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "oldhead", Merged: []string{"T1"}}
	var p Progress
	if err := DeterministicFix("STALE_HEAD", ws, nil, rs, git, &p, ""); err != nil {
		t.Fatalf("DeterministicFix: %v", err)
	}
	if rs.Head != "newhead" {
		t.Errorf("rs.Head = %q, want newhead", rs.Head)
	}
}

func TestDeterministicFixUnknownKind(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	git := NewFakeGitOps()
	rs := &RepoState{}
	var p Progress
	if err := DeterministicFix("UNKNOWN_KIND", ws, nil, rs, git, &p, ""); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestDispatchRescue(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	// Seed a rescue bundle so the rescue role has something to read.
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Status: TaskMerged}}}
	dag.Save(ws)
	rs := &RepoState{Branch: "sdd/feature", Head: "abc"}
	rs.Save(ws)
	var p Progress
	p.Append(ws, "T1", "MERGED", "branch", "sdd/T1")
	AssembleRescueBundle(ws)

	// Fake factory: returns a runner whose RunTask writes a rescue report.
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		r := &agent.Runner{}
		r.RunTaskFunc = func(ctx context.Context, goal string) (*agent.Task, error) {
			// Write the rescue report to the expected path.
			writeFile(ws.Dir()+"/reports/orchestrator-rescue.md", "status: NEEDS_HUMAN\n\nMODEL_ESCALATION recommended\n")
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "rescue done"}, nil
		}
		return r, nil
	}

	rep, err := DispatchRescue(context.Background(), factory, ws, &p)
	if err != nil {
		t.Fatalf("DispatchRescue: %v", err)
	}
	if rep.Status != ReportNeedsHuman {
		t.Errorf("Status = %q, want NEEDS_HUMAN", rep.Status)
	}
}

func TestDispatchRescueUsesRescueRole(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Status: TaskMerged}}}
	dag.Save(ws)
	rs := &RepoState{Branch: "sdd/feature", Head: "abc"}
	rs.Save(ws)
	var p Progress
	AssembleRescueBundle(ws)

	var seenRole agent.AgentRole
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		seenRole = role
		r := &agent.Runner{}
		r.RunTaskFunc = func(ctx context.Context, goal string) (*agent.Task, error) {
			writeFile(ws.Dir()+"/reports/orchestrator-rescue.md", "status: NEEDS_HUMAN\n\nx\n")
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "x"}, nil
		}
		return r, nil
	}

	_, _ = DispatchRescue(context.Background(), factory, ws, &p)
	if seenRole != routing.RoleSDDRescue {
		t.Errorf("factory called with role %q, want RoleSDDRescue", seenRole)
	}
}
