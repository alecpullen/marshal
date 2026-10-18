// internal/app/tui/jobs_test.go — job-count handling in the TUI.
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
