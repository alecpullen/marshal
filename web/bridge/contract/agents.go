package contract

import (
	"encoding/json"
	"time"
)

// AgentState is the lifecycle state an agent reports.
type AgentState string

// The agent states the bridge reports.
const (
	AgentIdle             AgentState = "idle"
	AgentRunning          AgentState = "running"
	AgentAwaitingApproval AgentState = "awaiting-approval"
	AgentAwaitingQuestion AgentState = "awaiting-question"
	AgentError            AgentState = "error"
)

// PendingKind is what an agent is parked on.
type PendingKind string

// The kinds of pending request an agent can be parked on.
const (
	PendingApproval PendingKind = "approval"
	PendingQuestion PendingKind = "question"
)

// AccessState is the bridge-owned projection of a session's committed
// interaction tuple. Every field is always present on the wire: a client must
// be able to tell an explicit false from an omitted value, so no field carries
// omitempty.
type AccessState struct {
	Mode     string `json:"mode"`
	Auto     bool   `json:"auto"`
	System   bool   `json:"system_access"`
	Revision uint64 `json:"access_revision"`
}

// AgentStatus is one agent as GET /api/agents reports it.
type AgentStatus struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Name    string `json:"name,omitempty"`
	// OwnerID, Origin and ClientID mirror Agent.OwnerID, Agent.Origin and
	// Agent.ClientID in workspace.go.
	OwnerID  string `json:"ownerId,omitempty"`
	Origin   string `json:"origin,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	// Access is the session's committed interaction state, as the agent last
	// reported it. Nil means the bridge cannot vouch for it — the agent has
	// not reported, or the read failed — and a client must present that as
	// unavailable rather than inferring privileges from the project's
	// defaults or the agent's persisted starting mode.
	Access       *AccessState `json:"access,omitempty"`
	Status       AgentState   `json:"status"`
	Activity     string       `json:"activity,omitempty"`
	ContextPct   int          `json:"contextPct,omitempty"`
	ChangedFiles int          `json:"changedFiles,omitempty"`
	Interrupted  bool         `json:"interrupted,omitempty"`
	// Held is true while a terminal is holding the agent (the agent is
	// paused for someone typing), so a reloaded page shows it without
	// waiting for the next "hold" delta.
	Held      bool      `json:"held,omitempty"`
	Isolated  bool      `json:"isolated,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	// SourceKind is "local" or "git"; the exit panel routes on it and
	// ReadOnly to decide between merge, push, and patch.
	SourceKind string `json:"sourceKind,omitempty"`
	// ReadOnly marks an arbitrary-URL clone with no push path.
	ReadOnly bool `json:"readOnly,omitempty"`
	// TargetBranch is the repo's default branch at spawn time.
	TargetBranch string `json:"targetBranch,omitempty"`
	// PRUrl is the validated pull-request URL from the last push.
	PRUrl string `json:"prUrl,omitempty"`
	// PushedAt is when the agent's branch last reached the remote.
	PushedAt *time.Time `json:"pushedAt,omitempty"`
	// GateOverride, when non-nil, records a pushed-despite-failure decision.
	GateOverride *GateOverride `json:"gateOverride,omitempty"`
	// Workspace is the workspace the agent runs in, when it has one.
	Workspace *AgentWorkspace `json:"workspace,omitempty"`
	// Pending is set only while the agent is genuinely parked on an
	// approval or question, so the dashboard can resolve it in place.
	Pending *PendingRequest `json:"pending,omitempty"`
}

// PendingRequest is an agent's outstanding approval or question.
type PendingRequest struct {
	// Kind is "approval" or "question", matching Registry.Pending.
	Kind PendingKind `json:"kind"`
	// ID is the toolCallId or questionId to POST the resolution to.
	ID string `json:"id"`
	// Params is the original request payload, verbatim.
	Params json.RawMessage `json:"params,omitempty"`
}

// GateOverride records a pushed-despite-failure decision.
type GateOverride struct {
	Reason        string    `json:"reason"`
	At            time.Time `json:"at"`
	By            string    `json:"by"` // OwnerID
	FailedCommand string    `json:"failedCommand,omitempty"`
	Skipped       bool      `json:"skipped,omitempty"`
}

// WorkspaceSource says where an agent's workspace came from.
type WorkspaceSource string

// The workspace sources.
const (
	WorkspaceFromStudio WorkspaceSource = "studio"
	WorkspaceFromRepo   WorkspaceSource = "repo"
)

// AgentWorkspace is the workspace an agent runs in.
type AgentWorkspace struct {
	Name    string `json:"name"`
	Version int    `json:"version,omitempty"`
	// Source is "studio" or "repo".
	Source WorkspaceSource `json:"source"`
	// Timeout is the agent deadline from [resources], armed on the first
	// prompt, so a restarted runtime can arm it again.
	Timeout string `json:"timeout,omitempty"`
}
