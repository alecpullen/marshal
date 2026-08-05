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
	"syscall"
	"time"

	"marshal/web/bridge"
)

// config holds the resolved flag/env settings.
type config struct {
	addr       string
	token      string
	marshalBin string
	cwdRoot    string
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
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 0 {
		return config{}, fmt.Errorf("unknown argument %q", fs.Arg(0))
	}
	cfg := config{addr: *addr, token: *token, marshalBin: *marshalBin, cwdRoot: *cwdRoot}
	if cfg.cwdRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return config{}, fmt.Errorf("--cwd-root unset and cannot determine working directory: %w", err)
		}
		cfg.cwdRoot = cwd
	}
	return cfg, nil
}

// envOr returns the environment variable's value when set and
// non-empty, else def. An explicitly empty variable means "unset".
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
	if err := run(context.Background(), os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "webbridge: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
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

	// Build the whole stack before starting the child: Attach and
	// NewRegistry install hooks the child's read loop reads
	// unsynchronized.
	child := &bridge.Child{MarshalBin: cfg.marshalBin}
	reg := bridge.NewRegistry(child)
	reg.RootCwd = cfg.cwdRoot
	events := bridge.NewEventLog()
	bridge.Attach(events, child, reg)
	if err := child.Start(); err != nil {
		return fmt.Errorf("start marshal acp child: %w", err)
	}

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           bridge.NewServer(reg, events, cfg.token),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		// Stop escalates SIGTERM→SIGKILL over stopGrace, so a stubborn
		// child delays the error return slightly; correct cleanup beats
		// a faster failure here.
		child.Stop()
		return fmt.Errorf("listen %s: %w", cfg.addr, err)
	}
	fmt.Fprintf(stderr, "webbridge: listening on http://%s (cwd-root %s)\n", ln.Addr(), cfg.cwdRoot)

	serveErr := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
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
		child.Stop()
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
	child.Stop()
	return nil
}
