package sessionsheet

import (
	"strings"
	"time"

	"marshal/internal/db"
)

// sparkRunes are the eight block heights, lowest first.
var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// Sparkline renders values as block runes, oldest first. When there are
// more values than columns the oldest are dropped — the recent tail is
// what matters. A flat series renders at the lowest block.
func Sparkline(values []int, width int) string {
	if len(values) == 0 || width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	lo, hi := values[0], values[0]
	for _, v := range values {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	var b strings.Builder
	for _, v := range values {
		idx := 0
		if hi > lo {
			idx = (v - lo) * (len(sparkRunes) - 1) / (hi - lo)
		}
		b.WriteRune(sparkRunes[idx])
	}
	return b.String()
}

// TelemetryStats is the derived per-turn summary the context section
// renders below the composition breakdown.
type TelemetryStats struct {
	AvgTokens  int
	BurnPerMin int
	// PromptCachePct is the share of prompt tokens the provider served
	// from its prompt cache across these turns. 0 when none were reported.
	// The previous metric counted turns with a tool-result cache hit,
	// which read as a prompt-cache rate next to token stats.
	PromptCachePct int
	Series         []int // oldest first
}

// Telemetry derives per-turn statistics from persisted turn metrics.
// turns is most-recent-first, as db.RecentTurnMetrics returns it.
func Telemetry(turns []db.TurnMetricsRow, now time.Time) TelemetryStats {
	if len(turns) == 0 {
		return TelemetryStats{Series: []int{}}
	}

	series := make([]int, len(turns))
	total, prompt, cached := 0, 0, 0
	for i, t := range turns {
		n := t.PromptTokens + t.CompletionTokens
		total += n
		prompt += t.PromptTokens
		cached += t.CacheReadTokens
		// Reverse into oldest-first order for left-to-right rendering.
		series[len(turns)-1-i] = n
	}

	stats := TelemetryStats{
		AvgTokens: total / len(turns),
		Series:    series,
	}
	if prompt > 0 {
		// Clamp: a provider whose prompt_tokens excludes cached tokens
		// would otherwise read above 100%.
		stats.PromptCachePct = min(cached*100/prompt, 100)
	}

	oldest := turns[len(turns)-1].StartedAt
	if elapsed := now.Sub(oldest); elapsed >= time.Minute {
		stats.BurnPerMin = int(float64(total) / elapsed.Minutes())
	}
	return stats
}
