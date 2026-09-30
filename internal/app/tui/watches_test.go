package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/pubsub"
	"marshal/internal/tools/native"
	"marshal/internal/watch"
)

func watchEvent(id, name string, kind watch.Kind, state watch.State) watch.Event {
	return watch.Event{WatchID: id, Name: name, Kind: kind, State: state}
}

// resumeWatchEvent builds the report-bearing event shape the auto-resume
// wake sites act on: a fired command watch opted into resume, carrying its
// mode so the TUI can add the repeat stop hint to the wrapper goal.
func resumeWatchEvent(id, name string, mode watch.Mode) watch.Event {
	return watch.Event{WatchID: id, Name: name, Kind: watch.KindCommand, State: watch.StateFired, Mode: mode, Resume: true}
}

func TestPumpBridgesWatchEventsToMsgs(t *testing.T) {
	// First call: nothing published. The pump cmd must block until a
	// publish arrives or ctx is cancelled (not return nil immediately).
	blockingBroker := pubsub.NewBroker[watch.Event]()
	blockingCtx, blockingCancel := context.WithCancel(context.Background())
	cmd := pumpWatchEvents(blockingBroker.Subscribe(blockingCtx))
	first := runCmdOnce(cmd, 20*time.Millisecond)
	blockingCancel()
	if first != nil {
		t.Fatalf("expected pump to block on empty broker, got immediate msg: %#v", first)
	}

	// Second call: publish from another goroutine, then call the pump cmd
	// and expect a watchMsg.
	b := pubsub.NewBroker[watch.Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx)
	go func() {
		time.Sleep(10 * time.Millisecond)
		b.Publish("watch", watchEvent("w1", "build", watch.KindCommand, watch.StateWatching))
	}()
	cmd = pumpWatchEvents(ch)
	msg := runCmdOnce(cmd, time.Second)
	if msg == nil {
		t.Fatal("pump did not bridge the event")
	}
	wm, ok := msg.(watchMsg)
	if !ok {
		t.Fatalf("got %T, want watchMsg", msg)
	}
	if wm.event.WatchID != "w1" || wm.event.Name != "build" {
		t.Fatalf("event = %+v, want w1/build", wm.event)
	}
}

func TestWatchLaneEmptyWhenNoWatches(t *testing.T) {
	m := newTestModel(t)
	if got := m.renderActivityLane(); got != "" {
		t.Fatalf("no watches must render nothing, got %q", got)
	}
}

// Watches must be visible as a COUNT. Task 14 removed the per-watch rows, so the
// name, kind and state are no longer on this row.
//
// This replaces TestWatchLaneShowsWatches, which demanded them. The count is
// still asserted, because losing it while dropping the rows is the failure this
// guards against.
func TestWatchLaneShowsWatchesAsACount(t *testing.T) {
	m := newTestModel(t)
	m.watches = []watch.Event{
		watchEvent("w1", "build", watch.KindCommand, watch.StateWatching),
		watchEvent("w2", "test", watch.KindJob, watch.StateFired),
	}
	out := m.renderActivityLane()
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "2 watches") {
		t.Errorf("lane missing the watch count:\n%s", plain)
	}
	if got := strings.Count(out, "\n"); got != laneActivityRows {
		t.Errorf("lane rendered %d rows for two watches, want %d:\n%s", got, laneActivityRows, plain)
	}
}

func TestWatchLaneHasSeparatorAndRail(t *testing.T) {
	m := newTestModel(t)
	m.watches = []watch.Event{watchEvent("w1", "build", watch.KindCommand, watch.StateWatching)}
	out := m.renderActivityLane()
	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.Contains(ansi.Strip(rows[0]), "─") {
		t.Fatalf("lane must open with a separator rule, got %q", ansi.Strip(rows[0]))
	}
	// The caption row directly beneath the separator must NOT draw a second
	// rule: a full-width separator immediately followed by a ruled header
	// reads as a messy double line. The caption is a plain label.
	if strings.Contains(ansi.Strip(rows[1]), "─") {
		t.Fatalf("caption row must be a plain label without a rule (double line), got %q", ansi.Strip(rows[1]))
	}
	if !strings.Contains(ansi.Strip(rows[1]), "1 watch") {
		t.Fatalf("caption row missing the count label, got %q", ansi.Strip(rows[1]))
	}
	// The caption row carries the rail but no watch marker; the marker lives in
	// the body rows, so check those (rows[2:] after the separator and caption).
	for i, r := range rows[2:] {
		if !strings.Contains(ansi.Strip(r), glyph.Watch) {
			t.Errorf("lane row %d has no watch marker: %q", i+2, ansi.Strip(r))
		}
	}
}

func TestWatchLaneRowsMatchesRender(t *testing.T) {
	m := newTestModel(t)
	for _, n := range []int{0, 1, 2, 4, 9} {
		m.watches = nil
		for i := 0; i < n; i++ {
			m.watches = append(m.watches, watchEvent("w", "cmd", watch.KindCommand, watch.StateWatching))
		}
		out := m.renderActivityLane()
		want := 0
		if out != "" {
			want = strings.Count(out, "\n")
		}
		if got := m.laneRows(); got != want {
			t.Fatalf("%d watches: laneRows()=%d but lane rendered %d rows:\n%s", n, got, want, out)
		}
	}
}

// Task 14 consolidated the lane to one count row, so watches are COUNTED rather
// than listed. This replaces TestWatchLaneCapsWithOverflowRow.
func TestWatchLaneCountsWatchesOnOneRow(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 9; i++ {
		m.watches = append(m.watches, watchEvent("w", "cmd", watch.KindCommand, watch.StateWatching))
	}
	out := m.renderActivityLane()
	if got := strings.Count(out, "\n"); got > laneActivityRows {
		t.Fatalf("lane rendered %d rows with 9 watches, want at most %d:\n%s",
			got, laneActivityRows, ansi.Strip(out))
	}
	if !strings.Contains(ansi.Strip(out), "9 watches") {
		t.Fatalf("the count is not the full nine:\n%s", ansi.Strip(out))
	}
}

// All three kinds share the ONE row, and the caption combines all three counts.
// There are no per-kind rows any more, so what has to hold is that no count is
// dropped when the others are present.
func TestWatchLaneCombinesAllThreeCounts(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "reviewer")
	m.jobs = []native.JobInfo{runningJob(1, "npm run dev", time.Minute)}
	m.watches = []watch.Event{watchEvent("w1", "build", watch.KindCommand, watch.StateWatching)}
	out := m.renderActivityLane()
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "1 agent · 1 job · 1 watch") {
		t.Fatalf("caption must combine all three parts, got:\n%s", plain)
	}
	if got := strings.Count(out, "\n"); got != laneActivityRows {
		t.Fatalf("lane rendered %d rows, want %d:\n%s", got, laneActivityRows, plain)
	}
}

// The lane plan must COUNT watches into the total alongside agents and jobs.
// There is no per-kind row and no overflow bucket any more, so what has to hold
// is that a watch is never dropped from the count.
func TestLanePlanCountsWatchesAlongsideAgentsAndJobs(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "agent-a")
	m.jobs = []native.JobInfo{runningJob(1, "cmd", time.Second)}
	m.watches = []watch.Event{watchEvent("w1", "build", watch.KindCommand, watch.StateWatching)}
	plan := m.lanePlan()
	if plan.nAgents != 1 || plan.nJobs != 1 || plan.nWatches != 1 {
		t.Fatalf("counts = agents %d jobs %d watches %d, want 1/1/1",
			plan.nAgents, plan.nJobs, plan.nWatches)
	}
	if plan.total != 3 {
		t.Fatalf("total = %d, want 3", plan.total)
	}
	// The agents slice carries the running child, which is what makes the
	// inspector reachable from this row.
	if len(plan.agents) != 1 {
		t.Fatalf("plan carries %d agents, want the one running child", len(plan.agents))
	}
}

// Every kind contributes to the total, so a reader sees the whole picture in the
// one count. There is no overflow bucket any more — the total IS the count, and
// that is the property this replaces TestLanePlanOverflowIncludesWatches with.
func TestLanePlanTotalIncludesEveryKind(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 3; i++ {
		registerRunningSubagent(t, &m, "agent")
	}
	for i := 0; i < 3; i++ {
		m.jobs = append(m.jobs, runningJob(i+1, "cmd", time.Second))
	}
	for i := 0; i < 3; i++ {
		m.watches = append(m.watches, watchEvent("w", "cmd", watch.KindCommand, watch.StateWatching))
	}
	plan := m.lanePlan()
	if plan.total != 9 {
		t.Fatalf("total = %d, want 9", plan.total)
	}
	if plan.nAgents != 3 || plan.nJobs != 3 || plan.nWatches != 3 {
		t.Fatalf("counts = agents %d jobs %d watches %d, want 3/3/3",
			plan.nAgents, plan.nJobs, plan.nWatches)
	}
	// The agents slice must carry all three children: it is the inspector's
	// source, so a count of three with one entry would open a tab showing one.
	if len(plan.agents) != 3 {
		t.Fatalf("plan carries %d agents, want all three running children", len(plan.agents))
	}
	// And the whole thing must still render as one count row.
	plain := ansi.Strip(m.renderActivityLane())
	for _, want := range []string{"3 agents", "3 jobs", "3 watches"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("the lane does not report %q:\n%s", want, plain)
		}
	}
}

// handleWatchMsg updates the cached snapshot and re-arms the pump.
func TestHandleWatchMsgUpdatesSnapshot(t *testing.T) {
	m := newTestModel(t)
	m2, _ := m.handleWatchMsg(watchMsg{event: watchEvent("w1", "build", watch.KindCommand, watch.StateWatching)})
	mm := asModel(t, m2)
	if len(mm.watches) != 1 {
		t.Fatalf("watches = %d, want 1", len(mm.watches))
	}
	if mm.watches[0].Name != "build" {
		t.Fatalf("watch name = %q, want build", mm.watches[0].Name)
	}

	// A second event for the same watch replaces the entry.
	m3, _ := mm.handleWatchMsg(watchMsg{event: watchEvent("w1", "build", watch.KindCommand, watch.StateFired)})
	mm3 := asModel(t, m3)
	if len(mm3.watches) != 1 {
		t.Fatalf("watches = %d, want 1 after update", len(mm3.watches))
	}
	if mm3.watches[0].State != watch.StateFired {
		t.Fatalf("watch state = %q, want fired", mm3.watches[0].State)
	}
}
