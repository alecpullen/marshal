package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

func TestJobCardIsOneLineAndSettles(t *testing.T) {
	ev := registry.AuditEvent{ToolName: "shell.run", Args: []byte(`{"command":"cd /w && pytest -q --junitxml=/tmp/a.xml 2>&1 | tail -40"}`), ResultSummary: "started background job job-4"}
	row := &viewmodel.Node{Kind: viewmodel.KindTool, Tools: []registry.AuditEvent{ev},
		Job: &viewmodel.JobState{ID: "job-4", Command: "pytest -q --junitxml=/tmp/a.xml 2>&1 | tail -40", Running: true}}
	out := ansi.Strip(renderJobCard(row, "⠋", time.Now(), false, 60))
	if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 || !strings.Contains(out, "job-4") || !strings.Contains(out, "running") {
		t.Fatalf("running card = %q", out)
	}
	row.Job.Running, row.Job.ExitCode, row.Job.Duration = false, 1, 122*time.Second
	out = ansi.Strip(renderJobCard(row, "", time.Now(), false, 60))
	if !strings.Contains(out, "exit 1") || !strings.Contains(out, "2m") {
		t.Fatalf("settled card = %q", out)
	}
}

func TestMarkerLine(t *testing.T) {
	m := &viewmodel.MarkerInfo{Labels: []string{"#3 Pass 1 Task 1"}, Dur: 81 * time.Second}
	if out := ansi.Strip(renderMarker(m, 80)); !strings.Contains(out, "#3 Pass 1 Task 1 done") || !strings.Contains(out, "1m") {
		t.Fatalf("marker = %q", out)
	}
	m = &viewmodel.MarkerInfo{Labels: []string{"#3", "#5"}, Failed: true}
	if out := ansi.Strip(renderMarker(m, 80)); !strings.Contains(out, "#3 #5") {
		t.Fatalf("merged marker = %q", out)
	}
}

func TestRunningSubagentCardHasNoPaddingOrFooter(t *testing.T) {
	v := session.SubagentView{ID: 3, Label: "Pass 1 Task 1", Status: session.SubagentRunning, StartedAt: time.Now()}
	out := ansi.Strip(renderSubagentCard(v, false, "⠋", regionView{minRows: 6}, 80))
	if strings.Contains(out, "ctrl+f") {
		t.Fatalf("no drill-in footer:\n%s", out)
	}
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.TrimSpace(strings.Trim(l, "▌┃│ ")) == "" {
			t.Fatalf("no blank padding rows:\n%s", out)
		}
	}
}

func TestSettledSubagentSummaryHasNoMarkdownHeading(t *testing.T) {
	v := session.SubagentView{ID: 2, Label: "Task 2", Status: session.SubagentDone, Summary: "## Task 2 complete — AccessibleModal\n\nDetails."}
	if out := ansi.Strip(renderSubagentCard(v, false, "", regionView{}, 80)); strings.Contains(out, "##") {
		t.Fatalf("markdown leaked:\n%s", out)
	}
}
