package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"marshal/internal/app"
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/pubsub"
	"marshal/internal/tools/policy"
	"marshal/internal/trust"
)

// InitializeParams is the parameter type for the initialize method.
type InitializeParams struct {
	ProtocolVersion int `json:"protocolVersion"`
}

// acpLogLevel returns the slog level for the ACP server, controlled by the
// MARSHAL_ACP_LOG_LEVEL environment variable (debug, info, warn, error).
// Defaults to info.
func acpLogLevel() slog.Level {
	lvl := slog.LevelInfo
	switch os.Getenv("MARSHAL_ACP_LOG_LEVEL") {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	return lvl
}

// runConfig contains the dependency injection points for runWithConfig.
type runConfig struct {
	startRuntime RuntimeStarter
	closeRuntime RuntimeCloser
	lister       SessionLister
	shutdown     time.Duration
	logger       *slog.Logger
}

// runWithConfig constructs the ACP server, session manager, and turn manager
// in dependency order, runs the server until ctx is cancelled or stdin closes,
// then performs bounded cleanup of every session.
func runWithConfig(ctx context.Context, stdin io.Reader, stdout io.Writer, cfg runConfig) error {
	if cfg.shutdown <= 0 {
		cfg.shutdown = connectionShutdownTimeout
	}

	log := cfg.logger
	if log == nil {
		log = slog.Default()
	}

	srv := NewServer(stdin, stdout, WithLogger(log))

	srv.Handle("initialize", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p InitializeParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, invalidParamsError("parse initialize params: %v", err)
			}
		}
		if p.ProtocolVersion == 0 {
			return nil, invalidParamsError("protocolVersion is required")
		}

		return map[string]any{
			"protocolVersion": 1,
			"agentCapabilities": map[string]any{
				"loadSession": true,
				"sessionCapabilities": map[string]any{
					"close":                 map[string]any{},
					"list":                  map[string]any{},
					"resume":                map[string]any{},
					"additionalDirectories": map[string]any{},
					"delete":                map[string]any{},
					"commandDispatch":       map[string]any{},
					"swarmDispatch":         map[string]any{},
					"sddDispatch":           map[string]any{},
					"sessionTelemetry":      map[string]any{},
					"memoryAccess":          map[string]any{},
					"agentsRoster":          map[string]any{},
					"skillsAccess":          map[string]any{},
					"pluginsAccess":         map[string]any{},
				},
			},
			"agentInfo": map[string]any{
				"name":  "marshal",
				"title": "Marshal",
			},
			"authMethods": []any{},
		}, nil
	})

	manager := NewSessionManager(SessionManagerConfig{
		StartRuntime: cfg.startRuntime,
		CloseRuntime: cfg.closeRuntime,
		Lister:       cfg.lister,
		Notify:       srv.Notify,
	}, WithSessionManagerLogger(log))
	srv.Handle("session/new", manager.Create)
	srv.Handle("session/load", manager.Load)
	srv.Handle("session/close", manager.CloseSession)
	srv.Handle("session/list", manager.List)
	srv.Handle("session/resume", manager.Resume)
	srv.Handle("session/delete", manager.Delete)

	turns := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			rt, ok := manager.Get(sessionID)
			if !ok || rt == nil {
				return nil, false
			}
			if rt.Runner == nil {
				reason := "agent runner not built"
				if rt.State != nil {
					if perr := rt.State.ProviderError(); perr != nil {
						reason = perr.Error()
					}
				}
				log.Warn("acp: session has no runner; rejecting prompt",
					"session", sessionID, "reason", reason)
				return nil, false
			}
			var run RunnerFunc
			run = rt.Runner.Run
			evBroker, _ := rt.EventBroker.(*pubsub.Broker[session.Event])

			// Box rt.SwarmRunner (a concrete *swarm.Orchestrator) into the
			// AgentRunner interface only when non-nil — assigning a nil
			// concrete pointer directly would produce a non-nil interface
			// wrapping nil, which SwarmStart's `== nil` check would miss.
			var swarmRunner AgentRunner
			if rt.SwarmRunner != nil {
				swarmRunner = rt.SwarmRunner
			}
			var pipelineFactory func(planPath string) AgentRunner
			if rt.PipelineFactory != nil {
				pipelineFactory = func(planPath string) AgentRunner {
					return rt.PipelineFactory(planPath)
				}
			}

			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: rt.BeginWork,
				Run:       run,
				Events:    evBroker,
				SetMode: func(mode string) error {
					m := policy.ParseApprovalMode(mode)
					// Reject unknown modes explicitly instead of silently falling
					// back to the default read-only mode.
					if string(m) != strings.ToLower(mode) {
						return fmt.Errorf("invalid approval mode %q", mode)
					}
					rt.Runner.SetApprovalMode(m)
					return nil
				},
				Steer:           rt.State.PushSteering,
				State:           rt.State,
				SwarmRunner:     swarmRunner,
				PipelineFactory: pipelineFactory,
			}, true
		},
		Notify:    srv.Notify,
		Perms:     &serverPermissionClient{server: srv},
		Questions: &serverQuestionClient{server: srv},
	})
	srv.Handle("session/prompt", turns.PromptTurn)
	srv.Handle("session/set_mode", turns.SetMode)
	srv.Handle("session/steer", turns.Steer)
	srv.HandleNotification("session/cancel", turns.Cancel)

	srv.Handle("session/swarm_start", turns.SwarmStart)
	srv.Handle("session/swarm_status", turns.SwarmStatus)
	srv.Handle("session/sdd_start", turns.SDDStart)
	srv.Handle("session/sdd_answer", turns.SDDAnswer)
	srv.Handle("session/sdd_status", turns.SDDStatus)

	cmds := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			rt, ok := manager.Get(sessionID)
			if !ok || rt == nil {
				return nil, false
			}
			return &CommandRuntime{State: rt.State, Registry: rt.CommandRegistry}, true
		},
		HasActive: turns.HasActiveTurn,
	})
	srv.Handle("session/command", cmds.Command)
	srv.Handle("session/command_list", cmds.CommandList)

	mem := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(sessionID string) (*MemoryRuntime, bool) {
			rt, ok := manager.Get(sessionID)
			if !ok || rt == nil {
				return nil, false
			}
			dbHandle, _ := rt.DB.(*db.DB)
			return &MemoryRuntime{DB: dbHandle, ProjectID: rt.ProjectID, State: rt.State}, true
		},
	})
	srv.Handle("session/memory_list", mem.MemoryList)
	srv.Handle("session/memory_delete", mem.MemoryDelete)
	srv.Handle("session/memory_set_confidence", mem.MemorySetConfidence)
	srv.Handle("session/agents_roster", mem.AgentsRoster)

	skillsMgr := NewSkillsManager(SkillsManagerConfig{
		Lookup: func(sessionID string) (*SkillsRuntime, bool) {
			rt, ok := manager.Get(sessionID)
			if !ok || rt == nil {
				return nil, false
			}
			return &SkillsRuntime{HomeDir: rt.HomeDir, WorkingDir: rt.WorkingDir, State: rt.State}, true
		},
	})
	srv.Handle("session/skills_list", skillsMgr.SkillsList)
	srv.Handle("session/skills_install_preview", skillsMgr.SkillsInstallPreview)
	srv.Handle("session/skills_install_confirm", skillsMgr.SkillsInstallConfirm)
	srv.Handle("session/skills_install_discard", skillsMgr.SkillsInstallDiscard)
	srv.Handle("session/skills_remove", skillsMgr.SkillsRemove)
	srv.Handle("session/skills_load", skillsMgr.SkillsLoad)

	pluginsMgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			rt, ok := manager.Get(sessionID)
			if !ok || rt == nil {
				return nil, false
			}
			trusted := rt.State != nil && rt.State.Trusted()
			return &PluginsRuntime{HomeDir: rt.HomeDir, WorkingDir: rt.WorkingDir, Trusted: trusted}, true
		},
	})
	srv.Handle("session/plugins_list", pluginsMgr.PluginsList)
	srv.Handle("session/plugins_install_scan", pluginsMgr.PluginsInstallScan)
	srv.Handle("session/plugins_install_confirm", pluginsMgr.PluginsInstallConfirm)
	srv.Handle("session/plugins_install_discard", pluginsMgr.PluginsInstallDiscard)
	srv.Handle("session/plugins_remove", pluginsMgr.PluginsRemove)

	manager.SetTurnCanceller(func(ctx context.Context, sessionID string) error {
		err := turns.CancelAndWait(ctx, sessionID)
		skillsMgr.CloseSession(sessionID)
		pluginsMgr.CloseSession(sessionID)
		return err
	})

	serveErr := srv.Serve(ctx)

	closeCtx, cancel := context.WithTimeout(context.Background(), cfg.shutdown)
	closeErr := manager.CloseAll(closeCtx)
	cancel()
	return errors.Join(serveErr, closeErr)
}

// Run is the production entrypoint. It constructs the ACP server with
// production dependencies, serves until the connection closes, then
// performs bounded cleanup.
func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	log := logging.New(stderr, acpLogLevel())
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("acp: find home directory: %w", err)
	}
	dataDir := filepath.Join(home, ".local", "share", "marshal")
	trustStore := trust.NewStore(dataDir)
	return runWithConfig(ctx, stdin, stdout, runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			opts = append(opts, app.WithTrustResolver(trust.NewHeadlessResolver(trustStore, log)))
			return app.StartRuntime(ctx, opts...)
		},
		lister:   newPerCwdLister(),
		shutdown: connectionShutdownTimeout,
		logger:   log,
	})
}
