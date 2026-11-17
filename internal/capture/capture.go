// Package capture grabs a screenshot of the active window or of the whole
// screen from the display session the agent is running in. It is a leaf
// package: it reaches the compositor only through a Runner, so the tool layer
// can decide when a capture is allowed.
package capture

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type Target string

const (
	TargetActiveWindow Target = "active_window"
	TargetScreen       Target = "screen"
)

// Runner runs one helper command. Stdout is the result; stderr folds into the
// error so a missing grim or a declined TCC prompt reads as itself.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Env is the ambient session a backend detects from. Tests pass it explicitly;
// production passes runtime.GOOS and os.Getenv.
type Env struct {
	GOOS   string
	Getenv func(string) string
}

type Image struct {
	PNG     []byte // always PNG
	Label   string // "window: kitty — ~/src/marshal" | "entire screen"
	Backend string // "sway" | "hyprland" | "darwin"
}

type Backend interface {
	Capture(ctx context.Context, t Target) (Image, error)
}

var (
	ErrNoBackend     = errors.New("capture: no supported display session")
	ErrPermission    = errors.New("capture: screen recording is not permitted")
	ErrHelperMissing = errors.New("capture: a required helper command is missing")
	ErrNoFocused     = errors.New("capture: no focused window")
)

// defaultRunner runs one helper command and folds its stderr into the error, so
// a missing binary surfaces as an *exec.Error the caller can map.
func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Detect picks a backend for this session, or says why none can run.
func Detect(env Env, forced string) (Backend, error) {
	switch forced {
	case "sway":
		return &wlrootsBackend{kind: "sway", run: defaultRunner, env: env}, nil
	case "hyprland":
		return &wlrootsBackend{kind: "hyprland", run: defaultRunner, env: env}, nil
	case "":
	default:
		return nil, fmt.Errorf("%w: unknown backend %q", ErrNoBackend, forced)
	}
	if env.Getenv("SWAYSOCK") != "" {
		return &wlrootsBackend{kind: "sway", run: defaultRunner, env: env}, nil
	}
	if env.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" {
		return &wlrootsBackend{kind: "hyprland", run: defaultRunner, env: env}, nil
	}
	return nil, fmt.Errorf("%w: looked for SWAYSOCK and HYPRLAND_INSTANCE_SIGNATURE", ErrNoBackend)
}
