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
	"marshal/internal/pubsub"
	"marshal/internal/skills"
	"marshal/internal/snapshot"
	"marshal/internal/tools/mcp"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
	"marshal/internal/trust"
)

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
	DB             *db.DB
	ProjectID      int64
	SessionID      string
	JobBroker      *pubsub.Broker[native.JobEvent]
	SteeringBroker *pubsub.Broker[session.SteeringEvent]
	EventBroker    *pubsub.Broker[session.Event]
	MCPManager     *mcp.Manager
	Snapshot       *snapshot.Service
	Logger         *slog.Logger
	WorkingDir     string
	HomeDir        string
	DataDir        string
	SkillIndex     *skills.Index
	JobManager     *native.JobManager

	workCtx    context.Context
	workCancel context.CancelFunc
	mu         sync.Mutex
	closeFns   []func()
}

// Close tears down resources owned by the runtime in reverse order. It is
// safe to call exactly once after StartRuntime returns successfully; the
// TUI's Run also calls Close on its way out.
func (rt *Runtime) Close(ctx context.Context) error {
	var closeErr error
	for i := len(rt.closeFns) - 1; i >= 0; i-- {
		rt.closeFns[i]()
	}
	if rt.workCancel != nil {
		rt.workCancel()
	}
	if rt.JobManager != nil {
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := rt.JobManager.Shutdown(sc); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	if rt.JobBroker != nil {
		rt.JobBroker.Close()
	}
	if rt.SteeringBroker != nil {
		rt.SteeringBroker.Close()
	}
	if rt.EventBroker != nil {
		rt.EventBroker.Close()
	}
	if rt.MCPManager != nil {
		_ = rt.MCPManager.Close()
	}
	if rt.Snapshot != nil {
		pruneCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if rt.DB != nil && rt.Logger != nil {
			if perr := rt.DB.PruneSnapshotsOlderThan(rt.Config.Snapshots.RetentionDays); perr != nil {
				rt.Logger.Warn("snapshot DB prune failed", "error", perr)
			}
		}
		if rt.Logger != nil {
			if perr := rt.Snapshot.Prune(pruneCtx, rt.Config.Snapshots.RetentionDays); perr != nil {
				rt.Logger.Warn("snapshot prune failed", "error", perr)
			}
		}
	}
	if rt.DB != nil {
		_ = rt.DB.Close()
	}
	if rt.State != nil {
		rt.State.Shutdown()
	}
	return closeErr
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

	workingDir, err := resolveWorkingDir(runOpts.workingDir)
	if err != nil {
		return nil, err
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

	database, err := db.Open(dbPath(workingDir))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := database.Migrate(); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	projectID, err := database.GetOrCreateProject(workingDir, cfg.Project.Name)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("get or create project: %w", err)
	}

	now := runOpts.now()
	sessionID := runOpts.sessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("sess_%d", now.UnixNano())
	}
	if err := database.CreateSession(sessionID, projectID, "", now); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("create session: %w", err)
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

	globalSkillsDir := filepath.Join(homeDir, ".config", "marshal", "skills")
	projectSkillsDir := filepath.Join(workingDir, ".marshal", "skills")
	skillIndex, err := skills.LoadSkills(globalSkillsDir, projectSkillsDir)
	if err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		_ = database.Close()
		return nil, fmt.Errorf("load skills: %w", err)
	}

	workCtx, workCancel := context.WithCancel(ctx)
	jobBroker := pubsub.NewBroker[native.JobEvent]()
	steeringBroker := pubsub.NewBroker[session.SteeringEvent]()
	eventBroker := pubsub.NewBroker[session.Event]()
	state.SetSteeringBroker(steeringBroker)
	state.SetEventBroker(eventBroker)

	runner, toolReg, swarmRunner, mcpMgr, snapSvc, jobMgr, err := buildAgentRunner(workCtx, cfg, state, database, projectID, skillIndex, dataDir, jobBroker)
	if err == nil && state.Trusted() && len(cfg.Hooks.Entries) > 0 {
		runner.HookRunner = hooks.NewRunnerFromConfig(cfg.Hooks)
	}
	if err != nil {
		state.SetProviderError(err)
	}

	rt := &Runtime{
		Config:         cfg,
		State:          state,
		Runner:         runner,
		ToolRegistry:   toolReg,
		SwarmRunner:    swarmRunner,
		DB:             database,
		ProjectID:      projectID,
		SessionID:      sessionID,
		JobBroker:      jobBroker,
		SteeringBroker: steeringBroker,
		EventBroker:    eventBroker,
		MCPManager:     mcpMgr,
		Snapshot:       snapSvc,
		JobManager:     jobMgr,
		Logger:         logger,
		WorkingDir:     workingDir,
		HomeDir:        homeDir,
		DataDir:        dataDir,
		SkillIndex:     skillIndex,
		workCtx:        workCtx,
		workCancel:     workCancel,
	}
	if logFile != nil {
		rt.closeFns = append(rt.closeFns, func() { _ = logFile.Close() })
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
