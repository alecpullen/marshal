package agent

import "time"

// TurnMetrics summarises one RunTask execution. It is emitted exactly once
// per turn via Runner.MetricsObserver, including on error exits, so every
// turn is measurable: outcome, iterations, parse failures, stalls, tokens.
type TurnMetrics struct {
	StartedAt        time.Time
	DurationMs       int64
	Goal             string
	Class            string
	Role             string
	Provider         string
	Model            string
	Iterations       int
	ToolCalls        int
	ToolErrors       int
	CacheHits        int
	ParseFailures    int
	SoftStalls       int
	HardStalls       int
	Outcome          string
	SalvageReason    string
	PromptTokens     int
	CompletionTokens int
}

// turnStats is the mutable per-turn collector behind TurnMetrics. It has no
// mutex of its own: Runner guards every access with statsMu (mirroring the
// tracker/trackerMu pattern), because executeActions mutates counters from
// worker goroutines.
type turnStats struct {
	m TurnMetrics
}

// truncateGoal caps goal at max runes without splitting a UTF-8 rune.
func truncateGoal(goal string, max int) string {
	runes := []rune(goal)
	if len(runes) <= max {
		return goal
	}
	return string(runes[:max])
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
	r.MetricsObserver(m)
}
