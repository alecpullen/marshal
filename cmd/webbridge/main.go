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
	addr   string
	// previewAddr is where previews are served, on an origin of their own.
	// Empty means the --addr host on an automatic port; "off" disables.
	previewAddr string
	token       string
	// tokenFile is where the bearer token is read from, and written to
	// when the file does not exist yet. It is how a local daemon hands
	// the Studio its token without putting the secret on a command line
	// or asking the user to paste it.
	tokenFile   string
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
	// and no run lives on in the daemon (P4 D7a); zero never
	// ends one.
	idleSessionTimeout time.Duration
	// oauthRefresh runs `<marshal-bin> daemon refresh` beside the bridge
	// (P4 D12). Off unless asked.
	oauthRefresh bool
	// relayJoins names the hosts that may join this bridge as a relay, each
	// with the file holding that name's join secret (P8 D11). A bridge with
	// none relays nothing.
	relayJoins []relayJoin
	// join is the relay this bridge dials out to, joinName the name it
	// joins under, joinTokenFile the file holding that name's secret, and
	// joinCAFile an optional certificate to trust when dialling it (P8
	// D12). Empty join means this bridge joins nothing.
	join          string
	joinName      string
	joinTokenFile string
	joinCAFile    string
}

// relayJoin is one host a relay names and the file holding its join secret
// (P8 D11). The command reads the file and hands the bridge the value
// (decision 2); the bridge never opens a secret file.
type relayJoin struct {
	name string
	path string
}

// parseConfig resolves flags over environment variables over defaults.
// Flag values win; each env var is consulted only when its flag was
// left at the default.
func parseConfig(args []string, stderr io.Writer) (config, error) {
	fs := flag.NewFlagSet("webbridge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", envOr("WEBBRIDGE_PROFILE", ""), "retired: local is accepted and ignored")
	socket := fs.String("socket", envOr("WEBBRIDGE_SOCKET", ""), "unix socket to serve on")
	addr := fs.String("addr", envOr("WEBBRIDGE_ADDR", "127.0.0.1:7700"), "listen address")
	previewAddr := fs.String("preview-addr", envOr("WEBBRIDGE_PREVIEW_ADDR", ""), "listen address for previews, served from a separate origin (default: the --addr host on a free port; \"off\" disables)")
	token := fs.String("token", envOr("WEBBRIDGE_TOKEN", ""), "bearer token for /api routes (generated when empty)")
	tokenFile := fs.String("token-file", envOr("WEBBRIDGE_TOKEN_FILE", ""), "file to read the bearer token from, and create it 0600 when absent")
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
	// The relay's side: each NAME=PATH names a host that may join this
	// bridge and the file holding that name's secret (P8 D11). The flag is
	// repeatable; the variable carries the same pairs comma-separated, as
	// one value, when the flag is not given.
	var relayJoinArgs stringList
	fs.Func("relay-join", "host NAME=PATH that may join this bridge as a relay, its secret in PATH (repeatable; WEBBRIDGE_RELAY_JOIN carries pairs comma-separated)", func(v string) error {
		relayJoinArgs = append(relayJoinArgs, v)
		return nil
	})
	// The joining side: the relay this bridge dials out to (P8 D12).
	join := fs.String("join", envOr("WEBBRIDGE_JOIN", ""), "relay URL to join: https, or http on a loopback host")
	joinName := fs.String("join-name", envOr("WEBBRIDGE_JOIN_NAME", ""), "the name this bridge joins its relay under")
	joinTokenFile := fs.String("join-token-file", envOr("WEBBRIDGE_JOIN_TOKEN_FILE", ""), "file holding the relay's join secret for --join-name")
	joinCAFile := fs.String("join-ca-file", envOr("WEBBRIDGE_JOIN_CA_FILE", ""), "extra CA certificate to trust when dialling the relay")
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
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 0 {
		return config{}, fmt.Errorf("unknown argument %q", fs.Arg(0))
	}
	cfg := config{socket: *socket, addr: *addr, previewAddr: *previewAddr, token: *token, tokenFile: *tokenFile, marshalBin: *marshalBin, cwdRoot: *cwdRoot, projects: projects, workspace: *workspace, stateDir: *stateDir, stateVolume: *stateVolume, agentEnv: agentEnv, tlsCert: *tlsCert, tlsKey: *tlsKey, maxConcurrent: *maxConcurrent, maxDiskMB: *maxDiskMB, maxCloneMB: *maxCloneMB,
		secrets: *secrets, secretsKeyFile: *secretsKeyFile, baoAddr: *baoAddr, baoMount: *baoMount,
		baoRoleIDFile: *baoRoleIDFile, baoSecretIDFile: *baoSecretIDFile, baoCAFile: *baoCAFile, egress: *egress, publicURL: *publicURL,
		idleSessionTimeout: *idleSessionTimeout, oauthRefresh: *oauthRefresh,
		join: *join, joinName: *joinName, joinTokenFile: *joinTokenFile, joinCAFile: *joinCAFile}
	pm, err := parseProjectMounts(projectMounts)
	if err != nil {
		return config{}, err
	}
	cfg.projectMounts = pm
	given := givenFrom(fs)
	if err := applyDaemonDefaults(&cfg, given, *profile); err != nil {
		return config{}, err
	}
	if err := applyRelay(&cfg, given, relayJoinArgs); err != nil {
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

	// A token belongs to the TCP listener, or to the join a bridge makes: D12
	// names either as a joining bridge's bearer token, because relayed
	// requests are checked with it. Without a join, --addr off has no use for
	// one, and refusing beats accepting it and never using it.
	if cfg.addr == "off" && cfg.join == "" && given.set("token", "WEBBRIDGE_TOKEN", cfg.token) {
		return fmt.Errorf("--token is for the TCP listener or a join, and neither is set")
	}
	if cfg.addr == "off" && cfg.join == "" && given.set("token-file", "WEBBRIDGE_TOKEN_FILE", cfg.tokenFile) {
		return fmt.Errorf("--token-file is for the TCP listener or a join, and neither is set")
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

// applyRelay validates the relay's flags and the join's, and resolves the
// --relay-join pairs (P8 D11, D12). The refusals sit beside
// applyDaemonDefaults', because the daemon can both relay and join.
func applyRelay(cfg *config, given givenSettings, pairs []string) error {
	// The flag is repeatable; the variable carries the same pairs
	// comma-separated, as one value, when the flag is not given.
	if len(pairs) == 0 {
		if env := os.Getenv("WEBBRIDGE_RELAY_JOIN"); env != "" {
			pairs = strings.Split(env, ",")
		}
	}
	seen := make(map[string]bool, len(pairs))
	for _, pair := range pairs {
		name, path, ok := strings.Cut(pair, "=")
		if !ok || name == "" || path == "" {
			return fmt.Errorf("--relay-join %q: expected NAME=PATH", pair)
		}
		if seen[name] {
			return fmt.Errorf("--relay-join %s: the name is given twice", name)
		}
		seen[name] = true
		cfg.relayJoins = append(cfg.relayJoins, relayJoin{name: name, path: path})
	}

	// A join needs the url it dials, the name it joins under and the file
	// holding that name's secret. Every one is required: a join that
	// cannot present all three is refused before it dials anything.
	joined := given.set("join", "WEBBRIDGE_JOIN", cfg.join)
	if joined {
		if !given.set("join-name", "WEBBRIDGE_JOIN_NAME", cfg.joinName) {
			return fmt.Errorf("--join needs --join-name")
		}
		if !given.set("join-token-file", "WEBBRIDGE_JOIN_TOKEN_FILE", cfg.joinTokenFile) {
			return fmt.Errorf("--join needs --join-token-file")
		}
		// Relayed requests are checked with this bridge's own bearer token
		// (D15), so a joining bridge without one would join and then refuse
		// everything that reached it.
		if cfg.token == "" && cfg.tokenFile == "" {
			return fmt.Errorf("--join needs a bearer token: give --token or --token-file")
		}
		return nil
	}
	for _, f := range []struct{ flag, env, value string }{
		{"join-name", "WEBBRIDGE_JOIN_NAME", cfg.joinName},
		{"join-token-file", "WEBBRIDGE_JOIN_TOKEN_FILE", cfg.joinTokenFile},
		{"join-ca-file", "WEBBRIDGE_JOIN_CA_FILE", cfg.joinCAFile},
	} {
		if given.set(f.flag, f.env, f.value) {
			return fmt.Errorf("--%s is for --join", f.flag)
		}
	}
	return nil
}

// readSecretFile reads a join secret from path. A file group or other can
// read is refused, naming it: the file is a credential, and this is the one
// place the command touches one (decision 2). The bridge is handed the
// value and never opens the file.
func readSecretFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("join secret %s: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("join secret %s has mode %o; it must be 0600", path, perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("join secret %s: %w", path, err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", fmt.Errorf("join secret %s is empty", path)
	}
	return secret, nil
}

// readRelaySecrets reads each --relay-join secret file, keyed by the name
// that may join with it (P8 D11).
func readRelaySecrets(joins []relayJoin) (map[string]string, error) {
	secrets := make(map[string]string, len(joins))
	for _, join := range joins {
		secret, err := readSecretFile(join.path)
		if err != nil {
			return nil, err
		}
		secrets[join.name] = secret
	}
	return secrets, nil
}

// buildJoiner builds the joiner this bridge dials out with (P8 D12). The join
// secret is read here, from the file the command names, and the bridge is
// handed the value (decision 2): bridge never opens a secret file.
func buildJoiner(cfg config, api *bridge.Server) (*bridge.Joiner, error) {
	secret, err := readSecretFile(cfg.joinTokenFile)
	if err != nil {
		return nil, err
	}
	return bridge.NewJoiner(bridge.JoinConfig{
		URL:    cfg.join,
		Name:   cfg.joinName,
		Secret: secret,
		CAFile: cfg.joinCAFile,
	}, api.RelayedHandler())
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

// genToken returns a random 128-bit bearer token, hex-encoded.
func genToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// readOrCreateToken returns the token held in path, creating the file with a
// fresh token when it is missing or empty.
//
// This is how the daemon hands the Studio its token: the daemon reads
// or mints the file once at startup and the browser is pointed at a URL
// carrying the same value, so the secret is never typed, never on a command
// line a process list would show, and never in the log. The file is written
// 0600 because it is a credential: anything that can read it can drive the
// daemon.
func readOrCreateToken(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(data)); tok != "" {
			return tok, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	tok, err := genToken()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return tok, nil
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
	// --addr off means the unix socket is the only listener, and the
	// socket is peer-checked rather than token-checked: there is no
	// bearer token to generate.
	//
	// --token-file is how the daemon hands the Studio its token.
	// It is read when present and minted when absent, so a restarted
	// daemon keeps one token and an open Studio tab keeps working. The
	// token stays out of the log either way. A joining bridge reads or
	// mints it too, even under --addr off: relayed requests are checked
	// with this token, so a join without one would refuse everything that
	// reached it (D12, D15).
	if cfg.token == "" && cfg.tokenFile != "" && (cfg.addr != "off" || cfg.join != "") {
		cfg.token, err = readOrCreateToken(cfg.tokenFile)
		if err != nil {
			return err
		}
	}
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

	api := bridge.NewServer(fleet, cfg.token, cfg.publicURL, bridge.NewFactoryAPI(factoryStore, factoryController, fleet))

	// A bridge given --relay-join relays the hosts that join it (D11). Each
	// secret is read here and handed to the bridge as a value (decision 2):
	// a file group or other can read stops the start, naming the file. A
	// bridge given none relays nothing, and its own API is unchanged.
	if len(cfg.relayJoins) > 0 {
		secrets, err := readRelaySecrets(cfg.relayJoins)
		if err != nil {
			shutdownFactory()
			fleet.Close()
			return err
		}
		relay, err := bridge.NewHostRelay(secrets)
		if err != nil {
			shutdownFactory()
			fleet.Close()
			return err
		}
		api.SetHostRelay(relay)
	}

	// A joining bridge dials out to its relay and serves the connections the
	// relay reverses onto it (D12, D13). It is built and set here, before any
	// listener serves, so the per-request read in GET /api/daemon cannot race
	// the field; only Run waits for the signal context below.
	var joiner *bridge.Joiner
	if cfg.join != "" {
		joiner, err = buildJoiner(cfg, api)
		if err != nil {
			shutdownFactory()
			fleet.Close()
			return err
		}
		api.SetJoiner(joiner)
	}

	// serveErr carries the first listener to stop serving. The socket and
	// the TCP listener can both exist, so it holds two, and each goroutine
	// parks its answer here rather than dropping it.
	serveErr := make(chan error, 2)

	// The Studio listener is bound before the socket starts serving, so
	// GET /api/daemon can report what the bind did. A port that cannot be
	// bound is a failure the launcher can name, not a page that silently
	// fails to load.
	//
	// --addr off leaves the socket as the only listener: no TCP, no
	// previews, no token. The scheme and the TCP listener exist only
	// when one is opened.
	var srv *http.Server
	var ln net.Listener
	scheme := "http"
	if cfg.tlsCert != "" {
		scheme = "https"
	}
	var studioAddr, studioErr string
	if cfg.addr != "off" {
		ln, err = net.Listen("tcp", cfg.addr)
		if err != nil {
			// The socket is the daemon's real interface: the TUI,
			// `marshal fleet` and every session live there. A busy Studio
			// port costs the browser tab, not the daemon, so the socket
			// carries on — but the failure is logged at error level and
			// recorded for /api/daemon, so `marshal studio` repeats the
			// real cause instead of "nothing is serving".
			studioErr = err.Error()
			slog.Default().Error("webbridge: studio listener unavailable", "addr", cfg.addr, "err", err)
			ln = nil
		}
		if ln != nil {
			studioAddr = ln.Addr().String()
			srv = &http.Server{
				Addr:              cfg.addr,
				Handler:           api,
				ReadHeaderTimeout: 10 * time.Second,
			}
			fmt.Fprintf(stderr, "webbridge: listening on %s://%s (%d project(s))\n", scheme, studioAddr, len(ws.Projects()))
		}
	}

	// The daemon listens on a unix socket of the user's own: the same routes
	// as ServeHTTP without the bearer check, peer-checked instead, so a client
	// on the same account drives it without a secret on the command line
	// (decisions 5-7).
	var socketSrv *http.Server
	api.SetDaemonInfo(bridge.DaemonInfo{
		Version:     daemonVersion(),
		Profile:     "local",
		PID:         os.Getpid(),
		StartedAt:   startedAt.UnixMilli(),
		Socket:      cfg.socket,
		StudioAddr:  studioAddr,
		StudioError: studioErr,
	})
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

	// Previews get a listener, and so an origin, of their own: a page the
	// agent serves must not share storage with the UI that holds the token.
	// They ride with the Studio listener: without it there is no page to
	// show them in, so a failed bind above opens none.
	var previewSrv *http.Server
	if srv != nil && cfg.previewAddr != "off" {
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
		if srv != nil {
			if err := srv.Shutdown(shutdownCtx); err != nil {
				slog.Default().Warn("webbridge: HTTP shutdown", "err", err)
			}
		}
		shutdownFactory()
		fleet.Close()
	})

	// A joining bridge dials out to its relay and serves the connections the
	// relay reverses onto it (D12, D13). The joiner itself was built and set
	// before the listeners started; here it only runs, on sigCtx, so a
	// shutdown stops it with the listeners and run waits for it before it
	// returns.
	if joiner != nil {
		joined := make(chan struct{})
		go func() {
			defer close(joined)
			_ = joiner.Run(sigCtx)
		}()
		// stop is idempotent, so cancelling here as well as in the defer
		// above is what lets run wait for the joiner on every path out.
		defer func() {
			stop()
			<-joined
		}()
	}

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
