// Package worker defines the minimal lifecycle contract for supervised,
// long-lived background duties. app.Run starts each Worker in its own goroutine
// bound to the shutdown context. This is the seam a future background-task
// subsystem (supervisor/registry, agent dispatch, scheduling) will build on;
// for now the index watcher is the only implementation.
package worker

import "context"

type Worker interface {
	// Name identifies the worker in logs.
	Name() string
	// Run blocks until ctx is cancelled, performing the background duty. It
	// returns nil on clean shutdown, or an error on abnormal termination.
	Run(ctx context.Context) error
}
