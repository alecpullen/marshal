package capture

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// jxaFrontWindowID is a single-line JXA program that returns the CGWindowID of
// the frontmost application's layer-0 window, or an empty string when that
// application has no such window.
const jxaFrontWindowID = `ObjC.import('Cocoa'); ObjC.import('CoreGraphics'); var app = $.NSWorkspace.sharedWorkspace.frontmostApplication; var list = ObjC.deepUnwrap($.CGWindowListCopyWindowInfo($.kCGWindowListOptionOnScreenOnly, $.kCGNullWindowID)); var pid = app.processIdentifier; var win = list.find(w => w.kCGWindowLayer == 0 && w.kCGWindowOwnerPID == pid); win ? String(win.kCGWindowNumber) : "";`

// darwinBackend captures through the screencapture tool: the whole screen, or
// one window by the CGWindowID osascript reads from the frontmost application.
type darwinBackend struct {
	run Runner
}

func (b *darwinBackend) Capture(ctx context.Context, t Target) (Image, error) {
	switch t {
	case TargetScreen:
		png, err := b.shoot(ctx, "-x")
		if err != nil {
			return Image{}, err
		}
		return Image{PNG: png, Label: "entire screen", Backend: "darwin"}, nil
	case TargetActiveWindow:
		id, err := b.activeWindowID(ctx)
		if err != nil {
			return Image{}, err
		}
		png, err := b.shoot(ctx, "-x", "-l"+id)
		if err != nil {
			return Image{}, err
		}
		return Image{PNG: png, Label: "window id " + id, Backend: "darwin"}, nil
	default:
		return Image{}, fmt.Errorf("capture: unknown target %q", t)
	}
}

// shoot runs screencapture into a temp file and reads it back. The path is
// removed on every return, success or not: screencapture writes a file where
// the caller only ever wants the bytes.
func (b *darwinBackend) shoot(ctx context.Context, args ...string) ([]byte, error) {
	f, err := os.CreateTemp("", "marshal-capture-*.png")
	if err != nil {
		return nil, fmt.Errorf("capture: creating a temp file: %w", err)
	}
	path := f.Name()
	f.Close()
	// screencapture writes the file itself, so it must not already exist.
	os.Remove(path)
	defer os.Remove(path)

	if _, err := b.run(ctx, "screencapture", append(args, "-t", "png", path)...); err != nil {
		return nil, helperError("screencapture", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// screencapture exited cleanly without producing a file: TCC
			// declined the capture instead of failing the command.
			return nil, tccError()
		}
		return nil, fmt.Errorf("capture: reading screencapture output: %w", err)
	}
	if len(data) == 0 {
		// A zero-byte file is the same decline as no file at all: the bytes
		// are what the caller wants, and there are none.
		return nil, tccError()
	}
	return data, nil
}

// activeWindowID asks the frontmost application for its layer-0 CGWindowID,
// the handle `screencapture -l` takes.
func (b *darwinBackend) activeWindowID(ctx context.Context) (string, error) {
	out, err := b.run(ctx, "osascript", "-l", "JavaScript", "-e", jxaFrontWindowID)
	if err != nil {
		return "", helperError("osascript", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", ErrNoFocused
	}
	return id, nil
}

// isTCCDenial reports whether a helper's stderr reads as macOS withholding
// Screen Recording from this process.
func isTCCDenial(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "not authorized") || strings.Contains(lower, "screen recording")
}

func tccError() error {
	return fmt.Errorf("%w: grant Screen Recording to the app running marshal "+
		"in System Settings → Privacy & Security → Screen Recording", ErrPermission)
}
