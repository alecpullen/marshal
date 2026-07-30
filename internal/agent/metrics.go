package agent

import (
	"time"

	"marshal/internal/llm/pricing"
	"marshal/internal/llm/schema"
)

// TurnMetrics summarises one RunTask execution. It is emitted exactly once
// per turn via Runner.MetricsObserver, including on error exits, so every
// turn is measurable: outcome, iterations, parse failures, stalls, tokens.
type TurnMetrics struct {
	StartedAt  time.Time
	DurationMs int64
	Goal       string
	Class      string
	Role       string
	Provider   string
	Model      string
	Iterations int
	ToolCalls  int
	ToolErrors int
	CacheHits  int
	// ParseFailures counts unparseable JSON-envelope actions.
	// In native tool-calling mode this is always 0 because the provider
	// returns parsed tool_calls directly.
	ParseFailures int
	// StreamRecoveries counts turns continued from a partial response after
	// the provider stream failed mid-flight (e.g. an undecodable SSE chunk).
	// A non-zero value means the turn survived an error that used to end it.
	StreamRecoveries int
	HardStalls       int
	Outcome          string
	SalvageReason    string // non-empty when Outcome is "salvaged": "exhausted", "stalled", or "malformed"
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
	CacheReadTokens  int
	CacheWriteTokens int
	// EstimatedCostCents is the estimated cost in hundredths of a cent
	// (1/10000 of a dollar), computed from the token counts and the
	// pricing table at metrics-emission time. 0 for local/unpriced models.
	EstimatedCostCents int64
}

// turnStats is the mutable per-turn collector behind TurnMetrics. It has no
// mutex of its own: Runner guards every access with statsMu (mirroring the
// tracker/trackerMu pattern), because executeActions mutates counters from
// worker goroutines.
type turnStats struct {
	m TurnMetrics
}

// outcomeFor maps a finished task to the metrics outcome vocabulary. Any
// status other than completed (failed, or executing after an interrupt)
// counts as failed.
func outcomeFor(task *Task) string {
	switch {
	case task.Status == TaskStatusCompleted && task.SalvagedReason == "":
		return "answered"
	case task.Status == TaskStatusCompleted:
		return "salvaged"
	default:
		return "failed"
	}
}

// withStats runs f with the current turn's collector under statsMu. It is a
// no-op before the first RunTask (stats nil), so direct calls to chatOnce or
// executeToolCall in tests never panic.
func (r *Runner) withStats(f func(*turnStats)) {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	if r.stats != nil {
		f(r.stats)
	}
}

// countToolCall records one tool message fed back to the model.
func (r *Runner) countToolCall(errored, cached bool) {
	r.withStats(func(s *turnStats) {
		s.m.ToolCalls++
		if errored {
			s.m.ToolErrors++
		}
		if cached {
			s.m.CacheHits++
		}
	})
}

// noteInvalidArgs increments the per-round counter of tool calls rejected
// for schema violations. Cleared by RunTask at the start of each round.
func (r *Runner) noteInvalidArgs() {
	r.trackerMu.Lock()
	r.invalidArgsThisRound++
	r.trackerMu.Unlock()
}

// invalidArgsCount returns the count of schema-violation rejections for
// the current executeNativeToolCalls pass.
func (r *Runner) invalidArgsCount() int {
	r.trackerMu.Lock()
	defer r.trackerMu.Unlock()
	return r.invalidArgsThisRound
}

// emitMetrics finalizes the turn's metrics from the finished task and hands
// them to MetricsObserver. Called exactly once per RunTask via defer.
func (r *Runner) emitMetrics(task *Task) {
	if r.MetricsObserver == nil {
		return
	}
	r.statsMu.Lock()
	m := r.stats.m
	r.statsMu.Unlock()
	m.DurationMs = r.Now().Sub(m.StartedAt).Milliseconds()
	m.Class = string(task.Class)
	m.Outcome = outcomeFor(task)
	m.SalvageReason = task.SalvagedReason
	m.EstimatedCostCents = pricing.EstimateCostCents(schema.TokenUsage{
		PromptTokens:     m.PromptTokens,
		CompletionTokens: m.CompletionTokens,
		ReasoningTokens:  m.ReasoningTokens,
		CacheReadTokens:  m.CacheReadTokens,
		CacheWriteTokens: m.CacheWriteTokens,
	}, r.Pricing)
	r.MetricsObserver(m)
}
