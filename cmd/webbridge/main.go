// Command webbridge exposes Marshal sessions over HTTP and SSE: it
// supervises a `marshal acp` child, brokers its permission/question
// requests, and serves the REST API, event stream, and embedded SPA.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// daemonVersion is the bare version, "dev" when the build was not
// stamped. The local profile reports it through GET /api/daemon, which
// wants the version itself rather than the banner around it.
func daemonVersion() string {
	if version == "" {
		return "dev"
	}
	return version
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
	// profile is "server" (the default) or "local" (P4 D1).
	profile string
	// socket is the local profile's unix listener.
	socket string
	addr   string
	// previewAddr is where previews are served, on an origin of their own.
	// Empty means the --addr host on an automatic port; "off" disables.
	previewAddr string
	token       string
	marshalBin  string
	cwdRoot     string
	projects    []string
	workspace   string
	stateDir    string
	stateVolume string
	agentEnv    stringList
	// tlsCert and tlsKey enable HTTPS when both are set. Certificate
	// lifecycle — issuance, renewal — is deliberately out of scope:
	// every deployment already solves it differently (a reverse proxy,
	// a homelab CA, tailscale cert), and baking ACME in would add a
	// dependency to replace something the environment provides.
	tlsCert string
	tlsKey  string
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
	// and no run lives on in the local profile (P4 D7a); zero never
	// ends one.
	idleSessionTimeout time.Duration
	// oauthRefresh runs `<marshal-bin> daemon refresh` beside the local
	// profile's bridge (P4 D12). Off unless asked.
	oauthRefresh bool
}

// parseConfig resolves flags over environment variables over defaults.
// Flag values win; each env var is consulted only when its flag was
// left at the default.
func parseConfig(args []string, stderr io.Writer) (config, error) {
	fs := flag.NewFlagSet("webbridge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", envOr("WEBBRIDGE_PROFILE", "server"), "profile: server (network listeners, container-capable) or local (unix socket, host processes)")
	socket := fs.String("socket", envOr("WEBBRIDGE_SOCKET", ""), "unix socket to serve on (--profile local)")
	addr := fs.String("addr", envOr("WEBBRIDGE_ADDR", "127.0.0.1:7700"), "listen address")
	previewAddr := fs.String("preview-addr", envOr("WEBBRIDGE_PREVIEW_ADDR", ""), "listen address for previews, served from a separate origin (default: the --addr host on a free port; \"off\" disables)")
	token := fs.String("token", envOr("WEBBRIDGE_TOKEN", ""), "bearer token for /api routes (generated when empty)")
	marshalBin := fs.String("marshal-bin", envOr("WEBBRIDGE_MARSHAL_BIN", "marshal"), "marshal binary to supervise")
	cwdRoot := fs.String("cwd-root", envOr("WEBBRIDGE_CWD_ROOT", ""), "default cwd for session list/load (defaults to the working directory)")
	var projects stringList
	fs.Var(&projects, "project", "project root to manage (repeatable)")
	var projectMounts stringList
	fs.Var(&projectMounts, "project-mount", "host:container path mapping for local project mounts (repeatable)")
	workspace := fs.String("workspace", envOr("WEBBRIDGE_WORKSPACE", ""), "fleet workspace path")
	tlsCert := fs.String("tls-cert", envOr("WEBBRIDGE_TLS_CERT", ""), "PEM certificate file; enables HTTPS when set with --tls-key")
	tlsKey := fs.String("tls-key", envOr("WEBBRIDGE_TLS_KEY", ""), "PEM private key file; enables HTTPS when set with --tls-cert")
	stateDir := fs.String("state-dir", envOr("WEBBRIDGE_STATE_DIR", defaultStateDir), "directory for repo mirrors and agent workspaces (defaults to /state)")
	stateVolume := fs.String("state-volume", envOr("WEBBRIDGE_STATE_VOLUME", "marshal-state"),
		"name of the volume mounted at --state-dir; agents mount subpaths of it")
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
	egress := fs.String("egress", envOr("WEBBRIDGE_EGRESS", "on"), "egress proxy for agents: on or off")
	baoCAFile := fs.String("bao-ca-file", envOr("WEBBRIDGE_BAO_CA_FILE", ""), "extra CA certificate to trust for OpenBao")
	publicURL := fs.String("public-url", envOr("WEBBRIDGE_PUBLIC_URL", ""), "externally reachable base URL, used for links in outbound notifications")
	// The duration flags read their variable through envOr like the rest,
	// then hand the text to the parser: a variable that does not parse is
	// a configuration error rather than a silent fallback.
	idleTimeoutDefault, err := time.ParseDuration(envOr("WEBBRIDGE_IDLE_SESSION_TIMEOUT", "30m"))
	if err != nil {
		return config{}, fmt.Errorf("WEBBRIDGE_IDLE_SESSION_TIMEOUT: %w", err)
	}
	idleSessionTimeout := fs.Duration("idle-session-timeout", idleTimeoutDefault, "how long a project session with no viewer and no run lives on (--profile local; 0 never ends one)")
	// The boolean flag reads its variable the same way, before it is
	// declared: a variable that does not parse is a configuration error.
	oauthRefreshDefault, err := strconv.ParseBool(envOr("WEBBRIDGE_OAUTH_REFRESH", "false"))
	if err != nil {
		return config{}, fmt.Errorf("WEBBRIDGE_OAUTH_REFRESH: %w", err)
	}
	oauthRefresh := fs.Bool("oauth-refresh", oauthRefreshDefault, "run <marshal-bin> daemon refresh alongside the bridge (--profile local)")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 0 {
		return config{}, fmt.Errorf("unknown argument %q", fs.Arg(0))
	}
	cfg := config{profile: *profile, socket: *socket, addr: *addr, previewAddr: *previewAddr, token: *token, marshalBin: *marshalBin, cwdRoot: *cwdRoot, projects: projects, workspace: *workspace, stateDir: *stateDir, stateVolume: *stateVolume, agentEnv: agentEnv, tlsCert: *tlsCert, tlsKey: *tlsKey, maxConcurrent: *maxConcurrent, maxDiskMB: *maxDiskMB, maxCloneMB: *maxCloneMB,
		secrets: *secrets, secretsKeyFile: *secretsKeyFile, baoAddr: *baoAddr, baoMount: *baoMount,
		baoRoleIDFile: *baoRoleIDFile, baoSecretIDFile: *baoSecretIDFile, baoCAFile: *baoCAFile, egress: *egress, publicURL: *publicURL,
		idleSessionTimeout: *idleSessionTimeout, oauthRefresh: *oauthRefresh}
	pm, err := parseProjectMounts(projectMounts)
	if err != nil {
		return config{}, err
	}
	cfg.projectMounts = pm
	if err := applyProfile(&cfg, givenFrom(fs)); err != nil {
		return config{}, err
	}
	// Half-configured TLS fails loudly. Serving plaintext because one
	// flag name was mistyped is the failure nobody notices.
	if (cfg.tlsCert == "") != (cfg.tlsKey == "") {
		return config{}, fmt.Errorf("--tls-cert and --tls-key must be given together")
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
	if cfg.workspace == "" {
		p, err := bridge.DefaultWorkspacePath()
		if err != nil {
			return config{}, err
		}
		cfg.workspace = p
	}
	// The working-directory project is a server-profile convenience:
	// the local profile registers no project unless it was named.
	if cfg.cwdRoot == "" && cfg.profile != "local" {
		cwd, err := os.Getwd()
		if err != nil {
			return config{}, fmt.Errorf("--cwd-root unset and cannot determine working directory: %w", err)
		}
		cfg.cwdRoot = cwd
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

// applyProfile validates --profile and applies the local profile's
// defaults and refusals (decisions 1-3). The server profile is today's
// bridge and is left alone apart from the settings that belong to the
// local profile and would silently do nothing here.
func applyProfile(cfg *config, given givenSettings) error {
	switch cfg.profile {
	case "server":
		if cfg.socket != "" {
			return fmt.Errorf("--socket is for --profile local")
		}
		if cfg.addr == "off" {
			return fmt.Errorf("--addr off is for --profile local")
		}
		if given.given("idle-session-timeout", "WEBBRIDGE_IDLE_SESSION_TIMEOUT") {
			return fmt.Errorf("--idle-session-timeout is for --profile local")
		}
		if given.given("oauth-refresh", "WEBBRIDGE_OAUTH_REFRESH") {
			return fmt.Errorf("--oauth-refresh is for --profile local")
		}
		return nil
	case "local":
	default:
		return fmt.Errorf("--profile %q: expected server or local", cfg.profile)
	}

	// Decision 3: no egress proxy, and no secret backend but the
	// environment. Each refusal fires only when the setting was given;
	// the quiet answers below are the defaults.
	if given.given("egress", "WEBBRIDGE_EGRESS") {
		if cfg.egress == "on" {
			return fmt.Errorf("--egress on: the local profile runs no egress proxy")
		}
	} else {
		cfg.egress = "off"
	}
	if cfg.secrets != "env" {
		return fmt.Errorf("--secrets %s: the local profile takes secrets from the environment", cfg.secrets)
	}
	// A token belongs to the TCP listener, which --addr off does not
	// open. Refusing beats accepting it and never using it, which is
	// the rule the rest of this function follows.
	if cfg.addr == "off" && given.set("token", "WEBBRIDGE_TOKEN", cfg.token) {
		return fmt.Errorf("--token is for the TCP listener, and --addr off opens none")
	}
	if cfg.idleSessionTimeout < 0 {
		return fmt.Errorf("--idle-session-timeout %s: the local profile waits a negative time for an idle session", cfg.idleSessionTimeout)
	}

	// Decision 2: the local profile's defaults, each applying only when
	// its flag and its variable are both unset.
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

// localProjectEnv is the local profile's project environment resolver: project
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

// genToken returns a random 128-bit bearer token, hex-encoded.
func genToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func main() {
	// Drop libmalloc's debugging knobs before any child is spawned. The
	// bridge hands os.Environ() to every `marshal acp` child it supervises
	// (fleet.go, transport.go), so an inherited knob would make each child
	// print a diagnostic line to stderr — one per agent — and corrupt the
	// terminal a TUI is drawing on. Scrubbing here covers those children
	// even when the supervised binary predates the same fix in
	// cmd/marshal. See internal/procenv.
	procenv.ScrubMallocDebugEnv()
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "webbridge: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	// The local profile reports when it started, and the plan dates that
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
	// --addr off means the local socket is the only listener, and the
	// socket is peer-checked rather than token-checked: there is no
	// bearer token to generate.
	if cfg.token == "" && cfg.addr != "off" {
		cfg.token, err = genToken()
		if err != nil {
			return fmt.Errorf("generate token: %w", err)
		}
		// Printed once, to stderr only: never logged, never in a URL.
		fmt.Fprintf(stderr, "webbridge: no --token given; generated bearer token: %s\n", cfg.token)
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
	}, version, cfg.projectMounts, cfg.stateVolume)

	// The local profile runs its fleet as host processes (decision 4).
	// This must precede every start below — egress, reattach, the poller
	// and the scheduler — so no agent or probe sees a container runtime.
	if cfg.profile == "local" {
		fleet.SetHostProcessesOnly()
		// A project child starts with its project's environment (decision
		// 7). A bridge that cannot name its own binary leaves children
		// inheriting the bridge's environment instead.
		if projectEnv, err := localProjectEnv(); err != nil {
			slog.Default().Warn("webbridge: project children inherit the bridge's environment", "err", err)
		} else {
			fleet.SetProjectEnv(projectEnv)
		}
		// A session with no viewer and no run is ended after the idle
		// timeout (P4 D7a), swept on its own goroutine; zero never ends
		// one, and the sweep returns at once.
		fleet.SetIdleSessionTimeout(cfg.idleSessionTimeout)
		go fleet.RunIdleSweep(ctx)
	}

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

	// The local profile starts each pinned project's child on its own
	// goroutine, one after another (decision 9). The server profile pins
	// nothing, so it starts none.
	if cfg.profile == "local" {
		go fleet.StartPinned(ctx)
	}

	// Watch labelled issues on registered repos. Off by default; only
	// repos with Watch set are polled.
	fleet.StartPoller(0)
	// Run due schedules. Fleet mode only: registry mode has no scheduler.
	fleet.StartScheduler(nil)

	api := bridge.NewServer(fleet, cfg.token, cfg.publicURL, bridge.NewFactoryAPI(factoryStore, factoryController, fleet))

	// serveErr carries the first listener to stop serving. The socket and
	// the TCP listener can both exist, so it holds two, and each goroutine
	// parks its answer here rather than dropping it.
	serveErr := make(chan error, 2)

	// The local profile listens on a unix socket of the user's own: the
	// same routes as ServeHTTP without the bearer check, peer-checked
	// instead, so a client on the same account drives it without a
	// secret on the command line (decisions 5-7).
	var socketSrv *http.Server
	if cfg.profile == "local" {
		api.SetDaemonInfo(bridge.DaemonInfo{
			Version:   daemonVersion(),
			Profile:   "local",
			PID:       os.Getpid(),
			StartedAt: startedAt.UnixMilli(),
			Socket:    cfg.socket,
		})
		if cfg.oauthRefresh {
			// The refresh helper (P4 D12): `<marshal-bin> daemon refresh`
			// beside the bridge, on the run's context. run waits for it to
			// return before it returns, so the bridge never leaves a
			// helper behind.
			helper := &bridge.Helper{
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
	}

	// --addr off leaves the socket as the only listener: no TCP, no
	// previews, no token. The scheme and the TCP listener exist only
	// when one is opened.
	var srv *http.Server
	var ln net.Listener
	scheme := "http"
	if cfg.tlsCert != "" {
		scheme = "https"
	}
	if cfg.addr != "off" {
		srv = &http.Server{
			Addr:              cfg.addr,
			Handler:           api,
			ReadHeaderTimeout: 10 * time.Second,
		}
		ln, err = net.Listen("tcp", cfg.addr)
		if err != nil {
			// Stop escalates SIGTERM→SIGKILL over stopGrace, so a stubborn
			// child delays the error return slightly; correct cleanup beats
			// a faster failure here.
			if socketSrv != nil {
				_ = socketSrv.Close()
			}
			shutdownFactory()
			fleet.Close()
			return fmt.Errorf("listen %s: %w", cfg.addr, err)
		}
		fmt.Fprintf(stderr, "webbridge: listening on %s://%s (%d project(s))\n", scheme, ln.Addr(), len(ws.Projects()))
	}

	// Previews get a listener, and so an origin, of their own: a page the
	// agent serves must not share storage with the UI that holds the token.
	var previewSrv *http.Server
	if cfg.addr != "off" && cfg.previewAddr != "off" {
		pln, perr := listenPreview(cfg.addr, cfg.previewAddr)
		if perr != nil {
			slog.Default().Warn("webbridge: previews unavailable", "err", perr)
		} else {
			api.SetPreviewPort(pln.Addr().(*net.TCPAddr).Port)
			previewSrv = &http.Server{Handler: api.PreviewHandler(), ReadHeaderTimeout: 10 * time.Second}
			go func() { _ = serveHTTP(previewSrv, pln, cfg.tlsCert, cfg.tlsKey) }()
			fmt.Fprintf(stderr, "webbridge: previews on %s://%s\n", scheme, pln.Addr())
		}
	}

	// Reconcile orphaned worktrees after the server is listening, so a slow
	// prune cannot delay accepting requests.
	go fleet.ReconcileWorktrees(context.Background())

	if srv != nil {
		go func() {
			err := serveHTTP(srv, ln, cfg.tlsCert, cfg.tlsKey)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			serveErr <- err
		}()
	}

	// SIGINT/SIGTERM (or the caller's ctx) triggers shutdown: HTTP
	// first, then the child.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

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
		if srv != nil {
			_ = srv.Close()
		}
		shutdownFactory()
		fleet.Close()
		return err
	case <-sigCtx.Done():
	}
	stop() // restore default signal handling so a second Ctrl-C kills

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
	if srv != nil {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Default().Warn("webbridge: HTTP shutdown", "err", err)
		}
	}
	shutdownFactory()
	fleet.Close()
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

// serveHTTP serves srv on ln, using TLS when both a certificate and a
// key are supplied.
//
// The pair is validated in parseConfig, so reaching here with exactly
// one set is a programming error rather than a user one; this function
// treats an empty cert as "plaintext" and nothing more.
func serveHTTP(srv *http.Server, ln net.Listener, certFile, keyFile string) error {
	if certFile == "" || keyFile == "" {
		return srv.Serve(ln)
	}
	return srv.ServeTLS(ln, certFile, keyFile)
}

// listenPreview opens the preview listener: previewAddr when given, else
// the API address's host on a free port. A port different from the API's
// is what makes it a different origin.
func listenPreview(apiAddr, previewAddr string) (net.Listener, error) {
	if previewAddr == "" {
		host, _, err := net.SplitHostPort(apiAddr)
		if err != nil {
			return nil, err
		}
		previewAddr = net.JoinHostPort(host, "0")
	}
	if _, port, err := net.SplitHostPort(previewAddr); err == nil {
		if _, aport, aerr := net.SplitHostPort(apiAddr); aerr == nil && port == aport && port != "0" {
			return nil, fmt.Errorf("--preview-addr %s must differ from --addr: previews need their own origin", previewAddr)
		}
	}
	return net.Listen("tcp", previewAddr)
}
