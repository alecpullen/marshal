package acp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"marshal/internal/acpclient"
	"marshal/internal/app"
)

// TestACPClientMethodTableMatchesTheServer keeps acpclient.Methods' Exists
// column honest in both directions: a row that claims the ACP method already
// exists must find a handler registered here, and a row that claims it is new
// must not (T4).
func TestACPClientMethodTableMatchesTheServer(t *testing.T) {
	h, err := newAgentHost(runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return nil, errors.New("no runtime in tests")
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("newAgentHost: %v", err)
	}

	srv := NewServer(strings.NewReader(""), io.Discard)
	var alive atomic.Bool
	alive.Store(true)
	cleanup := h.registerHandlers(srv, &alive)
	defer cleanup()

	for _, row := range acpclient.Methods {
		if row.ACP == "" {
			continue
		}
		_, registered := srv.handlers[row.ACP]
		switch {
		case row.Exists && !registered:
			t.Errorf("acpclient.Methods marks %s.%s's %s as existing, but acp.Server does not register it",
				row.Iface, row.Name, row.ACP)
		case !row.Exists && registered:
			t.Errorf("acp.Server registers %s now: mark %s.%s as existing in acpclient.Methods",
				row.ACP, row.Iface, row.Name)
		}
	}
}
