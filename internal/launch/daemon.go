package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"marshal/internal/acp"
	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/clipboard"
	"marshal/internal/app/config"
	"marshal/internal/app/notify"
	"marshal/internal/app/tui"
	"marshal/internal/app/tui/fleet"
	"marshal/internal/app/tui/reconnect"
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

	// reconnectWindow is how long a lost connection is reconnected for before
	// the loss is reported (decision 7). A daemon restart takes seconds, and a
	// minute leaves room for a rebuild on either side of it.
	reconnectWindow = 60 * time.Second
	// reconnectFirstWait is the wait before the first retry, and
	// reconnectMaxWait the largest wait between retries (decision 7).
	reconnectFirstWait = time.Second
	reconnectMaxWait   = 5 * time.Second

	// noteOnceWindow is how many of the session's newest messages the
	// note-once check reads (decision 6): a resumed session appends its own
	// startup messages after the reloaded transcript, so the note it is
	// looking for is not the last one.
	noteOnceWindow = 32
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
	root      string      // the project
	sessionID string      // a daemon session; "" opens a new one on root
	dialled   *daemonConn // a connection the reconnect already dialled
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

// withReconnect sets the window and the waits (decision 7).
func withReconnect(window, first, max time.Duration) Option {
	return func(o *options) {
		o.reconnectWindow = window
		o.reconnectFirst = first
		o.reconnectMax = max
	}
}

// withAgentVersion replaces acp.AgentVersion as this build's version.
func withAgentVersion(v string) Option {
	return func(o *options) { o.agentVersion = v }
}

// daemonClientFor is the client daemon mode talks to: the seam's, else the
// remote one a server profile's loop was given, else one built for the socket.
func (o *options) daemonClientFor() daemonClient {
	if o.daemon != nil {
		return o.daemon
	}
	if o.remote != nil {
		return o.remote.attach
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

	// fleetClient is the loop's fleet client, created when the loop starts and
	// kept for its whole life (decision 8): every session screen counts with
	// it, so a plain `marshal` shows the count from its first frame. built is
	// the one the loop made, which it alone closes; an injected client is the
	// test's.
	fleetClient acpclient.Fleet
	built       *fleetclient.Client
	// watch is the loop's notification watch, created when the first fleet
	// program runs and handed to every program after it (decision 8). It is
	// nil until then, so a loop that never opens the fleet never notifies.
	watch *fleet.Watch
	// fleetRan reports whether the fleet program has run in this loop.
	fleetRan bool

	// drafts is what each session screen left in its composer, by session ID,
	// kept for the loop's life (decision 5): a reconnect and a return from the
	// fleet both open the screen with it.
	drafts map[string]tui.Draft

	cfg       config.Config
	cfgLoaded bool
}

// run runs the steps until one ends the loop, and returns why it did.
func (l *daemonLoop) run(ctx context.Context, first daemonStep) error {
	// Decision 8: the fleet client starts with the loop, not with the fleet
	// program, so a plain `marshal`'s session screen has the count from the
	// start. The loop closes the client it made, and only that one.
	l.fleetClientFor(ctx)
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
	watch := l.watchFor()
	opts := []fleet.Option{
		fleet.WithTUIConfig(l.tuiConfig()),
		fleet.WithSelected(step.selected),
		fleet.WithWatch(watch),
	}
	if step.notice != "" {
		opts = append(opts, fleet.WithNotice(step.notice))
	}

	client := l.fleetClientFor(ctx)
	l.sawFleetWiring(client, watch)
	result := l.opts.programRunner(ctx, fleet.New(client, opts...), l.stdout)
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
	// Decision 5: the reconnect's step carries the connection it dialled, so
	// the screen runs on it rather than dialling again.
	conn := step.dialled
	if conn == nil {
		var err error
		conn, err = dialDaemon(ctx, l.opts, step)
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
	}

	// The session the screen runs on: the draft below is keyed by it, and the
	// loss reports it. The ID does not change with the connection.
	sessionID := conn.sess.ID()

	// A daemon from a different build still serves the session, but its
	// agent is not this binary: say so once, so an upgrade that left the
	// daemon running is not mistaken for the new build's behaviour.
	if theirs := conn.host.Agent().Version; theirs != l.opts.version() {
		if nerr := addNoteOnce(ctx, conn.sess, l.opts.versionNote(theirs)); nerr != nil {
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

	tuiOpts := daemonTUIOptions(ctx, conn, step.root, l.home, l.logger, l.opts.remote == nil)
	// Decision 8: every daemon session screen counts with the loop's fleet
	// client, and gets the watch once the fleet has run. The watch is nil
	// before that, so a plain `marshal` counts without notifying.
	tuiOpts = append(tuiOpts, tui.WithFleet(l.fleetClient, l.watch))
	l.sawFleetWiring(l.fleetClient, l.watch)
	if step.agentID != "" {
		tuiOpts = append(tuiOpts, tui.WithAgentSession(step.agentID, step.agentName))
	}
	// Decision 5: the screen opens with the draft its session kept, so a
	// reconnect and a return from the fleet both find it.
	if draft, kept := l.drafts[sessionID]; kept && !draft.Empty() {
		tuiOpts = append(tuiOpts, tui.WithDraft(draft))
	}
	progResult := l.opts.programRunner(progCtx, tui.New(conn.host, conn.sess, tuiOpts...), l.stdout)
	progCancel()

	// Decision 5: the draft is kept for every ending, so the screen after a
	// reconnect or a return from the fleet opens with it. A screen that ends
	// with nothing in the composer clears the entry.
	l.keepDraft(sessionID, progResult.Draft)

	if cerr := conn.close(); cerr != nil {
		l.logger.Warn("launch: closing the daemon's connection", "error", cerr)
	}

	if conn.connectionLost() {
		// Decision 4: the reconnect reuses the session's ID, so a first attach
		// that opened a new session reconnects to the session it opened rather
		// than opening another one.
		resume := step
		resume.sessionID = sessionID
		resume.dialled = nil
		reconnected, rerr := l.reconnect(ctx, resume, conn.restartReason())
		if rerr == nil {
			// Decision 5: the step carries the connection, so the screen that
			// follows runs on it.
			resume.dialled = reconnected
			return &resume, nil
		}
		// The reconnect returns a cause whenever it fails, so the loss names
		// it: there is no path here without one.
		lost := fmt.Errorf("launch: the daemon's connection closed: %w; `marshal attach %s` reattaches", rerr, sessionID)
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

// keepDraft keeps the draft a session screen ended with, and clears the entry
// when it ended with nothing in the composer (decision 5).
func (l *daemonLoop) keepDraft(sessionID string, draft tui.Draft) {
	if draft.Empty() {
		delete(l.drafts, sessionID)
		return
	}
	if l.drafts == nil {
		l.drafts = make(map[string]tui.Draft)
	}
	l.drafts[sessionID] = draft
}

// reconnectOutcome is what the attempts ended with: the connection one of them
// attached, or why none did.
type reconnectOutcome struct {
	conn *daemonConn
	err  error
}

// reconnect dials step again until it attaches, a refusal ends it, the
// window ends or the user gives up (decisions 2, 3 and 7).
func (l *daemonLoop) reconnect(ctx context.Context, step daemonStep, reason string) (*daemonConn, error) {
	window, first, max := l.opts.reconnectTiming()
	deadline := time.Now().Add(window)

	// Decision 7: the window bounds every attempt, and the waiting screen's
	// end cancels them early. The dial context takes the deadline itself, so
	// the seconds the screen counts down are the seconds the dials get.
	dialCtx, stopDials := context.WithDeadline(ctx, deadline)
	defer stopDials()

	attempts := make(chan reconnect.Attempt)
	outcome := make(chan reconnectOutcome, 1)

	// Decision 2: the attempts run on their own goroutine while the screen
	// waits. The channel closes when they stop, which ends the screen.
	go func() {
		conn, err := l.dialUntilAttached(dialCtx, step, first, max, attempts)
		outcome <- reconnectOutcome{conn: conn, err: err}
	}()

	progResult := l.opts.programRunner(ctx, reconnect.New(step.label(), reason, deadline, attempts), l.stdout)
	if progResult.GaveUp {
		// Decision 2: the user ended the wait, so the attempts stop here, and
		// the outcome below waits for them to have stopped before returning.
		stopDials()
	}
	res := <-outcome
	if progResult.GaveUp {
		// The user gave up: a connection that arrived anyway is not resumed,
		// because the screen that asked for it is gone.
		if res.conn != nil {
			_ = res.conn.close()
		}
		if res.err != nil {
			return nil, res.err
		}
		return nil, errors.New("the wait for the daemon was given up")
	}
	if res.err != nil {
		return nil, res.err
	}
	return res.conn, nil
}

// dialUntilAttached dials step until it attaches, a 4xx refusal ends it at
// once, or the window does, reporting each retry on attempts and closing the
// channel when it stops (decision 3).
func (l *daemonLoop) dialUntilAttached(ctx context.Context, step daemonStep, first, max time.Duration, attempts chan<- reconnect.Attempt) (*daemonConn, error) {
	defer close(attempts)

	wait := first
	var last error
	for n := 1; ; n++ {
		conn, err := dialDaemon(ctx, l.opts, step)
		if err == nil {
			return conn, nil
		}
		last = err
		if !retryableReconnect(err) {
			return nil, err
		}
		select {
		case attempts <- reconnect.Attempt{N: n, Err: err.Error()}:
		case <-ctx.Done():
			// The window ended, or the user gave up. The failure the screen
			// last saw is the one to report.
			return nil, last
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, last
		case <-timer.C:
		}
		if wait < max {
			wait *= 2
			if wait > max {
				wait = max
			}
		}
	}
}

// retryableReconnect reports whether a failed reconnect is worth another
// attempt (decision 3): a refusal with a 4xx status is not, and a dial failure,
// a 5xx refusal and a session the new connection cannot attach yet are.
func retryableReconnect(err error) bool {
	var refused *daemon.AttachError
	if errors.As(err, &refused) {
		return refused.Status < 400 || refused.Status >= 500
	}
	return true
}

// reconnectTiming is the reconnect's window and the waits between attempts: the
// seam's, else production's 60s, 1s and 5s (decision 7).
func (o *options) reconnectTiming() (window, first, max time.Duration) {
	window, first, max = o.reconnectWindow, o.reconnectFirst, o.reconnectMax
	if window <= 0 {
		window = reconnectWindow
	}
	if first <= 0 {
		first = reconnectFirstWait
	}
	if max <= 0 {
		max = reconnectMaxWait
	}
	return window, first, max
}

// addNoteOnce adds text as a note unless the session already carries it near
// its end (decision 6). A read that fails adds the note, as before: a note too
// many is better than a skew nobody is told about.
//
// The check reads a window of the newest messages rather than the single
// newest one: a resumed session appends its own startup messages — the loaded
// skill bodies among them — after the transcript it reloaded, so the note this
// loop wrote before a daemon restart is not the last message when the next
// attach reads. Comparing only the newest message would rewrite the note once
// per restart, which is exactly the stacking decision 6 exists to stop.
func addNoteOnce(ctx context.Context, sess acpclient.Session, text string) error {
	page, err := sess.Messages(ctx, acpwire.MessageQuery{Last: noteOnceWindow})
	if err == nil {
		for _, msg := range page.Messages {
			if msg.Content == text {
				return nil
			}
		}
	}
	return sess.AddNote(ctx, acpwire.Note{Text: text, ContentType: acpwire.ContentTypePlain})
}

// fleetClientFor is the loop's fleet client: the injected one as it is, else
// one built for the loop's transport the first time the loop needs it
// (decision 7): the server profile's HTTPS transport for a server's loop, the
// socket's for the daemon's. The loop asks on its first step, so its client
// outlives every screen it shows.
func (l *daemonLoop) fleetClientFor(ctx context.Context) acpclient.Fleet {
	if l.fleetClient != nil {
		return l.fleetClient
	}
	transport := fleetclient.Socket(l.opts.daemonSocket)
	if l.opts.remote != nil {
		transport = l.opts.remote.fleet
	}
	built := fleetclient.New(transport)
	built.Start(ctx)
	l.built = built
	l.fleetClient = built
	return built
}

// watchFor is the loop's watch, created with the first fleet program and kept
// for the rest of the loop (decision 8): notifications run wherever the fleet
// is open, and every program after it rings the bell through the same watch.
func (l *daemonLoop) watchFor() *fleet.Watch {
	if l.watch == nil {
		l.watch = fleet.NewWatch(l.opts.notifier(l.logger), time.Now)
	}
	return l.watch
}

// sawFleetWiring hands the test what a program was given. Production has no
// hook, and the call is a no-op.
func (l *daemonLoop) sawFleetWiring(client acpclient.Fleet, watch *fleet.Watch) {
	if l.opts.onFleetWiring != nil {
		l.opts.onFleetWiring(client, watch)
	}
}

// notifier is the watch's notifier: the system's when notifications are on,
// and nil when they are off, so the bell still rings and nothing else happens
// (decision 8).
func (o *options) notifier(log *slog.Logger) fleet.Notifier {
	if o.notifyOff {
		return nil
	}
	run := o.notifyRun
	if run == nil {
		run = runNotificationCommand
	}
	return notify.New(runtime.GOOS, run, log)
}

// runNotificationCommand runs a desktop notification's program. The side
// effect is launch's, not the TUI's (decision 6); internal/app/notify bounds it
// and runs it off the watch's path.
func runNotificationCommand(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
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
// session running, the directories are the daemon's project's, and the connect
// panel opens when this machine has no user config yet. local says whether the
// session runs on this machine's daemon: a server's screen never offers the
// panel, because a server's config is not this machine's (decision 8).
func daemonTUIOptions(ctx context.Context, conn *daemonConn, root, home string, logger *slog.Logger, local bool) []tui.Option {
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
	if _, serr := os.Stat(config.UserConfigPath(home)); local && os.IsNotExist(serr) {
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

// versionNote is what a session screen says when its agent is not this build
// (decision 8). A server's agent is not restarted from here, so that note
// carries no advice; the daemon's does.
func (o *options) versionNote(theirs string) string {
	ours := daemonVersionLabel(o.version())
	if o.remote != nil {
		return fmt.Sprintf("This marshal is %s; the server's agent is %s.", ours, daemonVersionLabel(theirs))
	}
	return fmt.Sprintf(
		"This marshal is %s; the daemon's agent is %s. Restart the daemon after upgrading: `marshal daemon stop` then `marshal daemon start`.",
		ours, daemonVersionLabel(theirs),
	)
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
	// restarting is the last marshal/restarting the agent sent: why it is
	// about to close the stream (decision 4).
	restarting *acpwire.RestartingParams
	lostCh     chan struct{}
	done       chan struct{}
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
	// The connection record exists before the dial, so the notification
	// handler can record the agent's marshal/restarting on it (decision 4).
	conn := newDaemonConn()
	// The local daemon's agent is a different process on the same filesystem,
	// so the client reads its files directly. A server's agent is not on it at
	// all (decision 8), so nothing of the session is read as a local file. The
	// version is not pinned either way, because a daemon older than this binary
	// still serves the session (the note says so).
	host, err := rpc.Dial(handshakeCtx, stream, rpc.DialOptions{
		SharedFilesystem: opts.remote == nil,
		Handlers:         rpc.Handlers{Notify: conn.note},
	})
	if err != nil {
		_ = stream.Close()
		return nil, &daemonDialFailure{what: "its agent could not be dialled", err: err}
	}

	sess, err := host.Attach(handshakeCtx, attachment.SessionID)
	if err != nil {
		_ = stream.Close()
		return nil, &daemonDialFailure{what: fmt.Sprintf("session %q could not be attached", attachment.SessionID), err: err}
	}
	return conn.adopt(host, sess), nil
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

// newDaemonConn is the connection record dialDaemon fills as it dials: it
// exists before the host does, so the notification handler Dial installs can
// record a marshal/restarting on it (decision 4).
func newDaemonConn() *daemonConn {
	return &daemonConn{lostCh: make(chan struct{}), done: make(chan struct{})}
}

// adopt records the dialled host and session, and starts watching the
// connection's end: lost closes when it ends on its own, before close claims
// the shutdown.
func (c *daemonConn) adopt(host *rpc.Host, sess *rpc.Session) *daemonConn {
	c.host = host
	c.sess = sess
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

// note records a marshal/restarting: why the agent is about to close the
// stream (decision 4). It runs on the connection's read goroutine.
func (c *daemonConn) note(method string, params json.RawMessage) {
	if method != acpwire.MethodRestarting {
		return
	}
	var restarting acpwire.RestartingParams
	if err := json.Unmarshal(params, &restarting); err != nil {
		return
	}
	c.mu.Lock()
	c.restarting = &restarting
	c.mu.Unlock()
}

// restartReason is the reason the last marshal/restarting gave, "" when none
// arrived.
func (c *daemonConn) restartReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.restarting == nil {
		return ""
	}
	return c.restarting.Reason
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
