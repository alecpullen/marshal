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
	// AgentReady says the session's agent runner is built, so Prompt, Review
	// and RunAgent can start. It is false while the provider is not built.
	AgentReady   bool          `json:"agentReady,omitempty"`
	Workspace    Workspace     `json:"workspace"`
	Browser      Browser       `json:"browser"`
	Sandbox      Sandbox       `json:"sandbox"`
	Steering     Steering      `json:"steering"`
	RunningJobs  int           `json:"runningJobs,omitempty"`
	Subagents    []Subagent    `json:"subagents,omitempty"`
	Swarm        SwarmProgress `json:"swarm"`
	SDD          SDDProgress   `json:"sdd"`
	SDDGate      SDDGate       `json:"sddGate"`
	Git          Git           `json:"git"`
	ChangedFiles int           `json:"changedFiles,omitempty"` // against the sheet's base; 0 means "no changes", never "not refreshed"
	WatchResume  *WatchResume  `json:"watchResume,omitempty"`
	Notice       *Notice       `json:"notice,omitempty"` // the banner; nil when none
	// UserConfigPath and ProjectConfigPath are the files the config writes
	// change: the user config, and the session's project config. Each is
	// empty when the agent cannot locate it.
	UserConfigPath    string `json:"userConfigPath,omitempty"`
	ProjectConfigPath string `json:"projectConfigPath,omitempty"`
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
