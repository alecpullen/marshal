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
	// PeekJob returns the job's current state and a bounded output tail
	// without waiting.
	PeekJob(id string, tailLines int) (AwaitJobInfo, string, error)
	// KillJob terminates a running background job.
	KillJob(id string) error
}
