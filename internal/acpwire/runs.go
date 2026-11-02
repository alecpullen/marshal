package acpwire

// Steering is the steering queue and the Send now state.
type Steering struct {
	Queue   []string         `json:"queue,omitempty"` // the queued texts, oldest first
	State   SteeringState    `json:"state"`
	Receipt *SteeringReceipt `json:"receipt,omitempty"` // the newest receipt for State.TurnID
}

// Subagent mirrors the session's SubagentView without its child state.
type Subagent struct {
	ID       int64  `json:"id"`
	Label    string `json:"label,omitempty"`
	Status   string `json:"status"`
	Role     string `json:"role,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	Fallback bool   `json:"fallback,omitempty"`
	Headline string `json:"headline,omitempty"` // running children only

	// Drill is set while the session keeps the child's state, which is what
	// Session.Stack reads for the subagent. Nil means the subagent cannot be
	// drilled into: a status-only card, or a child the session has released.
	Drill *SubagentDrill `json:"drill,omitempty"`
}

// SubagentDrill is what a view drilled into a subagent reads about the child
// beyond its transcript (C5, C6).
type SubagentDrill struct {
	ActiveRoot  string   `json:"activeRoot,omitempty"` // the root a completion row elides
	ActiveRoute Route    `json:"activeRoute"`          // the route a beat's model is compared against
	Activity    Activity `json:"activity"`             // the reconnect notice reads it
}

// The subagent statuses.
const (
	SubagentRunning = "running"
	SubagentDone    = "done"
	SubagentFailed  = "failed"
)

// SwarmProgress mirrors the session's SwarmProgress.
type SwarmProgress struct {
	Goal       string      `json:"goal,omitempty"`
	Active     bool        `json:"active,omitempty"`
	Roles      []SwarmRole `json:"roles,omitempty"`
	TokensUsed int         `json:"tokensUsed,omitempty"`
	TokensMax  int         `json:"tokensMax,omitempty"`
}

// SwarmRole mirrors the session's SwarmRole.
type SwarmRole struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Tokens    int    `json:"tokens,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"`
}

// The swarm role statuses.
const (
	SwarmRolePending = "pending"
	SwarmRoleActive  = "active"
	SwarmRoleDone    = "done"
	SwarmRoleFailed  = "failed"
)

// SDDProgress mirrors the session's SDDProgress.
type SDDProgress struct {
	Active         bool         `json:"active,omitempty"`
	PlanName       string       `json:"planName,omitempty"`
	PlanPath       string       `json:"planPath,omitempty"`
	Branch         string       `json:"branch,omitempty"`
	BaseRef        string       `json:"baseRef,omitempty"`
	LedgerPath     string       `json:"ledgerPath,omitempty"`
	ArtifactsDir   string       `json:"artifactsDir,omitempty"`
	Tasks          []string     `json:"tasks,omitempty"`
	TaskTimings    []TaskTiming `json:"taskTimings,omitempty"`
	TaskExecTypes  []string     `json:"taskExecTypes,omitempty"`
	TotalTasks     int          `json:"totalTasks,omitempty"`
	DoneTasks      int          `json:"doneTasks,omitempty"`
	CurrentTask    int          `json:"currentTask,omitempty"`
	Phase          string       `json:"phase,omitempty"`
	PhaseStartedAt int64        `json:"phaseStartedAt,omitempty"`
	Detail         string       `json:"detail,omitempty"`
	FixRound       int          `json:"fixRound,omitempty"`
	MaxFixRounds   int          `json:"maxFixRounds,omitempty"`
	TokensUsed     int          `json:"tokensUsed,omitempty"`
	TokensMax      int          `json:"tokensMax,omitempty"`
	StartedAt      int64        `json:"startedAt,omitempty"`
	Finished       bool         `json:"finished,omitempty"`
	Succeeded      bool         `json:"succeeded,omitempty"`
	EndedAt        int64        `json:"endedAt,omitempty"`
	Error          string       `json:"error,omitempty"`
}

// TaskTiming mirrors the session's TaskTiming.
type TaskTiming struct {
	StartedAt int64 `json:"startedAt,omitempty"`
	EndedAt   int64 `json:"endedAt,omitempty"`
}

// SDDGate mirrors the session's SDDGate. A gate with no question is closed.
type SDDGate struct {
	TaskN     int    `json:"taskN,omitempty"`
	Question  string `json:"question,omitempty"`
	TaskTitle string `json:"taskTitle,omitempty"`
	Report    string `json:"report,omitempty"`
}

// Git mirrors gitinfo.Info for the session's active root.
type Git struct {
	Branch   string `json:"branch,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	InRepo   bool   `json:"inRepo,omitempty"`
}

// WatchResume is the newest resume-bearing watch fire. Seq increases with
// each fire, so a reader acts on a value once.
type WatchResume struct {
	Name   string `json:"name"`
	Repeat bool   `json:"repeat,omitempty"`
	Seq    uint64 `json:"seq"`
}
