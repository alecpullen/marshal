package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"marshal/internal/acp"
	"marshal/internal/app/config"
	"marshal/internal/launch"
	"marshal/internal/llm/provider"
	"marshal/internal/procenv"
	"marshal/internal/repo"
	"marshal/internal/trust"
)

var appRunner = launch.Run
var attachRunner = launch.Attach
var acpRunner = acp.Run
var acpListener = acp.ListenAndServe
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
		if spec != "" && viewers != "" {
			return fmt.Errorf("marshal acp: --viewers cannot be combined with --listen")
		}
		if viewers != "" {
			network, path, err := acp.ParseListenAddr(viewers)
			if err != nil {
				return err
			}
			if network != "unix" {
				return fmt.Errorf("marshal acp: --viewers takes a unix socket, got %s://", network)
			}
			return acpViewersRunner(ctx, stdin, stdout, stderr, path)
		}
		if spec == "" {
			return acpRunner(ctx, stdin, stdout, stderr)
		}
		network, addr, err := acp.ParseListenAddr(spec)
		if err != nil {
			return err
		}
		return acpListener(ctx, network, addr, stderr)
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
		return runAttach(ctx, args[1:], stdout, *embeddedFlag)
	}
	if len(args) > 0 {
		return fmt.Errorf("unknown argument %q", args[0])
	}
	// Daemon mode is the default for a plain launch (P4 D5): when a local
	// daemon answers and the project needs no trust question, the session runs
	// there and outlives this program. --embedded keeps today's mode, and so
	// does a missing home, which leaves no socket to look for.
	if *embeddedFlag {
		return appRunner(ctx, stdout)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return appRunner(ctx, stdout)
	}
	return appRunner(ctx, stdout, launch.WithDaemon(config.DaemonSocketPath(home)))
}

// runAttach dispatches `marshal attach [session-id]`: it reattaches the TUI to
// a daemon session, the project's most recently active one when no ID is given.
func runAttach(ctx context.Context, args []string, stdout io.Writer, embedded bool) error {
	if embedded {
		return fmt.Errorf("marshal attach: --embedded cannot be combined with attach")
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
	return attachRunner(ctx, stdout, config.DaemonSocketPath(home), sessionID)
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
