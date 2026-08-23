package agent

import (
	"context"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestRunReclassifiesOnKeywordMiss(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{`{"rationale":"r","action":{"type":"answer","content":"done"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	runner := NewRunner(p, reg, pol, newTestState(t), "test-model")
	called := false
	runner.Classifier = func(ctx context.Context, goal string) (TaskClass, error) {
		called = true
		return ClassEdit, nil
	}
	var gotClass string
	runner.MetricsObserver = func(m TurnMetrics) { gotClass = m.Class }

	// "make" is deliberately absent from the edit keyword list — a miss.
	if err := runner.Run(context.Background(), "how do I make the parser faster?"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !called {
		t.Fatal("Classifier was not consulted on keyword miss")
	}
	if gotClass != string(ClassEdit) {
		t.Fatalf("TurnMetrics.Class = %q, want edit (reclassified)", gotClass)
	}
}

func TestRunSkipsClassifierOnKeywordHit(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{`{"rationale":"r","action":{"type":"answer","content":"done"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	runner := NewRunner(p, reg, pol, newTestState(t), "test-model")
	called := false
	runner.Classifier = func(ctx context.Context, goal string) (TaskClass, error) {
		called = true
		return ClassQuestion, nil
	}
	var gotClass string
	runner.MetricsObserver = func(m TurnMetrics) { gotClass = m.Class }

	if err := runner.Run(context.Background(), "fix the parser bug"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if called {
		t.Fatal("Classifier consulted on keyword hit — must only fire on miss")
	}
	if gotClass != string(ClassEdit) {
		t.Fatalf("TurnMetrics.Class = %q, want edit (keyword)", gotClass)
	}
}

func TestRunKeepsKeywordClassOnClassifierError(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{`{"rationale":"r","action":{"type":"answer","content":"done"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	runner := NewRunner(p, reg, pol, newTestState(t), "test-model")
	runner.Classifier = func(ctx context.Context, goal string) (TaskClass, error) {
		return "", context.DeadlineExceeded
	}
	var gotClass string
	runner.MetricsObserver = func(m TurnMetrics) { gotClass = m.Class }

	if err := runner.Run(context.Background(), "how do I make the parser faster?"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotClass != string(ClassQuestion) {
		t.Fatalf("TurnMetrics.Class = %q, want question (keyword fallback)", gotClass)
	}
}
