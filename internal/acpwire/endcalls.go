package acpwire

// The pipeline, end-flow and session methods, and the end flow's update (P3
// plan 6c).
const (
	MethodSwarmStart   = "session/swarm_start"
	MethodSDDPreflight = "session/sdd_preflight"
	MethodSDDStart     = "session/sdd_start"
	MethodSDDAnswer    = "session/sdd_answer"
	MethodSDDStop      = "session/sdd_stop"
	MethodSDDAuthor    = "session/sdd_author"
	MethodEndWork      = "session/end_work"
	MethodEndAccept    = "session/end_accept"
	MethodEndWait      = "session/end_wait"
	MethodExitAnalysis = "session/exit_analysis"
	MethodPostmortem   = "session/postmortem"
	MethodSessionNew   = "session/new"
	MethodSessionClose = "session/close"
	MethodSessionList  = "session/list"
	UpdateEndWork      = "end_work"
)

// SwarmCallParams is session/swarm_start's params from a marshal client: ACP's
// session and goal, plus SwarmParams' role overrides.
type SwarmCallParams struct {
	SessionID string            `json:"sessionId"`
	Goal      string            `json:"goal"`
	Overrides map[string]string `json:"overrides,omitempty"`
}

// SDDCallParams is session/sdd_start's params from a marshal client: ACP's
// session and plan path, plus SDDParams' strategy, overrides and verify
// commands.
type SDDCallParams struct {
	SessionID string            `json:"sessionId"`
	PlanPath  string            `json:"planPath"`
	Strategy  string            `json:"strategy,omitempty"`
	Overrides map[string]string `json:"overrides,omitempty"`
	Verify    *VerifyCommands   `json:"verify,omitempty"`
}

// SDDAnswerParams is session/sdd_answer's params; ACP's carry the same keys.
type SDDAnswerParams struct {
	SessionID string `json:"sessionId"`
	Answer    string `json:"answer"`
}

// PreflightCallParams is session/sdd_preflight's params.
type PreflightCallParams struct {
	SessionID string              `json:"sessionId"`
	Preflight PlanPreflightParams `json:"preflight"`
}

// AuthorCallParams is session/sdd_author's params.
type AuthorCallParams struct {
	SessionID string           `json:"sessionId"`
	Author    AuthorPlanParams `json:"author"`
}

// EndAcceptCallParams is session/end_accept's params.
type EndAcceptCallParams struct {
	SessionID string          `json:"sessionId"`
	Accept    EndAcceptParams `json:"accept"`
}

// ExitAnalysisParams is session/exit_analysis' params.
type ExitAnalysisParams struct {
	SessionID string `json:"sessionId"`
	Goal      string `json:"goal"`
}

// PostmortemResult is session/postmortem's result: the report's path.
type PostmortemResult struct {
	Path string `json:"path"`
}

// NewSessionParams is session/new's params when it replaces a session: the
// session to replace, and the new one's title. Without Replaces, session/new
// is ACP's own and starts a runtime.
type NewSessionParams struct {
	Replaces string `json:"replaces"`
	Name     string `json:"name,omitempty"`
}

// NewSessionResult is the part of session/new's result a replacing client
// reads.
type NewSessionResult struct {
	SessionID string `json:"sessionId"`
}

// ListSessionsParams is session/list's params from a marshal client: its
// current session, whose service host serves the call, and the query.
type ListSessionsParams struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Limit     int    `json:"limit,omitempty"`
}

// ListSessionsResult is session/list's result for a marshal client.
type ListSessionsResult struct {
	Sessions []SessionEntry `json:"sessions"`
}

// EndWorkUpdate is an end_work update: the whole membership an end would
// stop, sent whenever it differs from the last one sent on the connection.
type EndWorkUpdate struct {
	Kind string  `json:"kind"`
	Work EndWork `json:"work"`
}
