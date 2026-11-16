package contract

// The stack is a session's transcript tree as a client outside the agent
// process sees it. It is not assembled by the client: internal/viewmodel and
// internal/acpwire build the tree and project it, the bridge relays the
// snapshot at GET /api/sessions/{id}/stack and the patches on the session's
// event stream, and a client keeps a node map plus the root order.
//
// These declarations are the wire, field for field: every JSON key the agent
// sends is here, because the bridge relays the agent's own encoding. A field
// the agent omits when empty is `omitempty` here too, and a field the agent
// sends as null (a call's exit code, before or after a command ran) is a
// pointer. The agent's projectors are the authority; these types exist so
// TypeScript and Dart have one generated declaration instead of two
// hand-written copies.
//
// Times are Unix milliseconds; zero (absent) means unset.

// WireNode is one node. Exactly one payload pointer is set, chosen by Kind;
// a turn node carries none.
type WireNode struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Parent   string   `json:"parent,omitempty"`
	Children []string `json:"children,omitempty"`
	Live     bool     `json:"live,omitempty"`
	// Version is the hex of the node's payload hash: a client caches a
	// rendered row while it is unchanged.
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

// WireStep is a step's header and reasoning.
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
	// Verb is the word the row leads with.
	Verb string `json:"verb,omitempty"`
	// WorkKey is the stable conversation-work identity this row dispatched,
	// so a client can pair the retained dispatch row with the pinned live
	// card by identity rather than by display text.
	WorkKey string       `json:"workKey,omitempty"`
	Calls   []WireCall   `json:"calls,omitempty"`
	Running *WireRunning `json:"running,omitempty"`
	// SubagentID names the subagent this row started, 0 when it started none.
	SubagentID int64 `json:"subagentId,omitempty"`
	// Callers says the row's call changed a resolved symbol whose callers the
	// agent can look up.
	Callers bool `json:"callers,omitempty"`
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

	// Subject is what the call acted on, scrubbed, its first line.
	Subject string `json:"subject,omitempty"`
	// SummaryLine is the summary's first line, scrubbed.
	SummaryLine string `json:"summaryLine,omitempty"`
	// DiffStat is "+N −M" over the whole diff of an edit or a whole-file
	// write.
	DiffStat string `json:"diffStat,omitempty"`
	// HookHint is the hook decision the row shows first.
	HookHint string `json:"hookHint,omitempty"`
	// KilledReason is why the sandbox killed the command.
	KilledReason string `json:"killedReason,omitempty"`
	// JobID is the background job the call started.
	JobID string `json:"jobId,omitempty"`
	// Tail is a failed call's last output lines.
	Tail []string `json:"tail,omitempty"`
}

// WireRunning is a call that has not finished.
type WireRunning struct {
	Target    string `json:"target,omitempty"`
	Output    string `json:"output,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`

	// Subject is what the call acts on, scrubbed.
	Subject string `json:"subject,omitempty"`
	CallID  string `json:"callId,omitempty"`
	// Args is the arguments as the tool shows them, capped.
	Args string `json:"args,omitempty"`
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

// WireMessage is a user prompt, an assistant reply, or any other message the
// transcript shows on its own row.
type WireMessage struct {
	Role        string `json:"role"`
	ContentType string `json:"contentType,omitempty"`
	Content     string `json:"content"`
	// IsFinal is the wire's "final": the field is spelled out here because
	// Dart reserves the bare word, and both generators name a Dart field (but
	// never a JSON key) after the Go field.
	IsFinal       bool   `json:"final,omitempty"`
	Usage         string `json:"usage,omitempty"`
	Salvaged      bool   `json:"salvaged,omitempty"`
	SalvageReason string `json:"salvageReason,omitempty"`
	ThinkMs       int64  `json:"thinkMs,omitempty"`
	At            int64  `json:"at,omitempty"`
	// Reasoning is the message's reasoning text, capped.
	Reasoning string `json:"reasoning,omitempty"`
	// Truncated says the reasoning was cut.
	Truncated bool `json:"truncated,omitempty"`
}

// WireThinking is a reasoning row outside any step, finished or live.
type WireThinking struct {
	Text       string `json:"text"`
	DurationMs int64  `json:"durationMs,omitempty"`
	// StartedAt is when the live reasoning began.
	StartedAt int64 `json:"startedAt,omitempty"`
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
	// BodyLines is the whole body's line count.
	BodyLines int `json:"bodyLines,omitempty"`
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

// WireBand is the pinned band: todo progress with its stale signal, and the
// running cards. It travels as a node with ID "band" that is deliberately
// not in roots, so a transcript walk ignores it.
type WireBand struct {
	Todos      bool   `json:"todos,omitempty"`
	Done       int    `json:"done"`
	Total      int    `json:"total"`
	Active     string `json:"active,omitempty"`
	Stale      bool   `json:"stale,omitempty"`
	StaleAgeMs int64  `json:"staleAgeMs,omitempty"`
	// StaleSince is when the stale clock started, so a client can keep the
	// age current between patches.
	StaleSince int64    `json:"staleSince,omitempty"`
	Next       []string `json:"next,omitempty"`
	// Tasks is the whole list for the Tasks tab, sent whenever the session
	// has a list, idle or not.
	Tasks    []WireTask       `json:"tasks,omitempty"`
	Entities []WireBandEntity `json:"entities,omitempty"`
}

// WireTask is one todo in the Tasks tab, with its share of the transcript.
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

// StackSnapshot is the whole stack at Rev, as session/stack answers.
type StackSnapshot struct {
	SessionID  string     `json:"sessionId"`
	SubagentID int64      `json:"subagentId,omitempty"`
	Rev        uint64     `json:"rev"`
	Roots      []string   `json:"roots"`
	Nodes      []WireNode `json:"nodes"`
}

// StackPatch is one stack_patch update: what changed in one stack since
// BaseRev. A client whose revision is BaseRev applies it and moves to Rev; a
// client whose revision is anything else refetches the snapshot.
type StackPatch struct {
	Kind       string     `json:"kind"`
	SubagentID int64      `json:"subagentId,omitempty"`
	Rev        uint64     `json:"rev"`
	BaseRev    uint64     `json:"baseRev"`
	Roots      []string   `json:"roots"`
	Upsert     []WireNode `json:"upsert,omitempty"`
	Remove     []string   `json:"remove,omitempty"`
}
