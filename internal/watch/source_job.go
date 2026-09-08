package watch

import (
	"context"
	"fmt"

	"marshal/internal/pubsub"
	"marshal/internal/tools/native"
)

// sampleJob is the synchronous state check used at job-watch registration. It
// resolves the watched job's current status and returns a Sample whose Stdout
// is the report text `status=<s> exit=<n>`.
func (s sourceSampler) sampleJob(ctx context.Context, w *watch) (Sample, error) {
	if s.deps.JobLookup == nil {
		return Sample{}, fmt.Errorf("job watch %q: job lookup not configured", w.name)
	}
	info, ok := s.deps.JobLookup(w.jobID)
	if !ok {
		return Sample{}, fmt.Errorf("job watch %q: unknown job ID %q", w.name, w.jobID)
	}
	return jobSample(info), nil
}

// jobSample builds the report text for a job's current state.
func jobSample(info native.JobInfo) Sample {
	s := fmt.Sprintf("status=%s", info.Status)
	if info.ExitCode != nil {
		s += fmt.Sprintf(" exit=%d", *info.ExitCode)
	}
	return Sample{Stdout: s}
}

// isTerminal reports whether a job status is a terminal state.
func isTerminal(s native.JobStatus) bool {
	switch s {
	case native.StatusCompleted, native.StatusFailed, native.StatusTimedOut, native.StatusKilled:
		return true
	default:
		return false
	}
}

// checkJobTerminal performs the synchronous initial state check at job-watch
// goroutine start. It returns true when the watch is done (the job was already
// terminal and fired, or the lookup failed).
func (m *Manager) checkJobTerminal(w *watch) bool {
	if m.deps.JobLookup == nil {
		m.handleSampleError(w, fmt.Errorf("job watch %q: job lookup not configured", w.name))
		return true
	}
	info, ok := m.deps.JobLookup(w.jobID)
	if !ok {
		m.handleSampleError(w, fmt.Errorf("job watch %q: unknown job ID %q", w.name, w.jobID))
		return true
	}
	if isTerminal(info.Status) {
		m.fire(w, jobSample(info))
		return true
	}
	return false
}

// checkJobTerminalFromEvent inspects a JobEvent snapshot for the watched job
// and fires if it is terminal. Returns true when the watch is done.
func (m *Manager) checkJobTerminalFromEvent(w *watch, ev pubsub.Event[native.JobEvent]) bool {
	for _, info := range ev.Payload.Jobs {
		if info.ID != w.jobID {
			continue
		}
		if isTerminal(info.Status) {
			m.fire(w, jobSample(info))
			return true
		}
		return false
	}
	return false
}
