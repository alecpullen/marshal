package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestTruncateGoal(t *testing.T) {
	cases := []struct {
		name string
		goal string
		want string
	}{
		{name: "short goal unchanged", goal: "fix the bug", want: "fix the bug"},
		{name: "exactly 200 runes unchanged", goal: strings.Repeat("a", 200), want: strings.Repeat("a", 200)},
		{name: "long goal truncated to 200 runes", goal: strings.Repeat("a", 250), want: strings.Repeat("a", 200)},
		{
			name: "multibyte runes not split",
			goal: strings.Repeat("é", 250),
			want: strings.Repeat("é", 200),
		},
		{name: "empty goal", goal: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateGoal(tc.goal, 200)
			if got != tc.want {
				t.Fatalf("truncateGoal length = %d runes, want %d", len([]rune(got)), len([]rune(tc.want)))
			}
		})
	}
}

func TestOutcomeFor(t *testing.T) {
	cases := []struct {
		name string
		task *Task
		want string
	}{
		{
			name: "completed without salvage is answered",
			task: &Task{Status: TaskStatusCompleted},
			want: "answered",
		},
		{
			name: "completed with salvage reason is salvaged",
			task: &Task{Status: TaskStatusCompleted, SalvagedReason: "stalled"},
			want: "salvaged",
		},
		{
			name: "failed status is failed",
			task: &Task{Status: TaskStatusFailed},
			want: "failed",
		},
		{
			name: "executing (interrupted) is failed",
			task: &Task{Status: TaskStatusExecuting},
			want: "failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := outcomeFor(tc.task); got != tc.want {
				t.Fatalf("outcomeFor = %q, want %q", got, tc.want)
			}
		})
	}
}

// registerFakeRead registers a file.read fake; when cacheable is true, the
// turn cache serves repeat calls.
func registerFakeRead(t *testing.T, reg *registry.Registry, cacheable bool) {
	t.Helper()
	if err := reg.Register(registry.Tool{
		Name:      "file.read",
		Risk:      registry.RiskReadOnly,
		Cacheable: cacheable,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "package main"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

func captureMetrics(r *Runner) *TurnMetrics {
	var captured TurnMetrics
	got := &captured
	r.MetricsObserver = func(m TurnMetrics) { *got = m }
	return got
}

func TestRunTaskEmitsMetricsOnAnswer(t *testing.T) {
	reg := registry.New()
	registerFakeRead(t, reg, false)
	p := &scriptedProvider{responses: []string{
		`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
		`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"b.go"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Answer."}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	m := captureMetrics(r)

	if _, err := r.RunTask(context.Background(), "how does pkg work?"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if m.Outcome != "answered" || m.SalvageReason != "" {
		t.Fatalf("outcome = %q/%q, want answered/\"\"", m.Outcome, m.SalvageReason)
	}
	if m.Iterations != 3 || m.ToolCalls != 2 || m.ToolErrors != 0 || m.ParseFailures != 0 {
		t.Fatalf("counters = %+v, want Iterations=3 ToolCalls=2 ToolErrors=0 ParseFailures=0", *m)
	}
	if m.Class != "question" || m.Role != "general" || m.Model != "test-model" || m.Provider != "scripted" {
		t.Fatalf("identity fields = %+v", *m)
	}
	if m.Goal != "how does pkg work?" || m.StartedAt.IsZero() {
		t.Fatalf("goal/startedAt = %q / %v", m.Goal, m.StartedAt)
	}
}

func TestRunTaskMetricsCountsParseFailures(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{responses: []string{
		"this is not a json action",
		`{"rationale":"done","action":{"type":"final","content":"Recovered."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	m := captureMetrics(r)

	if _, err := r.RunTask(context.Background(), "question"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if m.Outcome != "answered" || m.ParseFailures != 1 || m.ToolCalls != 0 {
		t.Fatalf("metrics = %+v, want answered with ParseFailures=1 ToolCalls=0", *m)
	}
}

func TestRunTaskMetricsCountsToolErrorsAndCacheHits(t *testing.T) {
	reg := registry.New()
	registerFakeRead(t, reg, true) // cacheable: second identical read is a cache hit
	p := &scriptedProvider{responses: []string{
		`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
		`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
		`{"rationale":"r","action":{"type":"tool_call","tool":"missing.tool","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	m := captureMetrics(r)

	if _, err := r.RunTask(context.Background(), "question"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if m.ToolCalls != 3 || m.CacheHits != 1 || m.ToolErrors != 1 {
		t.Fatalf("metrics = %+v, want ToolCalls=3 CacheHits=1 ToolErrors=1", *m)
	}
}

func TestRunTaskMetricsCountsStalls(t *testing.T) {
	reg := registry.New()
	registerFakeRead(t, reg, false)
	read := `{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`
	responses := make([]string, 0, repeatHardStall+1)
	for i := 0; i < repeatHardStall; i++ {
		responses = append(responses, read)
	}
	responses = append(responses, `{"rationale":"done","action":{"type":"final","content":"Forced."}}`)
	p := &scriptedProvider{responses: responses}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.Role = RoleRepoScout
	r.SetForceClass(string(ClassQuestion))
	m := captureMetrics(r)

	if _, err := r.RunTask(context.Background(), "question"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if m.Outcome != "salvaged" || m.SalvageReason != "stalled" || m.HardStalls != 1 {
		t.Fatalf("metrics = %+v, want salvaged/stalled with HardStalls=1", *m)
	}
}

func TestRunTaskMetricsFailedOnProviderError(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{errs: []error{errors.New("boom")}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 0
	r.SetForceClass(string(ClassQuestion))
	m := captureMetrics(r)

	if _, err := r.RunTask(context.Background(), "question"); err == nil {
		t.Fatal("RunTask err = nil, want provider failure")
	}
	if m.Outcome != "failed" {
		t.Fatalf("Outcome = %q, want failed (metrics must emit on error exits)", m.Outcome)
	}
}

func TestRunTaskMetricsAccumulatesTokens(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{
		responses: []string{
			"garbage that fails to parse",
			`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
		},
		usages: []*schema.TokenUsage{
			{PromptTokens: 10, CompletionTokens: 5},
			{PromptTokens: 7, CompletionTokens: 3},
		},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	m := captureMetrics(r)

	if _, err := r.RunTask(context.Background(), "question"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if m.PromptTokens != 17 || m.CompletionTokens != 8 {
		t.Fatalf("tokens = %d/%d, want 17/8 accumulated across calls", m.PromptTokens, m.CompletionTokens)
	}
}
