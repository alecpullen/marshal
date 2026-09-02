package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/logging"
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

// runWithConfig constructs the agentHost, serves a single connection, then
// performs bounded cleanup of every session. The host outlives the
// connection so sessions survive a dropped connection.
func runWithConfig(ctx context.Context, stdin io.Reader, stdout io.Writer, cfg runConfig) error {
	host, err := newAgentHost(cfg)
	if err != nil {
		return err
	}
	serveErr := host.serveConn(ctx, stdin, stdout)
	closeCtx, cancel := context.WithTimeout(context.Background(), host.shutdown)
	closeErr := host.close(closeCtx)
	cancel()
	if cfg.lister != nil {
		_ = cfg.lister.Close()
	}
	return errors.Join(serveErr, closeErr)
}

// Run is the production entrypoint. It constructs the ACP server with
// production dependencies, serves until the connection closes, then
// performs bounded cleanup.
func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	log := logging.New(stderr, acpLogLevel(), false)
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("acp: find home directory: %w", err)
	}
	trustStore := trust.NewStore(config.DataDir(home))
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
