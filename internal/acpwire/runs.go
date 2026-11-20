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

	// Workspace is the retained lifecycle evidence of the child's owned
	// workspace, when this conversation owns one. It is the session's copy of
	// the durable record joined by task key, so a card keeps naming its
	// candidate and its integration state after a restart. Nil means no owned
	// workspace was ever recorded, which is not the same as one that was
	// discarded.
	Workspace *SubagentWorkspace `json:"workspace,omitempty"`
}

// SubagentWorkspace is one child's owned-workspace lifecycle evidence as a
// client reads it: what the child was bound to, the commit it produced, the
// checkout that work is integrated into, and what became of the integration
// and of the workspace's own artifacts.
//
// Every field is optional, and an empty one is unknown rather than
// zero-valued fact. Empty Execution, Integration or Cleanup means the record
// predates the state or has not settled — it is never success.
// CandidateSHA is empty for a child that has produced no commit (a running
// child, a cancelled one, a failed one), and Reason is empty unless the record
// explains its own outcome. A client must not infer an integration from the
// presence of a candidate.
type SubagentWorkspace struct {
	TaskKey      string `json:"taskKey,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Root         string `json:"root,omitempty"`
	Branch       string `json:"branch,omitempty"`
	BaseSHA      string `json:"baseSha,omitempty"`
	CandidateSHA string `json:"candidateSha,omitempty"`
	TargetRoot   string `json:"targetRoot,omitempty"`
	TargetBranch string `json:"targetBranch,omitempty"`
	Execution    string `json:"execution,omitempty"`
	Integration  string `json:"integration,omitempty"`
	Cleanup      string `json:"cleanup,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// SubagentDrill is what a view drilled into a subagent reads about the child
// beyond its transcript (C5, C6).
type SubagentDrill struct {
	ActiveRoot  string   `json:"activeRoot,omitempty"` // the root a completion row elides
	ActiveRoute Route    `json:"activeRoute"`          // the route a beat's model is compared against
	Activity    Activity `json:"activity"`             // the reconnect notice reads it

	SessionID string `json:"sessionId,omitempty"` // the child's session ID, which a work item's reference names
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

// RunKind names what a run on the session's run path is (P4 D4).
type RunKind string

const (
	RunTurn   RunKind = "turn"   // session/prompt, a watch resume included
	RunReview RunKind = "review" // session/review
	RunAgent  RunKind = "agent"  // session/agent_run
	RunSwarm  RunKind = "swarm"  // session/swarm_start
	RunSDD    RunKind = "sdd"    // session/sdd_start and session/sdd_answer
	RunAuthor RunKind = "author" // session/sdd_author
)

// RunOutcome is how a finished run ended (P4 D4).
type RunOutcome string

const (
	RunDone      RunOutcome = "done"
	RunCancelled RunOutcome = "cancelled"
	RunFailed    RunOutcome = "failed"
	RunGated     RunOutcome = "gated"
)

// Run is the run in flight. ID is its stop group's turn ID; StartedAt is Unix
// milliseconds.
type Run struct {
	ID        string  `json:"id"`
	Kind      RunKind `json:"kind"`
	StartedAt int64   `json:"startedAt"`
}

// RunResult is the last finished run. Error is set for RunFailed only.
type RunResult struct {
	ID        string     `json:"id"`
	Kind      RunKind    `json:"kind"`
	StartedAt int64      `json:"startedAt"`
	EndedAt   int64      `json:"endedAt"`
	Outcome   RunOutcome `json:"outcome"`
	Error     *Error     `json:"error,omitempty"`
}
