package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// fixedStepClock advances a fixed amount per read, so every span has a known
// duration and the recorder can be asserted without sleeping.
func fixedStepClock(step time.Duration) func() time.Time {
	cur := time.Unix(1700000000, 0).UTC()
	return func() time.Time {
		out := cur
		cur = cur.Add(step)
		return out
	}
}

func TestPhaseClockRecordsSpansInOrder(t *testing.T) {
	now := fixedStepClock(10 * time.Millisecond)
	start := now()
	pc := newPhaseClock(now, start)

	pc.span("first", func() {})
	pc.begin("second")
	pc.end()

	got := pc.phases()
	if len(got) != 2 {
		t.Fatalf("recorded %d phases, want 2: %+v", len(got), got)
	}
	if got[0].Name != "first" || got[1].Name != "second" {
		t.Fatalf("phase names = %q, %q; want first, second", got[0].Name, got[1].Name)
	}
	for _, p := range got {
		if p.DurationMs != 10 {
			t.Fatalf("phase %q duration = %dms, want 10ms", p.Name, p.DurationMs)
		}
	}
}

// A span opened while another is open must not silently swallow time: the
// inner begin is ignored rather than replacing the outer span.
func TestPhaseClockIgnoresNestedBegin(t *testing.T) {
	now := fixedStepClock(10 * time.Millisecond)
	pc := newPhaseClock(now, now())
	pc.begin("outer")
	pc.begin("inner")
	pc.end()

	got := pc.phases()
	if len(got) != 1 || got[0].Name != "outer" {
		t.Fatalf("phases = %+v, want the single outer span", got)
	}
}

// A sub-millisecond phase must still appear: "ran and was fast" has to stay
// distinguishable from "did not run".
func TestPhaseClockStringKeepsSubMillisecondPhases(t *testing.T) {
	frozen := time.Unix(1700000000, 0).UTC()
	pc := newPhaseClock(func() time.Time { return frozen }, frozen)
	pc.span("fast", func() {})

	if line := pc.String(); !strings.Contains(line, "fast=<1ms") {
		t.Fatalf("String() = %q, want fast=<1ms", line)
	}

	clk := frozen
	pc2 := newPhaseClock(func() time.Time { return clk }, clk)
	pc2.begin("pins")
	clk = clk.Add(250 * time.Millisecond)
	pc2.end()

	if line := pc2.String(); !strings.Contains(line, "pins=250ms") {
		t.Fatalf("String() = %q, want pins=250ms", line)
	}
}

// The turn must report the preflight breakdown to whichever observer persists
// metrics, in execution order, with the wall-clock total alongside it.
func TestRunTaskReportsPreflightPhases(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{`{"rationale":"r","action":{"type":"answer","content":"done"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	runner := NewRunner(p, reg, pol, newTestState(t), "test-model")

	var got TurnMetrics
	runner.MetricsObserver = func(m TurnMetrics) { got = m }

	if err := runner.Run(context.Background(), "fix the parser bug"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got.Phases) == 0 {
		t.Fatal("no preflight phases reported")
	}
	index := make(map[string]int, len(got.Phases))
	for i, p := range got.Phases {
		index[p.Name] = i
		if p.DurationMs < 0 {
			t.Fatalf("phase %q has negative duration %dms", p.Name, p.DurationMs)
		}
	}
	// The prompt-critical phases run every general turn, in this order.
	for _, name := range []string{"register", "classify", "route", "semantic", "history"} {
		if _, ok := index[name]; !ok {
			t.Fatalf("phase %q missing from %v", name, got.Phases)
		}
	}
	if index["route"] > index["history"] {
		t.Fatalf("route recorded after history: %v", got.Phases)
	}
	if got.PromptReadyMs < 0 {
		t.Fatalf("PromptReadyMs = %d, want non-negative", got.PromptReadyMs)
	}
}
