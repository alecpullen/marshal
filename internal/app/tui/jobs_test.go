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

func runningJob(id int, cmd string, ago time.Duration) native.JobInfo {
	return native.JobInfo{
		ID: fmt.Sprintf("job-%d", id), Command: cmd, Status: native.StatusRunning,
		StartedAt: time.Now().Add(-ago),
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
