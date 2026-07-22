package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

func newLiveStripTestModel(t *testing.T) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	return m
}

func TestLiveStripEmptyWhenNothingRunning(t *testing.T) {
	m := newLiveStripTestModel(t)
	if out := m.renderLiveStrip(); out != "" {
		t.Fatalf("live strip should be empty when idle, got %q", out)
	}
}

func TestLiveStripShowsSwarmProgress(t *testing.T) {
	m := newLiveStripTestModel(t)
	m.state.SetSwarmProgress(session.SwarmProgress{
		Active: true,
		Goal:   "add parser",
		Roles: []session.SwarmRole{
			{Name: "planner", Status: session.SwarmRoleDone},
			{Name: "scouts", Status: session.SwarmRoleDone},
			{Name: "implementer", Status: session.SwarmRoleActive, Detail: "round 1/2"},
			{Name: "tester", Status: session.SwarmRolePending},
			{Name: "reviewer", Status: session.SwarmRolePending},
		},
	})
	out := stripANSI(m.renderLiveStrip())
	if !strings.Contains(out, "swarm 2/5") {
		t.Fatalf("live strip missing swarm counts:\n%s", out)
	}
	if !strings.Contains(out, "implementer") || !strings.Contains(out, "round 1/2") {
		t.Fatalf("live strip missing active role:\n%s", out)
	}
	if strings.Count(out, "\n") != 0 {
		t.Fatalf("live strip must be one row:\n%s", out)
	}
}

func TestLiveStripShowsSDDProgress(t *testing.T) {
	m := newLiveStripTestModel(t)
	m.state.SetSDDProgress(session.SDDProgress{
		Active:     true,
		PlanName:   "phase 3",
		TotalTasks: 7,
		DoneTasks:  2,
		Tasks: []session.SDDTaskStatus{
			{Name: "task one", Phase: session.SDDPhaseDone},
			{Name: "implement parser", Phase: session.SDDPhaseActive},
		},
	})
	out := stripANSI(m.renderLiveStrip())
	if !strings.Contains(out, "sdd task 2/7") {
		t.Fatalf("live strip missing sdd counts:\n%s", out)
	}
	if !strings.Contains(out, "implement parser") {
		t.Fatalf("live strip missing active task:\n%s", out)
	}
	if strings.Count(out, "\n") != 0 {
		t.Fatalf("live strip must be one row:\n%s", out)
	}
}

func TestLiveStripShowsBrowserSessionWhenNoRunActive(t *testing.T) {
	m := newLiveStripTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "https://example.com/docs",
		Mode:        "standalone",
	})
	out := stripANSI(m.renderLiveStrip())
	if !strings.Contains(out, "example.com/docs") {
		t.Fatalf("live strip missing browser url:\n%s", out)
	}
	if !strings.Contains(out, "standalone") {
		t.Fatalf("live strip missing browser mode:\n%s", out)
	}
}

func TestLiveStripSwarmOutranksBrowser(t *testing.T) {
	m := newLiveStripTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{SessionOpen: true, URL: "https://example.com"})
	m.state.SetSwarmProgress(session.SwarmProgress{
		Active: true,
		Roles:  []session.SwarmRole{{Name: "planner", Status: session.SwarmRoleActive}},
	})
	out := stripANSI(m.renderLiveStrip())
	if !strings.Contains(out, "swarm") {
		t.Fatalf("swarm must win over browser:\n%s", out)
	}
	if strings.Contains(out, "example.com") {
		t.Fatalf("browser must not appear while swarm is running:\n%s", out)
	}
}

func TestLiveStripFitsWidth(t *testing.T) {
	m := newLiveStripTestModel(t)
	m.resize(80, 24)
	m.state.SetSwarmProgress(session.SwarmProgress{
		Active: true,
		Roles: []session.SwarmRole{
			{Name: "implementer", Status: session.SwarmRoleActive, Detail: strings.Repeat("very long detail ", 20)},
		},
	})
	if n := visibleRunes(m.renderLiveStrip()); n > 80 {
		t.Fatalf("live strip exceeds width: %d > 80", n)
	}
}
