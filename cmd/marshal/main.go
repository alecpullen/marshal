package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"

	"marshal/internal/acp"
	"marshal/internal/app/config"
	"marshal/internal/credentials"
	"marshal/internal/fleetprofile"
	"marshal/internal/launch"
	"marshal/internal/llm/provider"
	"marshal/internal/procenv"
	"marshal/internal/repo"
	"marshal/internal/trust"
)

var appRunner = launch.Run
var attachRunner = launch.Attach
var fleetRunner = launch.Fleet
var fleetServerRunner = launch.ServerFleet

// fleetLoginStore opens the keychain `marshal fleet login` writes to; tests
// replace it.
var fleetLoginStore fleetprofile.OpenStore = func() (credentials.Store, error) {
	return credentials.New("marshal")
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

	// Stamp the release version into the provider HTTP client's
	// self-identifying User-Agent. OpenCode Zen asks clients to send their
	// own name and version rather than a generic HTTP-library default; a
	// plain `go build` leaves version empty and keeps the marshal/dev
	// default. acp.AgentVersion is stamped the same way below.
	if version != "" {
		provider.UserAgent = "marshal/" + version
	}

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
		// Report the agent's own version in the initialize handshake so a
		// bridge can detect skew on derived/custom images.
		acp.AgentVersion = version
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
// daemon (P5 F4), on the server profile `--server NAME` names (P5 F7), or
// stores a token for `login NAME` (decision 10).
func runFleet(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, embedded bool) error {
	// --embedded is refused wherever it appears in the invocation, and says
	// why: the fleet screen reads a daemon, so there is nothing to embed, and
	// login writes the keychain.
	if embedded {
		return fmt.Errorf("marshal fleet: --embedded cannot be combined with fleet: the fleet screen needs the daemon")
	}
	for _, arg := range args {
		if arg == "--embedded" || strings.HasPrefix(arg, "--embedded=") {
			return fmt.Errorf("marshal fleet: --embedded cannot be combined with fleet: the fleet screen needs the daemon")
		}
	}

	server := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--server":
			// An empty value is refused here as it is after `--server=`: an
			// empty name is indistinguishable from no flag, and would quietly
			// open the local daemon's fleet instead of the one named.
			if i+1 >= len(args) || args[i+1] == "" {
				return fmt.Errorf("marshal fleet: --server requires a server name")
			}
			if server != "" {
				return fmt.Errorf("marshal fleet: --server given more than once")
			}
			server = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--server="):
			if server != "" {
				return fmt.Errorf("marshal fleet: --server given more than once")
			}
			server = strings.TrimPrefix(args[i], "--server=")
			if server == "" {
				return fmt.Errorf("marshal fleet: --server requires a server name")
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

	// `login NAME` is the one positional form; anything else is refused.
	if len(rest) > 0 {
		if rest[0] != "login" {
			return fmt.Errorf("marshal fleet: unexpected argument %q", rest[0])
		}
		if server != "" {
			return fmt.Errorf("marshal fleet: --server cannot be combined with login: login names its server")
		}
		return runFleetLogin(ctx, rest[1:], stdin, stderr, home)
	}

	// Decision 9: the file is read once per invocation, and a file that cannot
	// be loaded stops the fleet screen rather than being ignored.
	file, err := fleetprofile.Load(fleetprofile.Path(home))
	if err != nil {
		return fmt.Errorf("marshal fleet: %w", err)
	}
	if server == "" {
		return fleetRunner(ctx, stdout, config.DaemonSocketPath(home), notifyOption(file.Local.Notify))
	}
	srv, err := file.Lookup(server)
	if err != nil {
		return fmt.Errorf("marshal fleet: %w", err)
	}
	return fleetServerRunner(ctx, stdout, srv, notifyOption(srv.Notify))
}

// runFleetLogin is `marshal fleet login NAME` (decision 10): it stores a token
// for a profile the file names, under the keychain key decision 4 reads.
func runFleetLogin(ctx context.Context, args []string, stdin io.Reader, stderr io.Writer, home string) error {
	switch {
	case len(args) == 0:
		return fmt.Errorf("marshal fleet login: a server name is required")
	case len(args) > 1:
		return fmt.Errorf("marshal fleet login: at most one server name")
	case strings.HasPrefix(args[0], "-"):
		return fmt.Errorf("marshal fleet login: unexpected flag %q", args[0])
	}
	name := args[0]

	file, err := fleetprofile.Load(fleetprofile.Path(home))
	if err != nil {
		return fmt.Errorf("marshal fleet login: %w", err)
	}
	srv, err := file.Lookup(name)
	if err != nil {
		return fmt.Errorf("marshal fleet login: %w", err)
	}
	// A stored token would never be read for a profile whose token comes from
	// token_cmd, so naming it is a mistake worth reporting.
	if srv.TokenCmd != "" {
		return fmt.Errorf("marshal fleet login: %s reads its token from token_cmd, so a stored token would never be read", name)
	}

	token, err := readFleetToken(name, stdin, stderr)
	if err != nil {
		return err
	}
	store, err := fleetLoginStore()
	if err != nil {
		return fmt.Errorf("marshal fleet login: open the keychain: %w", err)
	}
	if err := store.Set(fleetprofile.KeychainKey(name), []byte(token)); err != nil {
		return fmt.Errorf("marshal fleet login: store the token for %s: %w", name, err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	fmt.Fprintf(stderr, "Stored the token for %s.\n", name)
	return nil
}

// readFleetToken reads the token for name: without echo and behind a prompt
// on a terminal, else one line from stdin, so a password manager can pipe it
// in (decision 10).
func readFleetToken(name string, stdin io.Reader, stderr io.Writer) (string, error) {
	var raw []byte
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(f.Fd()) {
		fmt.Fprintf(stderr, "Token for %s: ", name)
		b, err := term.ReadPassword(f.Fd())
		fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("marshal fleet login: read the token: %w", err)
		}
		raw = b
	} else {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("marshal fleet login: read the token: %w", err)
		}
		raw = []byte(line)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("marshal fleet login: the token for %s is empty", name)
	}
	return token, nil
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
