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

// grim runs grim with extra args and folds a missing-binary failure into
// ErrHelperMissing.
func (b *wlrootsBackend) grim(ctx context.Context, args ...string) ([]byte, error) {
	out, err := b.run(ctx, "grim", append(args, "-")...)
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return nil, fmt.Errorf("%w: grim (install grim, or set [capture] enabled = false): %v", ErrHelperMissing, err)
		}
		return nil, err
	}
	return out, nil
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
		return windowRect{}, err
	}
	var tree swayTree
	if err := json.Unmarshal(out, &tree); err != nil {
		return windowRect{}, fmt.Errorf("capture: decoding sway tree: %w", err)
	}
	if win, ok := walkSway(tree.Nodes, 0, 0); ok {
		return win, nil
	}
	if win, ok := walkSway(tree.FloatingNodes, 0, 0); ok {
		return win, nil
	}
	return windowRect{}, ErrNoFocused
}

// walkSway descends the tree accumulating the parent containers' origin, and
// returns the first focused view that actually carries a window.
func walkSway(nodes []swayNode, ox, oy int) (windowRect, bool) {
	for _, n := range nodes {
		x, y := ox+n.Rect.X, oy+n.Rect.Y
		if n.Focused && n.Window != 0 {
			return windowRect{
				x:     x,
				y:     y,
				w:     n.Rect.Width,
				h:     n.Rect.Height,
				class: n.AppID,
				title: n.Name,
			}, true
		}
		if win, ok := walkSway(n.Nodes, x, y); ok {
			return win, true
		}
		if win, ok := walkSway(n.FloatingNodes, x, y); ok {
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
		return windowRect{}, err
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
