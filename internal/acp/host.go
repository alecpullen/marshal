package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/pubsub"
	"marshal/internal/tools/policy"
)

// notifySink forwards JSON-RPC notifications to whichever connection is
// currently attached. When no connection is attached (fn is nil),
// notifications are dropped silently. This lets the agentHost outlive a
// dropped connection: sessions keep running and emitting notifications,
// which are simply discarded until a new connection attaches.
type notifySink struct {
	mu sync.RWMutex
	fn func(method string, params any) error
}

// Set attaches fn as the current notification sink.
func (s *notifySink) Set(fn func(method string, params any) error) {
	s.mu.Lock()
	s.fn = fn
	s.mu.Unlock()
}

// Clear detaches the current notification sink, dropping notifications.
func (s *notifySink) Clear() {
	s.mu.Lock()
	s.fn = nil
	s.mu.Unlock()
}

// Notify forwards a notification to the attached sink, or drops it when
// detached. It matches the NotifyFunc signature.
func (s *notifySink) Notify(method string, params any) error {
	s.mu.RLock()
	fn := s.fn
	s.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(method, params)
}

// agentHost owns the session manager and can survive a dropped
// connection. It binds the manager to a notifySink rather than to a
// specific Server, so a new connection can attach and receive
// notifications while sessions continue running.
//
// The TurnManager is constructed per-connection in registerHandlers
// because its Perms and Questions clients are bound to the connection's
// Server (they send outbound JSON-RPC requests to the connected client).
type agentHost struct {
	manager  *SessionManager
	sink     *notifySink
	log      *slog.Logger
	shutdown time.Duration
}

// newAgentHost constructs the session manager and turn manager in
// dependency order, bound to the host's notifySink rather than to any
// particular connection. Handler registration happens per-connection in
// registerHandlers.
func newAgentHost(cfg runConfig) (*agentHost, error) {
	if cfg.shutdown <= 0 {
		cfg.shutdown = connectionShutdownTimeout
	}
	if cfg.startRuntime == nil {
		return nil, fmt.Errorf("acp: startRuntime is not configured")
	}

	log := cfg.logger
	if log == nil {
		log = slog.Default()
	}

	sink := &notifySink{}

	manager := NewSessionManager(SessionManagerConfig{
		StartRuntime: cfg.startRuntime,
		CloseRuntime: cfg.closeRuntime,
		Lister:       cfg.lister,
		Notify:       sink.Notify,
	}, WithSessionManagerLogger(log))

	return &agentHost{
		manager:  manager,
		sink:     sink,
		log:      log,
		shutdown: cfg.shutdown,
	}, nil
}

// newTurnManagerFor constructs the TurnManager for a connection, capturing
// the session manager in its Lookup closure. notify is the host-level sink
// so notifications survive connection changes. perms and questions are the
// per-connection clients that send outbound JSON-RPC requests to the
// connected client.
func newTurnManagerFor(manager *SessionManager, log *slog.Logger, notify NotifyFunc, perms PermissionClient, questions QuestionClient) *TurnManager {
	return NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			rt, ok := manager.Get(sessionID)
			if !ok || rt == nil {
				return nil, false
			}
			if rt.Runner == nil {
				reason := "agent runner not built"
				if rt.State != nil {
					// Only surface provider-originated notices as the
					// rejection reason; an internal notice (e.g. a
					// command-registration bug) is not a runner problem.
					if n, ok := rt.State.Notice(); ok && n.Category == session.NoticeProvider {
						reason = n.Message
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
			var pipelineFactory func(planPath string, overrides map[routing.AgentRole]string) AgentRunner
			if rt.PipelineFactory != nil {
				pipelineFactory = func(planPath string, overrides map[routing.AgentRole]string) AgentRunner {
					return rt.PipelineFactory(planPath, overrides)
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
		Notify:    notify,
		Perms:     perms,
		Questions: questions,
	})
}

// registerHandlers registers every JSON-RPC handler on srv, constructing
// the per-handler managers in the same dependency order as before. The
// managers are wired to this specific connection's server.
func (h *agentHost) registerHandlers(srv *Server) {
	manager := h.manager
	turns := newTurnManagerFor(manager, h.log, h.sink.Notify,
		&serverPermissionClient{server: srv},
		&serverQuestionClient{server: srv})

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
					// session/new isolation plus session/diff, /merge,
					// /discard and /worktree_prune.
					"worktreeIsolation": map[string]any{},
					// session/commit for non-isolated sessions.
					"exitPath": map[string]any{},
				},
			},
			"agentInfo": map[string]any{
				"name":  "marshal",
				"title": "Marshal",
			},
			"authMethods": []any{},
		}, nil
	})

	srv.Handle("session/new", manager.Create)
	srv.Handle("session/load", manager.Load)
	srv.Handle("session/close", manager.CloseSession)
	srv.Handle("session/list", manager.List)
	srv.Handle("session/resume", manager.Resume)
	srv.Handle("session/delete", manager.Delete)

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

	wtMgr := NewWorktreeManager(WorktreeManagerConfig{
		Lookup: func(sessionID string) (*WorktreeRuntime, bool) {
			rt, ok := manager.Get(sessionID)
			if !ok || rt == nil {
				return nil, false
			}
			return &WorktreeRuntime{State: rt.State, ProjectRoot: rt.State.Workspace().ProjectRoot}, true
		},
		KnownWorktrees: func(projectRoot string) []string {
			var out []string
			for _, rt := range manager.All() {
				if rt == nil || rt.State == nil {
					continue
				}
				ws := rt.State.Workspace()
				if ws.ProjectRoot == projectRoot && ws.ActiveRoot != ws.ProjectRoot {
					out = append(out, ws.ActiveRoot)
				}
			}
			return out
		},
	})
	srv.Handle("session/diff", wtMgr.Diff)
	srv.Handle("session/merge", wtMgr.Merge)
	srv.Handle("session/discard", wtMgr.Discard)
	srv.Handle("session/worktree_prune", wtMgr.Prune)

	exitMgr := NewExitManager(ExitManagerConfig{
		Lookup: func(sessionID string) (*ExitRuntime, bool) {
			rt, ok := manager.Get(sessionID)
			if !ok || rt == nil {
				return nil, false
			}
			return &ExitRuntime{State: rt.State, Dir: rt.State.Workspace().ActiveRoot}, true
		},
	})
	srv.Handle("session/commit", exitMgr.Commit)
	srv.Handle("session/verify", exitMgr.Verify)

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
}

// serveConn serves a single connection. It creates a fresh Server, wires
// the host's managers to it, attaches the sink, and serves until the
// connection closes or ctx is cancelled. On return the sink is detached
// so notifications are dropped until the next connection attaches.
func (h *agentHost) serveConn(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	srv := NewServer(stdin, stdout, WithLogger(h.log))
	h.registerHandlers(srv)
	h.sink.Set(srv.Notify)
	defer h.sink.Clear()
	return srv.Serve(ctx)
}

// close tears down every session with a bounded timeout.
func (h *agentHost) close(ctx context.Context) error {
	return h.manager.CloseAll(ctx)
}
