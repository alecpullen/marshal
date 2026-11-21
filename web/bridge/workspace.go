package bridge

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/web/bridge/contract"
)

// workspaceVersion is the fleet-state format. 12 added the pull request
// records and the asks that go with them; a version-11 file loads with no
// records.
const workspaceVersion = 12

// ErrUnknownRepo is returned when a repo id is not registered.
var ErrUnknownRepo = errors.New("bridge: unknown repo")

// DefaultOwnerID is the single implicit owner in a single-operator
// deployment. Every agent carries an owner from the first commit so that
// adding real accounts later is a new auth layer, not a migration of
// every persisted record.
const DefaultOwnerID = "local"

// Spawn origins. Policy varies by origin: an agent created by the UI is
// the operator acting directly, while other origins are subject to
// confirmation and scoping rules.
const (
	OriginUI    = "ui"
	OriginCLI   = "cli"
	OriginMCP   = "mcp"
	OriginIssue = "issue"
	// OriginSchedule, OriginReviewBot and OriginCI mark agents the bridge
	// started itself: a due schedule, a PR review automation, a CI fixer.
	OriginSchedule  = "schedule"
	OriginReviewBot = "review-bot"
	OriginCI        = "ci"
	// OriginCoordinator marks an agent a coordinated task delegated to a
	// project. It is deliberately not OriginUI: a model asking for work is not
	// the operator acting directly, so intake's usual confirmation applies and
	// no operator identity is claimed on its behalf.
	OriginCoordinator = "coordinator"
)

type Agent struct {
	ID                   string `json:"id"`
	FactoryItemID        string `json:"factoryItemId,omitempty"`
	FactoryRole          string `json:"factoryRole,omitempty"`
	FactorySpawnEffectID string `json:"factorySpawnEffectId,omitempty"`
	FactoryParked        bool   `json:"factoryParked,omitempty"`
	Project              string `json:"project"`
	Name                 string `json:"name,omitempty"`
	// Mode and Auto are the starting interaction defaults this agent was
	// spawned with, never its current grants. Auto is optional: nil means the
	// spawn named no Auto, which is not the same as an explicit false.
	Mode        string    `json:"mode,omitempty"`
	Auto        *bool     `json:"auto,omitempty"`
	Prompt      string    `json:"prompt,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	Interrupted bool      `json:"interrupted,omitempty"`
	Isolated    bool      `json:"isolated,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	// TargetBranch is the project's branch at spawn — the merge target. It
	// cannot be derived later, so it is persisted here.
	TargetBranch string `json:"targetBranch,omitempty"`
	// SessionID is the ACP session inside this agent, assigned by the
	// agent at session/new. It is persisted because reattaching to a
	// surviving container is useless without it: the transport
	// reconnects, but nothing can be addressed.
	SessionID string `json:"sessionId,omitempty"`
	// OwnerID is the human this agent belongs to. Always populated.
	OwnerID string `json:"ownerId"`
	// Origin records how the agent was created (OriginUI, OriginCLI, …).
	Origin string `json:"origin"`
	// ClientID is the MCP client that submitted this agent, when
	// Origin is "mcp". Empty for UI/CLI spawns.
	ClientID string `json:"clientId,omitempty"`
	// Profile is the resolved container shape this agent runs under.
	Profile RuntimeProfile `json:"profile"`
	// SourceKind is the workspace source ("local" in S1; "git" in S2).
	SourceKind string `json:"sourceKind,omitempty"`
	// SourceRef is the source-specific locator: a path for "local", a
	// repo URL for "git".
	SourceRef string `json:"sourceRef,omitempty"`
	// ReadOnly marks a spawn with no push path — an arbitrary-URL clone
	// rather than a registered repo. S2b routes these to a patch export
	// instead of a pull request.
	ReadOnly bool `json:"readOnly,omitempty"`
	// PushedAt is when this agent's branch last reached the remote.
	PushedAt time.Time `json:"pushedAt,omitempty"`
	// PRUrl is the pull-request URL extracted from push output, after
	// scheme and host validation. Empty when the forge printed none.
	PRUrl string `json:"prUrl,omitempty"`
	// GateOverride is nil when the gate passed on its own merits.
	GateOverride *GateOverride `json:"gateOverride,omitempty"`
	// IssueNumber and IssueURL are set when this agent was spawned from
	// an issue, so the exit path can link the pull request back to it.
	IssueNumber int    `json:"issueNumber,omitempty"`
	IssueURL    string `json:"issueUrl,omitempty"`
	// Workspace is the workspace this agent runs in, when it was spawned
	// with one (v9).
	Workspace *AgentWorkspace `json:"workspace,omitempty"`
	// ContainerName, WorkSubpath and SocketSubpath override the names
	// derived from the agent id. A pooled container keeps the names it was
	// started under. Empty means derived (v9).
	ContainerName string `json:"containerName,omitempty"`
	WorkSubpath   string `json:"workSubpath,omitempty"`
	SocketSubpath string `json:"socketSubpath,omitempty"`
	// Recipe names the recipe this agent was started from (v11).
	Recipe string `json:"recipe,omitempty"`
	// SpawnRequestID is the caller's own idempotency key for the first send
	// that started this agent (agent-first Studio, task 3). It is persisted
	// with the agent so a retried request, or one that arrives after a
	// restart, resolves to the agent the original request started rather
	// than starting a second one. Empty for every agent started without one.
	SpawnRequestID string `json:"spawnRequestId,omitempty"`
	// CoordinatorTaskID and CoordinatorAssignmentID bind this agent to the
	// coordinated task that delegated to it (agent-first Studio, task 6). They
	// are the worker's half of the durable effect identity: the assignment
	// writes them before its spawn resolves, so a lost response or a restart
	// finds the worker it already created instead of starting a second one.
	// Empty for every agent no coordinated task created.
	CoordinatorTaskID       string `json:"coordinatorTaskId,omitempty"`
	CoordinatorAssignmentID string `json:"coordinatorAssignmentId,omitempty"`
}

// AgentWorkspace is contract.AgentWorkspace; see that declaration.
type AgentWorkspace = contract.AgentWorkspace

// MCPProjectIdentity is one registered project's stable ID.
//
// It exists so an MCP client can name a project across restarts and
// equivalent spellings of the same path without ever being handed — or
// being able to supply — a host path. Root is server-only: it is written
// when the project is registered and never projected into a response.
type MCPProjectIdentity struct {
	ID   string `json:"id"`   // 128-bit random hex, assigned on registration
	Root string `json:"root"` // server-only canonical registered root
}

// projectIDBytes is the entropy in a project ID: 128 bits, hex encoded, so
// one project's ID cannot be guessed from another's.
const projectIDBytes = 16

// newProjectID mints a fresh project ID.
func newProjectID() (string, error) {
	var b [projectIDBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("bridge: generate project id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// ProjectSettings are a project's defaults for new agents and runs, and
// its intake wiring (v9).
// ProjectSettings is contract.ProjectSettings: one project's settings document.
type ProjectSettings = contract.ProjectSettings

// ProjectIntake is contract.ProjectIntake: the project's default intake.
type ProjectIntake = contract.ProjectIntake

// GateOverride is contract.GateOverride; see that declaration.
type GateOverride = contract.GateOverride

// Repo is a registered repository. Registration is what grants push
// capability: an agent spawned against a raw URL is read-only, and only
// a registered repo carries a credential reference.
type Repo struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Branch  string `json:"branch,omitempty"`
	CredRef string `json:"credRef,omitempty"`
	OwnerID string `json:"ownerId"`
	// Forge names the hosting provider's API family: "github" or
	// "gitea" (which also serves Forgejo). Declared at registration
	// rather than sniffed from the hostname, because a self-hosted
	// instance can live at any domain.
	Forge string `json:"forge,omitempty"`
	// APIBase overrides the API root for a self-hosted instance, e.g.
	// https://code.example.com/api/v1. Empty uses the family default.
	APIBase string `json:"apiBase,omitempty"`
	// Watch enables the issue poller for this repo. Off by default: a
	// watcher that starts itself is a surprise.
	Watch bool `json:"watch,omitempty"`
	// WatchLabel is the issue label that triggers a spawn.
	WatchLabel string `json:"watchLabel,omitempty"`
	// LastPolled and LastPollErr are surfaced in the UI. A silently
	// dead watcher is indistinguishable from "no matching issues".
	LastPolled  time.Time `json:"lastPolled,omitempty"`
	LastPollErr string    `json:"lastPollErr,omitempty"`
}

// PendingSpawn is an intake submission awaiting operator confirmation.
//
// It is persisted so a pending spawn survives a control-plane restart,
// for the same reason S1 made spawn records durable: the operator's
// decision is the slow part, and losing the queue to a restart discards
// work someone is waiting on.
type PendingSpawn struct {
	ID       string `json:"id"`
	Origin   string `json:"origin"`
	ClientID string `json:"clientId,omitempty"`
	// Title is what the operator reads when deciding. It comes from the
	// calling agent and is therefore untrusted: render as text, never
	// as markup.
	Title  string `json:"title"`
	RepoID string `json:"repoId"`
	Ref    string `json:"ref,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	// Plan is markdown in the format pipeline.ParsePlan reads. It is
	// stored here so the operator can read what they are approving.
	Plan      string    `json:"plan,omitempty"`
	Mode      string    `json:"mode,omitempty"`
	Auto      *bool     `json:"auto,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	// IssueNumber and IssueURL are set when the submission came from an
	// issue, so the exit path can link the pull request back to it.
	IssueNumber int    `json:"issueNumber,omitempty"`
	IssueURL    string `json:"issueUrl,omitempty"`
	// Project is the project root a coordinated assignment targets (agent-first
	// Studio, task 6). It is set only by a coordinator dispatch, which names a
	// registered project rather than a repository; a submission from every other
	// adapter leaves it empty and spawns from RepoID as it always has.
	Project string `json:"project,omitempty"`
	// TaskID and AssignmentID are the coordinated assignment this submission is
	// waiting on. They are the assignment's dispatch identity, carried from the
	// intent through this confirmation to the worker it starts, so approving one
	// submission can never start a worker another assignment owns.
	TaskID       string `json:"taskId,omitempty"`
	AssignmentID string `json:"assignmentId,omitempty"`
}

// SpawnRequestClaim is one caller-generated first-send request ID and the
// agent it resolved to (agent-first Studio, task 3).
//
// It is the durable half of exactly-once delivery: the claim is written
// before the spawn has any side effect, so a request that is retried — after
// a lost answer, a second press, or a daemon restart — finds the claim and
// resolves to the agent it names instead of starting another. The agent ID is
// chosen at claim time and handed to the spawn, so at most one agent can ever
// carry a request ID, even when the first attempt died mid-spawn.
type SpawnRequestClaim struct {
	// RequestID is the caller's own key, unique within Scope.
	RequestID string `json:"requestId"`
	// Scope is the caller/project the request was made in: the origin and the
	// project root. The same key in another project is another request.
	Scope string `json:"scope"`
	// Digest is the canonical digest of the payload the request named. A
	// claim replayed with a different digest is a conflict, not a retry.
	Digest string `json:"digest"`
	// AgentID is the agent this request started, chosen when the claim was
	// taken and persisted with the spawn.
	AgentID string `json:"agentId"`
	// Prompted records that the request's first prompt was dispatched to the
	// agent. A replay whose claim is not prompted yet dispatches it, so a
	// daemon that died between the spawn and the first turn does not lose the
	// message; one that is prompted never sends it again.
	Prompted  bool      `json:"prompted,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// spawnClaimKey is one claim's collection key: the scope and the request ID.
// The scope already names the project, so two projects cannot collide.
func spawnClaimKey(scope, requestID string) string { return scope + "\x00" + requestID }

// spawnRequestScope is the caller/project a request ID is scoped to. The
// caller is its origin (the UI acting directly, or a client of another kind),
// and the project is the canonical root the spawn runs in.
func spawnRequestScope(origin, projectRoot string) string {
	if origin == "" {
		origin = OriginUI
	}
	return origin + "\x00" + filepath.Clean(projectRoot)
}

type workspaceFile struct {
	Version         int              `json:"version"`
	Projects        []string         `json:"projects"`
	Repos           []Repo           `json:"repos,omitempty"`
	Agents          []Agent          `json:"agents"`
	Clients         []MCPClient      `json:"clients,omitempty"`
	Pending         []PendingSpawn   `json:"pending,omitempty"`
	SubmittedIssues map[string][]int `json:"submittedIssues,omitempty"`
	// SpawnClaims are the first-send request IDs that already resolved to an
	// agent. They are optional: a file written before they existed loads with
	// none, so a v12 file simply has no claims and no format change is owed.
	SpawnClaims []SpawnRequestClaim `json:"spawnClaims,omitempty"`
	// MarshalFactoryDefaults are the built-in factory access defaults, one
	// record per project, keyed by project ID. They sit beside the clients
	// because they are the same kind of record — an actor's authority — held
	// for an actor that has no client record at all. Both collections are
	// optional: a file written before they existed loads with none, and one
	// saved with none keeps its bytes.
	MarshalFactoryDefaults []marshalFactoryAccessRecord `json:"marshalFactoryDefaults,omitempty"`
	// MarshalFactoryOverrides are the per-session overrides, one per project
	// and session, with the worker a worker override names.
	MarshalFactoryOverrides []marshalFactoryAccessRecord `json:"marshalFactoryOverrides,omitempty"`
	// MarshalFactorySelections are the projects a person picked for a
	// session whose enrollment was ambiguous, one record per session. They
	// are a user's answer to a question the server refused to guess, so they
	// sit with the access records rather than with anything a request names.
	MarshalFactorySelections []marshalFactorySelectionRecord `json:"marshalFactorySelections,omitempty"`
	// Reviews maps an agent ID to its review comments (v7).
	Reviews map[string][]ReviewComment `json:"reviews,omitempty"`
	// WatchRules maps a Studio watch id to the action the bridge takes when
	// it fires (v8). The engine knows nothing of these rules.
	WatchRules map[string]WatchRule `json:"watchRules,omitempty"`
	// Credentials are git credential descriptions (v10). They hold refs
	// and variable names, never values.
	Credentials []Credential `json:"credentials,omitempty"`
	// ProjectSettings maps a project root to its settings (v9).
	ProjectSettings map[string]ProjectSettings `json:"projectSettings,omitempty"`
	// ProjectIdentities are the stable project IDs of the registered
	// projects. They are server-only and optional: an older file has none,
	// and Load mints what its registrations are missing, so the file
	// format and workspaceVersion do not change.
	ProjectIdentities []MCPProjectIdentity `json:"projectIdentities,omitempty"`
	// Schedules run recipes on a cron (v11).
	Schedules []Schedule `json:"schedules,omitempty"`
	// Notifications configures outbound webhooks (v11).
	Notifications NotifyConfig `json:"notifications,omitzero"`
	// StatusLinks are revocable public status page tokens, stored hashed (v11).
	StatusLinks []StatusLink `json:"statusLinks,omitempty"`
	// Pulls are the pull requests the bridge opened and has not seen close
	// (v12). They are optional: a file written before they existed loads with
	// none, so a v11 file simply has no records.
	Pulls []contract.PullRecord `json:"pulls,omitempty"`
	// PullAsks are the fix outcomes the Inbox still asks about, one per pull
	// request, keyed by the record's id (v12).
	PullAsks []contract.PullAsk `json:"pullAsks,omitempty"`
	// Pinned are the project roots the local daemon keeps running (P4 D9).
	Pinned []string `json:"pinned,omitempty"`
}

// WatchRule is what the bridge does when a Studio watch fires.
type WatchRule struct {
	// Reroute rebinds a role to a preset on the active routing profile.
	Reroute *RerouteRule `json:"reroute,omitempty"`
	// Notify and Resume echo the trip actions the watch was started with.
	// The engine's watch list does not report them, so the bridge keeps
	// them to show in GET /api/watches. A nil Notify means the engine's
	// default, which is to notify.
	Notify *bool `json:"notify,omitempty"`
	Resume bool  `json:"resume,omitempty"`
}

// RerouteRule names the role to rebind and the preset to bind it to.
type RerouteRule struct {
	Role   string `json:"role"`
	Preset string `json:"preset"`
}

type Workspace struct {
	path            string
	mu              sync.Mutex
	projects        []string
	repos           map[string]Repo
	agents          map[string]Agent
	clients         map[string]MCPClient
	pending         map[string]PendingSpawn
	submittedIssues map[string][]int
	reviews         map[string][]ReviewComment
	watchRules      map[string]WatchRule
	credentials     map[string]Credential
	projectSettings map[string]ProjectSettings
	// projectIdentities maps a canonical registered project root to its
	// stable ID. It is keyed by root because the registrations are, and
	// only a root the store already holds ever acquires one.
	projectIdentities map[string]MCPProjectIdentity
	// marshalDefaults maps a project ID to the built-in factory access a
	// verified session inherits. It is separate from the clients: a built-in
	// session holds no client record and no factory grant.
	marshalDefaults map[string]MarshalFactoryAccess
	// marshalOverrides maps one session of one project to its override, keyed
	// by marshalOverrideKey. A worker override is a session override that also
	// names the worker, so it lives here too.
	marshalOverrides map[string]marshalFactoryAccessRecord
	// marshalSelections maps a session ID to the project a person chose for
	// it when its enrollment named more than one. It is keyed by session
	// because the ambiguity is: the same project is not ambiguous for the
	// next session that enrolls against it.
	marshalSelections map[string]marshalFactorySelectionRecord
	schedules         map[string]Schedule
	notifications     NotifyConfig
	statusLinks       map[string]StatusLink
	// pulls maps a record id (<repoId>#<number>) to the pull request the
	// bridge opened for it (v12).
	pulls map[string]contract.PullRecord
	// pullAsks maps a pull record id to the ask the Inbox still shows about
	// its fix (v12). One ask per pull request: a new dispatch replaces it.
	pullAsks map[string]contract.PullAsk
	// spawnClaims maps a caller's first-send request ID, within its scope, to
	// the agent it resolved to (agent-first Studio, task 3). It is keyed by
	// spawnClaimKey because a request ID is only unique within one caller and
	// project.
	spawnClaims map[string]SpawnRequestClaim
	pinned      []string
}

func NewWorkspace(path string) *Workspace {
	return &Workspace{
		path:              path,
		repos:             make(map[string]Repo),
		agents:            make(map[string]Agent),
		clients:           make(map[string]MCPClient),
		pending:           make(map[string]PendingSpawn),
		submittedIssues:   make(map[string][]int),
		reviews:           make(map[string][]ReviewComment),
		watchRules:        make(map[string]WatchRule),
		credentials:       make(map[string]Credential),
		projectSettings:   make(map[string]ProjectSettings),
		projectIdentities: make(map[string]MCPProjectIdentity),
		marshalDefaults:   make(map[string]MarshalFactoryAccess),
		marshalOverrides:  make(map[string]marshalFactoryAccessRecord),
		marshalSelections: make(map[string]marshalFactorySelectionRecord),
		schedules:         make(map[string]Schedule),
		statusLinks:       make(map[string]StatusLink),
		pulls:             make(map[string]contract.PullRecord),
		pullAsks:          make(map[string]contract.PullAsk),
		spawnClaims:       make(map[string]SpawnRequestClaim),
	}
}

func (w *Workspace) Load() (string, error) {
	data, err := os.ReadFile(w.path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read workspace: %w", err)
	}
	var f workspaceFile
	if json.Unmarshal(data, &f) != nil || f.Version < 1 || f.Version > workspaceVersion {
		backup := w.path + ".corrupt." + time.Now().UTC().Format("20060102T150405Z")
		if err := os.Rename(w.path, backup); err != nil {
			return "", fmt.Errorf("quarantine corrupt workspace: %w", err)
		}
		return backup, nil
	}
	if f.Version < workspaceVersion {
		migrateWorkspace(&f)
	}
	normalizeStoredInteraction(&f)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.projects = append([]string(nil), f.Projects...)
	w.repos = make(map[string]Repo, len(f.Repos))
	for _, r := range f.Repos {
		w.repos[r.ID] = r
	}
	w.agents = make(map[string]Agent, len(f.Agents))
	for _, a := range f.Agents {
		w.agents[a.ID] = a
	}
	w.clients = make(map[string]MCPClient, len(f.Clients))
	for _, c := range f.Clients {
		w.clients[c.ID] = c
	}
	w.marshalDefaults = make(map[string]MarshalFactoryAccess, len(f.MarshalFactoryDefaults))
	for _, r := range f.MarshalFactoryDefaults {
		if r.ProjectID == "" {
			continue
		}
		w.marshalDefaults[r.ProjectID] = r.Access.clone()
	}
	w.marshalOverrides = make(map[string]marshalFactoryAccessRecord, len(f.MarshalFactoryOverrides))
	for _, r := range f.MarshalFactoryOverrides {
		if r.ProjectID == "" || r.SessionID == "" {
			continue
		}
		w.marshalOverrides[marshalOverrideKey(r.ProjectID, r.SessionID, r.WorkerID)] = marshalFactoryAccessRecord{
			ProjectID: r.ProjectID, SessionID: r.SessionID, WorkerID: r.WorkerID, Access: r.Access.clone(),
		}
	}
	w.marshalSelections = make(map[string]marshalFactorySelectionRecord, len(f.MarshalFactorySelections))
	for _, r := range f.MarshalFactorySelections {
		if r.SessionID == "" || r.ProjectID == "" {
			continue
		}
		w.marshalSelections[r.SessionID] = r
	}
	w.pending = make(map[string]PendingSpawn, len(f.Pending))
	for _, p := range f.Pending {
		w.pending[p.ID] = p
	}
	w.submittedIssues = make(map[string][]int, len(f.SubmittedIssues))
	for id, nums := range f.SubmittedIssues {
		w.submittedIssues[id] = append([]int(nil), nums...)
	}
	w.reviews = make(map[string][]ReviewComment, len(f.Reviews))
	for id, cs := range f.Reviews {
		w.reviews[id] = append([]ReviewComment(nil), cs...)
	}
	w.watchRules = make(map[string]WatchRule, len(f.WatchRules))
	for id, r := range f.WatchRules {
		w.watchRules[id] = r
	}
	w.credentials = make(map[string]Credential, len(f.Credentials))
	for _, c := range f.Credentials {
		w.credentials[c.ID] = c
	}
	w.projectSettings = make(map[string]ProjectSettings, len(f.ProjectSettings))
	for root, ps := range f.ProjectSettings {
		w.projectSettings[root] = ps
	}
	w.schedules = make(map[string]Schedule, len(f.Schedules))
	for _, sc := range f.Schedules {
		w.schedules[sc.ID] = sc
	}
	w.notifications = f.Notifications.clone()
	w.statusLinks = make(map[string]StatusLink, len(f.StatusLinks))
	for _, l := range f.StatusLinks {
		w.statusLinks[l.ID] = l
	}
	w.pulls = make(map[string]contract.PullRecord, len(f.Pulls))
	for _, p := range f.Pulls {
		w.pulls[p.ID] = p
	}
	w.pullAsks = make(map[string]contract.PullAsk, len(f.PullAsks))
	for _, a := range f.PullAsks {
		w.pullAsks[a.Pull] = a
	}
	// A claim without its request ID, scope or agent says nothing, so it is
	// skipped rather than kept as a key that could never resolve. An older
	// file has no claims at all and loads with none.
	w.spawnClaims = make(map[string]SpawnRequestClaim, len(f.SpawnClaims))
	for _, c := range f.SpawnClaims {
		if c.RequestID == "" || c.Scope == "" || c.AgentID == "" {
			continue
		}
		w.spawnClaims[spawnClaimKey(c.Scope, c.RequestID)] = c
	}
	w.pinned = make([]string, 0, len(f.Pinned))
	seenPins := make(map[string]bool, len(f.Pinned))
	for _, p := range f.Pinned {
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if seenPins[p] {
			continue
		}
		seenPins[p] = true
		w.pinned = append(w.pinned, p)
	}
	w.projectIdentities = make(map[string]MCPProjectIdentity, len(f.ProjectIdentities))
	for _, id := range f.ProjectIdentities {
		if id.ID == "" || id.Root == "" {
			continue
		}
		w.projectIdentities[id.Root] = id
	}
	// Every registered project has an ID, and no unregistered one does. A
	// first run on an older file mints what is missing and writes it before
	// Load returns: an ID that never reached the disk is not an ID, because
	// a grant saved against it would be lost at the next restart.
	loaded := w.projectIdentities
	changed, err := w.syncProjectIdentitiesLocked()
	if err != nil {
		return "", err
	}
	if changed {
		if err := w.save(); err != nil {
			w.projectIdentities = loaded
			return "", fmt.Errorf("persist project identities: %w", err)
		}
	}
	return "", nil
}

// syncProjectIdentitiesLocked keys the identity collection to the
// registrations: a registered project with no identity gets one, and an
// identity whose project is no longer registered is dropped. It reports
// whether the collection changed, and installs the new collection before
// returning, so a caller that cannot persist it restores what it had.
func (w *Workspace) syncProjectIdentitiesLocked() (bool, error) {
	next := make(map[string]MCPProjectIdentity, len(w.projects))
	changed := false
	for _, root := range w.projects {
		if id, ok := w.projectIdentities[root]; ok && id.ID != "" && id.Root == root {
			next[root] = id
			continue
		}
		fresh, err := newProjectID()
		if err != nil {
			return false, err
		}
		next[root] = MCPProjectIdentity{ID: fresh, Root: root}
		changed = true
	}
	if !changed && len(next) != len(w.projectIdentities) {
		changed = true
	}
	w.projectIdentities = next
	return changed, nil
}

// migrateWorkspace upgrades an older workspace file in place. A v1
// record predates owners, origins, and runtime profiles: it was created
// by the operator through the UI against a local checkout, so those are
// the defaults it receives.
func migrateWorkspace(f *workspaceFile) {
	for i := range f.Agents {
		a := &f.Agents[i]
		if a.OwnerID == "" {
			a.OwnerID = DefaultOwnerID
		}
		if a.Origin == "" {
			a.Origin = OriginUI
		}
		if a.Profile.Image == "" {
			a.Profile = DefaultRuntimeProfile()
		}
		if a.SourceKind == "" {
			a.SourceKind = "local"
			a.SourceRef = a.Project
		}
	}
	// v2 -> v3: promote each registered project to a repo entry. A v2
	// record predates the registry, so every project is promoted as a
	// plain local repo owned by the default operator.
	if len(f.Repos) == 0 {
		for _, p := range f.Projects {
			f.Repos = append(f.Repos, Repo{ID: p, URL: p, OwnerID: DefaultOwnerID})
		}
	}
	f.Version = workspaceVersion
}

// canonicalModeTuple canonicalizes one persisted interaction default. The
// retired five-mode spellings are translated through the same table as the
// engine's interaction.LegacyDefaults; web/ is stdlib-only, so the table is
// mirrored here rather than imported. Presence is preserved: a nil Auto means
// the source did not set it, which is not the same as an explicit false. An
// unknown spelling is an error, never Act — a stored value the bridge cannot
// read must be reported by the surface that reads it, not quietly promoted.
// The returned note describes a translation and is empty otherwise.
func canonicalModeTuple(mode string, auto *bool) (string, *bool, string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return "", auto, "", nil
	case "ask", "act":
		if strings.EqualFold(strings.TrimSpace(mode), "ask") && auto != nil && *auto {
			return "", nil, "", errors.New("mode ask cannot enable auto")
		}
		return strings.ToLower(strings.TrimSpace(mode)), auto, "", nil
	case "plan", "default":
		return "ask", auto, fmt.Sprintf("mode %q replaced with ask; planning remains a skill workflow", mode), nil
	case "edit":
		return "ask", auto, fmt.Sprintf("mode %q replaced with ask; approval for every file change was retired", mode), nil
	case "copilot":
		return "act", auto, fmt.Sprintf("mode %q replaced with act", mode), nil
	case "auto":
		next := true
		if auto != nil {
			next = *auto
		}
		return "act", &next, fmt.Sprintf("mode %q replaced with act + auto", mode), nil
	}
	return "", nil, "", fmt.Errorf("invalid interaction mode %q: want ask or act", mode)
}

// boolPtr returns a pointer to b, for the optional Auto defaults.
func boolPtr(b bool) *bool { return &b }

// autoAssessPlanChanges is the effective automatic-assessment preference: an
// absent setting means on, and only an explicit false turns it off.
func autoAssessPlanChanges(ps ProjectSettings) bool {
	return ps.AutoAssessPlanChanges == nil || *ps.AutoAssessPlanChanges
}

// normalizeStoredInteraction canonicalizes the starting defaults the workspace
// persists on agents, pending spawns and project settings. It runs on every
// load, not only a version upgrade, because ask/act is a translation of stored
// spellings rather than a new file format; workspaceVersion therefore does not
// change. The migration is memory-only — Load never rewrites the file it read,
// and a later explicit save writes the canonical pair. None of these fields is
// a live grant: System is not persisted and no revision is invented, so nothing
// here can restore access to a session. A spelling the bridge does not know is
// left exactly as stored, for its reader to report.
func normalizeStoredInteraction(f *workspaceFile) {
	for i := range f.Agents {
		a := &f.Agents[i]
		if mode, auto, _, err := canonicalModeTuple(a.Mode, a.Auto); err == nil {
			a.Mode, a.Auto = mode, auto
		}
	}
	for i := range f.Pending {
		p := &f.Pending[i]
		if mode, auto, _, err := canonicalModeTuple(p.Mode, p.Auto); err == nil {
			p.Mode, p.Auto = mode, auto
		}
	}
	for root, ps := range f.ProjectSettings {
		if mode, auto, _, err := canonicalModeTuple(ps.Mode, ps.Auto); err == nil {
			ps.Mode, ps.Auto = mode, auto
			f.ProjectSettings[root] = ps
		}
	}
}

func (w *Workspace) save() error {
	f := workspaceFile{Version: workspaceVersion, Projects: w.projects, Repos: make([]Repo, 0, len(w.repos)), Agents: make([]Agent, 0, len(w.agents))}
	f.Pinned = append([]string(nil), w.pinned...)
	sort.Strings(f.Pinned)
	for _, r := range w.repos {
		f.Repos = append(f.Repos, r)
	}
	sort.Slice(f.Repos, func(i, j int) bool { return f.Repos[i].ID < f.Repos[j].ID })
	for _, a := range w.agents {
		f.Agents = append(f.Agents, a)
	}
	sort.Slice(f.Agents, func(i, j int) bool { return f.Agents[i].ID < f.Agents[j].ID })
	for _, c := range w.clients {
		f.Clients = append(f.Clients, c)
	}
	sort.Slice(f.Clients, func(i, j int) bool { return f.Clients[i].ID < f.Clients[j].ID })
	// The built-in access records are sorted by project and session, so an
	// unchanged collection writes the same bytes on every save.
	for projectID, access := range w.marshalDefaults {
		f.MarshalFactoryDefaults = append(f.MarshalFactoryDefaults,
			marshalFactoryAccessRecord{ProjectID: projectID, Access: access.clone()})
	}
	sort.Slice(f.MarshalFactoryDefaults, func(i, j int) bool {
		return f.MarshalFactoryDefaults[i].ProjectID < f.MarshalFactoryDefaults[j].ProjectID
	})
	for _, r := range w.marshalOverrides {
		f.MarshalFactoryOverrides = append(f.MarshalFactoryOverrides, marshalFactoryAccessRecord{
			ProjectID: r.ProjectID, SessionID: r.SessionID, WorkerID: r.WorkerID, Access: r.Access.clone(),
		})
	}
	sort.Slice(f.MarshalFactoryOverrides, func(i, j int) bool {
		if f.MarshalFactoryOverrides[i].ProjectID != f.MarshalFactoryOverrides[j].ProjectID {
			return f.MarshalFactoryOverrides[i].ProjectID < f.MarshalFactoryOverrides[j].ProjectID
		}
		if f.MarshalFactoryOverrides[i].SessionID != f.MarshalFactoryOverrides[j].SessionID {
			return f.MarshalFactoryOverrides[i].SessionID < f.MarshalFactoryOverrides[j].SessionID
		}
		// One session may hold its own record and a worker's, so the worker is
		// part of the order: an unchanged collection must still write the same
		// bytes on every save.
		return f.MarshalFactoryOverrides[i].WorkerID < f.MarshalFactoryOverrides[j].WorkerID
	})
	for _, r := range w.marshalSelections {
		f.MarshalFactorySelections = append(f.MarshalFactorySelections, r)
	}
	sort.Slice(f.MarshalFactorySelections, func(i, j int) bool {
		return f.MarshalFactorySelections[i].SessionID < f.MarshalFactorySelections[j].SessionID
	})
	for _, p := range w.pending {
		f.Pending = append(f.Pending, p)
	}
	sort.Slice(f.Pending, func(i, j int) bool { return f.Pending[i].ID < f.Pending[j].ID })
	if len(w.submittedIssues) > 0 {
		f.SubmittedIssues = make(map[string][]int, len(w.submittedIssues))
		for id, nums := range w.submittedIssues {
			f.SubmittedIssues[id] = append([]int(nil), nums...)
		}
	}
	if len(w.reviews) > 0 {
		f.Reviews = make(map[string][]ReviewComment, len(w.reviews))
		for id, cs := range w.reviews {
			f.Reviews[id] = append([]ReviewComment(nil), cs...)
		}
	}
	if len(w.watchRules) > 0 {
		f.WatchRules = make(map[string]WatchRule, len(w.watchRules))
		for id, r := range w.watchRules {
			f.WatchRules[id] = r
		}
	}
	for _, c := range w.credentials {
		f.Credentials = append(f.Credentials, c)
	}
	sort.Slice(f.Credentials, func(i, j int) bool { return f.Credentials[i].ID < f.Credentials[j].ID })
	if len(w.projectSettings) > 0 {
		f.ProjectSettings = make(map[string]ProjectSettings, len(w.projectSettings))
		for root, ps := range w.projectSettings {
			f.ProjectSettings[root] = ps
		}
	}
	// Project identities are server-only and sorted by root, so an
	// unchanged collection writes the same bytes on every save.
	for _, id := range w.projectIdentities {
		f.ProjectIdentities = append(f.ProjectIdentities, id)
	}
	sort.Slice(f.ProjectIdentities, func(i, j int) bool { return f.ProjectIdentities[i].Root < f.ProjectIdentities[j].Root })
	for _, sc := range w.schedules {
		f.Schedules = append(f.Schedules, sc)
	}
	sort.Slice(f.Schedules, func(i, j int) bool { return f.Schedules[i].ID < f.Schedules[j].ID })
	f.Notifications = w.notifications.clone()
	for _, l := range w.statusLinks {
		f.StatusLinks = append(f.StatusLinks, l)
	}
	sort.Slice(f.StatusLinks, func(i, j int) bool { return f.StatusLinks[i].ID < f.StatusLinks[j].ID })
	for _, p := range w.pulls {
		f.Pulls = append(f.Pulls, p)
	}
	sort.Slice(f.Pulls, func(i, j int) bool { return f.Pulls[i].ID < f.Pulls[j].ID })
	for _, a := range w.pullAsks {
		f.PullAsks = append(f.PullAsks, a)
	}
	sort.Slice(f.PullAsks, func(i, j int) bool { return f.PullAsks[i].Pull < f.PullAsks[j].Pull })
	// Claims are sorted by scope and request ID, so an unchanged collection
	// writes the same bytes on every save.
	for _, c := range w.spawnClaims {
		f.SpawnClaims = append(f.SpawnClaims, c)
	}
	sort.Slice(f.SpawnClaims, func(i, j int) bool {
		if f.SpawnClaims[i].Scope != f.SpawnClaims[j].Scope {
			return f.SpawnClaims[i].Scope < f.SpawnClaims[j].Scope
		}
		return f.SpawnClaims[i].RequestID < f.SpawnClaims[j].RequestID
	})
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workspace: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(w.path), ".fleet-*.json")
	if err != nil {
		return fmt.Errorf("create temp workspace: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp workspace: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp workspace: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp workspace: %w", err)
	}
	if err := os.Rename(name, w.path); err != nil {
		return fmt.Errorf("rename workspace: %w", err)
	}
	dir, err := os.Open(filepath.Dir(w.path))
	if err != nil {
		return fmt.Errorf("open workspace dir: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync workspace dir: %w", err)
	}
	return nil
}

func (w *Workspace) Projects() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.projects...)
}

func (w *Workspace) AddProject(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("project root %q is not absolute", root)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	registered := false
	for _, p := range w.projects {
		// Registering a project again keeps the ID it already has: the
		// grant an operator wrote against it must survive a re-add.
		if p == root {
			registered = true
			break
		}
	}
	if !registered {
		w.projects = append(w.projects, root)
	}
	if _, ok := w.projectIdentities[root]; ok {
		return w.save()
	}
	// An identity whose save failed earlier is minted here rather than
	// re-exposed: nothing could have been granted against an ID that never
	// became observable, and the registration it belongs to may already be
	// in place.
	fresh, err := newProjectID()
	if err != nil {
		if !registered {
			w.projects = w.projects[: len(w.projects)-1 : len(w.projects)-1]
		}
		return err
	}
	w.projectIdentities[root] = MCPProjectIdentity{ID: fresh, Root: root}
	if err := w.save(); err != nil {
		// The registration stays, as it always has, but no ID is
		// exposed for it: one that did not reach the disk would point
		// at a project the next start could not resolve.
		delete(w.projectIdentities, root)
		return err
	}
	return nil
}

func (w *Workspace) RemoveProject(root string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, a := range w.agents {
		if a.Project == root && a.FactoryItemID != "" {
			return fmt.Errorf("%w: factory worker %s must be removed through its work item before project deletion", ErrFactoryConflict, a.ID)
		}
	}
	kept := w.projects[:0:0]
	for _, p := range w.projects {
		if p != root {
			kept = append(kept, p)
		}
	}
	w.projects = kept
	for id, a := range w.agents {
		if a.Project == root {
			delete(w.agents, id)
			delete(w.reviews, id)
		}
	}
	delete(w.projectSettings, root)
	// The identity goes with the registration, and every grant naming it
	// goes in the same save: an ID that no longer resolves must not keep
	// authority alive, and re-registering the project mints a fresh one.
	if identity, ok := w.projectIdentities[root]; ok {
		delete(w.projectIdentities, root)
		w.revokeProjectGrantsLocked(identity.ID)
	}
	return w.save()
}

// revokeProjectGrantsLocked drops every record that names projectID: the client
// grants, the built-in access a verified session inherits or overrides, and the
// selections that resolved an ambiguous enrollment onto it. It runs inside the
// removal's own save, so no authority — and no policy row for an ID that no
// longer resolves — outlives the project it describes.
func (w *Workspace) revokeProjectGrantsLocked(projectID string) {
	if projectID == "" {
		return
	}
	for id, c := range w.clients {
		if len(c.FactoryGrants) == 0 {
			continue
		}
		next := c.clone()
		kept := make([]MCPFactoryProjectGrant, 0, len(next.FactoryGrants))
		for _, g := range next.FactoryGrants {
			if g.ProjectID != projectID {
				kept = append(kept, g)
			}
		}
		if len(kept) == len(next.FactoryGrants) {
			continue
		}
		next.FactoryGrants = kept
		w.clients[id] = next
	}
	delete(w.marshalDefaults, projectID)
	for key, r := range w.marshalOverrides {
		if r.ProjectID == projectID {
			delete(w.marshalOverrides, key)
		}
	}
	for sessionID, r := range w.marshalSelections {
		if r.ProjectID == projectID {
			delete(w.marshalSelections, sessionID)
		}
	}
}

// Pinned returns the project roots the local daemon keeps running, sorted.
// The result is never nil.
func (w *Workspace) Pinned() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.pinned))
	copy(out, w.pinned)
	sort.Strings(out)
	return out
}

// IsPinned reports whether root is pinned.
func (w *Workspace) IsPinned(root string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, p := range w.pinned {
		if p == root {
			return true
		}
	}
	return false
}

// SetPinned adds or removes root and saves; a change that changes nothing
// saves nothing.
func (w *Workspace) SetPinned(root string, pinned bool) error {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return fmt.Errorf("pin root %q is not absolute", root)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	idx := -1
	for i, p := range w.pinned {
		if p == root {
			idx = i
			break
		}
	}
	if pinned {
		if idx >= 0 {
			return nil
		}
		w.pinned = append(w.pinned, root)
		return w.save()
	}
	if idx < 0 {
		return nil
	}
	w.pinned = append(w.pinned[:idx:idx], w.pinned[idx+1:]...)
	return w.save()
}

// PutProjectSettings stores a project's settings.
func (w *Workspace) PutProjectSettings(root string, ps ProjectSettings) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.projectSettings[root] = ps
	return w.save()
}

// projectSettingsETag is the opaque version token for a project's settings: a
// quoted, lowercase SHA-256 of their JSON encoding. It is what a settings GET
// returns and what a conditional PUT compares, so a client that writes from a
// stale read is refused rather than silently overwriting someone else's edit.
// The encoding is the store's own view of the value, so two equal settings
// always carry the same tag.
func projectSettingsETag(ps ProjectSettings) (string, error) {
	data, err := json.Marshal(ps)
	if err != nil {
		return "", fmt.Errorf("encode project settings: %w", err)
	}
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:]) + `"`, nil
}

// ProjectSettingsETag returns the ETag of the settings the store holds for
// root right now. A conditional write reads it before it looks at the body, so
// a caller whose view is stale is told exactly that instead of being sent away
// with a validation error about a version it can no longer see. The tag is
// computed under the lock that guards the map, so it describes one value and
// not a torn read of two.
func (w *Workspace) ProjectSettingsETag(root string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return projectSettingsETag(w.projectSettings[root])
}

// PutProjectSettingsIfMatch stores a project's settings only when the stored
// value still carries expected as its ETag. It reports whether it wrote, the
// value it replaced, and any persistence error. The comparison, the map update
// and the save all happen under one lock, so of two writers naming the same
// version exactly one wins. A failed save restores the exact prior value,
// including the case where no entry existed before. An empty expected is
// refused: a caller that names no version wants PutProjectSettings.
func (w *Workspace) PutProjectSettingsIfMatch(root string, ps ProjectSettings, expected string) (bool, ProjectSettings, error) {
	if expected == "" {
		return false, ProjectSettings{}, errors.New("bridge: conditional settings write needs an If-Match value")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	prev, had := w.projectSettings[root]
	current, err := projectSettingsETag(prev)
	if err != nil {
		return false, prev, err
	}
	if current != expected {
		return false, prev, nil
	}
	w.projectSettings[root] = ps
	if err := w.save(); err != nil {
		if had {
			w.projectSettings[root] = prev
		} else {
			delete(w.projectSettings, root)
		}
		return false, prev, err
	}
	return true, prev, nil
}

// ProjectSettingsFor returns a project's settings, zero when none are set.
func (w *Workspace) ProjectSettingsFor(root string) ProjectSettings {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.projectSettings[root]
}

func (w *Workspace) PutAgent(a Agent) error {
	if a.ID == "" {
		return errors.New("agent id is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.agents[a.ID] = a
	return w.save()
}
func (w *Workspace) Agent(id string) (Agent, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	a, ok := w.agents[id]
	return a, ok
}
func (w *Workspace) Agents() []Agent {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Agent, 0, len(w.agents))
	for _, a := range w.agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (w *Workspace) RemoveAgent(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.agents, id)
	delete(w.reviews, id)
	return w.save()
}
func (w *Workspace) MarkAllInterrupted() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, a := range w.agents {
		a.Interrupted = true
		w.agents[id] = a
	}
	return w.save()
}
func (w *Workspace) Repos() []Repo {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Repo, 0, len(w.repos))
	for _, r := range w.repos {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (w *Workspace) Repo(id string) (Repo, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.repos[id]
	return r, ok
}
func (w *Workspace) PutRepo(r Repo) error {
	if r.ID == "" {
		return errors.New("repo id is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.repos[r.ID] = r
	return w.save()
}

// RemoveRepo deletes a registered repo.
func (w *Workspace) RemoveRepo(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.repos[id]; !ok {
		return ErrUnknownRepo
	}
	delete(w.repos, id)
	return w.save()
}

// Credentials returns the persisted credential descriptions.
func (w *Workspace) Credentials() []Credential {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Credential, 0, len(w.credentials))
	for _, c := range w.credentials {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PutCredential saves a credential description.
func (w *Workspace) PutCredential(c Credential) error {
	if c.ID == "" {
		return errors.New("credential id is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.credentials[c.ID] = c
	return w.save()
}

// RemoveCredential deletes a credential description.
func (w *Workspace) RemoveCredential(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.credentials[id]; !ok {
		return ErrUnknownCredential
	}
	delete(w.credentials, id)
	return w.save()
}

// SubmittedIssues returns the issue numbers already submitted for a
// repo, so the poller does not resubmit work the operator has already
// seen.
func (w *Workspace) SubmittedIssues(repoID string) []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]int(nil), w.submittedIssues[repoID]...)
}

// MarkIssueSubmitted records that an issue was submitted for a repo.
// A polled issue that is edited upstream reappears in a since query,
// so the number record — not the cursor alone — is what stops a
// resubmission.
func (w *Workspace) MarkIssueSubmitted(repoID string, number int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, n := range w.submittedIssues[repoID] {
		if n == number {
			return nil // already recorded
		}
	}
	w.submittedIssues[repoID] = append(w.submittedIssues[repoID], number)
	return w.save()
}

func (w *Workspace) Clients() []MCPClient {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]MCPClient, 0, len(w.clients))
	for _, c := range w.clients {
		out = append(out, c.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (w *Workspace) PutClient(c MCPClient) error {
	if c.ID == "" {
		return errors.New("client id is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.clients[c.ID] = c.clone()
	return w.save()
}

func (w *Workspace) DeleteClient(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.clients, id)
	// A delegation designation names one client, so it cannot outlive it:
	// the project settings that named the deleted client are cleared in the
	// same save rather than left pointing at nothing.
	for root, ps := range w.projectSettings {
		if ps.DesignatedMCPClientID != id {
			continue
		}
		ps.DesignatedMCPClientID = ""
		ps.PlannerDelegationEnabled = false
		w.projectSettings[root] = ps
	}
	return w.save()
}

func (w *Workspace) Client(id string) (MCPClient, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c, ok := w.clients[id]
	if !ok {
		return MCPClient{}, false
	}
	return c.clone(), true
}

// ProjectID returns the stable ID of a registered project. An unregistered
// root has none: an ID is minted for a registration and dies with it.
func (w *Workspace) ProjectID(root string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	id, ok := w.projectIdentities[root]
	if !ok || id.ID == "" {
		return "", false
	}
	return id.ID, true
}

// ProjectRootByID returns the registered root a project ID belongs to. Only
// an ID this store minted resolves: a browser-supplied path is never a key.
func (w *Workspace) ProjectRootByID(id string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if id == "" {
		return "", false
	}
	for root, identity := range w.projectIdentities {
		if identity.ID == id {
			return root, true
		}
	}
	return "", false
}

// factoryGrantVersion is the opaque version of one grant: a lowercase hex
// SHA-256 of its JSON encoding. The grant is all scalars, so the encoding is
// deterministic and two equal grants always carry the same version.
//
// An empty authority means the same refusal as the explicit "none" the write
// path stores, so both encode alike: otherwise the zero grant a read reports
// would move to a new version the moment it was saved unchanged.
func factoryGrantVersion(g MCPFactoryProjectGrant) string {
	if g.ApprovalAuthority == "" {
		g.ApprovalAuthority = factoryAuthorityNone
	}
	data, _ := json.Marshal(g)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// factoryGrantState builds the pair's reported state from a grant.
func factoryGrantState(clientID, projectID string, g MCPFactoryProjectGrant) FactoryGrantState {
	return FactoryGrantState{ClientID: clientID, ProjectID: projectID, Grant: g, Version: factoryGrantVersion(g)}
}

// marshalFactoryAccessRecord is one stored built-in access value: a project's
// defaults (SessionID empty) or one session's override. A worker override is a
// session override that also names the worker, so no worker has a record of
// its own and a role is never stored as authority.
type marshalFactoryAccessRecord struct {
	ProjectID string               `json:"projectId"`
	SessionID string               `json:"sessionId,omitempty"`
	WorkerID  string               `json:"workerId,omitempty"`
	Access    MarshalFactoryAccess `json:"access"`
}

// marshalFactoryAccessState is one stored access value and its opaque version:
// the pair a versioned write is made against. An absent value reports the zero
// value and the stable version of it, so an editor's first write can be
// conditional on what it was shown — exactly as an absent grant does.
type marshalFactoryAccessState struct {
	ProjectID string
	SessionID string
	WorkerID  string
	Access    MarshalFactoryAccess
	Version   string
}

// marshalAccessVersion is the opaque version of one access value: a lowercase
// hex SHA-256 of its JSON encoding. Every field is a pointer to a scalar, so
// the encoding is deterministic and two equal values always carry the same
// version. An unset field is omitted and an explicit false is written, so a
// reset to unset moves the version rather than hiding as an equal value.
func marshalAccessVersion(a MarshalFactoryAccess) string {
	data, _ := json.Marshal(a)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// marshalAccessState builds one reported access state from a stored value.
func marshalAccessState(projectID, sessionID, workerID string, a MarshalFactoryAccess) marshalFactoryAccessState {
	return marshalFactoryAccessState{
		ProjectID: projectID, SessionID: sessionID, WorkerID: workerID,
		Access: a.clone(), Version: marshalAccessVersion(a),
	}
}

// marshalOverrideKey is one override record's collection key: the project, the
// session and — when the record names one — the worker. The worker is part of
// the key because a worker's access is the worker's: with one slot per session,
// a worker editor that had been shown its session's record would replace that
// record, and its session and every sibling worker would silently fall back to
// the project default. An empty worker is the session's own record, which is
// what a worker inherits until it is given one of its own.
func marshalOverrideKey(projectID, sessionID, workerID string) string {
	return projectID + "\x00" + sessionID + "\x00" + workerID
}

// marshalFactorySelectionRecord is one person's answer to an ambiguous
// enrollment: the project the session called SessionID acts on. An empty
// ProjectID is the absence of an answer, which is what a reset stores.
type marshalFactorySelectionRecord struct {
	SessionID string `json:"sessionId"`
	ProjectID string `json:"projectId,omitempty"`
}

// marshalFactorySelectionState is one stored selection and its opaque version:
// the pair a conditional write is made against. An absent selection reports the
// zero value and the stable version of it, so an editor's first write can be
// conditional on what it was shown.
type marshalFactorySelectionState struct {
	SessionID string
	ProjectID string
	Version   string
}

// marshalSelectionVersion is the opaque version of one selection: a lowercase
// hex SHA-256 of the JSON encoding of the project chosen. Clearing a selection
// moves the version back to the absent value's, which is the same version a
// session that was never ambiguous has.
func marshalSelectionVersion(projectID string) string {
	data, _ := json.Marshal(marshalFactorySelectionRecord{ProjectID: projectID})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// marshalSelectionState builds one reported selection from a stored value.
func marshalSelectionState(sessionID, projectID string) marshalFactorySelectionState {
	return marshalFactorySelectionState{
		SessionID: sessionID, ProjectID: projectID, Version: marshalSelectionVersion(projectID),
	}
}

// marshalSelection returns the project a person chose for one session and the
// version the choice carries. The second result is false when there is no
// answer stored, which an absent selection reports as the zero state: a
// selection nobody made is not a selection of the empty project.
func (w *Workspace) marshalSelection(sessionID string) (marshalFactorySelectionState, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sessionID == "" {
		return marshalFactorySelectionState{}, false
	}
	r, ok := w.marshalSelections[sessionID]
	if !ok || r.ProjectID == "" {
		return marshalFactorySelectionState{}, false
	}
	return marshalSelectionState(sessionID, r.ProjectID), true
}

// putMarshalSelectionIfMatch stores one session's selected project only when
// the stored selection still carries expected as its version, under the same
// rule as the access writes: one lock, a conditional write, and the exact prior
// record restored when the save fails. An empty projectID clears the selection
// — the session is ambiguous again, which is a reset rather than a choice.
func (w *Workspace) putMarshalSelectionIfMatch(sessionID, projectID, expected string) (bool, marshalFactorySelectionState, error) {
	if strings.TrimSpace(expected) == "" {
		return false, marshalFactorySelectionState{}, errors.New("bridge: conditional selection write needs a version")
	}
	if strings.TrimSpace(sessionID) == "" {
		return false, marshalFactorySelectionState{}, errors.New("bridge: a selection needs a session id")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.marshalSelections == nil {
		w.marshalSelections = make(map[string]marshalFactorySelectionRecord)
	}
	prev, had := w.marshalSelections[sessionID]
	current := marshalSelectionState(sessionID, prev.ProjectID)
	if current.Version != expected {
		return false, current, nil
	}
	if projectID == "" {
		delete(w.marshalSelections, sessionID)
	} else {
		w.marshalSelections[sessionID] = marshalFactorySelectionRecord{SessionID: sessionID, ProjectID: projectID}
	}
	if err := w.save(); err != nil {
		if had {
			w.marshalSelections[sessionID] = prev
		} else {
			delete(w.marshalSelections, sessionID)
		}
		return false, current, err
	}
	return true, marshalSelectionState(sessionID, projectID), nil
}

// FactoryGrant returns a copy of one client's grant for one project and its
// version. The second result is false when nothing is stored — no such
// client, or a client that names this project nowhere — which is the same
// answer as a grant that refuses everything: no authority.
func (w *Workspace) FactoryGrant(clientID, projectID string) (FactoryGrantState, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c, ok := w.clients[clientID]
	if !ok || projectID == "" {
		return FactoryGrantState{}, false
	}
	for _, g := range c.FactoryGrants {
		if g.ProjectID == projectID {
			return factoryGrantState(clientID, projectID, g), true
		}
	}
	return FactoryGrantState{}, false
}

// PutFactoryGrantIfMatch stores one client's grant for one project only when
// the stored grant still carries expected as its version, and reports
// whether it wrote. It returns the pair's state either way: on a conflict
// that is the current state, so a caller can show what it lost to.
//
// The comparison, the client update and the save happen under one lock, so
// of two editors naming the same version exactly one wins. A failed save
// restores the exact prior client value. An empty expected is refused: a
// caller that names no version is not editing a grant, it is resetting one.
func (w *Workspace) PutFactoryGrantIfMatch(clientID string, grant MCPFactoryProjectGrant, expected string) (bool, FactoryGrantState, error) {
	if expected == "" {
		return false, FactoryGrantState{}, errors.New("bridge: conditional grant write needs an If-Match value")
	}
	if strings.TrimSpace(grant.ProjectID) == "" {
		return false, FactoryGrantState{}, errors.New("bridge: grant needs a project id")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	c, ok := w.clients[clientID]
	if !ok {
		return false, FactoryGrantState{}, fmt.Errorf("bridge: unknown client %q", clientID)
	}
	prev := MCPFactoryProjectGrant{ProjectID: grant.ProjectID}
	for _, g := range c.FactoryGrants {
		if g.ProjectID == grant.ProjectID {
			prev = g
			break
		}
	}
	current := factoryGrantState(clientID, grant.ProjectID, prev)
	if current.Version != expected {
		return false, current, nil
	}
	next := c.clone()
	replaced := false
	for i := range next.FactoryGrants {
		if next.FactoryGrants[i].ProjectID == grant.ProjectID {
			next.FactoryGrants[i] = grant
			replaced = true
			break
		}
	}
	if !replaced {
		next.FactoryGrants = append(next.FactoryGrants, grant)
	}
	w.clients[clientID] = next
	if err := w.save(); err != nil {
		w.clients[clientID] = c
		return false, current, err
	}
	return true, factoryGrantState(clientID, grant.ProjectID, grant), nil
}

// marshalAccessDefault returns a project's stored built-in defaults and their
// version. The second result is false when the project has none: that is not an
// empty default but an absent one, and an absent one inherits the effective
// read-only default.
func (w *Workspace) marshalAccessDefault(projectID string) (marshalFactoryAccessState, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if projectID == "" {
		return marshalFactoryAccessState{}, false
	}
	a, ok := w.marshalDefaults[projectID]
	if !ok {
		return marshalFactoryAccessState{}, false
	}
	return marshalAccessState(projectID, "", "", a), true
}

// marshalAccessOverride returns the override that applies to one worker's
// scope and the version of that scope's own record. A worker's own record wins;
// with none, the session's record applies, and the returned WorkerID says which
// one this is. The version reported here is always the version of the scope
// that was asked for, so an editor holding it writes that scope's record and
// never its parent's.
func (w *Workspace) marshalAccessOverride(projectID, sessionID, workerID string) (marshalFactoryAccessState, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if projectID == "" || sessionID == "" {
		return marshalFactoryAccessState{}, false
	}
	if workerID != "" {
		if r, ok := w.marshalOverrides[marshalOverrideKey(projectID, sessionID, workerID)]; ok {
			return marshalAccessState(r.ProjectID, r.SessionID, r.WorkerID, r.Access), true
		}
	}
	r, ok := w.marshalOverrides[marshalOverrideKey(projectID, sessionID, "")]
	if !ok {
		return marshalFactoryAccessState{}, false
	}
	return marshalAccessState(r.ProjectID, r.SessionID, r.WorkerID, r.Access), true
}

// putMarshalAccessDefaultIfMatch stores a project's built-in defaults only when
// the stored value still carries expected as its version, and reports whether
// it wrote. It returns the project's state either way: on a conflict that is
// the current state, so an editor can show what it lost to.
//
// The comparison, the update and the save happen under one lock, so of two
// editors naming one version exactly one wins, and a failed save restores the
// exact prior value. An empty expected is refused, as for a grant: a caller
// that names no version is resetting, not editing.
func (w *Workspace) putMarshalAccessDefaultIfMatch(projectID string, access MarshalFactoryAccess, expected string) (bool, marshalFactoryAccessState, error) {
	if strings.TrimSpace(expected) == "" {
		return false, marshalFactoryAccessState{}, errors.New("bridge: conditional access write needs an If-Match value")
	}
	if strings.TrimSpace(projectID) == "" {
		return false, marshalFactoryAccessState{}, errors.New("bridge: access defaults need a project id")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.marshalDefaults == nil {
		w.marshalDefaults = make(map[string]MarshalFactoryAccess)
	}
	prev, had := w.marshalDefaults[projectID]
	current := marshalAccessState(projectID, "", "", prev)
	if current.Version != expected {
		return false, current, nil
	}
	w.marshalDefaults[projectID] = access.clone()
	if err := w.save(); err != nil {
		if had {
			w.marshalDefaults[projectID] = prev
		} else {
			delete(w.marshalDefaults, projectID)
		}
		return false, current, err
	}
	return true, marshalAccessState(projectID, "", "", access), nil
}

// putMarshalAccessOverrideIfMatch stores one session's override under the same
// versioned rule as the project defaults: one lock, a canonical version, a
// conditional write, and the exact prior record restored when the save fails.
func (w *Workspace) putMarshalAccessOverrideIfMatch(projectID, sessionID, workerID string, access MarshalFactoryAccess, expected string) (bool, marshalFactoryAccessState, error) {
	if strings.TrimSpace(expected) == "" {
		return false, marshalFactoryAccessState{}, errors.New("bridge: conditional access write needs an If-Match value")
	}
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(sessionID) == "" {
		return false, marshalFactoryAccessState{}, errors.New("bridge: an access override needs a project and a session id")
	}
	key := marshalOverrideKey(projectID, sessionID, workerID)
	next := marshalFactoryAccessRecord{ProjectID: projectID, SessionID: sessionID, WorkerID: workerID, Access: access.clone()}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.marshalOverrides == nil {
		w.marshalOverrides = make(map[string]marshalFactoryAccessRecord)
	}
	prev, had := w.marshalOverrides[key]
	current := marshalAccessState(prev.ProjectID, prev.SessionID, prev.WorkerID, prev.Access)
	if current.ProjectID == "" {
		current = marshalAccessState(projectID, sessionID, "", MarshalFactoryAccess{})
	}
	if current.Version != expected {
		return false, current, nil
	}
	w.marshalOverrides[key] = next
	if err := w.save(); err != nil {
		if had {
			w.marshalOverrides[key] = prev
		} else {
			delete(w.marshalOverrides, key)
		}
		return false, current, err
	}
	return true, marshalAccessState(projectID, sessionID, workerID, access), nil
}

func (w *Workspace) Pending() []PendingSpawn {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]PendingSpawn, 0, len(w.pending))
	for _, p := range w.pending {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (w *Workspace) PutPending(p PendingSpawn) error {
	if p.ID == "" {
		return errors.New("pending id is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending[p.ID] = p
	return w.save()
}

func (w *Workspace) DeletePending(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.pending, id)
	return w.save()
}

func (w *Workspace) PendingByID(id string) (PendingSpawn, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.pending[id]
	return p, ok
}

// ErrSpawnRequestConflict is one request ID reused for a different payload.
var ErrSpawnRequestConflict = errors.New("bridge: spawn request id reused with a different payload")

// ClaimSpawnRequest records a caller's first-send request ID durably, before
// the spawn has any side effect, and reports whether this call created it.
//
// The first caller's claim names the agent the request will start, so the
// agent ID is settled before anything runs: a retry, a second press or a
// request that arrives after a restart resolves to that one agent instead of
// starting another, even if the first attempt died mid-spawn. A claim replayed
// with the same digest is the same request; with a different one it is a
// conflict, because the same ID then means two different asks.
func (w *Workspace) ClaimSpawnRequest(scope, requestID, digest, agentID string) (SpawnRequestClaim, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if scope == "" || requestID == "" {
		return SpawnRequestClaim{}, false, errors.New("bridge: a spawn claim needs a scope and a request id")
	}
	if digest == "" || agentID == "" {
		return SpawnRequestClaim{}, false, errors.New("bridge: a spawn claim needs a payload digest and an agent id")
	}
	key := spawnClaimKey(scope, requestID)
	if existing, ok := w.spawnClaims[key]; ok {
		if existing.Digest != digest {
			return SpawnRequestClaim{}, false, fmt.Errorf("%w: %s", ErrSpawnRequestConflict, requestID)
		}
		return existing, false, nil
	}
	claim := SpawnRequestClaim{RequestID: requestID, Scope: scope, Digest: digest, AgentID: agentID, CreatedAt: time.Now().UTC()}
	w.spawnClaims[key] = claim
	if err := w.save(); err != nil {
		delete(w.spawnClaims, key)
		return SpawnRequestClaim{}, false, err
	}
	return claim, true, nil
}

// SpawnClaim returns the claim one request ID holds in a scope.
func (w *Workspace) SpawnClaim(scope, requestID string) (SpawnRequestClaim, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	claim, ok := w.spawnClaims[spawnClaimKey(scope, requestID)]
	return claim, ok
}

// MarkSpawnClaimPrompted records that a claim's first prompt has been handed
// to its agent, so a replay never sends it twice. It writes before the prompt
// is dispatched: one that is lost to a crash is recovered from the thread,
// where a duplicate turn would be a second instruction nobody gave.
func (w *Workspace) MarkSpawnClaimPrompted(scope, requestID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := spawnClaimKey(scope, requestID)
	claim, ok := w.spawnClaims[key]
	if !ok {
		return fmt.Errorf("bridge: no spawn claim for %s", requestID)
	}
	if claim.Prompted {
		return nil
	}
	previous := claim
	claim.Prompted = true
	w.spawnClaims[key] = claim
	if err := w.save(); err != nil {
		w.spawnClaims[key] = previous
		return err
	}
	return nil
}

// ReleaseSpawnClaim forgets a claim whose spawn definitely started nothing,
// so an edited draft retried under the same ID is not refused as a conflict
// with a request that never happened. Releasing an absent claim is not an
// error.
func (w *Workspace) ReleaseSpawnClaim(scope, requestID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := spawnClaimKey(scope, requestID)
	claim, ok := w.spawnClaims[key]
	if !ok {
		return nil
	}
	delete(w.spawnClaims, key)
	if err := w.save(); err != nil {
		w.spawnClaims[key] = claim
		return err
	}
	return nil
}

// SweepExpired drops pending spawns past their deadline and reports how
// many went. An unconfirmed spawn should not linger: the context that
// made it sensible goes stale, and a queue that only grows stops being
// read.
func (w *Workspace) SweepExpired(now time.Time) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	var removed int
	for id, p := range w.pending {
		if !p.ExpiresAt.IsZero() && now.After(p.ExpiresAt) {
			delete(w.pending, id)
			removed++
		}
	}
	if removed > 0 {
		_ = w.save()
	}
	return removed
}

// PutReviewComment stores a review comment under its agent.
func (w *Workspace) PutReviewComment(c ReviewComment) error {
	if c.ID == "" || c.AgentID == "" {
		return errors.New("review comment needs an id and an agent id")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	list := w.reviews[c.AgentID]
	for i := range list {
		if list[i].ID == c.ID {
			list[i] = c
			return w.save()
		}
	}
	w.reviews[c.AgentID] = append(list, c)
	return w.save()
}

// ReviewComments returns a copy of an agent's comments, oldest first.
func (w *Workspace) ReviewComments(agentID string) []ReviewComment {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := append([]ReviewComment(nil), w.reviews[agentID]...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// ResolveReviewComment marks a comment resolved at the given time.
func (w *Workspace) ResolveReviewComment(agentID, id string, at time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	list := w.reviews[agentID]
	for i := range list {
		if list[i].ID == id {
			list[i].ResolvedAt = &at
			return w.save()
		}
	}
	return fmt.Errorf("%w: review comment %s", ErrUnknownReviewComment, id)
}

// PutWatchRule stores the action to take when a Studio watch fires.
func (w *Workspace) PutWatchRule(watchID string, r WatchRule) error {
	if watchID == "" {
		return errors.New("watch rule needs a watch id")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.watchRules[watchID] = r
	return w.save()
}

// WatchRule returns the rule stored for a Studio watch.
func (w *Workspace) WatchRule(watchID string) (WatchRule, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.watchRules[watchID]
	return r, ok
}

// DeleteWatchRule forgets a watch's rule. Deleting an absent rule is not
// an error.
func (w *Workspace) DeleteWatchRule(watchID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.watchRules[watchID]; !ok {
		return nil
	}
	delete(w.watchRules, watchID)
	return w.save()
}

// PutPull stores one pull request record, keyed by its id. Recording the same
// id again replaces the record, so a writer that runs twice leaves one entry.
func (w *Workspace) PutPull(rec contract.PullRecord) error {
	if rec.ID == "" {
		return errors.New("pull record id is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pulls[rec.ID] = rec
	return w.save()
}

// Pull returns one pull request record.
func (w *Workspace) Pull(id string) (contract.PullRecord, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	rec, ok := w.pulls[id]
	return rec, ok
}

// Pulls returns every pull request record, ordered by id.
func (w *Workspace) Pulls() []contract.PullRecord {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]contract.PullRecord, 0, len(w.pulls))
	for _, rec := range w.pulls {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// UpdatePull applies mutate to one record and saves. mutate runs under the
// store's lock, so a read-modify-write cannot interleave with another.
func (w *Workspace) UpdatePull(id string, mutate func(*contract.PullRecord) error) (contract.PullRecord, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	rec, ok := w.pulls[id]
	if !ok {
		return contract.PullRecord{}, ErrUnknownPull
	}
	if err := mutate(&rec); err != nil {
		return contract.PullRecord{}, err
	}
	w.pulls[id] = rec
	return rec, w.save()
}

// RemovePull drops one record and the ask that belongs to it.
func (w *Workspace) RemovePull(id string) (contract.PullRecord, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	rec, ok := w.pulls[id]
	if !ok {
		return contract.PullRecord{}, false, nil
	}
	delete(w.pulls, id)
	delete(w.pullAsks, id)
	return rec, true, w.save()
}

// PutPullAsk stores the ask the Inbox shows about one pull request's fix. One
// ask per pull request: a newer one replaces the older. A record that is gone
// takes no ask: the pull request was closed while the fix finished, and an ask
// naming it would have no card to show on and no route to dismiss it.
func (w *Workspace) PutPullAsk(pull string, ask contract.PullAsk) error {
	if pull == "" {
		return errors.New("a pull ask needs a pull id")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.pulls[pull]; !ok {
		return nil
	}
	ask.Pull = pull
	w.pullAsks[pull] = ask
	return w.save()
}

// PullAsks returns every outstanding ask, oldest first.
func (w *Workspace) PullAsks() []contract.PullAsk {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]contract.PullAsk, 0, len(w.pullAsks))
	for _, a := range w.pullAsks {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At < out[j].At
		}
		return out[i].Pull < out[j].Pull
	})
	return out
}

// ClearPullAsk drops the ask for one pull request. Clearing an absent ask is
// not an error.
func (w *Workspace) ClearPullAsk(pull string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.pullAsks[pull]; !ok {
		return nil
	}
	delete(w.pullAsks, pull)
	return w.save()
}
