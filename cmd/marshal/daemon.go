package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/daemon"
	"marshal/internal/llm/provider"
)

// daemonClient is the part of daemon.Client that `status` needs, so a test
// answers with a scripted daemon instead of opening a socket.
type daemonClient interface {
	Info(ctx context.Context) (daemon.Info, error)
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

// daemonReadyPoll is the gap between readiness probes, and daemonStartTimeout
// the longest wait for a freshly loaded service to answer. It is a var so a
// test can shorten it.
const daemonReadyPoll = 100 * time.Millisecond

var daemonStartTimeout = 5 * time.Second

// runDaemon implements `marshal daemon <install|start|stop|status|uninstall>`,
// and dispatches `refresh` as well.
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
		return errors.New("marshal daemon: a subcommand is required (install|start|stop|status|uninstall)")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "install":
		return runDaemonInstall(ctx, rest, stdout, stderr)
	case "start", "stop", "uninstall":
		return runDaemonService(ctx, sub, rest, stdout, stderr)
	case "status":
		return runDaemonStatus(ctx, rest, stdout, stderr)
	case "refresh":
		return runDaemonRefresh(ctx, rest, stderr)
	default:
		return fmt.Errorf("marshal daemon: unknown subcommand %q (install|start|stop|status|uninstall)", sub)
	}
}

// ---------------------------------------------------------------------------
// refresh
// ---------------------------------------------------------------------------

// oauthRefreshRunner is the helper's body; tests replace it.
var oauthRefreshRunner = app.RunOAuthRefresh

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
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
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
// Usage: marshal daemon install [--webbridge PATH]
func runDaemonInstall(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	webbridge := fs.String("webbridge", "", "the webbridge binary to run (default: beside marshal, else on PATH)")
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
	daemonReadyNote(ctx, spec.Paths, stdout)
	return nil
}

// daemonUnitSpec is decision 10's unit: the webbridge, the resolved marshal,
// the paths internal/daemon resolves, the location environment, and the idle
// session timeout the user config gives.
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
	return daemon.UnitSpec{
		Webbridge:          webbridge,
		Marshal:            marshal,
		Paths:              daemon.PathsFor(home),
		Env:                daemonEnv(),
		IdleSessionTimeout: cfg.Daemon.IdleSessionTimeout,
	}, nil
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

// runDaemonStatus prints decision 12's three lines: the service state, the
// socket and whether it answered, and — for an answering daemon — its version
// against this marshal's, its start time, and its projects.
//
// It exits non-zero when the socket does not answer, but only after printing
// what it found: a service that is loaded and silent is exactly the case the
// user is running `status` for.
func runDaemonStatus(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon status", flag.ContinueOnError)
	fs.SetOutput(stderr)
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
	fmt.Fprintln(stdout, daemonServiceLine(state))

	infoCtx, cancel := context.WithTimeout(ctx, daemonStatusTimeout)
	defer cancel()
	info, err := daemonClientFor(paths.Socket).Info(infoCtx)
	if err != nil {
		fmt.Fprintf(stdout, "socket: %s (not answering)\n", paths.Socket)
		return fmt.Errorf("the daemon is not answering on %s", paths.Socket)
	}
	fmt.Fprintf(stdout, "socket: %s (answering)\n", paths.Socket)
	marshalVersion := daemonVersion()
	fmt.Fprintln(stdout, daemonInfoLine(info, marshalVersion))
	fmt.Fprintln(stdout, daemonRefreshLine(info))
	for _, child := range info.Children {
		fmt.Fprintln(stdout, daemonChildLine(child, marshalVersion))
		if warning := strings.TrimSpace(child.EnvWarning); warning != "" {
			fmt.Fprintf(stdout, "  environment: %s\n", warning)
		}
	}
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
func daemonRefreshLine(info daemon.Info) string {
	if info.Refresh == nil {
		return "refresh: off"
	}
	line := fmt.Sprintf("refresh: running (pid %d)", info.Refresh.PID)
	if info.Refresh.Restarts != 0 {
		line += fmt.Sprintf(", restarted %d %s", info.Refresh.Restarts, restartCount(info.Refresh.Restarts))
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
	return line
}

// sessionCount is the noun for n sessions.
func sessionCount(n int) string {
	if n == 1 {
		return "session"
	}
	return "sessions"
}

// daemonVersion is this marshal's version, with the same `dev` fallback
// versionString uses for an unstamped build.
func daemonVersion() string {
	if version == "" {
		return "dev"
	}
	return version
}

// daemonHome is where PathsFor and NewManager anchor.
func daemonHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return home, nil
}
