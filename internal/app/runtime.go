package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/dbmigrate"
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/hooks"
	"marshal/internal/index"
	"marshal/internal/llm/routing"
	"marshal/internal/lsp"
	"marshal/internal/pubsub"
	"marshal/internal/repo"
	"marshal/internal/sddauthor"
	"marshal/internal/skills"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
	"marshal/internal/trust"
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

	// LSPManager is the optional LSP server manager handle. When non-nil,
	// the current manager's worker loop is started in startRuntime (shared
	// by Run and StartRuntime) and the manager is restarted against the
	// active root on every WorkspaceEvent.
	LSPManager *lsp.Handle
	// lspCancel stops the current LSP manager's Run loop. It is managed by
	// startRuntime and reloadAgentRuntime so the manager can be hot-swapped
	// when config reload changes the LSP server set.
	lspCancel context.CancelFunc

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
	// WriteLock is the single swarm.WriteLock shared by the parent runner
	// and every agent.run child. It is created once at startRuntime and
	// reused across config reloads so in-flight background children (which
	// hold the lock from the pre-reload generation) keep serializing with
	// the post-reload parent on the same gate. A reload that minted a fresh
	// lock would let a pre-reload child and the post-reload parent write
	// concurrently.
	WriteLock      *swarm.WriteLock
	additionalDirs []string

	workCtx    context.Context
	workCancel context.CancelFunc
	mu         sync.Mutex
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

// runLSPManager starts m's Run loop under a child of rt.workCtx and returns a
// cancel function that stops it. It mirrors the inline runManager closure in
// startRuntime but can be reused during config reload.
func (rt *Runtime) runLSPManager(m *lsp.Manager) context.CancelFunc {
	mctx, cancel := context.WithCancel(rt.workCtx)
	go func() {
		if err := m.Run(mctx); err != nil {
			rt.Logger.Warn("worker exited", "worker", m.Name(), "err", err)
		}
	}()
	return cancel
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
		rt.quiesceErr = errors.Join(workErr, watchErr, jobErr, waitErr)
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

	if err := dbmigrate.AdoptStrayProjectDB(workingDir, slog.Default()); err != nil {
		slog.Warn("stray .marshal migration", "error", err)
	}

	database, err := db.Open(db.Path(workingDir))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := database.Migrate(); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	// Reconcile any generations left open by a crashed process.
	if _, err := database.ReconcileOpenGenerations(runOpts.now()); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("reconcile open generations: %w", err)
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
	runner, toolReg, swarmRunner, mcpMgr, snapSvc, commandService, watchMgr, desktopCloser, subagentFactory, lspHandle, pipelineFactory, planAuthorFactory, swarmOverrideFactory, err := buildAgentRunnerWithLock(workCtx, cfg, state, database, projectID, skillIndex, dataDir, runOpts.additionalDirs, jobBroker, watchBroker, watchResumeCell, runOpts.configReloader, homeDir, writeLock, nil)
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
		ConfigReloader:       runOpts.configReloader,
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
	// Start the LSP manager's worker loop here so both Run (TUI) and
	// StartRuntime (ACP/headless) get it, and restart the manager against
	// the session's active root on every WorkspaceEvent so hover /
	// references / definition keep working inside a worktree. Each manager
	// generation gets its own child context: a restart cancels the old
	// generation (Run shuts its servers down cleanly) and starts the new
	// one. workCancel (from Quiesce) ends the subscription and the current
	// generation. The closure references rt.LSPManager (not the local
	// lspHandle) so config reload can swap in a new handle and have the
	// next restart use it.
	if rt.LSPManager != nil {
		rt.lspCancel = rt.runLSPManager(rt.LSPManager.Get())
		go func() {
			ch := workspaceBroker.Subscribe(rt.workCtx)
			for {
				ev, ok := <-ch
				if !ok {
					return
				}
				rt.lspCancel()
				newM, _ := rt.LSPManager.Restart(ev.Payload.Workspace.ActiveRoot)
				rt.lspCancel = rt.runLSPManager(newM)
			}
		}()
	}
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
	// Serialize the whole replacement against config reloads. A reload rebuilds
	// the same pointers this function swaps, and both commit launch generations
	// on the service.
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
	// gate across the swap (in-flight children from the old session hold it).
	lock := rt.WriteLock
	if lock == nil {
		lock = &swarm.WriteLock{}
	}
	newRunner, newReg, newSwarmRunner, newMCP, newSnap, newJobMgr, newWatchMgr, newDesktopCloser, newSubagentFactory, newLSPHandle, newPipelineFactory, newPlanAuthorFactory, newSwarmOverrideFactory, err := buildAgentRunnerWithLock(
		rt.workCtx, rt.Config, newState, db, rt.ProjectID, rt.SkillIndex, rt.DataDir, rt.additionalDirs, jb, must[*pubsub.Broker[watch.Event]](rt.WatchBroker), rt.WatchResume, rt.ConfigReloader, rt.HomeDir, lock, nil,
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

	// Success — swap under the runtime lock and clean up old resources outside it.
	rt.mu.Lock()
	oldState := rt.State
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
	oldLSP := rt.LSPManager

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
	if newLSPHandle != nil {
		rt.LSPManager = newLSPHandle
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
	if oldLSP != nil && oldLSP != newLSPHandle {
		// Restart the LSP worker against the new handle, matching the
		// reloadAgentRuntime cleanup pattern.
		rt.lspCancel()
		if newLSPHandle != nil {
			rt.lspCancel = rt.runLSPManager(rt.LSPManager.Get())
		}
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
