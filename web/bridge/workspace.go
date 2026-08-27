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

const workspaceVersion = 2

// DefaultOwnerID is the single implicit owner in a single-operator
// deployment. Every agent carries an owner from the first commit so that
// adding real accounts later is a new auth layer, not a migration of
// every persisted record.
const DefaultOwnerID = "local"

// Spawn origins. Policy varies by origin: an agent created by the UI is
// the operator acting directly, while other origins are subject to
// confirmation and scoping rules.
const (
	OriginUI  = "ui"
	OriginCLI = "cli"
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
	// Profile is the resolved container shape this agent runs under.
	Profile RuntimeProfile `json:"profile"`
	// SourceKind is the workspace source ("local" in S1; "git" in S2).
	SourceKind string `json:"sourceKind,omitempty"`
	// SourceRef is the source-specific locator: a path for "local", a
	// repo URL for "git".
	SourceRef string `json:"sourceRef,omitempty"`
}

type workspaceFile struct {
	Version  int      `json:"version"`
	Projects []string `json:"projects"`
	Agents   []Agent  `json:"agents"`
}

type Workspace struct {
	path     string
	mu       sync.Mutex
	projects []string
	agents   map[string]Agent
}

func NewWorkspace(path string) *Workspace {
	return &Workspace{path: path, agents: make(map[string]Agent)}
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
	w.agents = make(map[string]Agent, len(f.Agents))
	for _, a := range f.Agents {
		w.agents[a.ID] = a
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
	f.Version = workspaceVersion
}

func (w *Workspace) save() error {
	f := workspaceFile{Version: workspaceVersion, Projects: w.projects, Agents: make([]Agent, 0, len(w.agents))}
	for _, a := range w.agents {
		f.Agents = append(f.Agents, a)
	}
	sort.Slice(f.Agents, func(i, j int) bool { return f.Agents[i].ID < f.Agents[j].ID })
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
