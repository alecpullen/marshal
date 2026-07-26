package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/hooks"
	"marshal/internal/lsp"
	"marshal/internal/plugins"
	"marshal/internal/pubsub"
	"marshal/internal/sdd"
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
	Config         config.Config
	State          *session.State
	Runner         *agent.Runner
	ToolRegistry   *registry.Registry
	SwarmRunner    *swarm.Orchestrator
	SDDRunner      *sdd.ControllerAdapter
	DB             DBCloser
	ProjectID      int64
	SessionID      string
	JobBroker      BrokerCloser
	SteeringBroker BrokerCloser
	EventBroker    BrokerCloser
	MCPManager     MCPCloser
	Snapshot       SnapshotCloser
	Logger         *slog.Logger
	WorkingDir     string
	HomeDir        string
	DataDir        string
	SkillIndex     *skills.Index
	JobManager     *native.JobManager
	DesktopCloser  func()

	// LSPManager is the optional LSP server manager. When non-nil, its
	// worker loop is started in startRuntime (shared by Run and
	// StartRuntime, so both TUI and ACP/headless sessions get it) and its
	// adapters are wired into the index, tools, and diagnostics subsystems.
	LSPManager *lsp.Manager

	// CustomAgentFactory builds a one-shot *agent.Runner for a named custom
	// agent. Used by the TUI's Run-now dispatch. Set by startRuntime.
	CustomAgentFactory func(agentName string) (*agent.Runner, error)
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

	resolver := runOpts.trustResolver
	if resolver == nil {
		resolver = trust.NewTerminalResolver(trust.NewStore(dataDir))
	}
	loader := func(lo config.LoadOptions) (config.Config, error) {
		lo.TrustResolver = resolver
		return runOpts.configLoader(lo)
	}
	var projectTrusted bool
	cfg, err := loader(config.LoadOptions{WorkingDir: workingDir, Trusted: &projectTrusted})
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Join(workingDir, ".marshal"), 0755); err != nil {
		return nil, fmt.Errorf("create .marshal directory: %w", err)
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
	state := session.New(cfg, workingDir, now, session.Persistence{DB: database, SessionID: sessionID, Logger: logger})
	state.SetTrusted(projectTrusted)

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

	if err := plugins.LoadStore(skillIndex, plugins.GlobalStoreDir(homeDir), plugins.GlobalLockPath(homeDir), logger); err != nil {
		logger.Warn("failed to load global plugins", "error", err)
	}
	if projectTrusted {
		if err := plugins.LoadStore(skillIndex, plugins.ProjectStoreDir(workingDir), plugins.ProjectLockPath(workingDir), logger); err != nil {
			logger.Warn("failed to load project plugins", "error", err)
		}
	}

	workCtx, workCancel := context.WithCancel(ctx)
	jobBroker := pubsub.NewBroker[native.JobEvent]()
	steeringBroker := pubsub.NewBroker[session.SteeringEvent]()
	eventBroker := pubsub.NewBroker[session.Event]()
	state.SetSteeringBroker(steeringBroker)
	state.SetEventBroker(eventBroker)

	runner, toolReg, swarmRunner, sddRunner, mcpMgr, snapSvc, jobMgr, desktopCloser, subagentFactory, lspMgr, err := buildAgentRunner(workCtx, cfg, state, database, projectID, skillIndex, dataDir, runOpts.additionalDirs, jobBroker, runOpts.configReloader, homeDir)
	if err == nil && state.Trusted() && len(cfg.Hooks.Entries) > 0 {
		runner.HookRunner = hooks.NewRunnerFromConfig(cfg.Hooks)
	}
	if err != nil {
		state.SetProviderError(err)
	}

	rt := &Runtime{
		Config:             cfg,
		State:              state,
		Runner:             runner,
		ToolRegistry:       toolReg,
		SwarmRunner:        swarmRunner,
		SDDRunner:          sddRunner,
		DB:                 database,
		ProjectID:          projectID,
		SessionID:          sessionID,
		JobBroker:          jobBroker,
		SteeringBroker:     steeringBroker,
		EventBroker:        eventBroker,
		JobManager:         jobMgr,
		DesktopCloser:      desktopCloser,
		CustomAgentFactory: subagentFactory,
		Logger:             logger,
		WorkingDir:         workingDir,
		HomeDir:            homeDir,
		DataDir:            dataDir,
		SkillIndex:         skillIndex,
		LSPManager:         lspMgr,
		ConfigReloader:     runOpts.configReloader,
		additionalDirs:     runOpts.additionalDirs,
		workCtx:            workCtx,
		workCancel:         workCancel,
	}
	// Start the LSP manager's worker loop here so both Run (TUI) and
	// StartRuntime (ACP/headless) get it — constructing lspMgr above only
	// builds the manager; Run() spawns the actual language server
	// subprocess and does the initialize handshake. Without this, every
	// ACP session's hover/references/definition silently return "no lsp"
	// regardless of config, because the manager is never started.
	// workCancel (called from Quiesce) cancels workCtx, which the
	// manager's Run() already handles by shutting servers down cleanly.
	if lspMgr != nil {
		go func() {
			if err := lspMgr.Run(workCtx); err != nil {
				logger.Warn("worker exited", "worker", lspMgr.Name(), "err", err)
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
