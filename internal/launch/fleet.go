package launch

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"marshal/internal/acpclient"
	"marshal/internal/app/config"
	"marshal/internal/app/tui/fleet"
	"marshal/internal/fleetclient"
)

// withFleetClient replaces the client built for the socket; tests inject it.
func withFleetClient(f acpclient.Fleet) Option {
	return func(o *options) {
		if f == nil {
			return
		}
		o.fleetClient = f
	}
}

// Fleet runs the fleet screen on the local daemon at socket (P5 F4). It fails,
// saying what to do, when no local daemon answers.
func Fleet(ctx context.Context, stdout io.Writer, socket string, opts ...Option) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	runOpts := options{programRunner: runProgram}
	for _, opt := range opts {
		opt(&runOpts)
	}
	// The explicit socket wins over one an option carried.
	runOpts.daemonSocket = socket

	// Probe first: the fleet is the local daemon's, so there is nothing to
	// show — and no point reading config or opening a log — without one.
	probeCtx, cancel := context.WithTimeout(ctx, daemonProbeTimeout)
	info, err := runOpts.daemonClientFor().Info(probeCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("launch: no daemon answers on %s: %v; `marshal daemon install` starts one", socket, err)
	}
	if info.Profile != daemonProfileLocal {
		return fmt.Errorf("launch: the daemon on %s runs the %s profile; the fleet screen needs the local daemon", socket, info.Profile)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("launch: resolve the home directory: %w", err)
	}

	logger, closeLog, err := openClientLog(home)
	if err != nil {
		return err
	}
	defer closeLog()

	// Decision 13: the fleet screen takes the user's [tui] config alone, with
	// no project layer. A user config that cannot be read must not stop the
	// screen from opening, so the defaults stand in.
	cfg := config.Default()
	if loaded, cerr := config.Load(config.LoadOptions{HomeDir: home, SkipProjectConfig: true}); cerr != nil {
		logger.Warn("launch: reading the user config", "error", cerr)
	} else {
		cfg = loaded
	}

	// An injected client is the test's: it owns its lifetime, so it is
	// neither started nor closed here.
	client := runOpts.fleetClient
	if client == nil {
		built := fleetclient.New(fleetclient.Socket(socket))
		built.Start(ctx)
		defer func() { _ = built.Close() }()
		client = built
	}

	result := runOpts.programRunner(ctx, fleet.New(client, fleet.WithTUIConfig(cfg.TUI)), stdout)
	return result.Err
}
