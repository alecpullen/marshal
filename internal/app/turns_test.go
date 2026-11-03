package app

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/agent"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// turnState builds a parent session for the turn-call tests.
func turnState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

func TestTurnRunnerIsNilWithoutARunner(t *testing.T) {
	rt := &Runtime{}
	if got := rt.TurnRunner(); got != nil {
		t.Fatalf("TurnRunner() = %v, want an untyped nil interface", got)
	}
	rt.Runner = agent.NewRunner(nil, nil, nil, turnState(t), "test-model")
	if got := rt.TurnRunner(); got == nil {
		t.Fatal("TurnRunner() = nil with a runner set, want it")
	}
}

func TestRunCustomAgentShowsTheChildAndItsSummary(t *testing.T) {
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})

	var got agent.SubagentRequest
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		got = req
		childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		runner := agent.NewRunner(nil, nil, nil, childState, "test-model")
		runner.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			if prompt != "check style" {
				t.Fatalf("child prompt = %q, want check style", prompt)
			}
			return &agent.Task{Summary: "Done."}, nil
		}
		return runner, childState, nil
	}

	if err := runCustomAgent(context.Background(), state, factory, "linter", "check style"); err != nil {
		t.Fatalf("runCustomAgent() error = %v", err)
	}
	if got.Agent != "linter" {
		t.Fatalf("request Agent = %q, want linter", got.Agent)
	}

	views := state.Subagents()
	if len(views) != 1 {
		t.Fatalf("registered subagents = %d, want 1", len(views))
	}
	if views[0].Label != "agent · linter" {
		t.Fatalf("label = %q, want agent · linter", views[0].Label)
	}
	if views[0].Status != session.SubagentDone {
		t.Fatalf("status = %v, want Done", views[0].Status)
	}

	msgs := state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected the child's summary in the transcript")
	}
	last := msgs[len(msgs)-1]
	if last.Role != session.RoleAssistant || last.Content != "Done." {
		t.Fatalf("last message = %+v, want the assistant's Done.", last)
	}
}

func TestRunCustomAgentReportsABuildError(t *testing.T) {
	state := turnState(t)
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		return nil, nil, errors.New("unknown custom agent")
	}

	err := runCustomAgent(context.Background(), state, factory, "linter", "check style")
	if err == nil {
		t.Fatal("expected an error from a failing factory")
	}
	if want := "run agent: build subagent: unknown custom agent"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err, want)
	}
	if views := state.Subagents(); len(views) != 0 {
		t.Fatalf("registered subagents = %d, want none", len(views))
	}
}

func TestProposeVerifyCommandsReadsTheChildTranscript(t *testing.T) {
	cfg := config.Default()
	parent := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	parent.AddMessage(session.RoleAssistant, `{"build":"wrong","test":"wrong"}`, session.ContentTypePlain)

	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		runner := agent.NewRunner(nil, nil, nil, childState, "test-model")
		runner.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			if prompt != verifyProposalPrompt {
				t.Fatalf("proposal prompt = %q, want the shared prompt", prompt)
			}
			childState.AddMessage(session.RoleAssistant, `{"build":"make build","test":"make test"}`, session.ContentTypePlain)
			return &agent.Task{Summary: `{"build":"make build","test":"make test"}`}, nil
		}
		return runner, childState, nil
	}

	got, err := proposeVerifyCommands(context.Background(), factory)
	if err != nil {
		t.Fatalf("proposeVerifyCommands() error = %v", err)
	}
	if got.Build != "make build" || got.Test != "make test" {
		t.Fatalf("got %q / %q, want the child's commands", got.Build, got.Test)
	}
	if msgs := parent.Messages(); len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "wrong") {
		t.Fatal("the proposal must read the child's transcript, not the parent's")
	}
}

func TestProposeVerifyCommandsWithoutJSONFails(t *testing.T) {
	cfg := config.Default()
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		runner := agent.NewRunner(nil, nil, nil, childState, "test-model")
		runner.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			childState.AddMessage(session.RoleAssistant, "I could not decide.", session.ContentTypePlain)
			return &agent.Task{Summary: "I could not decide."}, nil
		}
		return runner, childState, nil
	}

	_, err := proposeVerifyCommands(context.Background(), factory)
	if err == nil {
		t.Fatal("expected an error from a reply with no JSON object")
	}
	if want := "proposal task returned no parseable JSON"; err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestVerifyCommandsFromTakesTheNewestObject(t *testing.T) {
	msgs := []session.Message{
		{Role: session.RoleAssistant, Content: `{"build":"old build","test":"old test"}`},
		{Role: session.RoleAssistant, Content: `{"build":"new build","test":"new test"}`},
		{Role: session.RoleAssistant, Content: "no JSON here"},
	}

	got, err := verifyCommandsFrom(msgs)
	if err != nil {
		t.Fatalf("verifyCommandsFrom() error = %v", err)
	}
	if got.Build != "new build" || got.Test != "new test" {
		t.Fatalf("got %q / %q, want the newer object", got.Build, got.Test)
	}
}

func TestRunReviewPassesTheBaseAsAMergeBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	state := turnState(t) // an empty temp dir: not a repository
	rt := &Runtime{
		State: state,
		CustomAgentFactory: func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
			t.Fatalf("factory called for %+v; the range should have failed first", req)
			return nil, nil, nil
		},
	}

	err := rt.RunReview(context.Background(), acpwire.ReviewParams{Base: "main"})
	if err == nil {
		t.Fatal("expected an error for a base outside a repository")
	}
	if want := `cannot find merge-base for "main"`; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err, want)
	}
}

func TestTheRuntimeCallsFailWithoutAFactory(t *testing.T) {
	state := turnState(t)
	rt := &Runtime{State: state}
	ctx := context.Background()

	if err := rt.RunReview(ctx, acpwire.ReviewParams{}); err == nil {
		t.Error("RunReview() with no factory = nil, want an error")
	}
	if err := rt.RunCustomAgent(ctx, acpwire.AgentRunParams{Agent: "linter"}); err == nil {
		t.Error("RunCustomAgent() with no factory = nil, want an error")
	}
	if _, err := rt.ProposeVerifyCommands(ctx); err == nil {
		t.Error("ProposeVerifyCommands() with no factory = nil, want an error")
	}
	got, err := rt.SuggestNextReply(ctx, "hello")
	if err != nil || got != "" {
		t.Errorf("SuggestNextReply() = %q, %v; want empty and no error", got, err)
	}
}

func TestBindTurnDepsServesTheRuntime(t *testing.T) {
	state := turnState(t)
	rt := &Runtime{State: state}
	d := inproc.Deps{State: state}
	bindTurnDeps(&d, rt)

	_, sess := inproc.New(d)
	if sess.Status().AgentReady {
		t.Fatal("AgentReady = true before the runtime has a runner")
	}

	rt.Runner = agent.NewRunner(nil, nil, nil, state, "test-model")
	if !sess.Status().AgentReady {
		t.Fatal("AgentReady = false once the runtime has a runner; the bound deps must read it live")
	}
}
