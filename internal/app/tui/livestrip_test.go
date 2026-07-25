package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

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
			{Name: "implement parser", Phase: session.SDDPhaseActive, Implementer: session.SDDPhaseActive},
		},
	})
	out := stripANSI(m.renderLiveStrip())
	if !strings.Contains(out, "sdd task 2/7") {
		t.Fatalf("live strip missing sdd counts:\n%s", out)
	}
	if !strings.Contains(out, "implementer") {
		t.Fatalf("live strip missing active phase:\n%s", out)
	}
	if strings.Count(out, "\n") != 0 {
		t.Fatalf("live strip must be one row:\n%s", out)
	}
}

func TestSDDStripTextShowsAuditPhase(t *testing.T) {
	p := session.SDDProgress{DoneTasks: 2, TotalTasks: 5, Tasks: []session.SDDTaskStatus{
		{Name: "T3", Phase: session.SDDPhaseActive, Audit: session.SDDPhaseActive},
	}}
	text := sddStripText(p)
	if !strings.Contains(text, "audit") {
		t.Errorf("strip text missing audit phase: %q", text)
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

func TestTruncateURLIsRuneAware(t *testing.T) {
	out := truncateURL("https://例え.com/path/very/long", 12)
	if !utf8.ValidString(out) {
		t.Fatal("output is not valid UTF-8")
	}
	if ansi.StringWidth(out) > 12 {
		t.Fatalf("output exceeds max width: %d", ansi.StringWidth(out))
	}
}

func TestTruncateURL(t *testing.T) {
	cases := []struct {
		url  string
		max  int
		want string
	}{
		{"https://example.com", 30, "example.com"},
		{"http://example.com", 30, "example.com"},
		{"https://example.com", 0, "example.com"},
		{"https://developer.example.com/very/long/path/to/somewhere", 25, "developer.example.com/ve…"},
		{"https://example.com/short", 30, "example.com/short"},
		{"", 30, ""},
	}
	for _, c := range cases {
		got := truncateURL(c.url, c.max)
		if c.want == "" && got == "" {
			continue
		}
		if got != c.want {
			t.Errorf("truncateURL(%q, %d) = %q, want %q", c.url, c.max, got, c.want)
		}
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
