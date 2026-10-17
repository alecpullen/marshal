package agent

import (
	"context"

	"marshal/internal/app/session"
)

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

// ExecutionControl is the runner-facing execution-control seam: the narrow
// surface the agent uses to give an explicit stop precise targets. It is
// separate from JobAwaitSource (which observes jobs) and is injected by app
// construction at the wiring site, where the command service is in scope.
//
// The split is deliberate. A turn stop must reach the command service's
// admission/detachment boundary BEFORE the agent's own context is cancelled
// (shared contract 3), so the runner needs a way to name its operation's stop
// group and ask for the stop — not a general job-management API.
type ExecutionControl interface {
	// BeginOperation returns the stop group for actor. When a parent entry
	// point already registered one for the operation in flight, that
	// registration is returned so the turn reuses it instead of orphaning the
	// target the user's stop will name. owned reports whether THIS call created
	// the group, so the caller clears only its own registration.
	BeginOperation(actor string) (token *session.TurnStopToken, owned bool)
	// StopOperation accepts an explicit stop of the group's attached commands
	// and records the group's turn as stopped. It returns the affected
	// execution ids. Implementations must ask the service first and only then
	// let the caller cancel its agent waits.
	StopOperation(token *session.TurnStopToken, cause string) ([]string, error)
	// ReleaseOperation clears the registration when token is still the current
	// group, so a finishing operation cannot unregister a newer one. Ordinary
	// cleanup calls this and never requests execution stop.
	ReleaseOperation(token *session.TurnStopToken) bool
	// StopExecution requests termination of one execution without stopping a
	// whole group: the targeted form used for a single child or job.
	StopExecution(executionID, cause string) error
}

// SessionExecutionControl adapts a conversation's session state to the
// runner-facing ExecutionControl seam. It needs only the session: the
// conversation's command service is installed on it by app construction
// (session.State.SetTurnStopService), so allocation, re-use of a parent
// operation's registration, release and targeted stops all go through one
// owner.
//
// A runner built directly (tests, embedded callers) may carry nil; the runner
// then registers no group and a turn stop reaches only its own waits, which is
// the pre-feature behaviour.
func NewSessionExecutionControl(state *session.State) ExecutionControl {
	return sessionExecutionControl{state: state}
}

type sessionExecutionControl struct {
	state *session.State
}

var _ ExecutionControl = sessionExecutionControl{}

// BeginOperation reuses the session's current registration when there is one —
// a parent entry point registered before dispatch, and the turn must reuse it —
// and otherwise allocates one so a runner used directly still has a target.
func (c sessionExecutionControl) BeginOperation(actor string) (*session.TurnStopToken, bool) {
	if c.state == nil {
		return nil, false
	}
	if token := c.state.CurrentTurnStop(); token != nil {
		return token, false
	}
	token, _ := c.state.BeginTurnStop(actor)
	return token, true
}

func (c sessionExecutionControl) StopOperation(token *session.TurnStopToken, cause string) ([]string, error) {
	return token.Stop(cause)
}

func (c sessionExecutionControl) ReleaseOperation(token *session.TurnStopToken) bool {
	if c.state == nil {
		return false
	}
	return c.state.EndTurnStop(token)
}

func (c sessionExecutionControl) StopExecution(executionID, cause string) error {
	if c.state == nil {
		return session.ErrNoTurnStopService
	}
	return c.state.StopExecution(executionID, cause)
}

// NewSyntheticExecutionControl returns the control a synthetic worker uses — a
// review subagent, a pipeline role runner, an SDD plan author. Such a worker
// runs synchronously under a parent operation, so it INHERITS the parent's
// registered stop group when there is one: the stop the user's keystroke names
// is the parent's, and every command the worker launches must be reachable
// through it. When the parent has no registration (a worker started outside a
// turn), the worker allocates its own group from the parent's command service so
// its commands still have a stop target, without claiming the parent's slot.
//
// Asynchronous agent.run children deliberately do NOT use this: they get their
// own dedicated group (subagent.go) because they outlive the parent turn.
func NewSyntheticExecutionControl(parent *session.State) ExecutionControl {
	return syntheticExecutionControl{parent: parent}
}

type syntheticExecutionControl struct {
	parent *session.State
}

var _ ExecutionControl = syntheticExecutionControl{}

func (c syntheticExecutionControl) BeginOperation(actor string) (*session.TurnStopToken, bool) {
	if c.parent == nil {
		return nil, false
	}
	if token := c.parent.CurrentTurnStop(); token != nil {
		return token, false
	}
	return session.NewTurnStopToken(c.parent.GetTurnStopService(), actor), true
}

func (c syntheticExecutionControl) StopOperation(token *session.TurnStopToken, cause string) ([]string, error) {
	return token.Stop(cause)
}

// ReleaseOperation clears the parent's registration when the token is the
// parent's current one, and reports false for a self-allocated token (which was
// never registered on the session).
func (c syntheticExecutionControl) ReleaseOperation(token *session.TurnStopToken) bool {
	if c.parent == nil {
		return false
	}
	return c.parent.EndTurnStop(token)
}

func (c syntheticExecutionControl) StopExecution(executionID, cause string) error {
	if c.parent == nil {
		return session.ErrNoTurnStopService
	}
	return c.parent.StopExecution(executionID, cause)
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
