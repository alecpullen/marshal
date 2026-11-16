package bridge

import (
	"context"
	"encoding/json"
	"errors"
)

// Worker backends are the seam under the fleet's planner: a process, a
// container or a remote agent that the planner owns and drives. The planner
// service is the sole owner of each process and session — a backend never
// outlives the service that started it and never talks to a browser directly.
// Everything a backend observes leaves through the instance-owned sinks below,
// under stable session and request IDs, with an ordered event sequence, bounded
// message content, and explicit settlement.
//
// The seam stays deliberately small. A capability a backend has not proven is
// absent rather than faked, and the interface does not imply a generic
// session-load capability until a backend can actually do it.

// ErrWorkerUnsupported reports a WorkerBackend method the backend does not
// implement. It is a capability answer, not a failure: the planner must not
// treat it as a broken worker, and it must not retry the call to find out
// again.
var ErrWorkerUnsupported = errors.New("worker: unsupported")

// ErrWorkerUncertain reports a dispatched turn whose outcome the backend could
// not observe. It is never success: callers must not read a zero-valued
// result as a finished turn, and the planner never automatically retries it,
// because the turn may already have changed the workspace.
var ErrWorkerUncertain = errors.New("worker: uncertain outcome")

// WorkerCapabilities is what a running worker session can actually do. It is
// per session rather than per backend: the same backend can offer workspace
// edits in one session and merely prompt in another.
type WorkerCapabilities struct {
	Prompt             bool     `json:"prompt"`
	Cancel             bool     `json:"cancel"`
	PermissionRequests bool     `json:"permissionRequests"`
	SessionLoad        bool     `json:"sessionLoad"`
	WorkspaceEdits     bool     `json:"workspaceEdits"`
	Modes              []string `json:"modes"`
}

// WorkerSessionSpec is what the planner asks a backend to start. CWD is the
// resolved working directory on this host and is never part of the wire form.
type WorkerSessionSpec struct {
	WorkerID    string `json:"workerId"`
	ProjectRoot string `json:"projectRoot"`
	WorkspaceID string `json:"workspaceId"`
	CWD         string `json:"-"`
	Mode        string `json:"mode"`
}

// WorkerTurnResult is the outcome of one dispatched turn.
type WorkerTurnResult struct {
	StopReason string `json:"stopReason"`
}

// WorkerBackend drives one provider of worker sessions. Start returns the
// stable session ID the planner and the browser both use, together with the
// capabilities that session actually has.
type WorkerBackend interface {
	Start(context.Context, WorkerSessionSpec) (string, WorkerCapabilities, error)
	Prompt(context.Context, string, string) (WorkerTurnResult, error)
	Cancel(context.Context, string) error
	Close(context.Context, string) error
}

// workerEventSink receives one ordered backend notification. sequence is
// monotonic per backend; data is bounded JSON the planner journals as-is.
type workerEventSink func(sessionID string, sequence uint64, kind string, data json.RawMessage)

// workerRequestSink delivers one agent request to the planner and returns the
// browser's answer. It is answered once: a caller that never gets an answer
// does not get to settle the request by disconnecting.
type workerRequestSink func(ctx context.Context, sessionID string, requestID string, method string, params json.RawMessage) (json.RawMessage, error)
