package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/logging"
	"marshal/internal/trust"
)

// viewerMaxFrameBytes is the largest frame a viewer connection reads: P3's
// frame cap, rpc.MaxFrameBytes.
const viewerMaxFrameBytes = 64 << 20

// viewerLifecycleRefusal is what a viewer connection answers a call that would
// start or load a runtime (P4 D2): the bridge owns sessions' lifecycles.
func viewerLifecycleRefusal() error {
	return &jsonRPCError{
		Code:    invalidRequest,
		Message: "a viewer connection cannot start or load a session",
	}
}

// viewerCapabilityRefusal is what a viewer connection answers a client that did
// not declare the marshal capability (P4 D2).
func viewerCapabilityRefusal() error {
	return &jsonRPCError{
		Code:    invalidRequest,
		Message: "a viewer connection serves marshal clients only",
	}
}

// serveViewer serves one viewer connection until it ends or ctx does. It never
// sets the host's sink: every manager it builds notifies through the viewer's
// own server, so a viewer's updates reach that connection only (P4 D2).
func (h *agentHost) serveViewer(ctx context.Context, conn net.Conn) error {
	srv := NewServer(conn, conn, WithLogger(h.log))
	srv.MaxLineBytes = viewerMaxFrameBytes
	srv.MarshalOnly = true
	alive := &atomic.Bool{}
	alive.Store(true)
	cleanup := h.registerHandlers(srv, alive, srv.Notify, true)
	defer cleanup()
	defer alive.Store(false)
	return srv.Serve(ctx)
}

// RunWithViewers is Run plus a viewer listener on the unix socket at path
// (P4 D2): stdio serves the bridge's own connection while any number of
// marshal clients attach on the socket.
func RunWithViewers(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, path string) error {
	log := logging.New(stderr, acpLogLevel(), false)
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("acp: find home directory: %w", err)
	}
	trustStore := trust.NewStore(config.DataDir(home))

	// One project-host registry for the process, shared by every connection's
	// sessions and closed after all of them (see Run).
	hosts := app.NewHosts()
	// The socket binds before stdio is served, so a viewer can connect as soon
	// as initialize on stdio is answered.
	ln, err := listenUnix(path)
	if err != nil {
		return errors.Join(err, closeHosts(hosts))
	}
	err = runWithViewersConfig(ctx, stdin, stdout, ln, runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			opts = append(opts, app.WithTrustResolver(trust.NewHeadlessResolver(trustStore, log)), app.WithHosts(hosts))
			return app.StartRuntime(ctx, opts...)
		},
		lister:   newPerCwdLister(),
		shutdown: connectionShutdownTimeout,
		logger:   log,
		hold:     hostsHold(hosts, home, trust.NewHeadlessResolver(trustStore, log)),
	})
	return errors.Join(err, closeHosts(hosts))
}

// runWithViewersConfig is RunWithViewers over a listener the caller bound, for
// tests, as listenAndServeWithConfig is for ListenAndServe. stdio serves the
// caller's own connection; the listener serves viewers concurrently, each on
// its own goroutine.
func runWithViewersConfig(ctx context.Context, stdin io.Reader, stdout io.Writer, ln net.Listener, cfg runConfig) error {
	host, err := newAgentHost(cfg)
	if err != nil {
		_ = ln.Close()
		return err
	}

	// The viewers' context is the host's, not the caller's: the accept loop
	// and its connections end when stdio does, below, not before.
	viewersCtx, cancelViewers := context.WithCancel(context.Background())
	defer cancelViewers()

	var viewers sync.WaitGroup
	acceptDone := make(chan error, 1)
	go func() {
		acceptDone <- func() error {
			for {
				conn, err := ln.Accept()
				if err != nil {
					if viewersCtx.Err() != nil || errors.Is(err, net.ErrClosed) {
						return nil
					}
					// An accept error ends the accept loop, not stdio.
					host.log.Warn("acp viewer accept ended", "err", err)
					return fmt.Errorf("acp: accept viewer: %w", err)
				}
				viewers.Add(1)
				go func() {
					defer viewers.Done()
					defer conn.Close()
					// No idle deadline: a viewer is a person's screen, and P3's
					// frame cap bounds what it can send.
					if err := host.serveViewer(viewersCtx, conn); err != nil && viewersCtx.Err() == nil {
						host.log.Warn("acp viewer connection ended with error", "err", err)
					}
				}()
			}
		}()
	}()

	// stdio serves the bridge's own connection until it ends or ctx does.
	serveErr := host.serveConn(ctx, stdin, stdout)

	// The socket goes first, so no new viewer is accepted while the live ones
	// are ending.
	_ = ln.Close()
	cancelViewers()
	acceptErr := <-acceptDone

	waited := make(chan struct{})
	go func() {
		viewers.Wait()
		close(waited)
	}()
	var waitErr error
	select {
	case <-waited:
	case <-time.After(host.shutdown):
		waitErr = errors.New("acp: viewer connections did not end within the shutdown timeout")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), host.shutdown)
	closeErr := host.close(closeCtx)
	cancel()
	if cfg.lister != nil {
		_ = cfg.lister.Close()
	}
	return errors.Join(serveErr, acceptErr, waitErr, closeErr)
}
