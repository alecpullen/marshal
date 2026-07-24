package agent

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// TestAutoModeBlocksAskUser verifies that in auto mode, an ask_user
// action gets a correction message instead of blocking on the user.
func TestAutoModeBlocksAskUser(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"which?"}}`,
		`{"rationale":"done","action":{"type":"final","content":"Proceeded with best judgment."}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	pol.SetApprovalMode(policy.ModeAuto)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The first turn should have produced a correction telling the agent
	// ask_user is not available in auto mode, not a pending question.
	if state.PendingQuestion() != nil {
		t.Fatal("auto mode should not set a pending question")
	}
	// Find the correction message in the second provider request.
	second := p.Requests[len(p.Requests)-1]
	found := false
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "ask_user is not available in auto mode") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a correction message mentioning auto mode; none found in provider requests")
	}
}

// TestAutoModeBlocksQuestionAsk verifies that in auto mode, a question.ask
// action gets a correction message instead of blocking on the user.
func TestAutoModeBlocksQuestionAsk(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"ambiguous","action":{"type":"question.ask","questions":[{"question":"which?"}]}}`,
		`{"rationale":"done","action":{"type":"final","content":"Proceeded with best judgment."}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	pol.SetApprovalMode(policy.ModeAuto)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.PendingQuestion() != nil {
		t.Fatal("auto mode should not set a pending question")
	}
	second := p.Requests[len(p.Requests)-1]
	found := false
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "question.ask is not available in auto mode") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a correction message mentioning auto mode; none found in provider requests")
	}
}

// TestEditModeAllowsAskUser verifies that in edit mode (the default),
// ask_user works normally.
func TestEditModeAllowsAskUser(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Archive or delete?"}}`,
		`{"rationale":"done","action":{"type":"final","content":"Archived as requested."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	questionCh := answerPendingQuestion(state, "archive")

	task, err := r.RunTask(context.Background(), "clean up old records")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if got := <-questionCh; got != "Archive or delete?" {
		t.Fatalf("question = %q", got)
	}
	if task.Summary != "Archived as requested." {
		t.Fatalf("Summary = %q", task.Summary)
	}
}

// TestPlanModeAllowsAskUser verifies that in plan mode, ask_user is
// still allowed (plan mode may ask clarifying questions).
func TestPlanModeAllowsAskUser(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Which approach?"}}`,
		`{"rationale":"done","action":{"type":"final","content":"Planned."}}`,
	}}
	pol := policy.NewEngine(&config.Config{}, nil)
	pol.SetApprovalMode(policy.ModePlan)
	r := NewRunner(p, registry.New(), pol, state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	questionCh := answerPendingQuestion(state, "approach A")

	task, err := r.RunTask(context.Background(), "plan the work")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if got := <-questionCh; got != "Which approach?" {
		t.Fatalf("question = %q", got)
	}
	if task.Summary != "Planned." {
		t.Fatalf("Summary = %q", task.Summary)
	}
}
