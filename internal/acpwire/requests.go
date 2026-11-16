package acpwire

// PendingRequests is every open request a person can answer (C7): the
// session's own, and the approvals of its running subagents. A request's Key
// stays the same while it is open, and a settled request leaves.
type PendingRequests struct {
	// Approvals are the session's own approval first, when it has one, then
	// the approval of each running subagent that has one, in the order the
	// subagents were registered.
	Approvals []ApprovalRequest `json:"approvals,omitempty"`
	Question  *QuestionRequest  `json:"question,omitempty"`
	SkillGate *SkillGateRequest `json:"skillGate,omitempty"`
	Sudo      *SudoRequest      `json:"sudo,omitempty"`
	Access    *AccessRequest    `json:"access,omitempty"`
}

// ApprovalRequest is one tool call waiting for a person's approval.
type ApprovalRequest struct {
	Key        string `json:"key"`
	SubagentID int64  `json:"subagentId,omitempty"` // 0 when the session itself asks
	Subagent   string `json:"subagent,omitempty"`   // the subagent's label
	ToolCallID string `json:"toolCallId,omitempty"`
	Tool       string `json:"tool"`
	ToolLabel  string `json:"toolLabel,omitempty"` // the tool's display name
	Command    string `json:"command,omitempty"`
	Args       string `json:"args,omitempty"`
	Risk       string `json:"risk,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Diff       string `json:"diff,omitempty"`
	Schema     string `json:"schema,omitempty"`
	// Forced marks a prompt only an exact decision may answer: a session or
	// always answer to it is a deny.
	Forced bool `json:"forced,omitempty"`
	// Access marks a typed access request occupying the approval slot. The
	// access panel answers it, through Pending's Access and AnswerAccess.
	Access bool   `json:"access,omitempty"`
	Actor  string `json:"actor,omitempty"` // the requesting step's owner; "" for the orchestrator
	Why    string `json:"why,omitempty"`   // the first sentence of that step's narration
}

// ApprovalDecision is how a person answered an approval.
type ApprovalDecision string

const (
	ApprovalOnce    ApprovalDecision = "once"
	ApprovalSession ApprovalDecision = "session"
	ApprovalAlways  ApprovalDecision = "always"
	ApprovalDeny    ApprovalDecision = "deny"
)

// ApprovalAnswer is a person's answer to an approval.
type ApprovalAnswer struct {
	Decision ApprovalDecision `json:"decision"`
	Edited   string           `json:"edited,omitempty"` // a once answer's edited command or arguments
}

// QuestionRequest is the agent's clarifying questions, asked together.
type QuestionRequest struct {
	Key       string     `json:"key"`
	Questions []Question `json:"questions"`
}

type Question struct {
	Question string           `json:"question"`
	Options  []QuestionOption `json:"options,omitempty"`
	Multi    bool             `json:"multi,omitempty"`
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Answer is a person's answer to one Question, in the questions' order.
type Answer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// AnswerUnanswered is the answer recorded for a question a person skipped.
// It equals session.AnswerUnanswered, which the runner tests for.
const AnswerUnanswered = "Unanswered"

// SkillGateRequest asks whether a skill may load while the context window is
// small.
type SkillGateRequest struct {
	Key         string `json:"key"`
	Skill       string `json:"skill"`
	Description string `json:"description,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// SkillGateChoice is how a person answered a skill gate.
type SkillGateChoice string

const (
	SkillGateAllowOnce  SkillGateChoice = "allow_once"
	SkillGateAllowSkill SkillGateChoice = "allow_skill" // no more prompts for this skill this session
	SkillGateAllowAll   SkillGateChoice = "allow_all"   // the gate is off for this session
	SkillGateDeny       SkillGateChoice = "deny"
)

// SudoRequest asks for the password a privileged command needs.
type SudoRequest struct {
	Key             string `json:"key"`
	Command         string `json:"command,omitempty"`
	Reason          string `json:"reason,omitempty"`
	StepID          int64  `json:"stepId,omitempty"`
	AllowUnlock     bool   `json:"allowUnlock,omitempty"`
	Error           string `json:"error,omitempty"` // the previous attempt's failure; "" on the first
	CommandApproval bool   `json:"commandApproval,omitempty"`
}

// SudoAnswer is a person's answer to a sudo prompt. Password travels only
// here, and the handler zeroes it after use. OK false is a deny, with no
// password.
type SudoAnswer struct {
	OK       bool   `json:"ok"`
	Password []byte `json:"password,omitempty"`
}

// AccessState is the access tuple an access request was raised against. Mode
// keeps the session's spelling, so a choice that echoes it compares equal.
type AccessState struct {
	Mode     string `json:"mode"`
	Auto     bool   `json:"auto,omitempty"`
	System   bool   `json:"system,omitempty"`
	Revision uint64 `json:"revision,omitempty"`
}

// AccessRequest asks a person to grant editing, system access or both.
type AccessRequest struct {
	RequestID      string      `json:"requestId"`
	SessionID      string      `json:"sessionId"`
	Actor          string      `json:"actor,omitempty"`
	StepID         int64       `json:"stepId,omitempty"`
	Reason         string      `json:"reason"`
	Current        AccessState `json:"current"`
	MissingEditing bool        `json:"missingEditing"`
	MissingSystem  bool        `json:"missingSystem"`
}

// AccessChoice is a person's answer to an AccessRequest. Current is the tuple
// the panel showed: a stale one makes the result refresh_required.
type AccessChoice struct {
	RequestID string      `json:"requestId"`
	SessionID string      `json:"sessionId"`
	Current   AccessState `json:"current"`
	Approved  bool        `json:"approved"`
	Mode      string      `json:"mode,omitempty"`
}

// AccessResult is how an access request settled, and the tuple in force after.
type AccessResult struct {
	RequestID string      `json:"requestId,omitempty"`
	SessionID string      `json:"sessionId"`
	Status    string      `json:"status"` // an Access* value
	Effective AccessState `json:"effective"`
	Message   string      `json:"message,omitempty"`
}

// AccessResult.Status values. They equal session.AccessStatus's.
const (
	AccessAvailable = "already_available"
	AccessGranted   = "granted"
	AccessDenied    = "denied"
	AccessCanceled  = "canceled"
	AccessFailed    = "failed"
	AccessRefresh   = "refresh_required"
	AccessExpired   = "expired"
)

// The request methods and update kind (P3 R5).
const (
	MethodPending         = "session/pending"
	MethodAnswerApproval  = "session/answer_approval"
	MethodAnswerQuestion  = "session/answer_question"
	MethodAnswerSkillGate = "session/answer_skill_gate"
	MethodAnswerSudo      = "session/answer_sudo"
	MethodAnswerAccess    = "session/answer_access"
	MethodRollback        = "session/rollback"
	UpdatePending         = "pending_requests"
)

// PendingParams is session/pending's and session/rollback's params.
type PendingParams struct {
	SessionID string `json:"sessionId"`
}

// PendingState is what the request mirror holds: the open requests, and the
// typed access request the access panel presents (ReconcileAccess's answer),
// read after a request that asked for nothing was settled.
type PendingState struct {
	Requests PendingRequests `json:"requests"`
	Access   *AccessRequest  `json:"access,omitempty"`
}

// PendingUpdate is a pending_requests update: the whole state, sent whenever
// it differs from the last one sent on the connection.
type PendingUpdate struct {
	Kind    string       `json:"kind"`
	Pending PendingState `json:"pending"`
}

// AnswerApprovalParams is session/answer_approval's params.
type AnswerApprovalParams struct {
	SessionID string         `json:"sessionId"`
	Key       string         `json:"key"`
	Answer    ApprovalAnswer `json:"answer"`
}

// AnswerQuestionParams is session/answer_question's params: one answer per
// question, in order.
type AnswerQuestionParams struct {
	SessionID string   `json:"sessionId"`
	Key       string   `json:"key"`
	Answers   []Answer `json:"answers"`
}

// AnswerSkillGateParams is session/answer_skill_gate's params.
type AnswerSkillGateParams struct {
	SessionID string          `json:"sessionId"`
	Key       string          `json:"key"`
	Choice    SkillGateChoice `json:"choice"`
}

// AnswerSudoParams is session/answer_sudo's params. They carry a password:
// neither end logs them (P3 R8).
type AnswerSudoParams struct {
	SessionID string     `json:"sessionId"`
	Key       string     `json:"key"`
	Answer    SudoAnswer `json:"answer"`
}

// AnswerAccessParams is session/answer_access's params. The call answers an
// AccessResult.
type AnswerAccessParams struct {
	SessionID string       `json:"sessionId"`
	Choice    AccessChoice `json:"choice"`
}
