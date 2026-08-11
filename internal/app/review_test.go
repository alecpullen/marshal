package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
)

func TestRunReviewSubagentRegistersCardAndAppendsSummary(t *testing.T) {
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})

	factoryCalled := false
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		factoryCalled = true
		childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		runner := agent.NewRunner(nil, nil, nil, childState, "test-model")
		runner.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			if prompt == "" {
				t.Fatalf("review prompt empty")
			}
			return &agent.Task{Summary: "Looks good."}, nil
		}
		return runner, childState, nil
	}

	if err := runReviewSubagent(context.Background(), state, factory, "main.go", ""); err != nil {
		t.Fatalf("runReviewSubagent() error = %v", err)
	}
	if !factoryCalled {
		t.Fatal("factory was not called")
	}

	views := state.Subagents()
	if len(views) != 1 {
		t.Fatalf("registered subagents = %d, want 1", len(views))
	}
	v := views[0]
	if v.Status != session.SubagentDone {
		t.Fatalf("subagent status = %v, want Done", v.Status)
	}
	if v.Provider != "" {
		t.Fatalf("provider = %q, want empty when runner.Provider is nil", v.Provider)
	}
	if v.Model != "test-model" {
		t.Fatalf("model = %q, want test-model", v.Model)
	}
	if v.Role != routing.RoleReviewer {
		t.Fatalf("role = %v, want RoleReviewer", v.Role)
	}

	msgs := state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected assistant summary appended to transcript")
	}
	last := msgs[len(msgs)-1]
	if last.Role != session.RoleAssistant || last.Content != "Looks good." {
		t.Fatalf("last message = %+v, want assistant summary", last)
	}
}

func TestRunReviewSubagentFactoryError(t *testing.T) {
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		return nil, nil, errors.New("factory exploded")
	}

	err := runReviewSubagent(context.Background(), state, factory, "", "")
	if err == nil {
		t.Fatal("expected error from factory")
	}
	if len(state.Subagents()) != 0 {
		t.Fatal("no subagent should be registered on factory error")
	}
}

func TestRunReviewSubagentRunErrorMarksFailed(t *testing.T) {
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		runner := agent.NewRunner(nil, nil, nil, childState, "test-model")
		runner.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			return nil, errors.New("child failed")
		}
		return runner, childState, nil
	}

	err := runReviewSubagent(context.Background(), state, factory, "", "")
	if err == nil {
		t.Fatal("expected error from child run")
	}
	views := state.Subagents()
	if len(views) != 1 || views[0].Status != session.SubagentFailed {
		t.Fatalf("expected one failed subagent, got %+v", views)
	}
	if len(state.Messages()) != 0 {
		t.Fatal("no assistant message expected on failure")
	}
}

func TestRunReviewSubagentDispatchesReviewerRole(t *testing.T) {
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	var gotReq agent.SubagentRequest
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		gotReq = req
		childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		runner := agent.NewRunner(nil, nil, nil, childState, "test-model")
		runner.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			return &agent.Task{Summary: "Looks good."}, nil
		}
		return runner, childState, nil
	}

	if err := runReviewSubagent(context.Background(), state, factory, "", ""); err != nil {
		t.Fatalf("runReviewSubagent() error = %v", err)
	}
	if gotReq.Role != routing.RoleReviewer {
		t.Fatalf("dispatched role = %v, want RoleReviewer", gotReq.Role)
	}
}

func TestRunReviewSubagentExplicitModelBeatsRole(t *testing.T) {
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	var gotReq agent.SubagentRequest
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		gotReq = req
		childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		runner := agent.NewRunner(nil, nil, nil, childState, "test-model")
		runner.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			return &agent.Task{Summary: "Looks good."}, nil
		}
		return runner, childState, nil
	}

	if err := runReviewSubagent(context.Background(), state, factory, "main.go", "ollama/explicit"); err != nil {
		t.Fatalf("runReviewSubagent() error = %v", err)
	}
	if gotReq.Role != routing.RoleReviewer {
		t.Fatalf("dispatched role = %v, want RoleReviewer", gotReq.Role)
	}
	if gotReq.Model != "ollama/explicit" {
		t.Fatalf("dispatched model = %q, want ollama/explicit", gotReq.Model)
	}
}
