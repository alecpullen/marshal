// internal/app/tui/jobs_test.go — job-count handling in the TUI.
package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/progress"
	"marshal/internal/tools/native"
	"marshal/internal/viewmodel"
)

// runningJob models a job as the job lane receives it: a live snapshot from the
// JobManager's broker, where a background job is attached to no waiting caller.
// An attached command never reaches the lane — its own tool row is its display
// (see Model.runningJobs) — so a fixture without the background attachment
// stands for something the lane is not given.
func runningJob(id int, cmd string, ago time.Duration) native.JobInfo {
	return native.JobInfo{
		ID: fmt.Sprintf("job-%d", id), Command: cmd, Status: native.StatusRunning,
		Attachment: native.AttachmentBackground,
		StartedAt:  time.Now().Add(-ago),
	}
}

// Job exits are recorded agent-side: the JobManager's exit callback (wired in
// app.go) appends the JobExit to the session, so the TUI and ACP sessions both
// see a job's card settle. TestJobManagerOnExitFiresOncePerJob pins the
// append; viewmodel's card tests pin the settled card and its marker.
//
// What remains this layer's job is the repaint: a job finishing while the user
// is idle arrives only as a count change, so handleJobCount must refresh the
// viewport or the change stays invisible until some unrelated event triggers a
// rebuild. The job-exit *row* these tests once asserted no longer exists —
// the exit settles the card that started the job, and a marker records the
// moment — so this pins the refresh itself.
func TestJobCountRepaintsViewport(t *testing.T) {
	m := newTestModel(t)
	m.refreshViewport()
	const notice = "backend gate finished"
	if strings.Contains(ansi.Strip(m.viewport.GetContent()), notice) {
		t.Fatal("nothing should be recorded yet")
	}

	// Stand in for whatever the exit callback appended: the point is that a
	// transcript change that lands with the count message becomes visible.
	m.state.AddMessage(session.RoleSystem, notice, session.ContentTypePlain)

	m2, _ := m.handleJobCount(jobCountMsg{count: 0})
	mm := asModel(t, m2)

	if after := ansi.Strip(mm.viewport.GetContent()); !strings.Contains(after, notice) {
		t.Fatalf("handleJobCount did not repaint the viewport:\n%s", after)
	}
}

// The lane lists detached work only. An attached command is running inside the
// tool call that is waiting for it, which already has its own active row, so
// listing it here would show one command twice; a stopping execution is still
// work, so it stays until it settles.
func TestRunningJobsKeepsDetachedRunningAndStoppingOnly(t *testing.T) {
	m := newTestModel(t)
	now := time.Now()
	m.jobs = []native.JobInfo{
		{ID: "job-1", Command: "attached running", Status: native.StatusRunning,
			Attachment: native.AttachmentForeground, StartedAt: now},
		{ID: "job-2", Command: "detached running", Status: native.StatusRunning,
			Attachment: native.AttachmentBackground, StartedAt: now},
		{ID: "job-3", Command: "detached stopping", Status: native.StatusStopping,
			Attachment: native.AttachmentBackground, StartedAt: now},
		{ID: "job-4", Command: "settled", Status: native.StatusCompleted,
			Attachment: native.AttachmentBackground, StartedAt: now},
		{ID: "job-5", Command: "detached failed", Status: native.StatusFailed,
			Attachment: native.AttachmentBackground, StartedAt: now},
	}

	got := m.runningJobs()
	var ids []string
	for _, j := range got {
		ids = append(ids, j.ID)
	}
	if strings.Join(ids, ",") != "job-2,job-3" {
		t.Fatalf("runningJobs = %v, want the detached running and stopping jobs only", ids)
	}
}

// workProfileCard builds a running card with the named progress profile, so
// every profile variant renders through the same workCardRows path.
func workProfileCard(profile, source string, p *progress.Snapshot) viewmodel.WorkItem {
	return viewmodel.WorkItem{
		TaskKey:        "work:" + profile,
		Kind:           "command",
		Label:          profile + " job",
		SourceID:       profile,
		State:          "running",
		StartedAt:      time.Now().Add(-20 * time.Second),
		ProgressConfig: &progress.Config{Profile: profile, Source: source, Title: "job"},
		Progress:       p,
	}
}

func workIntPtr(v int) *int { return &v }

// Every profile variant renders through one bounded card: the profile-selected
// primary metric, a phase-local label, IEC bytes, a measured rate, and no
// percentage or ETA the producer did not support. Producer-supplied colour and
// layout never reach the row.
func TestWorkCardsGenericTestsDownloadTransferCustom(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name    string
		item    viewmodel.WorkItem
		want    []string
		notWant []string
	}{
		{
			name: "generic",
			item: workProfileCard("generic", "events-v1", &progress.Snapshot{
				Frame: progress.Frame{
					Version: 1, Sequence: 1, PrimaryMetricID: "items",
					Metrics: []progress.Metric{{ID: "items", Label: "Items", Unit: "items", Current: 3, Total: workFloatPtr(10)}},
				},
				ObservedAt: now,
			}),
			want: []string{"3/10 items", "30%"},
		},
		{
			name: "tests",
			item: workProfileCard("tests", "go-test-json", &progress.Snapshot{
				Frame: progress.Frame{
					Version: 1, Sequence: 1, PrimaryMetricID: "passed",
					Metrics: []progress.Metric{
						{ID: "passed", Label: "Passed", Unit: "cases", Current: 12},
						{ID: "failed", Label: "Failed", Unit: "cases", Current: 1},
						{ID: "running", Label: "Running", Unit: "cases", Current: 3},
					},
				},
				ObservedAt: now,
			}),
			want:    []string{"12 Passed", "1 Failed", "3 Running"},
			notWant: []string{"%"},
		},
		{
			name: "download",
			item: workProfileCard("download", "events-v1", &progress.Snapshot{
				Frame: progress.Frame{
					Version: 1, Sequence: 1, PrimaryMetricID: "bytes",
					Metrics: []progress.Metric{{ID: "bytes", Label: "Download", Unit: "bytes", Current: 1.5 * 1024 * 1024, Total: workFloatPtr(10 * 1024 * 1024)}},
				},
				ObservedAt: now,
			}),
			want: []string{"MiB"},
		},
		{
			name: "transfer",
			item: workProfileCard("transfer", "events-v1", &progress.Snapshot{
				Frame: progress.Frame{
					Version: 1, Sequence: 1, PrimaryMetricID: "files",
					Metrics: []progress.Metric{{ID: "files", Label: "Files", Unit: "files", Current: 4, Total: workFloatPtr(20), Rate: workFloatPtr(2)}},
				},
				ObservedAt: now,
			}),
			want: []string{"/s"},
		},
		{
			name: "custom phase label",
			item: workProfileCard("custom", "events-v1", &progress.Snapshot{
				Frame: progress.Frame{
					Version: 1, Sequence: 1, PrimaryMetricID: "units",
					Phase:   &progress.Phase{ID: "sync", Label: "Syncing", Index: 2, Total: 3},
					Metrics: []progress.Metric{{ID: "units", Label: "Units", Unit: "items", Current: 7}},
				},
				ObservedAt: now,
			}),
			want:    []string{"Syncing", "7 items"},
			notWant: []string{"%"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := workCardRows(tc.item, nil, now, 100, nowBarWorkCardExpandedRows)
			if len(rows) == 0 || len(rows) > nowBarWorkCardExpandedRows {
				t.Fatalf("card rows = %d, want 1..%d", len(rows), nowBarWorkCardExpandedRows)
			}
			plain := stripANSI(strings.Join(rows, "\n"))
			for _, w := range tc.want {
				if !strings.Contains(plain, w) {
					t.Errorf("missing %q in:\n%s", w, plain)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(plain, w) {
					t.Errorf("unexpected %q in:\n%s", w, plain)
				}
			}
		})
	}

	// A producer cannot smuggle colour or layout into a card: escapes are
	// stripped before the row is measured or painted by us.
	evil := workProfileCard("generic", "events-v1", &progress.Snapshot{
		Frame: progress.Frame{
			Version: 1, Sequence: 1, PrimaryMetricID: "items",
			Metrics: []progress.Metric{{ID: "items", Label: "Items", Unit: "items", Current: 1, Total: workFloatPtr(2)}},
		},
		ObservedAt: now,
	})
	evil.Label = "\x1b[31mred\x1b[0m command"
	plain := stripANSI(strings.Join(workCardRows(evil, nil, now, 100, nowBarWorkCardRows), "\n"))
	if strings.Contains(plain, "[31m") {
		t.Fatalf("producer escape reached the card:\n%q", plain)
	}
	if !strings.Contains(plain, "red command") {
		t.Fatalf("sanitized label lost its text:\n%q", plain)
	}
}

// An unknown total omits percent and ETA; a partial, stale or reported error is
// named; a stopping item at 100% says the reported work is complete while the
// process still runs; and a failed test row keeps its actual exit code.
func TestWorkCardsUnknownStalePartialAndStopping(t *testing.T) {
	now := time.Now()

	unknown := workProfileCard("download", "events-v1", &progress.Snapshot{
		Frame: progress.Frame{
			Version: 1, Sequence: 1, PrimaryMetricID: "bytes",
			Metrics: []progress.Metric{{ID: "bytes", Label: "Download", Unit: "bytes", Current: 2048}},
		},
		ObservedAt: now,
	})
	plain := stripANSI(strings.Join(workCardRows(unknown, nil, now, 100, nowBarWorkCardExpandedRows), "\n"))
	if !strings.Contains(plain, "2.0 KiB") {
		t.Errorf("unknown total must still show the observed count:\n%s", plain)
	}
	if strings.Contains(plain, "%") || strings.Contains(plain, "~") {
		t.Errorf("unknown total must omit percent and ETA:\n%s", plain)
	}

	stale := workProfileCard("generic", "events-v1", &progress.Snapshot{
		Frame: progress.Frame{
			Version: 1, Sequence: 1, PrimaryMetricID: "items",
			Metrics: []progress.Metric{{ID: "items", Label: "Items", Unit: "items", Current: 1, Total: workFloatPtr(4)}},
		},
		ObservedAt: now.Add(-time.Minute),
	})
	if plain := stripANSI(strings.Join(workCardRows(stale, nil, now, 100, nowBarWorkCardExpandedRows), "\n")); !strings.Contains(plain, "stale measurement") {
		t.Errorf("a stale measurement must be named:\n%s", plain)
	}

	partial := workProfileCard("generic", "events-v1", &progress.Snapshot{
		Frame: progress.Frame{
			Version: 1, Sequence: 1, PrimaryMetricID: "items",
			Metrics: []progress.Metric{{ID: "items", Label: "Items", Unit: "items", Current: 1, Total: workFloatPtr(4)}},
		},
		ObservedAt: now,
		Partial:    true,
	})
	if plain := stripANSI(strings.Join(workCardRows(partial, nil, now, 100, nowBarWorkCardExpandedRows), "\n")); !strings.Contains(plain, "partial output") {
		t.Errorf("a partial snapshot must be named:\n%s", plain)
	}

	stopping := workProfileCard("download", "events-v1", &progress.Snapshot{
		Frame: progress.Frame{
			Version: 1, Sequence: 1, PrimaryMetricID: "bytes",
			Metrics: []progress.Metric{{ID: "bytes", Label: "Download", Unit: "bytes", Current: 4 * 1024 * 1024, Total: workFloatPtr(4 * 1024 * 1024)}},
		},
		ObservedAt: now,
	})
	stopping.State = "stopping"
	plain = stripANSI(strings.Join(workCardRows(stopping, nil, now, 120, nowBarWorkCardExpandedRows), "\n"))
	if !strings.Contains(plain, "reported work complete") || !strings.Contains(plain, "process still running") {
		t.Errorf("100%% while stopping must report the completed count against a running process:\n%s", plain)
	}
	if strings.Contains(plain, "/s") {
		t.Errorf("a stopping item must not advertise a rate:\n%s", plain)
	}

	failedTest := workProfileCard("tests", "go-test-json", &progress.Snapshot{
		Frame: progress.Frame{
			Version: 1, Sequence: 1, PrimaryMetricID: "failed",
			Metrics: []progress.Metric{{ID: "failed", Label: "Failed", Unit: "cases", Current: 1}},
		},
		ObservedAt:    now,
		ReportedError: true,
	})
	failedTest.State = "completed"
	failedTest.ExitCode = workIntPtr(0)
	plain = stripANSI(strings.Join(workCardRows(failedTest, nil, now, 100, nowBarWorkCardExpandedRows), "\n"))
	if !strings.Contains(plain, "exit 0") {
		t.Errorf("a zero exit must be reported as observed, not rewritten:\n%s", plain)
	}
	if !strings.Contains(plain, "reported error") {
		t.Errorf("a failed test run with a zero exit must surface as attention:\n%s", plain)
	}
}
