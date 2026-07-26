package sdd_test

import (
	"context"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/sdd"
	"marshal/internal/tools/policy"
)

func TestControllerAdapterRunSetsPlanPath(t *testing.T) {
	ws, _ := sdd.NewWorkspace(t.TempDir())
	ss := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	factory := func(role routing.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		return &agent.Runner{RunTaskFunc: func(ctx context.Context, goal string) (*agent.Task, error) {
			return &agent.Task{Status: agent.TaskStatusCompleted, Summary: "status: DONE\nbatch_id: 0\ndispatched: []\nmerged: []\nblocked_tasks: []\nhealth_alerts: []\nedit_guard: clean\nnext_action: branch_review\n"}, nil
		}}, nil
	}
	c := sdd.NewController(ws, sdd.NewFakeGitOps(), &sdd.DAG{}, &sdd.RepoState{}, &sdd.Progress{}, factory, routing.Config{}, ss, "sdd/feature", "main")
	adapter := sdd.NewControllerAdapter(c)
	err := adapter.Run(context.Background(), "/path/to/plan.md")
	if err == nil {
		t.Fatal("expected ErrHumanGateRequired (no spec.md -> spec gate), got nil")
	}
	if c.PlanPath != "/path/to/plan.md" {
		t.Errorf("PlanPath = %q, want /path/to/plan.md", c.PlanPath)
	}
}

func TestControllerAdapterSettersAreNoOps(t *testing.T) {
	ws, _ := sdd.NewWorkspace(t.TempDir())
	c := sdd.NewController(ws, sdd.NewFakeGitOps(), &sdd.DAG{}, &sdd.RepoState{}, &sdd.Progress{}, nil, routing.Config{}, nil, "sdd/feature", "main")
	adapter := sdd.NewControllerAdapter(c)
	adapter.SetForceClass("question")
	adapter.SetPolicyRules(nil)
	adapter.SetApprovalMode(policy.ApprovalMode("")) // no panic
}
