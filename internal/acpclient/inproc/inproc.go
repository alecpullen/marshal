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
	"marshal/internal/app/session"
	"marshal/internal/interaction"
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

// Swap closes the current session and returns a new one over state. It serves
// today's swap path; plan 5 replaces it with Host.NewSession and Host.Resume.
func (h *Host) Swap(state *session.State) *Session {
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
	status.WatchResume = s.watchResume()
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

// interactionOf is s on the wire. Status uses it too.
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
