package tui

import (
	"marshal/internal/tools/native"
	"marshal/internal/watch"
)

// runningJobs returns the currently running background commands in the order
// the lane renders them. Detached work only: an attached command is running
// inside its own tool call, which the transcript already shows as an active
// row, so listing it here would double-count one command. A stopping execution
// is still work — its process is being terminated, not gone — so it stays in
// the lane until it settles. Jobs are deliberately not rendered as bounded live
// regions in the transcript: they outlive the turn that spawned them, so a
// row anchored where they were spawned misrepresents their lifetime.
func (m Model) runningJobs() []native.JobInfo {
	running := make([]native.JobInfo, 0, len(m.jobs))
	for _, j := range m.jobs {
		if j.Status.Terminal() || j.Attachment != native.AttachmentBackground {
			continue
		}
		running = append(running, j)
	}
	return running
}

// runningWatches returns the watches in the order the lane renders them.
// Watches in the watching state are shown; terminal states (fired/stopped/
// error) are kept so the lane can show the last known state. The manager
// never publishes a removal event, so terminal rows persist for the session
// (the lane keeps showing the last known state rather than dropping the
// row).
func (m Model) runningWatches() []watch.Event {
	return m.watches
}
