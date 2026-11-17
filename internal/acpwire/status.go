package acpwire

// SessionStatus is what the status line, the footer and input gating read
// about a session (C5).
//
// It arrives over several plans: plan 6 adds the current notice. The session
// ID is not here: Session.ID() carries it.
// The start time is not here either: only the sheet reads it, so SessionSheet
// carries it.
type SessionStatus struct {
	WorkingDir string `json:"workingDir"`
	// GenerationSeq and LeafID carry omitempty per the plan's contract, so a
	// legitimate zero — the first generation, the first leaf — encodes
	// exactly like an unset field. The struct is always carried whole, so a
	// consumer must read zero as "first", never as "absent".
	GenerationSeq int     `json:"generationSeq,omitempty"`
	LeafID        int64   `json:"leafId,omitempty"`
	Branches      []int64 `json:"branches,omitempty"`
	ActiveRoute   Route   `json:"activeRoute"`
	TurnRoute     *Route  `json:"turnRoute,omitempty"` // nil between turns
	// TurnUsed and TurnWindow are tokens: what the running turn has spent and
	// the context window it is measured against. They are not milliseconds,
	// unlike Quota's durations.
	TurnUsed        int         `json:"turnUsed,omitempty"`
	TurnWindow      int         `json:"turnWindow,omitempty"`
	Quota           *Quota      `json:"quota,omitempty"`
	Effort          Effort      `json:"effort"`
	Interaction     Interaction `json:"interaction"`
	SudoUnlock      bool        `json:"sudoUnlock,omitempty"`
	Trusted         bool        `json:"trusted,omitempty"`
	HasBackup       bool        `json:"hasBackup,omitempty"`
	Activity        Activity    `json:"activity"`
	SubagentRunning bool        `json:"subagentRunning,omitempty"`
	// Run is the run in flight, nil when none; LastRun is the last finished
	// run, nil until one finishes (P4 D4).
	Run     *Run       `json:"run,omitempty"`
	LastRun *RunResult `json:"lastRun,omitempty"`
	// AgentReady says the session's agent runner is built, so Prompt, Review
	// and RunAgent can start. It is false while the provider is not built.
	AgentReady  bool       `json:"agentReady,omitempty"`
	Workspace   Workspace  `json:"workspace"`
	Browser     Browser    `json:"browser"`
	Sandbox     Sandbox    `json:"sandbox"`
	Steering    Steering   `json:"steering"`
	RunningJobs int        `json:"runningJobs,omitempty"`
	Subagents   []Subagent `json:"subagents,omitempty"`
	// SubagentControls is which child controls this session actually
	// registers. Absent — an older agent, or another ACP backend — means
	// unsupported, so a client offers no control and never infers support
	// from an unrelated session.
	SubagentControls *SubagentCapabilities `json:"subagentControls,omitempty"`
	// SubagentDefaults is the ordinary child settings this session applies to
	// an agent.run child when no per-session snapshot is bound, plus the digest
	// of the config and route inputs they were derived from. Absent means the
	// agent does not report them: a client then offers no settings surface.
	SubagentDefaults *SubagentDefaults `json:"subagentDefaults,omitempty"`
	Swarm            SwarmProgress     `json:"swarm"`
	SDD              SDDProgress       `json:"sdd"`
	SDDGate          SDDGate           `json:"sddGate"`
	Git              Git               `json:"git"`
	ChangedFiles     int               `json:"changedFiles,omitempty"` // against the sheet's base; 0 means "no changes", never "not refreshed"
	WatchResume      *WatchResume      `json:"watchResume,omitempty"`
	Notice           *Notice           `json:"notice,omitempty"`  // the banner; nil when none
	Restart          *PendingRestart   `json:"restart,omitempty"` // nil while the child is current
	// SkillGate is the session's skill load gate (C11, plan 8b).
	SkillGate SkillGateState `json:"skillGate"`
	// UserConfigPath and ProjectConfigPath are the files the config writes
	// change: the user config, and the session's project config. Each is
	// empty when the agent cannot locate it.
	UserConfigPath    string `json:"userConfigPath,omitempty"`
	ProjectConfigPath string `json:"projectConfigPath,omitempty"`
}

// SubagentCapabilities is which child controls a session registers, as the
// session reports it. It is per session and per backend: absent (or false)
// means unsupported, and a client offers no control rather than inferring one
// from another session or from the ACP version.
type SubagentCapabilities struct {
	// Settings is per-session child settings: enablement, model and
	// concurrency.
	Settings bool `json:"settings"`
	// Steer delivers one user instruction to a live child
	// (session/subagent_steer).
	Steer bool `json:"steer"`
	// Stop asks a live child to stop (session/subagent_cancel).
	Stop bool `json:"stop"`
	// Observe is the child's own transcript and lifecycle, which
	// session/stack already reports.
	Observe bool `json:"observe"`
}

// MarshalSubagentCapabilities is what a Marshal session advertises: steering,
// stopping and observing a live child are registered
// (internal/acp/controls.go, registerControls), and a session accepts a
// resolved child-settings snapshot at creation and enforces it in agent.run,
// so Settings is true too.
func MarshalSubagentCapabilities() *SubagentCapabilities {
	return &SubagentCapabilities{Settings: true, Steer: true, Stop: true, Observe: true}
}

// SubagentSettings is a session's child settings: whether agent.run is
// offered at all, the model children run on, and the cap on children in
// flight. A session binds one only at creation, and only that session
// enforces it.
type SubagentSettings struct {
	Enabled bool `json:"enabled"`
	// Model is the child's model, as a "provider/model" pair. Empty means
	// normal child routing: the child inherits the parent's route, exactly as
	// it does with no snapshot at all. It is not the worker model.
	Model string `json:"model,omitempty"`
	// Concurrency is the cap on agent.run children in flight. A snapshot
	// supplied at creation carries a positive value and is validated, never
	// clamped.
	Concurrency int `json:"concurrency"`
}

// SubagentDefaults is the ordinary child settings a session reports: what
// agent.run does with no per-session snapshot bound, and the digest of the
// config and route inputs they were derived from. A client that bound its own
// snapshot compares the digest to tell an unchanged default from a new one,
// without re-resolving routing itself.
type SubagentDefaults struct {
	Settings SubagentSettings `json:"settings"`
	// Digest identifies the effective config and route inputs the settings
	// were derived from. Two sessions reporting the same digest would derive
	// the same defaults.
	Digest string `json:"digest"`
}

// PendingRestart is a stale child's restart as its sessions show it (B3).
type PendingRestart struct {
	Reason     string `json:"reason"`
	WaitingFor string `json:"waitingFor"`
}

// Route mirrors the session's RouteInfo: the route a turn runs on.
type Route struct {
	Role             string `json:"role,omitempty"`
	Profile          string `json:"profile,omitempty"`
	Preset           string `json:"preset,omitempty"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	LocalOnly        bool   `json:"localOnly,omitempty"`
	Thinking         string `json:"thinking,omitempty"`
	ReasoningCapable bool   `json:"reasoningCapable,omitempty"`
	Active           bool   `json:"active,omitempty"`
}

// Quota mirrors the provider's schema.QuotaInfo. Its durations are
// milliseconds, converted from the seconds and minutes the provider reports.
type Quota struct {
	PlanType              string `json:"planType,omitempty"`
	PrimaryUsedPercent    int    `json:"primaryUsedPercent"`
	PrimaryResetAfterMs   int64  `json:"primaryResetAfterMs,omitempty"`
	PrimaryWindowMs       int64  `json:"primaryWindowMs,omitempty"`
	SecondaryUsedPercent  int    `json:"secondaryUsedPercent"`
	SecondaryResetAfterMs int64  `json:"secondaryResetAfterMs,omitempty"`
	SecondaryWindowMs     int64  `json:"secondaryWindowMs,omitempty"`
}

// Effort mirrors the session's EffortOverride.
type Effort struct {
	Level    string `json:"level,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// Interaction mirrors the session's interaction.State.
type Interaction struct {
	Mode     string `json:"mode"`
	Auto     bool   `json:"auto,omitempty"`
	System   bool   `json:"system,omitempty"`
	Revision uint64 `json:"revision,omitempty"`
}

// InteractionPatch changes some of a session's interaction state (C8). A nil
// field is left alone, so revoking system access (false) and leaving it alone
// (nil) are different patches. Mode is "ask" or "act".
type InteractionPatch struct {
	Mode   *string `json:"mode,omitempty"`
	Auto   *bool   `json:"auto,omitempty"`
	System *bool   `json:"system,omitempty"`
}

// When an effort change takes effect, as SetEffort reports it. They equal
// session.EffortAppliesNextStep and session.EffortAppliesNextTurn.
const (
	EffortAppliesNextStep = "next_step"
	EffortAppliesNextTurn = "next_turn"
)

// The kinds of activity a session reports, as the session stores them.
const (
	ActivityIdle         = "idle"
	ActivityThinking     = "thinking"
	ActivityTool         = "tool"
	ActivityApproval     = "approval"
	ActivityQuestion     = "question"
	ActivityReconnecting = "reconnecting"
)

// Activity mirrors the session's Activity.
type Activity struct {
	Kind      string `json:"kind"`
	Label     string `json:"label,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"`
}

// Workspace mirrors the session's Workspace.
type Workspace struct {
	ProjectRoot  string `json:"projectRoot,omitempty"`
	ActiveRoot   string `json:"activeRoot,omitempty"`
	Branch       string `json:"branch,omitempty"`
	BaseSha      string `json:"baseSha,omitempty"`
	TargetBranch string `json:"targetBranch,omitempty"`
}

// Browser mirrors the session's BrowserInfo.
type Browser struct {
	Active      bool   `json:"active,omitempty"`
	ToolName    string `json:"toolName,omitempty"`
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	Mode        string `json:"mode,omitempty"`
	SessionOpen bool   `json:"sessionOpen,omitempty"`
	UpdatedAt   int64  `json:"updatedAt,omitempty"`
}

// Sandbox is the shell sandbox a running shell row and the approval panel
// describe (C5): the backend, whether it isolates the network, and the
// shell's configured network allowance, which the isolation line weighs.
type Sandbox struct {
	Backend          string `json:"backend,omitempty"`
	NetworkIsolation bool   `json:"networkIsolation,omitempty"`
	AllowNetwork     bool   `json:"allowNetwork,omitempty"` // tools.shell.allow_network
}

// The status methods and update kind (P3 R3).
const (
	MethodStatus        = "session/status"
	MethodNoticeSet     = "session/notice_set"
	MethodNoticeClear   = "session/notice_clear"
	MethodNoticeDismiss = "session/notice_dismiss"
	UpdateSessionStatus = "session_status"
)

// StatusParams is session/status's and session/notice_dismiss's params.
type StatusParams struct {
	SessionID string `json:"sessionId"`
}

// StatusUpdate is a session_status update: the session's whole status,
// sent whenever it differs from the last one sent on the connection.
type StatusUpdate struct {
	Kind   string        `json:"kind"`
	Status SessionStatus `json:"status"`
}
