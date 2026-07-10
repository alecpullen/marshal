package hooks

import "encoding/json"

const (
	EventPreToolUse = "pre_tool_use"
	EventTurnEnd    = "turn_end"
)

type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionBlock Decision = "block"
	DecisionHalt  Decision = "halt"
)

type Config struct {
	FailClosed bool
	Entries    []HookEntry
}

type HookEntry struct {
	Event     string
	Matcher   string
	Command   string
	TimeoutMS int
}

type PreToolUseInput struct {
	Event     string          `json:"event"`
	ToolName  string          `json:"tool_name"`
	Args      json.RawMessage `json:"args"`
	SessionID string          `json:"session_id,omitempty"`
	TurnIndex int             `json:"turn_index,omitempty"`
	WorkDir   string          `json:"work_dir,omitempty"`
}

type TurnEndInput struct {
	Event     string `json:"event"`
	SessionID string `json:"session_id,omitempty"`
	TurnIndex int    `json:"turn_index,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
}

type Output struct {
	Decision   Decision        `json:"decision,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Rewrite    json.RawMessage `json:"rewrite,omitempty"`
	Continue   bool            `json:"continue,omitempty"`
	Message    string          `json:"message,omitempty"`
	FailedOpen bool            `json:"-"`
	HookCount  int             `json:"-"`
}
