package bridge

import (
	"time"

	"marshal/web/bridge/contract"
)

// The planner is the web UI's own agent session: one persistent identity, one
// workspace, one backend session, all owned by PlannerService (see
// planner_service.go). This file holds the durable record the planner store
// reads and writes; the wire shapes it is projected into are
// contract/planner.go's (F6 decision 1), aliased below.
//
// Nothing here is a browser-supplied identity. A planner ID, a workspace ID
// and an adapter session ID are all minted by the bridge, so a record is the
// only thing that can claim a workspace.

// Planner access modes. A new planner asks for the permissive mode unless the
// caller names one: a browser that asked for a planner asked for one that can
// do the work, not for a question about every tool. "default" is the
// restricted mode.
const (
	plannerModePermissive = "bypassPermissions"
	plannerModeRestricted = "default"
)

// Planner lifecycle states, persisted on the record. "unknown" is never
// written to a live record: it is what an active record reports when the
// bridge that wrote it is gone.
const (
	plannerStateActive  = "active"
	plannerStateEnded   = "ended"
	plannerStateUnknown = "unknown"
)

// Turn states. A turn is recorded as dispatched before the backend is called,
// so a bridge that dies mid-turn leaves a durable "dispatched" or "running"
// that recovery rewrites to "unknown": the turn is never resent.
const (
	plannerTurnDispatched = "dispatched"
	plannerTurnRunning    = "running"
	plannerTurnDone       = "done"
	plannerTurnCancelled  = "cancelled"
	plannerTurnFailed     = "failed"
	plannerTurnUnknown    = "unknown"
)

// Decision states for one agent request. A request settles exactly once:
// answered by the browser, cancelled when its planner closes, or cancelled
// when the bridge restarts with it still open.
const (
	plannerDecisionPending   = "pending"
	plannerDecisionAnswered  = "answered"
	plannerDecisionCancelled = "cancelled"
)

// PlannerOccupancy is contract.PlannerOccupancy: how a workspace looks to a new
// planner.
type PlannerOccupancy = contract.PlannerOccupancy

// The occupancy states, under the names the bridge's own code reads them by.
const (
	plannerOccupancyFree    = contract.PlannerOccupancyFree
	plannerOccupancyInUse   = contract.PlannerOccupancyInUse
	plannerOccupancyUnknown = contract.PlannerOccupancyUnknown
)

// The planner's wire shapes, declared in contract/planner.go and aliased here
// so no bridge code changed with the move.
type (
	// PlannerArtifact is one reference the planner record carries.
	PlannerArtifact = contract.PlannerArtifact
	// PlannerTurn is one dispatched prompt.
	PlannerTurn = contract.PlannerTurn
	// PlannerDecision is one agent request and its single settlement.
	PlannerDecision = contract.PlannerDecision
	// PlannerView is a planner as the routes report it.
	PlannerView = contract.PlannerView
	// PlannerCreateRequest is the POST /api/planners body.
	PlannerCreateRequest = contract.PlannerCreateRequest
	// PlannerPromptRequest is the POST /api/planners/{id}/prompt body.
	PlannerPromptRequest = contract.PlannerPromptRequest
	// PlannerAnswerRequest is the POST .../requests/{requestId} body.
	PlannerAnswerRequest = contract.PlannerAnswerRequest
	// PlannerTurnReply is what a prompt answers.
	PlannerTurnReply = contract.PlannerTurnReply
	// PlannerEvent is one journaled event.
	PlannerEvent = contract.PlannerEvent
	// PlannerSnapshotArtifact is one piece of text a snapshot froze.
	PlannerSnapshotArtifact = contract.PlannerSnapshotArtifact
	// PlannerSnapshotRequest is the POST .../snapshots body.
	PlannerSnapshotRequest = contract.PlannerSnapshotRequest
	// PlannerRejection names one path a capture refused and why.
	PlannerRejection = contract.PlannerRejection
	// PlannerSnapshotFile is one captured file as a review reports it.
	PlannerSnapshotFile = contract.PlannerSnapshotFile
	// PlannerSnapshotView is a snapshot as the routes report it.
	PlannerSnapshotView = contract.PlannerSnapshotView
	// PlannerSnapshotReply is what a capture answers.
	PlannerSnapshotReply = contract.PlannerSnapshotReply
	// PlannerContextArtifact is one artifact source a briefing found.
	PlannerContextArtifact = contract.PlannerContextArtifact
	// PlannerContextDecision is one settled request a briefing reports.
	PlannerContextDecision = contract.PlannerContextDecision
	// PlannerContextQuestion is one unresolved question a briefing reports.
	PlannerContextQuestion = contract.PlannerContextQuestion
	// PlannerContextView is the bounded briefing a planner record supports.
	PlannerContextView = contract.PlannerContextView
)

// The snapshot shapes below are what a planner handoff freezes for review. A
// snapshot is the user's own selection, captured once and never rewritten: the
// bridge stores the bytes, and the hashes are what a reviewer holds the review
// to. PlannerArtifact above is a reference a planner record carries; a
// PlannerSnapshotArtifact is captured text, which is why the two are separate
// types.

// EditSnapshot is contract.EditSnapshot: the hashed core of a captured
// selection.
type EditSnapshot = contract.EditSnapshot

// SelectedFile is contract.SelectedFile: one repository-relative file a
// snapshot captured.
type SelectedFile = contract.SelectedFile

// PlannerRecord is the durable planner identity: what it is, what it owns and
// where it got to. It carries no secret material — no environment, no
// credential, no prompt text — because it is written to the state directory in
// the clear.
type PlannerRecord struct {
	ID            string `json:"id"`
	ProjectRoot   string `json:"projectRoot"`
	RepoID        string `json:"repoId"`
	BackendID     string `json:"backendId"`
	WorkspaceID   string `json:"workspaceId"`
	SessionID     string `json:"sessionId,omitempty"`
	RequestedMode string `json:"requestedMode"`
	// ConfirmedMode is the mode the backend's set_mode actually accepted. It
	// stays empty when the session did not confirm the requested one, and a
	// prompt is refused until it equals RequestedMode.
	ConfirmedMode string `json:"confirmedMode,omitempty"`
	// Capabilities are the ones the session reported, so a later generation
	// can answer what this planner could do without inventing an answer.
	Capabilities WorkerCapabilities `json:"capabilities,omitempty"`
	// Trust is the project's folder-trust state at creation. The bridge only
	// reports it; it never grants trust.
	Trust string `json:"trust,omitempty"`
	State string `json:"state"`
	// EventSequence is the last journaled event sequence. The journal is the
	// authority; this mirrors it for a reader who only has the record.
	EventSequence uint64 `json:"eventSequence"`
	// PID and Heartbeat are the ownership claim: a record whose owner is this
	// process and whose heartbeat is fresh is live.
	PID       int        `json:"pid"`
	Heartbeat time.Time  `json:"heartbeat"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	Reason    string     `json:"reason,omitempty"`

	Turns     []PlannerTurn     `json:"turns,omitempty"`
	Requests  []PlannerDecision `json:"requests,omitempty"`
	Artifacts []PlannerArtifact `json:"artifacts,omitempty"`
}
