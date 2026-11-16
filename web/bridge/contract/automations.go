package contract

import "encoding/json"

// Automations is a project's forge automation settings
// (web/bridge/automation.go).
type Automations struct {
	ReviewBot *ReviewBotSettings `json:"reviewBot,omitempty"`
	CIFixer   *CIFixerSettings   `json:"ciFixer,omitempty"`
}

// ReviewBotSettings configures the pull-request review bot.
type ReviewBotSettings struct {
	Enabled bool   `json:"enabled"`
	RepoID  string `json:"repoId"`
	// Labels, Authors and SkipDrafts filter which pull requests are
	// reviewed. Empty lists match everything.
	Labels     []string `json:"labels,omitempty"`
	SkipDrafts bool     `json:"skipDrafts"`
	Authors    []string `json:"authors,omitempty"`
	// Routing overrides the review recipe's model routing.
	Routing json.RawMessage `json:"routing,omitempty"`
	// AutoPost posts a finished review without waiting, unless a finding has
	// a severity in HoldSeverities.
	AutoPost       bool     `json:"autoPost"`
	HoldSeverities []string `json:"holdSeverities,omitempty"`
}

// CIFixerSettings configures the CI fixer.
type CIFixerSettings struct {
	Enabled bool   `json:"enabled"`
	RepoID  string `json:"repoId"`
	// Branches are the watched branches, as path.Match patterns.
	Branches   []string `json:"branches"`
	MaxMinutes int      `json:"maxMinutes,omitempty"`
	MaxUSD     float64  `json:"maxUsd,omitempty"`
	// Push pushes a fix straight to a branch matching PushBranches; every
	// other fix opens a pull request.
	Push         bool     `json:"push"`
	PushBranches []string `json:"pushBranches,omitempty"`
}

// ProjectAutomations is the part of a project's settings document the
// Automations screens read: GET and PUT /api/projects/settings carry the
// whole document, of which this is the one key these screens change.
type ProjectAutomations struct {
	Automations *Automations `json:"automations,omitempty"`
}

// Finding is one finding of a review draft. The bridge's DraftFinding embeds
// its review finding, so the wire is flat and this is too.
type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	StepNode string `json:"stepNode,omitempty"`
}

// ReviewDraft is one review awaiting a person: GET
// /api/automations/review/drafts' answer, and what post and edit answer
// (web/bridge/reviewbot.go).
type ReviewDraft struct {
	ID        string    `json:"id"`
	RepoID    string    `json:"repoId"`
	Number    int       `json:"number"`
	Title     string    `json:"title,omitempty"`
	PRURL     string    `json:"prUrl,omitempty"`
	HeadRef   string    `json:"headRef,omitempty"`
	HeadSHA   string    `json:"headSha"`
	Project   string    `json:"project,omitempty"`
	AgentID   string    `json:"agentId,omitempty"`
	Findings  []Finding `json:"findings"`
	Summary   string    `json:"summary"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	ReviewURL string    `json:"reviewUrl,omitempty"`
	CreatedAt string    `json:"createdAt"`
}

// CIHistory is one CI fixer run: GET /api/automations/ci/history's answer
// (web/bridge/cifixer.go).
type CIHistory struct {
	ID        string  `json:"id"`
	RepoID    string  `json:"repoId"`
	Branch    string  `json:"branch,omitempty"`
	SHA       string  `json:"sha"`
	Check     string  `json:"check"`
	Status    string  `json:"status"`
	Reason    string  `json:"reason,omitempty"`
	PRURL     string  `json:"prUrl,omitempty"`
	AgentID   string  `json:"agentId,omitempty"`
	CostUSD   float64 `json:"costUsd"`
	CreatedAt string  `json:"createdAt"`
	// Pushed marks a fix pushed straight to the branch.
	Pushed bool `json:"pushed,omitempty"`
}
