package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"marshal/internal/acp"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/clipboard"
	"marshal/internal/app/config"
	"marshal/internal/app/tui"
	"marshal/internal/daemon"
	"marshal/internal/repo"
	"marshal/internal/trust"
)

const (
	// daemonProbeTimeout bounds the read that decides whether a daemon is
	// there at all: a socket nobody listens on must cost one TUI start, not a
	// wait.
	daemonProbeTimeout = 500 * time.Millisecond
	// daemonHandshakeTimeout bounds one attach's upgrade, initialize and
	// session attach.
	daemonHandshakeTimeout = 30 * time.Second
	// daemonProfileLocal is the profile daemon mode runs on (P4 D1): a local
	// daemon serves host processes from one user's machine.
	daemonProfileLocal = "local"
)

// daemonClient is the part of *daemon.Client the daemon mode uses.
type daemonClient interface {
	Info(ctx context.Context) (daemon.Info, error)
	Attach(ctx context.Context, root, sessionID string) (*daemon.Attachment, error)
}

// WithDaemon makes Run try the local daemon on socket first (P4 D5): when a
// local-profile daemon answers and the project needs no trust question, the
// TUI runs over the relay; otherwise Run runs embedded.
func WithDaemon(socket string) Option {
	return func(o *options) { o.daemonSocket = socket }
}

// withDaemonClient replaces the client built for the socket.
func withDaemonClient(c daemonClient) Option {
	return func(o *options) { o.daemon = c }
}

// withWorkingDir replaces os.Getwd for daemon mode's project lookup.
func withWorkingDir(dir string) Option {
	return func(o *options) { o.workingDir = dir }
}

// withAgentVersion replaces acp.AgentVersion as this build's version.
func withAgentVersion(v string) Option {
	return func(o *options) { o.agentVersion = v }
}

// daemonClientFor is the client daemon mode talks to: the seam's, else one
// built for the socket.
func (o *options) daemonClientFor() daemonClient {
	if o.daemon != nil {
		return o.daemon
	}
	return daemon.NewClient(o.daemonSocket)
}

// version is this build's agent version: the seam's, else the acp package's.
func (o *options) version() string {
	if o.agentVersion != "" {
		return o.agentVersion
	}
	return acp.AgentVersion
}

// daemonTarget resolves the project root daemon mode serves and the home its
// state hangs off: the working directory (the seam's, else the process's), the
// git root that contains it, and the user's home.
func daemonTarget(opts *options) (root, home string, err error) {
	dir := opts.workingDir
	if dir == "" {
		dir, err = os.Getwd()
		if err != nil {
			return "", "", fmt.Errorf("launch: resolve the working directory: %w", err)
		}
	}
	home, err = os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("launch: resolve the home directory: %w", err)
	}
	return repo.Root(dir), home, nil
}

// chooseDaemon reports whether Run should run on the daemon instead of
// embedded, and the root and home it resolved (P4 D5, decision 2): a daemon
// answers on the socket, it is the local profile, and the project's trust
// needs no question. Anything else runs embedded, silently.
func chooseDaemon(ctx context.Context, opts *options) (root, home string, useDaemon bool) {
	root, home, err := daemonTarget(opts)
	if err != nil {
		return "", "", false
	}

	probeCtx, cancel := context.WithTimeout(ctx, daemonProbeTimeout)
	defer cancel()
	info, err := opts.daemonClientFor().Info(probeCtx)
	if err != nil {
		return root, home, false
	}
	if info.Profile != daemonProfileLocal {
		return root, home, false
	}
	if _, needsPrompt, terr := trust.Evaluate(trust.NewStore(config.DataDir(home)), root); terr != nil || needsPrompt {
		return root, home, false
	}
	return root, home, true
}

// Attach runs the TUI on a daemon session (marshal attach): sessionID, or the
// project's most recently active session when it is empty. It fails when no
// daemon answers on socket.
func Attach(ctx context.Context, stdout io.Writer, socket, sessionID string, opts ...Option) error {
	if ctx.Err() != nil {
		return nil
	}

	runOpts := options{programRunner: runProgram}
	for _, opt := range opts {
		opt(&runOpts)
	}
	// The explicit socket wins over one an option carried.
	runOpts.daemonSocket = socket

	root, _, err := daemonTarget(&runOpts)
	if err != nil {
		return err
	}

	probeCtx, cancel := context.WithTimeout(ctx, daemonProbeTimeout)
	info, err := runOpts.daemonClientFor().Info(probeCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("launch: no daemon answers on %s: %v; `marshal daemon install` starts one", socket, err)
	}

	if sessionID == "" {
		sessionID = firstSessionFor(info, root)
		if sessionID == "" {
			return fmt.Errorf("launch: no daemon session for %s; `marshal` starts one", root)
		}
	}
	return runDaemon(ctx, stdout, &runOpts, sessionID)
}

// firstSessionFor is the first session of the child serving root, or "" when
// no child serves it.
func firstSessionFor(info daemon.Info, root string) string {
	for _, child := range info.Children {
		if child.Root != root {
			continue
		}
		if len(child.Sessions) == 0 {
			return ""
		}
		return child.Sessions[0].ID
	}
	return ""
}

// runDaemon runs the TUI over the daemon's relay until the program ends, the
// connection is lost, or the user detaches. sessionID is the session to
// attach, "" for a new one; every resume after that names the chosen session.
func runDaemon(ctx context.Context, stdout io.Writer, opts *options, sessionID string) error {
	root, home, err := daemonTarget(opts)
	if err != nil {
		return err
	}

	logger, closeLog, err := openClientLog(home)
	if err != nil {
		return err
	}
	defer closeLog()

	for {
		conn, err := dialDaemon(ctx, opts, root, sessionID)
		if err != nil {
			return err
		}

		// A daemon from a different build still serves the session, but its
		// agent is not this binary: say so once, so an upgrade that left the
		// daemon running is not mistaken for the new build's behaviour.
		if theirs := conn.host.Agent().Version; theirs != opts.version() {
			note := fmt.Sprintf(
				"This marshal is %s; the daemon's agent is %s. Restart the daemon after upgrading: `marshal daemon stop` then `marshal daemon start`.",
				daemonVersionLabel(opts.version()), daemonVersionLabel(theirs),
			)
			if nerr := conn.sess.AddNote(ctx, acpwire.Note{Text: note, ContentType: acpwire.ContentTypePlain}); nerr != nil {
				logger.Warn("launch: writing the daemon's version note", "error", nerr)
			}
		}

		progCtx, progCancel := context.WithCancel(ctx)
		go func() {
			select {
			case <-conn.lost():
				progCancel()
			case <-progCtx.Done():
			}
		}()

		progResult := opts.programRunner(progCtx, tui.New(conn.host, conn.sess, daemonTUIOptions(ctx, conn, root, home, logger)...), stdout)
		progCancel()

		if cerr := conn.close(); cerr != nil {
			logger.Warn("launch: closing the daemon's connection", "error", cerr)
		}

		if conn.connectionLost() {
			lostErr := conn.err()
			if lostErr == nil {
				lostErr = errors.New("the connection closed")
			}
			return fmt.Errorf("launch: the daemon's connection closed: %w; `marshal attach %s` reattaches", lostErr, conn.sess.ID())
		}

		if progResult.Detached != "" {
			fmt.Fprintf(stdout, "Detached from session %s. `marshal attach %s` reattaches.\n", progResult.Detached, progResult.Detached)
			return nil
		}

		if progResult.ResumeSession != "" && progResult.Err == nil {
			sessionID = progResult.ResumeSession
			continue
		}

		return progResult.Err
	}
}

// daemonTUIOptions is the daemon mode's TUI wiring: detaching leaves the
// session running, the directories are the daemon's project's, and the
// connect panel opens when this machine has no user config yet.
func daemonTUIOptions(ctx context.Context, conn *daemonConn, root, home string, logger *slog.Logger) []tui.Option {
	tuiOpts := []tui.Option{
		tui.WithDetachOnQuit(),
		tui.WithHomeDir(home),
	}

	workDir := conn.sess.Status().WorkingDir
	if workDir == "" {
		workDir = root
	}
	tuiOpts = append(tuiOpts, tui.WithWorkingDir(workDir))

	// F18: eager-seed the @file completion popup with the repo file index,
	// read through the session so the popup and the lazy load share one path.
	if filePaths, ferr := conn.sess.FileIndex(ctx); ferr == nil && len(filePaths) > 0 {
		tuiOpts = append(tuiOpts, tui.WithFileIndex(filePaths))
	}
	if _, serr := os.Stat(config.UserConfigPath(home)); os.IsNotExist(serr) {
		tuiOpts = append(tuiOpts, tui.WithOpenConnectOnStart())
	}
	// Copy prefers a local clipboard helper and falls back to OSC 52 over SSH
	// or when no helper resolves.
	tuiOpts = append(tuiOpts,
		tui.WithClipboard(&clipboard.LocalWriter{}, func() bool { return clipboard.Remote(os.Getenv) }),
		tui.WithContext(ctx),
		tui.WithLogger(logger),
	)
	return tuiOpts
}

// daemonVersionLabel shows an empty version — a build from source — as dev.
func daemonVersionLabel(v string) string {
	if v == "" {
		return "dev"
	}
	return v
}

// openClientLog opens the daemon client's log: the daemon directory's tui.log,
// 0600, appended (decision 9). The caller closes it when the run returns.
func openClientLog(home string) (*slog.Logger, func(), error) {
	dir := config.DaemonDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("launch: create the daemon directory: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "tui.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("launch: open the daemon client log: %w", err)
	}
	return slog.New(slog.NewTextHandler(f, nil)), func() { _ = f.Close() }, nil
}

// daemonConn is one pass's connection to the daemon, and the loss it records,
// as agentConn tracks the pipe's.
type daemonConn struct {
	host *rpc.Host
	sess *rpc.Session

	mu       sync.Mutex
	closed   bool
	lostFlag bool
	lostCh   chan struct{}
	done     chan struct{}
}

// dialDaemon attaches a session on the daemon and dials its relay: the
// attachment's ID names the session the client runs on. A failed dial closes
// the stream.
func dialDaemon(ctx context.Context, opts *options, root, sessionID string) (*daemonConn, error) {
	handshakeCtx, cancel := context.WithTimeout(ctx, daemonHandshakeTimeout)
	defer cancel()

	attachment, err := opts.daemonClientFor().Attach(handshakeCtx, root, sessionID)
	if err != nil {
		return nil, err
	}

	stream := rpc.NewStream(attachment.Conn, attachment.Conn, rpc.MaxFrameBytes)
	// The agent is a different process on the same filesystem, so the client
	// reads the daemon's files directly; the version is not pinned, because a
	// daemon older than this binary still serves the session (the note says
	// so).
	host, err := rpc.Dial(handshakeCtx, stream, rpc.DialOptions{SharedFilesystem: true})
	if err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("launch: dial the daemon's agent: %w", err)
	}

	sess, err := host.Attach(handshakeCtx, attachment.SessionID)
	if err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("launch: attach %q on the daemon: %w", attachment.SessionID, err)
	}
	return newDaemonConn(host, sess), nil
}

// newDaemonConn tracks the connection's end: lost closes when it ends on its
// own, before close claims the shutdown.
func newDaemonConn(host *rpc.Host, sess *rpc.Session) *daemonConn {
	c := &daemonConn{host: host, sess: sess, lostCh: make(chan struct{}), done: make(chan struct{})}
	go func() {
		select {
		case <-host.Done():
			c.mu.Lock()
			claimed := c.closed
			c.mu.Unlock()
			if !claimed {
				c.markLost()
			}
		case <-c.done:
		}
	}()
	return c
}

// lost is closed when the connection ended before close claimed the shutdown.
func (c *daemonConn) lost() <-chan struct{} { return c.lostCh }

// markLost closes lostCh at most once.
func (c *daemonConn) markLost() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lostFlag {
		return
	}
	c.lostFlag = true
	close(c.lostCh)
}

// connectionLost reports whether the connection ended on its own. Read it
// after close: close records a loss that landed while it claimed the shutdown.
func (c *daemonConn) connectionLost() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lostFlag
}

// err is the connection's end error.
func (c *daemonConn) err() error { return c.host.Err() }

// close claims the shutdown and closes the host, recording a loss that landed
// in the window between the caller's last look and the claim.
func (c *daemonConn) close() error {
	c.mu.Lock()
	alreadyClosed := c.closed
	lostBefore := false
	if !alreadyClosed {
		c.closed = true
		select {
		case <-c.host.Done():
			lostBefore = true
		default:
		}
	}
	c.mu.Unlock()
	if alreadyClosed {
		return nil
	}
	if lostBefore {
		c.markLost()
	}
	close(c.done)
	return c.host.Close()
}
