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
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/hooks"
	"marshal/internal/lsp"
	"marshal/internal/pubsub"
	"marshal/internal/skills"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
	"marshal/internal/trust"
)

// jobShutdownTimeout controls how long Quiesce waits for background jobs to
// drain. Package-level so tests can override it without sleeping five seconds.
var jobShutdownTimeout = 5 * time.Second

// MCPCloser closes an MCP manager during Runtime.Close.
type MCPCloser interface {
	Close() error
}

// BrokerCloser closes a pubsub broker during Runtime.Close.
type BrokerCloser interface {
	Close()
}

// SnapshotCloser prunes old snapshots during Runtime.Close.
type SnapshotCloser interface {
	Prune(ctx context.Context, retentionDays int) error
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
	PipelineFactory func(planPath string) tui.AgentRunner
	DB              DBCloser
	ProjectID       int64
	SessionID       string
	JobBroker       BrokerCloser
	SteeringBroker  BrokerCloser
	EventBroker     BrokerCloser
	WorkspaceBroker BrokerCloser
	MCPManager      MCPCloser
	Snapshot        SnapshotCloser
	Logger          *slog.Logger
	WorkingDir      string
	HomeDir         string
	DataDir         string
	SkillIndex      *skills.Index
	// PluginCommands are slash commands contributed by verified plugins.
	// Run() registers them into the TUI command registry; headless
	// sessions ignore them.
	PluginCommands []commands.Command
	// CommandRegistry holds every built-in command plus PluginCommands,
	// shared by the TUI and any headless transport (ACP). Built once here
	// so there is a single source of truth for command registration.
	CommandRegistry *commands.Registry
	JobManager      *native.JobManager
	DesktopCloser   func()

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

	// CustomAgentFactory builds a one-shot *agent.Runner for a named custom
	// agent. Used by the TUI's Run-now dispatch. Set by startRuntime.
	CustomAgentFactory agent.SubagentRunnerFactory
	// ConfigReloader hot-swaps the agent runtime from a new config. Set by
	// Run() after the TUI is live; nil when the runtime is headless.
	ConfigReloader func(config.Config) error
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
// If the runtime is already quiescing, BeginWork returns
// session.ErrSessionQuiescing and does NOT increment the work counter.
// Nil parent, State, or runtime root context are rejected with a concrete
// error. If runtime cancellation races with registration, the child context
// is cancelled immediately; the caller still owns finish.
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

	if err := rt.State.BeginWork(); err != nil {
		return nil, nil, err
	}

	workCtx, cancel := context.WithCancel(parent)
	stopRuntimeCancel := context.AfterFunc(rt.workCtx, cancel)

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
		if rt.workCancel != nil {
			rt.workCancel()
		}
		rt.State.ResolvePendingForShutdown()
		rt.State.Shutdown()

		// Snapshot JobManager under the pointer mutex so a concurrent
		// reloadAgentRuntime can swap the pointer without a data race.
		rt.mu.Lock()
		jm := rt.JobManager
		rt.mu.Unlock()

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

		workErr := rt.State.WaitForWork(ctx)
		rt.quiesceErr = errors.Join(jobErr, workErr)
	})
	return rt.quiesceErr
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

		// 5b. rollover close — end the live generation with session_end.
		if rt.Runner != nil && rt.Runner.Rollover != nil {
			if err := rt.Runner.Rollover.Close(ctx); err != nil {
				errs = append(errs, fmt.Errorf("rollover close: %w", err))
			}
		}

		// 6. bounded snapshot DB prune and filesystem prune.
		if rt.Snapshot != nil {
			if rt.DB != nil && rt.Logger != nil {
				if perr := rt.DB.PruneSnapshotsOlderThan(rt.Config.Snapshots.RetentionDays); perr != nil {
					rt.Logger.Warn("snapshot DB prune failed", "error", perr)
				}
			}
			if rt.Logger != nil {
				pruneCtx, pruneCancel := context.WithTimeout(ctx, 30*time.Second)
				defer pruneCancel() // defer is scoped to this closeOnce.Do closure, not to Close itself
				if perr := rt.Snapshot.Prune(pruneCtx, rt.Config.Snapshots.RetentionDays); perr != nil {
					rt.Logger.Warn("snapshot prune failed", "error", perr)
				}
			}
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
		now:           time.Now,
		configLoader:  config.Load,
		programRunner: runProgram,
	}
	for _, opt := range opts {
		opt(&runOpts)
	}
	return startRuntime(ctx, runOpts)
}

// startRuntime is the internal implementation shared by Run and StartRuntime.
// It expects a fully-resolved options struct — no option iteration.
func startRuntime(ctx context.Context, runOpts options) (*Runtime, error) {
	if runOpts.layersLoader == nil {
		runOpts.layersLoader = config.LoadLayers
	}
	workingDir, err := resolveWorkingDir(runOpts.workingDir)
	if err != nil {
		return nil, err
	}

	if runOpts.sessionID != "" && runOpts.existingSessionID != "" {
		return nil, fmt.Errorf("app: WithSessionID and WithExistingSession are mutually exclusive")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find home directory: %w", err)
	}
	dataDir := filepath.Join(homeDir, ".local", "share", "marshal")

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
			SkipProjectConfig: needsPrompt,
		}
		cfg, err = runOpts.configLoader(loadOpts)
		if err == nil {
			layers, err = runOpts.layersLoader(loadOpts)
		}
	} else {
		resolver := runOpts.trustResolver
		if resolver == nil {
			resolver = trust.NewTerminalResolver(trust.NewStore(dataDir))
		}
		loader := func(lo config.LoadOptions) (config.Config, error) {
			lo.TrustResolver = resolver
			return runOpts.configLoader(lo)
		}
		loadOpts := config.LoadOptions{WorkingDir: workingDir, Trusted: &projectTrusted}
		cfg, err = loader(loadOpts)
		if err == nil {
			layers, err = runOpts.layersLoader(config.LoadOptions{
				WorkingDir:    workingDir,
				Trusted:       &projectTrusted,
				TrustResolver: resolver,
			})
		}
	}
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Join(workingDir, ".marshal"), 0755); err != nil {
		return nil, fmt.Errorf("create .marshal directory: %w", err)
	}
	// Best-effort: a failure here must not stop startup, but it does mean
	// the user's config could be committed, so it is worth logging.
	if err := config.EnsureMarshalIgnored(workingDir); err != nil {
		slog.Warn("could not update .gitignore", "error", err)
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
	logger := logging.New(logWriter, slog.LevelInfo)

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

	state := session.New(cfg, workingDir, now, session.Persistence{DB: database, SessionID: sessionID, Logger: logger})
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
	steeringBroker := pubsub.NewBroker[session.SteeringEvent]()
	eventBroker := pubsub.NewBroker[session.Event]()
	workspaceBroker := pubsub.NewBroker[session.WorkspaceEvent]()
	state.SetSteeringBroker(steeringBroker)
	state.SetEventBroker(eventBroker)
	state.SetWorkspaceBroker(workspaceBroker)

	runner, toolReg, swarmRunner, mcpMgr, snapSvc, jobMgr, desktopCloser, subagentFactory, lspHandle, pipelineFactory, err := buildAgentRunner(workCtx, cfg, state, database, projectID, skillIndex, dataDir, runOpts.additionalDirs, jobBroker, runOpts.configReloader, homeDir)
	if err == nil && state.Trusted() && len(cfg.Hooks.Entries) > 0 {
		runner.HookRunner = hooks.NewRunnerFromConfig(cfg.Hooks)
	}
	if err != nil {
		state.SetProviderError(err)
	}

	cmdReg := commands.New()
	// Surfaced via state.SetProviderError rather than aborting startRuntime,
	// matching the buildAgentRunner error handling immediately above: a
	// registration failure here is a programming error (duplicate/invalid
	// command), not a reason to fail the whole runtime construction.
	if regErr := commands.RegisterAll(cmdReg, toolReg); regErr != nil {
		state.SetProviderError(regErr)
	}
	for _, cmd := range pluginCommands {
		if regErr := cmdReg.Register(cmd); regErr != nil {
			logger.Warn("skipping plugin command", "command", cmd.Name, "error", regErr)
		}
	}

	rt := &Runtime{
		Config:             cfg,
		Layers:             layers,
		State:              state,
		Runner:             runner,
		ToolRegistry:       toolReg,
		SwarmRunner:        swarmRunner,
		PipelineFactory:    pipelineFactory,
		DB:                 database,
		ProjectID:          projectID,
		SessionID:          sessionID,
		JobBroker:          jobBroker,
		SteeringBroker:     steeringBroker,
		EventBroker:        eventBroker,
		WorkspaceBroker:    workspaceBroker,
		JobManager:         jobMgr,
		DesktopCloser:      desktopCloser,
		CustomAgentFactory: subagentFactory,
		Logger:             logger,
		WorkingDir:         workingDir,
		HomeDir:            homeDir,
		DataDir:            dataDir,
		SkillIndex:         skillIndex,
		PluginCommands:     pluginCommands,
		CommandRegistry:    cmdReg,
		TrustPromptPending: trustPromptPending,
		LSPManager:         lspHandle,
		ConfigReloader:     runOpts.configReloader,
		additionalDirs:     runOpts.additionalDirs,
		workCtx:            workCtx,
		workCancel:         workCancel,
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
		if err := skills.LoadSkillIntoSession(idx, state, name); err != nil {
			logger.Warn("skipping autoload skill", "skill", name, "error", err)
			continue
		}
		logger.Info("autoloaded skill", "skill", name)
	}
}

func resolveWorkingDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("find working directory: %w", err)
	}
	return wd, nil
}
