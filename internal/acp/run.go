package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"time"

	"marshal/internal/app"
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// InitializeParams is the parameter type for the initialize method.
type InitializeParams struct {
	ProtocolVersion int `json:"protocolVersion"`
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
			var run RunnerFunc
			if rt.Runner != nil {
				run = rt.Runner.Run
			}
			evBroker, _ := rt.EventBroker.(*pubsub.Broker[session.Event])
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: rt.BeginWork,
				Run:       run,
				Events:    evBroker,
			}, true
		},
		Notify: srv.Notify,
		Perms:  &serverPermissionClient{server: srv},
	})
	manager.SetTurnCanceller(turns.CancelAndWait)
	srv.Handle("session/prompt", turns.PromptTurn)
	srv.HandleNotification("session/cancel", turns.Cancel)

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
	return runWithConfig(ctx, stdin, stdout, runConfig{
		startRuntime: app.StartRuntime,
		lister:       newPerCwdLister(),
		shutdown:     connectionShutdownTimeout,
		logger:       logging.New(stderr, slog.LevelInfo),
	})
}
