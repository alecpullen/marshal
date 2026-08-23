package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

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
