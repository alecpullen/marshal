package session

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
)

func preflightTestState() *State {
	return New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
}

// TestPreflightCollectorBoundsSamplesKeepsAggregates pins the two halves of the
// collector's contract: detail is a bounded ring, while the aggregates stay
// complete for the session. A reader who sees Dropped must still be able to
// trust the per-phase totals.
func TestPreflightCollectorBoundsSamplesKeepsAggregates(t *testing.T) {
	s := preflightTestState()
	total := maxPreflightSamples + 3
	var wantTotal int64
	for i := 1; i <= total; i++ {
		s.RecordPreflightTiming(int64(i), []PhaseTiming{{Name: "route", DurationMs: int64(i)}})
		wantTotal += int64(i)
	}

	stats, samples := s.PreflightDiagnostics()
	if stats.Turns != total {
		t.Errorf("Turns = %d, want %d", stats.Turns, total)
	}
	if stats.Dropped != total-maxPreflightSamples {
		t.Errorf("Dropped = %d, want %d", stats.Dropped, total-maxPreflightSamples)
	}
	if len(samples) != maxPreflightSamples {
		t.Fatalf("retained samples = %d, want %d", len(samples), maxPreflightSamples)
	}
	if samples[0].Turn != total-maxPreflightSamples+1 {
		t.Errorf("oldest retained turn = %d, want %d", samples[0].Turn, total-maxPreflightSamples+1)
	}
	if samples[len(samples)-1].Turn != total {
		t.Errorf("newest retained turn = %d, want %d", samples[len(samples)-1].Turn, total)
	}
	if len(stats.Phases) != 1 {
		t.Fatalf("phases = %v, want one", stats.Phases)
	}
	agg := stats.Phases[0]
	if agg.Phase != "route" || agg.Count != total || agg.TotalMs != wantTotal {
		t.Errorf("aggregate = %+v, want phase route count %d total %d", agg, total, wantTotal)
	}
	if agg.MaxMs != int64(total) || agg.MaxTurn != total {
		t.Errorf("max = %d@%d, want %d@%d", agg.MaxMs, agg.MaxTurn, total, total)
	}
}

// TestPreflightAggregateSurvivesEviction proves MaxTurn stays traceable after
// the sample that produced it has been dropped from the ring.
func TestPreflightAggregateSurvivesEviction(t *testing.T) {
	s := preflightTestState()
	s.RecordPreflightTiming(999, []PhaseTiming{{Name: "snapshot", DurationMs: 999}})
	for i := 0; i < maxPreflightSamples; i++ {
		s.RecordPreflightTiming(1, []PhaseTiming{{Name: "snapshot", DurationMs: 1}})
	}

	stats, samples := s.PreflightDiagnostics()
	if stats.Dropped != 1 {
		t.Fatalf("Dropped = %d, want 1", stats.Dropped)
	}
	if samples[0].Turn != 2 {
		t.Fatalf("oldest retained turn = %d, want 2", samples[0].Turn)
	}
	if len(stats.Phases) != 1 {
		t.Fatalf("phases = %v, want one", stats.Phases)
	}
	if got := stats.Phases[0]; got.MaxMs != 999 || got.MaxTurn != 1 {
		t.Errorf("max = %d@%d, want 999@1", got.MaxMs, got.MaxTurn)
	}
}

// TestPreflightCollectorClampsAndCopies covers the input guards and the copy
// contract: a caller mutating what it read must not reach session state.
func TestPreflightCollectorClampsAndCopies(t *testing.T) {
	s := preflightTestState()
	longName := strings.Repeat("x", maxPhaseNameLen*2)
	s.RecordPreflightTiming(-5, []PhaseTiming{
		{Name: "   ", DurationMs: 5},
		{Name: "route", DurationMs: -3},
		{Name: longName, DurationMs: 2},
	})

	stats, samples := s.PreflightDiagnostics()
	if len(samples) != 1 {
		t.Fatalf("samples = %d, want 1", len(samples))
	}
	if samples[0].PromptReadyMs != 0 {
		t.Errorf("PromptReadyMs = %d, want clamped 0", samples[0].PromptReadyMs)
	}
	if len(samples[0].Phases) != 2 {
		t.Fatalf("phases = %+v, want 2", samples[0].Phases)
	}
	if got := samples[0].Phases[0]; got.Name != "route" || got.DurationMs != 0 {
		t.Errorf("first phase = %+v, want route at clamped 0", got)
	}
	if got := samples[0].Phases[1].Name; len(got) != maxPhaseNameLen {
		t.Errorf("long phase name length = %d, want %d", len(got), maxPhaseNameLen)
	}
	if len(stats.Phases) != 2 || stats.Phases[0].Phase != "route" {
		t.Fatalf("aggregates = %+v, want route first", stats.Phases)
	}

	samples[0].Phases[0].DurationMs = 99
	samples[0].PromptReadyMs = 99
	_, again := s.PreflightDiagnostics()
	if again[0].PromptReadyMs != 0 || again[0].Phases[0].DurationMs != 0 {
		t.Errorf("mutating a snapshot changed session state: %+v", again[0])
	}
}
