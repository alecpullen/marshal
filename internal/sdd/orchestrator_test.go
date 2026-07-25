package sdd

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
)

func TestOrchestratorDispatchParsesReport(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Title: "x", Status: TaskMerged}}}
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "abc", Merged: []string{"T1"}}
	var p Progress

	// Fake factory: returns a runner whose RunTask returns a canned report.
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		r := &agent.Runner{}
		r.RunTaskFunc = func(ctx context.Context, goal string) (*agent.Task, error) {
			return &agent.Task{
				Goal:    goal,
				Status:  agent.TaskStatusCompleted,
				Summary: "status: DONE\nbatch_id: 1\ndispatched: []\nmerged: []\nblocked_tasks: []\nhealth_alerts: []\nedit_guard: clean\nnext_action: drain\n",
			}, nil
		}
		return r, nil
	}

	o := NewOrchestrator(factory, ws, &p)
	report, err := o.Dispatch(context.Background(), dag, rs, "sdd/feature", 0)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if report.Status != ReportDone {
		t.Errorf("Status = %q, want DONE", report.Status)
	}
	if report.BatchID != 1 {
		t.Errorf("BatchID = %d", report.BatchID)
	}
}

func TestOrchestratorDispatchLogsProgress(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	ws.Ensure()
	dag := &DAG{Tasks: []DAGTask{{ID: "T1", Status: TaskMerged}}}
	rs := &RepoState{Branch: "sdd/feature", TargetBranch: "main", Head: "abc", Merged: []string{"T1"}}
	var p Progress

	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		r := &agent.Runner{}
		r.RunTaskFunc = func(ctx context.Context, goal string) (*agent.Task, error) {
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: DONE\nbatch_id: 1\ndispatched: []\nmerged: []\nblocked_tasks: []\nhealth_alerts: []\nedit_guard: clean\nnext_action: drain\n"}, nil
		}
		return r, nil
	}

	o := NewOrchestrator(factory, ws, &p)
	_, err := o.Dispatch(context.Background(), dag, rs, "sdd/feature", 0)
	if err != nil {
		t.Fatal(err)
	}
	lines, _ := p.Read(ws)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "DISPATCH_BATCH") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("progress log should contain DISPATCH_BATCH event")
	}
}
