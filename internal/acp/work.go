package acp

import "encoding/json"

type WorkConfigWire struct {
	Profile string `json:"profile,omitempty"`
	Source  string `json:"source,omitempty"`
	Title   string `json:"title,omitempty"`
}
type WorkPhaseWire struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Index int    `json:"index,omitempty"`
	Total int    `json:"total,omitempty"`
}
type WorkMetricWire struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Unit    string   `json:"unit"`
	Current float64  `json:"current"`
	Total   *float64 `json:"total,omitempty"`
	Rate    *float64 `json:"rate,omitempty"`
}
type WorkRowWire struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	State string `json:"state"`
}
type WorkProgressWire struct {
	Version         int              `json:"version"`
	Sequence        uint64           `json:"sequence"`
	Phase           *WorkPhaseWire   `json:"phase,omitempty"`
	PrimaryMetricID string           `json:"primaryMetricId,omitempty"`
	Metrics         []WorkMetricWire `json:"metrics,omitempty"`
	Rows            []WorkRowWire    `json:"rows,omitempty"`
	Detail          string           `json:"detail,omitempty"`
	ObservedAt      string           `json:"observedAt"`
	MeasuredAt      string           `json:"measuredAt,omitempty"`
	Partial         bool             `json:"partial,omitempty"`
	Warning         string           `json:"warning,omitempty"`
	ReportedError   bool             `json:"reportedError,omitempty"`
}
type WorkOriginWire struct {
	TurnID     string `json:"turnId,omitempty"`
	StepID     string `json:"stepId,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
}
type WorkReferenceWire struct {
	MessageID      int64  `json:"messageId,omitempty"`
	NodeKey        string `json:"nodeKey,omitempty"`
	ChildSessionID string `json:"childSessionId,omitempty"`
}
type WorkCapabilitiesWire struct {
	Inspect       bool `json:"inspect"`
	Stop          bool `json:"stop"`
	Acknowledge   bool `json:"acknowledge"`
	ResolvePrompt bool `json:"resolvePrompt"`
}
type WorkPromptWire struct {
	Kind      string `json:"kind"`
	RequestID string `json:"requestId"`
	AgentID   int64  `json:"agentId,omitempty"`
}
type WorkItemWire struct {
	TaskKey        string               `json:"taskKey"`
	DisplayID      string               `json:"displayId"`
	ConversationID string               `json:"conversationId"`
	RuntimeEpoch   string               `json:"runtimeEpoch"`
	SourceID       string               `json:"sourceId"`
	SourceRevision uint64               `json:"sourceRevision"`
	Kind           string               `json:"kind"`
	Label          string               `json:"label"`
	Owner          string               `json:"owner,omitempty"`
	Origin         WorkOriginWire       `json:"origin"`
	State          string               `json:"state"`
	Outcome        string               `json:"outcome,omitempty"`
	ExitCode       *int                 `json:"exitCode,omitempty"`
	Attachment     string               `json:"attachment,omitempty"`
	StartedAt      string               `json:"startedAt"`
	EndedAt        string               `json:"endedAt,omitempty"`
	ResultRevision string               `json:"terminalResultRevision,omitempty"`
	Activity       string               `json:"activity,omitempty"`
	OutputTail     string               `json:"outputTail,omitempty"`
	Warning        string               `json:"warning,omitempty"`
	Attention      string               `json:"attention,omitempty"`
	Reference      WorkReferenceWire    `json:"reference"`
	Prompt         *WorkPromptWire      `json:"prompt,omitempty"`
	Capabilities   WorkCapabilitiesWire `json:"capabilities"`
	ProgressConfig *WorkConfigWire      `json:"progressConfig,omitempty"`
	Progress       *WorkProgressWire    `json:"progress,omitempty"`
}
type WorkSnapshotWire struct {
	Version             int            `json:"version"`
	ConversationID      string         `json:"conversationId"`
	RuntimeEpoch        string         `json:"runtimeEpoch"`
	Revision            uint64         `json:"revision"`
	HistoryRevision     uint64         `json:"historyRevision"`
	ObservedAt          string         `json:"observedAt"`
	Items               []WorkItemWire `json:"items"`
	ActiveCount         int            `json:"activeCount"`
	AttentionCount      int            `json:"attentionCount"`
	SuccessReceiptCount int            `json:"successReceiptCount"`
	StoppedReceiptCount int            `json:"stoppedReceiptCount"`
}
type WorkPageWire struct {
	RuntimeEpoch string         `json:"runtimeEpoch"`
	Revision     uint64         `json:"revision"`
	Items        []WorkItemWire `json:"items"`
	NextCursor   string         `json:"nextCursor,omitempty"`
}
type WorkStateUpdateWire struct {
	Kind string `json:"kind"`
	WorkSnapshotWire
}
type WorkDetailWire struct {
	Item        WorkItemWire `json:"item"`
	Output      string       `json:"output"`
	Raw         bool         `json:"raw"`
	Report      string       `json:"report"`
	Unavailable string       `json:"unavailable"`
}
type WorkStopWire struct {
	TaskKey  string `json:"taskKey"`
	State    string `json:"state"`
	Summary  string `json:"summary"`
	Accepted bool   `json:"accepted"`
}
type workListParams struct {
	SessionID string `json:"sessionId"`
	View      string `json:"view,omitempty"`
	Query     string `json:"query,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}
type workTargetParams struct {
	SessionID      string `json:"sessionId"`
	TaskKey        string `json:"taskKey"`
	ResultRevision string `json:"terminalResultRevision,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Raw            bool   `json:"raw,omitempty"`
}

func workWire[T any](value any) (T, error) {
	var result T
	b, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(b, &result)
	return result, err
}
