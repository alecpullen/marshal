package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"marshal/internal/acp"
	"marshal/internal/app"
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
	if len(args) > 0 && args[0] == "history" {
		return historyRunner(ctx, args[1:], stdout)
	}
	if len(args) > 0 && args[0] == "calibrate-tokens" {
		return calibrateRunner(ctx, args[1:], stdout)
	}
	if len(args) > 0 && args[0] == "acp" {
		return acpRunner(ctx, stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "plugin" {
		return pluginRunner(ctx, args[1:], stdin, stdout, stderr)
	}
	return appRunner(ctx, stdout)
}
