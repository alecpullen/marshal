package viewmodel

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/progress"
)

func TestJobCompletionOutcomes(t *testing.T) {
	zero, three := 0, 3
	cases := []struct {
		name    string
		exit    *session.JobExit
		outcome string
		failed  bool
	}{
		{"clean exit", &session.JobExit{ID: "job-1", Exit: &zero, ExitCode: 0, State: "completed", At: at(5)}, OutcomeDone, false},
		{"killed with a zero exit", &session.JobExit{ID: "job-1", Exit: &zero, ExitCode: 0, State: "killed", At: at(5)}, "exit 0", true},
		{"nonzero exit", &session.JobExit{ID: "job-1", Exit: &three, ExitCode: 3, At: at(5)}, "exit 3", true},
		{"stopped", &session.JobExit{ID: "job-1", ExitCode: -1, StopCause: "turn stop", At: at(5)}, OutcomeStopped, true},
		{"failed state", &session.JobExit{ID: "job-1", ExitCode: -1, State: "failed", At: at(5)}, OutcomeFailed, true},
		{"legacy unknown", &session.JobExit{ID: "job-1", ExitCode: -1, At: at(5)}, OutcomeUnknown, true},
		{"lost", &session.JobExit{ID: "job-1", ExitCode: -1, Lost: true, At: at(5)}, OutcomeLost, true},
	}
	for _, tc := range cases {
		c := jobCompletion(tc.exit, nil)
		if c.Outcome != tc.outcome || c.Failed != tc.failed {
			t.Errorf("%s: outcome = %q failed = %v, want %q %v", tc.name, c.Outcome, c.Failed, tc.outcome, tc.failed)
		}
		if tc.exit.Lost && !c.EndedAt.IsZero() {
			t.Errorf("%s: a lost job has no end", tc.name)
		}
	}
}

func TestSubagentCompletionOutcomes(t *testing.T) {
	done := subagentCompletion(&session.SubagentView{ID: 3, Label: "review", Status: session.SubagentDone,
		StartedAt: at(1), EndedAt: at(4), Summary: "Looks right."}, NodeID{})
	if done.Outcome != OutcomeDone || done.Failed || done.ID != "#3" || done.Duration != 3*time.Second {
		t.Fatalf("done = %+v", done)
	}
	if done.Summary != "Looks right." {
		t.Fatalf("summary = %q", done.Summary)
	}

	failed := subagentCompletion(&session.SubagentView{ID: 5, Label: "review", Status: session.SubagentFailed,
		StartedAt: at(1), EndedAt: at(4), Error: "budget exhausted\nsecond line"}, NodeID{})
	if failed.Outcome != OutcomeFailed || !failed.Failed || failed.Summary != "budget exhausted" {
		t.Fatalf("failed = %+v", failed)
	}

	stopped := subagentCompletion(&session.SubagentView{ID: 7, Label: "review", Status: session.SubagentDone,
		StartedAt: at(1), EndedAt: at(4), SubagentWorkFacts: session.SubagentWorkFacts{Outcome: "stopped"}}, NodeID{})
	if stopped.Outcome != OutcomeStopped || !stopped.Failed {
		t.Fatalf("stopped = %+v", stopped)
	}
}

func TestMeasureSummary(t *testing.T) {
	total := func(v float64) *float64 { return &v }
	cases := []struct {
		name string
		snap *progress.Snapshot
		want string
	}{
		{"nil", nil, ""},
		{"go test counts", &progress.Snapshot{Frame: progress.Frame{Metrics: []progress.Metric{
			{ID: "passed", Label: "Passed", Current: 210},
			{ID: "failed", Label: "Failed", Current: 2},
			{ID: "skipped", Label: "Skipped", Current: 0},
			{ID: "running", Label: "Running", Current: 0},
		}}}, "210 passed, 2 failed"},
		{"bytes with a total", &progress.Snapshot{Frame: progress.Frame{Metrics: []progress.Metric{
			{ID: "bytes", Unit: "bytes", Current: 1536, Total: total(4096)},
		}}}, "1.5 KiB/4.0 KiB"},
		{"count with a total", &progress.Snapshot{Frame: progress.Frame{Metrics: []progress.Metric{
			{ID: "files", Unit: "files", Current: 3, Total: total(7)},
		}}}, "3/7 files"},
		{"bytes without a total", &progress.Snapshot{Frame: progress.Frame{Metrics: []progress.Metric{
			{ID: "bytes", Unit: "bytes", Current: 512},
		}}}, "512 B"},
		{"phase and count", &progress.Snapshot{Frame: progress.Frame{
			Phase:   &progress.Phase{Index: 2, Total: 3, Label: "verify"},
			Metrics: []progress.Metric{{ID: "files", Unit: "files", Current: 3, Total: total(7)}},
		}}, "2/3 verify · 3/7 files"},
	}
	for _, tc := range cases {
		if got := MeasureSummary(tc.snap); got != tc.want {
			t.Errorf("%s: MeasureSummary = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestJobCompletionSummary(t *testing.T) {
	fail := &session.JobExit{ID: "job-1", ExitCode: 1, At: at(5),
		Output:   "ok  \tfoo\n--- FAIL: TestX\nFAIL\n",
		Progress: &progress.Snapshot{Frame: progress.Frame{Detail: "go test ./..."}}}
	if got := jobCompletion(fail, nil).Summary; got != "--- FAIL: TestX" {
		t.Errorf("the failure line wins: %q", got)
	}

	detail := &session.JobExit{ID: "job-1", ExitCode: 0, At: at(5),
		Output:   "line one\nline two\n",
		Progress: &progress.Snapshot{Frame: progress.Frame{Detail: "compiling"}}}
	if got := jobCompletion(detail, nil).Summary; got != "compiling" {
		t.Errorf("the detail wins over the last line: %q", got)
	}

	goTest := &session.JobExit{ID: "job-1", ExitCode: 0, At: at(5),
		Output:   "line one\nline two\n",
		Progress: &progress.Snapshot{Frame: progress.Frame{Detail: "go test"}}}
	if got := jobCompletion(goTest, nil).Summary; got != "line two" {
		t.Errorf("go test is not a detail: %q", got)
	}

	truncated := &session.JobExit{ID: "job-1", ExitCode: 0, At: at(5),
		Output: "line one\n[output truncated]\n"}
	if got := jobCompletion(truncated, nil).Summary; got != "line one" {
		t.Errorf("a truncation marker is not a summary: %q", got)
	}
}

func TestReportHeadlineStripsAHeading(t *testing.T) {
	if got := ReportHeadline("## Task 2 complete. Details follow."); got != "Task 2 complete." {
		t.Errorf("ReportHeadline = %q", got)
	}
	if got := ReportHeadline("#3 done"); got != "#3 done" {
		t.Errorf("ReportHeadline = %q, want the number kept", got)
	}
}

func TestReportTailPutsFailuresFirst(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	lines[5] = "--- FAIL: TestA"
	lines[20] = "--- FAIL: TestB"
	got := ReportTail(strings.Join(lines, "\n"))
	if len(got) != ReportTailLines {
		t.Fatalf("lines = %d, want %d", len(got), ReportTailLines)
	}
	if got[0] != "--- FAIL: TestA" || got[1] != "--- FAIL: TestB" {
		t.Fatalf("failures first: %q", got[:2])
	}
	// The rest are the last 18 lines that are not failures, in order.
	if got[2] != "line 11" || got[len(got)-1] != "line 29" {
		t.Fatalf("tail = %q", got[2:])
	}
}
