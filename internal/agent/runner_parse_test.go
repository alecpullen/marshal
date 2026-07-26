package agent

import (
	"context"
	"errors"
	"fmt"
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

func TestParseFailuresDoNotConsumeToolIterations(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			"not a json action at all",
			`{"rationale":"done","action":{"type":"final","content":"Answer."}}`,
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	task, err := r.RunTask(context.Background(), "test goal")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("task.Status = %q, want completed", task.Status)
	}
	if got == nil {
		t.Fatal("no TurnMetrics emitted")
	}
	if got.Iterations != 1 {
		t.Fatalf("Iterations = %d, want 1 (parse failure must not consume budget)", got.Iterations)
	}
	if got.ParseFailures != 1 {
		t.Fatalf("ParseFailures = %d, want 1", got.ParseFailures)
	}
}

func TestSecondConsecutiveParseFailureEscalatesToRepair(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			"not json 1",
			"not json 2",
			`{"rationale":"recovered","action":{"type":"final","content":"recovered"}}`,
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	if _, err := r.RunTask(context.Background(), "test goal"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}

	foundRepair := false
	for _, m := range state.Messages() {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "two consecutive") {
			foundRepair = true
			break
		}
	}
	if !foundRepair {
		msgs := state.Messages()
		contents := make([]string, len(msgs))
		for i, m := range msgs {
			contents[i] = fmt.Sprintf("[%s] %s", m.Role, m.Content[:min(len(m.Content), 80)])
		}
		t.Fatalf("expected repair message in state.Messages() after 2 consecutive parse failures; got:\n%s", strings.Join(contents, "\n"))
	}
}

func TestSecondConsecutiveParseFailureEnablesJSONMode(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		ProviderCaps: schema.ProviderCapabilities{JSONMode: true},
		Responses: []string{
			"not json 1",
			"not json 2",
			`{"rationale":"recovered","action":{"type":"final","content":"recovered"}}`,
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	if _, err := r.RunTask(context.Background(), "test goal"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}

	if len(p.Requests) < 3 {
		t.Fatalf("expected at least 3 requests, got %d", len(p.Requests))
	}
	req := p.Requests[2]
	if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" {
		t.Fatalf("requests[2].ResponseFormat = %v, want {Type:\"json_object\"} after 2 consecutive parse failures", req.ResponseFormat)
	}
}

// TestResponseFormatResetsAcrossRunTaskCalls ensures that when a Runner
// triggers the JSON-mode response format escalation inside one RunTask,
// the next RunTask on the same *Runner starts with a clean response format
// (the original seed value, typically nil).
func TestResponseFormatResetsAcrossRunTaskCalls(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		ProviderCaps: schema.ProviderCapabilities{JSONMode: true},
		Responses: []string{
			// First RunTask: 2 parse failures → JSON mode, then recover
			"not json 1",
			"not json 2",
			`{"rationale":"recovered","action":{"type":"final","content":"first done"}}`,
			// Second RunTask: no JSON mode should leak across
			"not json 3",
			`{"rationale":"done","action":{"type":"final","content":"second done"}}`,
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	// First RunTask triggers JSON-mode escalation on the 3rd request
	if _, err := r.RunTask(context.Background(), "first goal"); err != nil {
		t.Fatalf("first RunTask err = %v", err)
	}

	firstRunRequestCount := len(p.Requests)

	// Second RunTask on the same *Runner
	if _, err := r.RunTask(context.Background(), "second goal"); err != nil {
		t.Fatalf("second RunTask err = %v", err)
	}

	// The first request of the second run must NOT inherit JSON mode
	req := p.Requests[firstRunRequestCount]
	if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_object" {
		t.Fatalf("second RunTask's first request has ResponseFormat = %v, want nil or non-json_object", req.ResponseFormat)
	}
}

// TestRunnerSequentialReuse verifies that the same *Runner can be called
// with RunTask twice in sequence (different goals, different ForceClass
// values) and both calls complete successfully.
func TestRunnerSequentialReuse(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		ProviderCaps: schema.ProviderCapabilities{JSONMode: true},
		Responses: []string{
			// First RunTask
			`{"rationale":"first","action":{"type":"final","content":"first done"}}`,
			// Second RunTask
			`{"rationale":"second","action":{"type":"final","content":"second done"}}`,
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	r.SetForceClass("question")
	if _, err := r.RunTask(context.Background(), "first goal"); err != nil {
		t.Fatalf("first RunTask err = %v", err)
	}

	r.SetForceClass("edit")
	if _, err := r.RunTask(context.Background(), "second goal"); err != nil {
		t.Fatalf("second RunTask err = %v", err)
	}

	// Both completed without error — the second call did not inherit
	// per-run state from the first.
	if len(p.Requests) < 2 {
		t.Fatalf("expected at least 2 chat requests, got %d", len(p.Requests))
	}
}

func TestPersistentMalformedOutputSalvagesWhenWorkExists(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "contents"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	read := func(p string) string {
		return fmt.Sprintf(`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":%q}}}`, p)
	}
	p := &agenttest.ScriptedProvider{Responses: []string{
		read("a.go"),
		read("b.go"),
		"garbage 1",
		"garbage 2",
		"garbage 3",
		`{"rationale":"salvaged","action":{"type":"final","content":"Salvaged from partial work."}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 10
	r.MaxRetries = 0

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	task, err := r.RunTask(context.Background(), "inspect files")
	if err != nil {
		t.Fatalf("RunTask err = %v, want nil (salvaged)", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("task.Status = %q, want completed", task.Status)
	}
	if task.SalvagedReason != "malformed" {
		t.Fatalf("SalvagedReason = %q, want %q", task.SalvagedReason, "malformed")
	}
	if task.Summary != "Salvaged from partial work." {
		t.Fatalf("Summary = %q, want salvaged content", task.Summary)
	}
	if got == nil {
		t.Fatal("no TurnMetrics emitted")
	}
	if got.ParseFailures != 3 {
		t.Fatalf("ParseFailures = %d, want 3", got.ParseFailures)
	}
	if got.SalvageReason != "malformed" {
		t.Fatalf("SalvageReason = %q, want malformed", got.SalvageReason)
	}
}

func TestPersistentMalformedOutputFailsFastWithoutWork(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"not json at all"}}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 10
	r.MaxRetries = 0

	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }

	_, err := r.RunTask(context.Background(), "test goal")
	if !errors.Is(err, ErrModelOutputMalformed) {
		t.Fatalf("err = %v, want ErrModelOutputMalformed", err)
	}
	if got != nil && got.Iterations != 0 {
		t.Fatalf("Iterations = %d, want 0 (no valid iterations consumed)", got.Iterations)
	}
	if got != nil && got.ParseFailures != 3 {
		t.Fatalf("ParseFailures = %d, want 3 (hit maxConsecutiveParseFailures)", got.ParseFailures)
	}
	if p.Calls != 3 {
		t.Fatalf("provider calls = %d, want 3 (fail fast, did not exhaust budget)", p.Calls)
	}
}

// scriptRepeats returns n copies of the same tool_call action envelope.
func TestHardStallAsksUserAndContinuesOnGuidance(t *testing.T) {
	toolResp := `{"rationale":"look","action":{"type":"tool_call","tool":"echo.tool","args":{}}}`
	responses := scriptRepeats(repeatHardStall, toolResp)
	responses = append(responses, `{"rationale":"done","action":{"type":"final","content":"finished after guidance"}}`)
	p := &agenttest.ScriptedProvider{Responses: responses}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "echo.tool", Description: "static output", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "same output"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassQuestion))

	go func() {
		for {
			if q := state.PendingQuestion(); q != nil && len(q.Questions) > 0 {
				canonical := q.Questions[0].Question
				q.ResponseChan <- []session.Answer{{Question: canonical, Answer: "try reading the config file instead"}}
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	task, err := runner.RunTask(context.Background(), "investigate")
	if err != nil {
		t.Fatalf("RunTask returned error: %v", err)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("task was salvaged (%s); guidance should have continued the loop", task.SalvagedReason)
	}
	if task.Summary != "finished after guidance" {
		t.Fatalf("task.Summary = %q", task.Summary)
	}
	// The guidance must reach the model as a user message.
	last := p.Requests[len(p.Requests)-1]
	found := false
	for _, m := range last.Messages {
		if m.Role == schema.RoleUser && strings.Contains(m.Content, "try reading the config file instead") {
			found = true
		}
	}
	if !found {
		t.Fatal("user guidance was not injected into the conversation")
	}
}

func TestHardStallFinalizesWhenUserDeclines(t *testing.T) {
	toolResp := `{"rationale":"look","action":{"type":"tool_call","tool":"echo.tool","args":{}}}`
	responses := scriptRepeats(repeatHardStall, toolResp)
	responses = append(responses, `{"rationale":"stopping","action":{"type":"final","content":"summary of progress"}}`)
	p := &agenttest.ScriptedProvider{Responses: responses}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "echo.tool", Description: "static output", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "same output"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassQuestion))

	go func() {
		for {
			if q := state.PendingQuestion(); q != nil && len(q.Questions) > 0 {
				canonical := q.Questions[0].Question
				q.ResponseChan <- []session.Answer{{Question: canonical, Answer: ""}} // decline
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	task, err := runner.RunTask(context.Background(), "investigate")
	if err != nil {
		t.Fatalf("RunTask returned error: %v", err)
	}
	if task.SalvagedReason != string(reasonStalled) {
		t.Fatalf("SalvagedReason = %q, want %q", task.SalvagedReason, reasonStalled)
	}
}

func TestHardStallAutoFinalizesForNonGeneralRole(t *testing.T) {
	toolResp := `{"rationale":"look","action":{"type":"tool_call","tool":"echo.tool","args":{}}}`
	responses := scriptRepeats(repeatHardStall, toolResp)
	responses = append(responses, `{"rationale":"stopping","action":{"type":"final","content":"scout findings"}}`)
	p := &agenttest.ScriptedProvider{Responses: responses}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "echo.tool", Description: "static output", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "same output"}, nil
		},
	})
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.Role = RoleRepoScout
	runner.SetForceClass(string(ClassQuestion))

	task, err := runner.RunTask(context.Background(), "scout the repo")
	if err != nil {
		t.Fatalf("RunTask returned error: %v", err)
	}
	if task.SalvagedReason != string(reasonStalled) {
		t.Fatalf("SalvagedReason = %q, want stalled auto-finalize for swarm role", task.SalvagedReason)
	}
}

// Some providers (Kimi's kimi-for-coding, confirmed live) reject the entire
// next request with HTTP 400 "message ... with role 'assistant' must not be
// empty" if the conversation ever contains an assistant message with empty
// content. An extended-thinking model in JSON tool-calling mode can return
// an empty res.Text on its first attempt (all output goes to the reasoning
// channel), which fails ParseAction (jsonextract finds nothing) and used to
// get appended verbatim as an empty assistant message — poisoning the very
// next request in the same turn. The native path already guards against
// this (see the empty-response branch above); the JSON path must too.
func TestEmptyResponseNeverAppendsEmptyAssistantMessage(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			"",
			`{"rationale":"done","action":{"type":"final","content":"Answer."}}`,
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	task, err := r.RunTask(context.Background(), "test goal")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("task.Status = %q, want completed", task.Status)
	}
	if len(p.Requests) != 2 {
		t.Fatalf("provider Requests = %d, want 2", len(p.Requests))
	}
	for _, msg := range p.Requests[1].Messages {
		if msg.Role == schema.RoleAssistant && strings.TrimSpace(msg.Content) == "" {
			t.Fatalf("second request contains an empty assistant message: %+v", p.Requests[1].Messages)
		}
	}
}
