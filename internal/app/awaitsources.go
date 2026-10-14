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

func (a jobAwaitAdapter) OutstandingJobs() []agent.AwaitJobInfo {
	jobs := a.m.Outstanding()
	out := make([]agent.AwaitJobInfo, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, agent.AwaitJobInfo{
			ID:       j.ID,
			Command:  j.Command,
			Status:   string(j.Status),
			ExitCode: j.ExitCode,
		})
	}
	return out
}

func (a jobAwaitAdapter) AwaitJob(ctx context.Context, id string) (agent.AwaitJobInfo, error) {
	info, err := a.m.Wait(ctx, id)
	if err != nil {
		return agent.AwaitJobInfo{}, err
	}
	return agent.AwaitJobInfo{
		ID:       info.ID,
		Command:  info.Command,
		Status:   string(info.Status),
		ExitCode: info.ExitCode,
	}, nil
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
	return agent.AwaitJobInfo{ID: info.ID, Command: info.Command, Status: string(info.Status), ExitCode: info.ExitCode}, strings.TrimRight(out, "\n"), nil
}

func (a jobAwaitAdapter) KillJob(id string) error { return a.m.Kill(id) }
