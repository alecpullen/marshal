package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/permissions"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestNativeQuestionAskDeclined(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Need to ask.", "Done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "call_q", Name: "question.ask", Args: json.RawMessage(`{"questions":[{"question":"Keep legacy?"}]}`)}},
			nil,
		},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	// Simulate what the TUI actually does: send AnswerUnanswered when user declines.
	_ = answerPendingQuestion(state, session.AnswerUnanswered)

	task, err := r.RunTask(context.Background(), "ask something")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "Done." {
		t.Fatalf("Summary = %q, want Done.", task.Summary)
	}
	// Verify the runner used the correct sentinel constant by checking state messages.
	foundUnanswered := false
	for _, a := range state.Messages() {
		if strings.Contains(a.Content, "Unanswered") {
			foundUnanswered = true
			break
		}
	}
	if !foundUnanswered {
		t.Fatal("expected state to contain AnswerUnanswered sentinel in content after declined question.ask")
	}
}

func TestNativeAskUserCountsAgainstIterationBudget(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Ask1.", "Ask2."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "ask_user", Args: json.RawMessage(`{"question":"Q1?"}`)}},
			{{ID: "c2", Name: "ask_user", Args: json.RawMessage(`{"question":"Q2?"}`)}},
		},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	done := make(chan struct{})
	go func() {
		for i := 0; i < 2; i++ {
			answerPendingQuestion(state, "yes")
		}
		close(done)
	}()

	task, err := r.RunTask(context.Background(), "ask repeatedly")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "Ask2." {
		t.Fatalf("Summary = %q, want Ask2.", task.Summary)
	}
	if got == nil || got.Iterations < 4 {
		// Each ask_user batch consumes 1 iteration from line 564 +
		// 1 iteration from executeNativeAskUser = 2 per call × 2 calls = 4
		t.Fatalf("Iterations = %d, want at least 4", got.Iterations)
	}
	if r.iterationBudget != nil {
		t.Fatalf("iterationBudget should be nil after RunTask")
	}
}

func TestNativeQuestionAskCountsAgainstIterationBudget(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"Ask1.", "Ask2."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "question.ask", Args: json.RawMessage(`{"questions":[{"question":"Q1?"}]}`)}},
			{{ID: "c2", Name: "question.ask", Args: json.RawMessage(`{"questions":[{"question":"Q2?"}]}`)}},
		},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	done := make(chan struct{})
	go func() {
		for i := 0; i < 2; i++ {
			answerPendingQuestion(state, "yes")
		}
		close(done)
	}()

	task, err := r.RunTask(context.Background(), "ask repeatedly")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if got == nil || got.Iterations < 4 {
		t.Fatalf("Iterations = %d, want at least 4", got.Iterations)
	}
	_ = task
}

func TestRequestApprovalTimeout(t *testing.T) {
	state := newTestState(t)
	r := NewRunner(&agenttest.ScriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.RequestTimeout = 10 * time.Millisecond

	ctx := context.Background()
	// Tool that will never get a response — the channel has a buffer of 1 but
	// nobody sends on it. The timeout arm should fire.
	_, _, err := r.requestApproval(ctx, registry.Tool{Name: "test", Risk: registry.RiskReadOnly, Description: "test"}, "test", nil, map[string]interface{}{}, "test reason")
	if !errors.Is(err, ErrRequestTimedOut) {
		t.Fatalf("requestApproval err = %v, want ErrRequestTimedOut", err)
	}
	if state.PendingApproval() != nil {
		t.Fatal("PendingApproval should be nil after timeout")
	}
}

func TestRequestQuestionsTimeout(t *testing.T) {
	state := newTestState(t)
	r := NewRunner(&agenttest.ScriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.RequestTimeout = 10 * time.Millisecond

	ctx := context.Background()
	_, err := r.requestQuestions(ctx, []session.Question{{Question: "Q?"}})
	if !errors.Is(err, ErrRequestTimedOut) {
		t.Fatalf("requestQuestions err = %v, want ErrRequestTimedOut", err)
	}
	if state.PendingQuestion() != nil {
		t.Fatal("PendingQuestion should be nil after timeout")
	}
}

func TestBuildQuestionLabel(t *testing.T) {
	tests := []struct {
		name      string
		questions []session.Question
		want      string
	}{
		{
			name:      "empty questions falls back",
			questions: nil,
			want:      "waiting for your answer",
		},
		{
			name:      "single short question",
			questions: []session.Question{{Question: "Archive or delete?"}},
			want:      "waiting for your answer: Archive or delete?",
		},
		{
			name:      "single long question truncated",
			questions: []session.Question{{Question: "Should the cache be per-session or global? This is a very long question that exceeds forty characters"}},
			want:      "waiting for your answer: Should the cache be per-session or globa…",
		},
		{
			name:      "multiple questions",
			questions: []session.Question{{Question: "Auth?"}, {Question: "Keep legacy?"}},
			want:      "waiting for your answer (Q1/2): Auth?",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildQuestionLabel(tt.questions)
			if got != tt.want {
				t.Fatalf("buildQuestionLabel = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunRequiresApprovalForShellRunAndRespectsApproval(t *testing.T) {
	reg := registry.New()
	executed := make(chan struct{}, 1)
	if err := reg.Register(registry.Tool{
		Name:        "shell.run",
		Description: "runs a shell command",
		Risk:        registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executed <- struct{}{}
			return registry.ToolResult{Summary: "ran ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"check status","action":{"type":"tool_call","tool":"shell.run","args":{"command":"echo hi"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Command ran."}}`,
	}}
	cfg := config.Default()
	pol := policy.NewEngine(&cfg, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	runErr := make(chan error, 1)
	go func() {
		runErr <- runner.Run(context.Background(), "Run echo hi")
	}()

	var tc *session.PendingToolCall
	deadline := time.After(2 * time.Second)
	for tc == nil {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for pending approval")
		default:
			tc = state.PendingApproval()
		}
	}
	if tc.Name != "shell.run" || tc.Command != "echo hi" {
		t.Fatalf("pending approval = %#v", tc)
	}
	tc.ResponseChan <- session.UserApprovalDecision{Approved: true}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to finish")
	}

	select {
	case <-executed:
	default:
		t.Fatal("tool handler was never executed")
	}
}

func TestRunnerShellEditNormalizesSuccessfully(t *testing.T) {
	reg := registry.New()
	var calledArgs string
	if err := reg.Register(registry.Tool{
		Name: "shell.run", Risk: registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			calledArgs = string(call.Args)
			return registry.ToolResult{Summary: "ran"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"run","action":{"type":"tool_call","tool":"shell.run","args":{"command":"echo hello"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"done"}}`,
	}}
	state := newTestState(t)
	pol := policy.NewEngine(&config.Config{}, nil)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassQuestion))

	go func() {
		for state.PendingApproval() == nil {
			time.Sleep(10 * time.Millisecond)
		}
		tc := state.PendingApproval()
		tc.ResponseChan <- session.UserApprovalDecision{
			Approved: true,
			Edited:   "echo edited",
		}
	}()

	if err := runner.Run(context.Background(), "run"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(calledArgs, "echo edited") {
		t.Errorf("calledArgs = %q, want to contain 'echo edited'", calledArgs)
	}
}

func TestRunnerReevaluatesPolicyAfterEditedArgs(t *testing.T) {
	// Verifies that a non-shell tool whose args edit is syntactically invalid
	// JSON causes the runner to abort with an error rather than silently
	// proceeding with the original args (the bug: the else branch at
	// runner.go:1125 silently swallowed invalid JSON).
	//
	// We use web.fetch because the policy engine unconditionally returns
	// DecisionConfirm for it, and it is NOT shell.run, so edits go through
	// the else branch.
	reg := registry.New()
	executed := make(chan struct{}, 1)
	if err := reg.Register(registry.Tool{
		Name: "web.fetch",
		Risk: registry.RiskNetwork,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			select {
			case executed <- struct{}{}:
			default:
			}
			return registry.ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Two Responses: the tool call (triggers approval) and a final message so
	// the loop terminates even if the buggy code ignores the edit.
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"fetch","action":{"type":"tool_call","tool":"web.fetch","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"done"}}`,
	}}
	cfg := config.Default()
	pol := policy.NewEngine(&cfg, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	runErr := make(chan error, 1)
	go func() {
		runErr <- runner.Run(context.Background(), "Fetch")
	}()

	var tc *session.PendingToolCall
	deadline := time.After(5 * time.Second)
	for tc == nil {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for pending approval")
		default:
			tc = state.PendingApproval()
		}
	}

	// Send a syntactically invalid JSON edit — the runner must reject it.
	tc.ResponseChan <- session.UserApprovalDecision{
		Approved: true,
		Edited:   "not json",
	}

	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("expected error from invalid JSON edit, got nil")
		}
		if !strings.Contains(err.Error(), "not valid JSON") {
			t.Fatalf("error does not mention invalid JSON: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Run to finish")
	}

	select {
	case <-executed:
		t.Fatal("tool handler was executed despite invalid JSON edit")
	default:
	}
}

func TestRunnerReevaluatesDenyAfterValidEdit(t *testing.T) {
	// Verifies that when the user edits args with valid JSON and the
	// re-evaluated policy returns DecisionDeny, the runner returns a
	// tool-error message (not a hard error) and does NOT execute the tool
	// handler.
	//
	// We use web.fetch because the policy engine unconditionally returns
	// DecisionConfirm for it on first evaluation, letting us reach the
	// approval dialog. After the approval is pending we inject a deny rule
	// via SetRules so the re-evaluation on the edited args returns Deny.
	reg := registry.New()
	executed := make(chan struct{}, 1)
	if err := reg.Register(registry.Tool{
		Name: "web.fetch",
		Risk: registry.RiskNetwork,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			select {
			case executed <- struct{}{}:
			default:
			}
			return registry.ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"fetch","action":{"type":"tool_call","tool":"web.fetch","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"done"}}`,
	}}
	cfg := config.Default()
	pol := policy.NewEngine(&cfg, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	runErr := make(chan error, 1)
	go func() {
		runErr <- runner.Run(context.Background(), "Fetch")
	}()

	var tc *session.PendingToolCall
	deadline := time.After(5 * time.Second)
	for tc == nil {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for pending approval")
		default:
			tc = state.PendingApproval()
		}
	}

	// First Evaluate returned DecisionConfirm (no deny rules for web.fetch).
	// Install a deny rule so the re-evaluation after edit sees DecisionDeny.
	pol.SetRules([]permissions.Rule{
		{Permission: "web.fetch", Pattern: "web.fetch", Action: permissions.ActionDeny},
	})

	// Send a valid JSON edit — the re-evaluation must deny it.
	tc.ResponseChan <- session.UserApprovalDecision{
		Approved: true,
		Edited:   `{"url":"https://evil.example/path"}`,
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Run to finish")
	}

	select {
	case <-executed:
		t.Fatal("tool handler was executed despite policy deny after edit")
	default:
	}

	// Verify the provider received a message mentioning the denial.
	if len(p.Requests) < 2 {
		t.Fatalf("expected at least 2 provider requests, got %d", len(p.Requests))
	}
	lastReq := p.Requests[len(p.Requests)-1]
	found := false
	for _, msg := range lastReq.Messages {
		if strings.Contains(msg.Content, "denied") || strings.Contains(msg.Content, "deny") {
			found = true
			break
		}
	}
	if !found {
		for i, msg := range lastReq.Messages {
			t.Logf("msg[%d] role=%s content=%q", i, msg.Role, msg.Content)
		}
		t.Fatal("expected a message mentioning denial in the last provider request")
	}
}

func TestRunnerNonShellToolApprovalAndJSONEditing(t *testing.T) {
	reg := registry.New()
	var calledArgs string
	if err := reg.Register(registry.Tool{
		Name: "mcp.github.create_issue", Description: "creates a github issue", Risk: registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			calledArgs = string(call.Args)
			return registry.ToolResult{Summary: "created"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale": "call tool", "action": {"type": "tool_call", "tool": "mcp.github.create_issue", "args": {"title": "old title", "body": "old body"}}}`,
		`{"rationale": "done", "action": {"type": "final", "content": "done"}}`,
	}}

	state := newTestState(t)

	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.SetForceClass("question")

	go func() {
		for state.PendingApproval() == nil {
			time.Sleep(10 * time.Millisecond)
		}
		tc := state.PendingApproval()
		if tc.Name != "mcp.github.create_issue" {
			t.Errorf("tc.Name = %q, want mcp.github.create_issue", tc.Name)
		}
		if tc.Schema != "creates a github issue" {
			t.Errorf("tc.Schema = %q, want 'creates a github issue'", tc.Schema)
		}
		tc.ResponseChan <- session.UserApprovalDecision{
			Approved: true,
			Edited:   `{"title":"new title","body":"new body"}`,
		}
	}()

	if err := runner.Run(context.Background(), "create issue"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantArgs := `{"title":"new title","body":"new body"}`
	if calledArgs != wantArgs {
		t.Errorf("calledArgs = %q, want %q", calledArgs, wantArgs)
	}
}

func TestAuditEventRecordsOriginalArgs(t *testing.T) {
	// Simulate a pre_tool_use hook rewriting "git status" to
	// "git --no-pager log". "git status" is auto-approved by the
	// allow list, so no first approval is needed. After the rewrite
	// the user is re-prompted to approve "git --no-pager log".
	// Verify the audit event preserves the original approved args.
	reg := registry.New()
	executed := false
	if err := reg.Register(registry.Tool{
		Name: "shell.run", Risk: registry.RiskCommand,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executed = true
			return registry.ToolResult{Summary: "ran"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Hook rewrites once, then stops on subsequent calls.
	hook := &onceRewriteHookRunner{}
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"r","action":{"type":"tool_call","tool":"shell.run","args":{"command":"git status"}}}`,
		`{"rationale":"r","action":{"type":"answer","content":"done"}}`,
	}}
	cfg := config.Default()
	pol := policy.NewEngine(&cfg, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.HookRunner = hook
	runner.SetForceClass(string(ClassQuestion))

	runErr := make(chan error, 1)
	go func() {
		runErr <- runner.Run(context.Background(), "check status")
	}()

	// "git status" is auto-approved (allow list). The hook rewrites it
	// to "git --no-pager log", which requires approval. Wait for that
	// single approval dialog.
	var tc *session.PendingToolCall
	deadline := time.After(5 * time.Second)
	for tc == nil {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for approval after rewrite")
		default:
			tc = state.PendingApproval()
		}
	}
	tc.ResponseChan <- session.UserApprovalDecision{Approved: true}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Run to finish")
	}

	if !executed {
		t.Fatal("tool was not executed")
	}

	log := state.AuditLog()
	// Find the execution event (the last successful shell.run event).
	var execEvent *registry.AuditEvent
	for i := len(log) - 1; i >= 0; i-- {
		if log[i].Error == "" && log[i].ToolName == "shell.run" {
			execEvent = &log[i]
			break
		}
	}
	if execEvent == nil {
		t.Fatal("no successful shell.run audit event found")
	}
	if !execEvent.Rewritten {
		t.Fatal("expected Rewritten=true on the audit event")
	}
	if execEvent.OriginalArgs == nil {
		t.Fatal("expected OriginalArgs to be set")
	}
	if !strings.Contains(string(execEvent.OriginalArgs), "git status") {
		t.Fatalf("OriginalArgs = %s, want to contain 'git status'", string(execEvent.OriginalArgs))
	}
}
