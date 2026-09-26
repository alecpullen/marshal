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
	state.PushWatchReport("w1", "[watch 1 finished] the report", "", false, false)
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
	if len(drained) != 1 || drained[0].Text != "[watch 1 finished] the report" {
		t.Fatalf("DrainWatchReports = %v, want the report", drained)
	}
	if got := state.WatchReports(); len(got) != 0 {
		t.Fatalf("watch report queue = %v, want empty after drain", got)
	}
}

// TestWatchReportQueueRoundTrip guards the push/drain/peek round-trip:
// a pushed report is visible via WatchReports (peek copy), survives a
// second push from a distinct watch, and is fully drained by
// DrainWatchReports.
func TestWatchReportQueueRoundTrip(t *testing.T) {
	state := newTestState()
	state.PushWatchReport("w1", "one", "", false, false)
	state.PushWatchReport("w2", "two", "", false, false)

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
	if len(drained) != 2 || drained[0].Text != "one" || drained[1].Text != "two" {
		t.Fatalf("DrainWatchReports() = %v, want [one two]", drained)
	}
	if got := state.WatchReports(); len(got) != 0 {
		t.Fatalf("WatchReports() after drain = %v, want empty", got)
	}
}

// TestWatchReportQueueCoalescesByWatchID guards I-3: two fires from the
// same watch before a drain fold into one pending entry, with the later
// text (carrying the cumulative FiredCount) replacing the earlier one.
func TestWatchReportQueueCoalescesByWatchID(t *testing.T) {
	state := newTestState()
	state.PushWatchReport("w1", "[watch build fired] kind=command", "old", false, false)
	state.PushWatchReport("w1", "[watch build fired] kind=command (fired 2 times)", "build", true, true)

	if got := state.WatchReports(); len(got) != 1 {
		t.Fatalf("WatchReports() = %v, want one coalesced entry", got)
	}
	if got := state.WatchReports(); got[0] != "[watch build fired] kind=command (fired 2 times)" {
		t.Fatalf("coalesced entry = %q, want the later (fired 2 times) text", got[0])
	}

	drained := state.DrainWatchReports()
	if len(drained) != 1 || drained[0].Text != "[watch build fired] kind=command (fired 2 times)" {
		t.Fatalf("DrainWatchReports() = %v, want one coalesced message", drained)
	}
	// The folded entry carries the latest fire's resume facts.
	if drained[0].Name != "build" || !drained[0].Repeat || !drained[0].Resume {
		t.Fatalf("coalesced entry = %+v, want the latest fire's fields", drained[0])
	}
}

// TestWatchReportQueueDistinctWatchesDoNotFold guards I-3: reports from
// different watches stay separate even when pushed back-to-back.
func TestWatchReportQueueDistinctWatchesDoNotFold(t *testing.T) {
	state := newTestState()
	state.PushWatchReport("w1", "one", "", false, false)
	state.PushWatchReport("w2", "two", "", false, false)
	state.PushWatchReport("w1", "one again", "build", true, true)

	// w1 folds into one entry; w2 stays separate -> two entries total.
	if got := state.WatchReports(); len(got) != 2 {
		t.Fatalf("WatchReports() = %v, want two entries (w1 folded, w2 separate)", got)
	}
	drained := state.DrainWatchReports()
	if len(drained) != 2 || drained[0].Text != "one again" || drained[1].Text != "two" {
		t.Fatalf("DrainWatchReports() = %v, want [one again two]", drained)
	}
	if drained[0].Name != "build" || !drained[0].Repeat || !drained[0].Resume {
		t.Fatalf("folded w1 entry = %+v, want the latest fire's fields", drained[0])
	}
}

// TestWatchReportQueueFreshAfterDrain guards I-3: after a drain, a new
// fire for the same watch starts a fresh message rather than folding into
// the already-drained one.
func TestWatchReportQueueFreshAfterDrain(t *testing.T) {
	state := newTestState()
	state.PushWatchReport("w1", "first", "", false, false)
	state.DrainWatchReports()

	state.PushWatchReport("w1", "second", "", false, false)
	if got := state.WatchReports(); len(got) != 1 || got[0] != "second" {
		t.Fatalf("WatchReports() after drain+push = %v, want [second]", got)
	}
}

// TestWatchReportQueueClearDiscards guards ClearWatchReports: it drops
// the queue without delivering it.
func TestWatchReportQueueClearDiscards(t *testing.T) {
	state := newTestState()
	state.PushWatchReport("w1", "stale", "", false, false)
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
	state.PushWatchReport("w1", "[watch 1 finished] stale report", "", false, false)

	state.Shutdown()

	if got := state.WatchReports(); len(got) != 0 {
		t.Fatalf("watch report queue after shutdown = %v, want empty", got)
	}
}

// TestShutdownPersistsWatchReports guards the Shutdown-time residual
// handling: still-pending watch reports are persisted as RoleUser
// ContentTypeWatchReport messages before the session ends, so they survive
// restart rather than being lost at process exit.
func TestShutdownPersistsWatchReports(t *testing.T) {
	state := newTestState()
	state.PushWatchReport("w1", "[watch 1 finished] pending report", "", false, false)

	state.Shutdown()

	var persisted bool
	for _, m := range state.Messages() {
		if m.Role == RoleUser && m.ContentType == ContentTypeWatchReport &&
			m.Content == "[watch 1 finished] pending report" {
			persisted = true
		}
	}
	if !persisted {
		t.Fatalf("pending watch report not persisted at Shutdown: %#v", state.Messages())
	}
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
			state.PushWatchReport("w1", "report", "", false, false)
		}
	}()
	for i := 0; i < n; i++ {
		state.PushWatchReport("w2", "report", "", false, false)
	}
	<-done
	// w1 folds into one entry; w2 stays separate -> 2 entries.
	if got := len(state.WatchReports()); got != 2 {
		t.Fatalf("WatchReports() len = %d, want 2 (w1 folded, w2 separate)", got)
	}
}

// TestWatchReportQueueUsesConfig guards that the queue is independent of
// the config (a zero config still works).
func TestWatchReportQueueUsesConfig(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	state.PushWatchReport("w1", "report", "", false, false)
	if got := state.WatchReports(); len(got) != 1 {
		t.Fatalf("WatchReports() = %v, want [report]", got)
	}
}

// TestWatchReportEntryCarriesResumeFields guards the resume pipeline's
// facts through coalescing: the entry carries name/repeat/resume, and a
// folded entry takes the latest fire's values.
func TestWatchReportEntryCarriesResumeFields(t *testing.T) {
	state := newTestState()
	state.PushWatchReport("w1", "first text", "build", true, true)
	state.PushWatchReport("w1", "second text", "build", true, true)
	got := state.DrainWatchReports()
	if len(got) != 1 {
		t.Fatalf("drained %d entries, want 1 (coalesced)", len(got))
	}
	if got[0].Text != "second text" || got[0].Name != "build" || !got[0].Repeat || !got[0].Resume {
		t.Fatalf("drained entry = %+v, want latest text with resume fields", got[0])
	}
}
