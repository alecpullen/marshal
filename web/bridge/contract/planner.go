package contract

import (
	"encoding/json"
	"time"
)

// The planner's wire types (F6 decision 8): the planning routes under
// `/api/planners`, the snapshots a handoff freezes, and the bounded briefing a
// planner record supports.
//
// The declarations are the bridge's own (web/bridge/planner_types.go and
// worker_backend.go), moved here so both clients are generated from one
// source; the bridge aliases each name to the type below. Two shapes are
// flattened rather than embedded: a Go embedded field has no mapping in either
// generated module, and flattening changes no JSON byte — the wire shape was
// already flat, because an embedded struct's fields are inlined by
// encoding/json.
//
// PlannerRecord, the durable record behind these views, is not here: it is the
// bridge's own state, never carried on the wire.

// PlannerOccupancy is how a workspace looks to a new planner. Ownership is
// never inferred from a missing record.
type PlannerOccupancy string

// The occupancy states a workspace can be in.
const (
	PlannerOccupancyFree    PlannerOccupancy = "free"
	PlannerOccupancyInUse   PlannerOccupancy = "in_use"
	PlannerOccupancyUnknown PlannerOccupancy = "unknown"
)

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

// PlannerArtifact is a reference to something the planner produced. The record
// persists the reference only; captured text lives on a snapshot instead.
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

// PlannerView is a planner as the routes report it: the durable record
// projected for the browser, plus what the bridge can say about it right now.
// Resume is reported separately from the capability so an absent capability is
// never read as an offer.
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
// the additional bounded text a user supplied alongside the prompt: it is
// composed into the dispatched text after the prompt's own text, and the
// journal keeps only its hash and length.
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

// PlannerSnapshotArtifact is one piece of text a snapshot froze: the plan or
// the spec the user approved. Its content is stored with the snapshot record
// and its SHA256 is verified on every read, so a review never shows bytes that
// differ from the hash it reports.
type PlannerSnapshotArtifact struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
}

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
//
// It flattens SelectedFile, which the bridge embeds: the wire shape is
// SelectedFile's fields followed by Diff.
type PlannerSnapshotFile struct {
	Path          string `json:"path"`
	Operation     string `json:"operation"`
	BaseSHA256    string `json:"baseSha256,omitempty"`
	ContentSHA256 string `json:"contentSha256,omitempty"`
	Executable    bool   `json:"executable"`
	Diff          string `json:"diff"`
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
//
// It flattens PlannerSnapshotView, which the bridge embeds: the wire shape is
// the view's fields followed by Rejected.
type PlannerSnapshotReply struct {
	ID         string                    `json:"id"`
	SHA256     string                    `json:"sha256"`
	BaseCommit string                    `json:"baseCommit"`
	Artifacts  []PlannerSnapshotArtifact `json:"artifacts"`
	Files      []PlannerSnapshotFile     `json:"files"`
	CreatedAt  time.Time                 `json:"createdAt"`
	Rejected   []PlannerRejection        `json:"rejected"`
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
