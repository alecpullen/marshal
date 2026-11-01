package acpwire

// The wire projection is the tree as a client outside the process sees it.
// Clients cannot import this package (web/ is standard library only), so the
// projection is plain JSON: a flat node list in which each node names its
// parent and its children by ID. Headlines, targets and owners are worked
// out here so every client says the same thing as the TUI.

// WireTextCap bounds every large text field (tool output, arguments, job
// output, run-event bodies) in the projection. A capped field keeps its
// first WireTextCap bytes and sets the node's Truncated flag. Full payloads
// are a separate, on-demand request.
const WireTextCap = 4096

// WireTree is a whole stack: the turn IDs in order, and every node with
// parents listed before their children.
type WireTree struct {
	Roots []string   `json:"roots"`
	Nodes []WireNode `json:"nodes"`
}

// WireNode is one node. Exactly one payload pointer is set, chosen by Kind;
// turn nodes carry none.
type WireNode struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Parent   string   `json:"parent,omitempty"`
	Children []string `json:"children,omitempty"`
	Live     bool     `json:"live,omitempty"`
	// Version is the hex of the node's payload hash: a client caches a
	// rendered row while it is unchanged. Empty for a node without one.
	Version string `json:"version,omitempty"`

	Step       *WireStep       `json:"step,omitempty"`
	Tool       *WireTool       `json:"tool,omitempty"`
	Message    *WireMessage    `json:"message,omitempty"`
	Thinking   *WireThinking   `json:"thinking,omitempty"`
	RunEvent   *WireRunEvent   `json:"runEvent,omitempty"`
	Completion *WireCompletion `json:"completion,omitempty"`
	Plan       *WirePlan       `json:"plan,omitempty"`
	Receipt    *WireReceipt    `json:"receipt,omitempty"`
	Band       *WireBand       `json:"band,omitempty"`
}

// WireBand is the pinned band: todo progress with its stale signal, and the
// running cards. It travels as a node with ID "band" that is deliberately not
// in roots, so a transcript walk ignores it while the stack patch protocol
// carries it with no change.
type WireBand struct {
	Todos      bool   `json:"todos,omitempty"`
	Done       int    `json:"done"`
	Total      int    `json:"total"`
	Active     string `json:"active,omitempty"`
	Stale      bool   `json:"stale,omitempty"`
	StaleAgeMs int64  `json:"staleAgeMs,omitempty"`
	// StaleSince is when the stale clock started (unix ms), so a client can
	// keep the age current between patches.
	StaleSince int64    `json:"staleSince,omitempty"`
	Next       []string `json:"next,omitempty"`
	// Tasks is the whole list for the Tasks tab. Unlike todos it is sent
	// whenever the session has a list, idle or not.
	Tasks    []WireTask       `json:"tasks,omitempty"`
	Entities []WireBandEntity `json:"entities,omitempty"`
}

// WireTask is one todo in the Tasks tab, with its share of the transcript.
// Times are Unix milliseconds; zero means unset.
type WireTask struct {
	ID          string `json:"id,omitempty"`
	Content     string `json:"content"`
	Status      string `json:"status"`
	StartedAt   int64  `json:"startedAt,omitempty"`
	CompletedAt int64  `json:"completedAt,omitempty"`
	Steps       int    `json:"steps,omitempty"`
	WorkMs      int64  `json:"workMs,omitempty"`
	// Ref is the first beat bound to the todo, where a click jumps.
	Ref string `json:"ref,omitempty"`
}

// WireBandEntity is one running card the band lists.
type WireBandEntity struct {
	Ref       string `json:"ref"`
	Label     string `json:"label"`
	Detail    string `json:"detail,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"`
}

// WireStep is a step's header and reasoning. Times are Unix milliseconds;
// zero means unset.
type WireStep struct {
	Headline  string         `json:"headline"`
	Rest      string         `json:"rest,omitempty"`
	StepIDs   []int64        `json:"stepIds,omitempty"`
	Owner     string         `json:"owner,omitempty"`
	RoleWord  string         `json:"roleWord,omitempty"`
	Role      string         `json:"role,omitempty"`
	Model     string         `json:"model,omitempty"`
	Provider  string         `json:"provider,omitempty"`
	TodoID    string         `json:"todoId,omitempty"`
	Todo      *WireTodoLabel `json:"todo,omitempty"`
	StartedAt int64          `json:"startedAt,omitempty"`
	EndedAt   int64          `json:"endedAt,omitempty"`
	Thoughts  []WireThought  `json:"thoughts,omitempty"`

	// Narration is the beat's narration texts in order, each trimmed and an
	// empty one kept, so the count is the beat's narration count.
	Narration []string `json:"narration,omitempty"`
}

// WireTodoLabel marks the beat where work moves to a different todo.
type WireTodoLabel struct {
	ID      string `json:"id"`
	Index   int    `json:"index"`
	Total   int    `json:"total"`
	Content string `json:"content"`
}

// WireThought is one finished reasoning block inside a step.
type WireThought struct {
	Text       string `json:"text"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// WireTool is a tool row: one call, a merged run of same-tool calls, or a
// call still running.
type WireTool struct {
	Name    string `json:"name"`
	Display string `json:"display"`
	Verb    string `json:"verb,omitempty"` // the word the row leads with
	// WorkKey is the stable conversation-work identity this row dispatched, so
	// a client can pair the retained dispatch row with the pinned live card by
	// identity rather than by display text. Omitted when the record carries
	// none: a legacy record, or a row that dispatched no work.
	WorkKey string       `json:"workKey,omitempty"`
	Calls   []WireCall   `json:"calls,omitempty"`
	Running *WireRunning `json:"running,omitempty"`
	// SubagentID names the subagent this row started, 0 when it started none.
	SubagentID int64 `json:"subagentId,omitempty"`
	// Callers says the row's call changed a resolved symbol whose callers
	// Session.Callers can look up.
	Callers bool `json:"callers,omitempty"`
}

// WireCompletion is one finished piece of background work at the moment it
// ended: a background job's exit or a subagent's end. Expanded, it is the
// issue report.
type WireCompletion struct {
	Source          string `json:"source"`
	ID              string `json:"id"`
	SubagentID      int64  `json:"subagentId,omitempty"`
	Label           string `json:"label"`
	Outcome         string `json:"outcome"`
	Failed          bool   `json:"failed,omitempty"`
	ExitCode        *int   `json:"exitCode,omitempty"`
	StopCause       string `json:"stopCause,omitempty"`
	StartedAt       int64  `json:"startedAt,omitempty"`
	EndedAt         int64  `json:"endedAt,omitempty"`
	DurationMs      int64  `json:"durationMs,omitempty"`
	Measure         string `json:"measure,omitempty"`
	Summary         string `json:"summary,omitempty"`
	Command         string `json:"command,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
	Output          string `json:"output,omitempty"`
	Report          string `json:"report,omitempty"`
	Error           string `json:"error,omitempty"`
	WorkKey         string `json:"workKey,omitempty"`
	ExecutionID     string `json:"executionId,omitempty"`
	ReportMessageID int64  `json:"reportMessageId,omitempty"`
	Truncated       bool   `json:"truncated,omitempty"`
}

// WirePlan is a beat's plan line: the todo list as the beat's last
// todo.write left it.
type WirePlan struct {
	// Status is each item's status in list order: "completed",
	// "in_progress" or "pending".
	Status     []string `json:"status,omitempty"`
	Done       int      `json:"done"`
	Total      int      `json:"total"`
	Active     int      `json:"active,omitempty"`
	ActiveText string   `json:"activeText,omitempty"`
	Cleared    bool     `json:"cleared,omitempty"`
}

// WireCall is one finished call.
type WireCall struct {
	CallID     string   `json:"callId,omitempty"`
	Target     string   `json:"target,omitempty"`
	Args       string   `json:"args,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Output     string   `json:"output,omitempty"`
	Error      string   `json:"error,omitempty"`
	ExitCode   *int     `json:"exitCode,omitempty"`
	Failed     bool     `json:"failed,omitempty"`
	Approval   string   `json:"approval,omitempty"`
	Risk       string   `json:"risk,omitempty"`
	Files      []string `json:"files,omitempty"`
	Role       string   `json:"role,omitempty"`
	Notice     string   `json:"notice,omitempty"`
	DurationMs int64    `json:"durationMs,omitempty"`
	At         int64    `json:"at,omitempty"`
	Truncated  bool     `json:"truncated,omitempty"`

	Subject      string   `json:"subject,omitempty"`      // what the call acted on, scrubbed, its first line
	SummaryLine  string   `json:"summaryLine,omitempty"`  // the summary's first line, scrubbed
	DiffStat     string   `json:"diffStat,omitempty"`     // "+N −M" over the whole diff, for an edit or a whole-file write
	HookHint     string   `json:"hookHint,omitempty"`     // the hook decision the row shows first
	KilledReason string   `json:"killedReason,omitempty"` // why the sandbox killed the command
	JobID        string   `json:"jobId,omitempty"`        // the background job the call started
	Tail         []string `json:"tail,omitempty"`         // a failed call's last output lines
}

// WireRunning is a call that has not finished.
type WireRunning struct {
	Target    string `json:"target,omitempty"`
	Output    string `json:"output,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`

	Subject string `json:"subject,omitempty"` // what the call acts on, scrubbed
	CallID  string `json:"callId,omitempty"`
	Args    string `json:"args,omitempty"` // the arguments as the tool shows them, capped
}

// WireMessage is a user prompt, an assistant reply, or any other message the
// transcript shows on its own row.
type WireMessage struct {
	Role          string `json:"role"`
	ContentType   string `json:"contentType,omitempty"`
	Content       string `json:"content"`
	Final         bool   `json:"final,omitempty"`
	Usage         string `json:"usage,omitempty"`
	Salvaged      bool   `json:"salvaged,omitempty"`
	SalvageReason string `json:"salvageReason,omitempty"`
	ThinkMs       int64  `json:"thinkMs,omitempty"`
	At            int64  `json:"at,omitempty"`
	Reasoning     string `json:"reasoning,omitempty"` // capped at WireTextCap
	Truncated     bool   `json:"truncated,omitempty"` // the reasoning was cut
}

// WireThinking is a reasoning row outside any step, finished or live.
type WireThinking struct {
	Text       string `json:"text"`
	DurationMs int64  `json:"durationMs,omitempty"`
	StartedAt  int64  `json:"startedAt,omitempty"` // when the live reasoning began
}

// WireRunEvent is one plan-run event (verify failure, review, commit, …).
type WireRunEvent struct {
	Kind      string `json:"kind"`
	TaskN     int    `json:"taskN,omitempty"`
	Title     string `json:"title,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Body      string `json:"body,omitempty"`
	Severity  string `json:"severity,omitempty"`
	At        int64  `json:"at,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	BodyLines int    `json:"bodyLines,omitempty"` // the whole body's line count
}

// WireReceipt is the summary line closing a finished turn.
type WireReceipt struct {
	DurationMs int64  `json:"durationMs"`
	Tasks      int    `json:"tasks"`
	Steps      int    `json:"steps"`
	Tools      int    `json:"tools"`
	Files      int    `json:"files"`
	Usage      string `json:"usage,omitempty"`
	Salvaged   bool   `json:"salvaged,omitempty"`
}

// StackSnapshot is the full stack at Rev (foundation spec §5.1).
type StackSnapshot struct {
	SessionID  string     `json:"sessionId"`
	SubagentID int64      `json:"subagentId,omitempty"`
	Rev        uint64     `json:"rev"`
	Roots      []string   `json:"roots"`
	Nodes      []WireNode `json:"nodes"`
}

// Node kinds: the values of WireNode.Kind.
const (
	KindTurn        = "turn"
	KindStep        = "step"
	KindTool        = "tool"
	KindMessage     = "message"
	KindFinal       = "final"
	KindSubagent    = "subagent"
	KindRunEvent    = "runEvent"
	KindJobExit     = "jobExit"
	KindThinking    = "thinking"
	KindPassthrough = "passthrough"
	KindReceipt     = "receipt"
	KindCompletion  = "completion"
	KindPlanUpdate  = "planUpdate"
	KindBand        = "band"
)

// BandID is the band node's ID and LiveThinkingID the live reasoning node's.
// Neither changes from one patch to the next.
const (
	BandID         = "band"
	LiveThinkingID = "think:live"
)

// Run-event kinds: the values of WireRunEvent.Kind.
const (
	RunEventVerifyFailed = "verifyFailed"
	RunEventGateSkipped  = "gateSkipped"
	RunEventReview       = "review"
	RunEventCommit       = "commit"
	RunEventRetry        = "retry"
	RunEventConcern      = "concern"
	RunEventTaskDone     = "taskDone"
)

// Completion sources: the values of WireCompletion.Source.
const (
	CompletionJob      = "job"
	CompletionSubagent = "subagent"
)

// Completion outcomes: the values of WireCompletion.Outcome, apart from a
// job's "exit N".
const (
	OutcomeDone    = "done"
	OutcomeStopped = "stopped"
	OutcomeLost    = "lost"
	OutcomeFailed  = "failed"
	OutcomeUnknown = "outcome unknown"
)
