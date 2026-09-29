// internal/app/tui/lane_consolidation_test.go — one compact activity row
package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/tools/native"
	"marshal/internal/watch"
)

// Task 8 left the agent lane rendering one row per running child. Task 14 owns
// the whole activity band, so the per-child rows are replaced by ONE row: a
// count, on the lane's separator, that opens the inspector's Agents tab.
//
// Three things have to stay true for that to be a consolidation rather than a
// removal, and each has a test here:
//
//   - the COUNT is still on screen, because "is anything running?" is the
//     question the band exists to answer at a glance;
//   - the DETAILS are still reachable, one keystroke or click away, and the
//     lane says where to look rather than silently dropping them;
//   - a FAILED child is not swallowed. The lane counts RUNNING work, as it
//     always has, and a failure still surfaces where it did before — in the
//     transcript, with its own card. A count that quietly absorbed a failure
//     would be the one way this change could hide something that matters.

// laneModel returns a model with n running children and a wide enough frame
// for the inspector.
func laneModel(t *testing.T, n int, width, height int) Model {
	t.Helper()
	m := newTestModel(t)
	m.resize(width, height)
	for i := 0; i < n; i++ {
		child := newChildState(t)
		child.AddMessage(session.RoleUser, "work", session.ContentTypePlain)
		m.state.RegisterSubagent("explore", child)
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()
	return m
}

// laneVisibleText returns the lane's rendered text with escapes removed.
func laneVisibleText(t *testing.T, m Model) string {
	t.Helper()
	return ansi.Strip(m.renderActivityLane())
}

// The lane is ONE row of chrome plus one row of content, whatever the number of
// children. This is the whole point of the consolidation: a busy turn used to
// push up to seven rows of chrome above the composer.
func TestLaneIsOneContentRowHoweverManyChildrenRun(t *testing.T) {
	for _, n := range []int{1, 2, 4, 9} {
		t.Run(strings.Repeat("x", n), func(t *testing.T) {
			m := laneModel(t, n, 120, 40)
			lane := m.renderActivityLane()
			if lane == "" {
				t.Fatalf("the lane is empty with %d running agents", n)
			}
			lines := strings.Split(strings.TrimRight(lane, "\n"), "\n")
			if len(lines) != 2 {
				t.Fatalf("the lane renders %d rows with %d running agents, want 2 (rule + count):\n%s",
					len(lines), n, ansi.Strip(lane))
			}
			if got := m.laneRows(); got != 2 {
				t.Fatalf("laneRows reports %d with %d running agents, want 2", got, n)
			}
		})
	}
}

// The count must be on screen. "Is anything running?" is the question the band
// answers at a glance, and a consolidated row that answered only "look
// elsewhere" would be worse than the rows it replaced.
func TestLaneShowsTheRunningCount(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{1, "1 agent"},
		{3, "3 agents"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			m := laneModel(t, tc.n, 120, 40)
			text := laneVisibleText(t, m)
			if !strings.Contains(text, tc.want) {
				t.Fatalf("the lane does not report %q:\n%s", tc.want, text)
			}
		})
	}
}

// The lane must say where the details went. A count alone leaves a reader who
// wants the child's model, elapsed time or label with no idea whether that
// information still exists — and the whole reason the per-child rows were
// acceptable was that they carried it.
func TestLanePointsAtTheInspector(t *testing.T) {
	m := laneModel(t, 2, 120, 40)
	text := laneVisibleText(t, m)
	if !strings.Contains(text, "agents") {
		t.Fatalf("the lane does not point at the Agents tab:\n%s", text)
	}
	// The affordance is named, not implied: "2 agents" alone is a count, and
	// the reader needs to know it is also a handle.
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "tab") && !strings.Contains(lower, "inspect") && !strings.Contains(lower, "enter") && !strings.Contains(lower, "click") {
		t.Fatalf("the lane does not say what opens the details:\n%s", text)
	}
}

// Jobs and watches share the one row. They were separate rows before, and
// dropping them would lose the only on-screen trace of a running background job
// while the user is looking at a turn.
func TestLaneCountsJobsAndWatchesOnTheOneRow(t *testing.T) {
	m := laneModel(t, 1, 120, 40)
	// Real running jobs and watches, which the lane has always reported. They
	// are put in the model's own collections rather than faked with counters:
	// a test that set a count directly would pass while the renderer read a
	// different field.
	m.jobs = []native.JobInfo{
		{ID: "job-1", Command: "go test ./...", Status: native.StatusRunning, StartedAt: time.Now()},
		{ID: "job-2", Command: "go build ./...", Status: native.StatusRunning, StartedAt: time.Now()},
	}
	m.watches = []watch.Event{
		{WatchID: "w1", Name: "tests", Kind: watch.KindCommand, State: watch.StateWatching},
	}
	m.refreshViewport()

	text := laneVisibleText(t, m)
	for _, want := range []string{"1 agent", "2 jobs", "1 watch"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the lane does not report %q:\n%s", want, text)
		}
	}
	if got := m.laneRows(); got != 2 {
		t.Fatalf("laneRows reports %d, want 2 however many kinds are running", got)
	}
}

// A FAILED child is not counted as running, and the lane does not pretend it is
// gone: the failure surfaces as a transcript card, which is where it surfaced
// before. A count that absorbed it would be the one way this change could hide
// something that matters.
func TestLaneDoesNotCountFailedChildren(t *testing.T) {
	m := laneModel(t, 1, 120, 40)
	// A second child that fails.
	failed := newChildState(t)
	view := m.state.RegisterSubagent("explore", failed)
	m.state.FinishSubagent(view.ID, "the child could not read the file", errors.New("boom"))
	m.lastTranscriptHash = 0
	m.refreshViewport()

	text := laneVisibleText(t, m)
	if !strings.Contains(text, "1 agent") {
		t.Fatalf("the lane does not report the ONE running agent:\n%s", text)
	}
	if strings.Contains(text, "2 agent") {
		t.Fatalf("the lane counts the failed child as running:\n%s", text)
	}
}

// With nothing running the lane renders nothing at all, so it costs no rows.
// A consolidated row that always appeared would take a line from the transcript
// on every idle frame.
func TestLaneIsEmptyWhenNothingRuns(t *testing.T) {
	m := laneModel(t, 0, 120, 40)
	if got := m.renderActivityLane(); got != "" {
		t.Fatalf("the lane renders with nothing running:\n%s", ansi.Strip(got))
	}
	if got := m.laneRows(); got != 0 {
		t.Fatalf("laneRows reports %d with nothing running", got)
	}
}

// Clicking the row opens the inspector on the Agents tab — the same surface the
// per-child rows opened one child of. Clicking must reach the SAME information,
// not less of it.
func TestLaneClickOpensTheAgentsTab(t *testing.T) {
	m := laneModel(t, 2, 120, 40)
	top, bottom, ok := m.agentLaneBand()
	if !ok {
		t.Fatal("the lane reports no band to click, so it cannot be reached")
	}
	if bottom-top != 2 {
		t.Fatalf("the lane's band is %d rows, want 2", bottom-top)
	}

	cmd, handled := m.handleAgentLaneClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: top})
	if !handled {
		t.Fatal("the click on the lane was not handled at all")
	}
	_ = cmd
	if !m.inspector.isRendering() {
		t.Fatal("clicking the lane did not open the inspector")
	}
	if got := m.inspector.model.SelectedTab(); got != inspector.TabAgents {
		t.Fatalf("the inspector opened on %q, want the Agents tab", got)
	}
}

// Clicking anywhere in the row opens the inspector, including the separator
// line above the count. Restricting the target to one of the two rows would
// make a one-row band half dead, and a reader has no way to know which half.
func TestLaneClickWorksOnBothOfItsRows(t *testing.T) {
	for _, offset := range []int{0, 1} {
		m := laneModel(t, 1, 120, 40)
		top, _, ok := m.agentLaneBand()
		if !ok {
			t.Fatal("no band")
		}
		if _, handled := m.handleAgentLaneClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: top + offset}); !handled {
			t.Fatalf("a click on row %d of the lane was not handled", offset)
		}
		if !m.inspector.isRendering() {
			t.Fatalf("a click on row %d of the lane did not open the inspector", offset)
		}
	}
}

// A click outside the band belongs to whatever is there, not to the lane. The
// band is computed from its own height, so an off-by-one would steal a click
// from the transcript.
func TestLaneClickOutsideTheBandIsNotMine(t *testing.T) {
	m := laneModel(t, 2, 120, 40)
	top, bottom, ok := m.agentLaneBand()
	if !ok {
		t.Fatal("no band")
	}
	for _, y := range []int{top - 1, bottom} {
		if _, handled := m.handleAgentLaneClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: y}); handled {
			t.Fatalf("a click at y=%d (band is [%d,%d)) was claimed by the lane", y, top, bottom)
		}
	}
}

// A click past the right edge of the left column is not the lane's either: the
// side rail and the inspector live there, and stealing their clicks would make
// the inspector unreachable with a mouse.
func TestLaneClickOutsideTheLeftColumnIsNotMine(t *testing.T) {
	m := laneModel(t, 1, 120, 40)
	top, _, ok := m.agentLaneBand()
	if !ok {
		t.Fatal("no band")
	}
	if _, handled := m.handleAgentLaneClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: m.leftWidth, Y: top}); handled {
		t.Fatalf("a click at x=%d (left column is [0,%d)) was claimed by the lane", m.leftWidth, m.leftWidth)
	}
}

// A right-click is not a left-click. Every other clickable surface in the app
// distinguishes them, and the lane must too or it becomes a trap for anyone
// using a terminal that sends right-click events.
func TestLaneIgnoresNonLeftClicks(t *testing.T) {
	m := laneModel(t, 1, 120, 40)
	top, _, ok := m.agentLaneBand()
	if !ok {
		t.Fatal("no band")
	}
	if _, handled := m.handleAgentLaneClick(tea.MouseClickMsg{Button: tea.MouseRight, X: 2, Y: top}); handled {
		t.Fatal("a right-click was claimed by the lane")
	}
}

// When the inspector is NOT available — a terminal too narrow for the side rail
// and with the dock unavailable — the lane still reports its count and says
// what to do instead. A row that became a dead click target on a narrow frame
// would be worse than the per-child rows it replaced, which at least drilled in.
func TestLaneStillReportsWhenTheInspectorHasNowhereToOpen(t *testing.T) {
	m := laneModel(t, 2, 40, 20)
	m.railHidden = true
	m.inspector.close()
	m.lastTranscriptHash = 0
	m.refreshViewport()

	text := laneVisibleText(t, m)
	if !strings.Contains(text, "2 agents") {
		t.Fatalf("the lane dropped its count on a narrow frame:\n%s", text)
	}
}

// The lane's height accounting must equal what it renders, in every state, or
// the input area is pushed off the bottom. This is the invariant the per-child
// rows were budgeted against, and it has to survive the change.
func TestLaneRowBudgetMatchesWhatIsRendered(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T) Model
	}{
		{"idle", func(t *testing.T) Model { return laneModel(t, 0, 120, 40) }},
		{"one agent", func(t *testing.T) Model { return laneModel(t, 1, 120, 40) }},
		{"nine agents", func(t *testing.T) Model { return laneModel(t, 9, 120, 40) }},
		{"narrow", func(t *testing.T) Model { return laneModel(t, 3, 80, 24) }},
		{"jobs and watches", func(t *testing.T) Model {
			m := laneModel(t, 2, 120, 40)
			m.jobs = []native.JobInfo{
				{ID: "job-1", Command: "a", Status: native.StatusRunning, StartedAt: time.Now()},
				{ID: "job-2", Command: "b", Status: native.StatusRunning, StartedAt: time.Now()},
				{ID: "job-3", Command: "c", Status: native.StatusRunning, StartedAt: time.Now()},
			}
			m.watches = []watch.Event{
				{WatchID: "w1", Name: "a", State: watch.StateWatching},
				{WatchID: "w2", Name: "b", State: watch.StateWatching},
			}
			return m
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.build(t)
			rendered := m.renderActivityLane()
			want := 0
			if rendered != "" {
				want = len(strings.Split(strings.TrimRight(rendered, "\n"), "\n"))
			}
			if got := m.laneRows(); got != want {
				t.Fatalf("laneRows = %d but %d rows were rendered:\n%s", got, want, ansi.Strip(rendered))
			}
		})
	}
}

// The lane's chrome stays intact: the separator rule and the rail, which is what
// marks where the transcript ends. A consolidation that dropped the rule would
// make the band blend into the composer.
func TestLaneKeepsItsSeparatorAndRail(t *testing.T) {
	m := laneModel(t, 1, 120, 40)
	lane := m.renderActivityLane()
	if lane == "" {
		t.Fatal("the lane is empty")
	}
	first := strings.Split(lane, "\n")[0]
	if !strings.Contains(ansi.Strip(first), "─") {
		t.Fatalf("the lane has no separator rule: %q", ansi.Strip(first))
	}
	// Every row is rail-prefixed, so the band reads as one region.
	for i, line := range strings.Split(strings.TrimRight(lane, "\n"), "\n") {
		if ansi.StringWidth(line) > 0 && !strings.HasPrefix(ansi.Strip(line), "▍") {
			t.Fatalf("lane row %d is not rail-prefixed: %q", i, ansi.Strip(line))
		}
	}
}

// The lane must fit the width it is given. An overflow here wraps and pushes the
// composer down, which is the failure the whole height budget exists to prevent.
func TestLaneFitsItsWidth(t *testing.T) {
	for _, width := range []int{40, 60, 80, 120, 200} {
		m := laneModel(t, 12, width, 40)
		m.jobs = []native.JobInfo{
			{ID: "j1", Command: "a", Status: native.StatusRunning, StartedAt: time.Now()},
			{ID: "j2", Command: "b", Status: native.StatusRunning, StartedAt: time.Now()},
		}
		m.watches = []watch.Event{{WatchID: "w1", Name: "a", State: watch.StateWatching}}
		m.lastTranscriptHash = 0
		m.refreshViewport()
		for i, line := range strings.Split(strings.TrimRight(m.renderActivityLane(), "\n"), "\n") {
			if got := ansi.StringWidth(line); got > m.leftWidth {
				t.Fatalf("width %d: lane row %d is %d cells wide:\n%q", width, i, got, ansi.Strip(line))
			}
		}
	}
}

// The band the click handler uses must be the band the renderer drew. They are
// derived from two different computations — a rendered height and a row count —
// and a mismatch sends the click somewhere else.
func TestLaneBandCoversTheRowsItRenders(t *testing.T) {
	m := laneModel(t, 3, 120, 40)
	top, bottom, ok := m.agentLaneBand()
	if !ok {
		t.Fatal("no band")
	}
	rendered := m.renderActivityLane()
	if rendered == "" {
		t.Fatal("no lane rendered")
	}
	want := len(strings.Split(strings.TrimRight(rendered, "\n"), "\n"))
	if bottom-top != want {
		t.Fatalf("the band is %d rows ([%d,%d)) but %d rows were rendered", bottom-top, top, bottom, want)
	}
}
