package pipeline

import (
	"context"
	"errors"
	"testing"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/llm/routing"
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
	rep, err := d.Implement(context.Background(), routing.RoleSDDImplementer, "prompt")
	if err != nil {
		t.Fatalf("Implement: %v", err)
	}
	if rep.Status != StatusDone {
		t.Errorf("Status = %q", rep.Status)
	}
}

func TestDispatcherReviewRunsReadOnly(t *testing.T) {
	d := Dispatcher{
		exec: func(ctx context.Context, role agent.AgentRole, scope swarm.RegistryScope, prompt string) (string, error) {
			if scope != swarm.ScopeReadOnly {
				t.Errorf("scope = %v, want ScopeReadOnly", scope)
			}
			return "SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n", nil
		},
	}
	rev, err := d.Review(context.Background(), routing.RoleSDDReviewer, "prompt")
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
	if _, err := d.Implement(context.Background(), routing.RoleSDDImplementer, "p"); err == nil {
		t.Fatal("unparseable implementer output: want error, got nil")
	}
}
