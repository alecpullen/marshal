package acpwire

// These are what Session.Sheet returns and what Session.RefreshSheet takes.
// Times are Unix milliseconds (see Millis) and durations are milliseconds.

// SessionSheet is what the session sheet's sections read (C5), for the
// session or for one subagent. Its database and git values are the ones the
// last RefreshSheet read.
type SessionSheet struct {
	SubagentID  int64              `json:"subagentId,omitempty"` // whose sheet: 0 for the session's own
	StartedAt   int64              `json:"startedAt,omitempty"`
	Context     SheetContext       `json:"context"`           // the session's only
	Changed     []SheetChangedFile `json:"changed,omitempty"` // the session's only
	Turns       []SheetTurn        `json:"turns,omitempty"`   // the session's only; most recent first
	Totals      SheetTotals        `json:"totals"`            // the session's only
	Fleet       []SheetFleetRow    `json:"fleet,omitempty"`
	Repo        SheetRepo          `json:"repo"`
	Tools       []SheetTool        `json:"tools,omitempty"` // most-called first
	Files       []SheetFile        `json:"files,omitempty"` // most recently touched first
	Rules       []string           `json:"rules,omitempty"`
	Skills      []string           `json:"skills,omitempty"`
	ToolBudget  SheetToolBudget    `json:"toolBudget"`
	LoadedTools int                `json:"loadedTools,omitempty"`
	Sandbox     SheetSandbox       `json:"sandbox"`
}

// SheetContext is the context pack's size and its sections' sizes. Every
// section is listed, an empty one included.
type SheetContext struct {
	MaxTokens       int                   `json:"maxTokens,omitempty"`
	EstimatedTokens int                   `json:"estimatedTokens,omitempty"`
	Sections        []SheetContextSection `json:"sections,omitempty"`
}

// SheetContextSection is one context-pack section's size.
type SheetContextSection struct {
	Title           string `json:"title"`
	EstimatedTokens int    `json:"estimatedTokens,omitempty"`
}

// SheetChangedFile is one file changed against the sheet's base. Status is
// git's status letter ("A", "M", "D", …).
type SheetChangedFile struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Added   int    `json:"added,omitempty"`
	Removed int    `json:"removed,omitempty"`
}

// SheetTurn is one turn's token counts, from the turn metrics.
type SheetTurn struct {
	StartedAt        int64 `json:"startedAt,omitempty"`
	PromptTokens     int   `json:"promptTokens,omitempty"`
	CompletionTokens int   `json:"completionTokens,omitempty"`
	CacheReadTokens  int   `json:"cacheReadTokens,omitempty"`
}

// SheetTotals is the session's usage across all its turns.
type SheetTotals struct {
	Turns            int   `json:"turns,omitempty"`
	PromptTokens     int64 `json:"promptTokens,omitempty"`
	CompletionTokens int64 `json:"completionTokens,omitempty"`
	CacheReadTokens  int64 `json:"cacheReadTokens,omitempty"`
}

// SheetFleetRow is one agent worktree, against the project root's HEAD.
// AgeMs is the age of its branch tip.
type SheetFleetRow struct {
	Branch string `json:"branch"`
	Ahead  int    `json:"ahead,omitempty"`
	Behind int    `json:"behind,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
	AgeMs  int64  `json:"ageMs,omitempty"`
}

// SheetRepo is the indexed repository's size.
type SheetRepo struct {
	Files   int `json:"files,omitempty"`
	Symbols int `json:"symbols,omitempty"`
}

// SheetTool is one tool's calls across the tool-call log.
type SheetTool struct {
	Name      string `json:"name"`
	Calls     int    `json:"calls"`
	Errors    int    `json:"errors,omitempty"`
	SlowestMs int64  `json:"slowestMs,omitempty"`
}

// SheetFile is one file's edits and reads across the tool-call log. LastAt
// is its latest touch.
type SheetFile struct {
	Path   string `json:"path"`
	Edits  int    `json:"edits,omitempty"`
	Reads  int    `json:"reads,omitempty"`
	LastAt int64  `json:"lastAt,omitempty"`
}

// SheetToolBudget mirrors the session's ToolBudget. Max 0 means no cap.
type SheetToolBudget struct {
	Used int `json:"used,omitempty"`
	Max  int `json:"max,omitempty"`
}

// SheetSandbox is the sandbox posture: the backend's name, and whether shell
// commands may reach the network.
type SheetSandbox struct {
	Backend      string `json:"backend,omitempty"`
	AllowNetwork bool   `json:"allowNetwork,omitempty"`
}

// SheetRefresh names the sheet values a RefreshSheet call re-reads.
type SheetRefresh struct {
	Rebase  bool `json:"rebase,omitempty"`  // HEAD of the active root as the base, then the changed files
	Changed bool `json:"changed,omitempty"` // the changed files against the current base
	Stats   bool `json:"stats,omitempty"`   // the turn metrics, the usage totals and the repo counts
	Fleet   bool `json:"fleet,omitempty"`   // the agent worktrees
}
