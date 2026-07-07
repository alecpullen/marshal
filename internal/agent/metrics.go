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
