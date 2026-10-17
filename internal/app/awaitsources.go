package app

import (
	"context"
	"strings"

	"marshal/internal/agent"
	"marshal/internal/tools/native"
)

// jobAwaitAdapter exposes the native JobManager to agent.await through
// agent.JobAwaitSource, converting JobInfo into the agent package's own
// struct so internal/agent never imports internal/tools/native (its
// in-package tests import agent; either direction closes the cycle).
type jobAwaitAdapter struct{ m *native.JobManager }

// awaitJobInfo converts one manager snapshot into the agent's own view. Status,
// exit and stop cause all travel: an await result has to be able to say what a
// job is doing, whether it exited, and why it stopped, without the caller
// guessing from the exit code alone.
func awaitJobInfo(info native.JobInfo) agent.AwaitJobInfo {
	return agent.AwaitJobInfo{
		ID:        info.ID,
		Command:   info.Command,
		Status:    string(info.Status),
		ExitCode:  info.ExitCode,
		StopCause: info.StopCause,
	}
}

// OutstandingJobs reports the detached running/stopping jobs. It reads
// JobManager.Outstanding deliberately: a foreground command attached to its
// waiting call is not background work, so it must not become an extra target
// for any/all while its own call is still blocked on it.
func (a jobAwaitAdapter) OutstandingJobs() []agent.AwaitJobInfo {
	jobs := a.m.Outstanding()
	out := make([]agent.AwaitJobInfo, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, awaitJobInfo(j))
	}
	return out
}

func (a jobAwaitAdapter) AwaitJob(ctx context.Context, id string) (agent.AwaitJobInfo, error) {
	info, err := a.m.Wait(ctx, id)
	if err != nil {
		return agent.AwaitJobInfo{}, err
	}
	return awaitJobInfo(info), nil
}

func (a jobAwaitAdapter) JobOutputTail(id string, maxLines int) string {
	// The Output error is swallowed deliberately: an unknown job already
	// errors the await itself via AwaitJob, and an empty tail simply means
	// the await result omits its output-tail section.
	_, out, err := a.m.Output(id, maxLines)
	if err != nil {
		return ""
	}
	return strings.TrimRight(out, "\n")
}

func (a jobAwaitAdapter) PeekJob(id string, tailLines int) (agent.AwaitJobInfo, string, error) {
	info, out, err := a.m.Output(id, tailLines)
	if err != nil {
		return agent.AwaitJobInfo{}, "", err
	}
	return awaitJobInfo(info), strings.TrimRight(out, "\n"), nil
}

func (a jobAwaitAdapter) KillJob(id string) error { return a.m.Kill(id) }
