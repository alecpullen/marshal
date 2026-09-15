package agent

import "context"

// AwaitJobInfo is the agent package's own snapshot of a background job. The
// app-level adapter converts native.JobInfo into it, so this package never
// imports internal/tools/native (native's in-package tests import agent;
// either import direction closes the known test-binary cycle).
type AwaitJobInfo struct {
	ID       string
	Command  string
	Status   string
	ExitCode *int
}

// JobAwaitSource is the await seam for background shell jobs. Implementations
// live at the wiring site (internal/app), where the JobManager is in scope.
type JobAwaitSource interface {
	// OutstandingJobs returns jobs currently in the running state.
	OutstandingJobs() []AwaitJobInfo
	// AwaitJob blocks until the job reaches a terminal state or ctx ends.
	AwaitJob(ctx context.Context, id string) (AwaitJobInfo, error)
	// JobOutputTail returns a bounded tail of the job's combined output;
	// "" when unknown.
	JobOutputTail(id string, maxLines int) string
}

// AwaitWatchInfo is the agent package's own snapshot of a watch.
type AwaitWatchInfo struct {
	ID         string
	Name       string
	Kind       string
	State      string
	Condition  string
	Mode       string
	FireCount  int
	LastSample string
	LastError  string
}

// WatchAwaitSource is the await seam for watches.
type WatchAwaitSource interface {
	// OutstandingWatches returns once-mode watches that have not yet fired.
	// Repeat-mode watches never finish, so they are never outstanding.
	OutstandingWatches() []AwaitWatchInfo
	// AwaitWatch blocks until the watch fires/stops/errors or ctx ends.
	AwaitWatch(ctx context.Context, id string) (AwaitWatchInfo, error)
}
