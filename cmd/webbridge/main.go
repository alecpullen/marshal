// Command webbridge exposes Marshal sessions over HTTP and SSE: it
// supervises a `marshal acp` child, brokers its permission/question
// requests, and serves the REST API, event stream, and embedded SPA.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	appconfig "marshal/internal/app/config"
	"marshal/internal/buildinfo"
	"marshal/internal/procenv"
	"marshal/web/bridge"
)

// version is stamped at release time via -ldflags -X (see .goreleaser.yaml).
// A plain `go build` leaves it empty and versionString reports "dev".
var version = ""

// versionString renders the one-line version banner printed by --version.
func versionString() string {
	return fmt.Sprintf("webbridge %s", daemonVersion())
}

// daemonVersion is the bare version: the stamped release version, else the
// source build's dev+<commit> (decision 7). The daemon reports it
// through GET /api/daemon, which wants the version itself rather than the
// banner around it, and the fleet carries it as its build version.
func daemonVersion() string {
	return buildinfo.Version(version)
}

// defaultStateDir is the in-container mount point for the state volume.
// When running as a host process (no container runtime), the state dir
// defaults to beside the workspace file instead.
const defaultStateDir = "/state"

// stringList collects repeatable project flags.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// config holds the resolved flag/env settings.
type config struct {
	// socket is the daemon's unix listener.
	socket string
	// webListen is --web-listen as given: none when empty, "unix", an
	// absolute socket path, or a loopback host:port (D3).
	webListen string
	// webGroup shares a unix --web-listen with this group (0660).
	webGroup string
	// previewAddr is where previews are served, on an origin of their own.
	// Empty means a free port on 127.0.0.1; "off" disables.
	previewAddr string
	// previewURL is the public origin preview links name, for a proxy that
	// publishes previews.
	previewURL  string
	marshalBin  string
	cwdRoot     string
	projects    []string
	workspace   string
	stateDir    string
	stateVolume string
	agentEnv    stringList
	// maxConcurrent bounds concurrent agents (0 = default 4).
	maxConcurrent int
	// maxDiskMB bounds the state directory size in MB (0 = unlimited).
	maxDiskMB int64
	// maxCloneMB bounds a single clone size in MB (0 = unlimited).
	maxCloneMB int64
	// projectMounts maps host paths to the bridge's in-container view,
	// for translating LocalPath agent workspace mounts.
	projectMounts []bridge.ProjectMount
	// secrets selects the secret backend: env (default), local or openbao.
	secrets         string
	secretsKeyFile  string
	baoAddr         string
	baoMount        string
	baoRoleIDFile   string
	baoSecretIDFile string
	baoCAFile       string
	// egress turns the egress proxy on (default) or off.
	egress string
	// publicURL is the externally reachable base URL, used for the links
	// in outbound notifications. Empty yields relative links.
	publicURL string
	// idleSessionTimeout is how long a project session with no viewer
	// and no run lives on in the daemon (P4 D7a); zero never
	// ends one.
	idleSessionTimeout time.Duration
	// oauthRefresh runs `<marshal-bin> daemon refresh` beside the bridge
	// (P4 D12). Off unless asked.
	oauthRefresh bool
	// tunnels runs `<marshal-bin> daemon tunnels` beside the bridge
	// (one-daemon D7).
	tunnels bool
}

// parseConfig resolves flags over environment variables over defaults.
// Flag values win; each env var is consulted only when its flag was
// left at the default.
func parseConfig(args []string, stderr io.Writer) (config, error) {
	fs := flag.NewFlagSet("webbridge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", envOr("WEBBRIDGE_PROFILE", ""), "retired: local is accepted and ignored")
	socket := fs.String("socket", envOr("WEBBRIDGE_SOCKET", ""), "unix socket to serve on")
	// The TCP listener, its bearer token and its TLS are retired (D2, D3):
	// the daemon serves a unix socket and, on request, a web listener a
	// proxy publishes. --addr and --token-file stay declared because the
	// units already installed carry them, and applyDaemonDefaults warns
	// about them; the token and TLS flags are refused there instead.
	fs.String("addr", envOr("WEBBRIDGE_ADDR", ""), "retired; ignored")
	fs.String("token-file", envOr("WEBBRIDGE_TOKEN_FILE", ""), "retired; ignored")
	fs.String("token", envOr("WEBBRIDGE_TOKEN", ""), "retired: the daemon holds no token")
	fs.String("tls-cert", envOr("WEBBRIDGE_TLS_CERT", ""), "retired: the daemon serves no TLS")
	fs.String("tls-key", envOr("WEBBRIDGE_TLS_KEY", ""), "retired: the daemon serves no TLS")
	webListen := fs.String("web-listen", envOr("WEBBRIDGE_WEB_LISTEN", ""), "the web listener: unix, an absolute socket path, or a loopback host:port (default: none)")
	webGroup := fs.String("web-group", envOr("WEBBRIDGE_WEB_GROUP", ""), "share a unix --web-listen with this group (0660)")
	previewAddr := fs.String("preview-addr", envOr("WEBBRIDGE_PREVIEW_ADDR", ""), "loopback address for previews (default: a free port on 127.0.0.1; \"off\" disables)")
	previewURL := fs.String("preview-url", envOr("WEBBRIDGE_PREVIEW_URL", ""), "the public origin preview links name, for a proxy that publishes previews")
	marshalBin := fs.String("marshal-bin", envOr("WEBBRIDGE_MARSHAL_BIN", "marshal"), "marshal binary to supervise")
	cwdRoot := fs.String("cwd-root", envOr("WEBBRIDGE_CWD_ROOT", ""), "default cwd for session list/load (defaults to the working directory)")
	var projects stringList
	fs.Var(&projects, "project", "project root to manage (repeatable)")
	var projectMounts stringList
	fs.Var(&projectMounts, "project-mount", "host:container path mapping for local project mounts (repeatable)")
	workspace := fs.String("workspace", envOr("WEBBRIDGE_WORKSPACE", ""), "fleet workspace path")
	stateDir := fs.String("state-dir", envOr("WEBBRIDGE_STATE_DIR", defaultStateDir), "directory for repo mirrors and agent workspaces (defaults to /state)")
	stateVolume := fs.String("state-volume", envOr("WEBBRIDGE_STATE_VOLUME", ""),
		"named volume holding the state, for a containerized bridge (default: state on the host)")
	var agentEnv stringList
	fs.Var(&agentEnv, "agent-env", "KEY=VALUE handed to every agent container (repeatable)")
	maxConcurrent := fs.Int("max-concurrent", 0, "max concurrent agents (0 = default 4)")
	maxDiskMB := fs.Int64("max-disk-mb", 0, "max state directory size in MB (0 = unlimited)")
	maxCloneMB := fs.Int64("max-clone-mb", 0, "max clone size in MB (0 = unlimited)")
	secrets := fs.String("secrets", envOr("WEBBRIDGE_SECRETS", "env"), "secret backend: env, local or openbao")
	secretsKeyFile := fs.String("secrets-key-file", envOr("WEBBRIDGE_SECRETS_KEY_FILE", ""), "32-byte key file for --secrets local (must be outside --state-dir, mode 0600)")
	baoAddr := fs.String("bao-addr", envOr("WEBBRIDGE_BAO_ADDR", ""), "OpenBao address for --secrets openbao")
	baoMount := fs.String("bao-mount", envOr("WEBBRIDGE_BAO_MOUNT", "secret"), "OpenBao KV v2 mount")
	baoRoleIDFile := fs.String("bao-role-id-file", envOr("WEBBRIDGE_BAO_ROLE_ID_FILE", ""), "file holding the AppRole role id")
	baoSecretIDFile := fs.String("bao-secret-id-file", envOr("WEBBRIDGE_BAO_SECRET_ID_FILE", ""), "file holding the AppRole secret id")
	egress := fs.String("egress", envOr("WEBBRIDGE_EGRESS", "off"), "egress proxy for agents: on or off")
	baoCAFile := fs.String("bao-ca-file", envOr("WEBBRIDGE_BAO_CA_FILE", ""), "extra CA certificate to trust for OpenBao")
	publicURL := fs.String("public-url", envOr("WEBBRIDGE_PUBLIC_URL", ""), "externally reachable base URL, used for links in outbound notifications")
	// The relay and the joiner are retired (one-daemon D6): a remote daemon
	// is a host reached over SSH, not a bridge that dials in or is dialled.
	// --relay-join is refused; it stays declared so the refusal names the
	// flag rather than the parser's "flag provided but not defined". The
	// join flags stay declared and are accepted and ignored, because the
	// unit files already installed carry them (applyDaemonDefaults warns).
	fs.Func("relay-join", "retired: a remote daemon is a host reached over SSH", func(string) error { return nil })
	for _, f := range []string{"join", "join-name", "join-token-file", "join-ca-file"} {
		fs.String(f, "", "retired; ignored")
	}
	// The duration flags read their variable through envOr like the rest,
	// then hand the text to the parser: a variable that does not parse is
	// a configuration error rather than a silent fallback.
	idleTimeoutDefault, err := time.ParseDuration(envOr("WEBBRIDGE_IDLE_SESSION_TIMEOUT", "30m"))
	if err != nil {
		return config{}, fmt.Errorf("WEBBRIDGE_IDLE_SESSION_TIMEOUT: %w", err)
	}
	idleSessionTimeout := fs.Duration("idle-session-timeout", idleTimeoutDefault, "how long a project session with no viewer and no run lives on (0 never ends one)")
	// The boolean flag reads its variable the same way, before it is
	// declared: a variable that does not parse is a configuration error.
	oauthRefreshDefault, err := strconv.ParseBool(envOr("WEBBRIDGE_OAUTH_REFRESH", "false"))
	if err != nil {
		return config{}, fmt.Errorf("WEBBRIDGE_OAUTH_REFRESH: %w", err)
	}
	oauthRefresh := fs.Bool("oauth-refresh", oauthRefreshDefault, "run <marshal-bin> daemon refresh alongside the bridge")
	// The tunnels helper's variable is read the same way (one-daemon D7).
	tunnelsDefault, err := strconv.ParseBool(envOr("WEBBRIDGE_TUNNELS", "false"))
	if err != nil {
		return config{}, fmt.Errorf("WEBBRIDGE_TUNNELS: %w", err)
	}
	tunnels := fs.Bool("tunnels", tunnelsDefault, "run <marshal-bin> daemon tunnels alongside the bridge")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 0 {
		return config{}, fmt.Errorf("unknown argument %q", fs.Arg(0))
	}
	cfg := config{socket: *socket, webListen: *webListen, webGroup: *webGroup, previewAddr: *previewAddr, previewURL: *previewURL, marshalBin: *marshalBin, cwdRoot: *cwdRoot, projects: projects, workspace: *workspace, stateDir: *stateDir, stateVolume: *stateVolume, agentEnv: agentEnv, maxConcurrent: *maxConcurrent, maxDiskMB: *maxDiskMB, maxCloneMB: *maxCloneMB,
		secrets: *secrets, secretsKeyFile: *secretsKeyFile, baoAddr: *baoAddr, baoMount: *baoMount,
		baoRoleIDFile: *baoRoleIDFile, baoSecretIDFile: *baoSecretIDFile, baoCAFile: *baoCAFile, egress: *egress, publicURL: *publicURL,
		idleSessionTimeout: *idleSessionTimeout, oauthRefresh: *oauthRefresh, tunnels: *tunnels}
	pm, err := parseProjectMounts(projectMounts)
	if err != nil {
		return config{}, err
	}
	cfg.projectMounts = pm
	given := givenFrom(fs)
	if err := applyDaemonDefaults(&cfg, given, *profile); err != nil {
		return config{}, err
	}
	// The web listener's syntax, and the pairing of its settings, are the
	// bridge's to judge (D3), so parseConfig names no rule the listener
	// repeats. --web-group on its own names a listener that is not there.
	if cfg.webGroup != "" && cfg.webListen == "" {
		return config{}, fmt.Errorf("--web-group needs --web-listen")
	}
	if cfg.webListen != "" {
		if _, err := bridge.ParseWebListen(cfg.webListen, cfg.webGroup, cfg.socket); err != nil {
			return config{}, err
		}
	}
	// Previews are a loopback origin of their own; "off" disables them.
	if cfg.previewAddr != "" && cfg.previewAddr != "off" {
		if err := bridge.CheckLoopback("--preview-addr", cfg.previewAddr); err != nil {
			return config{}, err
		}
	}
	// A public preview origin is what a proxy publishes, so it carries no
	// path, query or fragment of its own.
	if cfg.previewURL != "" {
		if err := bridge.CheckPreviewURL(cfg.previewURL); err != nil {
			return config{}, err
		}
	}
	if cfg.egress != "on" && cfg.egress != "off" {
		return config{}, fmt.Errorf("--egress %q: expected on or off", cfg.egress)
	}
	switch cfg.secrets {
	case "env":
	case "local":
		if cfg.secretsKeyFile == "" {
			return config{}, fmt.Errorf("--secrets local needs --secrets-key-file")
		}
	case "openbao":
		if cfg.baoAddr == "" || cfg.baoRoleIDFile == "" || cfg.baoSecretIDFile == "" {
			return config{}, fmt.Errorf("--secrets openbao needs --bao-addr, --bao-role-id-file and --bao-secret-id-file")
		}
	default:
		return config{}, fmt.Errorf("--secrets %q: expected env, local or openbao", cfg.secrets)
	}
	return cfg, nil
}

// givenSettings records which flags the command line set, so a setting
// is told apart from its default. Whether a variable was set is
// consulted per setting, from the environment.
type givenSettings struct {
	flags map[string]bool
}

// givenFrom collects the flags present in args. It must run after the
// flag set parses: Visit reports only flags actually given.
func givenFrom(fs *flag.FlagSet) givenSettings {
	g := givenSettings{flags: make(map[string]bool)}
	fs.Visit(func(f *flag.Flag) { g.flags[f.Name] = true })
	return g
}

// given reports whether a setting reached the command line or its
// variable is set to something. An empty variable means unset, as envOr
// treats it: os.Getenv cannot tell unset from set-and-blank, and this
// file has always read them the same way.
func (g givenSettings) given(flagName, env string) bool {
	if g.flags[flagName] {
		return true
	}
	return os.Getenv(env) != ""
}

// set is given plus "and not empty": an empty value means unset, as
// envOr treats it, so an explicitly blank flag falls back to the
// default rather than selecting nothing.
func (g givenSettings) set(flagName, env, value string) bool {
	return value != "" && g.given(flagName, env)
}

// applyDaemonDefaults validates the retired --profile and applies the one
// daemon's defaults and refusals. Every webbridge is the daemon
// (docs/one-daemon/design.md), so these settings always apply.
func applyDaemonDefaults(cfg *config, given givenSettings, profile string) error {
	// --profile is retired. "local", or no flag at all, is this daemon, and
	// saying so is worth a warning; any other value asked for a bridge that
	// no longer exists.
	switch profile {
	case "", "local":
		if given.given("profile", "WEBBRIDGE_PROFILE") {
			slog.Default().Warn("webbridge: --profile is retired; the flag is ignored")
		}
	default:
		return fmt.Errorf("--profile %q: the server profile is retired; every webbridge is the daemon (docs/one-daemon/design.md)", profile)
	}

	// --relay-join is retired: a remote daemon is a host reached over SSH,
	// not a bridge that dials in and is reversed onto (D6). It is refused
	// rather than ignored, because a unit that carries it asked for a
	// listener that no longer exists.
	if given.given("relay-join", "WEBBRIDGE_RELAY_JOIN") {
		return fmt.Errorf("--relay-join is retired: a remote daemon is a host reached over SSH (marshal fleet add; docs/one-daemon/design.md D6)")
	}

	// The TCP listener's token and TLS are retired with it (D2, D3): the
	// daemon holds no token and serves no TLS, and a proxy in front of
	// --web-listen is where a certificate belongs. These are refused, not
	// ignored: a unit that carries one asked for a secret or a
	// certificate this daemon no longer has.
	for _, f := range []struct{ flag, env string }{
		{"token", "WEBBRIDGE_TOKEN"},
		{"tls-cert", "WEBBRIDGE_TLS_CERT"},
		{"tls-key", "WEBBRIDGE_TLS_KEY"},
	} {
		if given.given(f.flag, f.env) {
			return fmt.Errorf("--%s is retired: the daemon holds no token and serves no TLS; put a proxy in front of --web-listen (docs/one-daemon/design.md D2, D3)", f.flag)
		}
	}

	// --addr and --token-file retired with the TCP listener, but are
	// accepted and ignored for the same reason as the join flags: the
	// units already installed carry them. They join the one warning.
	var retired []string
	for _, f := range []struct{ flag, env string }{
		{"join", "WEBBRIDGE_JOIN"},
		{"join-name", "WEBBRIDGE_JOIN_NAME"},
		{"join-token-file", "WEBBRIDGE_JOIN_TOKEN_FILE"},
		{"join-ca-file", "WEBBRIDGE_JOIN_CA_FILE"},
		{"addr", "WEBBRIDGE_ADDR"},
		{"token-file", "WEBBRIDGE_TOKEN_FILE"},
	} {
		if given.given(f.flag, f.env) {
			retired = append(retired, "--"+f.flag)
		}
	}
	if len(retired) > 0 {
		slog.Default().Warn("webbridge: " + strings.Join(retired, ", ") +
			" are retired and ignored; run marshal daemon install to rewrite the unit")
	}

	if cfg.idleSessionTimeout < 0 {
		return fmt.Errorf("--idle-session-timeout %s: a session timeout cannot be negative", cfg.idleSessionTimeout)
	}

	// The daemon's defaults, each applying only when its flag and its
	// variable are both unset.
	dir, err := bridge.DefaultDaemonDir()
	if err != nil {
		return err
	}
	if !given.set("socket", "WEBBRIDGE_SOCKET", cfg.socket) {
		cfg.socket = filepath.Join(dir, "marshal.sock")
	}
	if !given.set("state-dir", "WEBBRIDGE_STATE_DIR", cfg.stateDir) {
		cfg.stateDir = filepath.Join(dir, "state")
	}
	if !given.set("workspace", "WEBBRIDGE_WORKSPACE", cfg.workspace) {
		cfg.workspace = filepath.Join(dir, "fleet.json")
	}
	return nil
}

// buildSecretProvider constructs the backend the flags select.
func buildSecretProvider(cfg config) (bridge.SecretProvider, error) {
	switch cfg.secrets {
	case "local":
		return bridge.NewLocalProvider(cfg.stateDir, cfg.secretsKeyFile)
	case "openbao":
		return bridge.NewBaoProvider(cfg.baoAddr, cfg.baoMount, cfg.baoRoleIDFile, cfg.baoSecretIDFile, cfg.baoCAFile)
	default:
		return bridge.NewEnvProvider(), nil
	}
}

// runSubcommand handles "webbridge print-env", "webbridge egress" and
// "webbridge secrets init-key FILE". It reports whether args named a
// subcommand.
func runSubcommand(ctx context.Context, args []string, stdout io.Writer) (bool, error) {
	if len(args) >= 1 && args[0] == "print-env" {
		if len(args) != 1 {
			return true, fmt.Errorf("usage: webbridge print-env")
		}
		// The environment goes to stdout and nothing else does: the login
		// shell parses everything this process prints (P4 D7).
		return true, bridge.WriteEnv(stdout, os.Environ())
	}
	if len(args) >= 1 && args[0] == "egress" {
		fs := flag.NewFlagSet("webbridge egress", flag.ContinueOnError)
		listen := fs.String("listen", ":3128", "proxy listen address")
		previewListen := fs.String("preview-listen", "", "preview listener address (empty disables previews)")
		control := fs.String("control", "", "bridge control address (unix:///path)")
		if err := fs.Parse(args[1:]); err != nil {
			return true, err
		}
		if *control == "" || fs.NArg() > 0 {
			return true, fmt.Errorf("usage: webbridge egress --listen ADDR --control unix:///PATH")
		}
		sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		return true, bridge.ServeEgress(sigCtx, *listen, *previewListen, *control)
	}
	if len(args) >= 1 && args[0] == "secrets" {
		if len(args) != 3 || args[1] != "init-key" {
			return true, fmt.Errorf("usage: webbridge secrets init-key FILE")
		}
		if err := bridge.GenerateKeyFile(args[2]); err != nil {
			return true, err
		}
		fmt.Fprintf(stdout, "wrote 32-byte key to %s (mode 0600); keep it outside the state dir\n", args[2])
		return true, nil
	}
	return false, nil
}

// localProjectEnv is the daemon's project environment resolver: project
// children print their environment by re-running this binary with "print-env"
// (P4 D7). Symlinks are resolved, so a login shell that starts elsewhere still
// reaches the binary.
func localProjectEnv() (*bridge.ProjectEnv, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the webbridge binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return bridge.NewProjectEnv([]string{exe, "print-env"}), nil
}

// parseProjectMounts parses repeatable --project-mount HOST:CONTAINER
// flags into ProjectMount entries.
func parseProjectMounts(specs []string) ([]bridge.ProjectMount, error) {
	var mounts []bridge.ProjectMount
	for _, s := range specs {
		parts := strings.SplitN(s, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("--project-mount %q: expected HOST:CONTAINER", s)
		}
		mounts = append(mounts, bridge.ProjectMount{
			Host:      filepath.Clean(parts[0]),
			Container: filepath.Clean(parts[1]),
		})
	}
	return mounts, nil
}

// askpassResponse answers one git credential prompt. Git asks for a
// username and a password separately, so the prompt text selects which
// value to return.
func askpassResponse(prompt string) string {
	if strings.HasPrefix(strings.ToLower(prompt), "username") {
		if u := os.Getenv("MARSHAL_ASKPASS_USER"); u != "" {
			return u
		}
		return "x-access-token"
	}
	return os.Getenv("MARSHAL_ASKPASS_SECRET")
}

// envOr returns the environment variable's value when set and
// non-empty, else def. An explicitly empty variable means "unset".
//
// This is intentional: os.Getenv returns "" for both unset and
// empty-set variables, so we treat them identically. This means a
// user cannot override a default with an empty string — they must
// either set a non-empty value or unset the variable. This prevents
// accidental empty-string overrides (e.g. from a misconfigured
// .env file) from silently clearing a default.
func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// errReexec is run's answer when the daemon is to re-execute itself (decision 2).
type errReexec struct{ path string }

func (e *errReexec) Error() string { return e.path + ": re-execute requested" }

// The seams a test replaces.
var ownExecutable = os.Executable
var execSelf = syscall.Exec

// testAsBinaryEnv makes the test binary run as webbridge (TestMain).
const testAsBinaryEnv = "WEBBRIDGE_TEST_AS_BINARY"

func main() {
	// Drop libmalloc's debugging knobs before any child is spawned. The
	// bridge hands os.Environ() to every `marshal acp` child it supervises
	// (fleet.go, transport.go), so an inherited knob would make each child
	// print a diagnostic line to stderr — one per agent — and corrupt the
	// terminal a TUI is drawing on. Scrubbing here covers those children
	// even when the supervised binary predates the same fix in
	// cmd/marshal. See internal/procenv.
	procenv.ScrubMallocDebugEnv()
	err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	if err == nil {
		return
	}
	// A daemon that decided to restart replaces this process image with the
	// new build — same arguments, same environment — so its listeners, its
	// workspace and its state carry over unchanged (decision 2). Exec only
	// returns on failure, and then there is no daemon left to keep serving.
	if reexec, ok := err.(*errReexec); ok {
		if xerr := execSelf(reexec.path, os.Args, os.Environ()); xerr != nil {
			fmt.Fprintf(os.Stderr, "webbridge: re-execute %s: %v\n", reexec.path, xerr)
			os.Exit(1)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "webbridge: %v\n", err)
	os.Exit(1)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	// The daemon reports when it started, and the plan dates that
	// from the start of run rather than from the listener.
	startedAt := time.Now()

	// --version is handled before flag parsing: parseConfig uses
	// flag.ContinueOnError and would reject the flag as unknown. The
	// banner goes to stdout (the caller's writer), never os.Stdout.
	for _, a := range args {
		if a == "--version" {
			fmt.Fprintln(stdout, versionString())
			return nil
		}
	}

	// GIT_ASKPASS mode: git re-execs this binary to ask for a credential.
	// Answer from the environment and exit before doing anything else —
	// this process must not start a server or touch the workspace.
	if os.Getenv("MARSHAL_ASKPASS") == "1" {
		prompt := ""
		if len(args) > 0 {
			prompt = args[0]
		}
		fmt.Fprintln(stdout, askpassResponse(prompt))
		return nil
	}

	if handled, err := runSubcommand(ctx, args, stdout); handled {
		return err
	}

	cfg, err := parseConfig(args, stderr)
	if err != nil {
		return err
	}

	ws := bridge.NewWorkspace(cfg.workspace)
	quarantined, err := ws.Load()
	if err != nil {
		return fmt.Errorf("load workspace: %w", err)
	}
	if quarantined != "" {
		fmt.Fprintf(stderr, "webbridge: quarantined unreadable workspace at %s\n", quarantined)
	}
	if err := ws.MarkAllInterrupted(); err != nil {
		return fmt.Errorf("mark interrupted agents: %w", err)
	}
	for _, root := range cfg.projects {
		if err := bridge.ValidateProjectRoot(root); err != nil {
			return fmt.Errorf("--project %q: %w", root, err)
		}
		if err := ws.AddProject(root); err != nil {
			return err
		}
	}
	if cfg.cwdRoot != "" {
		if err := bridge.ValidateProjectRoot(cfg.cwdRoot); err != nil {
			return fmt.Errorf("--cwd-root %q: %w", cfg.cwdRoot, err)
		}
		if err := ws.AddProject(cfg.cwdRoot); err != nil {
			return err
		}
	}
	agentEnv := bridge.InheritedAgentEnv()
	explicit, err := bridge.ParseAgentEnv(cfg.agentEnv)
	if err != nil {
		return err
	}
	for k, v := range explicit {
		agentEnv[k] = v
	}

	fleet := bridge.NewFleet(ws, cfg.marshalBin, agentEnv, cfg.stateDir, bridge.Limits{
		MaxConcurrent: cfg.maxConcurrent,
		MaxDiskMB:     cfg.maxDiskMB,
		MaxCloneMB:    cfg.maxCloneMB,
	}, daemonVersion(), cfg.projectMounts, cfg.stateVolume)

	// The daemon's state is on the host unless a named volume was given: the
	// fleet then binds its state directory into the mounts it hands out,
	// instead of naming a volume. This must precede every start below —
	// egress, reattach, the poller and the scheduler — so no agent or probe
	// starts with the other answer.
	if cfg.stateVolume == "" {
		stateOnHost := ""
		if home, herr := os.UserHomeDir(); herr != nil {
			slog.Default().Warn("webbridge: no home directory for container agents' config; using the state subpath", "err", herr)
		} else {
			stateOnHost = appconfig.UserDir(home)
		}
		fleet.SetStateOnHost(stateOnHost)
	}

	// A project child starts with its project's environment (decision 7). A
	// bridge that cannot name its own binary leaves children inheriting the
	// bridge's environment instead.
	if projectEnv, err := localProjectEnv(); err != nil {
		slog.Default().Warn("webbridge: project children inherit the bridge's environment", "err", err)
	} else {
		fleet.SetProjectEnv(projectEnv)
	}
	// A session with no viewer and no run is ended after the idle timeout
	// (P4 D7a), swept on its own goroutine; zero never ends one, and the sweep
	// returns at once.
	fleet.SetIdleSessionTimeout(cfg.idleSessionTimeout)
	go fleet.RunIdleSweep(ctx)
	// A stale child is restarted on a new marshal build once it is idle
	// (P6 B4), on its own goroutine: unlike the idle sweep, this one runs
	// whatever the idle timeout is.
	go fleet.RunRestartSweep(ctx)

	secrets, err := buildSecretProvider(cfg)
	if err != nil {
		return fmt.Errorf("secrets backend: %w", err)
	}
	fleet.SetSecrets(secrets)
	// Recovery owns factory workers before ordinary reattachment or scheduled
	// work is admitted. A journal or ownership failure stops startup.
	factoryStore, factoryController, err := openRecoveredFactory(ctx, fleet, cfg.stateDir, bridge.NewFactoryEffects(fleet))
	if err != nil {
		fleet.Close()
		return err
	}
	factoryCtx, stopFactory := context.WithCancel(ctx)
	factoryDone := make(chan struct{})
	// Factory shutdown is one idempotent sequence: persist what the
	// controller owns, cancel the scheduler, wait for Run to return, then
	// close the journal. Every path that closes the fleet after this point
	// runs it first; the defer below is the backstop.
	shutdownFactory := sync.OnceFunc(func() {
		if err := factoryController.Quiesce(); err != nil {
			slog.Default().Error("webbridge: factory shutdown persistence", "err", err)
		}
		stopFactory()
		<-factoryDone
		_ = factoryStore.Close()
	})
	if cfg.egress == "on" {
		// A failure leaves open-network workspaces unproxied, as before
		// the proxy existed; workspaces that restrict the network or
		// inject credentials refuse to start agents until it is back.
		if err := fleet.StartEgress(ctx); err != nil {
			slog.Default().Warn("webbridge: egress proxy unavailable; restricted workspaces will refuse to start agents", "err", err)
		}
	}

	if errs := fleet.ReattachAll(ctx); len(errs) > 0 {
		for _, err := range errs {
			slog.Default().Warn("webbridge: reattach agent", "err", err)
		}
	}
	go func() {
		defer close(factoryDone)
		if err := factoryController.Run(factoryCtx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Default().Error("webbridge: factory scheduler stopped", "err", err)
		}
	}()
	// The backstop for every return below: Run is started above, so waiting
	// on factoryDone here is always safe.
	defer shutdownFactory()

	// Each pinned project's child starts on its own goroutine, one after
	// another (decision 9).
	go fleet.StartPinned(ctx)

	// Watch labelled issues on registered repos. Off by default; only
	// repos with Watch set are polled.
	fleet.StartPoller(0)
	// Run due schedules. Fleet mode only: registry mode has no scheduler.
	fleet.StartScheduler(nil)

	api := bridge.NewServer(fleet, "", cfg.publicURL, bridge.NewFactoryAPI(factoryStore, factoryController, fleet))

	// serveErr carries the first listener to stop serving. The socket and
	// the web listener can both exist, so it holds two, and each goroutine
	// parks its answer here rather than dropping it.
	serveErr := make(chan error, 2)

	// The daemon listens on a unix socket of the user's own: the same routes
	// as ServeHTTP without the bearer check, peer-checked instead, so a client
	// on the same account drives it without a secret on the command line
	// (decisions 5-7).
	var socketSrv *http.Server
	// The daemon records its own executable so a replaced build can
	// restart it, and so the socket's restart route knows what to
	// execute (P6 B5). A daemon that cannot name its binary serves on
	// without the restart route rather than failing to start.
	if own, oerr := ownExecutable(); oerr != nil {
		slog.Default().Warn("webbridge: restarting the daemon is unavailable", "err", oerr)
	} else if oerr := fleet.SetOwnBinary(own); oerr != nil {
		slog.Default().Warn("webbridge: restarting the daemon is unavailable", "path", own, "err", oerr)
	}
	if cfg.oauthRefresh {
		// The refresh helper (P4 D12): `<marshal-bin> daemon refresh`
		// beside the bridge, on the run's context. run waits for it to
		// return before it returns, so the bridge never leaves a
		// helper behind.
		helper := &bridge.Helper{
			Name:   "refresh",
			Argv:   []string{cfg.marshalBin, "daemon", "refresh"},
			Stderr: stderr,
		}
		api.SetRefreshHelper(helper)
		helperCtx, stopHelper := context.WithCancel(ctx)
		helperDone := make(chan struct{})
		go func() {
			defer close(helperDone)
			helper.Run(helperCtx)
		}()
		defer func() {
			stopHelper()
			<-helperDone
		}()
	}
	if cfg.tunnels {
		// The tunnels helper (one-daemon D7): `<marshal-bin> daemon
		// tunnels` keeps one SSH tunnel per host beside the bridge and
		// reports them on the socket. It is not the refresh helper: the
		// hosts are its report, so it is not handed to
		// SetRefreshHelper. run waits for it before returning, as it
		// does for the refresh helper.
		helper := &bridge.Helper{
			Name:   "tunnels",
			Argv:   []string{cfg.marshalBin, "daemon", "tunnels", "--socket", cfg.socket},
			Stderr: stderr,
		}
		tunnelsCtx, stopTunnels := context.WithCancel(ctx)
		tunnelsDone := make(chan struct{})
		go func() {
			defer close(tunnelsDone)
			helper.Run(tunnelsCtx)
		}()
		defer func() {
			stopTunnels()
			<-tunnelsDone
		}()
	}
	sln, lerr := bridge.ListenLocal(cfg.socket)
	if lerr != nil {
		// A refused socket — another daemon, a directory open to
		// other users — leaves nothing to serve. Stop the factory
		// before closing the fleet, as a failed TCP listen does
		// below: quiescing first releases the journal lock the next
		// start has to take.
		shutdownFactory()
		fleet.Close()
		return lerr
	}
	// The web listener is bound after the socket's directory exists and
	// before anything serves, so SetDaemonInfo below can report what the
	// bind did. "unix" puts web.sock beside the daemon's own socket. A
	// listener that cannot bind costs the web pages, not the daemon: the
	// failure is logged at error level and reported as WebError, and the
	// socket carries on (as a failed Studio port did, OD1).
	var webSrv *http.Server
	var webLn net.Listener
	var webListen, webErr string
	if cfg.webListen != "" {
		web, perr := bridge.ParseWebListen(cfg.webListen, cfg.webGroup, cfg.socket)
		if perr != nil {
			webErr = perr.Error()
			slog.Default().Error("webbridge: web listener unavailable", "err", perr)
		} else if wln, werr := bridge.ListenWeb(web); werr != nil {
			webErr = werr.Error()
			slog.Default().Error("webbridge: web listener unavailable", "web-listen", web.String(), "err", werr)
		} else {
			webLn = wln
			webListen = web.String()
			if web.Network == "tcp" {
				// A configured port 0 names its own once bound.
				webListen = wln.Addr().String()
			}
			webSrv = &http.Server{Handler: api, ReadHeaderTimeout: 10 * time.Second}
			fmt.Fprintf(stderr, "webbridge: web listener on %s\n", webListen)
		}
	}

	api.SetDaemonInfo(bridge.DaemonInfo{
		Version:   daemonVersion(),
		Profile:   "local",
		PID:       os.Getpid(),
		StartedAt: startedAt.UnixMilli(),
		Socket:    cfg.socket,
		WebListen: webListen,
		WebError:  webErr,
	})

	// Previews get a listener, and so an origin, of their own: a page the
	// agent serves must not share storage with the UI. They are independent
	// of the web listener: a preview is reachable whenever previews are on.
	var previewSrv *http.Server
	if cfg.previewAddr != "off" {
		pln, perr := listenPreview(cfg.previewAddr)
		if perr != nil {
			slog.Default().Warn("webbridge: previews unavailable", "err", perr)
		} else {
			api.SetPreviewPort(pln.Addr().(*net.TCPAddr).Port)
			if cfg.previewURL != "" {
				if uerr := api.SetPreviewURL(cfg.previewURL); uerr != nil {
					slog.Default().Warn("webbridge: preview URL", "err", uerr)
				}
			}
			previewSrv = &http.Server{Handler: api.PreviewHandler(), ReadHeaderTimeout: 10 * time.Second}
			go func() { _ = previewSrv.Serve(pln) }()
			fmt.Fprintf(stderr, "webbridge: previews on http://%s\n", pln.Addr())
		}
	}

	socketSrv = &http.Server{
		Handler:           api.SocketHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		err := socketSrv.Serve(bridge.PeerChecked(sln, os.Getuid(), slog.Default()))
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()
	fmt.Fprintf(stderr, "webbridge: listening on unix://%s\n", cfg.socket)

	if webSrv != nil {
		go func() {
			err := webSrv.Serve(webLn)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			serveErr <- err
		}()
	}

	// Reconcile orphaned worktrees after the server is listening, so a slow
	// prune cannot delay accepting requests.
	go fleet.ReconcileWorktrees(context.Background())

	// SIGINT/SIGTERM (or the caller's ctx) triggers shutdown: HTTP
	// first, then the child.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The one shutdown sequence every path below runs once: HTTP first, then
	// the factory and the children. It is idempotent so a signal, a failed
	// listener and a restart can each ask for it without checking.
	shutdown := sync.OnceFunc(func() {
		slog.Default().Info("webbridge: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if previewSrv != nil {
			_ = previewSrv.Shutdown(shutdownCtx)
		}
		if socketSrv != nil {
			// Closing the socket server closes the unix listener, which is
			// what removes the socket file.
			_ = socketSrv.Shutdown(shutdownCtx)
		}
		if webSrv != nil {
			if err := webSrv.Shutdown(shutdownCtx); err != nil {
				slog.Default().Warn("webbridge: HTTP shutdown", "err", err)
			}
		}
		shutdownFactory()
		fleet.Close()
	})

	select {
	case err := <-serveErr:
		// Serve only returns a non-ErrServerClosed error when the
		// listener itself fails, which net.Listen above has already
		// ruled out — in practice err is always nil here. The channel
		// exists so the serve goroutine has a buffered parking spot.
		// Whichever listener failed, the other one is still up: close
		// it too, so the socket file is removed and the process does
		// not linger answering nothing. Stopping the factory before the
		// fleet releases its journal lock.
		if socketSrv != nil {
			_ = socketSrv.Close()
		}
		if webSrv != nil {
			_ = webSrv.Close()
		}
		shutdownFactory()
		fleet.Close()
		return err
	case sr := <-fleet.SelfRestarts():
		// A replaced build, or a restart asked for over the socket (P6 B5):
		// serve out what is in flight, take the children down, and let main
		// re-execute the daemon in this process's place. stop() comes first
		// so a shutdown that drags on is still interruptible.
		slog.Default().Info("webbridge: restarting on " + sr.Version + ": " + sr.Reason)
		stop()
		shutdown()
		return &errReexec{path: sr.Path}
	case <-sigCtx.Done():
	}
	stop() // restore default signal handling so a second Ctrl-C kills

	shutdown()
	return nil
}

func openRecoveredFactory(ctx context.Context, fleet *bridge.Fleet, stateDir string, effects bridge.FactoryEffects) (*bridge.FactoryStore, *bridge.FactoryController, error) {
	store, err := bridge.OpenFactoryStore(filepath.Join(stateDir, "factory", "v1"))
	if err != nil {
		return nil, nil, fmt.Errorf("open factory: %w", err)
	}
	controller := bridge.NewFactoryController(store, effects, nil)
	fleet.SetFactoryController(controller)
	if err := controller.Recover(ctx); err != nil {
		_ = store.Close()
		return nil, nil, fmt.Errorf("recover factory: %w", err)
	}
	return store, controller, nil
}

// listenPreview opens the preview listener: previewAddr when given, else a
// free port on 127.0.0.1. It needs no address of the API's: previews have
// their own origin because they are their own listener.
func listenPreview(previewAddr string) (net.Listener, error) {
	if previewAddr == "" {
		previewAddr = "127.0.0.1:0"
	}
	return net.Listen("tcp", previewAddr)
}
