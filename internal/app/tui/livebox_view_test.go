package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/livebox"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// boxOut is the box as the frame draws it: the same layout the model planned
// for the frame, painted at the left column's width.
func boxOut(m Model) string {
	return livebox.Render(m.boxLayout(), m.leftWidth)
}

// boxRowY is the screen row of the layout row at index i, or -1 when the box
// draws no such row.
func boxRowY(m *Model, i int) int {
	if i < 0 || i >= len(m.boxLayout().Rows) {
		return -1
	}
	return m.boxTop() + i
}

// boxRowFor returns the index of the first layout row matching f.
func boxRowFor(m *Model, f func(livebox.Row) bool) int {
	for i, r := range m.boxLayout().Rows {
		if f(r) {
			return i
		}
	}
	return -1
}

// TestLiveBeatRendersInTheBoxNotTheViewport is the split's whole point: while
// the turn runs, the beat in flight is the box's content, and only settles
// into the transcript once it is history.
func TestLiveBeatRendersInTheBoxNotTheViewport(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Reading the parser.")
	m.state.LogToolCall(registry.AuditEvent{Timestamp: m.now(), ToolName: "file.read", StepID: id, ToolCallID: "r",
		Args: []byte(`{"path":"p.go"}`), ResultSummary: "ok"})
	m.busy, m.turnStartedAt = true, m.now()
	m.invalidateTranscript()
	m.refreshViewport()

	if box := stripANSI(boxOut(m)); !strings.Contains(box, "Reading the parser.") {
		t.Fatalf("the live beat must render in the box:\n%s", box)
	}
	if vp := stripANSI(m.viewport.GetContent()); strings.Contains(vp, "Reading the parser.") {
		t.Fatalf("the live beat must not be in the viewport:\n%s", vp)
	}

	// Settled, the same beat is history and belongs to the viewport.
	m.busy = false
	m.invalidateTranscript()
	m.refreshViewport()
	if vp := stripANSI(m.viewport.GetContent()); !strings.Contains(vp, "Reading the parser.") {
		t.Fatalf("a settled beat belongs in the viewport:\n%s", vp)
	}
}

// TestBoxIsBudgetedInTheFrameHeight is the geometry guard: the box's rows are
// part of the height budget, so a full box never pushes the input or the
// status footer off the bottom of the frame.
func TestBoxIsBudgetedInTheFrameHeight(t *testing.T) {
	prevHook := clipLeftColumnHook
	t.Cleanup(func() { clipLeftColumnHook = prevHook })

	for _, sz := range []struct{ w, h int }{{80, 24}, {120, 40}} {
		t.Run(fmt.Sprintf("%dx%d", sz.w, sz.h), func(t *testing.T) {
			m := newTestModel(t)
			m.resize(sz.w, sz.h)
			cards := make([]viewmodel.WorkItem, 0, 8)
			for i := range 8 {
				cards = append(cards, workRunningCard(fmt.Sprintf("work:%02d", i), workT0.Add(-time.Duration(i)*time.Second)))
			}
			m.workSnapshot = workSnapOf(cards...)
			items := make([]native.TodoItem, 0, 10)
			for i := range 9 {
				items = append(items, native.TodoItem{Content: fmt.Sprintf("task %d", i), Status: native.TodoCompleted})
			}
			items = append(items, native.TodoItem{Content: "current", Status: native.TodoInProgress})
			mustSetTodos(t, &m, items...)
			id := m.state.BeginStep(session.Actor{})
			m.state.AddNarration(id, "Running the suite.")
			m.busy, m.turnStartedAt = true, m.now()
			m.invalidateTranscript()
			m.refreshViewport()

			trimmed := 0
			clipLeftColumnHook = func(n int) { trimmed += n }
			frame := stripANSI(m.viewString())
			clipLeftColumnHook = prevHook

			if trimmed != 0 {
				t.Errorf("clipLeftColumn trimmed %d rows: the box is not in the height budget", trimmed)
			}
			if rows := len(strings.Split(frame, "\n")); rows != sz.h {
				t.Errorf("frame rows = %d, want %d", rows, sz.h)
			}
		})
	}
}

// TestBoxClickTogglesAWorkCard: a click on a running row opens that card, and
// a second click closes it again.
func TestBoxClickTogglesAWorkCard(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 40)
	cards := []viewmodel.WorkItem{
		workRunningCard("work:one", workT0.Add(-30*time.Second)),
		workRunningCard("work:two", workT0.Add(-20*time.Second)),
	}
	m.workSnapshot = workSnapOf(cards...)
	m.refreshViewport()
	m.viewString()

	i := boxRowFor(&m, func(r livebox.Row) bool {
		return r.Target.Kind == livebox.TargetWork && r.Target.Key == "work:two"
	})
	if i < 0 {
		t.Fatalf("no box row for work:two:\n%s", stripANSI(boxOut(m)))
	}
	if _, handled := m.handleBoxClick(tea.Mouse{Button: tea.MouseLeft, X: 1, Y: boxRowY(&m, i)}); !handled {
		t.Fatal("a click on a running row must be consumed")
	}
	if m.workExpanded != "work:two" {
		t.Fatalf("workExpanded = %q, want the clicked card", m.workExpanded)
	}

	m.viewString()
	i = boxRowFor(&m, func(r livebox.Row) bool {
		return r.Target.Kind == livebox.TargetWork && r.Target.Key == "work:two"
	})
	if _, handled := m.handleBoxClick(tea.Mouse{Button: tea.MouseLeft, X: 1, Y: boxRowY(&m, i)}); !handled {
		t.Fatal("a click on the expanded card must be consumed")
	}
	if m.workExpanded != "" {
		t.Fatalf("workExpanded = %q, want the card collapsed again", m.workExpanded)
	}
}

// TestBoxClickOnPlanItemJumpsToItsBeat: the plan item's row scrolls the
// transcript to the first beat bound to it.
func TestBoxClickOnPlanItemJumpsToItsBeat(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	for i := range 80 {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("filler %d", i), session.ContentTypePlain)
	}
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	mustSetTodos(t, &m,
		native.TodoItem{ID: "t1", Content: "First", Status: native.TodoInProgress},
		native.TodoItem{ID: "t2", Content: "Second", Status: native.TodoPending},
	)
	done := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(done, "Doing the first thing.")
	m.state.EndStep(done)
	// The list moves on, and the live beat binds to the new item.
	mustSetTodos(t, &m,
		native.TodoItem{ID: "t1", Content: "First", Status: native.TodoCompleted},
		native.TodoItem{ID: "t2", Content: "Second", Status: native.TodoInProgress},
	)
	live := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(live, "Doing the second thing.")
	m.busy, m.turnStartedAt = true, m.now()
	m.invalidateTranscript()
	m.refreshViewport()
	m.viewString()

	var want viewmodel.NodeID
	for _, t := range m.band.Tasks {
		if t.ID == "t1" {
			want = t.First
		}
	}
	if want == (viewmodel.NodeID{}) {
		t.Fatalf("todo t1 has no beat: %+v", m.band.Tasks)
	}

	i := boxRowFor(&m, func(r livebox.Row) bool {
		return r.Target.Kind == livebox.TargetPlan && r.Target.Key == "t1"
	})
	if i < 0 {
		t.Fatalf("no plan row for t1:\n%s", stripANSI(boxOut(m)))
	}
	if _, handled := m.handleBoxClick(tea.Mouse{Button: tea.MouseLeft, X: 1, Y: boxRowY(&m, i)}); !handled {
		t.Fatal("a click on a plan item must be consumed")
	}
	if m.viewportFollow {
		t.Fatal("jumping to a beat must stop following")
	}
	if got := m.viewport.YOffset(); got == 0 {
		t.Fatalf("viewport did not scroll to the beat (offset %d)", got)
	}
}

// TestBoxClickOnALiveCallExpandsIt: the live part's rows are click targets,
// so a call in flight can be opened to read its args and output.
func TestBoxClickOnALiveCallExpandsIt(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Running the suite.")
	m.state.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StartedAt: m.now(), StepID: id, ToolCallID: "run"})
	m.busy, m.turnStartedAt = true, m.now()
	m.invalidateTranscript()
	m.refreshViewport()
	m.viewString()

	i := boxRowFor(&m, func(r livebox.Row) bool {
		return r.Target.Kind == livebox.TargetLive && r.Target.Node.Kind == viewmodel.KindTool
	})
	if i < 0 {
		t.Fatalf("the live call has no box row:\n%s", stripANSI(boxOut(m)))
	}
	row := m.boxLayout().Rows[i]
	before := m.isToolExpanded(row.Target.Node, true)
	if _, handled := m.handleBoxClick(tea.Mouse{Button: tea.MouseLeft, X: 1, Y: boxRowY(&m, i)}); !handled {
		t.Fatal("a click on a live call must be consumed")
	}
	if got := m.isToolExpanded(row.Target.Node, true); got == before {
		t.Fatalf("click on the live call did not toggle its expansion (was %v)", before)
	}
}

// TestWheelOverTheBoxReasoningScrollsIt: the streaming reasoning draws in the
// box, and the wheel over it scrolls the bounded region rather than the
// transcript underneath.
func TestWheelOverTheBoxReasoningScrollsIt(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.state.BeginStreaming()
	for i := range 40 {
		m.state.AppendThinking(fmt.Sprintf("reasoning line %d that is long enough to be distinct\n", i))
	}
	m.refreshViewport()
	m.viewString()

	i := boxRowFor(&m, func(r livebox.Row) bool {
		return r.Target.Kind == livebox.TargetLive && r.Target.Node == viewmodel.LiveThinkingID
	})
	if i < 0 {
		t.Fatalf("the reasoning has no box row:\n%s", stripANSI(boxOut(m)))
	}
	before := m.viewport.YOffset()
	out, _ := m.Update(tea.MouseWheelMsg{X: 1, Y: boxRowY(&m, i), Button: tea.MouseWheelUp})
	mm := asModel(t, out)

	if got := mm.regionOffset[viewmodel.LiveThinkingID]; got != 1 {
		t.Fatalf("region offset = %d, want 1 after one wheel-up", got)
	}
	if got := mm.viewport.YOffset(); got != before {
		t.Fatalf("the wheel over the box scrolled the transcript: %d -> %d", before, got)
	}
}

// TestBoxStateShowsTheSDDRun: an active run's summary is the box's state.
func TestBoxStateShowsTheSDDRun(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.state.SetSDDProgress(session.SDDProgress{
		Active: true, PlanName: "p", TotalTasks: 5, DoneTasks: 1, CurrentTask: 2, Phase: "implement",
	})
	m.busy, m.turnStartedAt = true, m.now().Add(-time.Minute)
	m.refreshViewport()

	box := stripANSI(boxOut(m))
	if !strings.Contains(box, "task 2/5") {
		t.Fatalf("the box must carry the run summary:\n%s", box)
	}
	if !strings.Contains(box, "1m") {
		t.Fatalf("the box must carry the turn clock:\n%s", box)
	}
}
