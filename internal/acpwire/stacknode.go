package acpwire

import "encoding/json"

// NodeDetail is the uncapped detail behind one stack node.
type NodeDetail struct {
	Calls     []CallDetail    `json:"calls,omitempty"`
	Narration []string        `json:"narration,omitempty"`
	Thinking  []ThoughtDetail `json:"thinking,omitempty"`
	Todo      *TodoDetail     `json:"todo,omitempty"`
	Relations *RelationDetail `json:"relations,omitempty"`
	// Step is the step record behind this node: a call's own step, or a
	// beat's first. It carries who spoke, the model, the "why" line and the
	// todo the step began under.
	Step *StepDetail `json:"step,omitempty"`
	// Completion is a completion's retained output and its report, neither
	// capped.
	Completion *CompletionDetail `json:"completion,omitempty"`
}

// CallDetail is one tool call with nothing capped.
type CallDetail struct {
	CallID       string `json:"callId,omitempty"`
	StepID       int64  `json:"stepId,omitempty"`
	ToolName     string `json:"toolName"`
	Args         string `json:"args,omitempty"`
	OriginalArgs string `json:"originalArgs,omitempty"`
	Output       string `json:"output,omitempty"`
	Diff         string `json:"diff,omitempty"`
	Error        string `json:"error,omitempty"`
	ExitCode     *int   `json:"exitCode,omitempty"`
	Model        string `json:"model,omitempty"`
	FinishReason string `json:"finishReason,omitempty"`
	Rewritten    bool   `json:"rewritten,omitempty"`
	// Hooks is the hooks audit trail as the JSON registry.HookMetadata encodes
	// to. Its "failed_open" name is not camelCase, which the wire rules refuse
	// in a typed field, so it travels as raw bytes; HookDetail decodes what the
	// TUI reads.
	Hooks   json.RawMessage `json:"hooks,omitempty"`
	Sandbox json.RawMessage `json:"sandbox,omitempty"`
	Symbols []SymbolDetail  `json:"symbols,omitempty"`
	Notice  *NoticeDetail   `json:"notice,omitempty"`
	// Execution is the structured command-execution evidence this call
	// carries, separate from Notice: Notice.Text is coaching prose written
	// for the model, while this is the data a client explains a wait, a
	// handoff or a stop outcome with, without parsing a sentence.
	Execution *ExecutionDetail `json:"execution,omitempty"`
	Summary   string           `json:"summary,omitempty"` // the call's own summary, uncut
}

// ExecutionDetail is one managed command execution's structured evidence as
// the detail transport reports it: what was launched, who owned it, how the
// launching caller related to it and how it ended.
//
// Every field is optional. An absent Exit means no exit code was ever
// observed — a start-up failure, a still-running continuation, a lost record —
// and must never be rendered as success. Empty State/Outcome means the record
// predates structured evidence or has not settled, which is likewise unknown
// rather than zero-valued fact.
type ExecutionDetail struct {
	Version        int    `json:"version,omitempty"`
	ExecutionID    string `json:"executionId,omitempty"`
	DisplayID      string `json:"displayId,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	Actor          string `json:"actor,omitempty"`
	TurnID         string `json:"turnId,omitempty"`
	StepID         string `json:"stepId,omitempty"`
	ToolCallID     string `json:"toolCallId,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	Generation     int    `json:"generation,omitempty"`
	PID            int    `json:"pid,omitempty"`
	// State is the lifecycle status ("running", "stopping", "completed",
	// "failed", "killed", "timed_out"); Outcome is the finer cause
	// ("nonzero_exit", "startup_failure", "stopped", "resource_limit", …).
	State      string `json:"state,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	Attachment string `json:"attachment,omitempty"`
	// Wait is the effective foreground wait the launch used; DetachReason is
	// why and DetachedAt when a wait handed the execution to the background.
	Wait         string `json:"wait,omitempty"`
	DetachReason string `json:"detachReason,omitempty"`
	DetachedAt   int64  `json:"detachedAt,omitempty"`
	// StopCause and StopRequestedAt record an accepted explicit stop request
	// separately from the outcome it produced.
	StopCause       string `json:"stopCause,omitempty"`
	StopRequestedAt int64  `json:"stopRequestedAt,omitempty"`
	// Exit is the observed exit code; absent means none was observed.
	Exit      *int  `json:"exit,omitempty"`
	Truncated bool  `json:"truncated,omitempty"`
	StartedAt int64 `json:"startedAt,omitempty"`
	EndedAt   int64 `json:"endedAt,omitempty"`
}

// SymbolDetail is a symbol a call touched.
type SymbolDetail struct {
	File string `json:"file"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

// NoticeDetail is a tool-UX notice attached to a call.
type NoticeDetail struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// ThoughtDetail is one finished reasoning block.
type ThoughtDetail struct {
	Text       string `json:"text"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// TodoDetail is the todo a step or task belongs to.
type TodoDetail struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	Status      string `json:"status"`
	StartedAt   int64  `json:"startedAt,omitempty"`
	CompletedAt int64  `json:"completedAt,omitempty"`
}

// RelationDetail lists node keys of related steps.
type RelationDetail struct {
	CausedBy []string `json:"causedBy,omitempty"`
	FixedBy  []string `json:"fixedBy,omitempty"`
}

// StackNodeResult is session/stack_node's answer: one node's wire form and the
// detail the projection caps or leaves out. Detail comes first so the JSON
// matches the map the handler sent before this type existed.
type StackNodeResult struct {
	Detail NodeDetail `json:"detail"`
	Node   WireNode   `json:"node"`
}

// HookDetail is what a client reads of one hook run in CallDetail.Hooks.
type HookDetail struct {
	Event    string `json:"event"`
	Decision string `json:"decision,omitempty"`
}

// SandboxDetail is what a client reads of CallDetail.Sandbox. That JSON spells
// its keys in Go's field spelling ("Enabled", "KilledReason");
// encoding/json matches keys case-insensitively, so these names decode it.
type SandboxDetail struct {
	Enabled         bool   `json:"enabled,omitempty"`
	Backend         string `json:"backend,omitempty"`
	NetworkIsolated bool   `json:"networkIsolated,omitempty"`
	KilledReason    string `json:"killedReason,omitempty"`
}

// StepDetail is a step record: a call's own step, or a beat's first. Why is
// the first sentence of the step's narration, emphasis stripped. TodoID is the
// todo the step was bound to when it started.
type StepDetail struct {
	ID       int64  `json:"id,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	Why      string `json:"why,omitempty"`
	TodoID   string `json:"todoId,omitempty"`
}

// CompletionDetail is a completion's retained output and its report, neither
// capped.
type CompletionDetail struct {
	Output string `json:"output,omitempty"`
	Report string `json:"report,omitempty"`
}

// ApprovalNotRequired is the WireCall.Approval value of a call that needed no
// approval.
const ApprovalNotRequired = "not_required"
