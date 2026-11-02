package acpwire

// WorkSnapshot is a session's live work projection (C8): what the tray, the
// live box and the Tasks panel draw. A snapshot is never modified once made.
type WorkSnapshot struct {
	Version             int        `json:"version"`
	ConversationID      string     `json:"conversationId"`
	RuntimeEpoch        string     `json:"runtimeEpoch"`
	Revision            uint64     `json:"revision"`
	HistoryRevision     uint64     `json:"historyRevision"`
	ObservedAt          int64      `json:"observedAt,omitempty"`
	Items               []WorkItem `json:"items"`
	ActiveCount         int        `json:"activeCount"`
	AttentionCount      int        `json:"attentionCount"`
	SuccessReceiptCount int        `json:"successReceiptCount"`
	StoppedReceiptCount int        `json:"stoppedReceiptCount"`
}

// WorkItem is one unit of work: a command, a watch, a subagent.
type WorkItem struct {
	TaskKey        string              `json:"taskKey"`
	DisplayID      string              `json:"displayId"`
	ConversationID string              `json:"conversationId"`
	RuntimeEpoch   string              `json:"runtimeEpoch"`
	SourceID       string              `json:"sourceId"`
	SourceRevision uint64              `json:"sourceRevision"`
	Kind           string              `json:"kind"`
	Label          string              `json:"label"`
	Owner          string              `json:"owner,omitempty"`
	Origin         WorkOrigin          `json:"origin"`
	State          string              `json:"state"`
	Outcome        string              `json:"outcome,omitempty"`
	ExitCode       *int                `json:"exitCode,omitempty"`
	Attachment     string              `json:"attachment,omitempty"`
	StartedAt      int64               `json:"startedAt,omitempty"`
	EndedAt        int64               `json:"endedAt,omitempty"`
	ResultRevision string              `json:"terminalResultRevision,omitempty"`
	Activity       string              `json:"activity,omitempty"`
	OutputTail     string              `json:"outputTail,omitempty"`
	Warning        string              `json:"warning,omitempty"`
	Attention      string              `json:"attention,omitempty"`
	Reference      WorkReference       `json:"reference"`
	Prompt         *WorkPrompt         `json:"prompt,omitempty"`
	Capabilities   WorkCapabilities    `json:"capabilities"`
	ProgressConfig *WorkProgressConfig `json:"progressConfig,omitempty"`
	Progress       *WorkProgress       `json:"progress,omitempty"`
}

// WorkOrigin is the conversation work an item came from.
type WorkOrigin struct {
	TurnID     string `json:"turnId,omitempty"`
	StepID     string `json:"stepId,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
}

// WorkReference points at a kept result: the transcript message, the tree
// node or the child session that holds the full detail.
type WorkReference struct {
	MessageID      int64  `json:"messageId,omitempty"`
	NodeKey        string `json:"nodeKey,omitempty"`
	ChildSessionID string `json:"childSessionId,omitempty"`
}

// WorkCapabilities lists the actions a client may ask for on one item.
type WorkCapabilities struct {
	Inspect       bool `json:"inspect"`
	Stop          bool `json:"stop"`
	Acknowledge   bool `json:"acknowledge"`
	ResolvePrompt bool `json:"resolvePrompt"`
}

// WorkPrompt names a blocked item's request waiting for an answer.
type WorkPrompt struct {
	Kind      string `json:"kind"`
	RequestID string `json:"requestId"`
	AgentID   int64  `json:"agentId,omitempty"`
}

// WorkProgressConfig is how an item's progress is produced.
type WorkProgressConfig struct {
	Profile string `json:"profile,omitempty"`
	Source  string `json:"source,omitempty"`
	Title   string `json:"title,omitempty"`
}

// WorkProgress is one progress observation: the producer's frame, flattened,
// and the times Marshal observed and measured it.
type WorkProgress struct {
	Version         int          `json:"version"`
	Sequence        uint64       `json:"sequence"`
	Phase           *WorkPhase   `json:"phase,omitempty"`
	PrimaryMetricID string       `json:"primaryMetricId,omitempty"`
	Metrics         []WorkMetric `json:"metrics,omitempty"`
	Rows            []WorkRow    `json:"rows,omitempty"`
	Detail          string       `json:"detail,omitempty"`
	ObservedAt      int64        `json:"observedAt,omitempty"`
	MeasuredAt      int64        `json:"measuredAt,omitempty"`
	Partial         bool         `json:"partial,omitempty"`
	Warning         string       `json:"warning,omitempty"`
	ReportedError   bool         `json:"reportedError,omitempty"`
}

type WorkPhase struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Index int    `json:"index,omitempty"`
	Total int    `json:"total,omitempty"`
}

type WorkMetric struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Unit    string   `json:"unit"`
	Current float64  `json:"current"`
	Total   *float64 `json:"total,omitempty"`
	Rate    *float64 `json:"rate,omitempty"`
}

type WorkRow struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	State string `json:"state"`
}

// WorkQuery asks for one page of the work list. View is "attention" or
// "history"; Cursor resumes a previous page. The session names the
// conversation.
type WorkQuery struct {
	View   string `json:"view,omitempty"`
	Query  string `json:"query,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// WorkPage is one page of the work list.
type WorkPage struct {
	RuntimeEpoch string     `json:"runtimeEpoch"`
	Revision     uint64     `json:"revision"`
	Items        []WorkItem `json:"items"`
	NextCursor   string     `json:"nextCursor,omitempty"`
}

// WorkDetail is the bounded detail of one item.
type WorkDetail struct {
	Item        WorkItem `json:"item"`
	Output      string   `json:"output"`
	Raw         bool     `json:"raw"`
	Report      string   `json:"report"`
	Unavailable string   `json:"unavailable"`
}

// WorkStopResult describes an accepted or refused stop request. The work
// snapshot stays the authority on the item's state.
type WorkStopResult struct {
	TaskKey  string `json:"taskKey"`
	State    string `json:"state"`
	Summary  string `json:"summary"`
	Accepted bool   `json:"accepted"`
}
