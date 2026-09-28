package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/tools/registry"
)

// mouseTestTime is the fixed clock sessions are created at in this file,
// matching the time.Unix(100, 0) the other test models use so relative-time
// math stays deterministic.
func mouseTestTime() time.Time { return time.Unix(100, 0) }

// mouseToggleModel builds a model whose config sets the durable
// tui.mouse_capture default, isolated from the ambient default.
func mouseToggleModel(t *testing.T, capture bool) Model {
	t.Helper()
	m := mouseToggleModelNoSwapper(t, capture)
	// /new needs a session swapper to actually start a session; wire the
	// standard test fake so the reset path is exercised rather than skipped.
	cfg := config.Default()
	cfg.TUI.MouseCapture = capture
	m.sessionSwapper = &fakeSessionSwapper{
		newState: session.New(cfg, t.TempDir(), mouseTestTime(), session.Persistence{}),
	}
	return m
}

func mouseToggleModelNoSwapper(t *testing.T, capture bool) Model {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	cfg := config.Default()
	cfg.TUI.MouseCapture = capture
	state := session.New(cfg, t.TempDir(), mouseTestTime(), session.Persistence{})
	// /new and /clear need the real command registry to reach the TUI effect
	// table; without it dispatchCommand refuses before any effect runs.
	reg := commands.New()
	if err := commands.RegisterAll(reg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state, WithCommandRegistry(reg), WithHomeDir(t.TempDir()))
	m.resize(100, 30)
	return m
}

// The inherited default resolves from config, not from a stale bool: a fresh
// session with capture configured off must report the mouse as not captured.
func TestEffectiveMouseCaptureFollowsConfigByDefault(t *testing.T) {
	for _, tc := range []struct {
		capture bool
		want    bool
	}{
		{capture: true, want: true},
		{capture: false, want: false},
	} {
		m := mouseToggleModel(t, tc.capture)
		if got := m.effectiveMouseCapture(); got != tc.want {
			t.Fatalf("effectiveMouseCapture() = %v, want %v (capture=%v)", got, tc.want, tc.capture)
		}
		if got := m.mouseMode(); got != wantMouseMode(tc.want) {
			t.Fatalf("mouseMode() = %v, want %v", got, wantMouseMode(tc.want))
		}
	}
}

func wantMouseMode(capture bool) tea.MouseMode {
	if capture {
		return tea.MouseModeCellMotion
	}
	return tea.MouseModeNone
}

// The capture-off false-success bug: with capture configured off, the first
// Ctrl+S must CAPTURE (reporting what actually changed) rather than announce a
// release that already held. The three-state override is what makes the two
// states distinguishable.
func TestCtrlSFirstPressCapturesWhenConfiguredOff(t *testing.T) {
	m := mouseToggleModel(t, false)
	if m.effectiveMouseCapture() {
		t.Fatal("precondition: capture configured off must start un-captured")
	}

	mm, cmd := m.toggleMouseCapture()
	m = mm

	if !m.effectiveMouseCapture() {
		t.Fatal("first Ctrl+S with capture configured off must capture the mouse")
	}
	if m.mouseOverride != MouseCapture {
		t.Fatalf("override = %v, want MouseCapture", m.mouseOverride)
	}
	if cmd == nil {
		t.Fatal("toggle must schedule the toast expiry timer")
	}
	if text := m.toastText(); !strings.Contains(text, "captured") {
		t.Fatalf("toast should report the capture, got %q", text)
	}
}

// A second Ctrl+S toggles back relative to the effective state, regardless of
// which configured default the session started from.
func TestCtrlSTogglesBackFromEitherDefault(t *testing.T) {
	for _, start := range []bool{true, false} {
		m := mouseToggleModel(t, start)

		mm, _ := m.toggleMouseCapture()
		m = mm
		if got := m.effectiveMouseCapture(); got != !start {
			t.Fatalf("after first toggle effective capture = %v, want %v", got, !start)
		}
		if m.toastText() == "" {
			t.Fatal("first toggle must show toast feedback")
		}

		mm, _ = m.toggleMouseCapture()
		m = mm
		if got := m.effectiveMouseCapture(); got != start {
			t.Fatalf("after second toggle effective capture = %v, want back to %v", got, start)
		}
	}
}

// Ctrl+S must work with a draft in the composer: the toggle used to be gated
// on an empty input, which broke the exact use case (copy text while writing
// the next prompt) the key exists for.
func TestCtrlSWorksWithDraft(t *testing.T) {
	m := mouseToggleModel(t, true)
	m.setFocus(FocusComposer)
	m.input.SetValue("half-written prompt")

	mm, _ := m.toggleMouseCapture()
	m = mm
	if m.effectiveMouseCapture() {
		t.Fatal("Ctrl+S must release the mouse even with a draft present")
	}
	if m.input.Value() != "half-written prompt" {
		t.Fatalf("Ctrl+S must not disturb the draft, got %q", m.input.Value())
	}
}

// A config reload changes the inherited default without discarding an explicit
// session override. Inherit follows the new value; capture/release keep
// pinning the state the user chose.
func TestConfigReloadChangesInheritedDefaultNotOverride(t *testing.T) {
	m := mouseToggleModel(t, true)
	if !m.effectiveMouseCapture() {
		t.Fatal("precondition: inherited capture on")
	}

	// Explicit release survives a config change that would otherwise flip the
	// inherited default back on.
	m.mouseOverride = MouseRelease
	m.applyNewConfig(config.Default()) // still capture=true, but through the reload path
	if m.effectiveMouseCapture() {
		t.Fatal("explicit release must survive a config reload")
	}

	// Inherit follows the reloaded default.
	m.mouseOverride = MouseInherit
	newCfg := config.Default()
	newCfg.TUI.MouseCapture = false
	m.applyNewConfig(newCfg)
	if m.effectiveMouseCapture() {
		t.Fatal("inherit must follow the reloaded config (capture now off)")
	}

	// And capture pins on across a config that turned capture off.
	m.mouseOverride = MouseCapture
	if !m.effectiveMouseCapture() {
		t.Fatal("explicit capture must survive a capture-off config")
	}
}

// A new session resets to inherit, so the fresh conversation follows its own
// config rather than silently keeping an override from the previous one.
func TestNewSessionResetsOverrideToInherit(t *testing.T) {
	m := mouseToggleModel(t, true)
	m.mouseOverride = MouseRelease

	// /new is TUI-only; drive it through the dispatch table the same way the
	// composer does so the reset path (newSessionEffect) is the one exercised.
	// dispatchCommand returns *Model (it re-points the live pointer), so
	// asModel handles either shape.
	mm, _ := m.dispatchCommand("/new")
	m = asModel(t, mm)

	if m.mouseOverride != MouseInherit {
		t.Fatalf("new session override = %v, want MouseInherit", m.mouseOverride)
	}
}

// The View must declare the resolved effective mode, never the raw config or
// the raw override: reading one of them directly is how the footer and the
// terminal ended up disagreeing about whether the mouse was captured.
func TestViewDeclaresResolvedMouseMode(t *testing.T) {
	m := mouseToggleModel(t, false)
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("View().MouseMode = %v, want MouseModeNone for capture-off config", got)
	}
	m.mouseOverride = MouseCapture
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("View().MouseMode = %v, want CellMotion after explicit capture", got)
	}
	m.mouseOverride = MouseRelease
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("View().MouseMode = %v, want MouseModeNone after explicit release", got)
	}
}

// The toast is generation-tagged: a superseded toast's expiry must not clear
// the newer one. Pressing Ctrl+S twice quickly is the natural repro. The two
// expiry timers are modelled directly — invoking the real tea.Tick command
// would sleep the full duration — with the generations showToast handed out
// (1 for the first toast, 2 for the superseding one).
func TestSupersededToastExpiryDoesNotClearNewerToast(t *testing.T) {
	m := mouseToggleModel(t, true)

	_ = m.showToast("first")  // gen 1
	_ = m.showToast("second") // gen 2, supersedes gen 1
	if m.toastText() != "second" {
		t.Fatalf("toast text = %q, want the latest toast", m.toastText())
	}

	// The first toast's timer fires late. It must be ignored, not honoured.
	mm, _ := m.handleToastExpired(toastExpiredMsg{gen: 1})
	m = mm
	if m.toastText() != "second" {
		t.Fatalf("a superseded toast's expiry cleared the live one: %q", m.toastText())
	}

	// The live toast's own timer retires it.
	mm, _ = m.handleToastExpired(toastExpiredMsg{gen: 2})
	m = mm
	if m.toastText() != "" {
		t.Fatalf("the live toast must expire: %q", m.toastText())
	}
}

// The toast's wall-clock deadline is a second line of defence: a dropped
// timer message must not leave a stale line on the status bar forever. The
// fake clock is installed before the toast is shown so the deadline is
// derived from the frozen time, then advanced past the duration.
func TestToastExpiresByDeadlineWhenTimerDrops(t *testing.T) {
	m := mouseToggleModel(t, true)
	clock := mouseTestTime()
	m.now = func() time.Time { return clock }

	_ = m.showToast("stale")
	if m.toastText() == "" {
		t.Fatal("precondition: toast must be visible")
	}

	m.now = func() time.Time { return clock.Add(toastDuration + time.Second) }
	if m.toastText() != "" {
		t.Fatalf("toast must not outlive its deadline: %q", m.toastText())
	}
}

// Capture changes must never append a transcript message: the feedback is a
// transient toast, and a UI mode change is not conversation content. This is
// the exact regression that made every Ctrl+S pollute the history.
func TestCtrlSDoesNotAppendTranscriptMessage(t *testing.T) {
	m := mouseToggleModel(t, true)
	before := len(m.state.Messages())

	mm, _ := m.toggleMouseCapture()
	m = mm
	mm, _ = m.toggleMouseCapture()
	m = mm

	if after := len(m.state.Messages()); after != before {
		t.Fatalf("Ctrl+S appended %d transcript message(s)", after-before)
	}
}
