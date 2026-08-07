package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
)

// base is an arbitrary fixed clock; all timings are relative to it.
var etaBase = time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)

// progressWith builds a run where the first len(done) tasks took the given
// durations, the next task started currentElapsed ago, and the rest are
// pending.
func progressWith(total int, done []time.Duration, currentElapsed time.Duration) (session.SDDProgress, time.Time) {
	timings := make([]session.TaskTiming, total)
	at := etaBase
	for i, d := range done {
		timings[i] = session.TaskTiming{StartedAt: at, EndedAt: at.Add(d)}
		at = at.Add(d)
	}
	now := at
	if len(done) < total {
		timings[len(done)] = session.TaskTiming{StartedAt: at}
		now = at.Add(currentElapsed)
	}
	return session.SDDProgress{
		TotalTasks:  total,
		DoneTasks:   len(done),
		CurrentTask: len(done) + 1,
		TaskTimings: timings,
	}, now
}

// The spec's worked example, used verbatim as a test vector.
func TestEstimateMatchesSpecWorkedExample(t *testing.T) {
	p, now := progressWith(7,
		[]time.Duration{122 * time.Second, 340 * time.Second, 401 * time.Second},
		134*time.Second)

	low, high, openEnded, ok := estimateRemaining(p, now)
	if !ok {
		t.Fatal("3 completed tasks must be enough to estimate")
	}
	if openEnded {
		t.Error("currentElapsed (134s) is below longest (401s); must not be open-ended")
	}
	if want := 366 * time.Second; low != want {
		t.Errorf("low = %v, want %v", low, want)
	}
	if want := 1470 * time.Second; high != want {
		t.Errorf("high = %v, want %v", high, want)
	}
	if got := formatETA(low, high, openEnded); got != "~6–25m left" {
		t.Errorf("formatETA = %q, want %q", got, "~6–25m left")
	}
}

func TestEstimateNeedsThreeSamples(t *testing.T) {
	for n := range 3 {
		done := make([]time.Duration, n)
		for i := range done {
			done[i] = 2 * time.Minute
		}
		p, now := progressWith(7, done, 30*time.Second)
		if _, _, _, ok := estimateRemaining(p, now); ok {
			t.Errorf("with %d completed tasks the estimate must be withheld", n)
		}
	}
	p, now := progressWith(7, []time.Duration{2 * time.Minute, 2 * time.Minute, 2 * time.Minute}, 30*time.Second)
	if _, _, _, ok := estimateRemaining(p, now); !ok {
		t.Error("3 completed tasks must produce an estimate")
	}
}

func TestEstimateWithheldWhenNothingRemains(t *testing.T) {
	p, now := progressWith(3, []time.Duration{time.Minute, time.Minute, time.Minute}, 0)
	if _, _, _, ok := estimateRemaining(p, now); ok {
		t.Error("a run with no remaining tasks must not estimate")
	}
}

func TestEstimateEqualDurationsCollapseToAPoint(t *testing.T) {
	p, now := progressWith(7,
		[]time.Duration{4 * time.Minute, 4 * time.Minute, 4 * time.Minute}, 0)

	low, high, openEnded, ok := estimateRemaining(p, now)
	if !ok {
		t.Fatal("want an estimate")
	}
	if low != high {
		t.Errorf("equal samples must give equal bounds, got %v and %v", low, high)
	}
	if got := formatETA(low, high, openEnded); got != "~16m left" {
		t.Errorf("formatETA = %q, want a point estimate ~16m left", got)
	}
}

func TestEstimateWidensWithAnOutlier(t *testing.T) {
	tight, nowT := progressWith(7, []time.Duration{4 * time.Minute, 4 * time.Minute, 4 * time.Minute}, 0)
	lowT, highT, _, _ := estimateRemaining(tight, nowT)

	wide, nowW := progressWith(7, []time.Duration{4 * time.Minute, 4 * time.Minute, 30 * time.Minute}, 0)
	lowW, highW, _, _ := estimateRemaining(wide, nowW)

	if (highW - lowW) <= (highT - lowT) {
		t.Error("an outlier task must widen the range — that is the whole point")
	}
	if highW <= highT {
		t.Error("the upper bound must reflect the outlier")
	}
}

func TestEstimateOpenEndedWhenCurrentTaskOverruns(t *testing.T) {
	p, now := progressWith(7,
		[]time.Duration{2 * time.Minute, 3 * time.Minute, 4 * time.Minute},
		20*time.Minute) // current task already beats longest (4m)

	low, high, openEnded, ok := estimateRemaining(p, now)
	if !ok {
		t.Fatal("want an estimate")
	}
	if !openEnded {
		t.Error("a current task past the longest observed cannot be bounded")
	}
	got := formatETA(low, high, openEnded)
	if !strings.Contains(got, "+") {
		t.Errorf("formatETA = %q, want an open-ended form", got)
	}
}

func TestEstimateCurrentTaskReducesLowBound(t *testing.T) {
	early, nowE := progressWith(7, []time.Duration{4 * time.Minute, 4 * time.Minute, 4 * time.Minute}, 0)
	lowE, _, _, _ := estimateRemaining(early, nowE)

	late, nowL := progressWith(7, []time.Duration{4 * time.Minute, 4 * time.Minute, 4 * time.Minute}, 3*time.Minute)
	lowL, _, _, _ := estimateRemaining(late, nowL)

	if lowL >= lowE {
		t.Error("progress within the current task must reduce the lower bound")
	}
}

func TestEstimateIgnoresDegenerateTimings(t *testing.T) {
	// Zero-length and reversed timings are not samples.
	timings := []session.TaskTiming{
		{StartedAt: etaBase, EndedAt: etaBase},                      // zero length
		{StartedAt: etaBase.Add(time.Minute), EndedAt: etaBase},     // reversed
		{StartedAt: etaBase, EndedAt: etaBase.Add(2 * time.Minute)}, // valid
		{}, // pending
	}
	p := session.SDDProgress{TotalTasks: 4, DoneTasks: 3, CurrentTask: 4, TaskTimings: timings}
	if _, _, _, ok := estimateRemaining(p, etaBase.Add(time.Hour)); ok {
		t.Error("only 1 usable sample; the estimate must be withheld")
	}
}

func TestFormatETABoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		low, high time.Duration
		openEnded bool
		want      string
	}{
		{"sub-minute floors to 1m", 10 * time.Second, 10 * time.Second, false, "~1m left"},
		{"59s still 1m", 59 * time.Second, 59 * time.Second, false, "~1m left"},
		{"exact minute", time.Minute, time.Minute, false, "~1m left"},
		{"minutes range", 6 * time.Minute, 25 * time.Minute, false, "~6–25m left"},
		{"59m point", 59 * time.Minute, 59 * time.Minute, false, "~59m left"},
		{"crosses an hour", 50 * time.Minute, 75 * time.Minute, false, "~50m–1h 15m left"},
		{"whole hours", 2 * time.Hour, 2 * time.Hour, false, "~2h left"},
		{"ceiling", 30 * time.Minute, 5 * time.Hour, false, "~4h+ left"},
		{"open ended", 12 * time.Minute, 40 * time.Minute, true, "~12m+ left"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatETA(tc.low, tc.high, tc.openEnded); got != tc.want {
				t.Errorf("formatETA(%v, %v, %v) = %q, want %q", tc.low, tc.high, tc.openEnded, got, tc.want)
			}
		})
	}
}

func TestFormatPercentRounds(t *testing.T) {
	for _, tc := range []struct {
		done, total int
		want        string
	}{
		{0, 7, "0%"},
		{3, 7, "43%"}, // 42.857 — truncation would give 42%
		{4, 7, "57%"}, // 57.142
		{1, 3, "33%"},
		{2, 3, "67%"}, // 66.667 — truncation would give 66%
		{7, 7, "100%"},
		{1, 0, "0%"}, // guard against divide-by-zero
	} {
		if got := formatPercent(tc.done, tc.total); got != tc.want {
			t.Errorf("formatPercent(%d, %d) = %q, want %q", tc.done, tc.total, got, tc.want)
		}
	}
}
