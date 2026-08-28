package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrUnknownProject = errors.New("bridge: unknown project")

// ErrUnregisteredRepo is returned by a remote-source spawn whose git
// source is not a registered repo — either an unknown repo id, or a raw
// URL from an origin (MCP, issue) that policy requires to name a
// registered repo.
var ErrUnregisteredRepo = errors.New("bridge: repo is not registered")

// ErrUnknownAgent is returned by agent-scoped methods for ids the Fleet
// does not track. The HTTP layer maps it to 404.
var ErrUnknownAgent = errors.New("bridge: unknown agent")

// Agent lifecycle states, reported to the fleet UI.
const (
	AgentQueued           = "queued"
	AgentProvisioning     = "provisioning"
	AgentRunning          = "running"
	AgentAwaitingApproval = "awaiting-approval"
	AgentParked           = "parked"
	AgentDone             = "done"
	AgentFailed           = "failed"
)

// Limits bounds the fleet's resource usage. Zero values mean "default"
// or "unlimited" depending on the field.
type Limits struct {
	MaxConcurrent int
	MaxDiskMB     int64
	MaxCloneMB    int64
}

// agentRuntime is one agent: its own container, its own JSON-RPC child,
// its own registry and event log. Two agents on the same project share
// nothing.
type agentRuntime struct {
	id      string // bridge-minted, names the container
	root    string // project root
	profile RuntimeProfile

	child *Child
	reg   *Registry
	log   *EventLog

	// sessionID is the ACP session inside this agent, assigned by the
	// agent itself once session/new returns. Empty until then.
	sessionID string

	// sourceKind is "local" or "git"; stopAgent uses it to decide whether
	// the agent's prepared working tree must be removed.
	sourceKind string

	// containerized is true when the agent runs in a container whose
	// filesystem view differs from the bridge's. agentPath uses it to
	// decide whether translation is needed.
	containerized bool

	// versionWarning is set when the agent's initialize handshake reported
	// a version that differs from the bridge's buildVersion. It is a
	// warning only — the spawn is never refused on skew.
	versionWarning bool

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
	ws         *Workspace
	marshalBin string
	agentEnv   map[string]string
	fleetLog   *EventLog
	live       *liveState

	// buildVersion is the release version of the webbridge binary, stamped
	// at build time via -ldflags -X. Empty when built from source; the
	// --version banner reports "dev" in that case.
	buildVersion string

	// runner executes container-runtime commands for derived-image builds.
	// Nil means the real runner (exec.Command); tests inject a fake so the
	// derive path is exercisable without a daemon.
	runner commandRunner

	// git runs hardened git subprocesses for remote sources (mirroring,
	// worktree prep). Nil when git was not found at startup; local-path
	// spawns still work, git-sourced spawns fail with a clear error.
	git *gitRunner
	// creds resolves credential references for registered repos.
	creds *CredentialStore
	// stateDir is where git mirrors and agent working trees live.
	stateDir string
	// stateVolume is the name of the shared state volume mounted at
	// stateDir. Agents mount subpaths of it.
	stateVolume string
	// audit is the security-relevant action log. Nil in tests that do
	// not opt in; auditf tolerates that.
	audit *AuditLog
	// limits bounds concurrency and disk usage. Zero values mean
	// "default" (4 concurrent) or "unlimited" (no disk/clone cap).
	limits Limits

	// projectMounts maps host paths to the bridge's in-container view,
	// for translating LocalPath agent workspace mounts to the daemon's
	// view. Empty when the bridge is a host process.
	projectMounts []ProjectMount

	// newRuntime builds the Child for an agent. Tests inject a fake
	// transport here; production returns a container-backed Child.
	newRuntime func(a Agent) *Child

	// slots bounds concurrent agent spawns.
	slots *slots

	mu           sync.Mutex
	runtimes     map[string]*agentRuntime // keyed by agent id
	sessionAgent map[string]string        // ACP session id -> agent id
	orphans      map[string][]string
	reconciled   map[string]bool

	// done is closed by Close to signal background goroutines (the
	// poller) to stop.
	done chan struct{}
	// closeOnce guards Close against a double-close panic on done.
	closeOnce sync.Once
	// rateLimits tracks per-repo "not before" times for backoff.
	rateMu     sync.Mutex
	rateLimits map[string]time.Time

	// diskCache memoises measureDisk(stateDir) so the fleet UI can show
	// disk usage without walking multi-gigabyte mirrors on every poll.
	// diskCacheOK reports whether the cache is populated; it is cleared
	// by invalidateDisk after any prune or tree removal.
	diskCache   diskUsage
	diskCacheMu sync.Mutex
	diskCacheOK bool
}

func NewFleet(ws *Workspace, marshalBin string, agentEnv map[string]string, stateDir string, limits Limits, buildVersion string, projectMounts []ProjectMount, stateVolume string) *Fleet {
	if stateDir == "" {
		stateDir = filepath.Dir(ws.path)
	}
	if stateVolume == "" {
		stateVolume = "marshal-state"
	}
	maxConcurrent := limits.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	f := &Fleet{
		ws: ws, marshalBin: marshalBin, agentEnv: agentEnv,
		buildVersion: buildVersion,
		fleetLog:     NewEventLog(), live: newLiveState(),
		runtimes:     make(map[string]*agentRuntime),
		sessionAgent: make(map[string]string),
		orphans:      make(map[string][]string), reconciled: make(map[string]bool),
		slots:         newSlots(maxConcurrent),
		stateDir:      stateDir,
		stateVolume:   stateVolume,
		audit:         NewAuditLog(stateDir),
		limits:        limits,
		projectMounts: projectMounts,
		done:          make(chan struct{}),
		rateLimits:    make(map[string]time.Time),
	}
	// Remote sources need git and (later) credentials. Absent git is not
	// fatal at startup: local-path spawns still work, and a git-sourced
	// spawn reports a clear error via Spawn.
	if g, err := newGitRunner(); err == nil {
		f.git = g
	}
	f.creds = NewCredentialStore(nil)
	f.newRuntime = func(a Agent) *Child {
		runtime, name, ok := detectedRuntime()
		if !ok {
			// No container runtime: fall back to a host process so a
			// laptop without docker still works.
			return &Child{MarshalBin: marshalBin}
		}
		cfg := ContainerConfig{
			Runtime:       runtime,
			RuntimeName:   name,
			Image:         a.Profile.Image,
			Name:          containerNameFor(a.ID),
			WorkspaceDir:  a.Project,
			SocketDir:     socketDirFor(f.stateDir, a.ID),
			StateVolume:   f.stateVolume,
			WorkSubpath:   "work/" + a.ID,
			SocketSubpath: "sockets/" + a.ID,
			CPUs:          a.Profile.CPUs,
			MemoryMB:      a.Profile.MemoryMB,
			Env:           f.agentEnv,
		}
		// A LocalPath agent works on the host checkout itself, so its
		// workspace is a bind mount of the daemon's view of that path.
		// Git-sourced agents use a volume subpath instead.
		if a.SourceKind == "local" {
			hostPath, err := f.localMountFor(a)
			if err != nil {
				// A declared root that the path is not under is a
				// misconfiguration. newRuntime cannot return an error,
				// so log the actionable message (it names
				// --project-mount) and leave LocalMount empty. The
				// container starts with a volume-subpath workspace
				// instead of the intended bind mount, and session/new
				// fails because cwd points at a path the agent cannot
				// see. The API caller sees the JSON-RPC invalid-params
				// error; the operator who reads the bridge logs sees
				// the actionable message. Neither is great, but
				// deferring to the runtime's "mounts denied" would be
				// worse — it surfaces neither.
				slog.Default().Error("webbridge: refuse local agent mount", "agent", a.ID, "err", err)
			} else {
				cfg.LocalMount = hostPath
			}
		}
		return &Child{Transport: newContainerTransport(cfg)}
	}
	return f
}

// localMountFor resolves the bind-mount source for a LocalPath agent.
//
// Two situations look alike and are not. With no declared roots the
// bridge is a host process: its own view is the daemon's view, and the
// path passes through unchanged. With roots declared the bridge is
// containerized, and a path under none of them is a misconfiguration —
// returned as an error here, where it can name --project-mount, rather
// than deferred to the runtime's "mounts denied", which cannot.
func (f *Fleet) localMountFor(a Agent) (string, error) {
	if len(f.projectMounts) == 0 {
		return a.Project, nil
	}
	return TranslateToHost(f.projectMounts, a.Project)
}

func (f *Fleet) FleetLog() *EventLog { return f.fleetLog }

// auditf appends a record, and never propagates a failure to the caller.
func (f *Fleet) auditf(e AuditEvent) {
	if f.audit == nil {
		return
	}
	if err := f.audit.Append(e); err != nil {
		slog.Default().Warn("webbridge: audit append failed", "event", e.Event, "err", err)
	}
}

// enforceDisk refuses a new spawn when the state directory is over
// budget, after first reclaiming anything unreferenced.
//
// The order matters: prune, re-measure, then refuse. Refusing without
// pruning would strand an operator whose disk is full of mirrors nothing
// uses any more.
func (f *Fleet) enforceDisk() error {
	if f.limits.MaxDiskMB <= 0 {
		return nil
	}
	budget := f.limits.MaxDiskMB << 20
	if f.diskUsage().Total <= budget {
		return nil
	}
	if _, err := f.Prune(); err != nil {
		return fmt.Errorf("reclaim disk: %w", err)
	}
	if used := f.diskUsage().Total; used > budget {
		return fmt.Errorf("state directory is %d MB, over the %d MB budget; "+
			"raise --max-disk-mb or remove finished agents",
			used>>20, f.limits.MaxDiskMB)
	}
	return nil
}

// newAgentID mints the bridge-side identifier for an agent. It is
// generated before the agent starts, because a container must be named
// before it runs — so it cannot be the ACP session id, which only
// exists once session/new has returned.
func newAgentID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("bridge: generate agent id: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// clientByID returns a registered MCP client by id.
func (f *Fleet) clientByID(id string) (MCPClient, bool) {
	return f.ws.Client(id)
}

// Clients returns all registered MCP clients.
func (f *Fleet) Clients() []MCPClient {
	return f.ws.Clients()
}

// spawnFromRequest maps an intake SpawnRequest onto SpawnOptions and
// calls the existing Fleet.Spawn.
func (f *Fleet) spawnFromRequest(ctx context.Context, req SpawnRequest) (string, error) {
	opts := SpawnOptions{
		Name:   req.Title,
		Mode:   req.Mode,
		Prompt: req.Prompt,
		Origin: req.Origin,
		RepoID: req.RepoID,
		Ref:    req.Ref,
	}
	id, err := f.Spawn(ctx, "", opts)
	if err != nil {
		return "", err
	}
	// Record the submitting client and issue origin on the agent so
	// per-client scoping and PR-to-issue linking work.
	if req.ClientID != "" || req.IssueNumber != 0 || req.IssueURL != "" {
		if a, ok := f.ws.Agent(id); ok {
			a.ClientID = req.ClientID
			a.IssueNumber = req.IssueNumber
			a.IssueURL = req.IssueURL
			if err := f.ws.PutAgent(a); err != nil {
				return id, fmt.Errorf("persist client id on agent: %w", err)
			}
		}
	}
	return id, nil
}

// runtimeProbe is the memoised result of looking for a container runtime.
type runtimeProbe struct {
	path string // absolute path, pinned at detect time
	name string // "docker" or "podman", for user-facing messages
	ok   bool
}

// probeRuntime resolves the container runtime once per process.
var probeRuntime = sync.OnceValue(func() runtimeProbe {
	for _, candidate := range []string{"docker", "podman"} {
		abs, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		err = exec.CommandContext(ctx, abs, "info").Run()
		cancel()
		if err == nil {
			return runtimeProbe{path: abs, name: candidate, ok: true}
		}
	}
	return runtimeProbe{}
})

// detectedRuntime reports the container runtime's absolute path, its
// name, and whether one is usable at all.
func detectedRuntime() (path, name string, ok bool) {
	p := probeRuntime()
	return p.path, p.name, p.ok
}

// socketDirFor is the per-agent ACP socket directory.
//
// It lives under the state root like every other piece of agent state.
// It previously used os.TempDir(), which a containerized bridge cannot
// share with a sibling container: a bind-mount source resolves against
// the daemon's view, not the bridge's.
func socketDirFor(stateDir, agentID string) string {
	return filepath.Join(stateDir, "sockets", agentID)
}

// runtimeForAgent returns the live runtime for an agent id.
func (f *Fleet) runtimeForAgent(id string) (*agentRuntime, error) {
	f.mu.Lock()
	rt, ok := f.runtimes[id]
	f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: agent %s", ErrUnknownAgent, id)
	}
	if rt.spawnErr != nil {
		return nil, rt.spawnErr
	}
	return rt, nil
}

// runtimeForRoot returns any live runtime for a project root. With
// per-agent runtimes there may be several; callers that need a specific
// agent should use runtimeForAgent.
func (f *Fleet) runtimeForRoot(root string) (*agentRuntime, error) {
	f.mu.Lock()
	var rt *agentRuntime
	for _, candidate := range f.runtimes {
		if candidate.root == root {
			rt = candidate
			break
		}
	}
	f.mu.Unlock()
	if rt == nil {
		return nil, fmt.Errorf("%w: project %s", ErrUnknownProject, root)
	}
	if rt.spawnErr != nil {
		return nil, rt.spawnErr
	}
	return rt, nil
}

// startRuntime builds and starts one agent's runtime. The caller owns
// persisting the Agent record. ctx bounds the initialize handshake.
func (f *Fleet) startRuntime(ctx context.Context, a Agent) (*agentRuntime, error) {
	child := f.newRuntime(a)
	reg := NewRegistry(child)
	reg.RootCwd = a.Project
	log := NewEventLog()
	Attach(log, child, reg)

	_, isContainer := child.Transport.(*containerTransport)
	rt := &agentRuntime{id: a.ID, root: a.Project, profile: a.Profile,
		child: child, reg: reg, log: log, sourceKind: a.SourceKind,
		containerized: isContainer}
	f.attachClassifier(rt)

	if err := child.Start(); err != nil {
		rt.spawnErr = fmt.Errorf("start agent %s for %s: %w (stderr: %s)",
			a.ID, a.Project, err, child.StderrLog())
	} else {
		// Handshake: learn the agent's own version so a stale derived or
		// custom image can be flagged. A mismatch is a warning, never a
		// refusal — refusing would turn a nuisance into an outage.
		f.checkAgentVersion(ctx, rt)
	}

	f.mu.Lock()
	f.runtimes[a.ID] = rt
	f.mu.Unlock()
	return rt, rt.spawnErr
}

// checkAgentVersion calls initialize on a freshly started child, compares
// the reported agentInfo.version against the bridge's buildVersion, and
// records a warning on the runtime when they differ. It never refuses the
// spawn: a failed or unparseable handshake is logged and ignored.
func (f *Fleet) checkAgentVersion(ctx context.Context, rt *agentRuntime) {
	raw, err := rt.child.Request(ctx, "initialize", map[string]any{"protocolVersion": 1})
	if err != nil {
		slog.Default().Warn("webbridge: initialize handshake failed",
			"agent", rt.id, "err", err)
		return
	}
	var res struct {
		AgentInfo struct {
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	if uerr := json.Unmarshal(raw, &res); uerr != nil {
		slog.Default().Warn("webbridge: decode initialize handshake failed",
			"agent", rt.id, "err", uerr)
		return
	}
	agentVersion := res.AgentInfo.Version
	// Both sides must carry a real version; an empty or "dev" value on
	// either side means a source build, where skew is meaningless.
	if agentVersion == "" || agentVersion == "dev" ||
		f.buildVersion == "" || f.buildVersion == "dev" {
		return
	}
	if agentVersion != f.buildVersion {
		rt.versionWarning = true
		slog.Default().Warn("webbridge: agent version differs from bridge",
			"agent", rt.id, "agentVersion", agentVersion, "bridgeVersion", f.buildVersion)
	}
}

func (f *Fleet) liveRuntimeForSession(sessionID string) (*agentRuntime, error) {
	f.mu.Lock()
	agentID, ok := f.sessionAgent[sessionID]
	rt := f.runtimes[agentID]
	f.mu.Unlock()
	if !ok || rt == nil {
		return nil, ErrUnknownSession
	}
	if rt.spawnErr != nil {
		return nil, rt.spawnErr
	}
	return rt, nil
}

func (f *Fleet) RuntimeForSession(id string) (*agentRuntime, error) {
	// Try as a session id first.
	if rt, err := f.liveRuntimeForSession(id); err == nil {
		return rt, nil
	} else if !errors.Is(err, ErrUnknownSession) {
		return nil, err
	}
	// Try as an agent id.
	if rt, err := f.runtimeForAgent(id); err == nil {
		return rt, nil
	}
	// Fall back to persisted agent.
	a, ok := f.ws.Agent(id)
	if !ok || a.Project == "" {
		return nil, ErrUnknownSession
	}
	// The agent was persisted but has no live runtime — start one.
	if err := f.slots.acquire(context.Background()); err != nil {
		return nil, fmt.Errorf("wait for an agent slot: %w", err)
	}
	rt, err := f.startRuntime(context.Background(), a)
	if err != nil {
		f.stopAgent(a.ID) // removes the failed runtime and releases the slot
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := f.restoreSession(ctx, rt, a); err != nil {
		f.stopAgent(a.ID)
		return nil, err
	}
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
	rts := make([]*agentRuntime, 0, len(f.runtimes))
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
	rts := make([]*agentRuntime, 0, len(f.runtimes))
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

// SpawnOptions describes a new agent. Isolated puts it in a git worktree;
// Branch and BaseRef are forwarded to ACP's isolation object and may be
// empty, in which case marshal derives them.
//
// RepoID, URL and Ref select a remote source: a registered repo id, a
// raw URL (UI-only, read-only), and an optional git ref to check out.
type SpawnOptions struct {
	Name     string
	Mode     string
	Prompt   string
	Isolated bool
	Branch   string
	BaseRef  string
	Profile  RuntimeProfile
	RepoID   string
	URL      string
	Ref      string
	Origin   string
}

// gitSource is the resolved remote source for a spawn, or a local path.
type gitSource struct {
	kind     string // "local" | "git"
	ref      string // repo id, or the raw URL
	url      string
	gitRef   string
	credRef  string
	readOnly bool
}

// resolveSource decides what a spawn works on. A local path is the
// default; a registered repo (RepoID) or raw URL (URL, UI-only) selects
// a git remote. An unregistered or policy-rejected source returns
// ErrUnregisteredRepo.
func (f *Fleet) resolveSource(opts SpawnOptions, origin string) (gitSource, error) {
	switch {
	case opts.RepoID != "":
		r, ok := f.ws.Repo(opts.RepoID)
		if !ok {
			return gitSource{}, fmt.Errorf("%w: %s", ErrUnregisteredRepo, opts.RepoID)
		}
		ref := opts.Ref
		if ref == "" {
			ref = r.Branch
		}
		return gitSource{kind: "git", ref: opts.RepoID, url: r.URL,
			gitRef: ref, credRef: r.CredRef}, nil

	case opts.URL != "":
		if origin != OriginUI {
			return gitSource{}, fmt.Errorf("%w: %s spawns must name a registered repo",
				ErrUnregisteredRepo, origin)
		}
		return gitSource{kind: "git", ref: opts.URL, url: opts.URL,
			gitRef: opts.Ref, readOnly: true}, nil

	default:
		return gitSource{kind: "local"}, nil
	}
}

func (f *Fleet) Spawn(ctx context.Context, root string, opts SpawnOptions) (string, error) {
	origin := opts.Origin
	if origin == "" {
		origin = OriginUI
	}
	src, err := f.resolveSource(opts, origin)
	if err != nil {
		return "", err
	}

	a := Agent{
		ID:        newAgentID(),
		Name:      opts.Name,
		Mode:      opts.Mode,
		Prompt:    opts.Prompt,
		CreatedAt: time.Now().UTC(),
		OwnerID:   DefaultOwnerID,
		Origin:    origin,
	}
	a.SourceKind = src.kind
	a.SourceRef = src.ref
	a.ReadOnly = src.readOnly

	// The workspace directory is the local path for local spawns, or a
	// freshly prepared git working tree for remote sources.
	workDir := root
	if src.kind == "git" {
		if f.git == nil {
			return "", fmt.Errorf("bridge: git is required for remote sources but was not found at startup")
		}
		cred, err := f.creds.Resolve(DefaultOwnerID, src.credRef)
		if err != nil {
			return "", fmt.Errorf("resolve credential for %s: %w", src.ref, err)
		}
		// Fast-path pre-check: ask the forge for the repo size and refuse
		// before spending bandwidth on a repo the clone cap would reject
		// anyway. This is not the control — the clone monitor below is —
		// so a failed forge lookup (no forge, no PAT, API error) degrades
		// to proceeding with the clone, which has its own monitor.
		if f.limits.MaxCloneMB > 0 {
			if repo, ok := f.ws.Repo(src.ref); ok {
				cap := f.limits.MaxCloneMB << 20
				if forge, fcred, ferr := f.forgeFor(repo); ferr == nil {
					if size, serr := forge.RepoSize(ctx, repo, fcred); serr == nil && size > cap {
						return "", fmt.Errorf("repo %s is %d MB, over the %d MB clone cap",
							repo.ID, size>>20, f.limits.MaxCloneMB)
					}
				}
			}
		}
		mirror, err := f.git.EnsureMirrorCapped(ctx, f.stateDir, src.url, cred, f.limits.MaxCloneMB<<20)
		if err != nil {
			return "", err
		}
		// A raw-URL spawn may name no ref. Fall back to the mirror's HEAD
		// so TargetBranch (the base for S2b's patch export) is never
		// empty, and record the ref actually used.
		if src.gitRef == "" {
			head, err := f.git.mirrorHead(mirror)
			if err != nil {
				return "", err
			}
			src.gitRef = head
		}
		a.TargetBranch = src.gitRef
		workDir, err = f.git.PrepareTree(f.stateDir, a.ID, mirror, src.url, src.gitRef)
		if err != nil {
			return "", err
		}
	} else {
		if err := f.ws.AddProject(root); err != nil {
			return "", err
		}
	}
	a.Project = workDir

	// Resolve the runtime profile now that workDir is known, so a
	// git-sourced repo with a .devcontainer/devcontainer.json is
	// honoured. For local spawns workDir == root.
	profile, _ := ResolveProfile(workDir, opts.Profile, f.buildVersion)
	a.Profile = profile

	// A declared base (e.g. node:20) carries no marshal. Derive an image
	// that adds marshal on top, and run the agent against that. A marshal
	// image is used as-is. A build failure refuses the spawn — running an
	// agent in an environment the repo did not ask for is worse than
	// refusing to run it.
	if !strings.HasPrefix(a.Profile.Image, agentImageRepo) {
		derived, err := f.ensureDerivedImage(ctx, a.Profile.Image)
		if err != nil {
			if src.kind == "git" && f.git != nil {
				_ = f.git.RemoveTree(f.stateDir, a.ID)
			}
			return "", err
		}
		a.Profile.Image = derived
	}

	// Enforce the disk budget before acquiring a slot: refusing a new
	// spawn is the control, not stopping an existing agent.
	if err := f.enforceDisk(); err != nil {
		if src.kind == "git" && f.git != nil {
			_ = f.git.RemoveTree(f.stateDir, a.ID)
		}
		return "", err
	}

	if err := f.slots.acquire(ctx); err != nil {
		if src.kind == "git" && f.git != nil {
			_ = f.git.RemoveTree(f.stateDir, a.ID)
		}
		return "", fmt.Errorf("wait for an agent slot: %w", err)
	}

	rt, err := f.startRuntime(ctx, a)
	if err != nil {
		// startRuntime stores the failed runtime in f.runtimes so
		// ProjectStatus can report the error. For local spawns we
		// preserve that behaviour (release the slot, leave the
		// runtime). For git spawns the tree must be cleaned up, and
		// the failed runtime is not worth keeping.
		if src.kind == "git" {
			f.stopAgent(a.ID)
		} else {
			f.slots.release()
		}
		return "", err
	}

	// A cold project (not running when the bridge started) is reconciled the
	// first time it is used, so its orphan worktrees are reported even though
	// startup reconciliation never saw it.
	f.reconcileOnce(ctx, workDir)

	params := map[string]any{"cwd": workDir, "mcpServers": []any{}, "name": opts.Name}
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
		f.stopAgent(a.ID)
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
		f.stopAgent(a.ID)
		return "", fmt.Errorf("bridge: decode session/new result: %v", uerr)
	}

	f.mu.Lock()
	rt.sessionID = out.SessionID
	f.sessionAgent[out.SessionID] = a.ID
	f.mu.Unlock()

	// Persist the session id with the agent record so a later reattach
	// can restore the mapping. Must be set before the copy below.
	a.SessionID = out.SessionID

	agentCwd, aerr := rt.agentPath(workDir)
	if aerr != nil {
		f.stopAgent(a.ID)
		return "", fmt.Errorf("resolve cwd for agent %s: %w", a.ID, aerr)
	}
	rt.reg.track(out.SessionID, agentCwd)
	agent := a // copy the resolved agent
	if out.Workspace != nil {
		agent.Isolated = true
		agent.Branch = out.Workspace.Branch
		agent.TargetBranch = out.Workspace.TargetBranch
	}
	if opts.Mode != "" {
		if err := rt.reg.SetMode(ctx, out.SessionID, opts.Mode); err != nil {
			_ = f.ws.PutAgent(agent)
			return a.ID, fmt.Errorf("agent created but setting mode %q failed: %w", opts.Mode, err)
		}
	}
	if err := f.ws.PutAgent(agent); err != nil {
		return a.ID, fmt.Errorf("agent created but recording it failed: %w", err)
	}
	f.auditf(AuditEvent{Event: AuditSpawn, OwnerID: a.OwnerID, AgentID: a.ID, Origin: origin})
	return a.ID, nil
}

// Diff, Merge and Discard are thin ACP pass-throughs. The bridge holds no
// git knowledge; every operation happens inside marshal.
// sessionIDFor returns the ACP session id for a live runtime, reading
// under f.mu so it is safe to call concurrently with Spawn or
// restoreSession mutating the field.
func (f *Fleet) sessionIDFor(rt *agentRuntime) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return rt.sessionID
}

func (f *Fleet) Diff(ctx context.Context, id, path string) (json.RawMessage, error) {
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"sessionId": f.sessionIDFor(rt)}
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
	params := map[string]any{"sessionId": f.sessionIDFor(rt), "targetBranch": a.TargetBranch}
	if commitMessage != "" {
		params["commitMessage"] = commitMessage
	}
	out, err := rt.child.Request(ctx, "session/merge", params)
	if err != nil {
		return nil, err
	}
	// A successful merge returns the session to the project root and removes
	// the worktree/branch. Clear the agent's persisted isolation state so
	// /api/agents stops reporting it as isolated. A refusal (dirty, conflicts,
	// target moved) leaves the agent isolated, so only clear on merged==true.
	var res struct {
		Merged bool `json:"merged"`
	}
	if jerr := json.Unmarshal(out, &res); jerr == nil && res.Merged {
		if a, ok := f.ws.Agent(id); ok {
			a.Isolated, a.Branch, a.TargetBranch = false, "", ""
			if perr := f.ws.PutAgent(a); perr != nil {
				return nil, fmt.Errorf("agent merged but recording it failed: %w", perr)
			}
		}
	}
	return out, nil
}

func (f *Fleet) Discard(ctx context.Context, id string) error {
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		return err
	}
	if _, rerr := rt.child.Request(ctx, "session/discard", map[string]any{"sessionId": f.sessionIDFor(rt)}); rerr != nil {
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
	runtimeErrors := make(map[string]error)
	for _, rt := range f.runtimes {
		if rt.spawnErr != nil {
			runtimeErrors[rt.root] = rt.spawnErr
		}
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

// reconcileOnce asks a project to prune stale git metadata and report
// worktrees no agent claims, recording the result on ProjectStatus. It runs
// at most once per project per bridge lifetime, so a cold project is
// reconciled the first time it is used without re-pruning on every spawn.
func (f *Fleet) reconcileOnce(ctx context.Context, root string) {
	f.mu.Lock()
	if f.reconciled[root] {
		f.mu.Unlock()
		return
	}
	f.reconciled[root] = true
	f.mu.Unlock()

	// Find any live runtime for this project root.
	f.mu.Lock()
	var rt *agentRuntime
	for _, candidate := range f.runtimes {
		if candidate.root == root {
			rt = candidate
			break
		}
	}
	f.mu.Unlock()
	if rt == nil {
		return
	}
	cwd, cerr := rt.agentPath(root)
	if cerr != nil {
		slog.Default().Warn("webbridge: skip worktree prune", "project", root, "err", cerr)
		return
	}
	raw, rerr := rt.child.Request(ctx, "session/worktree_prune", map[string]any{"cwd": cwd})
	if rerr != nil {
		slog.Default().Warn("webbridge: worktree prune failed", "project", root, "err", rerr)
		return
	}
	var out struct {
		Unknown []string `json:"unknown"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return
	}
	f.mu.Lock()
	f.orphans[root] = out.Unknown
	f.mu.Unlock()
}

// ReconcileWorktrees asks each live project to prune stale git metadata and
// report worktrees no agent claims, recording the result on ProjectStatus.
//
// Only projects whose child is already up are asked: bringing every project
// up at startup just to prune would defeat lazy spawning. Cold projects are
// reconciled the first time they are used (see reconcileOnce).
func (f *Fleet) ReconcileWorktrees(ctx context.Context) {
	f.mu.Lock()
	roots := make(map[string]bool)
	for _, rt := range f.runtimes {
		if rt.spawnErr == nil {
			roots[rt.root] = true
		}
	}
	f.mu.Unlock()

	for root := range roots {
		f.reconcileOnce(ctx, root)
	}
}

const fleetStreamKey = "fleet"

func (f *Fleet) attachClassifier(rt *agentRuntime) {
	prev := rt.child.OnNotification
	rt.child.OnNotification = func(method string, params json.RawMessage) {
		if prev != nil {
			prev(method, params)
		}
		if d, ok := classifyNotification(method, params); ok {
			// The live state and fleet log are keyed by agent id, but
			// notifications carry the ACP session id. Each runtime owns
			// exactly one session, so the agent id is rt.id.
			d.SessionID = rt.id
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
			f.live.observePending(rt.id, p)
			_, _ = f.fleetLog.Append(fleetStreamKey, fleetDelta{
				Kind: "pending", SessionID: rt.id, PendingKind: p.kind,
			})
		}
	}
}

func (f *Fleet) Snapshot() []AgentStatus {
	out := make([]AgentStatus, 0)
	for _, a := range f.ws.Agents() {
		live := f.live.get(a.ID)
		st := AgentStatus{
			ID: a.ID, Project: a.Project, Name: a.Name, Mode: a.Mode,
			Status: "idle", Activity: live.activity, ContextPct: live.contextPct,
			ChangedFiles: live.changedFiles, Interrupted: a.Interrupted,
			Isolated: a.Isolated, Branch: a.Branch, UpdatedAt: live.updatedAt,
			SourceKind: a.SourceKind, ReadOnly: a.ReadOnly,
			TargetBranch: a.TargetBranch, PRUrl: a.PRUrl,
			GateOverride: a.GateOverride,
		}
		if !a.PushedAt.IsZero() {
			st.PushedAt = &a.PushedAt
		}
		if live.mode != "" {
			st.Mode = live.mode
		}
		if a.CreatedAt.After(st.UpdatedAt) {
			st.UpdatedAt = a.CreatedAt
		}
		if rt, err := f.runtimeForAgent(a.ID); err == nil {
			sid := f.sessionIDFor(rt)
			// Registry.Pending is the authority on whether anything is
			// still outstanding; live.pending only supplies the payload.
			// A resolved request therefore stops being advertised even
			// though its payload is still cached.
			pending := rt.reg.Pending(sid)
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
				if info, ok := rt.reg.lookup(sid); ok && info.Busy {
					st.Status = "running"
				}
			}
		} else if !errors.Is(err, ErrUnknownAgent) {
			st.Status = "error"
		}
		out = append(out, st)
	}
	return out
}

// stopAgent stops one agent's child and drops its runtime. The Agent
// record is left in the workspace; removing it is the caller's choice.
func (f *Fleet) stopAgent(id string) {
	f.mu.Lock()
	rt := f.runtimes[id]
	delete(f.runtimes, id)
	if rt != nil && rt.sessionID != "" {
		delete(f.sessionAgent, rt.sessionID)
	}
	f.mu.Unlock()
	if rt != nil {
		// Stop the child first so it is no longer writing to the
		// bind-mounted workspace, then remove the git-sourced tree.
		rt.child.Stop()
		if rt.sourceKind == "git" && f.git != nil {
			if err := f.git.RemoveTree(f.stateDir, id); err != nil {
				slog.Default().Warn("webbridge: remove agent workspace failed",
					"agent", id, "err", err)
			}
		}
		f.slots.release()
	}
}

// restoreSession reattaches the bridge to the ACP session already
// running inside a reattached container. An agent with no persisted
// session id (a pre-v2 record) is left addressable by agent id only.
func (f *Fleet) restoreSession(ctx context.Context, rt *agentRuntime, a Agent) error {
	if a.SessionID == "" {
		return nil
	}
	cwd, cerr := rt.agentPath(a.Project)
	if cerr != nil {
		return fmt.Errorf("resolve cwd for agent %s: %w", a.ID, cerr)
	}
	if err := rt.reg.Load(ctx, cwd, a.SessionID); err != nil {
		return fmt.Errorf("restore session %s for agent %s: %w", a.SessionID, a.ID, err)
	}
	f.mu.Lock()
	rt.sessionID = a.SessionID
	f.sessionAgent[a.SessionID] = a.ID
	f.mu.Unlock()
	return nil
}

// ReattachAll reconnects to every persisted agent that is still running.
// It is called once at start-up: an agent kept working while the control
// plane was down, and respawning it would duplicate its work.
//
// Errors are collected rather than returned on the first failure — one
// unreachable agent must not prevent the rest from reattaching.
func (f *Fleet) ReattachAll(ctx context.Context) []error {
	var errs []error
	for _, a := range f.ws.Agents() {
		if _, err := f.runtimeForAgent(a.ID); err == nil {
			continue // already live
		}
		if err := f.slots.acquire(ctx); err != nil {
			errs = append(errs, fmt.Errorf("agent %s: %w", a.ID, err))
			continue
		}
		rt, err := f.startRuntime(ctx, a)
		if err != nil {
			f.stopAgent(a.ID) // removes the failed runtime and releases the slot
			errs = append(errs, fmt.Errorf("reattach agent %s: %w", a.ID, err))
			continue
		}
		if err := f.restoreSession(ctx, rt, a); err != nil {
			f.stopAgent(a.ID)
			errs = append(errs, err)
		}
	}
	return errs
}

// Pause stops an agent's container while keeping its workspace volume and
// its persisted record. The agent can be resumed later onto the same work.
func (f *Fleet) Pause(id string) error {
	if _, err := f.runtimeForAgent(id); err != nil {
		return err
	}
	f.stopAgent(id)
	return nil
}

// Resume restarts a paused agent against its existing workspace.
func (f *Fleet) Resume(ctx context.Context, id string) error {
	if _, err := f.runtimeForAgent(id); err == nil {
		return nil // already running
	}
	a, ok := f.ws.Agent(id)
	if !ok {
		return fmt.Errorf("%w: agent %s", ErrUnknownAgent, id)
	}
	if err := f.slots.acquire(ctx); err != nil {
		return fmt.Errorf("wait for an agent slot: %w", err)
	}
	rt, err := f.startRuntime(ctx, a)
	if err != nil {
		f.slots.release()
		return err
	}
	if err := f.restoreSession(ctx, rt, a); err != nil {
		f.stopAgent(id)
		return err
	}
	return nil
}

func (f *Fleet) StopProject(root string) {
	f.mu.Lock()
	var toStop []string
	var removed []string
	for id, rt := range f.runtimes {
		if rt.root == root {
			toStop = append(toStop, id)
			if rt.sessionID != "" {
				removed = append(removed, rt.sessionID)
			}
		}
	}
	f.mu.Unlock()
	for _, id := range toStop {
		f.stopAgent(id)
	}
	f.live.removeProject(removed)
}

func (f *Fleet) Close() {
	f.closeOnce.Do(func() { close(f.done) })
	f.mu.Lock()
	rts := make([]*agentRuntime, 0, len(f.runtimes))
	for _, rt := range f.runtimes {
		rts = append(rts, rt)
	}
	f.runtimes = make(map[string]*agentRuntime)
	f.sessionAgent = make(map[string]string)
	f.mu.Unlock()

	var wg sync.WaitGroup
	for _, rt := range rts {
		wg.Add(1)
		go func(rt *agentRuntime) { defer wg.Done(); rt.child.Stop() }(rt)
	}
	wg.Wait()
}
