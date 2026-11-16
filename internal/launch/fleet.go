package launch

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"marshal/internal/acpclient"
	"marshal/internal/app/tui/fleet"
	"marshal/internal/daemon"
	"marshal/internal/fleetclient"
	"marshal/internal/fleetprofile"
)

// remoteTarget is what a server's loop dials instead of the daemon: the
// profile's name, its fleet transport, and the client that attaches its
// sessions (decision 8).
type remoteTarget struct {
	name   string
	fleet  fleetclient.Transport
	attach daemonClient
}

// withTokenStore replaces the keychain a server profile's token is read from.
func withTokenStore(open fleetprofile.OpenStore) Option {
	return func(o *options) { o.tokenStore = open }
}

// withTokenRunner replaces the runner token_cmd's script is run by, so no test
// spawns a real /bin/sh.
func withTokenRunner(run fleetprofile.CommandRunner) Option {
	return func(o *options) { o.tokenRunner = run }
}

// withFleetClient replaces the client built for the socket; tests inject it.
func withFleetClient(f acpclient.Fleet) Option {
	return func(o *options) {
		if f == nil {
			return
		}
		o.fleetClient = f
	}
}

// withFleetWiring sees the fleet client and watch each program is given; tests
// read what the loop wired without driving a real program.
func withFleetWiring(fn func(client acpclient.Fleet, watch *fleet.Watch)) Option {
	return func(o *options) { o.onFleetWiring = fn }
}

// withNotifyRun replaces the command runner the desktop notifier uses, so no
// test spawns a real osascript or notify-send.
func withNotifyRun(run func(ctx context.Context, name string, args ...string) error) Option {
	return func(o *options) { o.notifyRun = run }
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

	// The fleet client and the user's [tui] config belong to the loop: its
	// first step builds them, and it closes the client it built when it
	// returns (decision 7).
	return runDaemon(ctx, stdout, &runOpts, daemonStep{fleet: true})
}

// ServerFleet runs the fleet screen on the server profile srv (decision 8).
//
// The token and the TLS configuration are resolved before any program runs: a
// profile whose token or CA file cannot be read fails here, saying which
// server, rather than opening a screen that could never connect. Nothing is
// probed — an unreachable server is the screen's connection state (F3) — so
// every request the loop makes goes out as it needs to.
func ServerFleet(ctx context.Context, stdout io.Writer, srv fleetprofile.Server, opts ...Option) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	runOpts := options{programRunner: runProgram}
	for _, opt := range opts {
		opt(&runOpts)
	}

	tlsConfig, err := fleetprofile.TLSConfig(srv)
	if err != nil {
		return fmt.Errorf("launch: the server profile %s: %w", srv.Name, err)
	}
	// The token is resolved once, here: it is kept for the loop, so neither the
	// fleet screen's stream nor a session's attach runs the token command
	// again.
	token, err := fleetprofile.Token(ctx, srv, runOpts.tokenStore, runOpts.tokenRunner, os.Stderr)
	if err != nil {
		return fmt.Errorf("launch: the server profile %s: %w", srv.Name, err)
	}
	tokenFor := func(context.Context) (string, error) { return token, nil }

	attach, err := daemon.Remote(srv.URL, tlsConfig, tokenFor)
	if err != nil {
		return fmt.Errorf("launch: the server profile %s: %w", srv.Name, err)
	}

	runOpts.remote = &remoteTarget{
		name:   srv.Name,
		fleet:  fleetclient.HTTPS(srv.Name, srv.URL, tlsConfig, tokenFor),
		attach: attach,
	}
	// The loop starts at the fleet, as Fleet's does: the rest of it is the
	// daemon's, over the profile's transports.
	return runDaemon(ctx, stdout, &runOpts, daemonStep{fleet: true})
}
