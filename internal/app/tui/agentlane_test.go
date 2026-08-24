package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// registerRunningSubagent registers a running background child on the test
// model's session. The lane only tracks views with a live Child state;
// pipeline/SDD cards (Child == nil) are already pinned by the run panel.
func registerRunningSubagent(t *testing.T, m *Model, label string) {
	t.Helper()
	child := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagent(label, child)
}

func TestAgentLaneEmptyWithNoRunningSubagents(t *testing.T) {
	m := newTestModel(t)
	if got := m.renderAgentLane(); got != "" {
		t.Fatalf("no subagents must render nothing, got %q", got)
	}
	registerRunningSubagent(t, &m, "review")
	v := m.state.Subagents()[0]
	m.state.FinishSubagent(v.ID, "done", nil)
	if got := m.renderAgentLane(); got != "" {
		t.Fatalf("finished subagents must not render, got %q", got)
	}
}

func TestAgentLaneShowsRunningSubagents(t *testing.T) {
	m := newTestModel(t)
	registerRunningSubagent(t, &m, "tests")
	registerRunningSubagent(t, &m, "review")
	plain := ansi.Strip(m.renderAgentLane())
	for _, want := range []string{"agents 2", "tests", "review"} {
		if !strings.Contains(plain, want) {
			t.Errorf("lane missing %q:\n%s", want, plain)
		}
	}
}

func TestAgentLaneSkipsPipelineCards(t *testing.T) {
	m := newTestModel(t)
	m.state.RegisterSubagent("pipeline role", nil) // Child nil: pipeline/SDD shares the parent state
	if got := m.renderAgentLane(); got != "" {
		t.Fatalf("pipeline cards are pinned by the run panel, not the lane; got %q", got)
	}
}

// agentLaneRows must equal what the lane actually renders, or the height
// budget drifts and pushes the input area off the bottom of the frame.
func TestAgentLaneRowsMatchesRender(t *testing.T) {
	m := newTestModel(t)
	for _, n := range []int{0, 1, 2, 4, 9} {
		for _, v := range m.state.Subagents() {
			m.state.FinishSubagent(v.ID, "", nil)
		}
		for i := 0; i < n; i++ {
			registerRunningSubagent(t, &m, "task")
		}
		out := m.renderAgentLane()
		want := 0
		if out != "" {
			want = strings.Count(out, "\n")
		}
		if got := m.agentLaneRows(); got != want {
			t.Fatalf("%d running: agentLaneRows()=%d but lane rendered %d rows:\n%s", n, got, want, out)
		}
	}
}

func TestAgentLaneCapsWithOverflowRow(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 9; i++ {
		registerRunningSubagent(t, &m, "task")
	}
	out := m.renderAgentLane()
	if got := strings.Count(out, "\n"); got > agentLaneMaxRows {
		t.Fatalf("lane rendered %d rows, cap is %d", got, agentLaneMaxRows)
	}
	if !strings.Contains(ansi.Strip(out), "more") {
		t.Fatalf("expected an overflow row:\n%s", ansi.Strip(out))
	}
}
