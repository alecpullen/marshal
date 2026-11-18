package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"

	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/buildinfo"
	"marshal/internal/daemon"
	"marshal/internal/llm/provider"
	"marshal/internal/repo"
)

// daemonClient is the part of daemon.Client that `status`, `pin` and
// `restart` need, so a test answers with a scripted daemon instead of opening
// a socket.
type daemonClient interface {
	Info(ctx context.Context) (daemon.Info, error)
	Pin(ctx context.Context, root string, pinned bool) (daemon.PinState, error)
	Restart(ctx context.Context, now bool, progress func(daemon.RestartProgress)) error
}

// The two seams a test replaces: the service manager and the socket client.
// Neither test ever reaches a real service manager or a real daemon.
var daemonManagerFor = func(home string) (*daemon.Manager, error) {
	return daemon.NewManager(home)
}

var daemonClientFor = func(socket string) daemonClient {
	return daemon.NewClient(socket)
}

// daemonStatusTimeout bounds the socket call: `status` has to answer even
// when the service is wedged, so a silent socket is reported, not waited on.
const daemonStatusTimeout = 2 * time.Second

// daemonPinTimeout bounds a pin, which may start a child and resolve its
// environment.
const daemonPinTimeout = 60 * time.Second

// daemonReadyPoll is the gap between readiness probes, and daemonStartTimeout
// the longest wait for a freshly loaded service to answer. It is a var so a
// test can shorten it.
const daemonReadyPoll = 100 * time.Millisecond

var daemonStartTimeout = 5 * time.Second

// runDaemon implements
// `marshal daemon <install|start|stop|status|restart|uninstall>`, and
// dispatches `refresh` as well.
//
// It owns the user-facing half of the local daemon: it resolves the binaries
// and paths decision 10 names, renders them into a unit through
// internal/daemon, and reports what the service manager and the socket say.
// It never writes a unit itself and never runs a service-manager command —
// that is all daemon.Manager, behind the daemonManagerFor seam.
//
// `refresh` is dispatched but left out of the usage hint: the daemon's unit
// runs it, so it is an internal half of the daemon rather than a subcommand a
// user is meant to type.
//
// The I/O shape follows runSnapshots exactly (ctx, args, stdin, stdout,
// stderr) so main.go's dispatch is uniform and tests can substitute streams.
func runDaemon(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("marshal daemon: a subcommand is required (install|start|stop|status|restart|join-token|token|pin|unpin|spawn|uninstall)")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "install":
		return runDaemonInstall(ctx, rest, stdout, stderr)
	case "start", "stop", "uninstall":
		return runDaemonService(ctx, sub, rest, stdout, stderr)
	case "status":
		return runDaemonStatus(ctx, rest, stdout, stderr)
	case "restart":
		return runDaemonRestart(ctx, rest, stdout, stderr)
	case "join-token":
		return runDaemonJoinToken(ctx, rest, stdin, stdout, stderr)
	case "token":
		return runDaemonToken(rest, stdout)
	case "pin":
		return runDaemonPin(ctx, rest, true, stdout, stderr)
	case "unpin":
		return runDaemonPin(ctx, rest, false, stdout, stderr)
	case "spawn":
		return runDaemonSpawn(ctx, rest, stdout, stderr)
	case "refresh":
		return runDaemonRefresh(ctx, rest, stderr)
	default:
		return fmt.Errorf("marshal daemon: unknown subcommand %q (install|start|stop|status|restart|join-token|token|pin|unpin|spawn|uninstall)", sub)
	}
}

// ---------------------------------------------------------------------------
// refresh
// ---------------------------------------------------------------------------

// oauthRefreshRunner is the helper's body; tests replace it.
var oauthRefreshRunner = app.RunOAuthRefresh

// daemonSignalContext is the helper's own signal context (decision 9). Tests
// replace it rather than signalling the test process, which would kill it if
// the seam were ever removed.
var daemonSignalContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
}

// runDaemonRefresh implements `marshal daemon refresh`: it runs the OAuth
// refresh helper the daemon's unit starts, which keeps the user config's
// OAuth providers refreshed until the helper is interrupted.
//
// Usage: marshal daemon refresh
func runDaemonRefresh(ctx context.Context, args []string, stderr io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("marshal daemon refresh: unexpected argument %q", args[0])
	}
	home, err := daemonHome()
	if err != nil {
		return err
	}
	// Decision 9: main passes a background context, so the helper installs
	// its own signal context: the daemon stops the helper by signalling it,
	// and SIGTERM from the service manager is an orderly stop, not a failure.
	ctx, stop := daemonSignalContext(ctx)
	defer stop()
	// Decision 7: a text logger on standard error at info, so the helper's
	// lines land in the daemon's log.
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return oauthRefreshRunner(ctx, app.OAuthRefreshOptions{
		HomeDir: home,
		Auth:    provider.DefaultAuthResolver(),
		Logger:  logger,
	})
}

// ---------------------------------------------------------------------------
// install
// ---------------------------------------------------------------------------

// runDaemonInstall renders and loads the unit.
//
// Usage: marshal daemon install [--webbridge PATH] [--no-linger]
func runDaemonInstall(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	webbridge := fs.String("webbridge", "", "the webbridge binary to run (default: beside marshal, else on PATH)")
	noLinger := fs.Bool("no-linger", false, "do not enable systemd's linger for this user (Linux)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return fmt.Errorf("marshal daemon install: unexpected argument %q", rest[0])
	}

	home, err := daemonHome()
	if err != nil {
		return err
	}
	m, err := daemonManagerFor(home)
	if err != nil {
		return err
	}
	spec, err := daemonUnitSpec(*webbridge, home)
	if err != nil {
		return err
	}
	if err := m.Install(ctx, spec); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "unit: %s\n", m.UnitPath())
	fmt.Fprintf(stdout, "socket: %s\n", spec.Paths.Socket)
	daemonEnsureLinger(ctx, m, *noLinger, stdout)
	daemonReadyNote(ctx, spec.Paths, stdout)
	return nil
}

// daemonEnsureLinger enables the user's linger bit on Linux and says what it
// found. It never fails the install: the unit is written and loaded by the
// time it runs, so linger decides only whether the daemon survives logout.
func daemonEnsureLinger(ctx context.Context, m *daemon.Manager, skip bool, stdout io.Writer) {
	if m.GOOS != "linux" {
		return
	}
	if skip {
		fmt.Fprintln(stdout, "linger: left off (--no-linger)")
		return
	}
	state, err := m.Linger(ctx)
	if err != nil {
		fmt.Fprintf(stdout, "linger: not enabled (read Linger: %v)\n", err)
		return
	}
	if !state.Supported {
		// Supported is false for two causes — loginctl missing, and no
		// logind to answer for this user — so the parenthetical names
		// neither.
		fmt.Fprintln(stdout, "linger: not supported (loginctl unavailable)")
		return
	}
	if state.Enabled {
		fmt.Fprintln(stdout, "linger: already enabled")
		return
	}
	if err := m.EnableLinger(ctx); err != nil {
		fmt.Fprintf(stdout, "linger: not enabled (%v)\n", err)
		return
	}
	fmt.Fprintln(stdout, "linger: enabled")
}

// daemonLingerLine is daemon status's linger line, and whether this platform
// has one at all. macOS has no equivalent, so it gets no line rather than a
// "not supported" one for something it does not need.
func daemonLingerLine(ctx context.Context, m *daemon.Manager) (string, bool) {
	if m.GOOS != "linux" {
		return "", false
	}
	// status has to answer even when loginctl hangs: the socket read below
	// is bounded the same way, so a stuck read is a line, not a wait.
	lingerCtx, cancel := context.WithTimeout(ctx, daemonStatusTimeout)
	defer cancel()
	state, err := m.Linger(lingerCtx)
	if err != nil {
		return fmt.Sprintf("linger: unknown (%v)", err), true
	}
	if !state.Supported {
		return "linger: not supported (loginctl unavailable)", true
	}
	if state.Enabled {
		return "linger: enabled", true
	}
	return "linger: disabled (the daemon stops at logout)", true
}

// daemonUnitSpec is decision 10's unit: the webbridge, the resolved marshal,
// the paths internal/daemon resolves, the location environment, the idle
// session timeout the user config gives, and the loopback address the Studio
// is served on.
func daemonUnitSpec(webbridgeOverride, home string) (daemon.UnitSpec, error) {
	webbridge, err := daemon.FindWebbridge(webbridgeOverride)
	if err != nil {
		return daemon.UnitSpec{}, err
	}
	marshal, err := resolvedExecutable()
	if err != nil {
		return daemon.UnitSpec{}, err
	}
	// Decision 9: the timeout is read from the user config alone, so a
	// project cannot change the daemon's behaviour, and a change takes
	// effect at the next install.
	cfg, err := config.Load(config.LoadOptions{HomeDir: home, SkipProjectConfig: true})
	if err != nil {
		return daemon.UnitSpec{}, fmt.Errorf("read the user config: %w", err)
	}
	studioAddr, err := daemonStudioAddr(cfg.Daemon.StudioAddr)
	if err != nil {
		return daemon.UnitSpec{}, err
	}
	// The join is read from the same user config, for the same reason: a
	// project must not point this machine's daemon at a relay.
	join, err := daemonJoin(cfg.Daemon)
	if err != nil {
		return daemon.UnitSpec{}, err
	}
	paths := daemon.PathsFor(home)
	// A join with no stored secret would install a unit that dies at start, so
	// refuse here, where the user can act on it (D12: `marshal daemon
	// join-token` is what stores it).
	if join != nil {
		if _, err := os.Stat(paths.JoinToken); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return daemon.UnitSpec{}, fmt.Errorf("daemon.join_url is set but %s is missing: run marshal daemon join-token first", paths.JoinToken)
			}
			return daemon.UnitSpec{}, fmt.Errorf("read the join secret: %w", err)
		}
	}
	return daemon.UnitSpec{
		Webbridge:          webbridge,
		Marshal:            marshal,
		Paths:              paths,
		Env:                daemonEnv(),
		IdleSessionTimeout: cfg.Daemon.IdleSessionTimeout,
		StudioAddr:         studioAddr,
		Join:               join,
	}, nil
}

// daemonJoin translates daemon.join_url, join_name and join_ca_file into the
// relay the unit joins, or nil when the config names none (P8 D12).
//
// A join without a name is refused here rather than at the relay: the name is
// what the fleet's reader uses to tell this daemon from the relay's own
// agents, so an unnamed join would join and be unreadable.
func daemonJoin(cfg config.DaemonConfig) (*daemon.Join, error) {
	url := strings.TrimSpace(cfg.JoinURL)
	if url == "" {
		return nil, nil
	}
	name := strings.TrimSpace(cfg.JoinName)
	if name == "" {
		return nil, fmt.Errorf("daemon.join_url is set but daemon.join_name is empty: name the daemon that joins %s", url)
	}
	return &daemon.Join{
		URL:    url,
		Name:   name,
		CAFile: strings.TrimSpace(cfg.JoinCAFile),
	}, nil
}

// daemonStudioAddr validates daemon.studio_addr and returns the address to
// serve on, or "" for none.
//
// The Studio is the daemon's own HTTP face, so it is loopback-only: this
// daemon runs host processes with the user's full authority and has no
// authentication but a bearer token in a 0600 file. Binding it to a routable
// address would publish that authority, so an address that is not loopback is
// refused here rather than written into the unit and discovered later.
func daemonStudioAddr(setting string) (string, error) {
	addr := strings.TrimSpace(setting)
	switch addr {
	case "", "off", "none":
		return "", nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("daemon.studio_addr %q: %w", setting, err)
	}
	if port == "" {
		return "", fmt.Errorf("daemon.studio_addr %q: a port is required", setting)
	}
	if host == "" {
		return "", fmt.Errorf("daemon.studio_addr %q: the Studio is loopback-only; give an address like 127.0.0.1:7788", setting)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		resolved, err := net.LookupIP(host)
		if err != nil {
			return "", fmt.Errorf("daemon.studio_addr %q: %w", setting, err)
		}
		for _, candidate := range resolved {
			if !candidate.IsLoopback() {
				return "", fmt.Errorf("daemon.studio_addr %q: the Studio is loopback-only; %s resolves to %s", setting, host, candidate)
			}
		}
		return addr, nil
	}
	if !ip.IsLoopback() {
		return "", fmt.Errorf("daemon.studio_addr %q: the Studio is loopback-only", setting)
	}
	return addr, nil
}

// resolvedExecutable is the running marshal with symlinks resolved and made
// absolute: the unit outlives the shell that installed it, so the bin-dir
// symlink it may have been launched through cannot be relied on.
func resolvedExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find the marshal binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	return exe, nil
}

// daemonEnv is the installing process's PATH and SHELL plus the location
// variables that were set (decision 10). An unset or empty variable is
// omitted rather than written empty, so the daemon inherits Marshal's own
// defaults for it — and so an install from a context with no PATH does not
// hand the service an empty one, which would leave every command its agents
// spawn unresolvable.
func daemonEnv() map[string]string {
	env := make(map[string]string, 6)
	for _, key := range []string{
		"PATH",
		"SHELL",
		"MARSHAL_CONFIG_DIR",
		"MARSHAL_DATA_DIR",
		"XDG_CONFIG_HOME",
		"XDG_DATA_HOME",
	} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	return env
}

// ---------------------------------------------------------------------------
// start, stop, uninstall
// ---------------------------------------------------------------------------

// runDaemonService runs one of the manager's own operations and says so in
// one line. Decision 11 lives inside daemon.Manager: `start` loads, `stop`
// unloads, `uninstall` stops, disables and removes the unit and keeps the
// data directory.
func runDaemonService(ctx context.Context, sub string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return fmt.Errorf("marshal daemon %s: unexpected argument %q", sub, rest[0])
	}

	home, err := daemonHome()
	if err != nil {
		return err
	}
	m, err := daemonManagerFor(home)
	if err != nil {
		return err
	}
	switch sub {
	case "start":
		if err := m.Start(ctx); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "started")
		daemonReadyNote(ctx, daemon.PathsFor(home), stdout)
	case "stop":
		if err := m.Stop(ctx); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "stopped")
	case "uninstall":
		if err := m.Uninstall(ctx); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "uninstalled; data kept in %s\n", daemon.PathsFor(home).Dir)
		// The linger bit is per user, not per unit: Marshal set it at
		// install, but other user units depend on it, so it stays.
		if state, err := m.Linger(ctx); err == nil && state.Supported && state.Enabled {
			fmt.Fprintln(stdout, "linger: left enabled (per user, not per unit)")
		}
	}
	return nil
}

// daemonReadyNote says whether the daemon answered after being loaded, and
// names its log when it has not yet started answering. It reports nothing as
// an error: the service manager owns the process, so a slow start is a
// warning, not a failure — but `install && status` in one script must not
// exit non-zero only because launchd is still starting the daemon.
func daemonReadyNote(ctx context.Context, paths daemon.Paths, stdout io.Writer) {
	if daemonAnswers(ctx, paths.Socket) {
		fmt.Fprintln(stdout, "answering")
		return
	}
	fmt.Fprintf(stdout, "not answering yet; its log is %s\n", paths.Log)
}

// daemonAnswers polls the socket until the daemon answers or the wait runs
// out.
func daemonAnswers(ctx context.Context, socket string) bool {
	client := daemonClientFor(socket)
	deadline := time.Now().Add(daemonStartTimeout)
	for {
		infoCtx, cancel := context.WithTimeout(ctx, daemonStatusTimeout)
		_, err := client.Info(infoCtx)
		cancel()
		if err == nil {
			return true
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(daemonReadyPoll):
		}
	}
}

// ---------------------------------------------------------------------------
// join-token, token
// ---------------------------------------------------------------------------

// runDaemonJoinToken implements `marshal daemon join-token`: it reads the
// secret this daemon joins its relay with and stores it 0600 under the daemon
// directory, where the unit's --join-token-file points (P8 D12).
//
// The secret lives in a file of its own rather than in the config: the config
// is a document a user shares and edits, and this is a credential the daemon
// reads at start, exactly where the Studio's token lives.
func runDaemonJoinToken(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("marshal daemon join-token: unexpected argument %q", args[0])
	}
	home, err := daemonHome()
	if err != nil {
		return err
	}
	secret, err := readDaemonJoinSecret(stdin, stderr)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	paths := daemon.PathsFor(home)
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		return fmt.Errorf("marshal daemon join-token: create %s: %w", paths.Dir, err)
	}
	if err := daemonWritePrivate(paths.JoinToken, secret); err != nil {
		return fmt.Errorf("marshal daemon join-token: %w", err)
	}
	fmt.Fprintln(stdout, "Stored the join secret. Run marshal daemon install to apply it.")
	return nil
}

// runDaemonToken implements `marshal daemon token`: it writes the credential
// the daemon's listener answers on — the Studio's token when one is served,
// the join's secret otherwise — and a newline to stdout, for a program to
// read. It refuses a terminal, as `marshal fleet token` does, so a credential
// never reaches scrollback.
func runDaemonToken(args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("marshal daemon token: unexpected argument %q", args[0])
	}
	if fleetStdoutIsTerminal(stdout) {
		return errors.New("marshal daemon token: a token is not printed to a terminal; pipe it, as in marshal daemon token | pbcopy")
	}
	home, err := daemonHome()
	if err != nil {
		return err
	}
	path := daemon.PathsFor(home).Token
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("marshal daemon token: the daemon has no token yet: set daemon.studio_addr or daemon.join_url and run marshal daemon install")
	}
	if err != nil {
		return fmt.Errorf("marshal daemon token: read %s: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return fmt.Errorf("marshal daemon token: %s is empty: run marshal daemon install", path)
	}
	if _, err := fmt.Fprintln(stdout, token); err != nil {
		return fmt.Errorf("marshal daemon token: %w", err)
	}
	return nil
}

// readDaemonJoinSecret reads the join secret the way readFleetToken reads a
// fleet token: without echo and behind a prompt on a terminal, else one line
// from stdin, so a password manager can pipe it in.
func readDaemonJoinSecret(stdin io.Reader, stderr io.Writer) (string, error) {
	var raw []byte
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(f.Fd()) {
		fmt.Fprint(stderr, "Join secret: ")
		b, err := term.ReadPassword(f.Fd())
		fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("marshal daemon join-token: read the secret: %w", err)
		}
		raw = b
	} else {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("marshal daemon join-token: read the secret: %w", err)
		}
		raw = []byte(line)
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", errors.New("marshal daemon join-token: the secret is empty")
	}
	return secret, nil
}

// daemonWritePrivate writes body to path with a newline, at 0600, replacing
// any file there — including one whose mode was loosened since — so a secret
// is never left readable by anyone but its owner.
func daemonWritePrivate(path, body string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := f.WriteString(body + "\n"); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

// runDaemonStatus prints decision 12's lines, or writes the JSON document
// the tray app reads.
//
// Text mode prints the service state, the linger line where the platform has
// one, the socket and whether it answered, and — for an answering daemon —
// its version against this marshal's, its start time, its refresh helper, the
// relay it joins when it joins one, its projects and each project child. It
// exits non-zero when the socket does not answer, but only after printing what
// it found: a service that is loaded and silent is exactly the case the user
// is running `status` for.
//
// With --json it exits 0 whenever it wrote the document: the answer to
// "is the daemon answering" is in the document, so a machine consumer must
// not have to read an exit code for it.
func runDaemonStatus(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "write the status as one JSON document")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return fmt.Errorf("marshal daemon status: unexpected argument %q", rest[0])
	}

	home, err := daemonHome()
	if err != nil {
		return err
	}
	m, err := daemonManagerFor(home)
	if err != nil {
		return err
	}
	paths := daemon.PathsFor(home)

	state, err := m.State(ctx)
	if err != nil {
		return fmt.Errorf("read the marshal daemon service: %w", err)
	}

	status := daemon.Status{SchemaVersion: daemon.StatusSchema, Service: state}

	infoCtx, cancel := context.WithTimeout(ctx, daemonStatusTimeout)
	defer cancel()
	info, infoErr := daemonClientFor(paths.Socket).Info(infoCtx)
	if infoErr == nil {
		status.Answering = true
		status.Daemon = &info
	}

	if *asJSON {
		// One loginctl call per invocation: text mode asks
		// daemonLingerLine instead of reading it here.
		if m.GOOS == "linux" {
			if linger, err := m.Linger(ctx); err == nil {
				status.Linger = linger
			}
		}
		return json.NewEncoder(stdout).Encode(status)
	}

	fmt.Fprintln(stdout, daemonServiceLine(state))
	if line, ok := daemonLingerLine(ctx, m); ok {
		fmt.Fprintln(stdout, line)
	}
	if infoErr != nil {
		fmt.Fprintf(stdout, "socket: %s (not answering)\n", paths.Socket)
		return fmt.Errorf("the daemon is not answering on %s", paths.Socket)
	}
	fmt.Fprintf(stdout, "socket: %s (answering)\n", paths.Socket)
	marshalVersion := daemonVersion()
	fmt.Fprintln(stdout, daemonInfoLine(info, marshalVersion))
	for _, line := range daemonRestartText(info.Restart, "daemon ") {
		fmt.Fprintln(stdout, line)
	}
	fmt.Fprintln(stdout, daemonRefreshLine(info))
	if line, ok := daemonJoinLine(info); ok {
		fmt.Fprintln(stdout, line)
	}
	for _, child := range info.Children {
		fmt.Fprintln(stdout, daemonChildLine(child, marshalVersion))
		for _, line := range daemonRestartLines(child) {
			fmt.Fprintln(stdout, line)
		}
		if warning := strings.TrimSpace(child.EnvWarning); warning != "" {
			fmt.Fprintf(stdout, "  environment: %s\n", warning)
		}
	}
	for _, line := range daemonPinnedLines(info) {
		fmt.Fprintln(stdout, line)
	}
	return nil
}

// ---------------------------------------------------------------------------
// restart
// ---------------------------------------------------------------------------

// runDaemonRestart implements `marshal daemon restart [--now]`: it asks the
// daemon to re-execute its own binary, says what holds the restart while the
// daemon sweeps, and reports the version it came back on.
//
// The request is the daemon's to grant or refuse, so this command adds no
// policy of its own: without --now the daemon waits for idle, and the waiting
// lines are its account of what it is waiting for. ctrl+c withdraws the
// request — the command returns the context's error and the daemon stops
// waiting.
//
// A daemon that does not answer has no restart to ask for, so the command does
// what `start` does and loads the service instead.
func runDaemonRestart(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon restart", flag.ContinueOnError)
	fs.SetOutput(stderr)
	now := fs.Bool("now", false, "restart now, without waiting for idle")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return fmt.Errorf("marshal daemon restart: unexpected argument %q", rest[0])
	}

	home, err := daemonHome()
	if err != nil {
		return err
	}
	paths := daemon.PathsFor(home)
	socket := paths.Socket

	infoCtx, cancel := context.WithTimeout(ctx, daemonStatusTimeout)
	first, infoErr := daemonClientFor(socket).Info(infoCtx)
	cancel()
	if infoErr != nil {
		// Nothing answered, so there is no daemon to ask: start it, exactly
		// as `start` does, rather than reporting a restart of nothing.
		m, err := daemonManagerFor(home)
		if err != nil {
			return err
		}
		if err := m.Start(ctx); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "started")
		daemonReadyNote(ctx, paths, stdout)
		return nil
	}

	// A waiting line repeats on every sweep, so only a change in it is news;
	// the restarting line is the one that starts the clock for the poll below.
	lastWaiting := ""
	var restartedAt time.Time
	err = daemonClientFor(socket).Restart(ctx, *now, func(step daemon.RestartProgress) {
		if len(step.WaitingFor) != 0 {
			line := "waiting for " + strings.Join(step.WaitingFor, "; ")
			if line != lastWaiting {
				lastWaiting = line
				fmt.Fprintln(stdout, line)
			}
			return
		}
		if step.Restarting {
			restartedAt = time.Now()
			fmt.Fprintf(stdout, "restarting on %s\n", step.Version)
		}
	})
	if err != nil {
		// ctrl+c ends the context, which withdraws the request: the
		// context's error is the answer, not a daemon failure.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("marshal daemon restart: %w", err)
	}
	if restartedAt.IsZero() {
		restartedAt = time.Now()
	}

	// The daemon keeps its process ID, so a new start time is what says the
	// re-execution happened; the wait is longer than a fresh start's because
	// a restart first lets every child finish.
	if info, ok := daemonWaitsForRestart(ctx, socket, first.StartedAt, restartedAt.Add(daemonStartTimeout+30*time.Second)); ok {
		fmt.Fprintf(stdout, "restarted: version %s\n", info.Version)
		return nil
	}
	fmt.Fprintf(stdout, "not answering yet; its log is %s\n", paths.Log)
	return nil
}

// daemonWaitsForRestart polls until the daemon reports a start time later
// than before — the one thing a re-execution changes — and returns that
// answer. It reports false when the deadline passes or the context ends.
func daemonWaitsForRestart(ctx context.Context, socket string, restartedFrom int64, deadline time.Time) (daemon.Info, bool) {
	for {
		infoCtx, cancel := context.WithTimeout(ctx, daemonStatusTimeout)
		info, err := daemonClientFor(socket).Info(infoCtx)
		cancel()
		if err == nil && info.StartedAt > restartedFrom {
			return info, true
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return daemon.Info{}, false
		}
		select {
		case <-ctx.Done():
			return daemon.Info{}, false
		case <-time.After(daemonReadyPoll):
		}
	}
}

// ---------------------------------------------------------------------------
// pin, unpin
// ---------------------------------------------------------------------------

// runDaemonPin implements `marshal daemon pin [dir]` and `unpin [dir]`.
//
// The directory defaults to the working directory and is resolved to its
// repository root first, exactly as daemon mode resolves the project it
// serves: a pin names a project, and a project is anchored at its git root.
// The daemon does the pinning, so the answer is its answer.
func runDaemonPin(ctx context.Context, args []string, pinned bool, stdout, stderr io.Writer) error {
	sub := "unpin"
	if pinned {
		sub = "pin"
	}
	if len(args) > 1 {
		return fmt.Errorf("marshal daemon %s: unexpected argument %q", sub, args[1])
	}
	dir := ""
	if len(args) == 1 {
		dir = args[0]
	}
	if strings.HasPrefix(dir, "-") {
		return fmt.Errorf("marshal daemon %s: %q is not a directory", sub, dir)
	}
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("marshal daemon %s: resolve the working directory: %w", sub, err)
		}
		dir = wd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("marshal daemon %s: resolve %q: %w", sub, dir, err)
	}
	root := repo.Root(abs)

	home, err := daemonHome()
	if err != nil {
		return err
	}
	socket := daemon.PathsFor(home).Socket

	pinCtx, cancel := context.WithTimeout(ctx, daemonPinTimeout)
	defer cancel()
	if _, err := daemonClientFor(socket).Pin(pinCtx, root, pinned); err != nil {
		// A transport failure means the daemon never answered; a refusal is
		// the daemon's own sentence, so it is reported as itself.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("the daemon is not answering on %s: %w", socket, err)
		}
		return fmt.Errorf("marshal daemon %s: %w", sub, err)
	}
	if pinned {
		fmt.Fprintf(stdout, "pinned %s\n", root)
		return nil
	}
	fmt.Fprintf(stdout, "unpinned %s\n", root)
	return nil
}

// daemonServiceLine is decision 12's first line.
func daemonServiceLine(state daemon.ServiceState) string {
	switch {
	case !state.Installed:
		return "service: not installed"
	case state.Running && state.PID > 0:
		return fmt.Sprintf("service: loaded and running (pid %d)", state.PID)
	case state.Running:
		return "service: loaded and running"
	default:
		return "service: stopped"
	}
}

// daemonInfoLine is decision 12's third line.
func daemonInfoLine(info daemon.Info, marshalVersion string) string {
	started := "unknown"
	if info.StartedAt > 0 {
		// startedAt is Unix milliseconds on the wire, which is what the
		// bridge sends (web/bridge/daemoninfo.go).
		started = time.UnixMilli(info.StartedAt).UTC().Format(time.RFC3339)
	}
	projects := "(none)"
	if len(info.Projects) > 0 {
		projects = strings.Join(info.Projects, ", ")
	}
	return fmt.Sprintf("daemon: version %s (this marshal %s), started %s, projects: %s",
		info.Version, marshalVersion, started, projects)
}

// daemonRefreshLine renders info's refresh helper: its pid, and its restart
// count when it has restarted. A daemon that reports no helper — an older
// build, or one run without --oauth-refresh — says `off`, so the absence is
// stated rather than left to be inferred from a missing line.
//
// A helper between runs reports no process, because its supervisor is waiting
// out a restart backoff: calling that "running" would misinform exactly the
// reader who consults this line because refreshing is not working.
func daemonRefreshLine(info daemon.Info) string {
	if info.Refresh == nil {
		return "refresh: off"
	}
	line := "refresh: not running"
	if info.Refresh.PID > 0 {
		line = fmt.Sprintf("refresh: running (pid %d)", info.Refresh.PID)
	}
	if info.Refresh.Restarts != 0 {
		line += fmt.Sprintf(", restarted %d %s", info.Refresh.Restarts, restartCount(info.Refresh.Restarts))
	}
	return line
}

// daemonJoinLine renders the relay the daemon joins (P8 D12): where it joined
// and under what name, and — when it is not connected — its last error, which
// is why the reader consulted this line.
//
// A daemon that joins no relay reports no line at all: there is nothing to say
// about a join that does not exist, and the absence is the answer.
func daemonJoinLine(info daemon.Info) (string, bool) {
	if info.Join == nil {
		return "", false
	}
	if info.Join.Connected {
		return fmt.Sprintf("relay: joined %s as %s", info.Join.URL, info.Join.Name), true
	}
	line := fmt.Sprintf("relay: %s as %s, not connected", info.Join.URL, info.Join.Name)
	if lastErr := strings.TrimSpace(info.Join.LastError); lastErr != "" {
		line += " (" + lastErr + ")"
	}
	return line, true
}

// restartCount is the noun for n restarts.
func restartCount(n int) string {
	if n == 1 {
		return "time"
	}
	return "times"
}

// daemonChildLine is one project child's line under the daemon's: its root,
// its version and its session count, and — when the child runs another
// build — this marshal's version beside it, as the daemon's line does.
//
// An unstamped child build reports `dev` for the same reason daemonVersion
// does: the child is a marshal, and an empty version is a build with no
// stamp, not an unknown one.
func daemonChildLine(child daemon.ChildInfo, marshalVersion string) string {
	childVersion := child.Version
	if childVersion == "" {
		childVersion = "dev"
	}
	line := fmt.Sprintf("child %s: version %s, %d %s",
		child.Root, childVersion, len(child.Sessions), sessionCount(len(child.Sessions)))
	if childVersion != marshalVersion {
		line += fmt.Sprintf(" (this marshal %s)", marshalVersion)
	}
	if child.Pinned {
		line += ", pinned"
	}
	return line
}

// daemonRestartText renders one held restart with prefix in front of each
// line: what the daemon is holding it for and, while something holds it, what
// it waits for. A restart that was tried and refused says so on a line of its
// own — the reasons are the same, but the build the daemon would swap in
// failed, which is the state a reader most needs told.
//
// The reasons are the pending line's subject, so a restart held with none
// (only a waiter, or only a refusal) prints no bare `restart pending: `: the
// line says what it has and nothing more.
func daemonRestartText(restart *daemon.DaemonRestart, prefix string) []string {
	if restart == nil {
		return nil
	}
	var lines []string
	if len(restart.Reasons) != 0 {
		line := prefix + "restart pending: " + strings.Join(restart.Reasons, ", ")
		if waiting := restart.WaitingFor; waiting != "" {
			line += ", waiting for " + waiting
		}
		lines = append(lines, line)
	}
	if refused := restart.Refused; refused != "" {
		lines = append(lines, prefix+"restart refused: "+refused)
	}
	return lines
}

// daemonRestartLines are the restart lines under a child's, indented so they
// read as that child's.
func daemonRestartLines(child daemon.ChildInfo) []string {
	return daemonRestartText(child.Restart, "  ")
}

// daemonPinnedLines are the `pinned <root>: not running` lines, one per
// pinned root no child serves: a pin outlives the child that served it, so a
// pinned project with nothing running is the state a reader most needs told.
func daemonPinnedLines(info daemon.Info) []string {
	served := make(map[string]bool, len(info.Children))
	for _, child := range info.Children {
		served[child.Root] = true
	}
	var lines []string
	for _, root := range info.Pinned {
		if served[root] {
			continue
		}
		lines = append(lines, fmt.Sprintf("pinned %s: not running", root))
	}
	return lines
}

// sessionCount is the noun for n sessions.
func sessionCount(n int) string {
	if n == 1 {
		return "session"
	}
	return "sessions"
}

// daemonVersion is this marshal's version, as run computes it for the banner,
// the User-Agent and the agent: a stamped release, else the dev form naming
// the commit (decision 2).
func daemonVersion() string {
	return buildinfo.Version(version)
}

// daemonHome is where PathsFor and NewManager anchor.
func daemonHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return home, nil
}
