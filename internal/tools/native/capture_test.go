package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/capture"
	"marshal/internal/tools/registry"
)

// fakeCaptureBackend stands in for a detected backend: it records the target it
// was asked for and returns canned bytes, so no test ever reaches a real
// compositor.
type fakeCaptureBackend struct {
	img     capture.Image
	err     error
	targets []capture.Target
}

func (f *fakeCaptureBackend) Capture(_ context.Context, t capture.Target) (capture.Image, error) {
	f.targets = append(f.targets, t)
	if f.err != nil {
		return capture.Image{}, f.err
	}
	return f.img, nil
}

// captureTestPNG encodes an in-memory width×height PNG. The store sniffs and
// decodes what it is handed, so the fake backend must produce real PNG bytes.
func captureTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return buf.Bytes()
}

func TestCaptureRegisteredOnlyWhenEnabledAndStored(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, stored := range []bool{false, true} {
			t.Run(fmt.Sprintf("enabled=%t/store=%t", enabled, stored), func(t *testing.T) {
				opts := Options{
					WorkspaceRoot: t.TempDir(),
					Capture:       config.CaptureConfig{Enabled: enabled},
				}
				if stored {
					opts.Images = imageTestStore(t)
				}
				reg := registry.New()
				if err := RegisterAll(reg, opts); err != nil {
					t.Fatalf("RegisterAll: %v", err)
				}
				_, ok := reg.Lookup("image.capture")
				if want := enabled && stored; ok != want {
					t.Fatalf("image.capture registered = %t, want %t", ok, want)
				}
			})
		}
	}
}

func TestCaptureIsReadOnlyAndSessionOnly(t *testing.T) {
	ts, err := newToolSet(Options{
		WorkspaceRoot: t.TempDir(),
		Images:        imageTestStore(t),
		Capture:       config.CaptureConfig{Enabled: true},
	})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	tool := ts.captureTool()
	if tool.Risk != registry.RiskReadOnly {
		t.Errorf("Risk = %q, want %q", tool.Risk, registry.RiskReadOnly)
	}
	if tool.WorkspaceEffect != registry.WorkspaceEffectSessionOnly {
		t.Errorf("WorkspaceEffect = %v, want WorkspaceEffectSessionOnly", tool.WorkspaceEffect)
	}
	if tool.NeedsWorkspacePreflight() {
		t.Error("image.capture needs a workspace preflight; it only reads the screen")
	}
}

func TestCaptureUnknownTargetRejected(t *testing.T) {
	tool := (&toolSet{root: t.TempDir(), images: imageTestStore(t)}).captureTool()

	_, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "image.capture",
		Args: json.RawMessage(`{"target":"window"}`),
	})
	if err == nil {
		t.Fatal("image.capture accepted target \"window\"")
	}
	if !errors.Is(err, registry.ErrInvalidArgs) {
		t.Fatalf("err = %v, want ErrInvalidArgs", err)
	}
}

func TestCaptureReturnsOneRef(t *testing.T) {
	store := imageTestStore(t)
	backend := &fakeCaptureBackend{img: capture.Image{
		PNG:     captureTestPNG(t, 8, 6),
		Label:   "window: kitty — ~/src/marshal",
		Backend: "sway",
	}}
	// Presetting captureBack is the injection point: the tool then never calls
	// Detect, so the test is about the handler's result, not detection.
	tool := (&toolSet{root: t.TempDir(), images: store, captureBack: backend}).captureTool()

	result, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "image.capture",
		Args: json.RawMessage(`{"label":"my shot"}`),
	})
	if err != nil {
		t.Fatalf("image.capture: %v", err)
	}
	if len(result.Images) != 1 {
		t.Fatalf("len(Images) = %d, want 1", len(result.Images))
	}
	ref := result.Images[0]
	if ref.Width != 8 || ref.Height != 6 {
		t.Fatalf("ref dimensions = %d×%d, want 8×6", ref.Width, ref.Height)
	}
	if ref.MIME != "image/png" {
		t.Errorf("ref MIME = %q, want image/png", ref.MIME)
	}
	if ref.Hash == "" {
		t.Error("ref hash is empty")
	}
	if ref.Label != "my shot" {
		t.Errorf("ref label = %q, want the argument", ref.Label)
	}
	if !strings.Contains(result.Summary, "my shot") {
		t.Errorf("summary %q does not name the label", result.Summary)
	}
	if !strings.Contains(result.Summary, "8×6") {
		t.Errorf("summary %q does not report the dimensions", result.Summary)
	}

	var content struct {
		Target  string `json:"target"`
		Backend string `json:"backend"`
		Width   int    `json:"width"`
		Height  int    `json:"height"`
		Bytes   int    `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(result.Content), &content); err != nil {
		t.Fatalf("content %q is not JSON: %v", result.Content, err)
	}
	if content.Target != "active_window" {
		t.Errorf("content target = %q, want the default active_window", content.Target)
	}
	if content.Backend != "sway" {
		t.Errorf("content backend = %q, want the backend's own name", content.Backend)
	}
	if content.Width != 8 || content.Height != 6 {
		t.Errorf("content dimensions = %d×%d, want 8×6", content.Width, content.Height)
	}
	if content.Bytes != ref.Bytes {
		t.Errorf("content bytes = %d, want the stored size %d", content.Bytes, ref.Bytes)
	}
	if len(backend.targets) != 1 || backend.targets[0] != capture.TargetActiveWindow {
		t.Errorf("backend targets = %v, want one active_window", backend.targets)
	}
	if _, count, err := store.Size(); err != nil || count != 1 {
		t.Errorf("store entries = %d (err %v), want 1", count, err)
	}
}

func TestCaptureNoBackendIsToolError(t *testing.T) {
	store := imageTestStore(t)
	reg := registry.New()
	err := RegisterAll(reg, Options{
		WorkspaceRoot: t.TempDir(),
		Images:        store,
		Capture:       config.CaptureConfig{Enabled: true},
		// An Env with nothing set rather than the literal zero value: newToolSet
		// fills a nil Getenv with os.Getenv, and a developer running sway would
		// then detect a real backend. This stays deterministic without reading
		// the ambient session.
		CaptureEnv: capture.Env{GOOS: "linux", Getenv: func(string) string { return "" }},
		CaptureRunner: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			t.Errorf("capture ran helper %q in a session with no backend", name)
			return nil, errors.New("unexpected helper call")
		},
	})
	if err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err = reg.Dispatch(context.Background(), registry.ToolCall{
		Name: "image.capture",
		Args: json.RawMessage(`{"target":"screen"}`),
	})
	if err == nil {
		t.Fatal("image.capture succeeded with no display session")
	}
	if !strings.Contains(err.Error(), "SWAYSOCK") {
		t.Fatalf("err = %v, want a detection failure naming SWAYSOCK", err)
	}
	if _, count, serr := store.Size(); serr != nil || count != 0 {
		t.Fatalf("store entries = %d (err %v), want none", count, serr)
	}
}
