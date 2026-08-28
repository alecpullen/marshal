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
	"strings"
	"syscall"
	"time"

	"marshal/web/bridge"
)

// version is stamped at release time via -ldflags -X (see .goreleaser.yaml).
// A plain `go build` leaves it empty and versionString reports "dev".
var version = ""

// versionString renders the one-line version banner printed by --version.
func versionString() string {
	v := version
	if v == "" {
		v = "dev"
	}
	return fmt.Sprintf("webbridge %s", v)
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
	addr       string
	token      string
	marshalBin string
	cwdRoot    string
	projects   []string
	workspace  string
	stateDir   string
	agentEnv   stringList
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
}

// parseConfig resolves flags over environment variables over defaults.
// Flag values win; each env var is consulted only when its flag was
// left at the default.
func parseConfig(args []string, stderr io.Writer) (config, error) {
	fs := flag.NewFlagSet("webbridge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", envOr("WEBBRIDGE_ADDR", "127.0.0.1:7700"), "listen address")
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
	var agentEnv stringList
	fs.Var(&agentEnv, "agent-env", "KEY=VALUE handed to every agent container (repeatable)")
	maxConcurrent := fs.Int("max-concurrent", 0, "max concurrent agents (0 = default 4)")
	maxDiskMB := fs.Int64("max-disk-mb", 0, "max state directory size in MB (0 = unlimited)")
	maxCloneMB := fs.Int64("max-clone-mb", 0, "max clone size in MB (0 = unlimited)")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 0 {
		return config{}, fmt.Errorf("unknown argument %q", fs.Arg(0))
	}
	cfg := config{addr: *addr, token: *token, marshalBin: *marshalBin, cwdRoot: *cwdRoot, projects: projects, workspace: *workspace, stateDir: *stateDir, agentEnv: agentEnv, tlsCert: *tlsCert, tlsKey: *tlsKey, maxConcurrent: *maxConcurrent, maxDiskMB: *maxDiskMB, maxCloneMB: *maxCloneMB}
	pm, err := parseProjectMounts(projectMounts)
	if err != nil {
		return config{}, err
	}
	cfg.projectMounts = pm
	// Half-configured TLS fails loudly. Serving plaintext because one
	// flag name was mistyped is the failure nobody notices.
	if (cfg.tlsCert == "") != (cfg.tlsKey == "") {
		return config{}, fmt.Errorf("--tls-cert and --tls-key must be given together")
	}
	if cfg.workspace == "" {
		p, err := bridge.DefaultWorkspacePath()
		if err != nil {
			return config{}, err
		}
		cfg.workspace = p
	}
	if cfg.cwdRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return config{}, fmt.Errorf("--cwd-root unset and cannot determine working directory: %w", err)
		}
		cfg.cwdRoot = cwd
	}
	return cfg, nil
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
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "webbridge: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
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

	cfg, err := parseConfig(args, stderr)
	if err != nil {
		return err
	}
	if cfg.token == "" {
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
	}, version, cfg.projectMounts)

	if errs := fleet.ReattachAll(ctx); len(errs) > 0 {
		for _, err := range errs {
			slog.Default().Warn("webbridge: reattach agent", "err", err)
		}
	}

	// Watch labelled issues on registered repos. Off by default; only
	// repos with Watch set are polled.
	fleet.StartPoller(0)

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           bridge.NewServer(fleet, cfg.token),
		ReadHeaderTimeout: 10 * time.Second,
	}
	scheme := "http"
	if cfg.tlsCert != "" {
		scheme = "https"
	}
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		// Stop escalates SIGTERM→SIGKILL over stopGrace, so a stubborn
		// child delays the error return slightly; correct cleanup beats
		// a faster failure here.
		fleet.Close()
		return fmt.Errorf("listen %s: %w", cfg.addr, err)
	}
	fmt.Fprintf(stderr, "webbridge: listening on %s://%s (%d project(s))\n", scheme, ln.Addr(), len(ws.Projects()))

	// Reconcile orphaned worktrees after the server is listening, so a slow
	// prune cannot delay accepting requests.
	go fleet.ReconcileWorktrees(context.Background())

	serveErr := make(chan error, 1)
	go func() {
		err := serveHTTP(srv, ln, cfg.tlsCert, cfg.tlsKey)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

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
		fleet.Close()
		return err
	case <-sigCtx.Done():
	}
	stop() // restore default signal handling so a second Ctrl-C kills

	slog.Default().Info("webbridge: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Default().Warn("webbridge: HTTP shutdown", "err", err)
	}
	fleet.Close()
	return nil
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
