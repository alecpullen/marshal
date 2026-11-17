package contract

import "encoding/json"

// A project's settings, its health and its discovery report (F6 decision 12):
// `GET`/`PUT /api/projects/settings` (a conditional write), `GET
// /api/projects/health` and `POST /api/repos/{id}/discover`.
//
// The declarations are the bridge's own (web/bridge/workspace.go,
// projects.go and discovery.go), moved here so both clients are generated from
// one source; the bridge aliases each name to the type below.
//
// Every pointer is a stated absence, never an unset default: an explicit false
// and a missing value are different, and the resolution chain the bridge
// applies depends on telling them apart.

// ProjectIntake is the project's default intake configuration.
type ProjectIntake struct {
	RepoID  string   `json:"repoId,omitempty"`
	Labels  []string `json:"labels,omitempty"`
	Clients []string `json:"clients,omitempty"`
}

// FactoryDefaults is a project's default factory configuration: every field a
// handoff inherits unless it states its own.
type FactoryDefaults struct {
	TargetBranch         string                    `json:"targetBranch,omitempty"`
	Workspace            string                    `json:"workspace,omitempty"`
	HostID               string                    `json:"hostId,omitempty"`
	ImplementerRouting   json.RawMessage           `json:"implementerRouting,omitempty"`
	ReviewerRouting      json.RawMessage           `json:"reviewerRouting,omitempty"`
	ReviewerInstructions string                    `json:"reviewerInstructions,omitempty"`
	ExpectedChecks       []FactoryCheckKey         `json:"expectedChecks,omitempty"`
	MaxRounds            int                       `json:"maxRounds,omitempty"`
	CIPollSeconds        int                       `json:"ciPollSeconds,omitempty"`
	CIWaitSeconds        int                       `json:"ciWaitSeconds,omitempty"`
	ImplementerSubagents *FactorySubagentOverrides `json:"implementerSubagents,omitempty"`
	ReviewerSubagents    *FactorySubagentOverrides `json:"reviewerSubagents,omitempty"`
}

// ProjectSettings is one project's settings document. The ETag that names a
// particular value is a header, never part of the body.
type ProjectSettings struct {
	Workspace string          `json:"workspace,omitempty"`
	Routing   json.RawMessage `json:"routing,omitempty"`
	Mode      string          `json:"mode,omitempty"`
	// Auto accompanies Mode as a starting default. It is a pointer so an
	// absent value stays distinguishable from an explicit false.
	Auto *bool `json:"auto,omitempty"`
	// Isolated is the default isolation; nil leaves the caller's default.
	Isolated    *bool         `json:"isolated,omitempty"`
	ShipTarget  string        `json:"shipTarget,omitempty"`
	Intake      ProjectIntake `json:"intake"`
	Automations *Automations  `json:"automations,omitempty"`
	// FactoryDefaults are the project's default factory configuration. A
	// handoff inherits every field it leaves unset.
	FactoryDefaults *FactoryDefaults `json:"factoryDefaults,omitempty"`
	// PlannerDelegationEnabled lets one designated MCP client answer the
	// planner's questions for this project.
	PlannerDelegationEnabled bool `json:"plannerDelegationEnabled,omitempty"`
	// DesignatedMCPClientID names the one client that may answer while
	// delegation is enabled. Empty means no client is designated.
	DesignatedMCPClientID string `json:"designatedMCPClientId,omitempty"`
	// AutoAssessPlanChanges turns automatic assessment of a proposed factory
	// plan revision on or off. Nil means the default, which is on.
	AutoAssessPlanChanges *bool `json:"autoAssessPlanChanges,omitempty"`
}

// MirrorHealth is whether a repository's mirror exists and how fresh it is.
type MirrorHealth struct {
	RepoID string `json:"repoId"`
	// Present is false when the repo has no mirror yet.
	Present bool `json:"present"`
	// AgeSeconds is the time since the mirror last fetched.
	AgeSeconds int64  `json:"ageSeconds,omitempty"`
	Head       string `json:"head,omitempty"`
}

// WorkspaceHealth is whether a project's default workspace is usable.
type WorkspaceHealth struct {
	Ref      string `json:"ref"`
	Resolves bool   `json:"resolves"`
	Built    bool   `json:"built"`
	Error    string `json:"error,omitempty"`
}

// VerifyCommands is a project's configured gate commands. Empty strings mean
// unset, in which case the gate is skipped unless the engine detects a
// default.
type VerifyCommands struct {
	Build string `json:"build"`
	Test  string `json:"test"`
}

// ProjectHealth is GET /api/projects/health.
type ProjectHealth struct {
	// GateRunnable is "yes", "no" (the last verify was skipped: nothing to
	// run) or "unknown" (no agent of the project has verified).
	GateRunnable      string           `json:"gateRunnable"`
	MirrorFresh       []MirrorHealth   `json:"mirrorFresh"`
	OrphanWorktrees   []string         `json:"orphanWorktrees"`
	Trust             string           `json:"trust"`
	WorkspaceResolves *WorkspaceHealth `json:"workspaceResolves,omitempty"`
	// Verify is the project's verify-gate commands from [sdd.verify] in
	// .marshal/config.toml. Only reported for a trusted project, since the
	// engine ignores an untrusted project's config.
	Verify VerifyCommands `json:"verify"`
}

// DiscoveryCapabilityState is the outcome of one read-only capability probe.
type DiscoveryCapabilityState string

// The capability states a probe reports.
const (
	DiscoveryCapabilitySupported              DiscoveryCapabilityState = "supported"
	DiscoveryCapabilityUnconfigured           DiscoveryCapabilityState = "unconfigured"
	DiscoveryCapabilityInsufficientPermission DiscoveryCapabilityState = "insufficient_permission"
	DiscoveryCapabilityTemporarilyUnavailable DiscoveryCapabilityState = "temporarily_unavailable"
	DiscoveryCapabilityUnsupported            DiscoveryCapabilityState = "unsupported"
)

// DiscoveryCapability is one capability the discovery probe tested.
type DiscoveryCapability struct {
	Key    string                   `json:"key"`
	State  DiscoveryCapabilityState `json:"state"`
	Detail string                   `json:"detail,omitempty"`
}

// DiscoveryFact is something detected in the checkout. Origin distinguishes a
// detected fact from a recommendation.
type DiscoveryFact struct {
	Origin   string `json:"origin"`
	Section  string `json:"section"`
	Kind     string `json:"kind"`
	Value    string `json:"value"`
	Source   string `json:"source"`
	Revision string `json:"revision,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// DiscoveryRecommendation is an advisory setup option. It is never applied by
// discovery.
type DiscoveryRecommendation struct {
	Origin  string `json:"origin"`
	Section string `json:"section"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
	Reason  string `json:"reason"`
}

// DiscoveryGap explains a capability that is not supported, so the UI can turn
// it into an actionable next step.
type DiscoveryGap struct {
	Key     string                   `json:"key"`
	Section string                   `json:"section"`
	State   DiscoveryCapabilityState `json:"state"`
	Detail  string                   `json:"detail"`
}

// DiscoveryWorktree is the locally observable Git state of a linked checkout.
// Dirty is a pointer: nil means not determined, which differs from a known
// clean checkout.
type DiscoveryWorktree struct {
	Path       string   `json:"path"`
	Branch     string   `json:"branch,omitempty"`
	Head       string   `json:"head,omitempty"`
	Dirty      *bool    `json:"dirty"`
	DirtyFiles []string `json:"dirtyFiles,omitempty"`
	Detail     string   `json:"detail,omitempty"`
}

// DiscoveryReport is the single document `POST /api/repos/{id}/discover`
// returns. Status is "ok", "degraded" or "unavailable"; the report slices are
// always non-nil so the wire shape stays stable.
type DiscoveryReport struct {
	RepoID       string                    `json:"repoId"`
	Forge        string                    `json:"forge"`
	Status       string                    `json:"status"`
	Error        string                    `json:"error,omitempty"`
	Revision     string                    `json:"revision,omitempty"`
	Branch       string                    `json:"branch,omitempty"`
	Worktree     *DiscoveryWorktree        `json:"worktree,omitempty"`
	Capabilities []DiscoveryCapability     `json:"capabilities"`
	Facts        []DiscoveryFact           `json:"facts"`
	Recommended  []DiscoveryRecommendation `json:"recommended"`
	Gaps         []DiscoveryGap            `json:"gaps"`
}
