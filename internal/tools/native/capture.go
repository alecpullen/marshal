package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"marshal/internal/capture"
	"marshal/internal/imagepayload"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

type captureArgs struct {
	Target string `json:"target"`
	Label  string `json:"label"`
}

// captureTool photographs the display session this process runs in and hands
// the image back through the store, so the model can look at what the user is
// pointing at instead of asking them to describe it.
func (t *toolSet) captureTool() registry.Tool {
	tool := registry.Tool{
		Name:            "image.capture",
		Description:     "Capture the desktop and return the image so you can look at it. target=active_window (default) captures the focused window; target=screen captures every display. Requires approval.",
		Schema:          json.RawMessage(`{"type":"object","properties":{"target":{"type":"string","enum":["active_window","screen"],"default":"active_window","description":"active_window captures the focused window; screen captures every display"},"label":{"type":"string","description":"Short caption to identify this capture later"}},"additionalProperties":false}`),
		Risk:            registry.RiskReadOnly,
		WorkspaceEffect: registry.WorkspaceEffectSessionOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[captureArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Target == "" {
			args.Target = string(capture.TargetActiveWindow)
		}
		target := capture.Target(args.Target)
		// The schema's enum already refuses anything else; the handler repeats
		// the check because a hand-built Tool never went through Register and
		// so has no compiled validator. A bad target is an error, never a
		// silent fall back to the default: the caller asked for something
		// specific.
		switch target {
		case capture.TargetActiveWindow, capture.TargetScreen:
		default:
			return registry.ToolResult{}, fmt.Errorf("image.capture: unknown target %q: use active_window or screen", args.Target)
		}
		if t.images == nil {
			return registry.ToolResult{}, errors.New("image.capture is unavailable: this session has no image store")
		}
		backend, err := t.captureBackend()
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("image.capture: %w", err)
		}
		cctx := ctx
		if t.capture.Timeout > 0 {
			var cancel context.CancelFunc
			cctx, cancel = context.WithTimeout(ctx, t.capture.Timeout)
			defer cancel()
		}
		img, err := backend.Capture(cctx, target)
		if err != nil {
			// The session this backend detected may have gone away — a
			// compositor restarted, a permission was revoked. Dropping it
			// makes the next call re-detect instead of failing forever on a
			// stale choice.
			t.dropCaptureBackend()
			return registry.ToolResult{}, fmt.Errorf("image.capture: %w", err)
		}
		label := args.Label
		if label == "" {
			// The summary line leads with the label; the backend's own name for
			// what it saw is the honest fallback.
			label = img.Label
		}
		ref, err := t.images.Put(ctx, bytes.NewReader(img.PNG), imagepayload.Options{
			Label:  label,
			Format: "png",
		})
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("capture: store image: %w", err)
		}
		label = ref.Label
		return registry.ToolResult{
			Summary: fmt.Sprintf("image.capture: %s → %d×%d %s (%s) hash %s",
				label, ref.Width, ref.Height, shortMIME(ref.MIME), humanBytes(ref.Bytes), ref.Hash),
			Content: fmt.Sprintf(`{"target":%q,"backend":%q,"width":%d,"height":%d,"bytes":%d}`,
				args.Target, img.Backend, ref.Width, ref.Height, ref.Bytes),
			Images: []schema.ImageRef{ref},
		}, nil
	}
	return tool
}

// captureBackend returns the detected backend, detecting on first use. The
// detection is cached because a backend is chosen per session, not per call.
func (t *toolSet) captureBackend() (capture.Backend, error) {
	t.captureMu.Lock()
	defer t.captureMu.Unlock()
	if t.captureBack != nil {
		return t.captureBack, nil
	}
	backend, err := capture.DetectWithRunner(t.captureEnv, t.capture.Backend, t.captureRunner)
	if err != nil {
		return nil, err
	}
	t.captureBack = backend
	return backend, nil
}

// dropCaptureBackend forgets the detected backend so the next call re-detects.
func (t *toolSet) dropCaptureBackend() {
	t.captureMu.Lock()
	t.captureBack = nil
	t.captureMu.Unlock()
}

// execCaptureRunner is the production capture runner: one helper command, its
// stderr folded into the error, so a missing grim or a declined TCC prompt
// reads as itself. capture's own default runner is unexported, and
// DetectWithRunner uses exactly the runner it is given rather than guessing.
func execCaptureRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
