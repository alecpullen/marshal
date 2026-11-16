package bridge

import (
	"encoding/json"
	"time"

	"marshal/web/bridge/contract"
)

// The planner is the web UI's own agent session: one persistent identity, one
// workspace, one backend session, all owned by PlannerService (see
// planner_service.go). This file holds the durable record and the wire shapes;
// planner_store.go owns their on-disk form.
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

// PlannerOccupancy is how a workspace looks to a new planner. "free" is the
// absence of any planner record naming it, "in_use" is a live owner, and
// "unknown" is an owner that is neither live nor gone; ownership is never
// inferred from a missing record.
type PlannerOccupancy string

const (
	plannerOccupancyFree    PlannerOccupancy = "free"
	plannerOccupancyInUse   PlannerOccupancy = "in_use"
	plannerOccupancyUnknown PlannerOccupancy = "unknown"
)

// PlannerArtifact is a reference to something the planner produced. This task
// persists the references only: capturing their content belongs to the
// snapshot work that extends this record, and nothing here pretends a planner
// record is a cross-project knowledge store.
type PlannerArtifact struct {
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	Label string `json:"label,omitempty"`
}

// PlannerTurn is one dispatched prompt. TextHash is the digest of the text the
// request ID was first used for, so a replay of the same request ID can be
// recognised without storing the prompt twice.
type PlannerTurn struct {
	RequestID  string    `json:"requestId"`
	TextHash   string    `json:"textHash"`
	State      string    `json:"state"`
	StopReason string    `json:"stopReason,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	At         time.Time `json:"at"`
}

// PlannerDecision is one agent request and its single settlement. The chosen
// response is kept verbatim so a browser that reconnects learns what was
// decided rather than asking again.
type PlannerDecision struct {
	RequestID string          `json:"requestId"`
	Method    string          `json:"method,omitempty"`
	State     string          `json:"state"`
	Response  json.RawMessage `json:"response,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	At        time.Time       `json:"at"`
}

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

// PlannerView is a planner as the routes report it: the record projected for
// the browser, plus what this bridge can say about it right now. Resume is
// reported separately from the capability so an absent capability is never
// read as an offer.
type PlannerView struct {
	ID            string             `json:"id"`
	ProjectRoot   string             `json:"projectRoot"`
	RepoID        string             `json:"repoId"`
	BackendID     string             `json:"backendId"`
	WorkspaceID   string             `json:"workspaceId"`
	Occupancy     PlannerOccupancy   `json:"occupancy"`
	Owners        []string           `json:"owners,omitempty"`
	SessionID     string             `json:"sessionId,omitempty"`
	RequestedMode string             `json:"requestedMode"`
	ConfirmedMode string             `json:"confirmedMode,omitempty"`
	ModeConfirmed bool               `json:"modeConfirmed"`
	State         string             `json:"state"`
	Live          bool               `json:"live"`
	EventSequence uint64             `json:"eventSequence"`
	Capabilities  WorkerCapabilities `json:"capabilities"`
	SessionLoad   bool               `json:"sessionLoad"`
	// ResumeAvailable is true only for a backend that can load a session and a
	// replay this bridge actually performed. It is false today, with
	// ResumeReason saying why, so the browser offers a new planner instead.
	ResumeAvailable bool              `json:"resumeAvailable"`
	ResumeReason    string            `json:"resumeReason,omitempty"`
	Trust           string            `json:"trust,omitempty"`
	Turns           []PlannerTurn     `json:"turns,omitempty"`
	Requests        []PlannerDecision `json:"requests,omitempty"`
	Artifacts       []PlannerArtifact `json:"artifacts,omitempty"`
	Reason          string            `json:"reason,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
	EndedAt         *time.Time        `json:"endedAt,omitempty"`
}

// PlannerCreateRequest is the POST /api/planners body. WorkspaceID names a
// workspace the bridge returned earlier; a filesystem path is never accepted
// in its place. ProceedShared is what a caller sets after seeing an in_use or
// unknown occupancy; it is never implied.
type PlannerCreateRequest struct {
	ProjectRoot   string `json:"projectRoot"`
	RepoID        string `json:"repoId"`
	WorkspaceID   string `json:"workspaceId,omitempty"`
	ProceedShared bool   `json:"proceedShared,omitempty"`
	Mode          string `json:"mode,omitempty"`
}

// PlannerPromptRequest is the POST /api/planners/{id}/prompt body. Context is
// the additional bounded text a user supplied alongside the prompt (see the
// planner's context briefing): it is composed into the dispatched text after
// the prompt's own text, and the journal keeps only its hash and length.
type PlannerPromptRequest struct {
	RequestID string `json:"requestId"`
	Text      string `json:"text"`
	Context   string `json:"context,omitempty"`
}

// PlannerAnswerRequest is the POST /api/planners/{id}/requests/{requestId}
// body: the chosen response, passed through to the backend unchanged.
type PlannerAnswerRequest struct {
	Response json.RawMessage `json:"response"`
}

// PlannerTurnReply is what a prompt answers: the dispatch of a new turn, or
// the outcome already recorded for a replayed request ID.
type PlannerTurnReply struct {
	RequestID  string `json:"requestId"`
	State      string `json:"state"`
	StopReason string `json:"stopReason,omitempty"`
}

// PlannerEvent is one journaled event. Sequence is the planner's own strictly
// increasing counter, not the backend's; Truncated marks a payload that
// exceeded the journal's per-event bound and was replaced by its marker.
type PlannerEvent struct {
	Sequence  uint64          `json:"sequence"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
}

// The snapshot shapes below are what a planner handoff freezes for review. A
// snapshot is the user's own selection, captured once and never rewritten: the
// bridge stores the bytes, and the hashes are what a reviewer holds the review
// to. PlannerArtifact above is a reference a planner record carries; a
// PlannerSnapshotArtifact is captured text, which is why the two are separate
// types.

// PlannerSnapshotArtifact is one piece of text a snapshot froze: the plan or
// the spec the user approved. Its content is stored with the snapshot record
// and its SHA256 is verified on every read, so a review never shows bytes that
// differ from the hash it reports.
type PlannerSnapshotArtifact struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
}

// EditSnapshot is contract.EditSnapshot: the hashed core of a captured
// selection.
type EditSnapshot = contract.EditSnapshot

// SelectedFile is contract.SelectedFile: one repository-relative file a
// snapshot captured.
type SelectedFile = contract.SelectedFile

// PlannerSnapshotRequest is the POST /api/planners/{id}/snapshots body: the
// exact plan and spec text the user approved, and the repository-relative
// paths they selected. An empty Paths list is a documents-only snapshot.
type PlannerSnapshotRequest struct {
	Plan  string   `json:"plan"`
	Spec  string   `json:"spec"`
	Paths []string `json:"paths"`
}

// PlannerRejection names one path a capture refused and why. A selected path
// is either captured or reported here; it is never silently dropped.
type PlannerRejection struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// PlannerSnapshotFile is one captured file as a review reports it: the frozen
// metadata plus its diff against the base commit. The diff is the text that
// was built at capture time, so the review still works after the workspace it
// came from is gone.
type PlannerSnapshotFile struct {
	SelectedFile
	Diff string `json:"diff"`
}

// PlannerSnapshotView is a snapshot as the routes report it. It carries no
// absolute host path: every path in it is the repository-relative one the
// caller selected.
type PlannerSnapshotView struct {
	ID         string                    `json:"id"`
	SHA256     string                    `json:"sha256"`
	BaseCommit string                    `json:"baseCommit"`
	Artifacts  []PlannerSnapshotArtifact `json:"artifacts"`
	Files      []PlannerSnapshotFile     `json:"files"`
	CreatedAt  time.Time                 `json:"createdAt"`
}

// PlannerSnapshotReply is what a capture answers: the snapshot it froze and
// the paths it refused.
type PlannerSnapshotReply struct {
	PlannerSnapshotView
	Rejected []PlannerRejection `json:"rejected"`
}

// PlannerContextArtifact is one artifact source a briefing found. Approved is
// true only for text a user captured in a snapshot; a reference the planner
// record merely names is reported as unapproved, carrying no captured content
// and no hash this bridge can vouch for.
type PlannerContextArtifact struct {
	Kind       string `json:"kind"`
	SHA256     string `json:"sha256,omitempty"`
	Source     string `json:"source"`
	Approved   bool   `json:"approved"`
	SnapshotID string `json:"snapshotId,omitempty"`
	Ref        string `json:"ref,omitempty"`
}

// PlannerContextDecision is one settled agent request a briefing reports, with
// the request it came from as its source.
type PlannerContextDecision struct {
	Source    string    `json:"source"`
	RequestID string    `json:"requestId"`
	Method    string    `json:"method,omitempty"`
	State     string    `json:"state"`
	At        time.Time `json:"at"`
}

// PlannerContextQuestion is one recorded question that is still unresolved.
type PlannerContextQuestion struct {
	Source    string    `json:"source"`
	RequestID string    `json:"requestId"`
	Method    string    `json:"method,omitempty"`
	State     string    `json:"state"`
	At        time.Time `json:"at"`
}

// PlannerContextView is the bounded briefing a planner record supports. Absent
// names the sources it does not hold: nothing here is invented to fill one in.
type PlannerContextView struct {
	Artifacts  []PlannerContextArtifact `json:"artifacts"`
	Decisions  []PlannerContextDecision `json:"decisions"`
	Unresolved []PlannerContextQuestion `json:"unresolved"`
	Absent     []string                 `json:"absent"`
}
