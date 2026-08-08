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
	if out := renderRunPanel(session.SDDProgress{}, "⠋", time.Now(), 80); out != "" {
		t.Errorf("want empty when no run and no finished summary, got %q", out)
	}
}

func TestRunPanelSummaryLine(t *testing.T) {
	p := runPanelFixture()
	p.FixRound, p.MaxFixRounds = 1, 3
	now := p.StartedAt.Add(4*time.Minute + 12*time.Second)
	out := ansi.Strip(renderRunPanel(p, "⠋", now, 100))
	for _, want := range []string{"⠋", "task 3/4", "implementing", "fix 1/3", "src/auth.go", "4m 12s"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestRunPanelSummaryOmitsEmptySegments(t *testing.T) {
	p := runPanelFixture()
	p.Phase, p.Detail = "", ""
	out := ansi.Strip(renderRunPanel(p, "⠋", p.StartedAt, 100))
	if strings.Contains(out, " ·  · ") || strings.Contains(out, "fix ") {
		t.Errorf("empty segments leaked into summary:\n%s", out)
	}
}

func TestRunPanelFinishedSuccess(t *testing.T) {
	p := runPanelFixture()
	p.Active, p.Finished, p.Succeeded = false, true, true
	p.DoneTasks, p.TotalTasks = 4, 4
	p.EndedAt = p.StartedAt.Add(23*time.Minute + 41*time.Second)
	out := ansi.Strip(renderRunPanel(p, "⠋", time.Unix(100, 0), 100))
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
	out := renderRunPanel(p, "⠋", time.Unix(100, 0), 100)
	stripped := ansi.Strip(out)
	if !strings.Contains(stripped, "✗") || !strings.Contains(stripped, "sdd stopped — 2/4 tasks · 12s") {
		t.Errorf("collapsed failure line wrong:\n%s", stripped)
	}
	if !strings.Contains(stripped, "/run for details") {
		t.Errorf("failure line missing outcome-panel hint:\n%s", stripped)
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
	out := ansi.Strip(renderRunPanel(p, "⠋", time.Unix(100, 0), 100))
	want := "sdd stopped — 2/4 tasks · 12s · transient timeout — /run for details · /sdd to resume"
	if !strings.Contains(out, want) {
		t.Errorf("failure line missing reason:\n%s\nwant %q", out, want)
	}
}

func TestRunPanelFinishedFailureTruncatesHintFirst(t *testing.T) {
	p := runPanelFixture()
	p.Active, p.Finished, p.Succeeded = false, true, false
	p.EndedAt = p.StartedAt.Add(12 * time.Second)
	p.Error = "transient timeout"
	out := ansi.Strip(renderRunPanel(p, "⠋", time.Unix(100, 0), 55))
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

func TestSummaryLineShowsPhaseElapsedAndTotal(t *testing.T) {
	start := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	now := start.Add(18 * time.Minute)
	p := session.SDDProgress{
		Active: true, CurrentTask: 3, TotalTasks: 7,
		Phase: "verifying", StartedAt: start,
		PhaseStartedAt: now.Add(-2*time.Minute - 14*time.Second),
	}
	got := stripANSI(runPanelSummaryLine(p, "⠋", now, 100))

	if !strings.Contains(got, "verifying 2m 14s") {
		t.Errorf("summary must show how long the current phase has run:\n%s", got)
	}
	if !strings.Contains(got, "18m 0s") {
		t.Errorf("summary must still show total elapsed:\n%s", got)
	}
}

func TestRunPanelNoLongerDuplicatesTheGate(t *testing.T) {
	p := session.SDDProgress{Active: true, CurrentTask: 2, TotalTasks: 5, Phase: "implementing"}

	got := stripANSI(renderRunPanel(p, "⠋", time.Now(), 100))
	if strings.Contains(got, "Which timeout applies?") {
		t.Errorf("the run panel must not repeat the gate question — the panel owns it now:\n%s", got)
	}
}

func TestFinishedLineAdvertisesTheOutcomePanel(t *testing.T) {
	p := session.SDDProgress{
		Finished: true, Succeeded: true, DoneTasks: 4, TotalTasks: 4,
		StartedAt: time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC),
		EndedAt:   time.Date(2026, 8, 7, 10, 23, 41, 0, time.UTC),
	}
	got := stripANSI(runPanelFinishedLine(p, 100))
	if !strings.Contains(got, "/run") {
		t.Errorf("a finished run must point at the outcome panel:\n%s", got)
	}
}

func TestFinishedLineDropsHintBeforeReasonWhenNarrow(t *testing.T) {
	p := session.SDDProgress{
		Finished: true, Succeeded: false, DoneTasks: 2, TotalTasks: 4,
		Error:     "task 3 still fails go test",
		StartedAt: time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC),
		EndedAt:   time.Date(2026, 8, 7, 10, 0, 12, 0, time.UTC),
	}
	wide := stripANSI(runPanelFinishedLine(p, 100))
	if !strings.Contains(wide, "task 3 still fails") || !strings.Contains(wide, "/run") {
		t.Errorf("wide line must carry both reason and hint:\n%s", wide)
	}
	narrow := stripANSI(runPanelFinishedLine(p, 70))
	if !strings.Contains(narrow, "task 3 still fails") {
		t.Errorf("the reason must outlive the hint when space runs out:\n%s", narrow)
	}
}

func TestSummaryLineOmitsPhaseElapsedWhenUnset(t *testing.T) {
	start := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	p := session.SDDProgress{
		Active: true, CurrentTask: 1, TotalTasks: 3,
		Phase: "implementing", StartedAt: start,
	}
	got := stripANSI(runPanelSummaryLine(p, "⠋", start.Add(time.Minute), 100))
	if !strings.Contains(got, "implementing") {
		t.Errorf("phase must still render:\n%s", got)
	}
	if strings.Contains(got, "implementing 0s") {
		t.Errorf("a zero PhaseStartedAt must not render a bogus 0s:\n%s", got)
	}
}

func etaTestProgress() session.SDDProgress {
	start := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	timings := make([]session.TaskTiming, 7)
	at := start
	for i, d := range []time.Duration{122 * time.Second, 340 * time.Second, 401 * time.Second} {
		timings[i] = session.TaskTiming{StartedAt: at, EndedAt: at.Add(d)}
		at = at.Add(d)
	}
	timings[3] = session.TaskTiming{StartedAt: at}
	return session.SDDProgress{
		Active: true, TotalTasks: 7, DoneTasks: 3, CurrentTask: 4,
		Phase: "verifying", PhaseStartedAt: at, StartedAt: start,
		Tasks:       []string{"a", "b", "c", "d", "e", "f", "g"},
		TaskTimings: timings,
	}
}

func etaTestNow(p session.SDDProgress) time.Time {
	return p.TaskTimings[3].StartedAt.Add(134 * time.Second)
}

func TestRunPanelIsOneRowDuringARun(t *testing.T) {
	p := etaTestProgress()
	got := renderRunPanel(p, "⠋", etaTestNow(p), 100)
	if n := strings.Count(strings.TrimRight(got, "\n"), "\n"); n != 0 {
		t.Errorf("the run panel must be exactly one row; got %d newlines:\n%s", n+1, got)
	}
	if strings.Contains(stripANSI(got), "1 a") {
		t.Error("the checklist must no longer render in the top bar — /run owns it")
	}
}

func TestSummaryLineCarriesPercentAndETA(t *testing.T) {
	p := etaTestProgress()
	got := stripANSI(runPanelSummaryLine(p, "⠋", etaTestNow(p), 100))

	for _, want := range []string{"task 4/7", "43%", "verifying 2m 14s", "~6–25m left"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
}

func TestPercentCountsCompletedNotCurrent(t *testing.T) {
	p := etaTestProgress() // 3 done of 7, task 4 running
	got := stripANSI(runPanelSummaryLine(p, "⠋", etaTestNow(p), 100))
	if !strings.Contains(got, "43%") {
		t.Errorf("percent must be DoneTasks/TotalTasks = 3/7 = 43%%:\n%s", got)
	}
	if strings.Contains(got, "57%") {
		t.Errorf("percent must not count the in-flight task (4/7 = 57%%):\n%s", got)
	}
}

func TestSummaryLineOmitsETABeforeThreeTasks(t *testing.T) {
	p := etaTestProgress()
	p.DoneTasks = 1
	p.TaskTimings[1] = session.TaskTiming{}
	p.TaskTimings[2] = session.TaskTiming{}
	got := stripANSI(runPanelSummaryLine(p, "⠋", etaTestNow(p), 100))
	if strings.Contains(got, "left") {
		t.Errorf("no ETA is shown before it is earned:\n%s", got)
	}
	if !strings.Contains(got, "task 4/7") {
		t.Errorf("the task counter must still render:\n%s", got)
	}
}

func TestSummaryLineDropsSegmentsInSpecOrder(t *testing.T) {
	p := etaTestProgress()
	p.Detail = "internal/retry/retry.go"
	p.FixRound, p.MaxFixRounds = 1, 3
	now := etaTestNow(p)

	// Each entry is the substring that must be gone once the width has
	// shrunk past the point that drops it, in the spec's order.
	drops := []string{"internal/retry/retry.go", "43%", "16m", "left", "fix 1/3", "2m 14s"}

	var lastSeen int
	for width := 120; width >= 12; width-- {
		got := stripANSI(runPanelSummaryLine(p, "⠋", now, width))
		if !strings.Contains(got, "task 4/7") {
			t.Fatalf("task counter dropped at width %d:\n%s", width, got)
		}
		// Once a segment is gone, everything earlier in the drop order must
		// also be gone.
		present := 0
		for i, s := range drops {
			if strings.Contains(got, s) {
				present = i + 1
			}
		}
		if present > lastSeen && width < 120 {
			t.Fatalf("a segment reappeared as width shrank to %d:\n%s", width, got)
		}
		lastSeen = present
	}
}

func TestSummaryLineFloorIsTaskCounter(t *testing.T) {
	p := etaTestProgress()
	got := stripANSI(runPanelSummaryLine(p, "⠋", etaTestNow(p), 14))
	if !strings.Contains(got, "task 4/7") {
		t.Errorf("the task counter is the floor and must survive any width:\n%s", got)
	}
}

func TestFinishedLineUnchanged(t *testing.T) {
	p := session.SDDProgress{
		Finished: true, Succeeded: true, DoneTasks: 4, TotalTasks: 4,
		StartedAt: time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC),
		EndedAt:   time.Date(2026, 8, 7, 10, 23, 41, 0, time.UTC),
	}
	got := stripANSI(runPanelFinishedLine(p, 100))
	want := "✓ sdd done — 4/4 tasks · 23m 41s — /run for details"
	if !strings.Contains(got, want) {
		t.Errorf("the finished path is out of scope and must not change:\n got %q\nwant %q", got, want)
	}
}

func TestRunPanelShowsApplyingPhase(t *testing.T) {
	p := session.SDDProgress{
		Active:      true,
		PlanName:    "test",
		TotalTasks:  3,
		CurrentTask: 2,
		Phase:       "applying",
		StartedAt:   time.Now(),
	}
	out := stripANSI(renderRunPanel(p, "⠋", time.Now(), 80))
	if !strings.Contains(out, "applying") {
		t.Errorf("run panel should show 'applying' phase:\n%s", out)
	}
}

func TestRunPanelShowsAgentFallbackPhase(t *testing.T) {
	p := session.SDDProgress{
		Active:      true,
		PlanName:    "test",
		TotalTasks:  3,
		CurrentTask: 2,
		Phase:       "agent fallback",
		StartedAt:   time.Now(),
	}
	out := stripANSI(renderRunPanel(p, "⠋", time.Now(), 80))
	if !strings.Contains(out, "agent fallback") {
		t.Errorf("run panel should show 'agent fallback' phase:\n%s", out)
	}
}

func TestRunPanelOccupiesOneRow(t *testing.T) {
	m := newTestModelInRepo(t)
	m.width, m.height = 100, 40
	m.state.SetSDDProgress(etaTestProgress())

	if got := m.runPanelRows(); got != 1 {
		t.Errorf("runPanelRows() = %d, want 1 — reclaiming transcript rows is the "+
			"reason for this change", got)
	}
}
