package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"marshal/internal/app/session"
	"marshal/internal/tools/native"
)

var nowBarT0 = time.Unix(10_000, 0)

func nowBarBase() nowBarInput {
	return nowBarInput{Now: nowBarT0, Width: 100, Height: 40}
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
			return in
		}, 1, []string{"▰▰▱▱", "2/4 · task 3", "1m 12s", "⠋"}, nil},
		{"todos idle still shown", func() nowBarInput {
			in := nowBarBase()
			in.Todos = nowBarTodos("completed", "pending")
			return in
		}, 1, []string{"1/2 · task 2"}, nil},
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
		}, 4, []string{"#1  agent1", "#2  agent2", "… 4 more"}, []string{"○ w2"}},
		{"short frame is one summary row", func() nowBarInput {
			in := busy(nowBarBase())
			in.Height = 24
			in.Todos = nowBarTodos("completed", "in_progress", "pending")
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
	in.Agents = nowBarAgents(6)
	plan := planNowBar(in)
	if len(plan.rows) != nowBarMaxRows || len(plan.agents) != nowBarMaxRows-1 {
		t.Fatalf("rows=%d agents=%d", len(plan.rows), len(plan.agents))
	}
	if !strings.Contains(stripANSI(plan.rows[3]), "… 3 more") {
		t.Fatalf("overflow row = %q", stripANSI(plan.rows[3]))
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
