package session

import (
	"slices"
	"sort"
	"strings"
)

// Preflight timings are the hidden work between the user's prompt and the
// first provider request. The agent runner measures each turn and records it
// here, and the postmortem report reads it back at session end.
//
// The collector is in-memory only: nothing persists it, and a resumed process
// starts with an empty history. Detailed samples are a bounded ring, while the
// per-phase aggregates below are accumulated at record time so evicting a
// sample never changes them. The bounds are deliberately far below what the
// postmortem section can show, so eviction trims detail rather than hiding a
// turn the report could have used.
const (
	maxPreflightSamples   = 64
	maxPhasesPerPreflight = 16
	maxPhaseNameLen       = 32
)

// PhaseTiming is one measured slice of a turn's preflight.
type PhaseTiming struct {
	Name       string
	DurationMs int64
}

// PreflightSample is one turn's detailed preflight timing.
type PreflightSample struct {
	// Turn is the 1-based ordinal of the measured turn in this process, not a
	// transcript turn number. A resumed process starts again at 1.
	Turn          int
	PromptReadyMs int64
	Phases        []PhaseTiming
}

// PhaseAggregate is the session-wide aggregate for one preflight phase.
// MaxTurn pairs the maximum with the sample it came from, so a slow phase can
// be traced back to a turn even after that sample was evicted.
type PhaseAggregate struct {
	Phase   string
	Count   int
	TotalMs int64
	MaxMs   int64
	MaxTurn int
}

// PreflightStats is the session-wide view of preflight measurement.
type PreflightStats struct {
	// Turns is how many preflights were measured in this process.
	Turns int
	// Dropped is how many detailed samples the bound evicted. The aggregates
	// still include them.
	Dropped int
	Phases  []PhaseAggregate
}

// RecordPreflightTiming appends one turn's measured preflight. The agent
// runner calls it once per turn, after the prompt is complete and before the
// first provider request.
//
// Best effort by contract: it never fails and never blocks the turn beyond the
// state lock. Negative durations are clamped to zero, an empty phase name is
// skipped, and phase names are length-bounded so a future caller cannot grow
// the report with unbounded labels.
func (s *State) RecordPreflightTiming(promptReadyMs int64, phases []PhaseTiming) {
	if s == nil {
		return
	}
	if promptReadyMs < 0 {
		promptReadyMs = 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.preflightTurns++
	turn := s.preflightTurns

	if s.preflightAgg == nil {
		s.preflightAgg = make(map[string]PhaseAggregate, len(phases))
	}
	sample := PreflightSample{Turn: turn, PromptReadyMs: promptReadyMs}
	if n := min(len(phases), maxPhasesPerPreflight); n > 0 {
		sample.Phases = make([]PhaseTiming, 0, n)
	}
	for _, p := range phases {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		if len(name) > maxPhaseNameLen {
			name = name[:maxPhaseNameLen]
		}
		dur := p.DurationMs
		if dur < 0 {
			dur = 0
		}
		if len(sample.Phases) < maxPhasesPerPreflight {
			sample.Phases = append(sample.Phases, PhaseTiming{Name: name, DurationMs: dur})
		}

		agg := s.preflightAgg[name]
		agg.Phase = name
		agg.Count++
		agg.TotalMs += dur
		if agg.Count == 1 || dur > agg.MaxMs {
			agg.MaxMs, agg.MaxTurn = dur, turn
		}
		s.preflightAgg[name] = agg
	}

	if len(s.preflightSamples) < maxPreflightSamples {
		s.preflightSamples = append(s.preflightSamples, sample)
		return
	}
	// Full: drop the oldest sample so the ring always holds the most recent
	// turns. Aggregates are untouched by eviction.
	copy(s.preflightSamples, s.preflightSamples[1:])
	s.preflightSamples[len(s.preflightSamples)-1] = sample
	s.preflightDropped++
}

// PreflightDiagnostics returns a consistent snapshot of the collector: the
// session-wide aggregates and the retained detailed samples, oldest first.
// Both are copies, so a caller cannot mutate session state.
//
// A zero-valued State (or one that measured nothing) returns empty values,
// which the postmortem reports as unavailable coverage rather than zero
// latency.
func (s *State) PreflightDiagnostics() (PreflightStats, []PreflightSample) {
	if s == nil {
		return PreflightStats{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	stats := PreflightStats{
		Turns:   s.preflightTurns,
		Dropped: s.preflightDropped,
		Phases:  make([]PhaseAggregate, 0, len(s.preflightAgg)),
	}
	for _, agg := range s.preflightAgg {
		stats.Phases = append(stats.Phases, agg)
	}
	// Phase order is fixed for a stable report: the aggregate set is small and
	// a reader groups by phase name, so name order beats map iteration order.
	sort.Slice(stats.Phases, func(i, j int) bool {
		return stats.Phases[i].Phase < stats.Phases[j].Phase
	})

	samples := make([]PreflightSample, len(s.preflightSamples))
	for i, sample := range s.preflightSamples {
		cp := sample
		cp.Phases = slices.Clone(sample.Phases)
		samples[i] = cp
	}
	return stats, samples
}
