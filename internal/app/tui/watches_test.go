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

func TestWatchLaneShowsWatches(t *testing.T) {
	m := newTestModel(t)
	m.watches = []watch.Event{
		watchEvent("w1", "build", watch.KindCommand, watch.StateWatching),
		watchEvent("w2", "test", watch.KindJob, watch.StateFired),
	}
	plain := ansi.Strip(m.renderActivityLane())
	for _, want := range []string{"2 watches", "build", "command", "watching", "test", "job", "fired"} {
		if !strings.Contains(plain, want) {
			t.Errorf("lane missing %q:\n%s", want, plain)
		}
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

func TestWatchLaneCapsWithOverflowRow(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 9; i++ {
		m.watches = append(m.watches, watchEvent("w", "cmd", watch.KindCommand, watch.StateWatching))
	}
	out := m.renderActivityLane()
	if got := strings.Count(out, "\n"); got > laneMaxRows {
		t.Fatalf("lane rendered %d rows, cap is %d", got, laneMaxRows)
	}
	if !strings.Contains(ansi.Strip(out), "more") {
		t.Fatalf("expected an overflow row:\n%s", ansi.Strip(out))
	}
}

// Watches render after jobs in the consolidated lane, and the caption
// combines all three parts.
func TestWatchLaneRendersAfterJobs(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "reviewer")
	m.jobs = []native.JobInfo{runningJob(1, "npm run dev", time.Minute)}
	m.watches = []watch.Event{watchEvent("w1", "build", watch.KindCommand, watch.StateWatching)}
	plain := ansi.Strip(m.renderActivityLane())
	if !strings.Contains(plain, "1 agent · 1 job · 1 watch") {
		t.Fatalf("caption must combine all three parts, got:\n%s", plain)
	}
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	agentIdx, jobIdx, watchIdx := -1, -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "reviewer"):
			agentIdx = i
		case strings.Contains(l, "npm run dev"):
			jobIdx = i
		case strings.Contains(l, "build"):
			watchIdx = i
		}
	}
	if agentIdx < 0 || jobIdx < 0 || watchIdx < 0 {
		t.Fatalf("lane missing agent/job/watch row:\n%s", plain)
	}
	if jobIdx <= agentIdx {
		t.Fatalf("expected job row after agent row, got agent=%d job=%d:\n%s", agentIdx, jobIdx, plain)
	}
	if watchIdx <= jobIdx {
		t.Fatalf("expected watch row after job row, got job=%d watch=%d:\n%s", jobIdx, watchIdx, plain)
	}
}

// The lane plan must count watches into total/overflow and order them after
// agents and jobs.
func TestLanePlanWatchesAfterJobs(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "agent-a")
	m.jobs = []native.JobInfo{runningJob(1, "cmd", time.Second)}
	m.watches = []watch.Event{watchEvent("w1", "build", watch.KindCommand, watch.StateWatching)}
	plan := m.lanePlan()
	if len(plan.agents) != 1 {
		t.Fatalf("expected 1 visible agent, got %d", len(plan.agents))
	}
	if len(plan.jobTexts) != 1 {
		t.Fatalf("expected 1 job row, got %d", len(plan.jobTexts))
	}
	if len(plan.watchTexts) != 1 {
		t.Fatalf("expected 1 watch row, got %d", len(plan.watchTexts))
	}
	if plan.nAgents != 1 || plan.nJobs != 1 || plan.nWatches != 1 {
		t.Fatalf("caption counts = agents %d jobs %d watches %d, want 1/1/1", plan.nAgents, plan.nJobs, plan.nWatches)
	}
	if plan.total != 3 {
		t.Fatalf("total = %d, want 3", plan.total)
	}
}

// When total exceeds the cap, one slot is surrendered to a single shared
// overflow row; agents keep priority, then jobs, then watches.
func TestLanePlanOverflowIncludesWatches(t *testing.T) {
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
	// Agents take all 3 visible slots; jobs and watches are all overflow.
	if len(plan.agents) != 3 {
		t.Fatalf("visible agents = %d, want 3", len(plan.agents))
	}
	if len(plan.jobTexts) != 0 {
		t.Fatalf("visible jobs = %d, want 0", len(plan.jobTexts))
	}
	if len(plan.watchTexts) != 0 {
		t.Fatalf("visible watches = %d, want 0", len(plan.watchTexts))
	}
	if plan.overflow != 6 {
		t.Fatalf("overflow = %d, want 6", plan.overflow)
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
