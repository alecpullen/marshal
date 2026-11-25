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

// NewProjectKind is how POST /api/projects/new makes a project.
type NewProjectKind string

// The ways a project is made from the Studio (docs/studio-flutter/new-project.md).
const (
	// NewProjectBlank makes an empty git repository at the root.
	NewProjectBlank NewProjectKind = "blank"
	// NewProjectClone clones a URL into the root.
	NewProjectClone NewProjectKind = "clone"
	// NewProjectExisting registers a directory already on the host.
	NewProjectExisting NewProjectKind = "existing"
)

// NewProjectRequest is the body of POST /api/projects/new.
type NewProjectRequest struct {
	Kind NewProjectKind `json:"kind"`
	// Root is the project directory, an absolute path on the host.
	Root string `json:"root"`
	// URL is what a clone gives git clone.
	URL string `json:"url,omitempty"`
	// Branch is a clone's --branch, when one is named.
	Branch string `json:"branch,omitempty"`
	// CredRef names a stored credential a clone is made with. Empty clones
	// with the host's own git setup.
	CredRef string `json:"credRef,omitempty"`
}

// CreationState is where a project creation has got to.
type CreationState string

// The creation states.
const (
	CreationCloning CreationState = "cloning"
	CreationReady   CreationState = "ready"
	CreationFailed  CreationState = "failed"
)

// ProjectCreation is one project the Studio asked the host to make: what
// POST /api/projects/new answers, and what GET /api/projects/new/{id} reads
// while a clone runs.
type ProjectCreation struct {
	ID    string         `json:"id"`
	Kind  NewProjectKind `json:"kind"`
	Root  string         `json:"root"`
	URL   string         `json:"url,omitempty"`
	State CreationState  `json:"state"`
	// Progress is git's latest progress line while a clone runs.
	Progress string `json:"progress,omitempty"`
	// Error is why the creation failed, in git's or the bridge's words.
	Error string `json:"error,omitempty"`
	// StartedAt is when it was asked for, in Unix milliseconds.
	StartedAt int64 `json:"startedAt"`
}

// NewProjectOptions is GET /api/projects/new: where this host puts a new
// project, and the creations it remembers, newest first.
type NewProjectOptions struct {
	// ProjectsDir is the host's projects folder, absolute.
	ProjectsDir string            `json:"projectsDir"`
	Creations   []ProjectCreation `json:"creations"`
}
