package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

var ErrUnknownProject = errors.New("bridge: unknown project")

type projectRuntime struct {
	root     string
	child    *Child
	reg      *Registry
	log      *EventLog
	spawnErr error
}

type ProjectStatus struct {
	Root      string `json:"root"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
	Trust     string `json:"trust"`
	// Isolation is "available", or a human-readable reason isolation cannot
	// be offered for this project. The composer disables its toggle and shows
	// the reason, rather than letting a spawn fail.
	Isolation string `json:"isolation"`
	// OrphanWorktrees are worktrees under this project that no known agent
	// claims. Reported, never deleted — they may hold uncommitted work.
	OrphanWorktrees []string `json:"orphanWorktrees,omitempty"`
}

type Fleet struct {
	ws             *Workspace
	marshalBin     string
	fleetLog       *EventLog
	live           *liveState
	newChild       func(root string) *Child
	mu             sync.Mutex
	runtimes       map[string]*projectRuntime
	sessionProject map[string]string
	orphans        map[string][]string
}

func NewFleet(ws *Workspace, marshalBin string) *Fleet {
	f := &Fleet{ws: ws, marshalBin: marshalBin, fleetLog: NewEventLog(), live: newLiveState(), runtimes: make(map[string]*projectRuntime), sessionProject: make(map[string]string), orphans: make(map[string][]string)}
	f.newChild = func(string) *Child { return &Child{MarshalBin: marshalBin} }
	return f
}
func (f *Fleet) FleetLog() *EventLog { return f.fleetLog }

func (f *Fleet) runtimeFor(root string) (*projectRuntime, error) {
	f.mu.Lock()
	if rt, ok := f.runtimes[root]; ok {
		f.mu.Unlock()
		if rt.spawnErr != nil {
			return nil, rt.spawnErr
		}
		return rt, nil
	}
	f.mu.Unlock()

	child := f.newChild(root)
	reg := NewRegistry(child)
	reg.RootCwd = root
	log := NewEventLog()
	Attach(log, child, reg)
	rt := &projectRuntime{root: root, child: child, reg: reg, log: log}
	f.attachClassifier(rt)
	if err := child.Start(); err != nil {
		rt.spawnErr = fmt.Errorf("start marshal acp for %s: %w (stderr: %s)", root, err, child.StderrLog())
		f.mu.Lock()
		f.runtimes[root] = rt
		f.mu.Unlock()
		return nil, rt.spawnErr
	}
	f.mu.Lock()
	if existing, ok := f.runtimes[root]; ok {
		f.mu.Unlock()
		child.Stop()
		if existing.spawnErr != nil {
			return nil, existing.spawnErr
		}
		return existing, nil
	}
	f.runtimes[root] = rt
	f.mu.Unlock()
	return rt, nil
}

func (f *Fleet) liveRuntimeForSession(id string) (*projectRuntime, error) {
	f.mu.Lock()
	root, ok := f.sessionProject[id]
	rt := f.runtimes[root]
	f.mu.Unlock()
	if !ok || rt == nil {
		return nil, ErrUnknownSession
	}
	if rt.spawnErr != nil {
		return nil, rt.spawnErr
	}
	return rt, nil
}

func (f *Fleet) RuntimeForSession(id string) (*projectRuntime, error) {
	if rt, err := f.liveRuntimeForSession(id); err == nil {
		return rt, nil
	} else if !errors.Is(err, ErrUnknownSession) {
		return nil, err
	}
	a, ok := f.ws.Agent(id)
	if !ok || a.Project == "" {
		return nil, ErrUnknownSession
	}
	rt, err := f.runtimeFor(a.Project)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := rt.reg.Load(ctx, a.Project, id); err != nil {
		return nil, fmt.Errorf("restore agent %s: %w", id, err)
	}
	f.track(id, a.Project)
	return rt, nil
}
func (f *Fleet) LogForSession(id string) (*EventLog, error) {
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		return nil, err
	}
	return rt.log, nil
}
func (f *Fleet) ResolvePermission(id string, d Decision) error {
	f.mu.Lock()
	rts := make([]*projectRuntime, 0, len(f.runtimes))
	for _, rt := range f.runtimes {
		rts = append(rts, rt)
	}
	f.mu.Unlock()
	for _, rt := range rts {
		if err := rt.reg.ResolvePermission(id, d); !errors.Is(err, ErrGone) {
			return err
		}
	}
	return ErrGone
}

func (f *Fleet) ResolveQuestion(id string, a Answers) error {
	f.mu.Lock()
	rts := make([]*projectRuntime, 0, len(f.runtimes))
	for _, rt := range f.runtimes {
		rts = append(rts, rt)
	}
	f.mu.Unlock()
	for _, rt := range rts {
		if err := rt.reg.ResolveQuestion(id, a); !errors.Is(err, ErrGone) {
			return err
		}
	}
	return ErrGone
}

func (f *Fleet) RegistryForSession(id string) (*Registry, error) {
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		return nil, err
	}
	return rt.reg, nil
}
func (f *Fleet) track(id, root string) { f.mu.Lock(); f.sessionProject[id] = root; f.mu.Unlock() }

// SpawnOptions describes a new agent. Isolated puts it in a git worktree;
// Branch and BaseRef are forwarded to ACP's isolation object and may be
// empty, in which case marshal derives them.
type SpawnOptions struct {
	Name     string
	Mode     string
	Isolated bool
	Branch   string
	BaseRef  string
}

func (f *Fleet) Spawn(ctx context.Context, root string, opts SpawnOptions) (string, error) {
	if err := f.ws.AddProject(root); err != nil {
		return "", err
	}
	rt, err := f.runtimeFor(root)
	if err != nil {
		return "", err
	}
	params := map[string]any{"cwd": root, "mcpServers": []any{}, "name": opts.Name}
	if opts.Isolated {
		iso := map[string]any{}
		if opts.Branch != "" {
			iso["branch"] = opts.Branch
		}
		if opts.BaseRef != "" {
			iso["baseRef"] = opts.BaseRef
		}
		params["isolation"] = iso
	}
	raw, err := rt.child.Request(ctx, "session/new", params)
	if err != nil {
		return "", err
	}
	var out struct {
		SessionID string `json:"sessionId"`
		Workspace *struct {
			Branch       string `json:"branch"`
			TargetBranch string `json:"targetBranch"`
		} `json:"workspace"`
	}
	if uerr := json.Unmarshal(raw, &out); uerr != nil || out.SessionID == "" {
		return "", fmt.Errorf("bridge: decode session/new result: %v", uerr)
	}
	f.track(out.SessionID, root)
	rt.reg.track(out.SessionID, root)
	agent := Agent{ID: out.SessionID, Project: root, Name: opts.Name, Mode: opts.Mode, CreatedAt: time.Now().UTC()}
	if out.Workspace != nil {
		agent.Isolated = true
		agent.Branch = out.Workspace.Branch
		agent.TargetBranch = out.Workspace.TargetBranch
	}
	if opts.Mode != "" {
		if err := rt.reg.SetMode(ctx, out.SessionID, opts.Mode); err != nil {
			_ = f.ws.PutAgent(agent)
			return out.SessionID, fmt.Errorf("agent created but setting mode %q failed: %w", opts.Mode, err)
		}
	}
	if err := f.ws.PutAgent(agent); err != nil {
		return out.SessionID, fmt.Errorf("agent created but recording it failed: %w", err)
	}
	return out.SessionID, nil
}

// Diff, Merge and Discard are thin ACP pass-throughs. The bridge holds no
// git knowledge; every operation happens inside marshal.
func (f *Fleet) Diff(ctx context.Context, id, path string) (json.RawMessage, error) {
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"sessionId": id}
	if path != "" {
		params["path"] = path
	}
	return rt.child.Request(ctx, "session/diff", params)
}

func (f *Fleet) Merge(ctx context.Context, id, commitMessage string) (json.RawMessage, error) {
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		return nil, err
	}
	a, ok := f.ws.Agent(id)
	if !ok || a.TargetBranch == "" {
		return nil, fmt.Errorf("bridge: agent %s has no recorded merge target", id)
	}
	params := map[string]any{"sessionId": id, "targetBranch": a.TargetBranch}
	if commitMessage != "" {
		params["commitMessage"] = commitMessage
	}
	return rt.child.Request(ctx, "session/merge", params)
}

func (f *Fleet) Discard(ctx context.Context, id string) error {
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		return err
	}
	if _, rerr := rt.child.Request(ctx, "session/discard", map[string]any{"sessionId": id}); rerr != nil {
		return rerr
	}
	a, ok := f.ws.Agent(id)
	if ok {
		a.Isolated, a.Branch, a.TargetBranch = false, "", ""
		return f.ws.PutAgent(a)
	}
	return nil
}

func (f *Fleet) ProjectStatus() []ProjectStatus {
	roots := f.ws.Projects()
	f.mu.Lock()
	runtimeErrors := make(map[string]error, len(f.runtimes))
	for root, rt := range f.runtimes {
		runtimeErrors[root] = rt.spawnErr
	}
	f.mu.Unlock()
	seen := make(map[string]bool)
	out := make([]ProjectStatus, 0, len(roots)+len(f.runtimes))
	add := func(root string) {
		if seen[root] {
			return
		}
		seen[root] = true
		st := ProjectStatus{
			Root: root, Available: true, Trust: projectTrust(root),
			Isolation: isolationSupport(root), OrphanWorktrees: f.orphans[root],
		}
		if spawnErr := runtimeErrors[root]; spawnErr != nil {
			st.Available = false
			st.Error = spawnErr.Error()
		}
		out = append(out, st)
	}
	for _, root := range roots {
		add(root)
	}
	for root := range runtimeErrors {
		add(root)
	}
	return out
}

// isolationSupport reports whether a project can host isolated agents, or
// why not. Deliberately cheap: a .git entry plus git on PATH. It does not
// shell out to git — the bridge performs no git operations.
func isolationSupport(root string) string {
	if _, err := exec.LookPath("git"); err != nil {
		return "git is not installed on the bridge host"
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "not a git repository"
	}
	return "available"
}

// ReconcileWorktrees asks each live project to prune stale git metadata and
// report worktrees no agent claims, recording the result on ProjectStatus.
//
// Only projects whose child is already up are asked: bringing every project
// up at startup just to prune would defeat lazy spawning. Cold projects are
// reconciled the first time they are used.
func (f *Fleet) ReconcileWorktrees(ctx context.Context) {
	f.mu.Lock()
	roots := make([]string, 0, len(f.runtimes))
	for root, rt := range f.runtimes {
		if rt.spawnErr == nil {
			roots = append(roots, root)
		}
	}
	f.mu.Unlock()

	for _, root := range roots {
		rt, err := f.runtimeFor(root)
		if err != nil {
			continue
		}
		raw, rerr := rt.child.Request(ctx, "session/worktree_prune", map[string]any{"cwd": root})
		if rerr != nil {
			slog.Default().Warn("webbridge: worktree prune failed", "project", root, "err", rerr)
			continue
		}
		var out struct {
			Unknown []string `json:"unknown"`
		}
		if json.Unmarshal(raw, &out) != nil {
			continue
		}
		f.mu.Lock()
		f.orphans[root] = out.Unknown
		f.mu.Unlock()
	}
}

const fleetStreamKey = "fleet"

func (f *Fleet) attachClassifier(rt *projectRuntime) {
	prev := rt.child.OnNotification
	rt.child.OnNotification = func(method string, params json.RawMessage) {
		if prev != nil {
			prev(method, params)
		}
		if d, ok := classifyNotification(method, params); ok {
			f.live.apply(d)
			_, _ = f.fleetLog.Append(fleetStreamKey, d)
		}
	}

	// Permission and question requests are child-initiated REQUESTS, so
	// they never pass through OnNotification. Registry.emitEvent is the
	// only place they surface, so chain onto OnEvent (installed by Attach,
	// which runs first) to learn what an agent is parked on.
	prevEvent := rt.reg.OnEvent
	rt.reg.OnEvent = func(sessionID string, payload any) {
		if prevEvent != nil {
			prevEvent(sessionID, payload)
		}
		if p, ok := classifyRegistryEvent(payload); ok {
			f.live.observePending(sessionID, p)
			_, _ = f.fleetLog.Append(fleetStreamKey, fleetDelta{
				Kind: "pending", SessionID: sessionID, PendingKind: p.kind,
			})
		}
	}
}

func (f *Fleet) Snapshot() []AgentStatus {
	out := make([]AgentStatus, 0)
	for _, a := range f.ws.Agents() {
		live := f.live.get(a.ID)
		st := AgentStatus{ID: a.ID, Project: a.Project, Name: a.Name, Mode: a.Mode, Status: "idle", Activity: live.activity, ContextPct: live.contextPct, ChangedFiles: live.changedFiles, Interrupted: a.Interrupted, Isolated: a.Isolated, Branch: a.Branch, UpdatedAt: live.updatedAt}
		if live.mode != "" {
			st.Mode = live.mode
		}
		if a.CreatedAt.After(st.UpdatedAt) {
			st.UpdatedAt = a.CreatedAt
		}
		if rt, err := f.liveRuntimeForSession(a.ID); err == nil {
			// Registry.Pending is the authority on whether anything is
			// still outstanding; live.pending only supplies the payload.
			// A resolved request therefore stops being advertised even
			// though its payload is still cached.
			pending := rt.reg.Pending(a.ID)
			if pending != "" && live.pending != nil && live.pending.kind == pending {
				st.Pending = &PendingRequest{
					Kind: live.pending.kind, ID: live.pending.id, Params: live.pending.params,
				}
			}
			switch pending {
			case "approval":
				st.Status = "awaiting-approval"
			case "question":
				st.Status = "awaiting-question"
			default:
				if info, ok := rt.reg.lookup(a.ID); ok && info.Busy {
					st.Status = "running"
				}
			}
		} else if !errors.Is(err, ErrUnknownSession) {
			st.Status = "error"
		}
		out = append(out, st)
	}
	return out
}

func (f *Fleet) StopProject(root string) {
	f.mu.Lock()
	rt := f.runtimes[root]
	delete(f.runtimes, root)
	var removed []string
	for id, project := range f.sessionProject {
		if project == root {
			removed = append(removed, id)
			delete(f.sessionProject, id)
		}
	}
	f.mu.Unlock()
	f.live.removeProject(removed)
	if rt != nil && rt.spawnErr == nil {
		rt.child.Stop()
	}
}

func (f *Fleet) Close() {
	f.mu.Lock()
	rts := make([]*projectRuntime, 0, len(f.runtimes))
	for _, rt := range f.runtimes {
		rts = append(rts, rt)
	}
	f.runtimes = make(map[string]*projectRuntime)
	f.sessionProject = make(map[string]string)
	f.mu.Unlock()
	for _, rt := range rts {
		if rt.spawnErr == nil {
			rt.child.Stop()
		}
	}
}
