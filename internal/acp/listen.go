package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/app"
	"marshal/internal/app/logging"
	"marshal/internal/trust"
)

// ListenAndServe binds addr and serves the ACP protocol over successive
// connections, all sharing one agent host. A client may disconnect and
// reconnect without losing sessions — this is what makes a containerized
// agent survive a control-plane restart.
//
// network is "unix" or "tcp". For "unix", a stale socket file at addr is
// removed first and the socket is unlinked on return.
func ListenAndServe(ctx context.Context, network, addr string, stderr io.Writer) error {
	log := logging.New(stderr, acpLogLevel(), false)
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("acp: find home directory: %w", err)
	}
	dataDir := filepath.Join(home, ".local", "share", "marshal")
	trustStore := trust.NewStore(dataDir)

	if network == "unix" {
		if err := os.MkdirAll(filepath.Dir(addr), 0o700); err != nil {
			return fmt.Errorf("acp: create socket directory: %w", err)
		}
		// A leftover socket from a crashed generation would make Listen
		// fail with EADDRINUSE even though nothing holds it.
		if err := os.Remove(addr); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("acp: remove stale socket: %w", err)
		}
	}

	ln, err := net.Listen(network, addr)
	if err != nil {
		return fmt.Errorf("acp: listen on %s://%s: %w", network, addr, err)
	}
	if network == "unix" {
		// Only the owner may drive the agent.
		if err := os.Chmod(addr, 0o600); err != nil {
			_ = ln.Close()
			return fmt.Errorf("acp: restrict socket permissions: %w", err)
		}
	}

	return listenAndServeWithConfig(ctx, ln, runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			opts = append(opts, app.WithTrustResolver(trust.NewHeadlessResolver(trustStore, log)))
			return app.StartRuntime(ctx, opts...)
		},
		lister:   newPerCwdLister(),
		shutdown: connectionShutdownTimeout,
		logger:   log,
	})
}

// listenAndServeWithConfig owns the accept loop. Exactly one connection
// is served at a time: a second dialer waits in the listen backlog until
// the first hangs up. Sessions belong to the host, not the connection,
// so a hangup is not a shutdown.
func listenAndServeWithConfig(ctx context.Context, ln net.Listener, cfg runConfig) error {
	// Ensure the listener and any unix socket file are cleaned up on
	// every return path, including unexpected Accept errors.
	defer ln.Close()

	host, err := newAgentHost(cfg)
	if err != nil {
		return err
	}

	// Unblock a pending Accept when the caller cancels.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var serveErr error
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			serveErr = fmt.Errorf("acp: accept: %w", err)
			break
		}
		// A connection error ends that connection, never the host.
		// Use the host's resolved logger (nil-guarded by newAgentHost)
		// rather than the raw cfg.logger, which may be nil.
		if err := host.serveConn(ctx, conn, conn); err != nil && ctx.Err() == nil {
			host.log.Warn("acp connection ended with error", "err", err)
		}
		_ = conn.Close()
		if ctx.Err() != nil {
			break
		}
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), host.shutdown)
	closeErr := host.close(closeCtx)
	cancel()
	if cfg.lister != nil {
		_ = cfg.lister.Close()
	}
	return errors.Join(serveErr, closeErr)
}

// ParseListenAddr splits a "unix:///path" or "tcp://host:port" listen
// specification into its network and address.
func ParseListenAddr(spec string) (network, addr string, err error) {
	switch {
	case strings.HasPrefix(spec, "unix://"):
		return "unix", strings.TrimPrefix(spec, "unix://"), nil
	case strings.HasPrefix(spec, "tcp://"):
		return "tcp", strings.TrimPrefix(spec, "tcp://"), nil
	default:
		return "", "", fmt.Errorf("acp: listen address must start with unix:// or tcp://, got %q", spec)
	}
}
