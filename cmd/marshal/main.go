package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"marshal/internal/acp"
	"marshal/internal/app"
	"marshal/internal/trust"
)

var appRunner = app.Run
var acpRunner = acp.Run
var historyRunner = runHistory
var calibrateRunner = runCalibrateTokens
var pluginRunner = runPlugin

func main() {
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	args = fs.Args()

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
		return acpRunner(ctx, stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "plugin" {
		return pluginRunner(ctx, args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 {
		return fmt.Errorf("unknown argument %q", args[0])
	}
	return appRunner(ctx, stdout)
}

// recordPermanentTrust writes a permanent trust record for the current working
// directory so the next normal startup skips the trust prompt.
func recordPermanentTrust() error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find working directory: %w", err)
	}
	abs, err := filepath.Abs(wd)
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	store := trust.NewStore(filepath.Join(home, ".local", "share", "marshal"))
	hash, err := trust.ConfigHashFor(abs)
	if err != nil {
		return fmt.Errorf("hash project config: %w", err)
	}
	return store.SetTrust(abs, true, hash)
}
