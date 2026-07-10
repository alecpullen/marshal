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

	return srv.Serve(ctx)
}
