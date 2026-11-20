package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"

	"marshal/internal/acp"
	"marshal/internal/app/config"
	"marshal/internal/buildinfo"
	"marshal/internal/daemon"
	"marshal/internal/fleetprofile"
	"marshal/internal/launch"
	"marshal/internal/llm/provider"
	"marshal/internal/procenv"
	"marshal/internal/repo"
	"marshal/internal/trust"
	"marshal/web/bridge/contract"
)

var appRunner = launch.Run
var attachRunner = launch.Attach
var fleetRunner = launch.Fleet

// hostOption is launch.WithHost, indirected so a test can read the name a
// runner was handed: launch's options are unexported.
var hostOption = launch.WithHost

// fleetHostWait is how long `marshal fleet add` and `remove` wait for the local
// daemon's report to catch up with the file. A test sets it to zero.
var fleetHostWait = 5 * time.Second

// fleetStdoutIsTerminal reports whether a writer is a terminal. `marshal daemon
// token` refuses one, so a token never reaches scrollback (F1 decision 3). A
// test replaces it, because a test binary has no terminal to point at.
var fleetStdoutIsTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

// notifyOption is launch.WithNotify, indirected so a test can read the flag a
// runner was handed: launch's options are unexported.
var notifyOption = launch.WithNotify

var acpRunner = acp.Run
var acpListener = acp.ListenAndServe
var acpListenerWithViewers = acp.ListenAndServeWithViewers
var acpViewersRunner = acp.RunWithViewers
var historyRunner = runHistory
var calibrateRunner = runCalibrateTokens
var pluginRunner = runPlugin
var snapshotsRunner = runSnapshots
var daemonRunner = runDaemon

func main() {
	// Drop libmalloc's debugging knobs before anything spawns a child.
	// Inherited, they make every process marshal runs print a diagnostic
	// line to stderr — one per tool call — which corrupts the TUI. See
	// internal/procenv.
	procenv.ScrubMallocDebugEnv()
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// Parse top-level flags before subcommand dispatch so unknown flags are
	// rejected instead of silently ignored.
	fs := flag.NewFlagSet("marshal", flag.ContinueOnError)
	fs.SetOutput(stderr)
	trustFlag := fs.Bool("trust", false, "trust the current project permanently")
	versionFlag := fs.Bool("version", false, "print version information and exit")
	embeddedFlag := fs.Bool("embedded", false, "run the session in this process, without the daemon")
	if err := fs.Parse(args); err != nil {
		return err
	}
	args = fs.Args()

	// Decision 2: this build's version is computed once, here, and everything
	// that names it uses this one value. buildinfo.Version folds in the VCS
	// stamps Go embeds, so a source build names the commit it came from
	// wherever it names itself.
	v := buildinfo.Version(version)

	// Name this build in the provider HTTP client's User-Agent. OpenCode Zen
	// asks clients to send their own name and version rather than a generic
	// HTTP-library default.
	provider.UserAgent = "marshal/" + v

	// Report the agent's own version in the initialize handshake so a bridge
	// can detect skew on derived/custom images — and set it in every mode, so
	// the TUI's own version is this build's rather than empty.
	acp.AgentVersion = v

	if *versionFlag {
		fmt.Fprintln(stdout, versionString())
		return nil
	}

	if *trustFlag {
		if err := recordPermanentTrust(); err != nil {
			return fmt.Errorf("--trust: %w", err)
		}
	}

	if len(args) > 0 && args[0] == "history" {
		return historyRunner(ctx, args[1:], stdout)
	}
	if len(args) > 0 && args[0] == "calibrate-tokens" {
		return calibrateRunner(args[1:], stdout)
	}
	if len(args) > 0 && args[0] == "acp" {
		spec, err := acpListenSpec(args[1:])
		if err != nil {
			return err
		}
		viewers, err := acpViewersSpec(args[1:])
		if err != nil {
			return err
		}
		// Decision 3: the bridge names a container's viewer socket in the
		// environment rather than a flag, so a marshal that predates this
		// still starts. Read it once, here, and remove it again before any
		// runner is called: nothing the agent spawns may inherit it.
		envViewers := os.Getenv(acp.ViewersEnv)
		if envViewers != "" {
			os.Unsetenv(acp.ViewersEnv)
		}
		if spec == "" {
			// --viewers alone ignores the variable: stdio, with or without
			// a viewer socket of its own.
			if viewers == "" {
				return acpRunner(ctx, stdin, stdout, stderr)
			}
			path, err := acpUnixViewerPath(viewers, "--viewers")
			if err != nil {
				return err
			}
			return acpViewersRunner(ctx, stdin, stdout, stderr, path)
		}
		network, addr, err := acp.ParseListenAddr(spec)
		if err != nil {
			return err
		}
		// An explicit --viewers wins over the variable; the variable is only
		// consulted when --listen is given and --viewers is not.
		viewerSpec, viewerSource := viewers, "--viewers"
		if viewerSpec == "" {
			viewerSpec, viewerSource = envViewers, acp.ViewersEnv
		}
		if viewerSpec == "" {
			return acpListener(ctx, network, addr, stderr)
		}
		path, err := acpUnixViewerPath(viewerSpec, viewerSource)
		if err != nil {
			return err
		}
		return acpListenerWithViewers(ctx, network, addr, path, stderr)
	}
	if len(args) > 0 && args[0] == "plugin" {
		return pluginRunner(ctx, args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "snapshots" {
		return snapshotsRunner(ctx, args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "daemon" {
		return daemonRunner(ctx, args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "attach" {
		return runAttach(ctx, args[1:], stdout, stderr, *embeddedFlag)
	}
	if len(args) > 0 && args[0] == "fleet" {
		return runFleet(ctx, args[1:], stdin, stdout, stderr, *embeddedFlag)
	}
	if len(args) > 0 && args[0] == "studio" {
		return runStudio(ctx, args[1:], stdout, stderr)
	}
	if len(args) > 0 {
		return fmt.Errorf("unknown argument %q", args[0])
	}
	// Daemon mode is the default for a plain launch (P4 D5): when a local
	// daemon answers, the session runs there and outlives this program.
	// --embedded keeps today's mode, and so does a missing home, which leaves
	// no socket to look for.
	if *embeddedFlag {
		if home, err := os.UserHomeDir(); err == nil {
			return appRunner(ctx, stdout, notifyOption(localNotify(home, stderr)))
		}
		return appRunner(ctx, stdout)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return appRunner(ctx, stdout)
	}
	return appRunner(ctx, stdout, launch.WithDaemon(config.DaemonSocketPath(home)), notifyOption(localNotify(home, stderr)))
}

// runAttach dispatches `marshal attach [session-id]`: it reattaches the TUI to
// a daemon session, the project's most recently active one when no ID is given.
func runAttach(ctx context.Context, args []string, stdout, stderr io.Writer, embedded bool) error {
	// --embedded is refused wherever it appears in the invocation, and says
	// why: attach has no embedded mode, so "unexpected flag" would misdescribe
	// the mistake.
	if embedded {
		return fmt.Errorf("marshal attach: --embedded cannot be combined with attach")
	}
	for _, arg := range args {
		if arg == "--embedded" || strings.HasPrefix(arg, "--embedded=") {
			return fmt.Errorf("marshal attach: --embedded cannot be combined with attach")
		}
	}
	if len(args) > 1 {
		return fmt.Errorf("marshal attach: at most one session ID")
	}
	sessionID := ""
	if len(args) == 1 {
		if strings.HasPrefix(args[0], "-") {
			return fmt.Errorf("marshal attach: unexpected flag %q", args[0])
		}
		sessionID = args[0]
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("marshal attach: find home directory: %w", err)
	}
	return attachRunner(ctx, stdout, config.DaemonSocketPath(home), sessionID, notifyOption(localNotify(home, stderr)))
}

// runFleet dispatches `marshal fleet`: it runs the fleet screen on the local
// daemon (P5 F4), on the tunnel to the host `--host NAME` names (D8), or
// writes and deletes a host's table in fleet.toml.
func runFleet(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, embedded bool) error {
	// --embedded is refused wherever it appears in the invocation, and says
	// why: the fleet screen reads a daemon, so there is nothing to embed.
	if embedded {
		return fmt.Errorf("marshal fleet: --embedded cannot be combined with fleet: the fleet screen needs the daemon")
	}
	for _, arg := range args {
		if arg == "--embedded" || strings.HasPrefix(arg, "--embedded=") {
			return fmt.Errorf("marshal fleet: --embedded cannot be combined with fleet: the fleet screen needs the daemon")
		}
	}

	host := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--server" || strings.HasPrefix(args[i], "--server="):
			// D8: a remote daemon is a host, reached through the local
			// daemon's tunnel, and the token a bridge wanted is gone with the
			// bridge.
			return fmt.Errorf("marshal fleet: --server is retired; a remote daemon is a host: marshal fleet --host NAME (docs/one-daemon/design.md D8)")
		case args[i] == "--host":
			// An empty value is refused here as it is after `--host=`: an
			// empty name is indistinguishable from no flag, and would quietly
			// open the local daemon's fleet instead of the one named.
			if i+1 >= len(args) || args[i+1] == "" {
				return fmt.Errorf("marshal fleet: --host requires a host name")
			}
			if host != "" {
				return fmt.Errorf("marshal fleet: --host given more than once")
			}
			host = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--host="):
			if host != "" {
				return fmt.Errorf("marshal fleet: --host given more than once")
			}
			host = strings.TrimPrefix(args[i], "--host=")
			if host == "" {
				return fmt.Errorf("marshal fleet: --host requires a host name")
			}
		case len(rest) > 0 && rest[0] == "add" &&
			(args[i] == "--marshal" || strings.HasPrefix(args[i], "--marshal=") || args[i] == "--no-notify"):
			// add is the one subcommand with flags of its own, and they follow
			// the name, so they are handed to runFleetAdd rather than read here.
			rest = append(rest, args[i])
			if args[i] == "--marshal" && i+1 < len(args) {
				rest = append(rest, args[i+1])
				i++
			}
		case strings.HasPrefix(args[i], "-"):
			return fmt.Errorf("marshal fleet: unexpected flag %q", args[i])
		default:
			rest = append(rest, args[i])
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("marshal fleet: find home directory: %w", err)
	}

	// The subcommands name a host and act on its table; anything else is
	// refused.
	if len(rest) > 0 {
		switch rest[0] {
		case "login", "token":
			// D8: a host is reached over SSH, so there is no token to store or
			// print.
			return fmt.Errorf("marshal fleet %s: retired; a host is reached over SSH and needs no token", rest[0])
		case "servers":
			return fmt.Errorf("marshal fleet servers: retired; marshal daemon status lists the hosts")
		case "add":
			if host != "" {
				return fmt.Errorf("marshal fleet: --host cannot be combined with add: add writes the file")
			}
			return runFleetAdd(ctx, rest[1:], stderr, home)
		case "remove":
			if host != "" {
				return fmt.Errorf("marshal fleet: --host cannot be combined with remove: remove names its host")
			}
			return runFleetRemove(ctx, rest[1:], stderr, home)
		}
		return fmt.Errorf("marshal fleet: unexpected argument %q", rest[0])
	}

	// Decision 9: the file is read once per invocation, and a file that cannot
	// be loaded stops the fleet screen rather than being ignored.
	file, err := fleetprofile.Load(fleetprofile.Path(home))
	if err != nil {
		return fmt.Errorf("marshal fleet: %w", err)
	}
	if host == "" {
		return fleetRunner(ctx, stdout, config.DaemonSocketPath(home), notifyOption(file.Local.Notify))
	}
	h, err := file.LookupHost(host)
	if err != nil {
		return fmt.Errorf("marshal fleet: %w", err)
	}
	tunnel, err := hostTunnel(ctx, home, host)
	if err != nil {
		return err
	}
	return fleetRunner(ctx, stdout, tunnel.Socket, notifyOption(h.Notify), hostOption(host))
}

// hostTunnel reads the local daemon's report of the host name (D7): the host
// is reached through the tunnel that daemon keeps, so its socket is the one to
// run the screen on.
func hostTunnel(ctx context.Context, home, name string) (daemon.HostInfo, error) {
	socket := config.DaemonSocketPath(home)
	infoCtx, cancel := context.WithTimeout(ctx, daemonStatusTimeout)
	defer cancel()
	info, err := daemonClientFor(socket).Info(infoCtx)
	if err != nil {
		return daemon.HostInfo{}, fmt.Errorf("marshal fleet: the local daemon is not answering on %s, and it keeps the tunnel to %s; marshal daemon start", socket, name)
	}
	var found daemon.HostInfo
	for _, h := range info.Hosts {
		if h.Name == name {
			found = h
			break
		}
	}
	if found.Name == "" {
		return daemon.HostInfo{}, fmt.Errorf("marshal fleet: the daemon reports no tunnel to %s; wait a few seconds after adding it, or run marshal daemon install if the daemon predates hosts", name)
	}
	if found.State != contract.HostLive {
		if found.LastError != "" {
			return daemon.HostInfo{}, fmt.Errorf("marshal fleet: %s is %s: %s", name, found.State, found.LastError)
		}
		return daemon.HostInfo{}, fmt.Errorf("marshal fleet: %s is %s", name, found.State)
	}
	return found, nil
}

// waitForHost waits up to fleetHostWait for the local daemon's report to list
// name (present) or not list it, and returns silently either way: add and
// remove have already written the file, and the tunnel helper catches up on
// its own schedule.
func waitForHost(ctx context.Context, home, name string, present bool) {
	if fleetHostWait <= 0 {
		return
	}
	socket := config.DaemonSocketPath(home)
	deadline := time.Now().Add(fleetHostWait)
	for {
		infoCtx, cancel := context.WithTimeout(ctx, daemonStatusTimeout)
		info, err := daemonClientFor(socket).Info(infoCtx)
		cancel()
		if err != nil {
			// Nothing answered, so no report is coming: there is nothing to
			// wait for.
			return
		}
		listed := false
		for _, h := range info.Hosts {
			if h.Name == name {
				listed = true
				break
			}
		}
		if listed == present {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// runFleetAdd is `marshal fleet add NAME DEST [--marshal PATH] [--no-notify]`
// (D6, D8): it writes the host's table to the fleet file, which keeps its
// comments, and waits for the local daemon's report to pick it up.
func runFleetAdd(ctx context.Context, args []string, stderr io.Writer, home string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	var marshalPath string
	notify := true
	// A path may follow --marshal, so the flags are read by position and the
	// rest are the name and the ssh destination.
	positionals := make([]string, 0, 2)
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--marshal":
			if i+1 >= len(args) || args[i+1] == "" ||
				strings.HasPrefix(args[i+1], "-") {
				return fmt.Errorf("marshal fleet add: --marshal requires a path")
			}
			if marshalPath != "" {
				return fmt.Errorf("marshal fleet add: --marshal given more than once")
			}
			marshalPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--marshal="):
			if marshalPath != "" {
				return fmt.Errorf("marshal fleet add: --marshal given more than once")
			}
			marshalPath = strings.TrimPrefix(args[i], "--marshal=")
			if marshalPath == "" || strings.HasPrefix(marshalPath, "-") {
				return fmt.Errorf("marshal fleet add: --marshal requires a path")
			}
		case args[i] == "--no-notify":
			notify = false
		case strings.HasPrefix(args[i], "-"):
			return fmt.Errorf("marshal fleet add: unexpected flag %q", args[i])
		default:
			positionals = append(positionals, args[i])
		}
	}
	switch {
	case len(positionals) < 2:
		return fmt.Errorf("marshal fleet add: a host name and an ssh destination are required")
	case len(positionals) > 2:
		return fmt.Errorf("marshal fleet add: unexpected argument %q", positionals[2])
	}
	name, dest := positionals[0], positionals[1]

	// Notify is a bool that reads as on when the file omits it, so the absent
	// flag writes nothing and --no-notify writes notify = false.
	h := fleetprofile.Host{Name: name, SSH: dest, Marshal: marshalPath, Notify: notify}
	if err := fleetprofile.AddHost(fleetprofile.Path(home), h); err != nil {
		return fmt.Errorf("marshal fleet add: %w", err)
	}
	waitForHost(ctx, home, name, true)
	fmt.Fprintf(stderr, "Added %s. marshal fleet --host %s opens its fleet once its tunnel is live; marshal daemon status shows it.\n", name, name)
	return nil
}

// runFleetRemove is `marshal fleet remove NAME`: it deletes the host's table
// from the fleet file, keeping the file's comments, and waits for the local
// daemon's report to drop it.
func runFleetRemove(ctx context.Context, args []string, stderr io.Writer, home string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch {
	case len(args) == 0:
		return fmt.Errorf("marshal fleet remove: a host name is required")
	case len(args) > 1:
		return fmt.Errorf("marshal fleet remove: at most one host name")
	case strings.HasPrefix(args[0], "-"):
		return fmt.Errorf("marshal fleet remove: unexpected flag %q", args[0])
	}
	name := args[0]

	if err := fleetprofile.RemoveHost(fleetprofile.Path(home), name); err != nil {
		return fmt.Errorf("marshal fleet remove: %w", err)
	}
	waitForHost(ctx, home, name, false)
	fmt.Fprintf(stderr, "Removed %s.\n", name)
	return nil
}

// localNotify is local.notify for plain marshal and marshal attach, printing
// why on stderr when the file cannot be loaded (decision 9). A fleet file must
// not stop the session screen, so a file that cannot be loaded keeps
// notifications on.
func localNotify(home string, stderr io.Writer) bool {
	file, err := fleetprofile.Load(fleetprofile.Path(home))
	if err != nil {
		fmt.Fprintf(stderr, "marshal: %v; notifications stay on\n", err)
		return true
	}
	return file.Local.Notify
}

// acpListenSpec extracts the value of --listen (or --listen=VALUE) from
// the acp subcommand's arguments. An empty return means stdio mode.
func acpListenSpec(args []string) (string, error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--listen":
			if i+1 >= len(args) {
				return "", fmt.Errorf("marshal acp: --listen requires an address")
			}
			return args[i+1], nil
		case strings.HasPrefix(args[i], "--listen="):
			return strings.TrimPrefix(args[i], "--listen="), nil
		}
	}
	return "", nil
}

// acpViewersSpec extracts --viewers (or --viewers=VALUE) from the acp
// subcommand's arguments. An empty return means no viewer socket. A repeated
// flag is refused rather than silently using the first: the viewer socket is a
// path the operator means to bind, and a second one is a mistake worth naming.
func acpViewersSpec(args []string) (string, error) {
	var spec string
	set := func(value string) error {
		if value == "" {
			return fmt.Errorf("marshal acp: --viewers requires a unix:// address")
		}
		if spec != "" {
			return fmt.Errorf("marshal acp: --viewers given more than once")
		}
		spec = value
		return nil
	}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--viewers":
			if i+1 >= len(args) {
				return "", fmt.Errorf("marshal acp: --viewers requires a unix:// address")
			}
			if err := set(args[i+1]); err != nil {
				return "", err
			}
			i++
		case strings.HasPrefix(args[i], "--viewers="):
			if err := set(strings.TrimPrefix(args[i], "--viewers=")); err != nil {
				return "", err
			}
		}
	}
	return spec, nil
}

// acpUnixViewerPath parses a viewer address and requires a unix socket: a
// viewer socket is a path on the host whichever way the address arrived, so a
// tcp address is refused whether it came from --viewers or from the
// environment. source names where it came from in the refusal.
func acpUnixViewerPath(spec, source string) (string, error) {
	network, path, err := acp.ParseListenAddr(spec)
	if err != nil {
		return "", err
	}
	if network != "unix" {
		return "", fmt.Errorf("marshal acp: %s takes a unix socket, got %s://", source, network)
	}
	return path, nil
}

// recordPermanentTrust writes a permanent trust record for the current
// working directory so the next normal startup skips the trust prompt.
func recordPermanentTrust() error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find working directory: %w", err)
	}
	return recordPermanentTrustIn(wd)
}

// recordPermanentTrustIn anchors the trust record the same way a session
// is anchored: at the repository root containing dir (repo.Root), then
// canonicalized. Keying on the launch subdirectory would write a record
// the next root-anchored launch never matches, and hashing the
// subdirectory's (absent) config would store an empty hash that forces a
// re-prompt even with the right key.
func recordPermanentTrustIn(dir string) error {
	root := repo.Root(dir)
	abs := trust.Canonicalize(root)
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	store := trust.NewStore(config.DataDir(home))
	hash, err := trust.ConfigHashFor(root)
	if err != nil {
		return fmt.Errorf("hash project config: %w", err)
	}
	return store.SetTrust(abs, true, hash)
}
