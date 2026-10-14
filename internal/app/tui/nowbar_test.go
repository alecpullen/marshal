package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/tools/native"
	"marshal/internal/viewmodel"
	"marshal/internal/watch"
)

// registerRunningSubagent registers a running background child on the test
// model's session. The bar only tracks views with a live Child state;
// pipeline/SDD cards (Child == nil) are already pinned by the progress row.
func registerRunningSubagent(t *testing.T, m *Model, label string) {
	t.Helper()
	child := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagent(label, child)
}

var nowBarT0 = time.Unix(10_000, 0)

func nowBarBase() nowBarInput {
	return nowBarInput{Now: nowBarT0, Width: 100, Height: 40}
}

// baseNowBarInput is a busy, wide input: the shape the band tests start from.
func baseNowBarInput() nowBarInput {
	in := nowBarBase()
	in.Busy = true
	in.TurnStartedAt = nowBarT0.Add(-72 * time.Second)
	in.Spinner = "⠋"
	return in
}

// The band's progress head carries the todo count, the active item and the
// stale tag, and its second line collapses the done count and names what
// comes next.
func TestNowBarShowsStaleTagAndChecklist(t *testing.T) {
	in := baseNowBarInput()
	// Narrow enough that only the first waiting item fits: the checklist
	// names what it can and counts the rest, rather than truncating.
	in.Width = 60
	in.Band = viewmodel.BandInfo{Todos: true, Done: 2, Total: 9, Active: "Task 2", Stale: true,
		StaleAge: 12 * time.Minute,
		Next:     []string{"Task 3 — LoRA picker", "Task 4 — VerdictControl", "Task 5 — Ship it"}}
	p := planNowBar(in)
	if len(p.rows) < 2 {
		t.Fatalf("rows = %d:\n%s", len(p.rows), stripANSI(strings.Join(p.rows, "\n")))
	}
	head := ansi.Strip(p.rows[0])
	if !strings.Contains(head, "2/9") || !strings.Contains(head, "stale 12m") {
		t.Fatalf("head = %q", head)
	}
	list := ansi.Strip(p.rows[1])
	if !strings.Contains(list, "2 done") || !strings.Contains(list, "next: Task 3") || !strings.Contains(list, "+2") {
		t.Fatalf("checklist = %q", list)
	}
	// The named items are the earliest ones, never the ones that did not fit.
	if strings.Contains(list, "Task 5") {
		t.Fatalf("checklist named an item it should have counted: %q", list)
	}
	if w := lipgloss.Width(list); w > in.Width {
		t.Fatalf("checklist is %d cells, frame is %d", w, in.Width)
	}
}

// A short frame collapses the bar to one row and drops the checklist, but the
// stale tag must survive: it is the signal the design keeps on every frame.
func TestNowBarCompactRowKeepsStaleTag(t *testing.T) {
	in := baseNowBarInput()
	in.Width = 80
	in.Height = nowBarCompactHeight - 1
	in.Band = viewmodel.BandInfo{Todos: true, Done: 2, Total: 9, Active: "Task 2", Stale: true, StaleAge: 7 * time.Minute}
	p := planNowBar(in)
	if len(p.rows) != 1 {
		t.Fatalf("rows = %d, want the one compact row", len(p.rows))
	}
	if row := ansi.Strip(p.rows[0]); !strings.Contains(row, "stale 7m") {
		t.Fatalf("compact row = %q, want the stale tag", row)
	}
}

// The band is capped: progress, checklist, entity rows and the overflow row
// together never exceed nowBarMaxRows.
func TestNowBarCapsAtSixRows(t *testing.T) {
	in := baseNowBarInput()
	in.Band = viewmodel.BandInfo{Todos: true, Done: 1, Total: 4, Active: "A", Next: []string{"B"}}
	for i := 0; i < 8; i++ {
		in.JobTexts = append(in.JobTexts, fmt.Sprintf("job-%d", i))
	}
	p := planNowBar(in)
	if len(p.rows) != nowBarMaxRows {
		t.Fatalf("rows = %d, want %d:\n%s", len(p.rows), nowBarMaxRows, stripANSI(strings.Join(p.rows, "\n")))
	}
	if !strings.Contains(ansi.Strip(p.rows[nowBarMaxRows-1]), "more") {
		t.Fatalf("last row = %q", ansi.Strip(p.rows[nowBarMaxRows-1]))
	}
}

// A tall frame opens the checklist into one row per waiting item, as far as
// the rows the cards leave free, counting the rest on the last row.
func TestNowBarTallFrameOpensTheChecklist(t *testing.T) {
	in := baseNowBarInput()
	in.Height = nowBarTallHeight
	in.Band = viewmodel.BandInfo{Todos: true, Done: 1, Total: 8, Active: "A",
		Next: []string{"B", "C", "D", "E", "F", "G"}}
	p := planNowBar(in)
	got := make([]string, len(p.rows))
	for i, r := range p.rows {
		got[i] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ansi.Strip(r)), "▍"))
	}
	want := []string{glyph.Ambient + " B", glyph.Ambient + " C", glyph.Ambient + " D", glyph.Ambient + " E", "… +2 more"}
	if len(p.rows) != nowBarMaxRows || strings.Join(got[1:], "|") != strings.Join(want, "|") {
		t.Fatalf("rows = %q, want the head then %q", got, want)
	}
	for _, r := range p.rows {
		if w := lipgloss.Width(r); w > in.Width {
			t.Fatalf("row %q is %d cells, frame is %d", ansi.Strip(r), w, in.Width)
		}
	}

	// Cards keep their rows: with three running, the list has two rows left,
	// too few to name two items and count the rest, so it folds back to the
	// summary line.
	in.JobTexts = []string{"job-1", "job-2", "job-3"}
	p = planNowBar(in)
	if len(p.rows) != 5 || !strings.Contains(ansi.Strip(p.rows[1]), "next: B") {
		t.Fatalf("with cards: rows = %q, want the head, the summary line and the cards", stripANSI(strings.Join(p.rows, "\n")))
	}

	// Below the tall height the summary line is all the list gets.
	in.JobTexts = nil
	in.Height = nowBarTallHeight - 1
	if p = planNowBar(in); len(p.rows) != 2 {
		t.Fatalf("short frame: rows = %d, want the head and the summary line", len(p.rows))
	}
}

func nowBarTodos(statuses ...string) []native.TodoItem {
	out := make([]native.TodoItem, len(statuses))
	for i, s := range statuses {
		out[i] = native.TodoItem{Content: fmt.Sprintf("task %d", i+1), Status: s}
	}
	return out
}

func nowBarAgents(n int) []session.SubagentView {
	out := make([]session.SubagentView, n)
	for i := range out {
		out[i] = session.SubagentView{
			ID: int64(i + 1), Label: fmt.Sprintf("agent%d", i+1),
			Status: session.SubagentRunning, StartedAt: nowBarT0.Add(-12 * time.Second),
		}
	}
	return out
}

func TestPlanNowBar(t *testing.T) {
	busy := func(in nowBarInput) nowBarInput {
		in.Busy = true
		in.TurnStartedAt = nowBarT0.Add(-72 * time.Second)
		in.Spinner = "⠋"
		return in
	}
	cases := []struct {
		name string
		in   func() nowBarInput
		rows int
		want []string // substrings of the stripped, joined rows
		not  []string
	}{
		{"idle", func() nowBarInput { return nowBarBase() }, 0, nil, nil},
		{"busy no todos shows turn row", func() nowBarInput {
			in := busy(nowBarBase())
			in.ActivityLabel = "shell.run"
			return in
		}, 1, []string{"⠋ 1m 12s", "shell.run"}, nil},
		{"todos 2/4 busy", func() nowBarInput {
			in := busy(nowBarBase())
			in.Todos = nowBarTodos("completed", "completed", "in_progress", "pending")
			in.Band = viewmodel.BandInfo{Todos: true, Done: 2, Total: 4, Active: "task 3", Next: []string{"task 4"}}
			return in
		}, 2, []string{"▰▰▱▱", "2/4 · task 3", "1m 12s", "⠋", "2 done", "next: task 4"}, nil},
		{"abandoned todos do not pin the bar when idle", func() nowBarInput {
			in := nowBarBase()
			in.Todos = nowBarTodos("completed", "pending")
			return in
		}, 0, nil, nil},
		{"todos all done disappear", func() nowBarInput {
			in := nowBarBase()
			in.Todos = nowBarTodos("completed", "completed")
			return in
		}, 0, nil, nil},
		{"sdd active", func() nowBarInput {
			in := busy(nowBarBase())
			in.SDD = session.SDDProgress{Active: true, TotalTasks: 7, DoneTasks: 3, CurrentTask: 4, Phase: "verifying"}
			in.Todos = nowBarTodos("pending")
			return in
		}, 1, []string{"▰▰▰▱▱▱▱", "task 4/7", "verifying"}, []string{"1/1"}},
		{"sdd finished", func() nowBarInput {
			in := nowBarBase()
			in.SDD = session.SDDProgress{Finished: true, Succeeded: true, TotalTasks: 3, DoneTasks: 3, StartedAt: nowBarT0.Add(-time.Minute), EndedAt: nowBarT0}
			return in
		}, 1, []string{"sdd done", "3/3 tasks"}, nil},
		{"swarm", func() nowBarInput {
			in := busy(nowBarBase())
			in.Swarm = session.SwarmProgress{Active: true, Roles: []session.SwarmRole{{Name: "implementer", Status: session.SwarmRoleActive}}}
			return in
		}, 1, []string{"swarm 0/1 · implementer"}, nil},
		{"browser", func() nowBarInput {
			in := nowBarBase()
			in.Browser = session.BrowserInfo{SessionOpen: true, URL: "https://example.com/docs"}
			return in
		}, 1, []string{"example.com/docs"}, nil},
		{"2 agents + job under busy turn row", func() nowBarInput {
			in := busy(nowBarBase())
			in.Agents = nowBarAgents(2)
			in.JobTexts = []string{"┆ j1  make  3s"}
			return in
		}, 4, []string{"1m 12s", "#1  agent1", "#2  agent2", "┆ j1"}, nil},
		{"overflow", func() nowBarInput {
			in := busy(nowBarBase())
			in.Agents = nowBarAgents(2)
			in.JobTexts = []string{"┆ j1", "┆ j2"}
			in.WatchTexts = []string{"○ w1", "○ w2"}
			return in
		}, nowBarMaxRows, []string{"#1  agent1", "#2  agent2", "… 2 more"}, []string{"○ w2"}},
		{"short frame is one summary row", func() nowBarInput {
			in := busy(nowBarBase())
			in.Height = 24
			in.Todos = nowBarTodos("completed", "in_progress", "pending")
			in.Band = viewmodel.BandInfo{Todos: true, Done: 1, Total: 3, Active: "task 2"}
			in.Agents = nowBarAgents(2)
			in.JobTexts = []string{"┆ j1"}
			return in
		}, 1, []string{"1/3 · task 2", "⧉2 ┆1", "1m 12s"}, []string{"○"}},
		{"short frame idle is empty", func() nowBarInput {
			in := nowBarBase()
			in.Height = 24
			return in
		}, 0, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in()
			plan := planNowBar(in)
			if len(plan.rows) != tc.rows {
				t.Fatalf("rows = %d, want %d:\n%s", len(plan.rows), tc.rows, stripANSI(strings.Join(plan.rows, "\n")))
			}
			out := renderNowBar(plan, in.Width)
			if h := lipgloss.Height(out); tc.rows > 0 && h != tc.rows {
				t.Fatalf("rendered height = %d, want %d", h, tc.rows)
			}
			if tc.rows == 0 && out != "" {
				t.Fatalf("zero-row bar rendered %q", out)
			}
			plain := stripANSI(out)
			for _, w := range tc.want {
				if !strings.Contains(plain, w) {
					t.Errorf("missing %q in:\n%s", w, plain)
				}
			}
			for _, w := range tc.not {
				if strings.Contains(plain, w) {
					t.Errorf("unexpected %q in:\n%s", w, plain)
				}
			}
			for i, line := range strings.Split(plain, "\n") {
				if tc.rows > 0 && lipgloss.Width(line) > in.Width {
					t.Errorf("row %d is %d cells, frame is %d", i, lipgloss.Width(line), in.Width)
				}
			}
		})
	}
}

func TestPlanNowBarAgentRowsMapForClicks(t *testing.T) {
	in := nowBarBase()
	in.Busy, in.TurnStartedAt = true, nowBarT0.Add(-time.Second)
	in.Agents = nowBarAgents(2)
	plan := planNowBar(in)
	if plan.agentRowStart != 1 || len(plan.agents) != 2 {
		t.Fatalf("agentRowStart=%d agents=%d, want 1 and 2", plan.agentRowStart, len(plan.agents))
	}
	if !strings.Contains(stripANSI(plan.rows[plan.agentRowStart+1]), "#2") || plan.agents[1].ID != 2 {
		t.Fatal("agent rows and plan.agents disagree on order")
	}
}

func TestPlanNowBarOverflowKeepsOnlyShownAgents(t *testing.T) {
	in := nowBarBase()
	in.Agents = nowBarAgents(8)
	plan := planNowBar(in)
	if len(plan.rows) != nowBarMaxRows || len(plan.agents) != nowBarMaxRows-1 {
		t.Fatalf("rows=%d agents=%d", len(plan.rows), len(plan.agents))
	}
	last := stripANSI(plan.rows[nowBarMaxRows-1])
	if !strings.Contains(last, "… 3 more") {
		t.Fatalf("overflow row = %q", last)
	}
	// The click map must reach exactly the agents still on screen.
	for i, v := range plan.agents {
		if v.ID != int64(i+1) {
			t.Fatalf("agent %d = #%d: the overflow row must not leave a gap in the click map", i, v.ID)
		}
	}
}

func TestProgressBlocks(t *testing.T) {
	cases := []struct {
		done, total, max int
		want             string
	}{
		{0, 4, 10, "▱▱▱▱"},
		{2, 4, 10, "▰▰▱▱"},
		{4, 4, 10, "▰▰▰▰"},
		{3, 7, 10, "▰▰▰▱▱▱▱"},
		{4, 20, 10, "▰▰▱▱▱▱▱▱▱▱"}, // scaled to 10 cells
		{5, 20, 10, "▰▰▰▱▱▱▱▱▱▱"}, // 2.5 rounds up
		{0, 0, 10, ""},
	}
	for _, c := range cases {
		if got := stripANSI(progressBlocks(c.done, c.total, c.max)); got != c.want {
			t.Errorf("progressBlocks(%d,%d,%d) = %q, want %q", c.done, c.total, c.max, got, c.want)
		}
	}
}

func TestNowBarModelAgentsSkipPipelineCards(t *testing.T) {
	m := newTestModel(t)
	m.state.RegisterSubagent("card", nil)
	if rows := m.nowBarRows(); rows != 0 {
		t.Fatalf("a Child-less card must not occupy the bar, rows = %d", rows)
	}
	registerRunningSubagent(t, &m, "reviewer")
	if rows := m.nowBarRows(); rows != 1 {
		t.Fatalf("one running subagent = %d rows, want 1", rows)
	}
}

func TestNowBarModelRowsMatchRender(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.busy = true
	m.turnStartedAt = m.now().Add(-5 * time.Second)
	for i := range 6 {
		registerRunningSubagent(t, &m, fmt.Sprintf("agent %d", i))
	}
	m.height = 40
	out := renderNowBar(m.nowBarPlan(), m.leftWidth)
	if got, want := lipgloss.Height(out), m.nowBarRows(); got != want {
		t.Fatalf("rendered %d rows, budget says %d", got, want)
	}
}

func TestNowBarAgentRowShowsModelAndProviderOnlyWhenDifferent(t *testing.T) {
	in := nowBarBase()
	in.Provider = "ollama"
	in.Agents = nowBarAgents(2)
	in.Agents[0].Model = "qwen"
	in.Agents[0].Provider = "ollama"
	in.Agents[1].Model = "gpt"
	in.Agents[1].Provider = "openai"
	plain := stripANSI(strings.Join(planNowBar(in).rows, "\n"))
	if !strings.Contains(plain, "qwen") || strings.Contains(plain, "qwen @") {
		t.Errorf("same-provider child should show the model alone:\n%s", plain)
	}
	if !strings.Contains(plain, "gpt @ openai") {
		t.Errorf("off-parent child should show the provider:\n%s", plain)
	}
}

// ↑ on an empty input recalls prompt history even with a running subagent:
// there is no lane cursor to capture it.
func TestUpArrowRecallsHistoryWithRunningSubagent(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "reviewer")
	m.history = []string{"previous prompt"}
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = mm.(Model)
	if m.input.Value() != "previous prompt" {
		t.Fatalf("input = %q, want history recall", m.input.Value())
	}
	if len(m.viewStack) != 0 {
		t.Fatal("↑ must not drill into an agent")
	}
}

func nowBarOut(m Model) string { return renderNowBar(m.nowBarPlan(), m.leftWidth) }

func TestNowBarEmptyWithoutLiveWork(t *testing.T) {
	m := newTestModel(t)
	if got := nowBarOut(m); got != "" {
		t.Fatalf("idle bar must render nothing, got %q", got)
	}
	m.jobs = []native.JobInfo{{ID: "job-1", Command: "x", Status: native.StatusCompleted}}
	if got := nowBarOut(m); got != "" {
		t.Fatalf("finished jobs must render nothing, got %q", got)
	}
}

func TestNowBarShowsJobsAndWatchesAfterAgents(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	registerRunningSubagent(t, &m, "reviewer")
	m.jobs = []native.JobInfo{runningJob(1, "npm run dev", time.Minute)}
	m.watches = []watch.Event{watchEvent("w1", "build", watch.KindCommand, watch.StateWatching)}
	plain := stripANSI(nowBarOut(m))
	lines := strings.Split(plain, "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 rows (agent, job, watch), got %d:\n%s", len(lines), plain)
	}
	for i, want := range []string{"reviewer", "job-1  npm run dev", "build  command  watching"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("row %d missing %q: %q", i, want, lines[i])
		}
	}
	if !strings.Contains(lines[1], glyph.Job) || !strings.Contains(lines[2], glyph.Watch) {
		t.Errorf("job/watch rows lost their markers:\n%s", plain)
	}
}

func TestNowBarCapsJobsAndWatchesWithOverflow(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	for i := range 5 {
		m.jobs = append(m.jobs, runningJob(i+1, "cmd", time.Second))
		m.watches = append(m.watches, watchEvent("w", "cmd", watch.KindCommand, watch.StateWatching))
	}
	plain := stripANSI(nowBarOut(m))
	if got := strings.Count(plain, "\n") + 1; got != nowBarMaxRows {
		t.Fatalf("bar rendered %d rows, cap is %d:\n%s", got, nowBarMaxRows, plain)
	}
	// Ten entities, five rows of room: five shown, five counted.
	if !strings.Contains(plain, "… 5 more") {
		t.Fatalf("expected a shared overflow row:\n%s", plain)
	}
}

// The SDD text must be fit to what is left after the glyph, blocks and
// elapsed clock, so low-priority segments are dropped whole instead of the
// row being cut mid-word with an ellipsis.
func TestNowBarSDDRowDropsSegmentsInsteadOfTruncating(t *testing.T) {
	in := nowBarBase()
	in.Width = 60
	in.Busy, in.TurnStartedAt, in.Spinner = true, nowBarT0.Add(-72*time.Second), "⠋"
	in.SDD = session.SDDProgress{
		Active: true, TotalTasks: 7, DoneTasks: 3, CurrentTask: 4,
		Phase: "verifying", PhaseStartedAt: nowBarT0.Add(-20 * time.Second),
		StartedAt: nowBarT0.Add(-10 * time.Minute), Detail: "src/auth/handler.go",
	}
	plan := planNowBar(in)
	if len(plan.rows) != 1 {
		t.Fatalf("rows = %d", len(plan.rows))
	}
	plain := stripANSI(plan.rows[0])
	if strings.Contains(plain, "…") {
		t.Errorf("row was truncated rather than dropping segments: %q", plain)
	}
	if !strings.Contains(plain, "task 4/7") || !strings.Contains(plain, "1m 12s") {
		t.Errorf("row lost the task counter or clock: %q", plain)
	}
	if w := lipgloss.Width(plain); w > in.Width {
		t.Errorf("row is %d cells, frame is %d", w, in.Width)
	}
}

// Embedded line breaks in todo content, job commands and page titles must
// not split a plan row across screen lines.
func TestNowBarRowsAreSingleLines(t *testing.T) {
	in := nowBarBase()
	in.Busy, in.TurnStartedAt = true, nowBarT0.Add(-time.Second)
	in.Todos = []native.TodoItem{{Content: "step one\nstep two", Status: native.TodoInProgress}}
	in.JobTexts = []string{"┆ job-1  cat <<EOF\nline2\nEOF  3s"}
	in.Browser = session.BrowserInfo{SessionOpen: true, URL: "https://example.com", Title: "a\nb"}
	in.Agents = nowBarAgents(1)
	in.Agents[0].Label = "review\nthe diff"
	plan := planNowBar(in)
	for i, r := range plan.rows {
		if strings.Contains(r, "\n") {
			t.Errorf("row %d spans lines: %q", i, r)
		}
	}
	if got := lipgloss.Height(renderNowBar(plan, in.Width)); got != len(plan.rows) {
		t.Errorf("rendered %d lines for %d plan rows", got, len(plan.rows))
	}
}

// When the browser row folds into `… N more`, the status line must keep the
// URL instead of both surfaces dropping it.
func TestNowBarShowsBrowserOnlyWhenRowIsVisible(t *testing.T) {
	in := nowBarBase()
	in.Busy, in.TurnStartedAt = true, nowBarT0.Add(-time.Second)
	in.Browser = session.BrowserInfo{SessionOpen: true, URL: "https://example.com"}

	in.Agents = nowBarAgents(1)
	if !planNowBar(in).showsBrowser {
		t.Error("browser row is visible with one agent; the status line should drop its copy")
	}
	in.Agents = nowBarAgents(6) // turn row + 4 agents + overflow row: the browser is past the cut
	if planNowBar(in).showsBrowser {
		t.Error("browser row folded into the overflow; the status line must carry the URL")
	}
}

func TestTasksPanelStaysLiveWhileTodosChange(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	if err := m.state.SetTodos([]native.TodoItem{{Content: "first draft", Status: native.TodoInProgress}}); err != nil {
		t.Fatal(err)
	}
	m = ctrlT(m)
	if view := stripANSI(m.viewString()); !strings.Contains(view, "Tasks 0/1") || !strings.Contains(view, "first draft") {
		t.Fatalf("initial panel wrong:\n%s", view)
	}
	if err := m.state.SetTodos([]native.TodoItem{
		{Content: "first draft", Status: native.TodoCompleted},
		{Content: "rewritten plan", Status: native.TodoPending},
	}); err != nil {
		t.Fatal(err)
	}
	view := stripANSI(m.viewString())
	if !strings.Contains(view, "Tasks 1/2") || !strings.Contains(view, "rewritten plan") {
		t.Fatalf("open Tasks panel did not follow the todo list:\n%s", view)
	}
}
