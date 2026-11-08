package acpwire

// The turn, control and work methods, and the work update (P3 plan 6b).
const (
	MethodPrompt          = "session/prompt"
	MethodStop            = "session/stop"
	MethodReview          = "session/review"
	MethodAgentRun        = "session/agent_run"
	MethodSuggest         = "session/suggest"
	MethodVerifyProposal  = "session/verify_proposal"
	MethodSteer           = "session/steer"
	MethodSteeringPop     = "session/steering_pop"
	MethodSteeringClear   = "session/steering_clear"
	MethodSetMode         = "session/set_mode"
	MethodSetEffort       = "session/set_effort"
	MethodSetSudoUnlock   = "session/set_sudo_unlock"
	MethodSubagentCancel  = "session/subagent_cancel"
	MethodSDDClear        = "session/sdd_clear"
	MethodWatchResumeTake = "session/watch_resume_take"
	MethodWorkList        = "session/work_list"
	MethodWorkInspect     = "session/work_inspect"
	MethodWorkStop        = "session/work_stop"
	UpdateWork            = "work_snapshot"

	// SteerDeliveryNow is session/steer's delivery for Send now.
	SteerDeliveryNow = "now"
)

// ControlParams is the params of a call that names only its session:
// session/verify_proposal, steering_pop, steering_clear, sdd_clear and
// watch_resume_take.
type ControlParams struct {
	SessionID string `json:"sessionId"`
}

// TextBlock is an ACP text content block. Type is "text".
type TextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// PromptCallParams is session/prompt's params from a marshal client: ACP's
// session and prompt blocks, plus PromptParams' queued text and watch resume.
// The client sends PromptParams.Text as one text block.
type PromptCallParams struct {
	SessionID   string       `json:"sessionId"`
	Prompt      []TextBlock  `json:"prompt"`
	Queued      bool         `json:"queued,omitempty"`
	WatchResume *WatchResume `json:"watchResume,omitempty"`
}

// StopParams is session/stop's params.
type StopParams struct {
	SessionID string `json:"sessionId"`
	Cause     string `json:"cause,omitempty"`
}

// ReviewCallParams is session/review's params.
type ReviewCallParams struct {
	SessionID string       `json:"sessionId"`
	Review    ReviewParams `json:"review"`
}

// AgentRunCallParams is session/agent_run's params.
type AgentRunCallParams struct {
	SessionID string         `json:"sessionId"`
	Run       AgentRunParams `json:"run"`
}

// SuggestParams is session/suggest's params.
type SuggestParams struct {
	SessionID   string `json:"sessionId"`
	LastMessage string `json:"lastMessage,omitempty"`
}

// SuggestResult is session/suggest's result.
type SuggestResult struct {
	Text string `json:"text"`
}

// SteerCallParams is session/steer's params: ACP's, whose delivery "now" with
// a turn and a request ID is Send now.
type SteerCallParams struct {
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
	Delivery  string `json:"delivery,omitempty"`
	TurnID    string `json:"turnId,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

// SteeringPopResult is session/steering_pop's result.
type SteeringPopResult struct {
	Text string `json:"text,omitempty"`
	OK   bool   `json:"ok"`
}

// EffortParams is session/set_effort's params.
type EffortParams struct {
	SessionID string `json:"sessionId"`
	Effort    string `json:"effort"`
}

// EffortResult is session/set_effort's result: the level asked for, and when
// it applies.
type EffortResult struct {
	Effort      string `json:"effort"`
	AppliesFrom string `json:"appliesFrom"`
}

// SudoUnlockParams is session/set_sudo_unlock's params.
type SudoUnlockParams struct {
	SessionID string `json:"sessionId"`
	On        bool   `json:"on"`
}

// SubagentCancelParams is session/subagent_cancel's params.
type SubagentCancelParams struct {
	SessionID  string `json:"sessionId"`
	SubagentID int64  `json:"subagentId"`
}

// SubagentCancelResult is session/subagent_cancel's result.
type SubagentCancelResult struct {
	Cancelled bool `json:"cancelled"`
}

// WatchResumeTakeResult is session/watch_resume_take's result.
type WatchResumeTakeResult struct {
	Name   string `json:"name,omitempty"`
	Repeat bool   `json:"repeat,omitempty"`
	OK     bool   `json:"ok"`
}

// WorkListParams is session/work_list's params. An empty or "live" view asks
// for the live snapshot.
type WorkListParams struct {
	SessionID string `json:"sessionId"`
	View      string `json:"view,omitempty"`
	Query     string `json:"query,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// WorkTargetParams is session/work_inspect's and session/work_stop's params.
type WorkTargetParams struct {
	SessionID string `json:"sessionId"`
	TaskKey   string `json:"taskKey"`
	Raw       bool   `json:"raw,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// WorkState is what the work mirror holds: Session.Work's two results.
type WorkState struct {
	Exists   bool         `json:"exists"`
	Snapshot WorkSnapshot `json:"snapshot"`
}

// WorkUpdate is a work_snapshot update: the whole state, sent whenever it
// differs from the last one sent on the connection.
type WorkUpdate struct {
	Kind string    `json:"kind"`
	Work WorkState `json:"work"`
}
