package app

import (
	"context"
	"strings"

	"marshal/internal/agent"
	"marshal/internal/tools/native"
	"marshal/internal/watch"
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

// watchAwaitAdapter exposes watch transitions to agent.await through
// agent.WatchAwaitSource.
type watchAwaitAdapter struct{ m *watch.Manager }

func (a watchAwaitAdapter) OutstandingWatches() []agent.AwaitWatchInfo {
	watches := a.m.List()
	out := make([]agent.AwaitWatchInfo, 0, len(watches))
	for _, w := range watches {
		// Only once-mode, still-watching watches can "finish"; repeat
		// watches never do, so await would block forever on them.
		if w.Mode != watch.ModeOnce || w.State != watch.StateWatching {
			continue
		}
		out = append(out, agent.AwaitWatchInfo{
			ID:         w.ID,
			Name:       w.Name,
			Kind:       string(w.Kind),
			State:      string(w.State),
			Condition:  w.Condition,
			Mode:       string(w.Mode),
			FireCount:  w.FireCount,
			LastSample: w.LastSample,
			LastError:  w.LastError,
		})
	}
	return out
}

func (a watchAwaitAdapter) AwaitWatch(ctx context.Context, id string) (agent.AwaitWatchInfo, error) {
	info, err := a.m.WaitFire(ctx, id)
	if err != nil {
		return agent.AwaitWatchInfo{}, err
	}
	return agent.AwaitWatchInfo{
		ID:         info.ID,
		Name:       info.Name,
		Kind:       string(info.Kind),
		State:      string(info.State),
		Condition:  info.Condition,
		Mode:       string(info.Mode),
		FireCount:  info.FireCount,
		LastSample: info.LastSample,
		LastError:  info.LastError,
	}, nil
}
