package livebox

import (
	"marshal/internal/acpwire"
)

// The live box is a client of the wire projection, so it reads work items as
// they arrive rather than the renderer's own copy of them. These two
// predicates are the ones the box needs, written over the wire item with the
// same literals viewmodel uses; work_test.go pins them equal to it.

// WorkActive reports whether an item belongs in the pinned live membership.
// It is viewmodel.WorkActive over the wire item.
func WorkActive(it acpwire.WorkItem) bool {
	if it.Kind == "command" && it.Attachment == "foreground" {
		return false
	}
	switch it.State {
	case "running", "stopping", "watching", "blocked":
		return true
	}
	return false
}

// WorkNeedsAttention reports whether a settled item should collapse to
// compact attention. It is viewmodel.WorkNeedsAttention over the wire item.
func WorkNeedsAttention(it acpwire.WorkItem) bool {
	switch it.State {
	case "completed", "failed", "stopped", "unknown":
	default:
		return false
	}
	switch it.State {
	case "failed", "unknown":
		return true
	}
	switch it.Outcome {
	case "startup_failure", "lost":
		return true
	}
	if it.Attention != "" {
		return true
	}
	if it.Progress != nil && it.Progress.ReportedError {
		return true
	}
	return false
}
