package launch

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"marshal/internal/acpclient"
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

	// The fleet client and the user's [tui] config belong to the loop: its
	// first step builds them, and it closes the client it built when it
	// returns (decision 7).
	return runDaemon(ctx, stdout, &runOpts, daemonStep{fleet: true})
}
