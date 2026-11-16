package postmortem

import (
	"encoding/json"
	"sort"

	"marshal/internal/app/session"
)

// The latency section is bounded detail, not a trace: the per-phase aggregates
// are complete for the process, while the per-turn breakdown is the slowest
// retained turns. The limits are part of the report contract, so an aggregator
// can rely on them.
const (
	maxLatencyExamples = 32
	maxLatencyBytes    = 64 << 10
)

// Latency is the optional preflight-timing section of a report: the hidden
// work between the user's prompt and the first provider request, which
// produces no transcript row and therefore used to be indistinguishable from a
// slow model.
//
// It is additive and optional. A session that measured no preflight omits the
// field rather than claiming zero latency, and Source names where the records
// came from — "live" for this process's in-memory collector. A resumed process
// starts its collector empty, so its earlier turns are simply absent rather
// than reported as fast; SessionTurns is there to make that partial coverage
// visible.
type Latency struct {
	Source string `json:"source"`
	// TurnsMeasured counts the preflights recorded in this process, including
	// the ones whose detailed sample the collector's bound evicted.
	TurnsMeasured int `json:"turns_measured"`
	// SessionTurns is the transcript's user-turn count. When it exceeds
	// TurnsMeasured the sample is partial (a resumed process, or turns that ran
	// before this session's collector was attached).
	SessionTurns int `json:"session_turns"`
	// Dropped counts detailed samples evicted by the collector's bound. The
	// aggregates below still include them.
	Dropped int `json:"dropped,omitempty"`
	// Phases is the session-wide aggregate for each measured phase, ordered by
	// name so an aggregator reads a stable shape.
	Phases []LatencyPhase `json:"phases"`
	// Examples is the slowest retained turns with their full breakdown,
	// slowest first.
	Examples []LatencyExample `json:"examples"`
}

// LatencyPhase is one preflight phase's session-wide aggregate.
type LatencyPhase struct {
	Phase   string `json:"phase"`
	Count   int    `json:"count"`
	TotalMs int64  `json:"total_ms"`
	MaxMs   int64  `json:"max_ms"`
	// MaxTurn identifies the turn the maximum came from, even after its
	// detailed sample was evicted.
	MaxTurn int `json:"max_turn,omitempty"`
}

// LatencyExample is one retained turn's detailed preflight breakdown.
type LatencyExample struct {
	Turn          int   `json:"turn"`
	PromptReadyMs int64 `json:"prompt_ready_ms"`
	// UnattributedMs is the part of PromptReadyMs no phase span covered. It is
	// reported rather than folded into a phase, so an unmeasured pause stays
	// visible instead of being blamed on whichever phase ran next.
	UnattributedMs int64                `json:"unattributed_ms,omitempty"`
	Phases         []LatencyPhaseTiming `json:"phases"`
}

// LatencyPhaseTiming is one phase of one turn.
type LatencyPhaseTiming struct {
	Phase      string `json:"phase"`
	DurationMs int64  `json:"duration_ms"`
}

// buildLatency renders the optional preflight section from the session's
// in-memory collector. It returns nil when nothing was measured: an absent
// section means "unavailable", never "zero latency".
func buildLatency(state *session.State, sessionTurns int) *Latency {
	stats, samples := state.PreflightDiagnostics()
	if stats.Turns == 0 {
		return nil
	}

	lat := &Latency{
		Source:        "live",
		TurnsMeasured: stats.Turns,
		SessionTurns:  sessionTurns,
		Dropped:       stats.Dropped,
		Phases:        make([]LatencyPhase, 0, len(stats.Phases)),
		Examples:      make([]LatencyExample, 0, min(len(samples), maxLatencyExamples)),
	}
	for _, agg := range stats.Phases {
		lat.Phases = append(lat.Phases, LatencyPhase{
			Phase:   agg.Phase,
			Count:   agg.Count,
			TotalMs: agg.TotalMs,
			MaxMs:   agg.MaxMs,
			MaxTurn: agg.MaxTurn,
		})
	}

	// Slowest first: that is the turn a reader chases. Ties break on the
	// earlier turn so the order is deterministic.
	sort.SliceStable(samples, func(i, j int) bool {
		if samples[i].PromptReadyMs != samples[j].PromptReadyMs {
			return samples[i].PromptReadyMs > samples[j].PromptReadyMs
		}
		return samples[i].Turn < samples[j].Turn
	})
	for _, sample := range samples {
		if len(lat.Examples) == maxLatencyExamples {
			break
		}
		ex := LatencyExample{Turn: sample.Turn, PromptReadyMs: sample.PromptReadyMs}
		var measured int64
		for _, p := range sample.Phases {
			ex.Phases = append(ex.Phases, LatencyPhaseTiming{Phase: p.Name, DurationMs: p.DurationMs})
			measured += p.DurationMs
		}
		if un := sample.PromptReadyMs - measured; un > 0 {
			ex.UnattributedMs = un
		}
		lat.Examples = append(lat.Examples, ex)
	}

	// The byte cap is a hard contract, not an assumption about how small phase
	// names are. Trimming drops the least-slow example first, so the cap costs
	// detail before it costs the headline.
	for len(lat.Examples) > 0 && latencyBytes(lat) > maxLatencyBytes {
		lat.Examples = lat.Examples[:len(lat.Examples)-1]
	}
	return lat
}

// latencyBytes is the section's serialized size. The shape is fixed and has no
// unsupported values, so an encoding failure is impossible in practice; if one
// ever happened it must shrink the report, not grow it, hence the over-budget
// answer.
func latencyBytes(lat *Latency) int {
	data, err := json.Marshal(lat)
	if err != nil {
		return maxLatencyBytes + 1
	}
	return len(data)
}
