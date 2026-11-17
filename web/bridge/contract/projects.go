package contract

// TrustState is a project's trust decision.
type TrustState string

// The trust states a project can be in.
const (
	TrustNA        TrustState = "na"
	TrustUntrusted TrustState = "untrusted"
	TrustTrusted   TrustState = "trusted"
)

// ProjectStatus is one project as GET /api/projects reports it.
type ProjectStatus struct {
	Root      string     `json:"root"`
	Available bool       `json:"available"`
	Error     string     `json:"error,omitempty"`
	Trust     TrustState `json:"trust"`
	// Isolation is "available", or a human-readable reason isolation cannot
	// be offered for this project. The composer disables its toggle and shows
	// the reason, rather than letting a spawn fail.
	Isolation string `json:"isolation"`
	// MCPProjectID is the project's stable opaque factory ID: what a client
	// grant is keyed by, and the only project identifier an MCP client is
	// given. It is optional so a bridge that has not minted one yet keeps the
	// response it always had.
	MCPProjectID string `json:"mcpProjectId,omitempty"`
	// RepoID names the registered repo this project's work comes from: the
	// repo its settings' intake names, while that repo is still registered.
	// It is what `GET /api/repos/{id}/issues` is keyed by, so it is what lets
	// a client ask a project for its issues at all. Omitted for a project
	// that names none — a local checkout with no remote, which has no issues
	// to serve — rather than an id the bridge made up.
	RepoID string `json:"repoId,omitempty"`
	// OrphanWorktrees are worktrees under this project that no known agent
	// claims. Reported, never deleted — they may hold uncommitted work.
	OrphanWorktrees []string `json:"orphanWorktrees,omitempty"`
}
