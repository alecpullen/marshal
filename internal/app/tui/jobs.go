package tui

import (
	"marshal/internal/tools/native"
)

// runningJobs returns the currently running background jobs in the order
// the lane renders them. Jobs are deliberately not rendered as bounded live
// regions in the transcript: they outlive the turn that spawned them, so a
// row anchored where they were spawned misrepresents their lifetime.
func (m Model) runningJobs() []native.JobInfo {
	running := make([]native.JobInfo, 0, len(m.jobs))
	for _, j := range m.jobs {
		if j.Status == native.StatusRunning {
			running = append(running, j)
		}
	}
	return running
}
