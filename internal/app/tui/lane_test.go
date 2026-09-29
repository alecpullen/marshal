package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/tools/native"
	"marshal/internal/watch"
)

func TestLaneItemPluralizes(t *testing.T) {
	if got := laneItem(1, "job", "jobs"); got != "1 job" {
		t.Fatalf("laneItem(1) = %q, want %q", got, "1 job")
	}
	if got := laneItem(2, "job", "jobs"); got != "2 jobs" {
		t.Fatalf("laneItem(2) = %q, want %q", got, "2 jobs")
	}
}

func TestRenderLaneEmptyWhenNoRows(t *testing.T) {
	if got := renderLane("1 job", nil, 80); got != "" {
		t.Fatalf("renderLane with no rows must be empty, got %q", got)
	}
}

func TestRenderLaneStructure(t *testing.T) {
	rows := []string{"row one", "row two"}
	// The header is a pre-formatted chrome.Header line (the caller wraps
	// the caption); renderLane renders it verbatim as the caption row.
	header := chrome.Header("2 agents", "", 79)
	out := renderLane(header, rows, 80)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected separator + header + 2 rows = 4 lines, got %d:\n%s", len(lines), out)
	}
	// Row 0 is the separator: starts with the rail and carries the rule.
	sep := ansi.Strip(lines[0])
	if !strings.HasPrefix(sep, glyph.Rail) {
		t.Fatalf("separator must start with the rail, got %q", sep)
	}
	if !strings.Contains(sep, "─") {
		t.Fatalf("separator must carry the rule, got %q", sep)
	}
	// Row 1 is the header: contains the header text and the rule on the
	// same line (via chrome.Header).
	headerLine := ansi.Strip(lines[1])
	if !strings.Contains(headerLine, "2 agents") {
		t.Fatalf("header must contain the caption, got %q", headerLine)
	}
	if !strings.Contains(headerLine, "─") {
		t.Fatalf("header must carry the rule on the same line, got %q", headerLine)
	}
	// Every row carries the rail.
	for i, l := range lines {
		if !strings.Contains(ansi.Strip(l), glyph.Rail) {
			t.Errorf("row %d has no rail: %q", i, ansi.Strip(l))
		}
	}
	// Every line is within the width budget.
	for i, l := range lines {
		if n := ansi.StringWidth(l); n > 80 {
			t.Errorf("row %d exceeds width: %d > 80", i, n)
		}
	}
}

// The lane and the todo panel sit directly on top of each other; their
// rails must land in the same column or the stack looks broken.
func TestRenderLaneBridgesTodoPanelRail(t *testing.T) {
	m := newTestModel(t)
	todos := []native.TodoItem{{Content: "a task", Status: native.TodoInProgress}}
	if err := m.state.SetTodos(todos); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	width := 80
	lane := renderLane("1 agent", []string{"row one"}, width)
	todo := renderTodoPanelBody(todos, todoPanelExpanded, 40, width)

	laneRows := strings.Split(strings.TrimRight(lane, "\n"), "\n")
	todoRows := strings.Split(strings.TrimRight(todo, "\n"), "\n")
	if len(laneRows) == 0 || len(todoRows) == 0 {
		t.Fatal("expected both panels to render")
	}
	railCol := func(s string) int { return strings.Index(ansi.Strip(s), glyph.Rail) }
	want := railCol(todoRows[len(todoRows)-1])
	for i, l := range laneRows {
		if got := railCol(l); got != want {
			t.Errorf("lane row %d rail at column %d, todo panel rail at column %d", i, got, want)
		}
	}
}

// The plan counts every kind, and keeps the running agents as a slice because
// the renderer decides from it whether to offer the inspector.
//
// This replaces TestLanePlanAgentsBeforeJobs and TestLanePlanOverflowShared,
// which pinned the per-kind visible rows and the shared overflow row Task 14
// removes. The counts they asserted are still asserted here: losing one while
// dropping the rows is exactly the failure this guards against.
func TestLanePlanCountsEveryKind(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "agent-a")
	registerRunningSubagent(t, &m, "agent-b")
	m.jobs = []native.JobInfo{runningJob(1, "cmd", time.Second)}
	m.watches = []watch.Event{watchEvent("w1", "build", watch.KindCommand, watch.StateWatching)}

	plan := m.lanePlan()
	if plan.nAgents != 2 || plan.nJobs != 1 || plan.nWatches != 1 {
		t.Fatalf("counts = agents %d jobs %d watches %d, want 2/1/1",
			plan.nAgents, plan.nJobs, plan.nWatches)
	}
	if plan.total != 4 {
		t.Fatalf("total = %d, want 4", plan.total)
	}
	// The agents slice is the inspector's source, so it must carry EVERY running
	// child — a count with an empty slice offers a tab with nothing in it.
	if len(plan.agents) != 2 {
		t.Fatalf("plan carries %d agents, want both running children", len(plan.agents))
	}
}

// laneRows must agree with what the renderer draws for every total 0..9. The
// budget is a constant while anything runs, which is what makes the agreement
// provable rather than arithmetic kept in step by hand.
func TestLaneRowsMatchesRenderPlan(t *testing.T) {
	for total := 0; total <= 9; total++ {
		m := newTestModel(t)
		for i := 0; i < total; i++ {
			registerRunningSubagent(t, &m, "agent")
		}
		want := 0
		if m.lanePlan().total > 0 {
			want = laneActivityRows
		}
		if got := m.laneRows(); got != want {
			t.Fatalf("total=%d: laneRows()=%d, want %d", total, got, want)
		}
		if want > laneActivityRows {
			t.Fatalf("total=%d: laneRows()=%d exceeds the lane's ceiling %d", total, want, laneActivityRows)
		}
	}
}
