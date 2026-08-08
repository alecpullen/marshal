package tui

import (
	"testing"

	"marshal/internal/pipeline"
)

func TestParseSDDArgsAcceptsSeparateStrategyValue(t *testing.T) {
	got, err := parseSDDArgs([]string{"--strategy", "adaptive", "plan.md"})
	if err != nil {
		t.Fatalf("parseSDDArgs: %v", err)
	}
	if got.Strategy != pipeline.StrategyAdaptive || got.PlanPath != "plan.md" {
		t.Fatalf("parsed = %+v", got)
	}
	if !got.ExplicitStrategy {
		t.Fatal("separate strategy value must set ExplicitStrategy")
	}
}

func TestParseSDDArgsRecognizesNewFromLastPlan(t *testing.T) {
	got, err := parseSDDArgs([]string{"new", "--from-last-plan"})
	if err != nil || !got.New || !got.FromLastPlan {
		t.Fatalf("parsed = %+v, err = %v", got, err)
	}
}

func TestParseSDDArgsEqualsStrategy(t *testing.T) {
	got, err := parseSDDArgs([]string{"--strategy=strict", "plan.md"})
	if err != nil {
		t.Fatalf("parseSDDArgs: %v", err)
	}
	if got.Strategy != pipeline.StrategyStrict || !got.ExplicitStrategy {
		t.Fatalf("parsed = %+v", got)
	}
}

func TestParseSDDArgsNewGoal(t *testing.T) {
	got, err := parseSDDArgs([]string{"new", "add", "retry", "logic"})
	if err != nil {
		t.Fatalf("parseSDDArgs: %v", err)
	}
	if !got.New {
		t.Fatal("expected New")
	}
	if got.Goal != "add retry logic" {
		t.Fatalf("Goal = %q, want %q", got.Goal, "add retry logic")
	}
}

func TestParseSDDArgsUnknownStrategyErrors(t *testing.T) {
	if _, err := parseSDDArgs([]string{"--strategy", "bogus", "plan.md"}); err == nil {
		t.Fatal("unknown strategy value must error")
	}
}

func TestParseSDDArgsMissingStrategyValueErrors(t *testing.T) {
	if _, err := parseSDDArgs([]string{"--strategy"}); err == nil {
		t.Fatal("missing strategy value must error")
	}
}

func TestParseSDDArgsQuotedPathPreserved(t *testing.T) {
	got, err := parseSDDArgs([]string{"my plan.md"})
	if err != nil {
		t.Fatalf("parseSDDArgs: %v", err)
	}
	if got.PlanPath != "my plan.md" {
		t.Fatalf("PlanPath = %q, want %q", got.PlanPath, "my plan.md")
	}
}
