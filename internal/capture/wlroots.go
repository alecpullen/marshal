package capture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// wlrootsBackend captures through grim, taking the geometry of the focused
// window from the compositor: swaymsg for sway, hyprctl for Hyprland.
type wlrootsBackend struct {
	kind string // "sway" | "hyprland"
	run  Runner
	env  Env
}

func (b *wlrootsBackend) Capture(ctx context.Context, t Target) (Image, error) {
	switch t {
	case TargetScreen:
		png, err := b.grim(ctx)
		if err != nil {
			return Image{}, err
		}
		return Image{PNG: png, Label: "entire screen", Backend: b.kind}, nil
	case TargetActiveWindow:
		win, err := b.activeWindow(ctx)
		if err != nil {
			return Image{}, err
		}
		png, err := b.grim(ctx, "-g", fmt.Sprintf("%d,%d %dx%d", win.x, win.y, win.w, win.h))
		if err != nil {
			return Image{}, err
		}
		return Image{
			PNG:     png,
			Label:   fmt.Sprintf("window: %s — %s", win.class, win.title),
			Backend: b.kind,
		}, nil
	default:
		return Image{}, fmt.Errorf("capture: unknown target %q", t)
	}
}

// grim runs grim with extra args and folds a failed call through helperError.
func (b *wlrootsBackend) grim(ctx context.Context, args ...string) ([]byte, error) {
	out, err := b.run(ctx, "grim", append(args, "-")...)
	if err != nil {
		return nil, helperError("grim", err)
	}
	return out, nil
}

// helperError folds a failed helper into the package's error space: a binary
// that is not installed is ErrHelperMissing, macOS withholding Screen Recording
// or a compositor refusing the capture is ErrPermission, and anything else
// keeps the helper's own failure under its name.
func helperError(helper string, err error) error {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return fmt.Errorf("%w: %s%s: %v", ErrHelperMissing, helper, helperHint(helper), err)
	}
	if isTCCDenial(err.Error()) {
		return tccError()
	}
	if isCaptureRefusal(err.Error()) {
		return fmt.Errorf("%w: %s refused the capture: %v", ErrPermission, helper, err)
	}
	return fmt.Errorf("capture: %s: %w", helper, err)
}

// helperHint is the extra advice a missing helper needs, if any.
func helperHint(helper string) string {
	if helper == "grim" {
		return " (install grim, or set [capture] enabled = false)"
	}
	return ""
}

// isCaptureRefusal reports whether a helper's error reads as a refusal to
// capture rather than a failure to run it. The check stays narrow: the text is
// a compositor's prose, not a contract.
func isCaptureRefusal(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "permission") || strings.Contains(lower, "denied")
}

type windowRect struct {
	x, y, w, h int
	class      string
	title      string
}

func (b *wlrootsBackend) activeWindow(ctx context.Context) (windowRect, error) {
	if b.kind == "hyprland" {
		return b.hyprlandActiveWindow(ctx)
	}
	return b.swayActiveWindow(ctx)
}

// swayTree is the slice of `swaymsg -t get_tree` this package reads.
type swayTree struct {
	Nodes         []swayNode `json:"nodes"`
	FloatingNodes []swayNode `json:"floating_nodes"`
}

type swayNode struct {
	Name          string     `json:"name"`
	AppID         string     `json:"app_id"`
	Window        int        `json:"window"`
	PID           int        `json:"pid"`
	Focused       bool       `json:"focused"`
	Rect          swayRect   `json:"rect"`
	Nodes         []swayNode `json:"nodes"`
	FloatingNodes []swayNode `json:"floating_nodes"`
}

type swayRect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (b *wlrootsBackend) swayActiveWindow(ctx context.Context) (windowRect, error) {
	out, err := b.run(ctx, "swaymsg", "-t", "get_tree", "-r")
	if err != nil {
		return windowRect{}, helperError("swaymsg", err)
	}
	var tree swayTree
	if err := json.Unmarshal(out, &tree); err != nil {
		return windowRect{}, fmt.Errorf("capture: decoding sway tree: %w", err)
	}
	if win, ok := walkSway(tree.Nodes); ok {
		return win, nil
	}
	if win, ok := walkSway(tree.FloatingNodes); ok {
		return win, nil
	}
	return windowRect{}, ErrNoFocused
}

// walkSway returns the first focused view below nodes. Sway reports every
// node's rect in absolute layout coordinates — a workspace or a container
// repeats the coordinates of its children, and a node already carries its
// output's origin — so the focused node's rect is the window's geometry as it
// stands, and no ancestor origin is added.
func walkSway(nodes []swayNode) (windowRect, bool) {
	for _, n := range nodes {
		// pid is set for every view and null for outputs, workspaces and
		// containers; window is an integer only for Xwayland views, so
		// requiring it alone would miss every native Wayland window.
		if n.Focused && (n.Window != 0 || n.PID != 0) {
			return windowRect{
				x:     n.Rect.X,
				y:     n.Rect.Y,
				w:     n.Rect.Width,
				h:     n.Rect.Height,
				class: n.AppID,
				title: n.Name,
			}, true
		}
		if win, ok := walkSway(n.Nodes); ok {
			return win, true
		}
		if win, ok := walkSway(n.FloatingNodes); ok {
			return win, true
		}
	}
	return windowRect{}, false
}

// hyprWindow is the slice of `hyprctl -j activewindow` this package reads.
type hyprWindow struct {
	Class string `json:"class"`
	Title string `json:"title"`
	At    []int  `json:"at"`
	Size  []int  `json:"size"`
}

func (b *wlrootsBackend) hyprlandActiveWindow(ctx context.Context) (windowRect, error) {
	out, err := b.run(ctx, "hyprctl", "-j", "activewindow")
	if err != nil {
		return windowRect{}, helperError("hyprctl", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return windowRect{}, ErrNoFocused
	}
	var win hyprWindow
	if err := json.Unmarshal(out, &win); err != nil {
		return windowRect{}, fmt.Errorf("capture: decoding hyprctl output: %w", err)
	}
	if len(win.Size) < 2 || win.Size[0] <= 0 || win.Size[1] <= 0 {
		return windowRect{}, ErrNoFocused
	}
	x, y := 0, 0
	if len(win.At) >= 2 {
		x, y = win.At[0], win.At[1]
	}
	return windowRect{
		x:     x,
		y:     y,
		w:     win.Size[0],
		h:     win.Size[1],
		class: win.Class,
		title: win.Title,
	}, nil
}
