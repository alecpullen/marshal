package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
)

func runPanelFixture() session.SDDProgress {
	return session.SDDProgress{
		Active:      true,
		CurrentTask: 3,
		DoneTasks:   2,
		TotalTasks:  4,
		Phase:       "implementing",
		Detail:      "src/auth.go",
		Tasks:       []string{"Scaffold config", "Add theme slots", "Consolidate run panel", "Remove live strip"},
		StartedAt:   time.Unix(100, 0),
	}
}

func TestRunPanelEmptyWhenNoRun(t *testing.T) {
	if out := renderRunPanel(session.SDDProgress{}, session.SDDGate{}, "⠋", time.Now(), 30, 80); out != "" {
		t.Errorf("want empty when no run and no finished summary, got %q", out)
	}
}

func TestRunPanelSummaryLine(t *testing.T) {
	p := runPanelFixture()
	p.FixRound, p.MaxFixRounds = 1, 3
	now := p.StartedAt.Add(4*time.Minute + 12*time.Second)
	out := ansi.Strip(renderRunPanel(p, session.SDDGate{}, "⠋", now, 40, 100))
	for _, want := range []string{"⠋", "task 3/4", "implementing", "fix 1/3", "src/auth.go", "4m 12s"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestRunPanelSummaryOmitsEmptySegments(t *testing.T) {
	p := runPanelFixture()
	p.Phase, p.Detail = "", ""
	out := ansi.Strip(renderRunPanel(p, session.SDDGate{}, "⠋", p.StartedAt, 40, 100))
	if strings.Contains(out, " ·  · ") || strings.Contains(out, "fix ") {
		t.Errorf("empty segments leaked into summary:\n%s", out)
	}
	// No spinner glyph before the 200ms gate — falls back to the static ▸.
	if !strings.Contains(out, "▸") {
		t.Errorf("static fallback glyph missing:\n%s", out)
	}
}

func TestRunPanelChecklistStatuses(t *testing.T) {
	out := ansi.Strip(renderRunPanel(runPanelFixture(), session.SDDGate{}, "⠋", time.Unix(100, 0), 40, 100))
	for _, want := range []string{"✓ 1 Scaffold config", "✓ 2 Add theme slots", "▸ 3 Consolidate run panel", "· 4 Remove live strip"} {
		if !strings.Contains(out, want) {
			t.Errorf("checklist missing %q:\n%s", want, out)
		}
	}
}

func TestRunPanelChecklistWindowsAroundCurrentTask(t *testing.T) {
	p := runPanelFixture()
	p.Tasks = []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8", "T9", "T10"}
	p.TotalTasks, p.CurrentTask, p.DoneTasks = 10, 8, 7
	out := ansi.Strip(renderRunPanel(p, session.SDDGate{}, "⠋", time.Unix(100, 0), 40, 100))
	if !strings.Contains(out, "↑ more") {
		t.Errorf("window should clip earlier tasks:\n%s", out)
	}
	if !strings.Contains(out, "▸ 8 T8") {
		t.Errorf("current task must stay visible:\n%s", out)
	}
	if strings.Contains(out, "✓ 1 T1") {
		t.Errorf("far-past tasks should be clipped out:\n%s", out)
	}
}

func TestRunPanelGateLine(t *testing.T) {
	gate := session.SDDGate{TaskN: 3, Question: "which DB driver?"}
	out := ansi.Strip(renderRunPanel(runPanelFixture(), gate, "⠋", time.Unix(100, 0), 40, 100))
	if !strings.Contains(out, "Task 3 needs an answer: which DB driver?") {
		t.Errorf("gate line missing:\n%s", out)
	}
}

func TestRunPanelFinishedSuccess(t *testing.T) {
	p := runPanelFixture()
	p.Active, p.Finished, p.Succeeded = false, true, true
	p.DoneTasks, p.TotalTasks = 4, 4
	p.EndedAt = p.StartedAt.Add(23*time.Minute + 41*time.Second)
	out := ansi.Strip(renderRunPanel(p, session.SDDGate{}, "⠋", time.Unix(100, 0), 40, 100))
	if !strings.Contains(out, "✓") || !strings.Contains(out, "sdd done — 4/4 tasks · 23m 41s") {
		t.Errorf("collapsed success line wrong:\n%s", out)
	}
	if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 {
		t.Errorf("finished panel must be one line:\n%s", out)
	}
}

func TestRunPanelFinishedFailure(t *testing.T) {
	p := runPanelFixture()
	p.Active, p.Finished, p.Succeeded = false, true, false
	p.EndedAt = p.StartedAt.Add(12 * time.Second)
	out := renderRunPanel(p, session.SDDGate{}, "⠋", time.Unix(100, 0), 40, 100)
	stripped := ansi.Strip(out)
	if !strings.Contains(stripped, "✗") || !strings.Contains(stripped, "sdd stopped — 2/4 tasks · 12s") {
		t.Errorf("collapsed failure line wrong:\n%s", stripped)
	}
	if !strings.Contains(stripped, "/sdd to resume") {
		t.Errorf("failure line missing resume hint:\n%s", stripped)
	}
	if strings.Contains(out, "\x1b[48;") || strings.Contains(out, "\x1b[48m") {
		t.Errorf("finished panel must not have a background color:\n%s", out)
	}
}

func TestRunPanelFinishedFailureWithReason(t *testing.T) {
	p := runPanelFixture()
	p.Active, p.Finished, p.Succeeded = false, true, false
	p.EndedAt = p.StartedAt.Add(12 * time.Second)
	p.Error = "transient timeout"
	out := ansi.Strip(renderRunPanel(p, session.SDDGate{}, "⠋", time.Unix(100, 0), 100, 100))
	want := "sdd stopped — 2/4 tasks · 12s · transient timeout — /sdd to resume"
	if !strings.Contains(out, want) {
		t.Errorf("failure line missing reason:\n%s\nwant %q", out, want)
	}
}

func TestRunPanelFinishedFailureTruncatesHintFirst(t *testing.T) {
	p := runPanelFixture()
	p.Active, p.Finished, p.Succeeded = false, true, false
	p.EndedAt = p.StartedAt.Add(12 * time.Second)
	p.Error = "transient timeout"
	out := ansi.Strip(renderRunPanel(p, session.SDDGate{}, "⠋", time.Unix(100, 0), 40, 55))
	want := "sdd stopped — 2/4 tasks · 12s · transient timeout"
	if !strings.Contains(out, want) {
		t.Errorf("narrow failure line should keep reason and drop hint:\n%s\nwant %q", out, want)
	}
	if strings.Contains(out, "/sdd to resume") {
		t.Errorf("narrow failure line should drop resume hint:\n%s", out)
	}
}

func TestNextTurnClearsRunEvents(t *testing.T) {
	m := newTestModel(t)
	m.state.AddRunEvent(session.RunEvent{Kind: session.RunEventCommit, TaskN: 1, Title: "a3f9e21"})
	m.state.FinishSDDRun(true, time.Now(), nil)

	m.clearFinishedRun() // the helper that already calls ClearSDDProgress

	if n := len(m.state.RunEvents()); n != 0 {
		t.Errorf("got %d run events after the next turn, want 0 — a second run must not "+
			"append to the first run's log", n)
	}
}
