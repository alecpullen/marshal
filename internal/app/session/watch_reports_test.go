package session

import (
	"testing"
	"time"

	"marshal/internal/app/config"
)

// TestWatchReportQueueSeparateFromSteering guards C1: a background
// watch child's completion report lives in its own queue, so ClearSteering
// (turn-cancel, Ctrl+X) and PopSteering (blank-Enter follow-up) must never
// drop it.
func TestWatchReportQueueSeparateFromSteering(t *testing.T) {
	state := newTestState()
	state.PushWatchReport("[watch 1 finished] the report")
	state.PushSteering("human steering")

	// ClearSteering drops only the human steering, not the report.
	state.ClearSteering()
	if got := state.SteeringQueue(); len(got) != 0 {
		t.Fatalf("steering queue = %v, want empty after clear", got)
	}
	if got := state.WatchReports(); len(got) != 1 {
		t.Fatalf("watch report queue = %v, want the report preserved", got)
	}

	// PopSteering (blank-Enter follow-up) also leaves the report intact.
	state.PushSteering("another steer")
	if _, ok := state.PopSteering(); !ok {
		t.Fatal("PopSteering returned ok=false")
	}
	if got := state.WatchReports(); len(got) != 1 {
		t.Fatalf("watch report queue = %v, want the report preserved after PopSteering", got)
	}

	// DrainWatchReports returns and clears only the report queue.
	drained := state.DrainWatchReports()
	if len(drained) != 1 || drained[0] != "[watch 1 finished] the report" {
		t.Fatalf("DrainWatchReports = %v, want the report", drained)
	}
	if got := state.WatchReports(); len(got) != 0 {
		t.Fatalf("watch report queue = %v, want empty after drain", got)
	}
}

// TestWatchReportQueueRoundTrip guards the push/drain/peek round-trip:
// a pushed report is visible via WatchReports (peek copy), survives a
// second push, and is fully drained by DrainWatchReports.
func TestWatchReportQueueRoundTrip(t *testing.T) {
	state := newTestState()
	state.PushWatchReport("one")
	state.PushWatchReport("two")

	// Peek returns a copy, not the live slice.
	peek := state.WatchReports()
	if len(peek) != 2 || peek[0] != "one" || peek[1] != "two" {
		t.Fatalf("WatchReports() = %v, want [one two]", peek)
	}
	// Mutating the peek must not affect the queue.
	peek[0] = "mutated"
	if got := state.WatchReports(); got[0] != "one" {
		t.Fatalf("WatchReports() after mutating peek = %v, want [one two]", got)
	}

	drained := state.DrainWatchReports()
	if len(drained) != 2 || drained[0] != "one" || drained[1] != "two" {
		t.Fatalf("DrainWatchReports() = %v, want [one two]", drained)
	}
	if got := state.WatchReports(); len(got) != 0 {
		t.Fatalf("WatchReports() after drain = %v, want empty", got)
	}
}

// TestWatchReportQueueClearDiscards guards ClearWatchReports: it drops
// the queue without delivering it.
func TestWatchReportQueueClearDiscards(t *testing.T) {
	state := newTestState()
	state.PushWatchReport("stale")
	state.ClearWatchReports()
	if got := state.WatchReports(); len(got) != 0 {
		t.Fatalf("WatchReports() after clear = %v, want empty", got)
	}
}

// TestShutdownClearsWatchReports guards M-5: on Shutdown, the watch
// report queue is cleared so late reports don't end up in a garbage
// transcript.
func TestShutdownClearsWatchReports(t *testing.T) {
	state := newTestState()
	state.PushWatchReport("[watch 1 finished] stale report")

	state.Shutdown()

	if got := state.WatchReports(); len(got) != 0 {
		t.Fatalf("watch report queue after shutdown = %v, want empty", got)
	}
}

// TestWatchReportContentType guards the persisted content type value.
func TestWatchReportContentType(t *testing.T) {
	if ContentTypeWatchReport != "watch_report" {
		t.Fatalf("ContentTypeWatchReport = %q, want %q", ContentTypeWatchReport, "watch_report")
	}
}

// TestWatchReportQueueConcurrent guards the mutex: concurrent pushes and
// drains must not race or lose reports.
func TestWatchReportQueueConcurrent(t *testing.T) {
	state := newTestState()
	const n = 50
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			state.PushWatchReport("report")
		}
	}()
	for i := 0; i < n; i++ {
		state.PushWatchReport("report")
	}
	<-done
	if got := len(state.WatchReports()); got != 2*n {
		t.Fatalf("WatchReports() len = %d, want %d", got, 2*n)
	}
}

// TestWatchReportQueueUsesConfig guards that the queue is independent of
// the config (a zero config still works).
func TestWatchReportQueueUsesConfig(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	state.PushWatchReport("report")
	if got := state.WatchReports(); len(got) != 1 {
		t.Fatalf("WatchReports() = %v, want [report]", got)
	}
}
