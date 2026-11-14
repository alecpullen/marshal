package contract

import "encoding/json"

// SpawnRequest is a POST /api/agents body.
type SpawnRequest struct {
	Project string `json:"project"`
	Name    string `json:"name,omitempty"`
	Mode    string `json:"mode,omitempty"`
	// Auto accompanies Mode as a starting default. It is a pointer so an
	// absent value stays distinguishable from an explicit false.
	Auto   *bool  `json:"auto,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	// Isolated is the default isolation; nil leaves the caller's default.
	Isolated *bool           `json:"isolated,omitempty"`
	Branch   string          `json:"branch,omitempty"`
	BaseRef  string          `json:"baseRef,omitempty"`
	Routing  json.RawMessage `json:"routing,omitempty"`
	// Workspace is the workspace reference the agent runs in.
	Workspace string `json:"workspace,omitempty"`
}

// SpawnResult is a POST /api/agents answer.
type SpawnResult struct {
	AgentID string `json:"agentId"`
	// Warning is a non-fatal spawn problem reported after an agent id was
	// minted.
	Warning string `json:"warning,omitempty"`
}
