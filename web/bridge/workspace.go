package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const workspaceVersion = 5

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
)

type Agent struct {
	ID          string    `json:"id"`
	Project     string    `json:"project"`
	Name        string    `json:"name,omitempty"`
	Mode        string    `json:"mode,omitempty"`
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
}

// GateOverride records an operator's decision to push despite a failed
// or skipped verify.
//
// It is persisted rather than transient because that is what makes the
// exception honest: it surfaces on the exit panel and feeds S3's audit
// log. A nil GateOverride means the gate passed on its own merits.
type GateOverride struct {
	Reason        string    `json:"reason"`
	At            time.Time `json:"at"`
	By            string    `json:"by"` // OwnerID
	FailedCommand string    `json:"failedCommand,omitempty"`
	Skipped       bool      `json:"skipped,omitempty"`
}

// Repo is a registered repository. Registration is what grants push
// capability: an agent spawned against a raw URL is read-only, and only
// a registered repo carries a credential reference.
type Repo struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Branch  string `json:"branch,omitempty"`
	CredRef string `json:"credRef,omitempty"`
	OwnerID string `json:"ownerId"`
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
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type workspaceFile struct {
	Version  int            `json:"version"`
	Projects []string       `json:"projects"`
	Repos    []Repo         `json:"repos,omitempty"`
	Agents   []Agent        `json:"agents"`
	Clients  []MCPClient    `json:"clients,omitempty"`
	Pending  []PendingSpawn `json:"pending,omitempty"`
}

type Workspace struct {
	path     string
	mu       sync.Mutex
	projects []string
	repos    map[string]Repo
	agents   map[string]Agent
	clients  map[string]MCPClient
	pending  map[string]PendingSpawn
}

func NewWorkspace(path string) *Workspace {
	return &Workspace{
		path:    path,
		repos:   make(map[string]Repo),
		agents:  make(map[string]Agent),
		clients: make(map[string]MCPClient),
		pending: make(map[string]PendingSpawn),
	}
}

func DefaultWorkspacePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "marshal", "fleet.json"), nil
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
	w.pending = make(map[string]PendingSpawn, len(f.Pending))
	for _, p := range f.Pending {
		w.pending[p.ID] = p
	}
	return "", nil
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

func (w *Workspace) save() error {
	f := workspaceFile{Version: workspaceVersion, Projects: w.projects, Repos: make([]Repo, 0, len(w.repos)), Agents: make([]Agent, 0, len(w.agents))}
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
	for _, p := range w.pending {
		f.Pending = append(f.Pending, p)
	}
	sort.Slice(f.Pending, func(i, j int) bool { return f.Pending[i].ID < f.Pending[j].ID })
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
	for _, p := range w.projects {
		if p == root {
			return nil
		}
	}
	w.projects = append(w.projects, root)
	return w.save()
}

func (w *Workspace) RemoveProject(root string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
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
		}
	}
	return w.save()
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

func (w *Workspace) Clients() []MCPClient {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]MCPClient, 0, len(w.clients))
	for _, c := range w.clients {
		out = append(out, c)
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
	w.clients[c.ID] = c
	return w.save()
}

func (w *Workspace) DeleteClient(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.clients, id)
	return w.save()
}

func (w *Workspace) Client(id string) (MCPClient, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c, ok := w.clients[id]
	return c, ok
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
