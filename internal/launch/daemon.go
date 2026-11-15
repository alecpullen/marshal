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
	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/clipboard"
	"marshal/internal/app/config"
	"marshal/internal/app/tui"
	"marshal/internal/app/tui/fleet"
	"marshal/internal/daemon"
	"marshal/internal/fleetclient"
	"marshal/internal/repo"
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
	AttachAgent(ctx context.Context, agentID string) (*daemon.Attachment, error)
}

// daemonStep is what the daemon loop runs next.
type daemonStep struct {
	fleet     bool
	selected  string // the fleet's selection to restore
	notice    string // an Events line for the fleet program
	agentID   string
	agentName string
	root      string // the project
	sessionID string // a daemon session; "" opens a new one on root
}

// label names the step's session in a message: the agent's name, else the
// session's ID, else the new session the daemon would have started.
func (s daemonStep) label() string {
	switch {
	case s.agentName != "":
		return s.agentName
	case s.sessionID != "":
		return s.sessionID
	}
	return "a new session"
}

// selection is the fleet row this step's session is: the agent's ID, else the
// session's.
func (s daemonStep) selection(sessionID string) string {
	if s.agentID != "" {
		return s.agentID
	}
	return sessionID
}

// WithDaemon makes Run try the local daemon on socket first (P4 D5): when a
// local-profile daemon answers, the TUI runs over the relay; otherwise Run
// runs embedded.
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
// answers on the socket and it is the local profile. Anything else runs
// embedded, silently.
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
	return root, home, true
}

// Attach runs the TUI on a daemon session (marshal attach): sessionID, or the
// project's most recently active session when it is empty. It fails when no
// daemon answers on socket.
func Attach(ctx context.Context, stdout io.Writer, socket, sessionID string, opts ...Option) error {
	if err := ctx.Err(); err != nil {
		return err
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
	return runDaemon(ctx, stdout, &runOpts, daemonStep{root: root, sessionID: sessionID})
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

// runDaemon runs the daemon's TUI from the first step until the loop ends: the
// fleet screen and a session, either of which can lead to the other (P5 F5).
func runDaemon(ctx context.Context, stdout io.Writer, opts *options, first daemonStep) error {
	root, home, err := daemonTarget(opts)
	if err != nil {
		return err
	}
	// A step that does not say which project it is for is this one's.
	if first.root == "" {
		first.root = root
	}

	logger, closeLog, err := openClientLog(home)
	if err != nil {
		return err
	}
	defer closeLog()

	loop := &daemonLoop{
		opts:        opts,
		stdout:      stdout,
		home:        home,
		logger:      logger,
		fleetClient: opts.fleetClient,
	}
	return loop.run(ctx, first)
}

// daemonLoop is one run of the daemon's TUI and the state that outlives its
// steps: the fleet client it builds and the user config it reads.
type daemonLoop struct {
	opts   *options
	stdout io.Writer
	home   string
	logger *slog.Logger

	// fleetClient is the loop's fleet client, built on the first fleet step
	// and kept for the rest of the loop (decision 7). built is the one the
	// loop made, which it alone closes; an injected client is the test's.
	fleetClient acpclient.Fleet
	built       *fleetclient.Client
	// fleetRan reports whether the fleet program has run in this loop.
	fleetRan bool

	cfg       config.Config
	cfgLoaded bool
}

// run runs the steps until one ends the loop, and returns why it did.
func (l *daemonLoop) run(ctx context.Context, first daemonStep) error {
	defer func() {
		if l.built != nil {
			_ = l.built.Close()
		}
	}()

	step := &first
	for step != nil {
		var err error
		if step.fleet {
			step, err = l.fleetStep(ctx, *step)
		} else {
			step, err = l.sessionStep(ctx, *step)
		}
		if step == nil {
			return err
		}
	}
	return nil
}

// fleetStep runs the fleet screen and reports the session it opened. The zero
// target ends the loop with the program's error.
func (l *daemonLoop) fleetStep(ctx context.Context, step daemonStep) (*daemonStep, error) {
	opts := []fleet.Option{
		fleet.WithTUIConfig(l.tuiConfig()),
		fleet.WithSelected(step.selected),
	}
	if step.notice != "" {
		opts = append(opts, fleet.WithNotice(step.notice))
	}

	result := l.opts.programRunner(ctx, fleet.New(l.fleetClientFor(ctx), opts...), l.stdout)
	l.fleetRan = true

	switch target := result.Open; {
	case target == fleet.Target{}:
		return nil, result.Err
	case target.AgentID != "":
		return &daemonStep{agentID: target.AgentID, agentName: target.AgentName, root: target.Root}, nil
	case target.Root != "" && target.SessionID != "":
		return &daemonStep{root: target.Root, sessionID: target.SessionID}, nil
	default:
		// A target that names a root but no session would dial Attach with an
		// empty id, which opens a new session: a create the fleet never asked
		// for. Report it on the Events ring instead.
		return &daemonStep{fleet: true, notice: "could not open a session: the fleet named none"}, nil
	}
}

// sessionStep dials the step's session, runs the TUI on it, and reports what
// comes next.
func (l *daemonLoop) sessionStep(ctx context.Context, step daemonStep) (*daemonStep, error) {
	conn, err := dialDaemon(ctx, l.opts, step)
	if err != nil {
		// Once the fleet has run, a session it could not open is a line on
		// its Events ring rather than the end of the loop. The notice carries
		// the plain failure: the "marshal attach / --embedded" way out that
		// daemonServingError adds is wrong here, where a fleet is already up.
		if l.fleetRan {
			return &daemonStep{fleet: true, notice: fmt.Sprintf("could not open %s: %v", step.label(), err)}, nil
		}
		var refused *daemonDialFailure
		if errors.As(err, &refused) {
			return nil, daemonServingError(l.opts, refused.what, refused.err)
		}
		return nil, err
	}

	// A daemon from a different build still serves the session, but its
	// agent is not this binary: say so once, so an upgrade that left the
	// daemon running is not mistaken for the new build's behaviour.
	if theirs := conn.host.Agent().Version; theirs != l.opts.version() {
		note := fmt.Sprintf(
			"This marshal is %s; the daemon's agent is %s. Restart the daemon after upgrading: `marshal daemon stop` then `marshal daemon start`.",
			daemonVersionLabel(l.opts.version()), daemonVersionLabel(theirs),
		)
		if nerr := conn.sess.AddNote(ctx, acpwire.Note{Text: note, ContentType: acpwire.ContentTypePlain}); nerr != nil {
			l.logger.Warn("launch: writing the daemon's version note", "error", nerr)
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

	tuiOpts := daemonTUIOptions(ctx, conn, step.root, l.home, l.logger)
	if step.agentID != "" {
		tuiOpts = append(tuiOpts, tui.WithAgentSession(step.agentID, step.agentName))
	}
	progResult := l.opts.programRunner(progCtx, tui.New(conn.host, conn.sess, tuiOpts...), l.stdout)
	progCancel()

	if cerr := conn.close(); cerr != nil {
		l.logger.Warn("launch: closing the daemon's connection", "error", cerr)
	}

	// The session the TUI ran on, read after the close as the loss below is.
	sessionID := conn.sess.ID()

	if conn.connectionLost() {
		lostErr := conn.err()
		if lostErr == nil {
			lostErr = errors.New("the connection closed")
		}
		lost := fmt.Errorf("launch: the daemon's connection closed: %w; `marshal attach %s` reattaches", lostErr, sessionID)
		if l.fleetRan {
			return &daemonStep{fleet: true, notice: fmt.Sprintf("lost the connection to %s: %v", step.label(), lost)}, nil
		}
		return nil, lost
	}

	if progResult.Err != nil {
		return nil, progResult.Err
	}

	if progResult.ToFleet {
		return &daemonStep{fleet: true, selected: step.selection(sessionID)}, nil
	}

	if progResult.ResumeSession != "" {
		return &daemonStep{root: step.root, sessionID: progResult.ResumeSession}, nil
	}

	if progResult.Detached != "" {
		if l.fleetRan {
			return &daemonStep{fleet: true, selected: step.selection(progResult.Detached)}, nil
		}
		fmt.Fprintf(l.stdout, "Detached from session %s. `marshal attach %s` reattaches.\n", progResult.Detached, progResult.Detached)
		return nil, nil
	}

	// Any other end goes back to the fleet once it has run, and ends the
	// program as it always did before that.
	if l.fleetRan {
		return &daemonStep{fleet: true, selected: step.selection(sessionID)}, nil
	}
	return nil, nil
}

// fleetClientFor is the loop's fleet client: the injected one as it is, else
// one built for the socket the first time the loop needs it (decision 7).
func (l *daemonLoop) fleetClientFor(ctx context.Context) acpclient.Fleet {
	if l.fleetClient != nil {
		return l.fleetClient
	}
	built := fleetclient.New(fleetclient.Socket(l.opts.daemonSocket))
	built.Start(ctx)
	l.built = built
	l.fleetClient = built
	return built
}

// tuiConfig is the user's [tui] config, read once for the loop: decision 13's
// read, with no project layer, and the defaults standing in when it cannot be
// read, because a config that does not parse must not stop the screen from
// opening.
func (l *daemonLoop) tuiConfig() config.TUIConfig {
	if !l.cfgLoaded {
		l.cfgLoaded = true
		l.cfg = config.Default()
		if loaded, err := config.Load(config.LoadOptions{HomeDir: l.home, SkipProjectConfig: true}); err != nil {
			l.logger.Warn("launch: reading the user config", "error", err)
		} else {
			l.cfg = loaded
		}
	}
	return l.cfg.TUI
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

// dialDaemon attaches the step's session on the daemon and dials its relay: the
// attachment's ID names the session the client runs on. A failed dial closes
// the stream.
func dialDaemon(ctx context.Context, opts *options, step daemonStep) (*daemonConn, error) {
	handshakeCtx, cancel := context.WithTimeout(ctx, daemonHandshakeTimeout)
	defer cancel()

	var (
		attachment *daemon.Attachment
		err        error
	)
	if step.agentID != "" {
		// An agent's session is the daemon's own: it is opened as a viewer of
		// that agent, not as a session of the project.
		attachment, err = opts.daemonClientFor().AttachAgent(handshakeCtx, step.agentID)
	} else {
		attachment, err = opts.daemonClientFor().Attach(handshakeCtx, step.root, step.sessionID)
	}
	if err != nil {
		return nil, &daemonDialFailure{what: "it could not be asked for a session", err: err}
	}

	stream := rpc.NewStream(attachment.Conn, attachment.Conn, rpc.MaxFrameBytes)
	// The agent is a different process on the same filesystem, so the client
	// reads the daemon's files directly; the version is not pinned, because a
	// daemon older than this binary still serves the session (the note says
	// so).
	host, err := rpc.Dial(handshakeCtx, stream, rpc.DialOptions{SharedFilesystem: true})
	if err != nil {
		_ = stream.Close()
		return nil, &daemonDialFailure{what: "its agent could not be dialled", err: err}
	}

	sess, err := host.Attach(handshakeCtx, attachment.SessionID)
	if err != nil {
		_ = stream.Close()
		return nil, &daemonDialFailure{what: fmt.Sprintf("session %q could not be attached", attachment.SessionID), err: err}
	}
	return newDaemonConn(host, sess), nil
}

// daemonDialFailure is what dialDaemon reports when the daemon answered but the
// step's session could not be opened. It keeps the failure in two forms: the
// plain one, which the fleet screen puts on its Events ring, and its parts,
// which daemonServingError adds the way out to for a loop that has not reached
// the fleet yet. That way out is wrong inside a running fleet — there is
// nothing to retry and nothing to run embedded — so the notice never carries it.
type daemonDialFailure struct {
	what string
	err  error
}

func (e *daemonDialFailure) Error() string { return e.what + ": " + e.err.Error() }
func (e *daemonDialFailure) Unwrap() error { return e.err }

// daemonServingError wraps a failure that lands after the daemon answered the
// probe. Because a daemon is demonstrably there, the message names it and says
// how to get a working session either way, as the probe's own error does.
func daemonServingError(opts *options, what string, err error) error {
	where := "the daemon"
	if opts.daemonSocket != "" {
		where = "the daemon at " + opts.daemonSocket
	}
	return fmt.Errorf(
		"launch: %s answered, but %s: %w; `marshal attach` retries once it is serving, `marshal --embedded` runs without it",
		where, what, err,
	)
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
