package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/tools/native"
)

func runningJob(id int, cmd string, ago time.Duration) native.JobInfo {
	return native.JobInfo{
		ID: fmt.Sprintf("job-%d", id), Command: cmd, Status: native.StatusRunning,
		StartedAt: time.Now().Add(-ago),
	}
}

func TestJobLaneEmptyWhenNoRunningJobs(t *testing.T) {
	m := newTestModel(t)
	if got := m.renderJobLane(); got != "" {
		t.Fatalf("no jobs must render nothing, got %q", got)
	}
	m.jobs = []native.JobInfo{{ID: "job-1", Command: "x", Status: native.StatusCompleted}}
	if got := m.renderJobLane(); got != "" {
		t.Fatalf("only finished jobs must render nothing, got %q", got)
	}
}

func TestJobLaneShowsRunningJobs(t *testing.T) {
	m := newTestModel(t)
	m.jobs = []native.JobInfo{
		runningJob(1, "npm run dev", 4*time.Minute),
		runningJob(2, "go test ./...", 47*time.Second),
	}
	plain := ansi.Strip(m.renderJobLane())
	for _, want := range []string{"job-1", "npm run dev", "job-2", "go test ./..."} {
		if !strings.Contains(plain, want) {
			t.Errorf("lane missing %q:\n%s", want, plain)
		}
	}
}

// The lane is out-of-band work and must not be tinted.
func TestJobLaneIsNotTinted(t *testing.T) {
	m := newTestModel(t)
	m.jobs = []native.JobInfo{runningJob(1, "npm run dev", time.Minute)}
	if strings.Contains(m.renderJobLane(), "48;5;") {
		t.Fatal("the job lane must not be tinted: position already separates it")
	}
}

// jobLaneRows must equal what the lane actually renders, or the height
// budget drifts and pushes the input area off the bottom of the frame.
func TestJobLaneRowsMatchesRender(t *testing.T) {
	m := newTestModel(t)
	for _, n := range []int{0, 1, 2, 4, 9} {
		m.jobs = nil
		for i := 0; i < n; i++ {
			m.jobs = append(m.jobs, runningJob(i+1, "cmd", time.Second))
		}
		out := m.renderJobLane()
		want := 0
		if out != "" {
			want = strings.Count(out, "\n")
		}
		if got := m.jobLaneRows(); got != want {
			t.Fatalf("%d jobs: jobLaneRows()=%d but lane rendered %d rows:\n%s", n, got, want, out)
		}
	}
}

func TestJobLaneCapsWithOverflowRow(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 9; i++ {
		m.jobs = append(m.jobs, runningJob(i+1, "cmd", time.Second))
	}
	out := m.renderJobLane()
	if got := strings.Count(out, "\n"); got > jobLaneMaxRows {
		t.Fatalf("lane rendered %d rows, cap is %d", got, jobLaneMaxRows)
	}
	if !strings.Contains(ansi.Strip(out), "more") {
		t.Fatalf("expected an overflow row:\n%s", ansi.Strip(out))
	}
}

// A job that was running in the previous snapshot and is absent (or no
// longer running) in the next one must be recorded as a JobExit in the
// transcript. This is the TUI-layer test for the diff-based detection in
// handleJobCount.
func TestHandleJobCountRecordsExitForFinishedJob(t *testing.T) {
	m := newTestModel(t)
	started := time.Now().Add(-2 * time.Minute)
	m.jobs = []native.JobInfo{{
		ID: "job-9", Command: "go test ./...", Status: native.StatusRunning, StartedAt: started,
	}}

	code := 3
	m2, _ := m.handleJobCount(jobCountMsg{
		count: 0,
		jobs: []native.JobInfo{{
			ID: "job-9", Command: "go test ./...", Status: native.StatusFailed, StartedAt: started, ExitCode: &code,
		}},
	})
	mm := asModel(t, m2)

	var exits []session.JobExit
	for _, item := range mm.state.Transcript() {
		if item.Kind == session.KindJobExit && item.JobExit != nil {
			exits = append(exits, *item.JobExit)
		}
	}
	if len(exits) != 1 {
		t.Fatalf("expected exactly one job exit, got %d", len(exits))
	}
	if exits[0].ID != "job-9" || exits[0].ExitCode != 3 {
		t.Fatalf("wrong exit recorded: %+v", exits[0])
	}
}

// A job that starts and finishes between two snapshots is still caught:
// the broker publishes on every change, so the running snapshot is always
// observed before the finished one.
func TestHandleJobCountRecordsExitForJobThatFinishedBetweenSnapshots(t *testing.T) {
	m := newTestModel(t)
	started := time.Now().Add(-time.Minute)
	m.jobs = []native.JobInfo{{
		ID: "job-10", Command: "make", Status: native.StatusRunning, StartedAt: started,
	}}

	m2, _ := m.handleJobCount(jobCountMsg{
		count: 0,
		jobs: []native.JobInfo{{
			ID: "job-10", Command: "make", Status: native.StatusCompleted, StartedAt: started,
		}},
	})
	mm := asModel(t, m2)

	var exits []session.JobExit
	for _, item := range mm.state.Transcript() {
		if item.Kind == session.KindJobExit && item.JobExit != nil {
			exits = append(exits, *item.JobExit)
		}
	}
	if len(exits) != 1 {
		t.Fatalf("expected exactly one job exit, got %d", len(exits))
	}
}

// A job that is still running across snapshots must NOT be recorded as an
// exit.
func TestHandleJobCountDoesNotRecordExitForStillRunningJob(t *testing.T) {
	m := newTestModel(t)
	started := time.Now().Add(-time.Minute)
	m.jobs = []native.JobInfo{{
		ID: "job-11", Command: "sleep 30", Status: native.StatusRunning, StartedAt: started,
	}}

	m2, _ := m.handleJobCount(jobCountMsg{
		count: 1,
		jobs: []native.JobInfo{{
			ID: "job-11", Command: "sleep 30", Status: native.StatusRunning, StartedAt: started,
		}},
	})
	mm := asModel(t, m2)

	for _, item := range mm.state.Transcript() {
		if item.Kind == session.KindJobExit {
			t.Fatal("a still-running job must not produce a job exit")
		}
	}
}

// The headline fix: when a job finishes while the user is idle, the exit
// row must appear in the rendered viewport immediately. handleJobCount
// must call refreshViewport, or the cached viewport content never shows
// the new row until some unrelated event triggers a rebuild.
func TestJobExitRepaintsViewport(t *testing.T) {
	m := newTestModel(t)
	started := time.Now().Add(-time.Minute)
	m.jobs = []native.JobInfo{{
		ID: "job-12", Command: "go vet ./...", Status: native.StatusRunning, StartedAt: started,
	}}
	m.refreshViewport()
	before := ansi.Strip(m.viewport.GetContent())
	if strings.Contains(before, "go vet ./...") {
		t.Fatal("the running job should not yet appear as an exit row")
	}

	code := 0
	m2, _ := m.handleJobCount(jobCountMsg{
		count: 0,
		jobs: []native.JobInfo{{
			ID: "job-12", Command: "go vet ./...", Status: native.StatusCompleted, StartedAt: started, ExitCode: &code,
		}},
	})
	mm := asModel(t, m2)

	after := ansi.Strip(mm.viewport.GetContent())
	if !strings.Contains(after, "go vet ./...") || !strings.Contains(after, "exit 0") {
		t.Fatalf("job exit row did not repaint into the viewport:\n%s", after)
	}
}
