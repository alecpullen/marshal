// Package inproc is the in-process adapter for the client seam (spec C4).
//
// It implements acpclient.Host and acpclient.Session over the runtime the TUI
// already holds, so the TUI can reach the agent side through the interfaces
// before the pipe exists. P3 deletes this package once the TUI talks to a
// separate process.
//
// It must not import internal/app: TUI tests import this package, and app
// imports tui, so an import back into app would be a cycle.
package inproc

import (
	"context"
	"sync"
	"sync/atomic"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/interaction"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/schema"
	"marshal/internal/pubsub"
	"marshal/internal/repo/gitinfo"
	"marshal/internal/tools/native"
	"marshal/internal/viewmodel"
	"marshal/internal/watch"
)

// Deps is what the adapter serves. Each P2 plan adds its group's
// dependencies (C4).
type Deps struct {
	State *session.State
	// Context bounds the session's event subscriptions. Nil means
	// context.Background().
	Context context.Context
	// The runtime's brokers. A nil broker is not subscribed.
	Jobs      *pubsub.Broker[native.JobEvent]
	Watches   *pubsub.Broker[watch.Event]
	Steering  *pubsub.Broker[session.SteeringEvent]
	Workspace *pubsub.Broker[session.WorkspaceEvent]
	Subagents *pubsub.Broker[session.SubagentEvent]
	// GitInfo reads the git info for a directory. Nil means gitinfo.Read.
	GitInfo func(dir string) gitinfo.Info
	// ProjectID is the project's database ID, for the sheet's turn metrics
	// and repo counts. The database itself is the state's.
	ProjectID int64
	// ReferenceFinder answers Callers. Nil means no callers lookups.
	ReferenceFinder ReferenceFinder

	// AccessRequest and ResolveAccess are the runtime's access seam:
	// AccessRequest returns the typed access request waiting for a decision,
	// settling one that asks for nothing, and ResolveAccess validates and
	// settles a choice. Without both, ReconcileAccess settles every typed
	// access request as failed instead of presenting it.
	AccessRequest func() (session.AccessRequest, bool)
	ResolveAccess func(session.AccessChoice) (session.AccessResult, error)

	// Work is the runtime's work service. Nil means the session serves no
	// work: Work reports false, and the work calls fail.
	Work viewmodel.WorkService

	// SetInteraction and SetEffort are the runtime's interaction and effort
	// setters. Each reads the runtime's current state, so the same Deps keep
	// working after a swap. Without one, the matching Session call fails.
	SetInteraction func(interaction.Patch) (interaction.State, error)
	SetEffort      func(level string) (applies string, err error)

	// Runner returns the runtime's current agent runner, or nil while the
	// provider is not built. A nil func means the session never has one.
	Runner func() Runner
	// Review, RunAgent, Suggest and VerifyProposal are the runtime's calls.
	// Each reads the runtime's current state, so the same Deps keep working
	// after a swap. Without one, the matching Session call fails.
	Review         func(ctx context.Context, params acpwire.ReviewParams) error
	RunAgent       func(ctx context.Context, params acpwire.AgentRunParams) error
	Suggest        func(ctx context.Context, lastMessage string) (string, error)
	VerifyProposal func(ctx context.Context) (acpwire.VerifyCommands, error)

	// Swarm, NewPlanRun, PlanPreflight and AuthorPlan are the runtime's
	// pipeline calls. Each reads the runtime's current orchestrator or
	// factory, so the same Deps keep working after a swap and a reload.
	// Without one, the matching Session call refuses with CodeNoRunner.
	Swarm         func(ctx context.Context, params acpwire.SwarmParams) error
	NewPlanRun    func(params acpwire.SDDParams) (PlanRun, error)
	PlanPreflight func(ctx context.Context, params acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error)
	AuthorPlan    func(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error)

	// End is the runtime's end service. Without it the session has no work to
	// warn about: ActiveWork is empty, AcceptEnd accepts, WaitForEnd settles
	// at once, and RunExitAnalysis refuses with CodeNoRunner.
	End EndService
	// Commands is the runtime's slash-command registry. Without it the session
	// lists no commands and runs none.
	Commands *commands.Registry
	// NewSession creates a session in the runtime and returns its state.
	// Without it, Host.NewSession fails.
	NewSession func(name string) (*session.State, error)

	// Auth is the runtime's credential and OAuth dependency: every provider
	// client and sign-in the adapter builds uses it. Nil means
	// provider.DefaultAuthResolver(), the substitution tui.New used to make.
	Auth provider.AuthResolver
	// ModelCacheDir is the data directory of the model cache and of the
	// clients the probes build, and where preset setup reads the models.dev
	// catalog and the provenance sidecar. Empty means no cache: ListModels
	// finds nothing, and the other cache calls do nothing.
	ModelCacheDir string

	// ReloadConfig is the runtime's config reloader: it rebuilds the runtime
	// from cfg. Nil means the session has no reload path: ReloadConfig and
	// ReconnectMCP report false and change nothing.
	ReloadConfig func(cfg config.Config) error
	// ReloadLayers re-reads the config layers from disk. False means the read
	// failed, and the session keeps the layers it has. Nil means a write
	// refreshes no layers.
	ReloadLayers func() (config.Layers, bool)
	// TrustRefresh advances the permanent-trust record's config hash for dir
	// after a project save. Nil means no hash is refreshed.
	TrustRefresh func(dir string)
	// HomeDir is the home directory the user config is found under. Empty
	// means os.UserHomeDir().
	HomeDir string
	// UserConfigPath is the user config file. Empty means
	// config.UserConfigPath of the home directory.
	UserConfigPath string
}

// ReferenceFinder resolves what references a symbol. ok is false when no
// language server is ready for the file's language. lsp.SymbolQuery
// satisfies it.
type ReferenceFinder interface {
	References(ctx context.Context, filePath string, line, col int) ([]string, bool)
}

// Host is the in-process transport: it owns the current session and hands out
// a new one on a swap.
type Host struct {
	mu      sync.Mutex
	deps    Deps
	current *Session
}

// Session is one conversation over a session.State.
type Session struct {
	state   *session.State
	gitInfo func(dir string) gitinfo.Info
	closed  atomic.Bool

	// projectID is the project's database ID, for the sheet's turn metrics
	// and repo counts.
	projectID int64
	// headSHA reads a directory's HEAD. Replaceable so the package's tests
	// can move the workspace mid-read.
	headSHA func(dir string) string

	// refs answers Callers. Nil means no callers lookups.
	refs ReferenceFinder

	// accessRequest and resolveAccess are the runtime's access seam. Without
	// both, ReconcileAccess settles every typed access request as failed
	// instead of presenting it.
	accessRequest func() (session.AccessRequest, bool)
	resolveAccess func(session.AccessChoice) (session.AccessResult, error)

	// work is Deps.Work. workSnap is the newest work snapshot the session
	// kept, and workConv the conversation its seed bound. workSnap and
	// workConv are guarded by mu.
	work     viewmodel.WorkService
	workSnap acpwire.WorkSnapshot
	workConv string

	// setInteraction and setEffort are the runtime's setters. Nil means the
	// matching call fails.
	setInteraction func(interaction.Patch) (interaction.State, error)
	setEffort      func(string) (string, error)

	// runner, review, runAgent, suggest and verifyProposal are the runtime's
	// calls. Nil means the matching call fails.
	runner         func() Runner
	review         func(context.Context, acpwire.ReviewParams) error
	runAgent       func(context.Context, acpwire.AgentRunParams) error
	suggest        func(context.Context, string) (string, error)
	verifyProposal func(context.Context) (acpwire.VerifyCommands, error)
	swarm          func(context.Context, acpwire.SwarmParams) error
	newPlanRun     func(acpwire.SDDParams) (PlanRun, error)
	planPreflight  func(context.Context, acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error)
	authorPlan     func(context.Context, acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error)
	// end is the runtime's end service. Nil means the session has no work to
	// warn about.
	end EndService
	// paused is the plan run its human gate paused, nil when none. It is
	// guarded by mu.
	paused PlanRun
	// turn is the run in flight, nil when none. It is guarded by mu.
	turn *turnSlot

	// cmds is the runtime's slash-command registry. Nil means the session
	// lists no commands and runs none.
	cmds *commands.Registry
	// auth is the runtime's credential and OAuth dependency: Deps.Auth, or
	// provider.DefaultAuthResolver() when the caller supplied none. Never nil.
	auth provider.AuthResolver
	// reloadConfig, reloadLayers, trustRefresh, homeDir and userConfig are the
	// runtime's config-write dependencies: Deps.ReloadConfig, Deps.ReloadLayers,
	// Deps.TrustRefresh, Deps.HomeDir and Deps.UserConfigPath.
	reloadConfig   func(cfg config.Config) error
	reloadLayers   func() (config.Layers, bool)
	trustRefresh   func(dir string)
	homeDir        string
	userConfigFile string
	// modelCacheDir is Deps.ModelCacheDir, for preset setup's catalog and
	// provenance sidecar.
	modelCacheDir string
	// actions holds the row actions of the most recent results that minted
	// any, by ID; actionResults lists each such result's IDs, oldest first;
	// actionSeq numbers them. All three are guarded by mu.
	actions       map[string]func(*session.State) commands.Result
	actionResults [][]string
	actionSeq     uint64

	// ctx bounds the event subscriptions; cancel releases them.
	ctx    context.Context
	cancel context.CancelFunc

	// mu guards the signal channel, the values the event loop keeps and the
	// sheet cache.
	mu            sync.Mutex
	changed       chan struct{}
	changedClosed bool
	receipts      map[string]session.SteeringReceipt
	resume        *acpwire.WatchResume
	resumeSeq     uint64
	sheet         sheetCache
	// localKeys holds the minted key of each open unregistered prompt, by the
	// prompt's pointer; localSeq numbers them. Both are guarded by mu.
	localKeys map[any]string
	localSeq  uint64
}

var (
	_ acpclient.Host    = (*Host)(nil)
	_ acpclient.Session = (*Session)(nil)
)

// errSessionClosed is what a session a swap replaced answers with (spec C4).
var errSessionClosed = &acpwire.Error{
	Code:    acpwire.CodeSessionClosed,
	Message: "inproc: the session was replaced",
}

// New builds the host and the session over d.State.
func New(d Deps) (*Host, *Session) {
	sess := newSession(d)
	return &Host{deps: d, current: sess}, sess
}

// swap closes the current session and returns a new one over state. It is the
// host's own swap path: NewSession is what callers use, and it reaches this
// after the runtime has created the state.
//
// The close is deliberately minimal — it marks the session closed, cancels its
// run and its change signal — because the runtime has already quiesced and shut
// the replaced state down before it builds the new one. Deps.NewSession must
// keep that promise: a factory that leaves the old state alive would leave the
// swapped-out session's pending requests open.
func (h *Host) swap(state *session.State) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current != nil {
		h.current.close()
	}
	d := h.deps
	d.State = state
	sess := newSession(d)
	h.current = sess
	return sess
}

// SharedFilesystem reports whether a path the agent names is also a path on
// this machine. The in-process adapter runs in the same process, so it does.
func (h *Host) SharedFilesystem() bool { return true }

// AuthResolver returns the resolver the adapter builds with: Deps.Auth, or the
// production default when it was nil. Never nil. It exists for app's wiring
// test and goes with the adapter in P3.
func (h *Host) AuthResolver() provider.AuthResolver {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.deps.Auth != nil {
		return h.deps.Auth
	}
	return provider.DefaultAuthResolver()
}

// ID is the session's ID. A closed session keeps answering with its final
// value (C4), which holds because its state never changes.
func (s *Session) ID() string { return s.state.SessionID() }

// Status is the session's current status (C5). It reads the state one field at
// a time, as the TUI read them before, so the value is not one atomic
// snapshot. A closed session keeps answering with its final value (C4).
func (s *Session) Status() acpwire.SessionStatus {
	st := s.state
	used, window := st.TurnUsage()
	effort := st.EffortOverride()
	access := st.InteractionState()
	act := st.Activity()
	ws := st.Workspace()
	bi := st.BrowserInfo()
	sb := st.SandboxInfo()
	gate := st.SDDGate()
	// An empty queue encodes as absent under omitempty, so keep it nil rather
	// than the non-nil empty slice SteeringQueue returns.
	queue := st.SteeringQueue()
	if len(queue) == 0 {
		queue = nil
	}
	snap := st.SteeringSnapshot()

	status := acpwire.SessionStatus{
		WorkingDir:    st.WorkingDir,
		GenerationSeq: st.Generation().Seq,
		LeafID:        st.LeafID(),
		Branches:      st.Branches(),
		ActiveRoute:   routeOf(st.ActiveRoute()),
		TurnUsed:      used,
		TurnWindow:    window,
		Quota:         quotaOf(st.TurnQuota()),
		Effort: acpwire.Effort{
			Level:    effort.Level,
			Provider: effort.Provider,
			Model:    effort.Model,
		},
		Interaction:     interactionOf(access),
		SudoUnlock:      st.SudoUnlockEnabled(),
		Trusted:         st.Trusted(),
		HasBackup:       st.HasBackup(),
		Activity:        activityWire(act),
		SubagentRunning: st.HasRunningSubagent(),
		AgentReady:      s.currentRunner() != nil,
		Workspace: acpwire.Workspace{
			ProjectRoot:  ws.ProjectRoot,
			ActiveRoot:   ws.ActiveRoot,
			Branch:       ws.Branch,
			BaseSha:      ws.BaseSha,
			TargetBranch: ws.TargetBranch,
		},
		Browser: acpwire.Browser{
			Active:      bi.Active,
			ToolName:    bi.ToolName,
			URL:         bi.URL,
			Title:       bi.Title,
			Mode:        bi.Mode,
			SessionOpen: bi.SessionOpen,
			UpdatedAt:   acpwire.Millis(bi.UpdatedAt),
		},
		Sandbox: acpwire.Sandbox{
			Backend:          sb.Backend,
			NetworkIsolation: sb.NetworkIsolation,
			AllowNetwork:     st.ConfigSnapshot().Tools.Shell.AllowNetwork,
		},
		Steering: acpwire.Steering{
			Queue: queue,
			State: viewmodel.SteeringStateOf(snap),
		},
		RunningJobs: st.RunningJobsCount(),
		Subagents:   subagentsOf(st.Subagents()),
		Swarm:       swarmOf(st.SwarmProgress()),
		SDD:         sddOf(st.SDDProgress()),
		SDDGate: acpwire.SDDGate{
			TaskN:     gate.TaskN,
			Question:  gate.Question,
			TaskTitle: gate.TaskTitle,
			Report:    gate.Report,
		},
		Git: gitOf(s.gitInfo(ws.ActiveRoot)),
		// The count the status line shows. The session refreshes it on turn
		// boundaries and workspace moves, never during render.
		ChangedFiles: s.changedFiles(),
	}
	if r, ok := st.TurnRoute(); ok {
		route := routeOf(r)
		status.TurnRoute = &route
	}
	if rec, ok := s.latestReceipt(snap.TurnID); ok {
		receipt := viewmodel.SteeringReceiptOf(rec)
		status.Steering.Receipt = &receipt
	}
	if notice, ok := st.Notice(); ok {
		n := noticeWire(notice)
		status.Notice = &n
	}
	status.WatchResume = s.watchResume()
	// The two files a config write changes, when the agent can locate them. A
	// home it cannot find leaves the user path empty rather than failing a
	// read that never blocks.
	if path, err := userConfigPath(Deps{HomeDir: s.homeDir, UserConfigPath: s.userConfigFile}); err == nil {
		status.UserConfigPath = path
	}
	if path := s.projectConfigPath(); path != "" {
		status.ProjectConfigPath = path
	}
	return status
}

// SendNow submits one Send now request: its text goes to the running turn at
// the next safe boundary, interrupting the operation in flight. The receipt is
// in Status before SendNow returns.
//
// Acceptance is a bounded, memory-only call, so ctx is not consulted. A
// refused request comes back unchanged, so the TUI shows the state's text as it
// always has.
func (s *Session) SendNow(ctx context.Context, req acpwire.SendNowRequest) (acpwire.SteeringReceipt, error) {
	if s.closed.Load() {
		return acpwire.SteeringReceipt{}, errSessionClosed
	}
	receipt, err := s.state.SubmitSendNow(session.SendNowRequest{
		SessionID: req.SessionID,
		TurnID:    req.TurnID,
		RequestID: req.RequestID,
		Text:      req.Text,
	})
	if err != nil {
		return acpwire.SteeringReceipt{}, err
	}
	// A swap can land between the check above and the submission. The receipt
	// would then belong to a session that no longer exists, so refuse it rather
	// than keep it and signal a channel that is already closed.
	if s.closed.Load() {
		return acpwire.SteeringReceipt{}, errSessionClosed
	}
	s.absorbReceipt(receipt)
	s.signal()
	return viewmodel.SteeringReceiptOf(receipt), nil
}

// interactionOf copies an interaction state onto the wire. Status uses it
// too, so the two never disagree about which fields cross the seam.
func interactionOf(s interaction.State) acpwire.Interaction {
	return acpwire.Interaction{
		Mode:     string(s.Mode),
		Auto:     s.Auto,
		System:   s.System,
		Revision: s.Revision,
	}
}

// routeOf copies a session route onto the wire.
func routeOf(r session.RouteInfo) acpwire.Route {
	return acpwire.Route{
		Role:             string(r.Role),
		Profile:          r.Profile,
		Preset:           r.Preset,
		Provider:         r.Provider,
		Model:            r.Model,
		LocalOnly:        r.LocalOnly,
		Thinking:         r.Thinking,
		ReasoningCapable: r.ReasoningCapable,
		Active:           r.Active,
	}
}

// quotaOf copies the provider's quota onto the wire, converting the seconds
// and minutes it reports into milliseconds. A nil quota stays nil.
func quotaOf(q *schema.QuotaInfo) *acpwire.Quota {
	if q == nil {
		return nil
	}
	return &acpwire.Quota{
		PlanType:              q.PlanType,
		PrimaryUsedPercent:    q.PrimaryUsedPercent,
		PrimaryResetAfterMs:   int64(q.PrimaryResetAfterSecs) * 1000,
		PrimaryWindowMs:       int64(q.PrimaryWindowMinutes) * 60000,
		SecondaryUsedPercent:  q.SecondaryUsedPercent,
		SecondaryResetAfterMs: int64(q.SecondaryResetAfterSecs) * 1000,
		SecondaryWindowMs:     int64(q.SecondaryWindowMinutes) * 60000,
	}
}
