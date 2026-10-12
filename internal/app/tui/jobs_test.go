// internal/app/tui/jobs_test.go — TestJobExitRepaintsViewport
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

// Job exits are recorded agent-side now: the JobManager's exit callback
// (wired in app.go) appends the JobExit to the session, so the TUI and ACP
// sessions both see a job's card settle. The TUI's job-count handler only
// refreshes its cached snapshot.
//
// What remains this layer's job is the repaint: an exit appended by the
// agent-side callback must become visible when the count change arrives,
// even while the user is idle (no tick, notice, or pulse would otherwise
// trigger a rebuild). The deleted diff-tests
// (TestHandleJobCountRecordsExitForFinishedJob and friends) pinned the old
// client-side diff, which no longer exists; the append itself is covered by
// TestJobManagerOnExitFiresOncePerJob in internal/tools/native.
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

	// The agent-side callback records the exit, then its count change lands
	// here.
	m.state.AddJobExit(session.JobExit{
		ID: "job-12", Command: "go vet ./...", ExitCode: 0, Duration: time.Minute,
	})

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
