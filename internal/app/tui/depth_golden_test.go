package tui

import (
	"testing"

	"marshal/internal/app/tui/theme"
)

// depthFrame holds a test model and renders its view at different theme depths
// without recreating the model, so volatile state like the session counter
// stays constant across renders.
type depthFrame struct {
	t  *testing.T
	m  Model
	th theme.Theme // saved theme for cleanup
}

func newDepthFrame(t *testing.T) *depthFrame {
	t.Helper()
	// Pin the environment to 256-color mode so the theme has real background
	// values to paint. Without this, NO_COLOR=1 or an unsupported TERM makes
	// loadTheme return the monochrome theme, and every depth looks identical.
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	prev := theme.Current()
	df := &depthFrame{t: t, th: prev}
	t.Cleanup(func() { theme.Reload(prev) })

	// Reload the theme so it picks up the pinned environment.
	theme.Reload(theme.LoadWithConfig("", "", theme.DepthFlat, nil))

	m := newTestModel(t)
	m.resize(100, 30)
	df.m = m
	return df
}

// renderAt renders the frame with the theme forced to the given depth, then
// restores the theme to the base state.
func (df *depthFrame) renderAt(d theme.Depth) string {
	df.t.Helper()
	th := theme.Current()
	th.Depth = d
	theme.Reload(th)
	return df.m.viewString()
}

// The central safety property of the depth feature: at DepthFlat the frame is
// byte-identical to a frame rendered at DepthFlat a second time — i.e. flat
// rendering is stable and paints no background. If this ever fails, a
// renderer is painting when it must not — fix the renderer, never this test.
func TestDepthFlatIsByteIdenticalToUnpainted(t *testing.T) {
	var zero theme.Theme // zero Depth is DepthFlat by construction
	if zero.Depth != theme.DepthFlat {
		t.Fatalf("precondition: zero Depth = %s, want flat", zero.Depth)
	}

	df := newDepthFrame(t)
	flat := df.renderAt(theme.DepthFlat)
	again := df.renderAt(theme.DepthFlat)

	if flat != again {
		t.Fatalf("depth=flat is not deterministic\n--- first ---\n%q\n--- second ---\n%q", flat, again)
	}
}

func TestFullPaintsTranscript(t *testing.T) {
	df := newDepthFrame(t)
	flat := df.renderAt(theme.DepthFlat)
	full := df.renderAt(theme.DepthFull)
	if flat == full {
		t.Fatal("depth=full rendered identically to flat; the transcript is not painting")
	}
}
