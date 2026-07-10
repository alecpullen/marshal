package acp

import (
	"context"
	"encoding/json"
	"io"

	"marshal/internal/app"
)

func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	srv := NewServer(stdin, stdout)
	srv.Handle("initialize", func(ctx context.Context, params json.RawMessage) (any, error) {
		return map[string]any{
			"protocolVersion": 1,
			"agent":           map[string]any{"name": "Marshal"},
		}, nil
	})

	// ACP spec v1 (https://agentclientprotocol.com/protocol/v1/session-setup)
	// defines session/new and session/load as the canonical session lifecycle
	// methods. The methods are registered with those exact names; no aliases
	// are needed by the reference client.
	manager := NewSessionManager(SessionManagerConfig{
		StartRuntime: app.StartRuntime,
	})
	srv.Handle("session/new", manager.Create)
	srv.Handle("session/load", manager.Load)

	// F21 v1: turn execution and event streaming. The Lookup adapter
	// converts the *app.Runtime the SessionManager stores into the
	// TurnRuntime the turn manager needs. The agent package exposes
	// Runner.Run(ctx, prompt), so we wrap it with a RunnerFunc rather
	// than depending on *agent.Runner directly in the turn package.
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
			return &TurnRuntime{
				SessionID: sessionID,
				Run:       run,
				Events:    rt.EventBroker,
			}, true
		},
		Notify: srv.Notify,
		Perms:  &serverPermissionClient{server: srv},
	})
	srv.Handle("session/prompt", turns.PromptTurn)
	srv.Handle("session/cancel", turns.Cancel)

	return srv.Serve(ctx)
}
