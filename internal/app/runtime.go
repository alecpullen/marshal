package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/hooks"
	"marshal/internal/index"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/lsp"
	"marshal/internal/progress"
	"marshal/internal/pubsub"
	"marshal/internal/repo"
	"marshal/internal/sddauthor"
	"marshal/internal/skills"
	"marshal/internal/strutil"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/trust"
	"marshal/internal/viewmodel"
	"marshal/internal/watch"
)

// jobShutdownTimeout controls how long Quiesce waits for background jobs to
// drain. Package-level so tests can override it without sleeping five seconds.
var jobShutdownTimeout = 5 * time.Second

// snapshotCloseTimeout bounds how long Close waits for snapshot work to be
// cancelled and joined, and how long the store's own status probe may take.
// Both are small on purpose: shutdown must do NO heavy storage work, so this
// budget only has to cover joining an in-flight capture or a bounded read-only
// status query.
var snapshotCloseTimeout = 5 * time.Second

// MCPCloser closes an MCP manager during Runtime.Close.
type MCPCloser interface {
	Close() error
}

// BrokerCloser closes a pubsub broker during Runtime.Close.
type BrokerCloser interface {
	Close()
}

// SnapshotCloser is the snapshot service's lifecycle on the shutdown path.
//
// It deliberately has NO prune method. The lifecycle it replaces ran a
// filesystem prune and then `git gc --prune=now` on every shutdown; the gc
// could not free anything (snapshots were parent-chained and reachable from
// HEAD), and an interrupted repack left 171 GiB of tmp_pack_* files behind.
// Shutdown now performs NO heavy storage work: it cancels and joins bounded
// snapshot work only. Maintenance runs at startup and before admission, where
// it is bounded and observable.
type SnapshotCloser interface {
	// CloseSnapshots cancels and joins any in-flight snapshot work. It must
	// not reconcile, reclaim, repack, or take the store lock for a sweep.
	CloseSnapshots(ctx context.Context) error
}

// SnapshotStatusCloser is the optional extension of SnapshotCloser for a
// snapshot service that can report its own health. It lets Runtime.Close
// surface a structured maintenance failure through the same user-visible
// warning the agent's capture hooks use, instead of only logging it.
//
// Status must be read-only and cheap: it runs at shutdown, so it may measure
// and list but must never reconcile or reclaim.
type SnapshotStatusCloser interface {
	SnapshotCloser
	// Status returns a non-nil error when the store needs attention, with a
	// message carrying the snapshot store's structured reason.
	Status(ctx context.Context) error
}

// maintenanceSnapshotCloser is the interface Close actually calls for the
// close-time status probe. It is separate from SnapshotStatusCloser only so a
// service that reports health WITHOUT a lifecycle method still works.
type maintenanceSnapshotCloser interface {
	Status(ctx context.Context) error
}

// DBCloser closes a database and prunes old snapshots during Runtime.Close.
type DBCloser interface {
	Close() error
	PruneSnapshotsOlderThan(days int) error
}

// Runtime is the headless application state shared between the TUI and any
// other transport (e.g. ACP). It owns configuration, the database, the
// session, the agent runner, and the supporting brokers/snapshots that
// survive for the duration of a session. A Runtime created by StartRuntime
// must be torn down with Close exactly once when the consumer is finished.
type Runtime struct {
	Config       config.Config
	Layers       config.Layers
	State        *session.State
	Runner       *agent.Runner
	ToolRegistry *registry.Registry
	SwarmRunner  *swarm.Orchestrator
	// PipelineFactory builds a plan-execution runner for one plan file.
	// The overrides map carries per-run role→preset overrides from the
	// castlist; nil when no overrides are set.
	PipelineFactory func(planPath string, overrides map[routing.AgentRole]string) tui.AgentRunner
	// SwarmOverrideFactory builds a per-run swarm.RunnerFactory with
	// role→preset overrides applied. Nil when the runtime has no provider.
	SwarmOverrideFactory tui.SwarmOverrideFactory
	// PlanAuthorFactory builds a scoped SDD plan-authoring runner for one
	// request. Set by startRuntime and replaced on config reload.
	PlanAuthorFactory sddauthor.Factory
	DB                DBCloser
	ProjectID         int64
	SessionID         string
	JobBroker         BrokerCloser
	WatchBroker       BrokerCloser
	SteeringBroker    BrokerCloser
	EventBroker       BrokerCloser
	WorkspaceBroker   BrokerCloser
	SubagentBroker    BrokerCloser
	IndexBroker       BrokerCloser
	MCPManager        MCPCloser
	Snapshot          SnapshotCloser
	Logger            *slog.Logger
	WorkingDir        string
	HomeDir           string
	DataDir           string
	SkillIndex        *skills.Index
	// PluginCommands are slash commands contributed by verified plugins.
	// Run() registers them into the TUI command registry; headless
	// sessions ignore them.
	PluginCommands []commands.Command
	// CommandRegistry holds every built-in command plus PluginCommands,
	// shared by the TUI and any headless transport (ACP). Built once here
	// so there is a single source of truth for command registration.
	CommandRegistry *commands.Registry
	// CommandService is the conversation's command supervisor: the one
	// service every ordinary command in this conversation is admitted
	// through, from admission to settlement, and the target an explicit turn
	// stop reaches (via the session's registered TurnStopService).
	//
	// It survives a config reload and a replacement session: a rebuild
	// BORROWS it and commits new launch defaults for future work, so
	// already-admitted commands, their handles and their identities are never
	// repointed or closed by a rebuild. JobManager is the same supervisor's
	// handle for callers that only need admission and status.
	CommandService *conversationCommandService
	JobManager     *native.JobManager
	WatchManager   *watch.Manager
	DesktopCloser  func()
	// SnapshotWarnings carries the runtime's snapshot-warning suppression
	// state. It is shared with the agent runner (Runner.SetSnapshotWarningState)
	// so an in-turn capture warning and a runtime maintenance warning about the
	// same workspace and reason are one event, and so a later successful
	// capture clears whichever one was showing. Never nil for a Runtime built
	// by startRuntime.
	SnapshotWarnings *agent.SnapshotWarningState

	// WatchResume is the settable auto-resume hook invoked by the watch
	// manager's OnFire closure after each fired report is queued. The ACP
	// host binds it per session; the TUI never sets it, so TUI behavior
	// is unchanged. Survives config reload (reloadAgentRuntime passes the
	// same cell); cleared by Close.
	WatchResume *WatchResumeHook

	// TrustPromptPending reports that a project config exists but was not
	// applied because the trust question was deferred to the TUI.
	TrustPromptPending bool

	// LSPManager is the runtime's one LSP handle, created in startRuntime
	// and kept for the runtime's whole life. bindLSP points it at the host's
	// pooled manager for the session's active root and server set (nil when
	// none applies), so tools, the diagnostics checker and the TUI's
	// reference finder never need rewiring.
	LSPManager *lsp.Handle
	// Host is the project host this runtime's session runs on: the
	// project's database setup, index and pooled language servers, shared
	// with the process's other sessions on the same root (see Hosts).
	Host *ProjectHost
	// lspMu serializes bindLSP; lspLease is the pooled manager LSPManager
	// points at, released on the next bind or at Close. lspClosed is set by
	// Close, so a WorkspaceEvent bind still in flight cannot rebind after it.
	lspMu     sync.Mutex
	lspLease  *lspLease
	lspClosed bool

	// reloadMu serializes whole-runtime config reloads. A reload rebuilds the
	// runner, the MCP manager, the registries, and the cleanup generation;
	// those phases are not individually mutex-guarded, so two interleaving
	// reloads would leave the runtime with mismatched generations even
	// though each individual pointer swap is atomic. Reloads arrive from
	// more than one goroutine — the TUI's /connect and /settings paths, the
	// agent's own config.*.set tools mid-turn, and the headless /mcp auth
	// completion callback (internal/acp/mcpauth.go) — so this gate is what
	// makes concurrent reloads safe.
	//
	// Lock order: reloadMu is always taken ABOVE Runtime.mu and never below
	// it, and a reload never re-enters itself, so holding it for a whole
	// reload cannot deadlock. The stacking to keep honest is the write gate:
	// a turn's config.* tool holds swarm.WriteLock and then calls the
	// reloader, so the reload build must NEVER acquire the write gate — it
	// does not today, and adding such an acquisition would deadlock against
	// that turn. Holding reloadMu across the old-generation cleanup can also
	// park a concurrent config.* tool for the duration of the bounded (2s)
	// manager shutdowns; that is acceptable, but worth remembering.
	reloadMu sync.Mutex

	// CustomAgentFactory builds a one-shot *agent.Runner for a named custom
	// agent. Used by the TUI's Run-now dispatch. Set by startRuntime.
	CustomAgentFactory agent.SubagentRunnerFactory
	// ConfigReloader hot-swaps the agent runtime from a new config. Run()
	// installs one after the TUI is live; startRuntime installs a default for
	// every other runtime (StartRuntime / ACP / CLI / embedded), so a headless
	// runtime always has a working reload handle. Reloads are serialized by
	// reloadMu. Note that a reload mutates the live *agent.Runner in place via
	// Runner.CopyFrom, so a turn already in flight keeps reading that pointer
	// mid-swap — the posture the TUI has always had, deliberately accepted
	// rather than fixed here (see docs/mcp-oauth-verification.md).
	ConfigReloader func(config.Config) error
	// AuthResolver is the credential/OAuth dependency this runtime selected
	// (WithAuthResolver, or provider.DefaultAuthResolver() when omitted). It
	// is resolved once at the outer construction boundary and retained here so
	// every rebuild — reloadAgentRuntime, NewSession, and the Run
	// resume/restart loop — hands the SAME resolver to provider construction,
	// the refresh workers and the MCP managers. Never nil for a Runtime built
	// by startRuntime, including the inactive-provider startup path.
	AuthResolver provider.AuthResolver
	// WriteLock is the single swarm.WriteLock shared by the parent runner
	// and every agent.run child. It is created once at startRuntime and
	// reused across config reloads so in-flight background children (which
	// hold the lock from the pre-reload generation) keep serializing with
	// the post-reload parent on the same gate. A reload that minted a fresh
	// lock would let a pre-reload child and the post-reload parent write
	// concurrently.
	WriteLock      *swarm.WriteLock
	additionalDirs []string

	// work is this conversation's private work projection and action facade.
	// It is captured under mu for the duration of one call and never read
	// dynamically from the collectors. nil until a runtime wires it.
	work *workCoordinator

	workCtx    context.Context
	workCancel context.CancelFunc
	// snapshotMaintenanceDone is closed when the startup snapshot sweep
	// finishes; nil when none was started. Quiesce waits on it.
	snapshotMaintenanceDone chan struct{}
	mu                      sync.Mutex
	// resourceClosers holds cleanup functions (log file, DB handle, open file
	// descriptors) appended in setup order. They are invoked in reverse during
	// Close, so the most recently opened resource is released first.
	resourceClosers []func()

	quiesceOnce sync.Once
	closeOnce   sync.Once
	quiesceErr  error
	closeErr    error

	// endMu serializes whole end operations: acceptance, the stop/join pass,
	// the postmortem window and closure. It is deliberately separate from mu
	// (which only guards pointer swaps), because an end operation must not hold
	// the pointer mutex across a join.
	//
	// Lock order: endMu is taken ABOVE mu and never below it. It is never held
	// while a stop, cancel or join callback runs: those are collected under the
	// session's gate and invoked after this lock is released, so a callback can
	// re-enter the runtime.
	endMu sync.Mutex
	// endAccepted records that a runtime end was accepted, so a second
	// acceptance neither stops anything again nor re-opens admission. endPhase
	// is the conversation's exit phase as this runtime last moved it.
	endAccepted bool
	endPhase    string
	// endIntended distinguishes a CLOSURE from a REPLACEMENT: a replacement
	// omits the postmortem phase entirely (shared contract 8).
	endIntended string
	// analysisScope is the open scoped exit-analysis authorization, if any.
	analysisScope *session.ExitAnalysisScope
	// scopedAnalysis is the operation RunExitAnalysis invokes. It is set at
	// startup by the transport that owns the analysis.
	scopedAnalysis ExitAnalysisFunc
	// endErr records the last end/join failure so an interactive caller can
	// offer a retry instead of advancing as if the join had succeeded.
	endErr error
}

// End phase names as the runtime spells them. They are the session's ExitPhase
// values, exposed as plain strings so a transport can carry them without
// importing the session package.
const (
	// EndPhaseOpen is a live conversation.
	EndPhaseOpen = string(session.ExitPhaseOpen)
	// EndPhaseStopping means an end was accepted and its work is being stopped
	// and joined.
	EndPhaseStopping = string(session.ExitPhaseStopping)
	// EndPhasePostmortem means the stopped work settled and only scoped exit
	// analysis may still launch.
	EndPhasePostmortem = string(session.ExitPhasePostmortem)
	// EndPhaseClosing means terminal teardown is running and nothing may launch.
	EndPhaseClosing = string(session.ExitPhaseClosing)
	// EndPhaseClosed means the runtime is closed.
	EndPhaseClosed = string(session.ExitPhaseClosed)
)

// EndIntent names what the user accepted: closing the conversation (an exit) or
// replacing it (/session new, clear, resume). A replacement omits the postmortem
// phase; a closure may enter it.
type EndIntent string

const (
	// EndIntentExit ends the conversation and the process.
	EndIntentExit EndIntent = "exit"
	// EndIntentReplace swaps the conversation for a new one, with no exit
	// postmortem.
	EndIntentReplace EndIntent = "replace"
)

// ActiveWork describes the runtime's currently active conversation work: the
// membership a warning must show before anything is stopped.
type ActiveWork struct {
	// Phase is the conversation's exit phase.
	Phase string
	// Items is the membership, oldest first. Each item carries its own distinct
	// opaque ID even when it is displayed under a parent, so new work can never
	// hide behind an existing parent's identity.
	Items []WorkEntry
	// Targets lists the distinct stoppable targets a stop would act on. Attached
	// commands shown under their parent are folded into that parent, so this
	// never counts one stoppable thing twice.
	Targets []string
}

// Empty reports whether no ordinary work is active.
func (a ActiveWork) Empty() bool { return len(a.Items) == 0 }

// WorkEntry is one active item, in the neutral shape a transport can render.
type WorkEntry struct {
	ID    string
	Kind  string
	Label string
	// Group is the ID of the operation an attached command belongs to; empty
	// when the item stands alone.
	Group string
	// Parent is true for a top-level operation, so a client can indent its
	// attached commands under it.
	Parent bool
	// SubagentID is the child card's id for a delegated child, or 0.
	SubagentID int64
}

// EndDecision is the outcome of an end acceptance.
type EndDecision struct {
	// Accepted reports that ordinary admission is now frozen.
	Accepted bool
	// Expanded lists the active IDs that were NOT displayed, when the caller
	// required displayed membership. A non-empty Expanded means nothing was
	// stopped and the caller must refresh and ask again.
	Expanded []string
	// Work is the membership at the moment of acceptance.
	Work ActiveWork
}

// EndAcceptRequest is one end acceptance request. DisplayedIDs are the opaque
// work IDs the user was actually shown; RequireDisplayed is true when the caller
// insists nothing may be stopped that the user has not seen.
type EndAcceptRequest struct {
	Intent           EndIntent
	DisplayedIDs     []string
	RequireDisplayed bool
}

// ActiveWork returns the runtime's currently active conversation work: the
// authoritative membership a warning panel renders. It reads the session's gate,
// which registers parent operations, delegated children and managed commands
// alike, so what it reports and what an accepted end acts on cannot drift apart.
func (rt *Runtime) ActiveWork() ActiveWork {
	if rt == nil || rt.State == nil {
		return ActiveWork{Phase: EndPhaseOpen}
	}
	snap := rt.State.WorkMembership()
	out := ActiveWork{Phase: string(snap.Phase)}
	for _, item := range snap.Items {
		out.Items = append(out.Items, WorkEntry{
			ID:         string(item.ID),
			Kind:       string(item.Kind),
			Label:      item.Label,
			Group:      string(item.Group),
			Parent:     item.Kind == session.WorkKindParent,
			SubagentID: item.SubagentID,
		})
	}
	for _, target := range snap.Targets() {
		out.Targets = append(out.Targets, string(target))
	}
	return out
}

// AcceptEnd accepts an end of the current conversation. It checks the displayed
// work IDs and freezes ordinary admission ATOMICALLY: if any currently active ID
// was not displayed, it changes nothing and stops nothing, so the caller can
// refresh its warning and ask again. Finished IDs need no reconfirmation — work
// that completed while the prompt was open is simply absent.
//
// A second acceptance of an already-accepted end is a no-op: it stops nothing
// again and reports Accepted with the membership it found.
func (rt *Runtime) AcceptEnd(req EndAcceptRequest) (EndDecision, error) {
	if rt == nil || rt.State == nil {
		return EndDecision{}, fmt.Errorf("runtime: AcceptEnd called with nil State")
	}
	intent := req.Intent
	if intent == "" {
		intent = EndIntentExit
	}
	rt.endMu.Lock()
	defer rt.endMu.Unlock()

	snap, expanded, frozen := rt.State.AcceptWorkDisplayed(req.DisplayedIDs, req.RequireDisplayed)
	if len(expanded) > 0 {
		// The displayed set did not cover what is active: refresh and stop
		// nothing. Ordinary admission is deliberately NOT frozen here — the
		// caller has not accepted this membership yet — and the runtime does not
		// record an intent it never acted on.
		decision := EndDecision{Accepted: false, Work: activeWorkFrom(snap)}
		for _, id := range expanded {
			decision.Expanded = append(decision.Expanded, string(id))
		}
		return decision, nil
	}
	// Only the acceptance that actually froze admission records the intent. A
	// second, idempotent acceptance must not RELABEL an end the user already
	// accepted: a replacement followed by a repeated exit request would
	// otherwise advance into the postmortem phase for a conversation the user
	// chose to replace.
	if frozen {
		rt.endIntended = string(intent)
		rt.endAccepted = true
	}
	rt.endPhase = EndPhaseStopping
	rt.State.SetWorkPhase(session.ExitPhaseStopping)
	return EndDecision{Accepted: true, Work: activeWorkFrom(snap)}, nil
}

// activeWorkFrom renders a session snapshot in the runtime's neutral shape.
func activeWorkFrom(snap session.WorkSnapshot) ActiveWork {
	out := ActiveWork{Phase: string(snap.Phase)}
	for _, item := range snap.Items {
		out.Items = append(out.Items, WorkEntry{
			ID:         string(item.ID),
			Kind:       string(item.Kind),
			Label:      item.Label,
			Group:      string(item.Group),
			Parent:     item.Kind == session.WorkKindParent,
			SubagentID: item.SubagentID,
		})
	}
	for _, target := range snap.Targets() {
		out.Targets = append(out.Targets, string(target))
	}
	return out
}

// EndPhase returns the runtime's current exit phase.
func (rt *Runtime) EndPhase() string {
	if rt == nil {
		return EndPhaseClosed
	}
	rt.endMu.Lock()
	defer rt.endMu.Unlock()
	if rt.endPhase != "" {
		return rt.endPhase
	}
	if rt.State == nil {
		return EndPhaseOpen
	}
	return string(rt.State.WorkPhase())
}

// EndAccepted reports whether an end has been accepted and ordinary admission is
// frozen.
func (rt *Runtime) EndAccepted() bool {
	if rt == nil {
		return false
	}
	rt.endMu.Lock()
	defer rt.endMu.Unlock()
	return rt.endAccepted
}

// LastEndError returns the error the most recent stop/join pass reported, so an
// interactive caller can offer a retry rather than advancing as if the work had
// settled. A nil return means the last pass succeeded (or none ran).
func (rt *Runtime) LastEndError() error {
	if rt == nil {
		return nil
	}
	rt.endMu.Lock()
	defer rt.endMu.Unlock()
	return rt.endErr
}

// WaitForEnd stops and joins the accepted end's work, then reports it settled.
// It is the interactive caller's settlement step: the caller runs it off the
// render loop, watches ActiveWork for the stopping set, and retries on failure
// instead of assuming the work is gone.
//
// The pass, in order: settle durable pending job-exit writes, resolve pending
// user decisions so a turn blocked on an approval can finish, stop every
// still-active target, and join the memberships the runtime owns. A failed join
// reports the outstanding work and returns its error, so a caller can retry; it
// never latches success.
func (rt *Runtime) WaitForEnd(ctx context.Context) error {
	if rt == nil || rt.State == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rt.endMu.Lock()
	accepted := rt.endAccepted
	analysis := rt.analysisScope
	rt.endMu.Unlock()
	if !accepted {
		return fmt.Errorf("runtime: WaitForEnd called before an end was accepted")
	}

	// 1. Retry any settled completion whose durable record has not landed yet,
	//    so a completion recorded during the prompt is stored before the
	//    database can close.
	if err := rt.State.RetryJobExitSaves(); err != nil && rt.Logger != nil {
		rt.Logger.Warn("job exit save retry failed before end settlement", "error", err)
	}

	// 2. Resolve pending user decisions, so a turn parked on an approval, a
	//    question, a skill gate or a privileged-auth prompt can observe the
	//    denial and finish instead of blocking the join.
	rt.State.ResolvePendingForShutdown()

	// 3. Stop every still-active target. Callbacks run without the runtime's end
	//    lock, so a stop that re-enters the runtime cannot deadlock.
	err := rt.stopEndWork(ctx, analysis)

	// 4. Join: the session's work waitgroup (parent operations), the session's
	//    gate membership (children and commands, which settle through their own
	//    paths), the command service, and the durable finalization.
	joinErr := rt.joinEndWork(ctx, analysis)
	err = errors.Join(err, joinErr)

	rt.endMu.Lock()
	rt.endErr = err
	if err == nil {
		if rt.endIntended == string(EndIntentExit) {
			rt.endPhase = EndPhasePostmortem
			rt.State.SetWorkPhase(session.ExitPhasePostmortem)
		} else {
			// A replacement omits postmortem entirely: the session never enters
			// the phase, so no concurrent reader can observe one mid-replacement.
			rt.endPhase = EndPhaseClosing
			rt.State.SetWorkPhase(session.ExitPhaseClosing)
		}
	}
	rt.endMu.Unlock()
	return err
}

// stopEndWork stops every still-active unit of work in the membership: attached
// and detached commands through the conversation's command service, delegated
// children through the session's subagent registry, and parent operations
// through the work context their registration carries.
//
// It is safe to call repeatedly: an already-stopped target is a no-op, and a
// target that finished meanwhile is simply gone.
func (rt *Runtime) stopEndWork(ctx context.Context, analysis *session.ExitAnalysisScope) error {
	scope := ""
	if analysis != nil {
		scope = analysis.ID()
	} else {
		// The ordinary membership is stopped by turn groups: a command still
		// attached to a turn is stopped by that turn's explicit stop, and the
		// command service refuses that turn any further admission.
		rt.stopOrdinaryTurnWork()
	}
	targets := rt.State.WorkStopTargets(scope)
	for _, target := range targets {
		switch target.Kind {
		case session.WorkKindChild:
			if target.SubagentID != 0 {
				rt.State.CancelSubagent(target.SubagentID)
			} else if target.TurnID != "" {
				rt.stopAttachedTurn(target.TurnID, "conversation ended")
			}
		case session.WorkKindCommand:
			if target.TurnID != "" {
				rt.stopAttachedTurn(target.TurnID, "conversation ended")
			}
		case session.WorkKindParent:
			// Cancelling the parent work context is what unblocks a turn parked
			// in provider work or an approval; it runs with no runtime lock held.
			target.Cancel()
		}
	}
	// Every execution still owned by the conversation's command service is
	// stopped through its own admission boundary, so detached jobs the
	// membership reached are stopped too.
	if jm := rt.commandManager(); jm != nil {
		for _, info := range jm.ActiveExecutions() {
			if err := jm.RequestStop(info.ID, "conversation ended"); err != nil && rt.Logger != nil {
				rt.Logger.Warn("stop at conversation end failed", "job", info.ID, "error", err)
			}
		}
	}
	_ = ctx
	return nil
}

// stopOrdinaryTurnWork stops every registered turn group's attached commands.
// Each group is a parent operation's stop target, so stopping them all is what
// freezes ordinary command admission at the service.
func (rt *Runtime) stopOrdinaryTurnWork() {
	targets := rt.State.WorkStopTargets("")
	seen := make(map[string]bool, len(targets))
	for _, target := range targets {
		if target.TurnID == "" || seen[target.TurnID] {
			continue
		}
		seen[target.TurnID] = true
		rt.stopAttachedTurn(target.TurnID, "conversation ended")
	}
	// A turn group with no registered work item of its own (a runner used
	// directly) is still reachable through the session's current registration.
	if token := rt.State.CurrentTurnStop(); token != nil && !seen[token.TurnID()] {
		_, _ = token.Stop("conversation ended")
	}
}

// stopAttachedTurn stops one turn group's attached commands at the command
// service's admission/detachment boundary.
func (rt *Runtime) stopAttachedTurn(turnID, cause string) {
	if turnID == "" {
		return
	}
	jm := rt.commandManager()
	if jm == nil {
		return
	}
	if _, err := jm.StopAttached(turnID, cause); err != nil && rt.Logger != nil {
		rt.Logger.Warn("turn stop at conversation end not accepted", "turn", turnID, "error", err)
	}
}

// joinEndWork waits for the stopped work to settle. Every join is bounded by the
// caller's context: a deadline bounds the JOIN, never the process lifetime. A
// failed join reports the outstanding work through the returned error and leaves
// the runtime's state alone, so the caller can retry.
func (rt *Runtime) joinEndWork(ctx context.Context, analysis *session.ExitAnalysisScope) error {
	var errs []error
	if err := rt.State.WaitForWork(ctx); err != nil {
		errs = append(errs, fmt.Errorf("join conversation work: %w", err))
	}
	if analysis != nil {
		if err := rt.State.WaitForScopedWorkDrain(ctx, analysis); err != nil {
			errs = append(errs, fmt.Errorf("join exit analysis: %w", err))
		}
	}
	if err := rt.State.WaitForWorkDrain(ctx); err != nil {
		outstanding := rt.State.OutstandingWork()
		names := make([]string, 0, len(outstanding))
		for _, item := range outstanding {
			names = append(names, string(item.ID))
		}
		errs = append(errs, fmt.Errorf("join active work: %w (outstanding: %s)", err, strings.Join(names, ", ")))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	// Durable finalization: every detached completion is stored before the
	// runtime may extract a report or close the database.
	if err := rt.State.RetryJobExitSaves(); err != nil {
		errs = append(errs, fmt.Errorf("persist job exits: %w", err))
	}
	if jm := rt.commandManager(); jm != nil {
		// One retry before reporting: the database is still open, so a
		// completion whose first durable write failed gets a second chance
		// here. A retry updates that execution's existing record rather than
		// producing a second completion.
		if pending := jm.RetrySettlements(); len(pending) > 0 {
			errs = append(errs, fmt.Errorf("job completions still pending: %d", len(pending)))
		}
	}
	// The work view and the watch terminal saves are the projection's durable
	// facts. Retry them in the same pass, while persistence is still open, so a
	// settled watch result or dirty work view is stored before the database
	// closes. Neither retry announces a durable acknowledgment when it fails.
	if err := rt.retryWorkPersistence(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// RunExitAnalysis runs one scoped exit-analysis operation. It is admitted only
// during the postmortem phase, on its own work epoch, with an authorization
// stamped on the operation's context so its commands and children inherit it.
//
// The epoch is always stopped and joined before this returns — on success, on
// failure and on cancellation — so no analysis work can outlive the phase.
// Analysis work is separate from the ordinary membership and cannot reopen the
// conversation's ordinary admission.
func (rt *Runtime) RunExitAnalysis(ctx context.Context, goal string) error {
	if rt == nil || rt.State == nil {
		return fmt.Errorf("runtime: RunExitAnalysis called with nil State")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rt.endMu.Lock()
	if rt.endPhase != EndPhasePostmortem {
		// A replacement omits postmortem entirely; a closure that has not
		// settled its work cannot start analysis either.
		rt.endMu.Unlock()
		return fmt.Errorf("%w: runtime exit phase is %q", session.ErrNotPostmortemPhase, rt.endPhase)
	}
	if rt.analysisScope != nil {
		rt.endMu.Unlock()
		return session.ErrExitAnalysisAlreadyOpen
	}
	scope, err := rt.State.OpenExitAnalysis()
	if err != nil {
		rt.endMu.Unlock()
		return err
	}
	rt.analysisScope = scope
	rt.endMu.Unlock()

	rt.State.SetWorkPhase(session.ExitPhasePostmortem)
	// Stamp the authorization here, in the runtime that owns it: the operation
	// receives a context whose commands and children are registered in this
	// epoch no matter which transport supplied the operation, so the epoch's
	// cleanup is complete and its work can never be mistaken for ordinary work.
	analysisCtx := session.WithExitAnalysisScope(ctx, scope)
	runErr := rt.runScopedAnalysis(analysisCtx, scope, goal)

	// Always stop and join this epoch before returning, whatever happened.
	rt.State.CloseExitAnalysis(scope)
	rt.stopEndWork(ctx, scope)
	joinErr := rt.joinEndWork(ctx, scope)

	rt.endMu.Lock()
	if rt.analysisScope == scope {
		rt.analysisScope = nil
	}
	rt.endMu.Unlock()
	return errors.Join(runErr, joinErr)
}

// ExitAnalysisFunc runs one scoped exit-analysis pass. It is a function value so
// a transport can supply the operation (the TUI's postmortem agent pass) without
// the runtime importing the transport. The scope is the epoch the runtime opened:
// the context it receives already carries that authorization, so the commands and
// children the pass launches are registered in the epoch and are joined with it.
type ExitAnalysisFunc func(ctx context.Context, scope *session.ExitAnalysisScope, goal string) error

// runScopedAnalysis invokes the installed operation. A runtime with none reports
// that exit analysis is unavailable rather than silently doing nothing.
func (rt *Runtime) runScopedAnalysis(ctx context.Context, scope *session.ExitAnalysisScope, goal string) error {
	rt.endMu.Lock()
	fn := rt.scopedAnalysis
	rt.endMu.Unlock()
	if fn == nil {
		return fmt.Errorf("runtime: no exit-analysis operation is wired")
	}
	return fn(ctx, scope, goal)
}

// SetExitAnalysisOperation installs the operation RunExitAnalysis invokes. It is
// a startup-time seam: the transport that owns the analysis (Run, for the TUI's
// postmortem pass) installs it there.
func (rt *Runtime) SetExitAnalysisOperation(fn ExitAnalysisFunc) {
	if rt == nil {
		return
	}
	rt.endMu.Lock()
	rt.scopedAnalysis = fn
	rt.endMu.Unlock()
}

// commandManager returns the conversation's command supervisor under the pointer
// mutex, or nil when the build never reached owner construction.
func (rt *Runtime) commandManager() *native.JobManager {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return commandServiceManager(rt.CommandService)
}

// watchManager returns the conversation's watch manager under the pointer mutex,
// or nil when the build never reached owner construction or a replacement has
// left the slot empty.
func (rt *Runtime) watchManager() *watch.Manager {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.WatchManager
}

// retryWorkPersistence re-offers the conversation's dirty work view and every
// failed watch terminal save. It runs while persistence is still open, so a
// completion whose first durable write failed gets a retried chance instead of
// being lost at close. A failed retry is reported; it never announces a durable
// acknowledgment of its own.
func (rt *Runtime) retryWorkPersistence() error {
	var errs []error
	if rt.State != nil {
		if err := rt.State.RetryWorkViewSave(); err != nil {
			errs = append(errs, fmt.Errorf("persist work view: %w", err))
		}
	}
	if wm := rt.watchManager(); wm != nil {
		if err := wm.RetryTerminalSaves(); err != nil {
			errs = append(errs, fmt.Errorf("persist watch results: %w", err))
		}
	}
	return errors.Join(errs...)
}

// commandServiceManager returns the supervisor's manager handle, or nil when
// the build never reached owner construction (a provider-resolution failure
// returns before the service exists). Callers that only want the handle must go
// through this rather than dereferencing the service.
func commandServiceManager(s *conversationCommandService) *native.JobManager {
	if s == nil {
		return nil
	}
	return s.manager
}

// CurrentWatchManager returns the runtime's watch manager under the pointer
// mutex, so a concurrent agent-runtime reload cannot race the read.
func (rt *Runtime) CurrentWatchManager() *watch.Manager {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.WatchManager
}

// forceClassForMode maps an interaction mode to the runner's force class: the
// read-only modes force the question class, every editing mode forces edit.
// Kept next to the access methods so the mode→class mapping has one home.
func forceClassForMode(mode string) string {
	if mode == "plan" || mode == "default" {
		return "question"
	}
	return "edit"
}

// AccessRequest returns the access request waiting for a decision, if any.
//
// It takes the reload gate so the snapshot it returns cannot describe a
// session that a concurrent reload or session swap is replacing. A request
// whose capabilities were satisfied while it waited — a manual mode change, or
// a system grant from elsewhere — is settled here as already-available rather
// than presented: the user must never be asked to approve something they
// already hold. That branch of ResolveAccess performs no setter and no
// application, so this is pure bookkeeping.
func (rt *Runtime) AccessRequest() (session.AccessRequest, bool) {
	rt.reloadMu.Lock()
	defer rt.reloadMu.Unlock()
	if rt.State == nil {
		return session.AccessRequest{}, false
	}
	req, ok := rt.State.AccessRequest()
	if !ok {
		return session.AccessRequest{}, false
	}
	if !req.MissingEditing && !req.MissingSystem {
		// Nothing is missing any more: settle it so the waiter unblocks and
		// the request stops being presented. A concurrent tuple change makes
		// ResolveAccess return refresh instead, and the re-read below then
		// hands back a fresh presentation rather than a stale one.
		_, _ = rt.State.ResolveAccess(session.AccessChoice{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Current:   req.Current,
			Approved:  true,
		}, nil)
		return rt.State.AccessRequest()
	}
	return req, true
}

// ResolveAccess validates and settles the outstanding access request. It holds
// the reload gate for the whole call, so the runner and session it validates
// against cannot be replaced mid-resolution.
func (rt *Runtime) ResolveAccess(choice session.AccessChoice) (session.AccessResult, error) {
	rt.reloadMu.Lock()
	defer rt.reloadMu.Unlock()
	if rt.State == nil {
		err := errors.New("app: no session state available to resolve access")
		return session.AccessResult{Status: session.AccessExpired, Message: err.Error()}, err
	}
	return rt.State.ResolveAccess(choice, rt.accessPrepare())
}

// accessPrepare builds the resolver the session calls to validate a choice and
// obtain the commit that applies it. It is the last gate before authority
// moves, so it revalidates the choice shape and the target pair rather than
// trusting the session's own checks, and it refuses before any mutation when
// the runtime cannot actually apply what was selected.
//
// The returned Apply is memory-only by contract: it touches the runner's
// in-memory policy and force class and nothing else. It must never save a
// config file, reload the runtime, build a provider, replace a manager or take
// the write gate — a grant is a session-scoped permission change, not a
// runtime rebuild.
func (rt *Runtime) accessPrepare() session.AccessPrepare {
	return func(req session.AccessRequest, choice session.AccessChoice) (session.AccessCommit, error) {
		if choice.RequestID != req.RequestID || choice.SessionID != req.SessionID {
			return session.AccessCommit{}, errors.New("access choice does not name the request")
		}
		if !choice.Approved {
			return session.AccessCommit{}, errors.New("access choice is not an approval")
		}
		if rt.State == nil {
			return session.AccessCommit{}, errors.New("no session state available to apply access")
		}
		target := req.Current
		if req.MissingEditing {
			if !policy.ValidApprovalMode(choice.Mode) {
				return session.AccessCommit{}, fmt.Errorf("invalid editing mode %q", choice.Mode)
			}
			switch policy.ApprovalMode(choice.Mode) {
			case policy.ModeEdit, policy.ModeCopilot, policy.ModeAuto:
			default:
				return session.AccessCommit{}, fmt.Errorf("mode %q does not grant editing", choice.Mode)
			}
			// An editing change needs a live policy engine to move; refuse
			// before mutating anything rather than reporting a grant that
			// never reached the runner.
			if rt.Runner == nil || rt.Runner.Policy == nil {
				return session.AccessCommit{}, errors.New("no policy engine available to change the interaction mode")
			}
			target.Mode = choice.Mode
		} else if choice.Mode != "" {
			return session.AccessCommit{}, fmt.Errorf("system-only request cannot select mode %q", choice.Mode)
		}
		if req.MissingSystem {
			target.System = true
		}
		// A system-only grant needs no runner work: the session flag is the
		// whole change. Apply stays non-nil so the commit is never mistaken
		// for an unapplied one.
		apply := func() {}
		if req.MissingEditing {
			runner := rt.Runner
			mode := policy.ApprovalMode(target.Mode)
			apply = func() {
				runner.SetForceClass("edit")
				runner.SetApprovalMode(mode)
			}
		}
		return session.AccessCommit{Effective: target, Apply: apply}, nil
	}
}

// SetSessionMode applies an explicit interaction-mode selection for this
// session. It is the manual path shared by the TUI and ACP, so it validates
// the selection and the runtime's ability to apply it before anything moves.
//
// The mode is recorded on the session, which owns the override that survives
// an unrelated config reload, and applied to the live runner's in-memory
// policy. Runtime.Config is deliberately untouched: a manual selection is a
// session-scoped choice, not a config edit, so it must not become the next
// conversation's default.
func (rt *Runtime) SetSessionMode(mode string) error {
	rt.reloadMu.Lock()
	defer rt.reloadMu.Unlock()
	if rt.State == nil {
		return errors.New("no session state available to change the interaction mode")
	}
	switch mode {
	case "plan", "default", "edit", "copilot", "auto":
	default:
		return fmt.Errorf("invalid approval mode %q", mode)
	}
	// An inactive runtime (no provider configured) still records the manual
	// selection, so the UI reflects the user's choice; there is simply no
	// runner to move. A runner that exists without a policy engine cannot
	// apply the change, so that fails rather than silently diverging.
	apply := func() {}
	if rt.Runner != nil {
		if rt.Runner.Policy == nil {
			return errors.New("no policy engine available to change the interaction mode")
		}
		runner := rt.Runner
		class := forceClassForMode(mode)
		apply = func() {
			runner.SetForceClass(class)
			runner.SetApprovalMode(policy.ApprovalMode(mode))
		}
	}
	return rt.State.SetSessionMode(mode, apply)
}

// SteeringSnapshot returns the current conversation's Send now snapshot through
// the runtime seam. It takes the pointer mutex only long enough to read the
// conversation's State, then releases it and delegates: the snapshot itself is
// a bounded, memory-only read that never takes the reload gate, waits for a
// process, builds a provider or commits a config. A runtime with no session (a
// failed build) reports the zero snapshot — inactive, unsupported — rather than
// panicking.
func (rt *Runtime) SteeringSnapshot() session.SteeringSnapshot {
	rt.mu.Lock()
	state := rt.State
	rt.mu.Unlock()
	if state == nil {
		return session.SteeringSnapshot{}
	}
	return state.SteeringSnapshot()
}

// SubmitSendNow accepts one Send now request against the current conversation.
// Like SteeringSnapshot it reads the current State under the pointer mutex and
// releases it before delegating, so the bounded, memory-only acceptance — never
// a process wait, a provider build, a config save or a manager replacement —
// runs against the conversation it targeted. The request's own SessionID/TurnID
// still name the turn, so a request built for a replaced conversation is
// refused by the session rather than retargeted onto its replacement. A
// runtime with no session fails closed.
func (rt *Runtime) SubmitSendNow(request session.SendNowRequest) (session.SteeringReceipt, error) {
	rt.mu.Lock()
	state := rt.State
	rt.mu.Unlock()
	if state == nil {
		return session.SteeringReceipt{}, errors.New("app: no session state available for send now")
	}
	return state.SubmitSendNow(request)
}

// SetSessionEffort sets (level != "") or clears (level == "") the session's
// unsaved effort override for the active route, and reports when it takes
// effect: from the next step of a running turn, or from the next turn when
// no turn runs or the change crosses the off boundary.
func (rt *Runtime) SetSessionEffort(level string) (string, error) {
	if rt.State == nil {
		return "", errors.New("no session state available to change the effort")
	}
	active := rt.State.ActiveRoute()
	if active.Model == "" {
		return "", errors.New("no active model")
	}
	if !active.ReasoningCapable {
		return "", fmt.Errorf("%s has no effort control", active.Model)
	}
	cfg := rt.State.ConfigSnapshot()
	preset := cfg.Models.Presets[active.Preset]
	opts := provider.ThinkingOptionsFor(rt.DataDir, active.Provider, cfg.Providers[active.Provider], active.Model, preset.ThinkingOptions)
	if !provider.ValidEffortLevel(opts, level) {
		return "", fmt.Errorf("effort %q is not supported by %s (valid: %s)", level, active.Model, strings.Join(opts.Levels, ", "))
	}
	rt.State.SetEffortOverride(session.EffortOverride{Level: level, Provider: active.Provider, Model: active.Model})

	applies := session.EffortAppliesNextTurn
	effective := level
	if level == "" {
		effective = preset.Thinking
	}
	current := effective
	if turn, running := rt.State.TurnRoute(); running {
		current = turn.Thinking
		// The runner only applies an override whose provider and model match
		// the running turn's route (agent/effort.go sessionEffortTarget). A
		// route that moved mid-turn therefore lands at the next turn, not at
		// the next step, however the off boundary falls.
		if turn.Provider == active.Provider && turn.Model == active.Model &&
			(current == "off") == (effective == "off") {
			applies = session.EffortAppliesNextStep
		}
	}
	rt.State.PublishEffortChange(session.EffortChange{Effective: current, Pending: effective, AppliesFrom: applies})
	return applies, nil
}

// WatchResumeHook is invoked by the watch manager's OnFire closure after
// a fired report is pushed to the session queue, for runtimes that want to
// auto-resume an idle session (ACP). It receives the report so the sink
// can derive the watch name/mode. It must return promptly — it runs on
// the watch goroutine — so implementations spawn their work in a
// goroutine. A nil receiver is safe (no-op).
type WatchResumeHook struct {
	mu sync.Mutex
	fn func(r watch.Report)
}

// Set installs (or clears, with nil) the hook fn.
func (h *WatchResumeHook) Set(fn func(r watch.Report)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.fn = fn
	h.mu.Unlock()
}

// Invoke calls the installed fn when non-nil. Nil-receiver-safe.
func (h *WatchResumeHook) Invoke(r watch.Report) {
	if h == nil {
		return
	}
	h.mu.Lock()
	fn := h.fn
	h.mu.Unlock()
	if fn != nil {
		fn(r)
	}
}

// bindLSP points the runtime's LSP handle at the host's pooled manager for
// root and cfg's server set, then releases the manager it pointed at before.
// The pool hands back the same manager when neither changed, so a reload that
// keeps the server set keeps its running servers.
func (rt *Runtime) bindLSP(cfg config.Config, root string) {
	if rt.LSPManager == nil || rt.Host == nil {
		return
	}
	rt.lspMu.Lock()
	defer rt.lspMu.Unlock()
	if rt.lspClosed {
		return
	}
	lease := rt.Host.lsp.acquire(root, lspServersFor(cfg, root))
	old := rt.lspLease
	rt.lspLease = lease
	rt.LSPManager.Set(lease.manager())
	old.release()
}

// unbindLSP clears the runtime's LSP handle, releases its pooled manager and
// refuses later binds.
func (rt *Runtime) unbindLSP() {
	rt.lspMu.Lock()
	defer rt.lspMu.Unlock()
	rt.lspClosed = true
	if rt.LSPManager != nil {
		rt.LSPManager.Set(nil)
	}
	rt.lspLease.release()
	rt.lspLease = nil
}

// AdditionalDirectories returns the list of extra workspace roots registered
// via WithAdditionalDirectories, or nil if none.
func (r *Runtime) AdditionalDirectories() []string {
	return r.additionalDirs
}

// BeginWork registers a unit of transport-visible work with the runtime and
// returns a child context that is cancelled when the runtime quiesces or the
// parent context is cancelled. The returned finish function is idempotent and
// MUST be called exactly once (typically deferred) to signal work completion
// to the shutdown gate.
//
// The registration is also the conversation end gate's parent-operation entry
// (Task 8): the operation is part of the membership ActiveWork reports, and its
// own cancel is what an accepted conversation end invokes to stop it, so a turn
// parked in provider work or waiting on an approval can be stopped without
// cancelling the runtime's infrastructure root.
//
// If the runtime is already quiescing, or an end has already been accepted,
// BeginWork returns an error wrapping session.ErrSessionQuiescing and does NOT
// increment the work counter. Nil parent, State, or runtime root context are
// rejected with a concrete error. If runtime cancellation races with
// registration, the child context is cancelled immediately; the caller still
// owns finish.
func (rt *Runtime) BeginWork(parent context.Context) (context.Context, func(), error) {
	if parent == nil {
		return nil, nil, fmt.Errorf("runtime: BeginWork parent context is nil")
	}
	if rt.State == nil {
		return nil, nil, fmt.Errorf("runtime: BeginWork called with nil State")
	}
	if rt.workCtx == nil {
		return nil, nil, fmt.Errorf("runtime: BeginWork called with nil runtime work context")
	}

	if err := rt.State.BeginWorkCancelable(nil); err != nil {
		return nil, nil, err
	}

	workCtx, cancel := context.WithCancel(parent)
	stopRuntimeCancel := context.AfterFunc(rt.workCtx, cancel)
	// Publish this operation's stop hook to the conversation gate, so an
	// accepted end stops exactly this turn rather than the infrastructure root.
	rt.State.SetWorkCancel(cancel)

	var finishOnce sync.Once
	finish := func() {
		finishOnce.Do(func() {
			stopRuntimeCancel()
			cancel()
			rt.State.EndWork()
		})
	}

	return workCtx, finish, nil
}

// Quiesce cancels and joins active turns and background jobs without closing
// persistence (database, logger). After Quiesce returns the session is
// quiesced: no new work can begin, all in-flight work has completed, and all
// running background jobs have been shut down. The database and logger remain
// open so that downstream consumers (e.g. knowledge finalization) can use
// them. Idempotent — subsequent calls are no-ops.
func (rt *Runtime) Quiesce(ctx context.Context) error {
	rt.quiesceOnce.Do(func() {
		if rt.State == nil {
			return
		}
		rt.State.BeginQuiesce()
		// Freeze ordinary admission at the conversation gate as well. The work
		// gate above stops new BeginWork registrations; this stops new commands
		// and children at the service and the session, which is what makes the
		// membership below stable while it is being stopped.
		rt.freezeOrdinaryAdmission()
		if rt.workCancel != nil {
			rt.workCancel()
		}
		// The startup snapshot sweep holds the store lock and may run Git;
		// it stops on the cancellation above and must be gone before
		// persistence closes.
		rt.waitSnapshotMaintenance(jobShutdownTimeout)
		rt.State.ResolvePendingForShutdown()
		rt.State.Shutdown()

		// Stop and join the conversation's own work while the command service and
		// the database are still open, so every settled completion is stored
		// before the service ends. This is the same pass an interactive end runs
		// (Runtime.WaitForEnd) and it is idempotent, so a headless/signal shutdown
		// reuses the work already stopped instead of stopping anything twice.
		workErr := rt.stopAndJoinConversationWork(ctx)

		// Snapshot JobManager and WatchManager under the pointer mutex so a
		// concurrent reloadAgentRuntime can swap the pointers without a data
		// race.
		rt.mu.Lock()
		jm := rt.JobManager
		wm := rt.WatchManager
		rt.mu.Unlock()

		// Shut the watch manager down BEFORE the job manager: watches may
		// depend on jobs, so their goroutines must be joined first.
		var watchErr error
		if wm != nil {
			watchCtx, watchCancel := context.WithTimeout(context.Background(), jobShutdownTimeout)
			watchErr = wm.Close(watchCtx)
			watchCancel()
		}

		var jobErr error
		if jm != nil {
			deadline, ok := ctx.Deadline()
			var shutdownCtx context.Context
			var shutdownCancel context.CancelFunc
			if ok {
				timeout := time.Until(deadline)
				if timeout > jobShutdownTimeout {
					timeout = jobShutdownTimeout
				}
				if timeout <= 0 {
					timeout = time.Nanosecond
				}
				shutdownCtx, shutdownCancel = context.WithTimeout(context.Background(), timeout)
			} else {
				shutdownCtx, shutdownCancel = context.WithTimeout(context.Background(), jobShutdownTimeout)
			}
			defer shutdownCancel()
			jobErr = jm.Shutdown(shutdownCtx)
		}

		// The join after the service's own shutdown is the terminal one: the
		// membership is drained by then, and a deadline here bounds the join, not
		// any process's lifetime.
		waitErr := rt.State.WaitForWork(ctx)

		// After the owners have finished, drain the last durable facts and retry
		// the work view and the watch terminal saves one final time, alongside
		// the job-exit and settlement retries already run above. A failed retry
		// is reported through quiesceErr; it is never announced as a durable
		// acknowledgment.
		retryErr := rt.retryWorkPersistence()
		// Reconcile the final facts into the projection and then stop and join
		// its coordinator BEFORE the database closes (Close closes the DB only
		// after Quiesce returns).
		if wc := rt.currentWork(); wc != nil {
			if err := wc.finalize(); err != nil {
				retryErr = errors.Join(retryErr, err)
			}
			wc.close()
		}
		rt.quiesceErr = errors.Join(workErr, watchErr, jobErr, waitErr, retryErr)
	})
	return rt.quiesceErr
}

// freezeOrdinaryAdmission closes ordinary admission at the conversation gate, so
// no new command, child or parent operation can register. It never reports
// displayed-membership expansion: this is the noninteractive path, where there is
// nobody to ask, so it freezes exactly what it found.
func (rt *Runtime) freezeOrdinaryAdmission() {
	if rt.State == nil {
		return
	}
	rt.State.AcceptWorkDisplayed(nil, false)
	rt.endMu.Lock()
	rt.endAccepted = true
	if rt.endPhase == "" || rt.endPhase == EndPhaseOpen {
		rt.endPhase = EndPhaseStopping
	}
	rt.endMu.Unlock()
	rt.State.SetWorkPhase(session.ExitPhaseStopping)
	// No watcher prompt, no auto-resume: a conversation that is ending must not
	// start a fresh turn from a fired watch. The hook is cleared here rather than
	// only in Close so the window between acceptance and closure cannot open one.
	rt.WatchResume.Set(nil)
}

// stopAndJoinConversationWork runs the shared stop/join pass on the ordinary
// membership with the command service and database still open. It is the one
// implementation both interactive settlement (WaitForEnd) and terminal closure
// (Quiesce/Close) use, so a failed interactive join that is retried never stops
// or settles anything a second time.
func (rt *Runtime) stopAndJoinConversationWork(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if rt.State == nil {
		return nil
	}
	if err := rt.State.RetryJobExitSaves(); err != nil && rt.Logger != nil {
		rt.Logger.Warn("job exit save retry failed before end settlement", "error", err)
	}
	rt.State.ResolvePendingForShutdown()
	stopErr := rt.stopEndWork(ctx, nil)
	joinErr := rt.joinEndWork(ctx, nil)
	return errors.Join(stopErr, joinErr)
}

// Close tears down resources owned by the runtime in the prescribed order.
// It first calls Quiesce to cancel and join active work/jobs, then closes
// MCP, brokers, snapshots, database, logger, and finally shuts down the
// session state. Idempotent — subsequent calls return the same errors.
//
// The order is:
//  1. Quiesce (cancels work, joins jobs, session quiesce/shutdown)
//  2. MCP manager
//  3. job broker
//  4. steering broker
//  5. event broker
//  6. snapshot DB prune and filesystem prune
//  7. database
//  8. reverse-order resourceClosers
//  9. idempotent state shutdown
//  10. the LSP binding and the project host, which outlive this session's
//     own database connection
func (rt *Runtime) Close(ctx context.Context) error {
	_ = rt.Quiesce(ctx)

	rt.closeOnce.Do(func() {
		var errs []error

		// Clear the watch auto-resume hook so a torn-down session's watch
		// fires can no longer start turns on a replacement runtime. The
		// watch goroutines are already stopped by Quiesce (which Close
		// calls first), so this closes a small race window rather than
		// eliminating one.
		rt.WatchResume.Set(nil)

		// 2. MCP manager.
		if rt.MCPManager != nil {
			if err := rt.MCPManager.Close(); err != nil {
				errs = append(errs, fmt.Errorf("mcp close: %w", err))
			}
		}

		// 3. job broker — close pump and broker.
		if rt.JobBroker != nil {
			rt.JobBroker.Close()
		}

		// 3.5. watch broker.
		if rt.WatchBroker != nil {
			rt.WatchBroker.Close()
		}

		// 4. steering broker.
		if rt.SteeringBroker != nil {
			rt.SteeringBroker.Close()
		}

		// 5. event broker.
		if rt.EventBroker != nil {
			rt.EventBroker.Close()
		}

		// 5.5. workspace broker.
		if rt.WorkspaceBroker != nil {
			rt.WorkspaceBroker.Close()
		}

		// 5.6. subagent broker.
		if rt.SubagentBroker != nil {
			rt.SubagentBroker.Close()
		}

		// 5.7. index broker.
		if rt.IndexBroker != nil {
			rt.IndexBroker.Close()
		}

		// 5b. rollover close — end the live generation with session_end.
		if rt.Runner != nil && rt.Runner.Rollover != nil {
			if err := rt.Runner.Rollover.Close(ctx); err != nil {
				errs = append(errs, fmt.Errorf("rollover close: %w", err))
			}
		}

		// 6. snapshot lifecycle and bounded DB bookkeeping.
		//
		// NO filesystem prune, NO reconciliation, and NO Git gc/repack run
		// here. That is the whole point of this stage: the old shutdown path
		// pruned refs and then ran `git gc --prune=now` under a 30 s deadline,
		// ignoring the error; the gc could not free anything because snapshots
		// were reachable from HEAD, and an interrupted repack left 171 GiB of
		// tmp_pack_* garbage. Shutdown now cancels and joins bounded snapshot
		// work, and the only storage query it makes is a read-only status
		// probe that measures and lists but never reconciles or reclaims.
		if rt.Snapshot != nil {
			// 6a. DB snapshot-row retention. This is bounded bookkeeping in
			// SQLite (one DELETE), not storage work on the snapshots root, so it
			// stays. An expired row's hash must now resolve to the explicit
			// typed expiry error rather than a leaked Git error, which the
			// store's lookup already does.
			if rt.DB != nil && rt.Logger != nil {
				if perr := rt.DB.PruneSnapshotsOlderThan(rt.Config.Snapshots.RetentionDays); perr != nil {
					rt.Logger.Warn("snapshot DB prune failed", "error", perr)
				}
			}
			// 6b. Cancel and join in-flight snapshot work.
			closeCtx, closeCancel := context.WithTimeout(context.Background(), snapshotCloseTimeout)
			if cerr := rt.Snapshot.CloseSnapshots(closeCtx); cerr != nil {
				if rt.Logger != nil {
					if ctxErr := closeCtx.Err(); ctxErr != nil && errors.Is(cerr, ctxErr) {
						rt.Logger.Debug("snapshot close cancelled", "error", cerr)
					} else {
						rt.Logger.Warn("snapshot close failed", "error", cerr)
					}
				}
			}
			// 6c. Read-only health probe for the active workspace. Its context
			// is deliberately NOT derived from the caller's ctx: Close is
			// normally reached from a cancelled context, and a probe that
			// inherited that cancellation would look like a storage failure on
			// every single shutdown. An expected shutdown is not a
			// recovery-required event.
			if sc, ok := rt.Snapshot.(maintenanceSnapshotCloser); ok && rt.Logger != nil {
				statusCtx, statusCancel := context.WithTimeout(context.Background(), snapshotCloseTimeout)
				statusErr := sc.Status(statusCtx)
				cancelled := statusCtx.Err() != nil
				statusCancel()
				// A status error is the store's own structured signal that it
				// needs attention — a condition the user can still act on after
				// the session ends, so it is reported through the same
				// user-visible warning the agent's capture hooks use.
				if statusErr == nil {
					// Healthy at close: clear any suppression so a future run's
					// first problem is visible again.
					rt.SnapshotWarnings.Reset()
				} else {
					rt.SnapshotWarnings.ReportMaintenance(rt.State,
						agent.SnapshotPhaseMaintenance, statusErr, cancelled)
				}
			}
			closeCancel()
		}

		// 7. database.
		if rt.DB != nil {
			if err := rt.DB.Close(); err != nil {
				errs = append(errs, fmt.Errorf("db close: %w", err))
			}
		}

		// 7b. desktop browser session.
		if rt.DesktopCloser != nil {
			rt.DesktopCloser()
		}

		// 8. reverse-order resourceClosers.
		for i := len(rt.resourceClosers) - 1; i >= 0; i-- {
			rt.resourceClosers[i]()
		}

		// 9. idempotent state shutdown.
		if rt.State != nil {
			rt.State.Shutdown()
		}

		// 10. release the pooled language servers and the project host. The
		// last session on a host leaves it lingering (or closes it, for a
		// private registry).
		rt.unbindLSP()
		if rt.Host != nil {
			rt.Host.detach(rt.State)
			rt.Host.release()
		}

		rt.closeErr = errors.Join(errs...)
	})
	return errors.Join(rt.quiesceErr, rt.closeErr)
}

// StartRuntime builds a Runtime from the supplied options. It performs every
// step of session bootstrap that is independent of the transport: working
// directory resolution, trust resolution, config load, .marshal directory
// creation, database open/migrate, project/session ensure, logger setup,
// session.State creation, skill loading, broker construction, and
// buildAgentRunner. The Bubble Tea onboarding gate and the TUI/program
// loop are intentionally left to Run.
//
// F20: when the project is trusted and the user has configured entries, the
// project-local hook runner is wired here so the TUI and any future
// headless transport share one wiring site.
func StartRuntime(ctx context.Context, opts ...Option) (*Runtime, error) {
	runOpts := options{
		now:                    time.Now,
		configWithLayersLoader: config.LoadWithLayers,
		programRunner:          runProgram,
	}
	for _, opt := range opts {
		opt(&runOpts)
	}
	return startRuntime(ctx, runOpts)
}

// startRuntime is the internal implementation shared by Run and StartRuntime.
// It expects a fully-resolved options struct — no option iteration.
func startRuntime(ctx context.Context, runOpts options) (*Runtime, error) {
	// Resolve the credential/OAuth dependency first: an explicitly nil
	// injection is rejected before any state is created, and an omitted one
	// selects the production default exactly once. The value is carried on the
	// Runtime so every rebuild path reuses this same object.
	authResolver, err := resolveAuthResolver(runOpts)
	if err != nil {
		return nil, err
	}

	workingDir, err := resolveWorkingDir(runOpts.workingDir)
	if err != nil {
		return nil, err
	}

	// Default config reloader for headless runtimes: every Runtime must carry
	// a working reload handle, not just those built by the TUI. When a caller
	// (Run) already supplied runOpts.configReloader it wins; when none did
	// (StartRuntime / ACP / CLI / embedded), the runtime self-wires the same
	// in-place rebuild the TUI uses. The closure captures rt by reference —
	// rt is assigned at the Runtime literal below — so the handle works as
	// soon as construction succeeds. Mirrors the two-phase capture in Run
	// (app.go:1928-1932).
	var rt *Runtime
	if runOpts.configReloader == nil {
		runOpts.configReloader = func(newCfg config.Config) error {
			if rt == nil {
				return fmt.Errorf("runtime not yet built")
			}
			return reloadAgentRuntime(ctx, newCfg, rt)
		}
	}

	if runOpts.sessionID != "" && runOpts.existingSessionID != "" {
		return nil, fmt.Errorf("app: WithSessionID and WithExistingSession are mutually exclusive")
	}

	homeDir := runOpts.homeDir
	if homeDir == "" {
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find home directory: %w", err)
		}
	}
	dataDir := config.DataDir(homeDir)

	var cfg config.Config
	var layers config.Layers
	var projectTrusted, trustPromptPending bool
	if runOpts.trustResolver == nil && runOpts.deferTrustPrompt {
		// Interactive TUI: decide without prompting; the TUI asks inline.
		store := trust.NewStore(dataDir)
		decision, needsPrompt, derr := trust.Evaluate(store, workingDir)
		if derr != nil {
			return nil, fmt.Errorf("evaluate project trust: %w", derr)
		}
		if runOpts.sessionTrusted {
			decision, needsPrompt = trust.DecisionTrustSession, false
		}
		trustPromptPending = needsPrompt
		projectTrusted = decision == trust.DecisionTrustPermanent || decision == trust.DecisionTrustSession
		loadOpts := config.LoadOptions{
			WorkingDir:        workingDir,
			HomeDir:           homeDir,
			SkipProjectConfig: needsPrompt,
		}
		cfg, layers, err = runOpts.configWithLayersLoader(loadOpts)
	} else {
		resolver := runOpts.trustResolver
		if resolver == nil {
			resolver = trust.NewTerminalResolver(trust.NewStore(dataDir))
		}
		loader := func(lo config.LoadOptions) (config.Config, config.Layers, error) {
			lo.TrustResolver = resolver
			return runOpts.configWithLayersLoader(lo)
		}
		loadOpts := config.LoadOptions{WorkingDir: workingDir, HomeDir: homeDir, Trusted: &projectTrusted}
		cfg, layers, err = loader(loadOpts)
	}
	if err != nil {
		return nil, err
	}
	if !runOpts.sessionRouting.Empty() {
		if err := cfg.ValidateSessionRouting(runOpts.sessionRouting); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidSessionRouting, err)
		}
		cfg.SessionRouting = runOpts.sessionRouting
	}

	if err := os.MkdirAll(filepath.Join(workingDir, ".marshal"), 0755); err != nil {
		return nil, fmt.Errorf("create .marshal directory: %w", err)
	}
	// Best-effort: a failure here must not stop startup, but it does mean
	// the user's config could be committed, so it is worth logging.
	if err := config.EnsureMarshalIgnored(workingDir); err != nil {
		slog.Warn("could not update .gitignore", "error", err)
	}
	// Machine-local state stays out of git even when .marshal/config.toml
	// is committed. Best-effort as well.
	if err := config.EnsureMarshalDirIgnored(workingDir); err != nil {
		slog.Warn("could not write .marshal/.gitignore", "error", err)
	}

	// The project host sets the database up once for every session in this
	// process (stray-database adoption, migration, and reconciling the
	// generations a crashed process left open); this session then opens its
	// own connection. Without WithHosts the runtime gets a private registry
	// whose host does not index and closes with the runtime.
	hosts := runOpts.hosts
	if hosts == nil {
		hosts = newHosts(false, 0)
	}
	host, err := hosts.acquire(workingDir, runOpts.now())
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			host.release()
		}
	}()

	database, err := db.Open(db.Path(workingDir))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	var projectID int64
	var now time.Time
	sessionID := runOpts.sessionID

	if runOpts.existingSessionID != "" {
		// ── Existing-session mode ────────────────────────────────────
		project, err := database.GetProjectByRoot(workingDir)
		if err != nil {
			_ = database.Close()
			return nil, fmt.Errorf("get project by root: %w", err)
		}
		projectID = project.ID

		storedSession, err := database.GetSession(runOpts.existingSessionID)
		if err != nil {
			_ = database.Close()
			return nil, fmt.Errorf("get existing session: %w", err)
		}
		if storedSession.ProjectID != projectID {
			_ = database.Close()
			return nil, fmt.Errorf("session project mismatch: session belongs to project %d, but working directory resolves to project %d", storedSession.ProjectID, projectID)
		}

		sessionID = runOpts.existingSessionID
		now = storedSession.StartedAt
	} else {
		// ── New-session mode ────────────────────────────────────────
		var err error
		projectID, err = database.GetOrCreateProject(workingDir, cfg.Project.Name)
		if err != nil {
			_ = database.Close()
			return nil, fmt.Errorf("get or create project: %w", err)
		}

		now = runOpts.now()
		if sessionID == "" {
			sessionID = fmt.Sprintf("sess_%d", now.UnixNano())
		}
		if err := database.CreateSession(sessionID, projectID, "", now); err != nil {
			_ = database.Close()
			return nil, fmt.Errorf("create session: %w", err)
		}
	}

	logWriter := io.Discard
	logFile, lerr := os.OpenFile(logPath(workingDir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if lerr == nil {
		logWriter = logFile
	} else {
		logFile = nil
	}
	logger := logging.New(logWriter, slog.LevelInfo, false)
	// Install as the process-wide default so stray package-level slog calls
	// (lsp, db, sandbox, tools, …) land in the log file instead of on stderr,
	// where they would garble the TUI frame (no alt-screen mode is used).
	slog.SetDefault(logger)

	globalSkillsDir := filepath.Join(config.UserDir(homeDir), "skills")
	projectSkillsDir := filepath.Join(workingDir, ".marshal", "skills")
	skillIndex, err := skills.LoadSkills(globalSkillsDir, projectSkillsDir)
	if err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		_ = database.Close()
		return nil, fmt.Errorf("load skills: %w", err)
	}

	pluginCommands := loadPluginContents(&cfg, skillIndex, homeDir, workingDir, projectTrusted, logger)

	state := session.New(cfg, workingDir, now, session.Persistence{DB: database, SessionID: sessionID, Logger: logger}, session.WithSubagentMaxConcurrency(cfg.Agent.MaxConcurrentSubagents))
	state.SetTrusted(projectTrusted)
	// Surface the merged-config layering snapshot to commands and the TUI.
	// startRuntime already produced `layers` via LoadLayers; copying it
	// onto the state lets /doctor and the diagnostic indicator see real
	// provenance instead of always reporting "default".
	if err == nil {
		state.SetLayers(layers)
	}

	// Abort startup if an existing session's transcript cannot be loaded.
	if runOpts.existingSessionID != "" {
		if loadErr := state.LoadError(); loadErr != nil {
			if logFile != nil {
				_ = logFile.Close()
			}
			_ = database.Close()
			return nil, fmt.Errorf("load existing session state: %w", loadErr)
		}
	}

	autoloadSkills(cfg, skillIndex, state, logger)

	workCtx, workCancel := context.WithCancel(ctx)
	jobBroker := pubsub.NewBroker[native.JobEvent]()
	watchBroker := pubsub.NewBroker[watch.Event]()
	steeringBroker := pubsub.NewBroker[session.SteeringEvent]()
	eventBroker := pubsub.NewBroker[session.Event]()
	workspaceBroker := pubsub.NewBroker[session.WorkspaceEvent]()
	subagentBroker := pubsub.NewBroker[session.SubagentEvent]()
	indexBroker := pubsub.NewBroker[index.Report]()
	state.SetSteeringBroker(steeringBroker)
	state.SetEventBroker(eventBroker)
	state.SetWorkspaceBroker(workspaceBroker)
	state.SetSubagentBroker(subagentBroker)

	// One stable write lock for the whole runtime: the parent runner and
	// every agent.run child serialize writes on it, and config reloads reuse
	// it so in-flight background children keep sharing the same gate.
	writeLock := &swarm.WriteLock{}
	// Allocate the auto-resume hook cell before the builder: the OnFire
	// closure built inside it captures the cell, and the OnFire closure is
	// built before the Runtime exists.
	watchResumeCell := &WatchResumeHook{}
	// The runtime's one LSP handle. It starts empty; bindLSP points it at
	// the host's pooled servers once the runtime exists.
	lspHandle := lsp.NewHandle(nil, nil, logger)
	runner, toolReg, swarmRunner, mcpMgr, snapSvc, commandService, watchMgr, desktopCloser, subagentFactory, pipelineFactory, planAuthorFactory, swarmOverrideFactory, err := buildAgentRunnerWithLock(workCtx, cfg, state, database, projectID, skillIndex, dataDir, runOpts.additionalDirs, jobBroker, watchBroker, watchResumeCell, runOpts.configReloader, homeDir, writeLock, nil, lspHandle, authResolver)
	if err == nil && state.Trusted() && len(cfg.Hooks.Entries) > 0 {
		runner.HookRunner = hooks.NewRunnerFromConfig(cfg.Hooks)
	}
	if err != nil {
		state.SetNotice(session.Notice{
			Category: session.NoticeProvider,
			Severity: session.SeverityError,
			Message:  err.Error(),
			Hint:     "Fix the provider configuration, then apply any settings change or restart to retry.",
			Source:   "startup",
		})
	}

	cmdReg := commands.New()
	// Surfaced as an internal notice rather than aborting startRuntime: a
	// registration failure here is a programming error (duplicate/invalid
	// command), not a provider problem and not a reason to fail the whole
	// runtime construction.
	if regErr := commands.RegisterAll(cmdReg, toolReg); regErr != nil {
		state.SetNotice(session.Notice{
			Category: session.NoticeInternal,
			Severity: session.SeverityError,
			Message:  regErr.Error(),
			Hint:     "This is a bug — please report it: a built-in command failed to register.",
			Source:   "command-registration",
		})
	}
	for _, cmd := range pluginCommands {
		if regErr := cmdReg.Register(cmd); regErr != nil {
			logger.Warn("skipping plugin command", "command", cmd.Name, "error", regErr)
		}
	}

	rt = &Runtime{
		Config:               cfg,
		Layers:               layers,
		State:                state,
		Runner:               runner,
		ToolRegistry:         toolReg,
		SwarmRunner:          swarmRunner,
		PipelineFactory:      pipelineFactory,
		SwarmOverrideFactory: swarmOverrideFactory,
		PlanAuthorFactory:    planAuthorFactory,
		DB:                   database,
		ProjectID:            projectID,
		SessionID:            sessionID,
		JobBroker:            jobBroker,
		WatchBroker:          watchBroker,
		SteeringBroker:       steeringBroker,
		EventBroker:          eventBroker,
		WorkspaceBroker:      workspaceBroker,
		SubagentBroker:       subagentBroker,
		IndexBroker:          indexBroker,
		CommandService:       commandService,
		JobManager:           commandServiceManager(commandService),
		WatchManager:         watchMgr,
		WatchResume:          watchResumeCell,
		DesktopCloser:        desktopCloser,
		CustomAgentFactory:   subagentFactory,
		WriteLock:            writeLock,
		Logger:               logger,
		WorkingDir:           workingDir,
		HomeDir:              homeDir,
		DataDir:              dataDir,
		SkillIndex:           skillIndex,
		PluginCommands:       pluginCommands,
		CommandRegistry:      cmdReg,
		TrustPromptPending:   trustPromptPending,
		LSPManager:           lspHandle,
		Host:                 host,
		ConfigReloader:       runOpts.configReloader,
		AuthResolver:         authResolver,
		additionalDirs:       runOpts.additionalDirs,
		workCtx:              workCtx,
		workCancel:           workCancel,
	}
	// One snapshot-warning state shared by the runtime and its runner, so a
	// pre-write capture warning and a maintenance warning about the same
	// workspace and reason are one visible event, and a later successful
	// capture clears whichever of them was showing. It is created even when
	// snapshots are disabled, because the close-time maintenance path always
	// runs; the state itself is nil-receiver safe, so tests that build a
	// Runtime literal are unaffected.
	rt.SnapshotWarnings = agent.NewSnapshotWarningState()
	if rt.Runner != nil {
		rt.Runner.SetSnapshotWarningState(rt.SnapshotWarnings)
	}
	if err == nil {
		rt.startSnapshotMaintenance(snapSvc, runner, state, cfg.Snapshots.RetentionDays)
	}
	// Attach the session to its host (index progress, the host's current
	// config; the first session starts the index) and point the LSP handle
	// at the host's pooled servers for the session's active root. Re-point it
	// on every WorkspaceEvent, so hover / references / definition follow the
	// session into and out of a worktree, sharing servers with any other
	// session on the same root. workCancel (from Quiesce) ends the
	// subscription; Close releases the last binding.
	host.attach(state, cfg, projectID)
	rt.bindLSP(cfg, state.Workspace().ActiveRoot)
	go func() {
		ch := workspaceBroker.Subscribe(rt.workCtx)
		for ev := range ch {
			rt.mu.Lock()
			current := rt.Config
			rt.mu.Unlock()
			rt.bindLSP(current, ev.Payload.Workspace.ActiveRoot)
		}
	}()
	// Assign MCP and snapshot only on success AND only when the underlying
	// concrete pointer is non-nil. A nil *mcp.Manager assigned to an MCPCloser
	// interface would create a non-nil interface wrapping nil — causing the
	// != nil guard in Close to pass while the method call panics.
	if err == nil {
		if mcpMgr != nil {
			rt.MCPManager = mcpMgr
		}
		if snapSvc != nil {
			rt.Snapshot = snapSvc
		}
	}
	if logFile != nil {
		rt.resourceClosers = append(rt.resourceClosers, func() { _ = logFile.Close() })
	}
	// The conversation's work projection. It is created once, after the source
	// owners and the State exist and before any transport can observe the
	// Runtime, with its initial snapshot seeded synchronously and one background
	// collector owned here (stopped and joined by Quiesce/Close). It subscribes
	// only the already-created brokers; no new source is minted for it.
	rt.work = newWorkCoordinator(rt.workCtx, state, workSources{
		Jobs:           rt.JobManager,
		Watches:        rt.WatchManager,
		JobEvents:      jobBroker,
		WatchEvents:    watchBroker,
		SubagentEvents: subagentBroker,
		SessionEvents:  eventBroker,
	}, runOpts.now)
	started = true
	return rt, nil
}

// autoloadSkills injects the configured always-on skills into the session
// before the first turn.
//
// Relying on the model to call skill.load for a suite's entry point does not
// work in practice: the entry point is exactly the skill that teaches the
// model to reach for skills at all, so nothing prompts it to load that one.
// Autoloaded skills bypass the decision entirely.
//
// Failures are logged and skipped. An uninstalled skill named in config, or
// one too large for the remaining budget, must not stop the session.
func autoloadSkills(cfg config.Config, idx *skills.Index, state *session.State, logger *slog.Logger) {
	for _, name := range cfg.Skills.Autoload {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := skills.LoadSkillIntoSessionQuiet(idx, state, name); err != nil {
			logger.Warn("skipping autoload skill", "skill", name, "error", err)
			continue
		}
		logger.Info("autoloaded skill", "skill", name)
	}
}

// NewSession creates a brand-new session (new DB row, new SessionID, fresh
// State) and rebuilds the agent runtime against it. On success it swaps the
// new state/runner into rt and returns the new pieces; on failure it leaves
// the current session untouched and returns the error.
//
// It is serialized against config reloads and against an end acceptance
// (Task 8, step 7). A replacement accepted through AcceptEnd freezes the old
// conversation first, so a NewSession that races an accepted end loses cleanly
// instead of starting a second conversation beside a stopping one; and nothing
// here mutates the runtime's state, owner or resume intent until the candidate
// build has succeeded. A failed build leaves the old conversation frozen, with
// an explicit error: reopening it implicitly would put a live conversation back
// beside whatever the replacement was for, and transferring its jobs would give
// them an owner that never admitted them.
func (rt *Runtime) NewSession(name string) (*session.State, *agent.Runner, *swarm.Orchestrator, func(planPath string, overrides map[routing.AgentRole]string) tui.AgentRunner, sddauthor.Factory, tui.SwarmOverrideFactory, *registry.Registry, error) {
	// Serialize the whole replacement against config reloads, and hold that gate
	// across the entire build/install transaction: a concurrent config reload or
	// access resolution must not observe (or apply to) a half-installed session,
	// and the resolver must never run against a State that is being replaced
	// underneath it. A reload rebuilds the same pointers this function swaps, and
	// both commit launch generations on the service.
	rt.reloadMu.Lock()
	defer rt.reloadMu.Unlock()

	rt.endMu.Lock()
	replacing := rt.endAccepted && rt.endIntended == string(EndIntentReplace)
	rt.endMu.Unlock()

	return rt.newSessionLocked(name, replacing)
}

// newSessionLocked is NewSession's body. It expects reloadMu held.
func (rt *Runtime) newSessionLocked(name string, replacing bool) (*session.State, *agent.Runner, *swarm.Orchestrator, func(planPath string, overrides map[routing.AgentRole]string) tui.AgentRunner, sddauthor.Factory, tui.SwarmOverrideFactory, *registry.Registry, error) {
	db := must[*db.DB](rt.DB)
	jb := must[*pubsub.Broker[native.JobEvent]](rt.JobBroker)

	now := time.Now()
	sessionID := fmt.Sprintf("sess_%d", now.UnixNano())
	if err := db.CreateSession(sessionID, rt.ProjectID, name, now); err != nil {
		return nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("create session: %w", err)
	}

	newState := session.New(rt.Config, rt.WorkingDir, now, session.Persistence{DB: db, SessionID: sessionID, Logger: rt.Logger}, session.WithSubagentMaxConcurrency(rt.Config.Agent.MaxConcurrentSubagents))
	if name != "" {
		newState.SetTitleManual(name)
		// Persist the manual flag; the title itself was written by
		// CreateSession above. Keeps re-titling treating the name as
		// manual across restarts.
		_ = db.UpdateSessionTitle(sessionID, name, true)
	}
	newState.SetTrusted(rt.State.Trusted())
	// rt.Layers is a startup snapshot: the TUI refreshes its own layer
	// snapshot after each config save but not the runtime's, so seeding a
	// new session from rt.Layers would make its first project-scope save
	// diff against the stale user layer and re-bake user-global values into
	// the committable .marshal/config.toml. Reload with the same
	// non-prompting trust replay used at startup; on error keep the startup
	// snapshot rather than fail session creation.
	layers := rt.Layers
	if fresh, err := config.LoadSessionLayers(rt.HomeDir, rt.WorkingDir, rt.State.Trusted()); err == nil {
		layers = fresh
		rt.Layers = fresh
	} else if rt.Logger != nil {
		rt.Logger.Warn("layer reload for new session failed; using startup snapshot", "error", err)
	}
	newState.SetLayers(layers)
	if rt.SteeringBroker != nil {
		newState.SetSteeringBroker(must[*pubsub.Broker[session.SteeringEvent]](rt.SteeringBroker))
	}
	if rt.EventBroker != nil {
		newState.SetEventBroker(must[*pubsub.Broker[session.Event]](rt.EventBroker))
	}
	if rt.WorkspaceBroker != nil {
		newState.SetWorkspaceBroker(must[*pubsub.Broker[session.WorkspaceEvent]](rt.WorkspaceBroker))
	}
	if rt.SubagentBroker != nil {
		newState.SetSubagentBroker(must[*pubsub.Broker[session.SubagentEvent]](rt.SubagentBroker))
	}
	autoloadSkills(rt.Config, rt.SkillIndex, newState, rt.Logger)

	// Reuse the runtime's stable WriteLock so a /session new keeps the same
	// gate across the swap (in-flight children from the old session hold it),
	// and hand the rebuild the same auth resolver the runtime selected: a new
	// session must not silently reacquire the production credential backend.
	lock := rt.WriteLock
	if lock == nil {
		lock = &swarm.WriteLock{}
	}
	newRunner, newReg, newSwarmRunner, newMCP, newSnap, newJobMgr, newWatchMgr, newDesktopCloser, newSubagentFactory, newPipelineFactory, newPlanAuthorFactory, newSwarmOverrideFactory, err := buildAgentRunnerWithLock(
		rt.workCtx, rt.Config, newState, db, rt.ProjectID, rt.SkillIndex, rt.DataDir, rt.additionalDirs, jb, must[*pubsub.Broker[watch.Event]](rt.WatchBroker), rt.WatchResume, rt.ConfigReloader, rt.HomeDir, lock, nil, rt.LSPManager, rt.AuthResolver,
	)
	if err != nil {
		// Roll back the empty session row so /sessions stays clean.
		_, _ = db.DeleteSession(context.Background(), sessionID)
		rt.Logger.Warn("new session: runner build failed; keeping previous session", "error", err)
		// Nothing about the runtime's state, owner or resume intent has been
		// mutated: the old conversation is exactly as it was before this call,
		// except that an accepted replacement has already frozen it. The error is
		// explicit about the way out — retry the replacement, or exit — because
		// reopening the old conversation implicitly would put a live conversation
		// back beside whatever the replacement was for.
		hint := "The previous session is still active. Fix the provider configuration and try /session new again."
		if replacing {
			hint = "The previous conversation stays frozen: retry the replacement once the provider is fixed, or exit Marshal. Its work is not reopened or transferred."
		}
		rt.State.SetNotice(session.Notice{
			Category: session.NoticeProvider,
			Severity: session.SeverityError,
			Message:  err.Error(),
			Hint:     hint,
			Source:   "session-new",
		})
		return nil, nil, nil, nil, nil, nil, nil, err
	}

	// Success — settle the outgoing session's pending prompts before the swap.
	// A tool blocked on an access request for the old conversation must
	// unblock with a cancellation rather than wait on managers that are about
	// to close, and the closed gate rejects any late registration against the
	// state being retired. Both calls run outside Runtime.mu: they take the
	// session's own locks and publish events.
	rt.mu.Lock()
	oldState := rt.State
	rt.mu.Unlock()
	if oldState != nil {
		oldState.BeginQuiesce()
		oldState.ResolvePendingForShutdown()
	}

	// Swap under the runtime lock and clean up old resources outside it.
	rt.mu.Lock()
	oldRunner := rt.Runner
	oldMCP := rt.MCPManager
	// The replaced conversation's own command supervisor. It is NOT the one
	// the candidate borrowed (the candidate got a fresh owner), so it is
	// genuinely retired here: every session-scoped identity it held belongs to
	// the session being replaced. A Runtime literal that predates the service
	// field still carries its manager, so adopt it rather than leaking it.
	oldCommandService := rt.CommandService
	if oldCommandService == nil && rt.JobManager != nil {
		oldCommandService = &conversationCommandService{manager: rt.JobManager, generation: 1}
	}
	oldWatchMgr := rt.WatchManager
	oldDesktopCloser := rt.DesktopCloser

	rt.State = newState
	rt.SessionID = sessionID
	rt.Runner = newRunner
	// A new session shares the runtime's snapshot-warning suppression state,
	// so the swap does not re-warn about a condition the runtime already
	// reported to this user.
	if rt.SnapshotWarnings != nil {
		newRunner.SetSnapshotWarningState(rt.SnapshotWarnings)
	}
	rt.ToolRegistry = newReg
	rt.SwarmRunner = newSwarmRunner
	rt.PipelineFactory = newPipelineFactory
	rt.PlanAuthorFactory = newPlanAuthorFactory
	rt.CustomAgentFactory = newSubagentFactory
	// A replacement session is a NEW conversation, so it gets its own
	// command supervisor: a fresh session id, a fresh transcript and a fresh
	// running-count callback all have to agree on which service they mean.
	// The previous conversation's service is torn down below with the rest of
	// the replaced generation.
	rt.CommandService = newJobMgr
	rt.JobManager = newJobMgr.manager
	rt.WatchManager = newWatchMgr
	rt.DesktopCloser = newDesktopCloser
	if newMCP != nil {
		rt.MCPManager = newMCP
	} else {
		rt.MCPManager = nil
	}
	if newSnap != nil {
		rt.Snapshot = newSnap
	} else {
		rt.Snapshot = nil
	}
	rt.mu.Unlock()

	if oldMCP != nil {
		_ = oldMCP.Close()
	}
	if oldCommandService != nil && oldCommandService.manager != nil && oldCommandService != rt.CommandService {
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = oldCommandService.manager.Shutdown(sc)
	}
	if oldWatchMgr != nil {
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = oldWatchMgr.Close(sc)
	}
	if oldDesktopCloser != nil {
		oldDesktopCloser()
	}
	if oldRunner != nil && oldRunner.Rollover != nil {
		_ = oldRunner.Rollover.Close(context.Background())
	}
	// The replacement session starts at the project root: move the host's
	// progress to it and point the LSP handle at the root's pooled servers.
	if rt.Host != nil {
		rt.Host.detach(oldState)
		rt.Host.attach(newState, rt.Config, rt.ProjectID)
	}
	rt.bindLSP(rt.Config, newState.Workspace().ActiveRoot)
	// Close the replaced conversation's projection. Its sources are stopped
	// above, so this reconciles and finalizes the last durable facts while the
	// old State is still live, then cancels, joins and closes its broker. Doing
	// it BEFORE the replacement's coordinator is created is what guarantees no
	// old queued callback or subscription can publish into the new one.
	rt.mu.Lock()
	oldWork := rt.work
	rt.work = nil
	rt.mu.Unlock()
	if oldWork != nil {
		if err := oldWork.finalize(); err != nil && rt.Logger != nil {
			rt.Logger.Warn("replaced conversation work view finalize failed", "error", err)
		}
		oldWork.close()
	}
	oldState.Shutdown()

	// The replacement is complete: the old conversation is gone and the new one
	// owns the runtime. A replacement NEVER runs the exit postmortem, so the
	// runtime's exit phase returns to open for the new conversation rather than
	// lingering in a stopping state that would refuse the new session's work.
	rt.endMu.Lock()
	rt.endAccepted = false
	rt.endIntended = ""
	rt.endPhase = EndPhaseOpen
	rt.endErr = nil
	rt.analysisScope = nil
	rt.endMu.Unlock()
	newState.SetWorkPhase(session.ExitPhaseOpen)

	// The replacement conversation gets its own projection over the same source
	// brokers and the freshly built owners. It starts from the new State alone,
	// so it carries no attention or receipt counters from the conversation it
	// replaced; the old coordinator was closed above, before this one existed.
	newWork := newWorkCoordinator(rt.workCtx, newState, workSources{
		Jobs:           newJobMgr.manager,
		Watches:        newWatchMgr,
		JobEvents:      jb,
		WatchEvents:    must[*pubsub.Broker[watch.Event]](rt.WatchBroker),
		SubagentEvents: must[*pubsub.Broker[session.SubagentEvent]](rt.SubagentBroker),
		SessionEvents:  must[*pubsub.Broker[session.Event]](rt.EventBroker),
	}, time.Now)
	rt.mu.Lock()
	rt.work = newWork
	rt.mu.Unlock()

	return newState, newRunner, newSwarmRunner, newPipelineFactory, newPlanAuthorFactory, newSwarmOverrideFactory, newReg, nil
}

// resolveWorkingDir resolves the session working directory. When the
// directory (or an explicit override) sits inside a git repository, the
// repository root is returned, so launching from a subdirectory lands in
// the same project — same config, database, trust record, and .marshal/
// directory — as launching from the root. Non-git directories are
// returned unchanged.
func resolveWorkingDir(override string) (string, error) {
	dir := override
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("find working directory: %w", err)
		}
		dir = wd
	}
	if root := repo.FindRoot(dir); root != "" {
		return root, nil
	}
	return dir, nil
}

// ═══════════════════════════════════════════════════════════════════════════
// Conversation work projection and action facade (Task 13)
//
// The workCoordinator is one conversation's recoverable view over its existing
// owners (the command service, the subagent registry and the watch manager)
// plus the exact actions a client may request for one item. It supervises no
// execution of its own: it holds no process handles, runs no timer and starts
// no second stop registry. Every action delegates to the owner that already
// owns the work.
// ═══════════════════════════════════════════════════════════════════════════

const (
	// Display-string caps applied at the projection boundary. Canonical
	// identities stay exact opaque data and are never passed through these.
	workLabelRunes    = 80
	workOwnerRunes    = 80
	workActivityRunes = 160
	workWarningRunes  = 256
	// workTailBytes and workInspectBytes bound a retained tail and an
	// inspected report/output respectively.
	workTailBytes    = 8 << 10
	workInspectBytes = 32 << 10
	// workTerminalDetails is how many newest settled items the live snapshot
	// carries; the full history index keeps the rest for paging.
	workTerminalDetails = 32
	// workReconcileEvery is the idle reconciliation cadence and
	// workPublishInterval the cosmetic publish throttle.
	workReconcileEvery  = 200 * time.Millisecond
	workPublishInterval = 200 * time.Millisecond
	// workRetryEvery bounds how often the normal runtime re-offers a dirty
	// durable write. A progress tick never forces one: a clean view is left
	// alone, so only work that actually failed to store is retried.
	workRetryEvery = time.Second

	// workSnapshotVersion is the projection's wire version.
	workSnapshotVersion = 1
)

// The normalized work-state vocabulary. The strings are the values the
// viewmodel helpers switch on.
const (
	workStateRunning   = "running"
	workStateStopping  = "stopping"
	workStateWatching  = "watching"
	workStateBlocked   = "blocked"
	workStateCompleted = "completed"
	workStateStopped   = "stopped"
	workStateFailed    = "failed"
	workStateLost      = "lost"
	workStateUnknown   = "unknown"
)

// Work kinds. The agent/watch kinds match the session work-index kinds so a
// saved result and a live source normalize onto the same identity.
const (
	workKindCommand = "command"
	workKindAgent   = "agent"
	workKindWatch   = "watch"
)

// workBlockedSourceID names the projection's single blocked-agent slot: a live
// agent waiting on a canonical pending request.
const workBlockedSourceID = "prompt"

// legacyWorkEpoch is the stable epoch stamped on a command record that carries
// an execution identity but no source epoch of its own — a record written
// before the epoch was recorded. The live manager epoch is minted per process,
// so falling back to it would give the same durable record a different
// canonical key on every restart. A record with no execution identity uses the
// same epoch, sharing the "legacy" naming.
const legacyWorkEpoch = "legacy"

// workEpochSeq mints process-unique coordinator epochs. An epoch is opaque and
// never sortable; it binds a client's cursor and a source identity.
var workEpochSeq atomic.Uint64

// workSources are the nonterminal source brokers and managers a coordinator
// reconciles. Any field may be nil.
type workSources struct {
	Jobs           *native.JobManager
	Watches        *watch.Manager
	JobEvents      *pubsub.Broker[native.JobEvent]
	WatchEvents    *pubsub.Broker[watch.Event]
	SubagentEvents *pubsub.Broker[session.SubagentEvent]
	SessionEvents  *pubsub.Broker[session.Event]
}

// workCandidate is one normalized item plus whether its terminal fact is still
// settling its first durable save.
type workCandidate struct {
	item     viewmodel.WorkItem
	settling bool
}

// workCoordinator is one conversation's private projection and action facade.
type workCoordinator struct {
	mu sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	state   *session.State
	jobs    *native.JobManager
	watches *watch.Manager
	now     func() time.Time

	epoch  string
	broker *pubsub.Broker[viewmodel.WorkSnapshot]

	snapshot viewmodel.WorkSnapshot
	history  []viewmodel.WorkItem
	acks     map[string]string

	sourceEpochs    map[string]string
	sourceRevisions map[string]uint64

	historyRevision      uint64
	revision             uint64
	publishedFingerprint string
	historyFingerprint   string
	lastLifecycle        string
	lastPublish          time.Time
	lastRetry            time.Time

	dirty chan struct{}
}

// newWorkCoordinator captures immutable conversation ownership and a new
// coordinator epoch. It subscribes the nonterminal source brokers BEFORE the
// initial source snapshots so no change between subscription and the first
// read is lost, reconciles once synchronously, then reconciles on every wake
// and at least every 200 ms. Callers stop it with close.
func newWorkCoordinator(ctx context.Context, state *session.State, sources workSources, now func() time.Time) *workCoordinator {
	if ctx == nil {
		ctx = context.Background()
	}
	if now == nil {
		now = time.Now
	}
	cctx, cancel := context.WithCancel(ctx)
	wc := &workCoordinator{
		ctx:             cctx,
		cancel:          cancel,
		state:           state,
		jobs:            sources.Jobs,
		watches:         sources.Watches,
		now:             now,
		epoch:           "work-" + strconv.FormatUint(workEpochSeq.Add(1), 36),
		broker:          pubsub.NewBroker[viewmodel.WorkSnapshot](),
		acks:            map[string]string{},
		sourceEpochs:    map[string]string{},
		sourceRevisions: map[string]uint64{},
		dirty:           make(chan struct{}, 1),
	}
	wc.subscribeSources(sources)
	wc.reconcileWork(now())
	wc.wg.Add(1)
	go wc.loop()
	return wc
}

// subscribeSources subscribes every configured broker before the initial
// snapshot read. Each subscription is only a wake: a late event never replaces
// authoritative state, so a dropped event is repaired by the next reconcile.
func (wc *workCoordinator) subscribeSources(s workSources) {
	if s.JobEvents != nil {
		ch := s.JobEvents.Subscribe(wc.ctx)
		wc.wg.Add(1)
		go func() {
			defer wc.wg.Done()
			for range ch {
				wc.wake()
			}
		}()
	}
	if s.WatchEvents != nil {
		ch := s.WatchEvents.Subscribe(wc.ctx)
		wc.wg.Add(1)
		go func() {
			defer wc.wg.Done()
			for range ch {
				wc.wake()
			}
		}()
	}
	if s.SubagentEvents != nil {
		ch := s.SubagentEvents.Subscribe(wc.ctx)
		wc.wg.Add(1)
		go func() {
			defer wc.wg.Done()
			for range ch {
				wc.wake()
			}
		}()
	}
	if s.SessionEvents != nil {
		ch := s.SessionEvents.Subscribe(wc.ctx)
		wc.wg.Add(1)
		go func() {
			defer wc.wg.Done()
			for range ch {
				wc.wake()
			}
		}()
	}
}

// wake is a nonblocking signal that a source moved. A dropped wake is harmless:
// the 200 ms cadence reconciles anyway.
func (wc *workCoordinator) wake() {
	select {
	case wc.dirty <- struct{}{}:
	default:
	}
}

// loop reconciles on every wake and at least every workReconcileEvery, so an
// idle conversation still reflects a change that no broker announced. It also
// re-offers a dirty durable write at most once per workRetryEvery, outside
// every source lock.
func (wc *workCoordinator) loop() {
	defer wc.wg.Done()
	ticker := time.NewTicker(workReconcileEvery)
	defer ticker.Stop()
	for {
		select {
		case <-wc.ctx.Done():
			return
		case <-wc.dirty:
		case <-ticker.C:
		}
		now := wc.now()
		wc.reconcileWork(now)
		wc.retryPersistence(now)
	}
}

// retryPersistence re-offers a dirty work view and any failed watch terminal
// save, at most once per workRetryEvery. It reads the source pointers under the
// coordinator mutex but calls the (potentially blocking) retries outside every
// lock. It writes nothing when there is nothing pending, so an idle progress
// tick can never turn into a clean-state write.
func (wc *workCoordinator) retryPersistence(now time.Time) {
	wc.mu.Lock()
	if !wc.lastRetry.IsZero() && now.Sub(wc.lastRetry) < workRetryEvery {
		wc.mu.Unlock()
		return
	}
	wc.lastRetry = now
	watches := wc.watches
	wc.mu.Unlock()

	if wc.state != nil {
		_ = wc.state.RetryWorkViewSave()
	}
	if watches != nil {
		_ = watches.RetryTerminalSaves()
	}
}

// finalize reconciles the projection one last time from the now-stopped
// sources and re-offers every durable save that failed, so the final facts are
// stored while persistence is still open. A failed retry is reported to the
// caller; it never announces a durable acknowledgment.
func (wc *workCoordinator) finalize() error {
	if wc == nil {
		return nil
	}
	wc.reconcileWork(wc.now())
	var errs []error
	if wc.state != nil {
		if err := wc.state.RetryWorkViewSave(); err != nil {
			errs = append(errs, fmt.Errorf("persist work view: %w", err))
		}
	}
	wc.mu.Lock()
	watches := wc.watches
	wc.mu.Unlock()
	if watches != nil {
		if err := watches.RetryTerminalSaves(); err != nil {
			errs = append(errs, fmt.Errorf("persist watch results: %w", err))
		}
	}
	// Reflect whatever the retries changed so a final observer sees the stored
	// facts and their cleared attention.
	wc.reconcileWork(wc.now())
	return errors.Join(errs...)
}

// rebindWatchManager binds a replacement watch manager after its predecessor
// has been joined and its terminal snapshot reconciled. The new manager mints
// its own epoch, so an old registration's numeric ID is never mistaken for one
// of the replacement's: old results stay history under their own epoch.
func (wc *workCoordinator) rebindWatchManager(w *watch.Manager) {
	if wc == nil {
		return
	}
	wc.mu.Lock()
	wc.watches = w
	wc.mu.Unlock()
	wc.reconcileWork(wc.now())
	wc.wake()
}

// close cancels and joins the coordinator's goroutines and closes its broker.
func (wc *workCoordinator) close() {
	if wc == nil {
		return
	}
	wc.cancel()
	wc.wg.Wait()
	wc.broker.Close()
}

// readJobs reads the command service snapshot without the coordinator mutex.
func (wc *workCoordinator) readJobs() *native.JobSnapshot {
	if wc.jobs == nil {
		return nil
	}
	s := wc.jobs.Snapshot()
	return &s
}

// readWatches reads the watch manager snapshot. The manager pointer is read
// under the coordinator mutex (a reload may rebind it) while the snapshot
// itself is taken outside it.
func (wc *workCoordinator) readWatches() *watch.Snapshot {
	wc.mu.Lock()
	w := wc.watches
	wc.mu.Unlock()
	if w == nil {
		return nil
	}
	s := w.Snapshot()
	return &s
}

// reconcileWork rebuilds the projection from the current sources. Source
// snapshots are read outside the coordinator mutex, a new item map is built,
// and only then is it swapped in atomically. The source lifecycle fingerprints
// are re-read after the memory indices; a changed epoch/revision, status,
// attachment, settling set or save-error set retries up to twice, then defers
// to the next wake/tick. Progress-sequence changes never force a retry.
func (wc *workCoordinator) reconcileWork(now time.Time) {
	state := wc.state
	if state == nil {
		return
	}
	conv := state.SessionID()

	var candidates []workCandidate
	var workView = state.WorkView()
	for attempt := 0; ; attempt++ {
		jobs := wc.readJobs()
		subs := state.SubagentSnapshot()
		watchSnap := wc.readWatches()
		workView = state.WorkView()
		exits := state.JobExitSnapshot()

		candidates = wc.buildCandidates(conv, jobs, subs, watchSnap, exits, workView)

		if attempt < 2 {
			jobs2 := wc.readJobs()
			subs2 := state.SubagentSnapshot()
			watchSnap2 := wc.readWatches()
			if sourceFingerprintChanged(jobs, jobs2, subs, subs2, watchSnap, watchSnap2) {
				continue
			}
		}
		wc.recordSourceFingerprints(jobs, subs, watchSnap)
		break
	}

	// The acknowledgment index is the durable view of which terminal results a
	// caller has already seen, keyed by task key and revision.
	acks := make(map[string]string, len(workView.Acknowledged))
	for _, a := range workView.Acknowledged {
		if a.TaskKey != "" {
			acks[a.TaskKey] = a.ResultRevision
		}
	}

	wc.mu.Lock()
	prior := make(map[string]viewmodel.WorkItem, len(wc.history))
	for _, it := range wc.history {
		prior[it.TaskKey] = it
	}
	seen := make(map[string]bool, len(candidates))
	merged := make([]viewmodel.WorkItem, 0, len(candidates)+1)
	for _, c := range candidates {
		if seen[c.item.TaskKey] {
			continue
		}
		seen[c.item.TaskKey] = true
		if c.settling && workItemTerminal(c.item.State) {
			// A terminal fact whose first durable save is in flight: retain the
			// prior published item rather than announce an outcome the next
			// resume could not restore. With no prior item, defer the new fact.
			if prev, ok := prior[c.item.TaskKey]; ok {
				merged = append(merged, prev)
			}
			continue
		}
		merged = append(merged, c.item)
	}
	if p := blockedPromptOf(state); p != nil {
		it := viewmodel.WorkItem{
			TaskKey:        viewmodel.WorkKey(conv, wc.epoch, workKindAgent, workBlockedSourceID),
			DisplayID:      workKindAgent,
			ConversationID: conv,
			RuntimeEpoch:   wc.epoch,
			SourceID:       workBlockedSourceID,
			Kind:           workKindAgent,
			Label:          blockedPromptLabel(p.Kind),
			Owner:          "",
			State:          workStateBlocked,
			StartedAt:      now,
			Prompt:         p,
		}
		if prev, ok := prior[it.TaskKey]; ok {
			it.StartedAt = prev.StartedAt
		}
		it.Capabilities = viewmodel.WorkCapabilities{Inspect: true, ResolvePrompt: true}
		merged = append(merged, it)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if !merged[i].StartedAt.Equal(merged[j].StartedAt) {
			return merged[i].StartedAt.Before(merged[j].StartedAt)
		}
		return merged[i].TaskKey < merged[j].TaskKey
	})
	// Apply the acknowledgment index to the membership: an acknowledged result
	// keeps its evidence but loses its acknowledgment capability, so the change
	// is part of the published semantic snapshot.
	for i := range merged {
		if rev, ok := acks[merged[i].TaskKey]; ok && rev == viewmodel.WorkResultRevision(merged[i]) {
			merged[i].Capabilities.Acknowledge = false
		}
	}
	lifecycle := lifecycleFingerprint(merged)
	urgent := lifecycle != wc.lastLifecycle
	wc.lastLifecycle = lifecycle
	wc.history = merged
	wc.acks = acks
	wc.mu.Unlock()

	wc.publishWork(urgent)
}

// buildCandidates normalizes every source into work items. It is pure with
// respect to the coordinator: it reads only the snapshots handed to it.
func (wc *workCoordinator) buildCandidates(conv string, jobs *native.JobSnapshot, subs session.SubagentSnapshot, watchSnap *watch.Snapshot, exits []session.JobExit, workView db.WorkViewRecord) []workCandidate {
	var out []workCandidate
	out = append(out, wc.commandCandidates(conv, jobs, exits)...)
	out = append(out, wc.subagentCandidates(conv, subs)...)
	if watchSnap != nil {
		out = append(out, watchCandidates(conv, *watchSnap)...)
	}
	out = append(out, resultCandidates(conv, workView, out)...)
	return out
}

// commandCandidates normalizes the command service's live executions and the
// conversation's durable exits. A durable terminal record wins over a live one
// for the same identity; a live running execution always supersedes.
func (wc *workCoordinator) commandCandidates(conv string, jobs *native.JobSnapshot, exits []session.JobExit) []workCandidate {
	var out []workCandidate
	byKey := make(map[string]int)
	add := func(c workCandidate) {
		if i, ok := byKey[c.item.TaskKey]; ok {
			if workItemTerminal(out[i].item.State) && !workItemTerminal(c.item.State) {
				out[i] = c
			}
			return
		}
		byKey[c.item.TaskKey] = len(out)
		out = append(out, c)
	}
	for _, e := range exits {
		if e.Lost {
			// A lost exit is synthesized history: it is not a completion, and
			// the next resume synthesizes it again.
			continue
		}
		add(workCandidate{item: commandExitItem(conv, e)})
	}
	if jobs != nil {
		for _, j := range jobs.Jobs {
			if j.ExecutionID == "" {
				continue
			}
			epoch := j.SourceEpoch
			if epoch == "" {
				epoch = jobs.Epoch
			}
			key := viewmodel.WorkKey(conv, epoch, workKindCommand, j.ExecutionID)
			settling := containsString(jobs.Settling, settlementKeyOf(j))
			it := commandItem(conv, epoch, key, j, jobs.SaveErrors)
			if i, ok := byKey[key]; ok {
				if workItemTerminal(out[i].item.State) {
					continue
				}
				out[i] = workCandidate{item: it, settling: settling}
				continue
			}
			byKey[key] = len(out)
			out = append(out, workCandidate{item: it, settling: settling})
		}
	}
	return out
}

// commandItem projects one live execution.
func commandItem(conv, epoch, key string, j native.JobInfo, saveErrors map[string]string) viewmodel.WorkItem {
	state := jobWorkState(j)
	it := viewmodel.WorkItem{
		TaskKey:        key,
		DisplayID:      j.ID,
		ConversationID: conv,
		RuntimeEpoch:   epoch,
		SourceID:       j.ExecutionID,
		Kind:           workKindCommand,
		Label:          cleanWorkLabel(j.Command),
		Owner:          cleanWorkOwner(j.Origin.Actor),
		Origin: viewmodel.WorkOrigin{
			TurnID:     j.Origin.TurnID,
			StepID:     j.Origin.StepID,
			ToolCallID: j.Origin.ToolCallID,
		},
		State:          state,
		Outcome:        string(j.Outcome),
		ExitCode:       cloneInt(j.ExitCode),
		Attachment:     string(j.Attachment),
		StartedAt:      j.StartedAt,
		EndedAt:        j.EndedAt,
		ResultRevision: j.ResultRevision,
		Activity:       cleanWorkActivity(lastLine(j.DisplayTail)),
		OutputTail:     cleanWorkTail(j.OutputTail),
		ProgressConfig: j.ProgressConfig,
		Progress:       j.Progress,
	}
	// An attached foreground command belongs to the tool call waiting for it:
	// it is inspectable history but not a stop target until it detaches.
	it.Capabilities = viewmodel.WorkCapabilities{
		Inspect: true,
		Stop:    !workItemTerminal(state) && j.Attachment != native.AttachmentForeground && j.ExecutionID != "",
	}
	if msg := saveErrors[settlementKeyOf(j)]; msg != "" {
		// A result whose first durable save is still in flight is not
		// acknowledgeable: acknowledging it would claim a durability the next
		// resume cannot restore.
		it.Attention = cleanWorkWarning(msg)
	} else if viewmodel.WorkNeedsAttention(it) {
		// A terminal command that needs attention is the one result a caller
		// collapses to compact attention by acknowledging; a running command is
		// never acknowledgeable.
		it.Capabilities.Acknowledge = true
	}
	return it
}

// commandExitItem projects one durable command exit. A record with no execution
// identity is legacy history: it uses the "legacy" epoch and its order-based
// identity as source ID, and it never receives stop capability. A record with an
// identity but no source epoch predates the epoch's recording and takes the same
// stable epoch — never the live manager epoch, which changes every process.
func commandExitItem(conv string, e session.JobExit) viewmodel.WorkItem {
	epoch := e.SourceEpoch
	sourceID := e.ExecutionID
	if sourceID == "" {
		epoch = legacyWorkEpoch
		sourceID = e.Identity()
	}
	if epoch == "" {
		epoch = legacyWorkEpoch
	}
	key := viewmodel.WorkKey(conv, epoch, workKindCommand, sourceID)
	state := jobExitWorkState(e)
	it := viewmodel.WorkItem{
		TaskKey:        key,
		DisplayID:      e.ID,
		ConversationID: conv,
		RuntimeEpoch:   epoch,
		SourceID:       sourceID,
		Kind:           workKindCommand,
		Label:          cleanWorkLabel(e.Command),
		Owner:          cleanWorkOwner(e.Actor),
		Origin: viewmodel.WorkOrigin{
			TurnID:     e.TurnID,
			StepID:     e.StepID,
			ToolCallID: e.ToolCallID,
		},
		State:          state,
		Outcome:        e.Outcome,
		ExitCode:       cloneInt(e.Exit),
		Attachment:     e.Attachment,
		StartedAt:      e.StartedAt,
		EndedAt:        e.EndedAt,
		ResultRevision: e.ResultRevision,
		OutputTail:     cleanWorkTail(e.Output),
		ProgressConfig: e.ProgressConfig,
		Progress:       e.Progress,
	}
	if it.StartedAt.IsZero() {
		it.StartedAt = e.At
	}
	if it.EndedAt.IsZero() {
		it.EndedAt = e.At
	}
	it.Capabilities = viewmodel.WorkCapabilities{Inspect: true}
	if viewmodel.WorkNeedsAttention(it) {
		// A terminal command exit that needs attention is acknowledged by its
		// exact result revision; the durable exit carries no live save error.
		it.Capabilities.Acknowledge = true
	}
	return it
}

// subagentCandidates projects the subagent registry. Each view carries its
// saved work key; the child's activity facts were read by the snapshot under
// the child's own locks, outside every parent and coordinator lock.
func (wc *workCoordinator) subagentCandidates(conv string, subs session.SubagentSnapshot) []workCandidate {
	var out []workCandidate
	for _, v := range subs.Views {
		if v.WorkKey == "" {
			continue
		}
		epoch := v.RuntimeEpoch
		if epoch == "" {
			epoch = subs.Epoch
		}
		sourceID := strconv.FormatInt(v.ID, 10)
		state := subagentWorkState(v)
		it := viewmodel.WorkItem{
			TaskKey:        v.WorkKey,
			DisplayID:      sourceID,
			ConversationID: conv,
			RuntimeEpoch:   epoch,
			SourceID:       sourceID,
			Kind:           workKindAgent,
			Label:          cleanWorkLabel(v.Label),
			Owner:          "subagent",
			Origin: viewmodel.WorkOrigin{
				TurnID:     v.Origin.TurnID,
				StepID:     v.Origin.StepID,
				ToolCallID: v.Origin.ToolCallID,
			},
			State:          state,
			Outcome:        v.Outcome,
			StartedAt:      v.StartedAt,
			EndedAt:        v.EndedAt,
			ResultRevision: v.ResultRevision,
			Activity:       cleanWorkActivity(v.CurrentTool),
			Reference: viewmodel.WorkReference{
				MessageID:      v.Report.MessageID,
				NodeKey:        v.Report.NodeKey,
				ChildSessionID: v.Report.ChildSessionID,
			},
		}
		it.Capabilities = viewmodel.WorkCapabilities{
			Inspect:     true,
			Stop:        v.Status == session.SubagentRunning,
			Acknowledge: workItemTerminal(state),
		}
		if msg := subs.SaveErrors[sourceID]; msg != "" {
			it.Attention = cleanWorkWarning(msg)
			it.Capabilities.Acknowledge = false
		}
		out = append(out, workCandidate{item: it, settling: containsString(subs.Settling, sourceID)})
	}
	return out
}

// watchCandidates projects the watch manager's active registrations and its
// retained terminal facts, keyed by registration ID and epoch.
func watchCandidates(conv string, snap watch.Snapshot) []workCandidate {
	var out []workCandidate
	seen := make(map[string]bool)
	add := func(info watch.Info, active bool) {
		epoch := info.Epoch
		if epoch == "" {
			epoch = snap.Epoch
		}
		key := viewmodel.WorkKey(conv, epoch, workKindWatch, info.ID)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, workCandidate{
			item:     watchItem(conv, epoch, key, info, active, snap.SaveErrors),
			settling: containsString(snap.Settling, info.ID),
		})
	}
	for _, info := range snap.Active {
		add(info, true)
	}
	for _, info := range snap.Terminal {
		add(info, false)
	}
	return out
}

// watchItem projects one watch registration.
func watchItem(conv, epoch, key string, info watch.Info, active bool, saveErrors map[string]string) viewmodel.WorkItem {
	state, outcome, warning := watchWorkState(info, active)
	it := viewmodel.WorkItem{
		TaskKey:        key,
		DisplayID:      info.Name,
		ConversationID: conv,
		RuntimeEpoch:   epoch,
		SourceID:       info.ID,
		Kind:           workKindWatch,
		Label:          cleanWorkLabel(info.Name),
		Owner:          cleanWorkOwner(info.Owner),
		State:          state,
		Outcome:        outcome,
		StartedAt:      info.CreatedAt,
		EndedAt:        info.EndedAt,
		ResultRevision: info.ResultRevision,
		Activity:       cleanWorkActivity(lastLine(info.LastSample)),
		Warning:        warning,
	}
	it.Capabilities = viewmodel.WorkCapabilities{
		Inspect:     true,
		Stop:        active && info.Active,
		Acknowledge: !active && workItemTerminal(state),
	}
	if msg := saveErrors[info.ID]; msg != "" {
		it.Attention = cleanWorkWarning(msg)
		it.Capabilities.Acknowledge = false
	}
	return it
}

// watchWorkResult converts a retained terminal watch fact into the durable
// work-index record the session's work view stores. Its key is derived from the
// owning conversation, the manager epoch and the registration ID, so a
// re-registered numeric ID under a new epoch never collides with an old one. A
// reload or a replacement therefore leaves the old result as history rather
// than repointing it at the new source.
func watchWorkResult(conv string, info watch.Info) db.WorkResultRecord {
	state, outcome, warning := watchWorkState(info, false)
	return db.WorkResultRecord{
		TaskKey:        viewmodel.WorkKey(conv, info.Epoch, workKindWatch, info.ID),
		SourceID:       info.ID,
		RuntimeEpoch:   info.Epoch,
		Kind:           workKindWatch,
		Label:          cleanWorkLabel(info.Name),
		Owner:          cleanWorkOwner(info.Owner),
		State:          state,
		Outcome:        outcome,
		StartedAt:      info.CreatedAt,
		EndedAt:        info.EndedAt,
		ResultRevision: info.ResultRevision,
		Detail:         warning,
	}
}

// resultCandidates adds durable work-index results whose live source is gone,
// so history, receipts and paging survive a swept registration.
func resultCandidates(conv string, workView db.WorkViewRecord, existing []workCandidate) []workCandidate {
	var out []workCandidate
	have := make(map[string]bool, len(existing))
	for _, c := range existing {
		have[c.item.TaskKey] = true
	}
	add := func(r db.WorkResultRecord) {
		if r.TaskKey == "" || have[r.TaskKey] {
			return
		}
		have[r.TaskKey] = true
		out = append(out, workCandidate{item: resultItem(conv, r)})
	}
	for _, r := range workView.AgentResults {
		add(r)
	}
	for _, r := range workView.WatchResults {
		add(r)
	}
	return out
}

// resultItem projects one durable work-index result.
func resultItem(conv string, r db.WorkResultRecord) viewmodel.WorkItem {
	state := normalizeStoredState(r.State)
	it := viewmodel.WorkItem{
		TaskKey:        r.TaskKey,
		DisplayID:      r.SourceID,
		ConversationID: conv,
		RuntimeEpoch:   r.RuntimeEpoch,
		SourceID:       r.SourceID,
		Kind:           r.Kind,
		Label:          cleanWorkLabel(r.Label),
		Owner:          cleanWorkOwner(r.Owner),
		Origin: viewmodel.WorkOrigin{
			TurnID:     r.TurnID,
			StepID:     r.StepID,
			ToolCallID: r.ToolCallID,
		},
		State:          state,
		Outcome:        r.Outcome,
		StartedAt:      r.StartedAt,
		EndedAt:        r.EndedAt,
		ResultRevision: r.ResultRevision,
		Reference: viewmodel.WorkReference{
			MessageID:      r.Reference.MessageID,
			NodeKey:        r.Reference.NodeKey,
			ChildSessionID: r.Reference.ChildSessionID,
		},
	}
	it.Capabilities = viewmodel.WorkCapabilities{Inspect: true, Acknowledge: workItemTerminal(state)}
	return it
}

// publishWork publishes the current projection. Lifecycle, membership and
// blocked-prompt changes publish immediately; a purely cosmetic change
// publishes at most 5 Hz. An idle, identical poll advances no revision.
func (wc *workCoordinator) publishWork(urgent bool) {
	now := wc.now()
	wc.mu.Lock()

	history := make([]viewmodel.WorkItem, len(wc.history))
	copy(history, wc.history)

	if fp := historyFingerprint(history); fp != wc.historyFingerprint {
		wc.historyRevision++
		wc.historyFingerprint = fp
	}

	items := selectPublished(history)
	fp := fullFingerprint(items)
	if fp == wc.publishedFingerprint && wc.revision > 0 {
		wc.mu.Unlock()
		return
	}
	if !urgent && wc.revision > 0 && !wc.lastPublish.IsZero() && now.Sub(wc.lastPublish) < workPublishInterval {
		// Cosmetic but too soon: leave the current snapshot in place and let the
		// next tick or wake publish it. The fingerprint is unchanged, so a later
		// identical poll still detects the pending cosmetic change.
		wc.mu.Unlock()
		return
	}
	wc.revision++
	snap := wc.buildSnapshot(history, items, now)
	wc.snapshot = snap
	wc.publishedFingerprint = fp
	wc.lastPublish = now
	broker := wc.broker
	wc.mu.Unlock()
	if broker != nil {
		broker.Publish("work", snap)
	}
}

// buildSnapshot selects the published items and independently computes every
// receipt and attention count from the full conversation history. Caller holds
// wc.mu.
func (wc *workCoordinator) buildSnapshot(history, items []viewmodel.WorkItem, now time.Time) viewmodel.WorkSnapshot {
	conv := ""
	if wc.state != nil {
		conv = wc.state.SessionID()
	}
	snap := viewmodel.WorkSnapshot{
		Version:         workSnapshotVersion,
		ConversationID:  conv,
		RuntimeEpoch:    wc.epoch,
		Revision:        wc.revision,
		HistoryRevision: wc.historyRevision,
		ObservedAt:      now,
	}
	snap.Items = make([]viewmodel.WorkItem, 0, len(items))
	for i := range items {
		it := viewmodel.CloneWorkItem(items[i])
		if rev, ok := wc.acks[it.TaskKey]; ok && rev == viewmodel.WorkResultRevision(it) {
			it.Capabilities.Acknowledge = false
		}
		// Strip prompt identity from acknowledged/published terminal items: a
		// blocked prompt is the only prompt the projection carries.
		snap.Items = append(snap.Items, it)
		if viewmodel.WorkActive(it) {
			snap.ActiveCount++
		}
	}
	for _, it := range history {
		if rev, ok := wc.acks[it.TaskKey]; ok && rev == viewmodel.WorkResultRevision(it) {
			continue
		}
		if viewmodel.WorkNeedsAttention(it) {
			snap.AttentionCount++
			continue
		}
		if success, stopped := viewmodel.WorkReceipts(it, now); success {
			snap.SuccessReceiptCount++
		} else if stopped {
			snap.StoppedReceiptCount++
		}
	}
	return snap
}

// selectPublished returns all active items plus the at-most-32 newest terminal
// details. The full history index keeps every omitted result for paging.
func selectPublished(history []viewmodel.WorkItem) []viewmodel.WorkItem {
	var active, terminal []viewmodel.WorkItem
	for _, it := range history {
		switch {
		case viewmodel.WorkActive(it):
			active = append(active, it)
		case workItemTerminal(it.State):
			terminal = append(terminal, it)
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		if !active[i].StartedAt.Equal(active[j].StartedAt) {
			return active[i].StartedAt.Before(active[j].StartedAt)
		}
		return active[i].TaskKey < active[j].TaskKey
	})
	sort.SliceStable(terminal, func(i, j int) bool {
		if !terminal[i].StartedAt.Equal(terminal[j].StartedAt) {
			return terminal[i].StartedAt.After(terminal[j].StartedAt)
		}
		return terminal[i].TaskKey < terminal[j].TaskKey
	})
	if len(terminal) > workTerminalDetails {
		terminal = terminal[:workTerminalDetails]
	}
	out := make([]viewmodel.WorkItem, 0, len(active)+len(terminal))
	out = append(out, active...)
	out = append(out, terminal...)
	return out
}

// snapshotNow returns a deep copy of the newest published snapshot.
func (wc *workCoordinator) snapshotNow() viewmodel.WorkSnapshot {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return viewmodel.CloneWorkSnapshot(wc.snapshot)
}

// subscribe returns a receive channel for published snapshots. A caller binds
// the epoch from a fresh snapshot or list before accepting events.
func (wc *workCoordinator) subscribe(ctx context.Context) <-chan pubsub.Event[viewmodel.WorkSnapshot] {
	if ctx == nil {
		ctx = context.Background()
	}
	return wc.broker.Subscribe(ctx)
}

// conversationID is the captured conversation's identity.
func (wc *workCoordinator) conversationID() string {
	if wc.state == nil {
		return ""
	}
	return wc.state.SessionID()
}

// database is the session's persistence handle, or nil.
func (wc *workCoordinator) database() *db.DB {
	if wc.state == nil {
		return nil
	}
	return wc.state.DB()
}

// page lists one page of captured history.
func (wc *workCoordinator) page(query viewmodel.WorkQuery) (viewmodel.WorkPage, error) {
	conv := wc.conversationID()
	if query.ConversationID != "" && query.ConversationID != conv {
		return viewmodel.WorkPage{}, viewmodel.ErrWorkStaleTarget
	}
	query.ConversationID = conv
	wc.mu.Lock()
	items := make([]viewmodel.WorkItem, len(wc.history))
	copy(items, wc.history)
	epoch := wc.epoch
	revision := wc.historyRevision
	wc.mu.Unlock()
	return viewmodel.PageWork(items, epoch, revision, query)
}

// itemByKey resolves one canonical task key in the captured history.
func (wc *workCoordinator) itemByKey(key string) (viewmodel.WorkItem, bool) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	for i := range wc.history {
		if wc.history[i].TaskKey == key {
			return viewmodel.CloneWorkItem(wc.history[i]), true
		}
	}
	return viewmodel.WorkItem{}, false
}

// inspect returns the read-only detail for one item: bounded, redacted output
// and, for an agent, its exact persisted report.
func (wc *workCoordinator) inspect(ctx context.Context, target viewmodel.WorkTarget, raw bool) (viewmodel.WorkDetail, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return viewmodel.WorkDetail{}, err
		}
	}
	conv := wc.conversationID()
	if target.ConversationID != "" && target.ConversationID != conv {
		return viewmodel.WorkDetail{}, viewmodel.ErrWorkStaleTarget
	}
	item, ok := wc.itemByKey(target.TaskKey)
	if !ok {
		return viewmodel.WorkDetail{}, viewmodel.ErrWorkStaleTarget
	}
	detail := viewmodel.WorkDetail{Item: item, Raw: raw}
	switch item.Kind {
	case workKindCommand:
		wc.fillCommandOutput(&detail, raw)
	case workKindAgent:
		wc.fillAgentReport(&detail)
	case workKindWatch:
		detail.Output = boundInspect(item.Activity)
		if detail.Output == "" {
			detail.Output = boundInspect(item.Warning)
		}
	default:
		detail.Unavailable = "no retained evidence for this work item"
	}
	return detail, nil
}

// fillCommandOutput resolves a command's retained output. The execution ID is
// validated against the current source before a display ID is used to call
// Output; a reused job-N alone is never trusted.
func (wc *workCoordinator) fillCommandOutput(detail *viewmodel.WorkDetail, raw bool) {
	item := detail.Item
	if wc.jobs != nil && item.SourceID != "" {
		snap := wc.jobs.Snapshot()
		var display string
		for _, j := range snap.Jobs {
			if j.ExecutionID != "" && j.ExecutionID == item.SourceID {
				display = j.ID
				break
			}
		}
		if display != "" && raw {
			if _, out, err := wc.jobs.Output(display, 0); err == nil {
				detail.Output = boundInspect(out)
				return
			}
		}
	}
	if item.OutputTail != "" {
		detail.Output = item.OutputTail
		return
	}
	detail.Unavailable = "command output is no longer retained"
}

// fillAgentReport resolves an agent's exact persisted report by its reference.
func (wc *workCoordinator) fillAgentReport(detail *viewmodel.WorkDetail) {
	item := detail.Item
	detail.Output = boundInspect(item.Activity)
	if item.Reference.MessageID <= 0 {
		detail.Unavailable = "the agent report is not durably available"
		return
	}
	database := wc.database()
	if database == nil {
		detail.Unavailable = "the agent report is unavailable without a database"
		return
	}
	content, err := database.WorkReport(wc.conversationID(), item.Reference.MessageID)
	if err != nil {
		detail.Unavailable = "the agent report is no longer available"
		return
	}
	detail.Report = boundInspect(content)
}

// stop routes one exact stop request to the owner that owns the work, outside
// every coordinator lock. It never falls back to cancelling a parent, and it
// never touches a process handle.
func (wc *workCoordinator) stop(ctx context.Context, target viewmodel.WorkTarget, summary string) (viewmodel.WorkStopResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return viewmodel.WorkStopResult{TaskKey: target.TaskKey, State: workStateUnknown, Summary: err.Error()}, err
	}
	conv := wc.conversationID()
	if target.ConversationID != "" && target.ConversationID != conv {
		return viewmodel.WorkStopResult{TaskKey: target.TaskKey, State: workStateUnknown, Summary: "the work belongs to another conversation"}, viewmodel.ErrWorkStaleTarget
	}
	item, ok := wc.itemByKey(target.TaskKey)
	if !ok {
		return viewmodel.WorkStopResult{TaskKey: target.TaskKey, State: workStateUnknown, Summary: "work target is unavailable"}, viewmodel.ErrWorkStaleTarget
	}
	if !item.Capabilities.Stop {
		return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: item.State, Summary: "work is no longer running", Accepted: false}, nil
	}
	switch item.Kind {
	case workKindCommand:
		return wc.stopCommand(item, summary)
	case workKindAgent:
		return wc.stopAgent(item, summary)
	case workKindWatch:
		return wc.stopWatch(item, summary)
	}
	return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: item.State, Summary: "work cannot be stopped"}, viewmodel.ErrWorkUnsupported
}

func (wc *workCoordinator) stopCommand(item viewmodel.WorkItem, summary string) (viewmodel.WorkStopResult, error) {
	if wc.jobs == nil {
		return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: item.State, Summary: "the command service is unavailable"}, viewmodel.ErrWorkUnsupported
	}
	snap := wc.jobs.Snapshot()
	var live *native.JobInfo
	for _, j := range snap.Jobs {
		if j.ExecutionID != "" && j.ExecutionID == item.SourceID && !j.Status.Terminal() {
			jj := j
			live = &jj
			break
		}
	}
	if live == nil || live.SourceEpoch != item.RuntimeEpoch {
		return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: item.State, Summary: "work is no longer running", Accepted: false}, nil
	}
	cause := summary
	if cause == "" {
		cause = "stopped from the work list"
	}
	if err := wc.jobs.RequestStop(item.SourceID, cause); err != nil {
		return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: item.State, Summary: err.Error()}, err
	}
	return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: workStateStopping, Summary: cause, Accepted: true}, nil
}

func (wc *workCoordinator) stopAgent(item viewmodel.WorkItem, summary string) (viewmodel.WorkStopResult, error) {
	if wc.state == nil {
		return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: item.State, Summary: "no session is available"}, viewmodel.ErrWorkUnsupported
	}
	id, err := strconv.ParseInt(item.SourceID, 10, 64)
	if err != nil {
		return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: item.State, Summary: "the child identity is malformed"}, viewmodel.ErrWorkStaleTarget
	}
	if !wc.state.CancelSubagent(id) {
		return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: item.State, Summary: "work is no longer running", Accepted: false}, nil
	}
	return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: workStateStopping, Summary: stopSummary(summary), Accepted: true}, nil
}

func (wc *workCoordinator) stopWatch(item viewmodel.WorkItem, summary string) (viewmodel.WorkStopResult, error) {
	if wc.watches == nil {
		return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: item.State, Summary: "the watch manager is unavailable"}, viewmodel.ErrWorkUnsupported
	}
	snap := wc.watches.Snapshot()
	active := false
	for _, info := range snap.Active {
		if info.ID == item.SourceID && info.Active && info.Epoch == item.RuntimeEpoch {
			active = true
			break
		}
	}
	if !active {
		return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: item.State, Summary: "work is no longer running", Accepted: false}, nil
	}
	if _, err := wc.watches.Stop(item.SourceID); err != nil {
		return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: item.State, Summary: err.Error()}, err
	}
	return viewmodel.WorkStopResult{TaskKey: item.TaskKey, State: workStateStopped, Summary: stopSummary(summary), Accepted: true}, nil
}

// acknowledge records that a terminal result at its exact revision has been
// seen. It serializes through the session's durable store and publishes only
// after that success. A repeated identical acknowledgment is idempotent.
func (wc *workCoordinator) acknowledge(ctx context.Context, ack viewmodel.WorkAck) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	conv := wc.conversationID()
	if ack.ConversationID != "" && ack.ConversationID != conv {
		return viewmodel.ErrWorkStaleTarget
	}
	if ack.TaskKey == "" || ack.ResultRevision == "" {
		return fmt.Errorf("work acknowledgment requires a task key and a result revision")
	}
	wc.mu.Lock()
	if wc.acks[ack.TaskKey] == ack.ResultRevision {
		wc.mu.Unlock()
		return nil
	}
	wc.mu.Unlock()
	item, ok := wc.itemByKey(ack.TaskKey)
	if !ok {
		return viewmodel.ErrWorkStaleTarget
	}
	if !workItemTerminal(item.State) {
		return viewmodel.ErrWorkUnsupported
	}
	if viewmodel.WorkResultRevision(item) != ack.ResultRevision {
		return fmt.Errorf("work result %q has moved to a newer revision", ack.TaskKey)
	}
	if wc.state == nil {
		return viewmodel.ErrWorkUnsupported
	}
	if err := wc.state.AcknowledgeWork(ack.TaskKey, ack.ResultRevision); err != nil {
		return err
	}
	wc.mu.Lock()
	wc.acks[ack.TaskKey] = ack.ResultRevision
	wc.mu.Unlock()
	// Rebuild the projection so the acknowledgment is reflected in the published
	// snapshot; the durable commit already happened above.
	wc.reconcileWork(wc.now())
	return nil
}

// recordSourceFingerprints records each source's lifecycle fingerprint so an
// epoch or revision move is observable.
func (wc *workCoordinator) recordSourceFingerprints(jobs *native.JobSnapshot, subs session.SubagentSnapshot, watches *watch.Snapshot) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	if jobs != nil {
		wc.sourceEpochs["jobs"] = jobs.Epoch
		wc.sourceRevisions["jobs"] = jobs.Revision
	}
	wc.sourceEpochs["subagents"] = subs.Epoch
	wc.sourceRevisions["subagents"] = subs.Revision
	if watches != nil {
		wc.sourceEpochs["watches"] = watches.Epoch
		wc.sourceRevisions["watches"] = watches.Revision
	}
}

// ── Runtime facade ──────────────────────────────────────────────────────────

// currentWork captures the private coordinator under the pointer mutex. Callers
// hold the returned pointer only for the duration of one facade call and always
// delegate outside Runtime.mu.
func (rt *Runtime) currentWork() *workCoordinator {
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.work
}

// WorkSnapshot returns the newest immutable projection for this conversation.
func (rt *Runtime) WorkSnapshot() viewmodel.WorkSnapshot {
	wc := rt.currentWork()
	if wc == nil {
		return viewmodel.WorkSnapshot{}
	}
	return wc.snapshotNow()
}

// ListWork returns one page of the captured conversation history.
func (rt *Runtime) ListWork(ctx context.Context, query viewmodel.WorkQuery) (viewmodel.WorkPage, error) {
	wc := rt.currentWork()
	if wc == nil {
		return viewmodel.WorkPage{}, viewmodel.ErrWorkUnsupported
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return viewmodel.WorkPage{}, err
		}
	}
	return wc.page(query)
}

// InspectWork returns the bounded, safe detail for one work item.
func (rt *Runtime) InspectWork(ctx context.Context, target viewmodel.WorkTarget, raw bool) (viewmodel.WorkDetail, error) {
	wc := rt.currentWork()
	if wc == nil {
		return viewmodel.WorkDetail{}, viewmodel.ErrWorkUnsupported
	}
	return wc.inspect(ctx, target, raw)
}

// StopWork routes one exact stop request to the owner of the work.
func (rt *Runtime) StopWork(ctx context.Context, target viewmodel.WorkTarget, summary string) (viewmodel.WorkStopResult, error) {
	wc := rt.currentWork()
	if wc == nil {
		return viewmodel.WorkStopResult{}, viewmodel.ErrWorkUnsupported
	}
	return wc.stop(ctx, target, summary)
}

// AcknowledgeWork records that a terminal result at its exact revision has been
// seen.
func (rt *Runtime) AcknowledgeWork(ctx context.Context, ack viewmodel.WorkAck) error {
	wc := rt.currentWork()
	if wc == nil {
		return viewmodel.ErrWorkUnsupported
	}
	return wc.acknowledge(ctx, ack)
}

// SubscribeWork subscribes to published snapshots. A caller binds the epoch from
// a fresh snapshot or list before accepting events.
func (rt *Runtime) SubscribeWork(ctx context.Context) <-chan pubsub.Event[viewmodel.WorkSnapshot] {
	wc := rt.currentWork()
	if wc == nil {
		ch := make(chan pubsub.Event[viewmodel.WorkSnapshot])
		close(ch)
		return ch
	}
	return wc.subscribe(ctx)
}

// ── Projection helpers ──────────────────────────────────────────────────────

// lifecycleFingerprint captures membership, lifecycle, attachment and blocked
// prompt identity: the facts whose change publishes immediately.
func lifecycleFingerprint(items []viewmodel.WorkItem) string {
	var b strings.Builder
	b.WriteString("l1")
	for _, it := range items {
		b.WriteByte(0)
		b.WriteString(it.TaskKey)
		b.WriteByte(1)
		b.WriteString(it.State)
		b.WriteByte(1)
		b.WriteString(it.Attachment)
		b.WriteByte(1)
		b.WriteString(promptID(it.Prompt))
		b.WriteByte(1)
		fmt.Fprintf(&b, "%v", it.Capabilities)
	}
	return b.String()
}

// historyFingerprint captures ordering, membership, the safe display fields a
// page filters and sorts on, and attention: the facts whose change advances
// HistoryRevision and invalidates a reader's cursor. Label and owner enter
// bounded so the hash stays cheap.
func historyFingerprint(items []viewmodel.WorkItem) string {
	var b strings.Builder
	b.WriteString("h1")
	for _, it := range items {
		b.WriteByte(0)
		b.WriteString(it.TaskKey)
		b.WriteByte(1)
		b.WriteString(it.State)
		b.WriteByte(1)
		b.WriteString(strutil.Truncate(it.Label, 64, false))
		b.WriteByte(1)
		b.WriteString(strutil.Truncate(it.Owner, 64, false))
		if viewmodel.WorkNeedsAttention(it) {
			b.WriteString("\x02a")
		}
	}
	return b.String()
}

// fullFingerprint captures every published semantic field, excluding the
// observation time, the publication revision and the derived counts.
func fullFingerprint(items []viewmodel.WorkItem) string {
	var b strings.Builder
	b.WriteString("f1")
	for _, it := range items {
		b.WriteByte(0)
		b.WriteString(it.TaskKey)
		b.WriteByte(1)
		fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%v|%v|%d",
			it.State, it.Outcome, it.Attachment, it.Label, it.Owner, it.Activity,
			it.OutputTail, it.Warning, it.Attention, promptID(it.Prompt),
			it.Capabilities, exitCodeString(it.ExitCode), progressSequence(it.Progress))
	}
	return b.String()
}

func promptID(p *viewmodel.WorkPrompt) string {
	if p == nil {
		return ""
	}
	return p.Kind + ":" + p.RequestID
}

func exitCodeString(code *int) string {
	if code == nil {
		return ""
	}
	return strconv.Itoa(*code)
}

func progressSequence(p *progress.Snapshot) uint64 {
	if p == nil {
		return 0
	}
	return p.Sequence
}

// sourceFingerprintChanged compares the lifecycle fingerprints of two reads of
// each source. It deliberately ignores progress sequence changes.
func sourceFingerprintChanged(jobs, jobs2 *native.JobSnapshot, subs, subs2 session.SubagentSnapshot, watches, watches2 *watch.Snapshot) bool {
	if jobFingerprint(jobs) != jobFingerprint(jobs2) {
		return true
	}
	if subagentFingerprint(subs) != subagentFingerprint(subs2) {
		return true
	}
	if watchFingerprint(watches) != watchFingerprint(watches2) {
		return true
	}
	return false
}

func jobFingerprint(s *native.JobSnapshot) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%d|%v|%s", s.Epoch, s.Revision, s.Settling, sortedSaveErrors(s.SaveErrors))
	for _, j := range s.Jobs {
		b.WriteByte(0)
		fmt.Fprintf(&b, "%s|%s|%s", j.ExecutionID, j.Status, j.Attachment)
	}
	return b.String()
}

func subagentFingerprint(s session.SubagentSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%d|%v|%s", s.Epoch, s.Revision, s.Settling, sortedSaveErrors(s.SaveErrors))
	for _, v := range s.Views {
		b.WriteByte(0)
		fmt.Fprintf(&b, "%d|%d", v.ID, v.Status)
	}
	return b.String()
}

func watchFingerprint(s *watch.Snapshot) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%d|%v|%s", s.Epoch, s.Revision, s.Settling, sortedSaveErrors(s.SaveErrors))
	for _, info := range s.Active {
		b.WriteByte(0)
		fmt.Fprintf(&b, "%s|%s|%v", info.ID, info.State, info.Active)
	}
	for _, info := range s.Terminal {
		b.WriteByte(1)
		fmt.Fprintf(&b, "%s|%s", info.ID, info.TerminalReason)
	}
	return b.String()
}

func sortedSaveErrors(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
		b.WriteByte(';')
	}
	return b.String()
}

// blockedPromptOf reads the canonical pending-request APIs and returns the one
// prompt a live agent is waiting on, if any. It decides no permission and
// resolves nothing: the request ID is only ever carried to a client.
func blockedPromptOf(state *session.State) *viewmodel.WorkPrompt {
	if state == nil {
		return nil
	}
	if req, ok := state.AccessRequest(); ok && req.RequestID != "" {
		return &viewmodel.WorkPrompt{Kind: "access", RequestID: req.RequestID}
	}
	if p := state.PendingApproval(); p != nil && p.Key.ID != "" {
		return &viewmodel.WorkPrompt{Kind: "approval", RequestID: p.Key.ID}
	}
	if p := state.PendingQuestion(); p != nil && p.Key.ID != "" {
		return &viewmodel.WorkPrompt{Kind: "question", RequestID: p.Key.ID}
	}
	if p := state.PendingSkillGate(); p != nil && p.Key.ID != "" {
		return &viewmodel.WorkPrompt{Kind: "skill", RequestID: p.Key.ID}
	}
	if p := state.PendingSudoAuth(); p != nil && p.Key.ID != "" {
		return &viewmodel.WorkPrompt{Kind: "sudo", RequestID: p.Key.ID}
	}
	return nil
}

// blockedPromptLabel names the blocked item's display label. It carries no
// prompt text: only the kind, the request ID and the agent ID are exposed.
func blockedPromptLabel(kind string) string {
	switch kind {
	case "approval":
		return "awaiting tool approval"
	case "question":
		return "awaiting an answer"
	case "skill":
		return "awaiting a skill decision"
	case "sudo":
		return "awaiting privileged authentication"
	case "access":
		return "awaiting an access decision"
	}
	return "awaiting a decision"
}

// jobWorkState maps a native lifecycle status onto the normalized vocabulary.
// A killed execution that was explicitly stopped maps stopped; a failed or
// historical timed-out execution maps failed while its actual outcome and exit
// code are retained separately.
func jobWorkState(j native.JobInfo) string {
	switch j.Status {
	case native.StatusRunning:
		return workStateRunning
	case native.StatusStopping:
		return workStateStopping
	case native.StatusCompleted:
		return workStateCompleted
	case native.StatusKilled:
		return workStateStopped
	case native.StatusFailed, native.StatusTimedOut:
		return workStateFailed
	}
	return workStateUnknown
}

// jobExitWorkState maps a durable command exit onto the normalized vocabulary.
func jobExitWorkState(e session.JobExit) string {
	if e.Lost {
		return workStateLost
	}
	switch e.State {
	case "completed":
		if e.ExitCode == 0 || (e.Exit != nil && *e.Exit == 0) {
			return workStateCompleted
		}
		if e.ExitCode == -1 && e.Exit == nil {
			return workStateUnknown
		}
		return workStateFailed
	case "failed", "timed_out":
		return workStateFailed
	case "killed":
		return workStateStopped
	case "":
		return workStateUnknown
	}
	return workStateUnknown
}

// subagentWorkState maps a subagent view onto the normalized vocabulary. The
// C2 outcome distinguishes an explicit cancellation, which is a stop rather
// than a failure.
func subagentWorkState(v session.SubagentView) string {
	switch v.Status {
	case session.SubagentDone:
		if v.Outcome == "stopped" {
			return workStateStopped
		}
		return workStateCompleted
	case session.SubagentFailed:
		if v.Outcome == "stopped" {
			return workStateStopped
		}
		return workStateFailed
	}
	return workStateRunning
}

// watchWorkState maps a watch registration onto the normalized vocabulary. A
// watch still receiving samples stays watching; a live recoverable error keeps
// it watching with a warning. A terminal registration maps its reason onto a
// distinct state and preserves that reason in the outcome.
func watchWorkState(info watch.Info, active bool) (state, outcome, warning string) {
	if active {
		if info.State == watch.StateError {
			return workStateWatching, "", cleanWorkWarning(info.LastError)
		}
		return workStateWatching, "", ""
	}
	switch info.TerminalReason {
	case "completed":
		return workStateCompleted, "completed", ""
	case "stopped":
		return workStateStopped, "stopped", ""
	case "failed":
		return workStateFailed, "failed", cleanWorkWarning(info.LastError)
	case "shutdown":
		return workStateStopped, "shutdown", ""
	}
	return workStateUnknown, info.TerminalReason, ""
}

// normalizeStoredState maps a persisted state onto the normalized vocabulary.
func normalizeStoredState(s string) string {
	switch s {
	case workStateRunning, workStateStopping, workStateWatching, workStateBlocked,
		workStateCompleted, workStateStopped, workStateFailed, workStateLost, workStateUnknown:
		return s
	}
	return workStateUnknown
}

// workItemTerminal reports whether a normalized state is settled.
func workItemTerminal(state string) bool {
	switch state {
	case workStateCompleted, workStateFailed, workStateStopped, workStateLost, workStateUnknown:
		return true
	}
	return false
}

// stopSummary supplies a truthful default cause for an accepted stop.
func stopSummary(summary string) string {
	if summary == "" {
		return "stopped from the work list"
	}
	return summary
}

// settlementKeyOf mirrors the command service's execution identity for a
// settling snapshot: the execution ID, or the display ID for a legacy record.
func settlementKeyOf(j native.JobInfo) string {
	if j.ExecutionID != "" {
		return j.ExecutionID
	}
	return j.ID
}

func cloneInt(code *int) *int {
	if code == nil {
		return nil
	}
	v := *code
	return &v
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// lastLine returns the final line of s, so a multi-line tail can label one
// activity.
func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// ── Bounded display boundaries ──────────────────────────────────────────────

func cleanWorkLabel(s string) string    { return progress.Clean(s, workLabelRunes, false) }
func cleanWorkOwner(s string) string    { return progress.Clean(s, workOwnerRunes, false) }
func cleanWorkActivity(s string) string { return progress.Clean(s, workActivityRunes, false) }
func cleanWorkWarning(s string) string  { return progress.Clean(s, workWarningRunes, false) }

// cleanWorkTail bounds a retained output tail to workTailBytes at a UTF-8
// boundary, preserving multi-line structure.
func cleanWorkTail(s string) string {
	s = progress.Clean(s, 0, true)
	if len(s) <= workTailBytes {
		return s
	}
	s = s[len(s)-workTailBytes:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// boundInspect bounds an inspected output or report to workInspectBytes at a
// UTF-8 boundary.
func boundInspect(s string) string {
	s = progress.Clean(s, 0, true)
	if len(s) <= workInspectBytes {
		return s
	}
	cut := workInspectBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
