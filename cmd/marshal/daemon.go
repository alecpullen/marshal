package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/buildinfo"
	"marshal/internal/daemon"
	"marshal/internal/fleetprofile"
	"marshal/internal/llm/provider"
	"marshal/internal/repo"
	"marshal/internal/tunnels"
	"marshal/web/bridge"
	"marshal/web/bridge/contract"
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
// dispatches `refresh` and `tunnels` as well.
//
// It owns the user-facing half of the local daemon: it resolves the binaries
// and paths decision 10 names, renders them into a unit through
// internal/daemon, and reports what the service manager and the socket say.
// It never writes a unit itself and never runs a service-manager command —
// that is all daemon.Manager, behind the daemonManagerFor seam.
//
// `refresh` and `tunnels` are dispatched but left out of the usage hint: the
// daemon's unit runs them, so they are the internal halves of the daemon
// rather than subcommands a user is meant to type.
//
// The I/O shape follows runSnapshots exactly (ctx, args, stdin, stdout,
// stderr) so main.go's dispatch is uniform and tests can substitute streams.
func runDaemon(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("marshal daemon: a subcommand is required (install|start|stop|status|restart|pin|unpin|spawn|uninstall)")
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
	case "pin":
		return runDaemonPin(ctx, rest, true, stdout, stderr)
	case "unpin":
		return runDaemonPin(ctx, rest, false, stdout, stderr)
	case "spawn":
		return runDaemonSpawn(ctx, rest, stdout, stderr)
	case "refresh":
		return runDaemonRefresh(ctx, rest, stderr)
	case "tunnels":
		return runDaemonTunnels(ctx, rest, stderr)
	default:
		return fmt.Errorf("marshal daemon: unknown subcommand %q (install|start|stop|status|restart|pin|unpin|spawn|uninstall)", sub)
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
// tunnels
// ---------------------------------------------------------------------------

// tunnelsRunner is the helper's body; tests replace it.
var tunnelsRunner = tunnels.Run

// runDaemonTunnels implements `marshal daemon tunnels --socket PATH`: it runs
// the SSH tunnel helper the daemon's unit starts, which keeps one forward per
// fleet.toml host to that host's daemon socket and reports each one's state
// back through the daemon's own socket.
//
// Usage: marshal daemon tunnels --socket PATH
func runDaemonTunnels(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon tunnels", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", "", "the daemon's socket, which the helper reports hosts to (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return fmt.Errorf("marshal daemon tunnels: unexpected argument %q", rest[0])
	}
	if *socket == "" {
		return errors.New("marshal daemon tunnels: --socket is required")
	}
	home, err := daemonHome()
	if err != nil {
		return err
	}
	// Decision 9, as for refresh: the daemon stops the helper by signalling
	// it, and SIGTERM from the service manager is an orderly stop.
	ctx, stop := daemonSignalContext(ctx)
	defer stop()
	return tunnelsRunner(ctx, tunnels.Config{
		FleetFile: fleetprofile.Path(home),
		Dir:       filepath.Join(filepath.Dir(*socket), "hosts"),
		Report:    daemon.NewClient(*socket).ReportHosts,
		Probe: func(ctx context.Context, socket string) (string, error) {
			info, err := daemon.NewClient(socket).Info(ctx)
			if err != nil {
				return "", err
			}
			return info.PreviewAddr, nil
		},
		Stderr: stderr,
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
	spec, retired, err := daemonUnitSpec(*webbridge, home)
	if err != nil {
		return err
	}
	// A retired key is read by nothing any more (D10, D6), so naming it is
	// the only way its author learns to drop it.
	for _, key := range retired {
		fmt.Fprintf(stderr, "marshal daemon install: daemon.%s is retired and ignored; %s\n", key, daemonRetiredHint(key))
	}
	if err := m.Install(ctx, spec); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "unit: %s\n", m.UnitPath())
	fmt.Fprintf(stdout, "socket: %s\n", spec.Paths.Socket)
	// The secrets the retired listeners read are gone with them: a stale
	// 0600 credential under the daemon directory outlives the feature that
	// used it, so the install that replaced the listener removes it.
	daemonRemoveOldTokenFiles(daemon.PathsFor(home).Dir, stderr)
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
// the paths internal/daemon resolves, the location environment, the web
// listener and the origins it advertises, and the idle session timeout the
// user config gives. The retired [daemon] keys are answered alongside it, so
// the install can name them.
func daemonUnitSpec(webbridgeOverride, home string) (daemon.UnitSpec, []string, error) {
	webbridge, err := daemon.FindWebbridge(webbridgeOverride)
	if err != nil {
		return daemon.UnitSpec{}, nil, err
	}
	marshal, err := resolvedExecutable()
	if err != nil {
		return daemon.UnitSpec{}, nil, err
	}
	// Decision 9: these are read from the user config alone, so a project
	// cannot change the daemon's behaviour, and a change takes effect at the
	// next install.
	cfg, err := config.Load(config.LoadOptions{HomeDir: home, SkipProjectConfig: true})
	if err != nil {
		return daemon.UnitSpec{}, nil, fmt.Errorf("read the user config: %w", err)
	}
	webListen, err := daemonWebListen(cfg.Daemon.WebListen, cfg.Daemon.WebGroup)
	if err != nil {
		return daemon.UnitSpec{}, nil, err
	}
	publicURL, err := daemonPublicURL("daemon.web_url", cfg.Daemon.WebURL)
	if err != nil {
		return daemon.UnitSpec{}, nil, err
	}
	// D11: "off" is a value of its own and is written as it stands; anything
	// else is the loopback address the previews are served on.
	previewListen := strings.TrimSpace(cfg.Daemon.PreviewListen)
	if previewListen != "" && previewListen != "off" {
		if previewListen, err = daemonLoopbackAddr("daemon.preview_listen", previewListen); err != nil {
			return daemon.UnitSpec{}, nil, err
		}
	}
	previewURL, err := daemonPreviewURL(cfg.Daemon.PreviewURL)
	if err != nil {
		return daemon.UnitSpec{}, nil, err
	}
	return daemon.UnitSpec{
		Webbridge:          webbridge,
		Marshal:            marshal,
		Paths:              daemon.PathsFor(home),
		Env:                daemonEnv(),
		IdleSessionTimeout: cfg.Daemon.IdleSessionTimeout,
		Egress:             cfg.Daemon.Egress,
		WebListen:          webListen,
		WebGroup:           strings.TrimSpace(cfg.Daemon.WebGroup),
		PublicURL:          publicURL,
		PreviewListen:      previewListen,
		PreviewURL:         previewURL,
	}, cfg.Daemon.Retired, nil
}

// daemonRetiredHint is what a retired [daemon] key is to be replaced with
// (D10, D6): a warning that names a key without saying what takes its place
// leaves the reader with nothing to do.
func daemonRetiredHint(key string) string {
	switch key {
	case "studio_addr":
		return "set daemon.web_listen (docs/one-daemon/design.md D10)"
	case "join_url", "join_name", "join_ca_file":
		return "a remote daemon is a host: marshal fleet add"
	}
	return "remove it"
}

// daemonRemoveOldTokenFiles deletes the credentials the retired listeners
// read, from the daemon directory the install just wrote its unit beside.
//
// A file that is already gone is not news, and one that cannot be removed is a
// line rather than a failed install: the unit is written and loaded by the
// time this runs, so the credential is all that is at stake.
func daemonRemoveOldTokenFiles(dir string, stderr io.Writer) {
	for _, name := range []string{"studio.token", "join.token"} {
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "marshal daemon install: could not remove %s (%v)\n", path, err)
		}
	}
}

// daemonWebListen validates daemon.web_listen against daemon.web_group and
// answers the --web-listen value the unit carries (D3): "unix", an absolute
// socket path, or a loopback host:port, and "" for no listener.
//
// A group needs a socket path of its own to share: the daemon directory is
// private, so a group could not reach web.sock there, and a loopback address
// has no group at all.
func daemonWebListen(listen, group string) (string, error) {
	value := strings.TrimSpace(listen)
	shared := strings.TrimSpace(group) != ""
	switch {
	case value == "":
		return "", nil
	case value == "unix":
		if shared {
			return "", fmt.Errorf("daemon.web_group %q needs daemon.web_listen to be an absolute socket path: the daemon directory is private, so the group could not reach web.sock there (docs/one-daemon/design.md D3)", strings.TrimSpace(group))
		}
		return "unix", nil
	case strings.HasPrefix(value, "/"):
		return value, nil
	}
	if _, err := daemonLoopbackAddr("daemon.web_listen", value); err != nil {
		return "", err
	}
	if shared {
		return "", fmt.Errorf("daemon.web_group %q needs daemon.web_listen to be a unix socket path: %s is a loopback address (docs/one-daemon/design.md D3)", strings.TrimSpace(group), value)
	}
	return value, nil
}

// daemonLoopbackAddr is the check one of key's listener addresses passes, and
// the address to serve on: "" answers "", as it does for a setting that is
// unset.
//
// The daemon runs host processes with the user's full authority and has no
// authentication but a bearer token in a 0600 file, so it listens on a unix
// socket or a loopback address only; binding it anywhere else would publish
// that authority (D3). A host name is not resolved here: only localhost and a
// loopback address literal are provably local without asking a resolver.
func daemonLoopbackAddr(key, setting string) (string, error) {
	addr := strings.TrimSpace(setting)
	if addr == "" {
		return "", nil
	}
	if err := bridge.CheckLoopback(key, addr); err != nil {
		return "", err
	}
	return addr, nil
}

// daemonPublicURL validates an http or https origin set under key (D10): the
// address a proxy publishes the Studio, or a preview, on.
func daemonPublicURL(key, setting string) (string, error) {
	base := strings.TrimSpace(setting)
	if base == "" {
		return "", nil
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("%s %q: %w", key, setting, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%s %q: scheme is not http or https", key, setting)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%s %q: no host", key, setting)
	}
	return base, nil
}

// daemonPreviewURL validates daemon.preview_url (D10): an http or https
// origin, or a template with {token} once in its first host label.
//
// A template is written into the unit as it stands, and the daemon fills the
// token in per preview. Its {token} is replaced with a plain one before the
// origin check, so a template cannot smuggle a path, query or fragment past it.
func daemonPreviewURL(setting string) (string, error) {
	base := strings.TrimSpace(setting)
	if !strings.Contains(base, "{token}") {
		return daemonPublicURL("daemon.preview_url", base)
	}
	// The host is what sits after the scheme and before its port or path, as
	// the bridge reads it.
	host := base
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+len("://"):]
	}
	if i := strings.IndexAny(host, "/:"); i >= 0 {
		host = host[:i]
	}
	label := host
	if i := strings.IndexByte(label, '.'); i >= 0 {
		label = label[:i]
	}
	if strings.Count(base, "{token}") != 1 || !strings.Contains(label, "{token}") {
		return "", fmt.Errorf("daemon.preview_url: {token} goes once, in the first label of the host, as in https://p{token}.preview.example.com")
	}
	if _, err := daemonPublicURL("daemon.preview_url", strings.Replace(base, "{token}", "0", 1)); err != nil {
		return "", err
	}
	return base, nil
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
// status
// ---------------------------------------------------------------------------

// runDaemonStatus prints decision 12's lines, or writes the JSON document
// the tray app reads.
//
// Text mode prints the service state, the linger line where the platform has
// one, the socket and whether it answered, and — for an answering daemon —
// its version against this marshal's, its start time, its refresh helper, its
// web listener, its projects and each project child. It
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
	fmt.Fprintln(stdout, daemonWebLine(info))
	fmt.Fprintln(stdout, daemonStudioLine(info))
	for _, host := range info.Hosts {
		fmt.Fprintln(stdout, daemonHostLine(host))
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

// daemonWebLine renders the web listener for `marshal daemon status` (D10):
// where the daemon serves the page and its API, why it serves none, or that it
// serves none at all.
//
// An address rather than a socket is worth naming as what it is: every account
// on the machine can reach a loopback port, so a listener there is not private
// to the user who installed it (D13).
func daemonWebLine(info daemon.Info) string {
	if info.WebError != "" {
		return fmt.Sprintf("web: not listening (%s)", info.WebError)
	}
	if info.WebListen == "" {
		return "web: off"
	}
	if strings.HasPrefix(info.WebListen, "unix:") {
		return "web: " + info.WebListen
	}
	return fmt.Sprintf("web: %s (loopback: every account on this machine can reach it)", info.WebListen)
}

// daemonStudioLine renders the Studio's directory for `marshal daemon status`
// (one-daemon D12): where the daemon serves the Studio from, or — when it
// found no files — that there are none, with the two ways to get them, because
// a daemon serving no Studio has nothing to point at and the reader needs to
// know which of the two situations they are in.
func daemonStudioLine(info daemon.Info) string {
	if info.StudioDir == "" {
		return "studio: no files found (reinstall Marshal, or build them: see AGENTS.md)"
	}
	return "studio: " + info.StudioDir
}

// daemonHostLine renders one host's tunnel for `marshal daemon status`
// (one-daemon D7): where the tunnel goes, its state, and — when it is down —
// its last error, which is why the reader consulted this line, as the web line
// does for a listener that bound nothing. A live host that serves previews
// says where they are, because that loopback port is the only way to reach
// them; a host with no port, and one that is down whatever it last reported,
// say nothing about previews.
func daemonHostLine(h daemon.HostInfo) string {
	line := fmt.Sprintf("host %s (%s): %s", h.Name, h.SSH, h.State)
	if h.State == contract.HostDown {
		if lastErr := strings.TrimSpace(h.LastError); lastErr != "" {
			line += " (" + lastErr + ")"
		}
	}
	if h.State == contract.HostLive && h.PreviewPort > 0 {
		line += fmt.Sprintf(", previews on localhost:%d", h.PreviewPort)
	}
	return line
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
