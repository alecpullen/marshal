package tui

import (
	"fmt"
	"math"
	"time"

	"marshal/internal/app/session"
)

// minETASamples is how many tasks must complete before a remaining-time
// estimate is shown at all. Below this the run has not demonstrated a pace,
// and an estimate would be a guess dressed as a measurement.
const minETASamples = 3

// etaCeilingMinutes is where the estimate stops being useful and becomes an
// open-ended "a long time".
const etaCeilingMinutes = 240

// estimateRemaining bounds the time left in a run using only what the run
// has actually done: the shortest and longest completed task set the
// optimistic and pessimistic pace for the tasks not yet started, and the
// in-flight task is credited for the time it has already spent.
//
// There are no tuning constants. The range widens on its own when a task
// burns fix rounds, so a run behaving unpredictably looks uncertain rather
// than confidently wrong.
//
// openEnded reports that the current task has already outrun every
// completed one, so its remaining time cannot be bounded at all; callers
// render an "at least" form. ok is false when there is nothing to estimate.
func estimateRemaining(p session.SDDProgress, now time.Time) (low, high time.Duration, openEnded, ok bool) {
	var shortest, longest time.Duration
	samples := 0
	for _, t := range p.TaskTimings {
		if t.StartedAt.IsZero() || t.EndedAt.IsZero() {
			continue
		}
		d := t.EndedAt.Sub(t.StartedAt)
		if d <= 0 {
			continue // a zero-length or reversed timing is not a measurement
		}
		if samples == 0 || d < shortest {
			shortest = d
		}
		if d > longest {
			longest = d
		}
		samples++
	}
	if samples < minETASamples {
		return 0, 0, false, false
	}
	remaining := p.TotalTasks - p.DoneTasks
	if remaining <= 0 {
		return 0, 0, false, false
	}

	elapsed := currentTaskElapsed(p, now)
	rest := time.Duration(remaining - 1)
	low = remainderOf(shortest, elapsed) + rest*shortest
	high = remainderOf(longest, elapsed) + rest*longest
	return low, high, elapsed > longest, true
}

// remainderOf returns how much of an expected task duration is left after
// elapsed, floored at zero — a task that has already run past its expected
// duration contributes nothing rather than a negative.
func remainderOf(expected, elapsed time.Duration) time.Duration {
	if d := expected - elapsed; d > 0 {
		return d
	}
	return 0
}

// currentTaskElapsed returns how long the in-flight task has been running,
// or zero when there is no in-flight task or it has no start stamp.
func currentTaskElapsed(p session.SDDProgress, now time.Time) time.Duration {
	i := p.CurrentTask - 1
	if i < 0 || i >= len(p.TaskTimings) {
		return 0
	}
	started := p.TaskTimings[i].StartedAt
	if started.IsZero() {
		return 0
	}
	if d := now.Sub(started); d > 0 {
		return d
	}
	return 0
}

// formatETA renders a bounded estimate. formatElapsed is deliberately not
// reused: it renders seconds, which is false precision for a forecast.
func formatETA(low, high time.Duration, openEnded bool) string {
	lo, hi := etaMinutes(low), etaMinutes(high)
	if openEnded {
		return "~" + etaLabel(lo) + "+ left"
	}
	if hi >= etaCeilingMinutes {
		return fmt.Sprintf("~%dh+ left", etaCeilingMinutes/60)
	}
	if lo == hi {
		return "~" + etaLabel(lo) + " left"
	}
	// Below an hour both bounds share the unit, so it is stated once.
	if hi < 60 {
		return fmt.Sprintf("~%d–%dm left", lo, hi)
	}
	return fmt.Sprintf("~%s–%s left", etaLabel(lo), etaLabel(hi))
}

// etaMinutes rounds a duration to whole minutes, and to 5-minute steps once
// past an hour, where minute precision is noise.
func etaMinutes(d time.Duration) int {
	m := int(math.Round(d.Minutes()))
	if m < 1 {
		m = 1
	}
	if m >= 60 {
		m = int(math.Round(float64(m)/5)) * 5
	}
	return m
}

// etaLabel renders whole minutes as "45m", "2h", or "1h 15m".
func etaLabel(mins int) string {
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	h, m := mins/60, mins%60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// formatPercent renders done/total as a rounded percentage. Integer
// division truncates — 3 of 7 becomes 42% instead of 43% — which is wrong
// by a point on most fractions and reads as an off-by-one bug.
func formatPercent(done, total int) string {
	if total <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%d%%", int(math.Round(float64(done)/float64(total)*100)))
}
