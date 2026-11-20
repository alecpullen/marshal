package contract

import "encoding/json"

// Inspecting one node: what its step was for, what its call did, and the
// model request behind it. The routes are under /api/sessions/{id}/:
// step-diffs for the review page's by-step view, nodes/{nodeId} for one
// node's detail, and last-request for the newest model request.
//
// Times are Unix milliseconds; zero (absent) means unset. Where a field is
// absent the answer is unknown, never zero-valued fact.

// StepDiff is one step's part of the session's diff, for the by-step view.
// Files is the paths it touched and Diff is their unified diff.
type StepDiff struct {
	StepNode string   `json:"stepNode"`
	TurnNode string   `json:"turnNode"`
	Headline string   `json:"headline"`
	At       int64    `json:"at,omitempty"`
	Files    []string `json:"files"`
	Diff     string   `json:"diff"`
}

// StepDiffs is GET /api/sessions/{id}/step-diffs' answer: the session's
// steps in order.
type StepDiffs struct {
	Steps []StepDiff `json:"steps"`
}

// NodeDetail is everything about one node that the projected transcript
// caps or leaves out.
type NodeDetail struct {
	Calls     []CallDetail `json:"calls,omitempty"`
	Narration []string     `json:"narration,omitempty"`
	// Thinking is a beat's thoughts, or a message's reasoning as one entry.
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
	// Body is a run event's whole body.
	Body string `json:"body,omitempty"`
}

// NodeDetailResponse is GET /api/sessions/{id}/nodes/{nodeId}'s answer: the
// node's wire form and the detail the projection left out.
type NodeDetailResponse struct {
	Node   WireNode   `json:"node"`
	Detail NodeDetail `json:"detail"`
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
	// Hooks is the hooks audit trail as the registry encodes it: an array of
	// records whose failed_open key is not camelCase, which the wire rules
	// refuse in a typed field, so each travels as raw bytes.
	Hooks   []json.RawMessage `json:"hooks,omitempty"`
	Sandbox json.RawMessage   `json:"sandbox,omitempty"`
	Symbols []SymbolDetail    `json:"symbols,omitempty"`
	Notice  *NoticeDetail     `json:"notice,omitempty"`
	// Execution is the structured command-execution evidence this call
	// carries: what was launched, who owned it, and how it ended. It is
	// separate from Notice, which is coaching prose written for the model.
	Execution *ExecutionDetail `json:"execution,omitempty"`
	// Workspace is the workspace lifecycle evidence this call carried, when it
	// carried any: the receipt's task key, candidate and integration state. It
	// is historical — what the call recorded when it ran.
	Workspace *SubagentWorkspace `json:"workspace,omitempty"`
	// Summary is the call's own summary, uncut.
	Summary string `json:"summary,omitempty"`
}

// SubagentWorkspace is one child's owned-workspace lifecycle evidence: what
// the child was bound to, the commit it produced, the checkout that work
// belongs in, and what became of the integration and of the workspace's
// artifacts.
//
// Every field is optional, and an empty one is unknown rather than
// zero-valued fact. Empty Execution, Integration or Cleanup means the record
// predates the state or has not settled — never success. CandidateSHA is empty
// for a child that produced no commit, and Reason is empty unless the record
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

// ExecutionDetail is one managed command execution's structured evidence:
// what was launched, who owned it, how the launching caller related to it
// and how it ended.
//
// Every field is optional. An absent Exit means no exit code was ever
// observed — a start-up failure, a still-running continuation, a lost
// record — and is never success. Empty State or Outcome means the record
// predates structured evidence or has not settled, which is likewise
// unknown rather than zero-valued fact.
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
	// Wait is the effective foreground wait the launch used; DetachReason
	// is why and DetachedAt when a wait handed the execution to the
	// background.
	Wait         string `json:"wait,omitempty"`
	DetachReason string `json:"detachReason,omitempty"`
	DetachedAt   int64  `json:"detachedAt,omitempty"`
	// StopCause and StopRequestedAt record an accepted explicit stop
	// request separately from the outcome it produced.
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

// StepDetail is the step record behind a node: who spoke, with which model,
// and why.
type StepDetail struct {
	ID       int64  `json:"id,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	// Why is the line the step was taken for.
	Why    string `json:"why,omitempty"`
	TodoID string `json:"todoId,omitempty"`
}

// CompletionDetail is a completion's retained output and its report,
// neither capped.
type CompletionDetail struct {
	Output string `json:"output,omitempty"`
	Report string `json:"report,omitempty"`
	// Truncated is the completion's own flag: the job dropped earlier
	// output before it was retained.
	Truncated bool `json:"truncated,omitempty"`
}

// LastRequest is GET /api/sessions/{id}/last-request's answer: the newest
// model request the session made, or a null Request when it has made none.
type LastRequest struct {
	Request *ModelRequest `json:"request"`
}

// ModelRequest is one model request as it went out, with what came of it.
type ModelRequest struct {
	AttemptID       uint64           `json:"attemptId"`
	At              int64            `json:"at,omitempty"`
	Provider        string           `json:"provider,omitempty"`
	Model           string           `json:"model,omitempty"`
	Generation      string           `json:"generation,omitempty"`
	LeafID          int64            `json:"leafId,omitempty"`
	Messages        []RequestMessage `json:"messages"`
	Tools           []RequestTool    `json:"tools"`
	Options         RequestOptions   `json:"options"`
	Outcome         RequestOutcome   `json:"outcome"`
	Truncated       bool             `json:"truncated,omitempty"`
	OmittedBytes    int              `json:"omittedBytes,omitempty"`
	MessagesOmitted int              `json:"messagesOmitted,omitempty"`
	ToolsOmitted    int              `json:"toolsOmitted,omitempty"`
	PackTokens      int              `json:"packTokens,omitempty"`
	PackWindow      int              `json:"packWindow,omitempty"`
	PackTruncated   bool             `json:"packTruncated,omitempty"`
	PackSections    int              `json:"packSections,omitempty"`
	PackKnown       bool             `json:"packKnown,omitempty"`
}

// RequestMessage is one message of the request.
type RequestMessage struct {
	Role         string            `json:"role"`
	Content      string            `json:"content"`
	ToolCalls    []RequestToolCall `json:"toolCalls,omitempty"`
	ToolCallID   string            `json:"toolCallId,omitempty"`
	Truncated    bool              `json:"truncated,omitempty"`
	OmittedBytes int               `json:"omittedBytes,omitempty"`
}

// RequestToolCall is a tool call attached to a message.
type RequestToolCall struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Args         string `json:"args"`
	Truncated    bool   `json:"truncated,omitempty"`
	OmittedBytes int    `json:"omittedBytes,omitempty"`
}

// RequestTool is a tool definition offered on the request.
type RequestTool struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Parameters   string `json:"parameters,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	OmittedBytes int    `json:"omittedBytes,omitempty"`
}

// RequestOptions records the options that actually went out.
type RequestOptions struct {
	Thinking       string   `json:"thinking,omitempty"`
	Streaming      bool     `json:"streaming"`
	MaxTokens      *int     `json:"maxTokens,omitempty"`
	Temperature    *float64 `json:"temperature,omitempty"`
	ResponseFormat string   `json:"responseFormat,omitempty"`
	ToolChoice     string   `json:"toolChoice,omitempty"`
}

// RequestOutcome is what became of the attempt.
type RequestOutcome struct {
	Status string `json:"status"`
	Err    string `json:"err,omitempty"`
	At     int64  `json:"at,omitempty"`
}
