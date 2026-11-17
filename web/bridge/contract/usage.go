package contract

// The usage, audit and disk routes (web/bridge/usage.go, audit.go,
// disk_http.go): what the fleet spent, what it did, and the disk it took.

// UsageTotals is the report's totals over its window.
type UsageTotals struct {
	CostUSD          float64 `json:"costUsd"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	AgentHours       float64 `json:"agentHours"`
	PRsShipped       int     `json:"prsShipped"`
}

// UsagePoint is one series entry: a day, a project, a role or a model.
type UsagePoint struct {
	Key     string  `json:"key"`
	CostUSD float64 `json:"costUsd"`
	Tokens  int     `json:"tokens"`
}

// UsageReport is GET /api/usage?range=&by='s answer.
type UsageReport struct {
	Range  string       `json:"range"`
	Totals UsageTotals  `json:"totals"`
	Series []UsagePoint `json:"series"`
}

// AuditEvent is one row of GET /api/audit?limit=, newest first.
type AuditEvent struct {
	TS       string `json:"ts"`
	Event    string `json:"event"`
	OwnerID  string `json:"ownerId,omitempty"`
	AgentID  string `json:"agentId,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	RepoID   string `json:"repoId,omitempty"`
	Origin   string `json:"origin,omitempty"`
	// Reason is an operator- or policy-supplied explanation.
	Reason string `json:"reason,omitempty"`
	// Detail is a short non-sensitive specific: a branch name, a repo id.
	Detail string `json:"detail,omitempty"`
	// Bytes is set by prune records.
	Bytes int64 `json:"bytes,omitempty"`
}

// DiskStatus is GET /api/disk's answer: the bytes each part of the state
// directory takes, and the budget it is held to.
type DiskStatus struct {
	Repos      int64  `json:"repos"`
	Work       int64  `json:"work"`
	Total      int64  `json:"total"`
	MeasuredAt string `json:"measuredAt"`
	BudgetMB   int64  `json:"budgetMB"`
}

// PruneResult is POST /api/prune's answer. A partial prune answers the bytes
// it did reclaim with a warning, rather than failing.
type PruneResult struct {
	Reclaimed int64  `json:"reclaimed"`
	Total     int64  `json:"total"`
	Warning   string `json:"warning,omitempty"`
}
