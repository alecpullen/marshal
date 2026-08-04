package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestRunHandlesAskUserAction(t *testing.T) {
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
	second := p.Requests[len(p.Requests)-1]
	found := false
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "User answered: archive") {
			found = true
		}
	}
	if !found {
		t.Fatal("answer not fed back to the model")
	}
	var sawRecord, sawQuestionEcho bool
	for _, m := range state.Messages() {
		if m.Role == session.RoleAssistant && strings.Contains(m.Content, "Archive or delete?") {
			sawQuestionEcho = true
		}
		if m.Role == session.RoleUser && m.Content == `- "Archive or delete?": "archive"` {
			sawRecord = true
		}
	}
	if sawQuestionEcho {
		t.Fatal("question text must not be written to the transcript before the popup")
	}
	if !sawRecord {
		t.Fatal("transcript missing the Q&A record")
	}
}

func TestRunAskUserDeclinedContinues(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Archive or delete?"}}`,
		`{"rationale":"done","action":{"type":"final","content":"Proceeded with best judgment."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	_ = answerPendingQuestion(state, "")

	task, err := r.RunTask(context.Background(), "clean up old records")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "Proceeded with best judgment." {
		t.Fatalf("Summary = %q", task.Summary)
	}
	second := p.Requests[len(p.Requests)-1]
	found := false
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "declined to answer") {
			found = true
		}
	}
	if !found {
		t.Fatal("declined marker not fed back to the model")
	}
}

func TestRunAskUserCancelledByContext(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Archive or delete?"}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for state.PendingQuestion() == nil {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	if _, err := r.RunTask(ctx, "clean up"); err == nil {
		t.Fatal("expected error on cancelled question wait")
	}
	if state.PendingQuestion() != nil {
		t.Fatal("pending question must be cleared on cancellation")
	}
}

func TestSwarmRolesCannotAskUser(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Which file?"}}`,
		`{"rationale":"done","action":{"type":"final","content":"Findings reported."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.Role = RoleRepoScout
	r.SetForceClass(string(ClassQuestion))

	if _, err := r.RunTask(context.Background(), "scout the repo"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	second := p.Requests[len(p.Requests)-1]
	found := false
	for _, m := range second.Messages {
		if strings.Contains(m.Content, "ask_user is not available") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a correction telling the role ask_user is unavailable")
	}
}

// TestRunNativeEmptyResponsesFinalizeWithReasonEmpty is the regression test
// for the infinite empty-response loop: in native mode, a model that returns
// no tool calls and empty text used to continue without incrementing
// iteration or invoking the stall detector, looping forever. After the fix,
// two consecutive empty responses short-circuit to finalize with reasonEmpty
// and the task completes (salvaged) instead of hanging.
func TestRunNativeEmptyResponsesFinalizeWithReasonEmpty(t *testing.T) {
	state := newTestState(t)
	// Two empty native responses (no text, no tool calls), then finalize's
	// own forced call produces a real prose answer.
	p := &agenttest.ScriptedProvider{
		Responses: []string{"", "", "Here is the salvaged answer."},
		ToolCalls: [][]schema.ToolCall{nil, nil, nil},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 16

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	task, err := r.RunTask(context.Background(), "vague goal")
	if err != nil {
		t.Fatalf("RunTask err = %v, want nil (salvaged)", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("task.Status = %q, want completed", task.Status)
	}
	if task.SalvagedReason != "empty" {
		t.Fatalf("SalvagedReason = %q, want %q", task.SalvagedReason, "empty")
	}
	if task.Summary != "Here is the salvaged answer." {
		t.Fatalf("Summary = %q, want the finalize prose answer", task.Summary)
	}
	// Two empty turns consumed the budget before finalize fired.
	if got == nil || got.Iterations != 2 {
		t.Fatalf("Iterations = %v, want 2 (empty turns must consume budget)", got)
	}
}

// TestRunNativeEmptyThenAnswerWins verifies that a single empty response
// followed by a real final answer is honored — the loop must not over-eagerly
// finalize after one silence.
func TestRunNativeEmptyThenAnswerWins(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"", "The real answer after a moment of silence."},
		ToolCalls: [][]schema.ToolCall{nil, nil},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	task, err := r.RunTask(context.Background(), "what is 2+2?")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty (normal completion)", task.SalvagedReason)
	}
	if task.Summary != "The real answer after a moment of silence." {
		t.Fatalf("Summary = %q", task.Summary)
	}
}

// TestRunAskUserDeclinedCountsAgainstBudget ensures a declined ask_user
// consumes a budget slot so repeated ask→decline→ask cannot loop unbounded.
func TestRunAskUserDeclinedCountsAgainstBudget(t *testing.T) {
	state := newTestState(t)
	// Repeated ask_user, always declined, until the budget runs out. With a
	// small budget the loop must terminate (via finalize/salvage or budget
	// exhaustion) rather than looping ask→decline forever.
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"ambiguous","action":{"type":"ask_user","content":"Which one?"}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 3
	r.MaxRetries = 0

	// Drain every pending question with an empty (declined) answer.
	go func() {
		for {
			if q := state.PendingQuestion(); q != nil && len(q.Questions) > 0 {
				canonical := q.Questions[0].Question
				q.ResponseChan <- []session.Answer{{Question: canonical, Answer: ""}}
				state.SetPendingQuestion(nil)
			} else {
				time.Sleep(time.Millisecond)
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.RunTask(context.Background(), "decide something")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunTask hung: declined ask_user did not consume the budget")
	}
}
