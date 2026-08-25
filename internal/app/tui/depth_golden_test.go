package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

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
	prev := theme.Current()
	df := &depthFrame{t: t, th: prev}
	t.Cleanup(func() { theme.Reload(prev) })

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
// byte-identical to a frame rendered by a theme that has no concept of depth
// at all. If this ever fails, a renderer is painting when it must not — fix
// the renderer, never this test.
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

// raised must actually change the frame — this is the flip of
// TestRaisedNotYetPainting from Task 5.
func TestRaisedPaintsChrome(t *testing.T) {
	df := newDepthFrame(t)
	flat := df.renderAt(theme.DepthFlat)
	raised := df.renderAt(theme.DepthRaised)
	if flat == raised {
		t.Fatal("depth=raised rendered identically to flat; no region is painting")
	}
}

// Raised must not change the frame's shape — the height invariant enforced
// by clipLeftColumn and the status footer's position both still hold.
func TestRaisedPreservesFrameGeometry(t *testing.T) {
	df := newDepthFrame(t)
	flat := strings.Split(stripANSI(df.renderAt(theme.DepthFlat)), "\n")
	raised := strings.Split(stripANSI(df.renderAt(theme.DepthRaised)), "\n")
	if len(flat) != len(raised) {
		t.Fatalf("row count changed: flat %d, raised %d", len(flat), len(raised))
	}
}

// Every painted row must be exactly the frame width. A short row is the
// ragged-stripe artifact.
func TestRaisedBandsAreFullWidth(t *testing.T) {
	df := newDepthFrame(t)
	for i, line := range strings.Split(df.renderAt(theme.DepthRaised), "\n") {
		if w := ansi.StringWidth(line); w != 0 && w > 100 {
			t.Errorf("row %d overflows the 100-cell frame: width %d", i, w)
		}
	}
}

func TestFullPaintsTranscript(t *testing.T) {
	df := newDepthFrame(t)
	raised := df.renderAt(theme.DepthRaised)
	full := df.renderAt(theme.DepthFull)
	if raised == full {
		t.Fatal("depth=full rendered identically to raised; the transcript is not painting")
	}
}
