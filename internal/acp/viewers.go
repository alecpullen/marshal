package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/logging"
	"marshal/internal/trust"
	"marshal/web/bridge/contract"
)

// viewerMaxFrameBytes is the largest frame a viewer connection reads: P3's
// frame cap, rpc.MaxFrameBytes.
const viewerMaxFrameBytes = 64 << 20

// ViewersEnv is the variable the bridge names a container's viewer socket in
// (decision 3).
const ViewersEnv = "MARSHAL_ACP_VIEWERS"

// ProjectChildEnv marks the child the bridge starts for a project, the only
// one that keeps a start record (P6 B3): a fleet agent's own child serves
// viewers too, and keeps none.
const ProjectChildEnv = "MARSHAL_ACP_PROJECT_CHILD"

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

// containerViewerRefusal is decision 2's error for method: a containerized
// agent's viewer refuses every method off its allowlist. It is an uncoded
// *acpwire.Error, so a marshal client shows the sentence itself, as
// settingsFailure's errors are shown.
func containerViewerRefusal(method string) error {
	return &acpwire.Error{Message: method + " is not available for a containerized agent"}
}

// containerViewerAllowed is contract.ContainerViewerMethods as a server looks
// it up (decision 1). The relay keeps its own set of the same list.
var containerViewerAllowed = func() map[string]bool {
	allowed := make(map[string]bool, len(contract.ContainerViewerMethods))
	for _, method := range contract.ContainerViewerMethods {
		allowed[method] = true
	}
	return allowed
}()

// serveViewer serves one viewer connection until it ends or ctx does. It never
// sets the host's sink: every manager it builds notifies through the viewer's
// own server, so a viewer's updates reach that connection only (P4 D2).
func (h *agentHost) serveViewer(ctx context.Context, conn net.Conn) error {
	srv := NewServer(conn, conn, WithLogger(h.log))
	srv.MaxLineBytes = viewerMaxFrameBytes
	srv.MarshalOnly = true
	if h.restrictViewers {
		// A container's viewers get the allowlist (decision 4): only a
		// container runs --listen, so there is no unrestricted pairing.
		srv.Allow = func(method string) bool { return containerViewerAllowed[method] }
	}
	alive := &atomic.Bool{}
	alive.Store(true)
	cleanup := h.registerHandlers(srv, alive, srv.Notify, true)
	defer cleanup()
	defer alive.Store(false)
	return srv.Serve(ctx)
}

// startExecutable is the executable the record keeps: os.Executable's path, or
// "" when it cannot be found, which keeps no executable entry (decision 2).
func startExecutable(log *slog.Logger) string {
	exe, err := os.Executable()
	if err != nil {
		log.Warn("acp: the start record could not locate the executable", "err", err)
		return ""
	}
	return exe
}

// startProjectConfig is the project config the record keeps:
// config.ProjectConfigPath of the child's working directory, which the bridge
// sets to the project's root. An unreadable working directory keeps none.
func startProjectConfig(log *slog.Logger) string {
	wd, err := os.Getwd()
	if err != nil {
		log.Warn("acp: the start record could not read the working directory", "err", err)
		return ""
	}
	return config.ProjectConfigPath(wd)
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
	// What this child started from (decision 1): its executable and the two
	// config files it read. Only a project child keeps one (P6 B3): the
	// bridge marks the children it starts with ProjectChildEnv, so embedded
	// mode, a `--listen` container and a fleet agent's own child keep none.
	// The mark is read and dropped here, before anything can be spawned, so
	// nothing this child starts inherits it.
	projectChild := os.Getenv(ProjectChildEnv) != ""
	os.Unsetenv(ProjectChildEnv)
	var record *startRecord
	if projectChild {
		record = newStartRecord(startExecutable(log), config.UserConfigPath(home), startProjectConfig(log), log)
	}

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
		record:   record,
		// A viewer only reads, so no connection of this child's is bound to a
		// session's turns: each session's wake resumes through its own service
		// unless a marshal viewer is on its screen (decisions 1, 2).
		resumeDetached: true,
	})
	return errors.Join(err, closeHosts(hosts))
}

// acceptViewers serves the connections ln accepts, each on its own goroutine,
// and returns the wait that ends them (decision 5): RunWithViewers and the
// listener's viewer socket share this loop rather than copying it. The
// returned wait closes the socket first, then ends the accept loop, then waits
// for every viewer within the host's shutdown time.
func (h *agentHost) acceptViewers(ctx context.Context, ln net.Listener) (wait func() error) {
	viewersCtx, cancelViewers := context.WithCancel(ctx)

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
					h.log.Warn("acp viewer accept ended", "err", err)
					return fmt.Errorf("acp: accept viewer: %w", err)
				}
				viewers.Add(1)
				go func() {
					defer viewers.Done()
					defer conn.Close()
					// No idle deadline: a viewer is a person's screen, and P3's
					// frame cap bounds what it can send.
					if err := h.serveViewer(viewersCtx, conn); err != nil && viewersCtx.Err() == nil {
						h.log.Warn("acp viewer connection ended with error", "err", err)
					}
				}()
			}
		}()
	}()

	return func() error {
		// The socket goes first, so no new viewer is accepted while the live
		// ones are ending.
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
		case <-time.After(h.shutdown):
			waitErr = errors.New("acp: viewer connections did not end within the shutdown timeout")
		}
		return errors.Join(acceptErr, waitErr)
	}
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
	waitViewers := host.acceptViewers(context.Background(), ln)

	// stdio serves the bridge's own connection until it ends or ctx does.
	serveErr := host.serveConn(ctx, stdin, stdout)

	// The socket goes first, then the viewers are waited for within the host's
	// shutdown time, then the host closes.
	viewerErr := waitViewers()

	closeCtx, cancel := context.WithTimeout(context.Background(), host.shutdown)
	closeErr := host.close(closeCtx)
	cancel()
	if cfg.lister != nil {
		_ = cfg.lister.Close()
	}
	return errors.Join(serveErr, viewerErr, closeErr)
}
