package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

// boxInnerRows is the tray's rows inside its outline, without the border
// lines.
func boxInnerRows(m Model) []livebox.Row {
	var out []livebox.Row
	for _, r := range m.boxLayout().Rows {
		if r.Frame == livebox.FrameInner {
			out = append(out, r)
		}
	}
	return out
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

// TestLiveBeatRendersInTheViewport: the step in progress streams at the end of
// the transcript, and settles where it was drawn.
func TestLiveBeatRendersInTheViewport(t *testing.T) {
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

	vp := stripANSI(m.viewport.GetContent())
	if !strings.Contains(vp, "Reading the parser.") {
		t.Fatalf("the live beat must render in the viewport:\n%s", vp)
	}
	if box := stripANSI(boxOut(m)); strings.Contains(box, "Reading the parser.") {
		t.Fatalf("the live beat must not be in the tray:\n%s", box)
	}
	busyLine := lineIndexOf(vp, "Reading the parser.")

	// Settled, the same beat is history and stays where it was drawn.
	m.busy = false
	m.invalidateTranscript()
	m.refreshViewport()
	settled := stripANSI(m.viewport.GetContent())
	if !strings.Contains(settled, "Reading the parser.") {
		t.Fatalf("a settled beat belongs in the viewport:\n%s", settled)
	}
	if got := lineIndexOf(settled, "Reading the parser."); got != busyLine {
		t.Errorf("the settled beat moved from line %d to %d", busyLine, got)
	}
}

// lineIndexOf is the index of the first line holding want, or -1.
func lineIndexOf(content, want string) int {
	for i, l := range strings.Split(content, "\n") {
		if strings.Contains(l, want) {
			return i
		}
	}
	return -1
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
// twoItemPlanModel is a busy session whose plan has two items, t1 finished
// and t2 in progress, each bound to its own beat, with enough history above
// them that jumping to t1's beat scrolls the viewport. gap more messages sit
// between the two beats.
func twoItemPlanModel(t *testing.T, gap int) Model {
	t.Helper()
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
	for i := range gap {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("between %d", i), session.ContentTypePlain)
	}
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
	return m
}

func TestBoxClickOnPlanItemJumpsToItsBeat(t *testing.T) {
	m := twoItemPlanModel(t, 0)

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

// TestBoxClickOnTheRightPlanColumn: the tray's rows sit inside its outline, so
// a click is measured from the row's text, two cells in. The gap cell just
// left of the right-hand entry still belongs to the left item, and the
// entry's own mark is the right item.
func TestBoxClickOnTheRightPlanColumn(t *testing.T) {
	// The history between the beats keeps t1's beat clear of the bottom, so
	// the jump to it is not clamped to the same offset as t2's.
	m := twoItemPlanModel(t, 80)
	var first viewmodel.NodeID
	for _, t := range m.band.Tasks {
		if t.ID == "t1" {
			first = t.First
		}
	}
	firstLine := -1
	for _, r := range m.nodeRegions {
		if r.target.node == first {
			firstLine = r.startLine
			break
		}
	}
	if firstLine < 0 {
		t.Fatal("t1's beat has no region")
	}

	i := boxRowFor(&m, func(r livebox.Row) bool {
		return r.Target.Kind == livebox.TargetPlan && r.Target.Key == "t1"
	})
	if i < 0 {
		t.Fatalf("no plan row for t1:\n%s", stripANSI(boxOut(m)))
	}
	// Find the right-hand entry on screen: its mark sits two cells before its
	// text.
	line := stripANSI(strings.Split(boxOut(m), "\n")[i])
	at := strings.Index(line, "Second")
	if at < 0 {
		t.Fatalf("no right-hand entry on the row: %q", line)
	}
	mark := ansi.StringWidth(line[:at]) - 2

	if _, handled := m.handleBoxClick(tea.Mouse{Button: tea.MouseLeft, X: mark - 1, Y: boxRowY(&m, i)}); !handled {
		t.Fatal("a click on a plan row must be consumed")
	}
	if got := m.viewport.YOffset(); got != firstLine {
		t.Errorf("a click left of the right-hand entry scrolled to line %d, want t1's beat at %d", got, firstLine)
	}
	if _, handled := m.handleBoxClick(tea.Mouse{Button: tea.MouseLeft, X: mark, Y: boxRowY(&m, i)}); !handled {
		t.Fatal("a click on a plan row must be consumed")
	}
	if got := m.viewport.YOffset(); got == firstLine {
		t.Errorf("a click on the right-hand entry's mark stayed on t1's beat (line %d)", got)
	}
}

// TestScrollToPlanItemUsesTheOneBasedSlot pins the fallback key a plan item
// uses when it has no ID: the slot is 1-based, so "#1" must reach the first
// item's beat and "#2" the second's.
func TestScrollToPlanItemUsesTheOneBasedSlot(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	a := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(a, "Doing the first thing.")
	m.state.EndStep(a)
	b := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(b, "Doing the second thing.")
	m.state.EndStep(b)
	// Filler after the beats, so both of them stay inside the scrollable range
	// and the offsets below are not clamped.
	for i := range 80 {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("filler %d", i), session.ContentTypePlain)
	}
	m.invalidateTranscript()
	m.refreshViewport()

	// The steps the transcript rendered, in order, with the line each one
	// starts at: the beats the plan's items would be bound to.
	var beats []viewmodel.NodeID
	starts := map[viewmodel.NodeID]int{}
	for _, r := range m.nodeRegions {
		if r.target.node.Kind != viewmodel.KindStep {
			continue
		}
		if _, seen := starts[r.target.node]; !seen {
			beats = append(beats, r.target.node)
			starts[r.target.node] = r.startLine
		}
	}
	if len(beats) < 2 {
		t.Fatalf("transcript rendered %d step regions, want 2", len(beats))
	}
	// Both todos are ID-less, so the plan names them by 1-based position.
	m.band = viewmodel.BandInfo{Tasks: []viewmodel.BandTask{
		{Content: "First", Status: "completed", TaskStat: viewmodel.TaskStat{First: beats[0]}},
		{Content: "Second", Status: "in_progress", TaskStat: viewmodel.TaskStat{First: beats[1]}},
	}}
	if plan := m.boxInput().Plan; len(plan) != 2 || plan[0].ID != "" {
		t.Fatalf("plan = %+v, want two ID-less items", plan)
	}

	m.scrollToPlanItem("#1")
	if got, want := m.viewport.YOffset(), starts[beats[0]]; got != want {
		t.Errorf("scrollToPlanItem(%q) went to line %d, want the first item's beat at %d", "#1", got, want)
	}
	m.scrollToPlanItem("#2")
	if got, want := m.viewport.YOffset(), starts[beats[1]]; got != want {
		t.Errorf("scrollToPlanItem(%q) went to line %d, want the second item's beat at %d", "#2", got, want)
	}
}

// TestTraySubagentCardShowsItsModelAndProvider: a running subagent's tray card
// is two rows and names the model and provider actually running the child, read
// from the subagent view rather than from the work projection.
func TestTraySubagentCardShowsItsModelAndProvider(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 40)
	child := newChildState(t)
	view := m.state.RegisterSubagentWithMeta("explore repo", child, session.SubagentMeta{
		Model: "kimi-k3", Provider: "kimi",
	})
	m.workSnapshot = workSnapOf(viewmodel.WorkItem{
		TaskKey: "work:child", Kind: "agent", SourceID: fmt.Sprint(view.ID),
		DisplayID: fmt.Sprintf("#%d", view.ID), Label: "explore repo",
		State: "running", StartedAt: m.now().Add(-time.Minute),
	})
	m.invalidateTranscript()
	m.refreshViewport()
	m.viewString()

	in := m.boxInput()
	if got := in.Agents["work:child"]; got.Model != "kimi-k3" || got.Provider != "kimi" {
		t.Fatalf("tray agent meta = %+v, want the subagent view's model and provider", got)
	}

	layout := m.boxLayout()
	head := boxRowFor(&m, func(r livebox.Row) bool {
		return r.Target.Kind == livebox.TargetWork && r.Target.Key == "work:child"
	})
	if head < 0 {
		t.Fatalf("no tray row for the subagent card:\n%s", stripANSI(boxOut(m)))
	}
	if head+1 >= len(layout.Rows) {
		t.Fatalf("the subagent card is one row, want two:\n%s", stripANSI(boxOut(m)))
	}
	detail := layout.Rows[head+1]
	if detail.Target != (livebox.Target{Kind: livebox.TargetWork, Key: "work:child"}) {
		t.Errorf("second row target = %+v, want the same work as the head row", detail.Target)
	}
	if got := stripANSI(detail.Text); !strings.Contains(got, "kimi-k3 @ kimi") {
		t.Errorf("second row = %q, want the model at the provider", got)
	}
}

// TestSwarmIsTheTraysFirstRow: an active swarm is the first row inside the
// tray's outline.
func TestSwarmIsTheTraysFirstRow(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.state.SetSwarmProgress(session.SwarmProgress{
		Active: true,
		Goal:   "ship the tray",
		Roles:  []session.SwarmRole{{Name: "implementer", Status: "running"}},
	})
	m.busy, m.turnStartedAt = true, m.now().Add(-time.Minute)
	m.invalidateTranscript()
	m.refreshViewport()

	rows := boxInnerRows(m)
	if len(rows) == 0 {
		t.Fatal("the tray has no rows")
	}
	first := stripANSI(rows[0].Text)
	for _, want := range []string{"run", "swarm"} {
		if !strings.Contains(first, want) {
			t.Errorf("tray's first row = %q, want it to contain %q", first, want)
		}
	}
}

// TestFinishedItemHoldsItsRowUntilTheNextPrompt: a finished item keeps its tray
// row until a turn starts, and an item that ended before the window opened is
// never shown.
func TestFinishedItemHoldsItsRowUntilTheNextPrompt(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	base := m.now()
	m.heldSince = base.Add(-time.Minute)

	finished := workRunningCard("k1", base.Add(-2*time.Minute))
	finished.State = "completed"
	finished.EndedAt = base.Add(-30 * time.Second)
	m.workSnapshot = workSnapOf(finished)
	m.invalidateTranscript()
	m.refreshViewport()

	box := stripANSI(boxOut(m))
	if !strings.Contains(box, "done") {
		t.Fatalf("a finished item must keep its row with its outcome:\n%s", box)
	}
	for _, r := range m.boxLayout().Rows {
		if r.Target.Kind == livebox.TargetWork && r.Target.Key == "k1" && r.Tint {
			t.Error("a held row must not be tinted")
		}
	}

	// A prompt opens a new held window, which drops the row.
	m.startHeldWindow()
	m.invalidateTranscript()
	m.refreshViewport()
	if box := stripANSI(boxOut(m)); strings.Contains(box, "k1") {
		t.Fatalf("the next prompt must drop the held row:\n%s", box)
	}

	// An item that ended before the window opened is never shown.
	old := workRunningCard("k2", base.Add(-5*time.Minute))
	old.State = "completed"
	old.EndedAt = base.Add(-4 * time.Minute)
	m.workSnapshot = workSnapOf(old)
	m.invalidateTranscript()
	m.refreshViewport()
	if box := stripANSI(boxOut(m)); strings.Contains(box, "k2") {
		t.Fatalf("an item that ended before the window must not be held:\n%s", box)
	}

	// A foreground-attached command is never held: it stays an ordinary tool
	// row, whatever its state.
	m.heldSince = base.Add(-time.Minute)
	fg := workRunningCard("k3", base.Add(-2*time.Minute))
	fg.Attachment = "foreground"
	fg.State = "completed"
	fg.EndedAt = base.Add(-30 * time.Second)
	m.workSnapshot = workSnapOf(fg)
	m.invalidateTranscript()
	m.refreshViewport()
	if box := stripANSI(boxOut(m)); strings.Contains(box, "k3") {
		t.Fatalf("a foreground command must never be held:\n%s", box)
	}
}

// TestNewModelStartsWithNothingHeld: the held window opens when the model is
// built, so a resumed session does not resurrect terminal rows from before it.
func TestNewModelStartsWithNothingHeld(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	if m.heldSince.IsZero() {
		t.Fatal("New must open the held window; a zero window holds every finished item")
	}

	// An item that ended before the model was built is never held.
	old := workRunningCard("k-old", time.Unix(50, 0))
	old.State = "completed"
	old.EndedAt = time.Unix(60, 0)
	m.workSnapshot = workSnapOf(old)
	m.invalidateTranscript()
	m.refreshViewport()
	if box := stripANSI(boxOut(m)); strings.Contains(box, "k-old") {
		t.Fatalf("a model built now must not hold an item that ended before it:\n%s", box)
	}
}

// TestHeldRowMatchesItsCompletionEvent: the held row's outcome and summary are
// the completion event's, so the tray and history always agree.
func TestHeldRowMatchesItsCompletionEvent(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	base := m.now()
	m.heldSince = base.Add(-time.Minute)

	const conv, epoch, execID = "conv-1", "epoch-1", "exec-1"
	if err := m.state.AddJobExit(session.JobExit{
		ID: "job-1", Command: "pytest -q", ExitCode: 1, Output: "boom\n",
		ExecutionID: execID, ConversationID: conv, SourceEpoch: epoch,
		At: base.Add(-30 * time.Second),
	}); err != nil {
		t.Fatalf("AddJobExit: %v", err)
	}
	key := viewmodel.WorkKey(conv, epoch, "command", execID)
	item := workRunningCard(key, base.Add(-2*time.Minute))
	item.State = "failed"
	item.EndedAt = base.Add(-30 * time.Second)
	m.workSnapshot = workSnapOf(item)
	m.invalidateTranscript()
	m.refreshViewport()

	ref, ok := m.completions[key]
	if !ok || ref.info == nil {
		t.Fatalf("no completion event for %q in %v", key, m.completions)
	}
	held, ok := m.heldItem(item)
	if !ok {
		t.Fatal("the finished item is not held")
	}
	if held.Outcome != ref.info.Outcome {
		t.Errorf("held outcome = %q, want the event's %q", held.Outcome, ref.info.Outcome)
	}
	if held.Summary != ref.info.Summary {
		t.Errorf("held summary = %q, want the event's %q", held.Summary, ref.info.Summary)
	}
	if held.Outcome != "exit 1" {
		t.Errorf("held outcome = %q, want exit 1", held.Outcome)
	}
}

// TestFinishedPlanStaysUntilTheNextPrompt: a finished list is one row until a
// turn starts, and a new open list shows again.
func TestFinishedPlanStaysUntilTheNextPrompt(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	mustSetTodos(t, &m,
		native.TodoItem{Content: "A", Status: native.TodoCompleted},
		native.TodoItem{Content: "B", Status: native.TodoCompleted})
	m.invalidateTranscript()
	m.refreshViewport()

	if box := stripANSI(boxOut(m)); !strings.Contains(box, "Plan 2/2 done") {
		t.Fatalf("a finished list must be one row:\n%s", box)
	}

	m.startHeldWindow()
	m.invalidateTranscript()
	m.refreshViewport()
	if box := stripANSI(boxOut(m)); strings.Contains(box, "Plan 2/2 done") {
		t.Fatalf("the next prompt must drop the finished plan:\n%s", box)
	}

	// A new open list shows again.
	mustSetTodos(t, &m,
		native.TodoItem{Content: "A", Status: native.TodoCompleted},
		native.TodoItem{Content: "C", Status: native.TodoInProgress})
	m.invalidateTranscript()
	m.refreshViewport()
	if box := stripANSI(boxOut(m)); !strings.Contains(box, "C") {
		t.Fatalf("a new open list must show again:\n%s", box)
	}
}
