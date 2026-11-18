package contract

// The session routes a client outside the agent process uses to drive one
// session: its summary in the project's session list, the prompt, steer and
// mode requests, the committed access state, and the roster of live role
// bindings. The bridge reads or writes each of these shapes in
// web/bridge/http.go; naming them here is what lets TypeScript and Dart be
// generated instead of hand-written.

// SessionSummary is one session as GET /api/sessions lists it: the entry an
// agent's session/list returns for this project.
type SessionSummary struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title,omitempty"`
	// UpdatedAt is when the session last changed, Unix milliseconds.
	UpdatedAt    int64 `json:"updatedAt,omitempty"`
	MessageCount int   `json:"messageCount,omitempty"`
}

// SessionList is GET /api/sessions' answer: the project's sessions.
type SessionList struct {
	Sessions []SessionSummary `json:"sessions"`
}

// PromptRequest is a POST /api/sessions/{id}/prompt body: the text that
// starts a turn.
type PromptRequest struct {
	Text string `json:"text"`
}

// SteerRequest is a POST /api/sessions/{id}/steer body: an instruction for
// the turn already running.
type SteerRequest struct {
	Text string `json:"text"`
}

// ModeRequest is a POST /api/sessions/{id}/mode body: the permission mode to
// switch to.
type ModeRequest struct {
	Mode string `json:"mode"`
}

// Roster is GET /api/sessions/{id}/roster's answer: every role's live model
// binding, plus the swarm and SDD budgets.
type Roster struct {
	Roles       []RosterRole `json:"roles"`
	SwarmBudget RosterBudget `json:"swarmBudget"`
	SDDBudget   RosterBudget `json:"sddBudget"`
}

// RosterRole is one role's resolved binding.
type RosterRole struct {
	Role        string `json:"role"`
	Profile     string `json:"profile"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	PresetName  string `json:"presetName,omitempty"`
	CustomAgent string `json:"customAgent,omitempty"`
	LocalOnly   bool   `json:"localOnly"`
	Error       string `json:"error,omitempty"`
}

// RosterBudget is one subsystem's token and round budget.
type RosterBudget struct {
	MaxFixRounds   int `json:"maxFixRounds"`
	MaxTotalTokens int `json:"maxTotalTokens"`
}

// SessionIndex is GET /api/sessions/all's answer: every project's sessions,
// sorted by root. Projects is never null.
type SessionIndex struct {
	Projects []ProjectSessions `json:"projects"`
}

// ProjectSessions is one project root's sessions. Sessions is never null.
// Error is why the root's listing failed; its open sessions still list.
type ProjectSessions struct {
	Root     string       `json:"root"`
	Sessions []SessionRow `json:"sessions"`
	Error    string       `json:"error,omitempty"`
}

// SessionRow is one session the index lists (P7 D7).
type SessionRow struct {
	SessionID    string          `json:"sessionId"`
	Title        string          `json:"title,omitempty"`
	UpdatedAt    int64           `json:"updatedAt,omitempty"` // Unix milliseconds
	MessageCount int             `json:"messageCount,omitempty"`
	Open         bool            `json:"open"`    // tracked on its project's live child
	Viewers      int             `json:"viewers"` // terminals attached through the relay
	Busy         bool            `json:"busy"`
	Pending      *PendingRequest `json:"pending,omitempty"` // a turn the bridge started is parked on it
}
