package tui

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/settings"
	"marshal/internal/commands"
	"marshal/internal/tools/native"
)

func newViewTestModel(t *testing.T, width, height int) Model {
	t.Helper()
	// View tests assert on color styling; force the default warm-sunset
	// palette even when the outer environment sets NO_COLOR.
	t.Setenv("NO_COLOR", "")
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(width, height)
	m.refreshViewport()
	return m
}

func TestViewIsSingleColumn(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.refreshViewport()
	view := m.View().Content

	for _, gone := range []string{"inspector", "1 Plan", "2 Context", "3 Log", "live transcript", "● ● ●", "MARSHAL"} {
		if strings.Contains(view, gone) {
			t.Fatalf("view still contains removed chrome %q", gone)
		}
	}
	if !strings.Contains(view, "❯") {
		t.Fatal("view missing input prompt / transcript")
	}
}

func TestViewContainsStatusLine(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Model: "qwen", Provider: "ollama", LocalOnly: true})
	view := m.View().Content
	if !strings.Contains(view, "qwen @ ollama") {
		t.Fatalf("view missing status line route info:\n%s", view)
	}
}

func TestViewShowsIdleHintsInStatusLine(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	view := stripANSI(m.View().Content)
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	last := lines[len(lines)-1]
	for _, want := range []string{"Tab mode", "? help"} {
		if !strings.Contains(last, want) {
			t.Fatalf("status line (last row) missing hint %q:\n%s", want, last)
		}
	}
}

func TestPickerRendersDockedAboveInput(t *testing.T) {
	m := newViewTestModel(t, 100, 40)
	m.openPicker("mode", "Interaction mode", "", m.modePickerItems(), "")

	lines := strings.Split(stripANSI(m.viewString()), "\n")
	panelLine, inputLine := -1, -1
	for index, line := range lines {
		if strings.Contains(line, "Interaction mode") {
			panelLine = index
		}
		if panelLine != -1 && strings.Contains(line, "❯") {
			inputLine = index
			break
		}
	}
	if panelLine == -1 {
		t.Fatal("picker panel not rendered")
	}
	if inputLine == -1 || inputLine < panelLine {
		t.Fatalf("picker must sit above the input area (panel=%d input=%d)", panelLine, inputLine)
	}
	if !strings.HasPrefix(lines[panelLine], "▍") {
		t.Errorf("panel should be left-aligned, got %q", lines[panelLine])
	}
}

func TestConnectRendersDockedAboveInput(t *testing.T) {
	m := newViewTestModel(t, 100, 40)
	m.openConnect("")

	view := stripANSI(m.viewString())
	if !strings.Contains(view, "Connect a provider") {
		t.Fatalf("connect panel content not rendered:\n%s", view)
	}

	lines := strings.Split(view, "\n")
	// connect.Model.View delegates straight to the active picker's own
	// gutter-framed panel (single border, titled with the step name) rather
	// than wrapping it in a second outer panel.
	panelLine, inputLine := -1, -1
	for index, line := range lines {
		if strings.Contains(line, "▍") && strings.Contains(line, "Connect a provider") {
			panelLine = index
		}
		if panelLine != -1 && strings.Contains(line, "❯") {
			inputLine = index
			break
		}
	}
	if panelLine == -1 {
		t.Fatal("connect panel gutter not rendered")
	}
	if inputLine == -1 || inputLine < panelLine {
		t.Fatalf("connect panel must sit above the input area (panel=%d input=%d)", panelLine, inputLine)
	}
	if !strings.HasPrefix(lines[panelLine], "▍") {
		t.Errorf("panel should be left-aligned, got %q", lines[panelLine])
	}
	for _, line := range lines {
		if strings.Count(line, "▍") > 1 {
			t.Errorf("connect panel should have a single gutter, got nested gutters: %q", line)
		}
	}
}

func TestTranscriptIsBorderless(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.refreshViewport()
	transcript := m.renderTranscriptFrame()
	if strings.Contains(transcript, "╭") || strings.Contains(transcript, "╰") {
		t.Fatalf("transcript should have no rounded border:\n%s", transcript)
	}
}

func TestTranscriptFrameDoesNotMoveWhenActivityStarts(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.AddMessage(session.RoleUser, strings.Repeat("hello ", 120), session.ContentTypePlain)
	m.refreshViewport()
	idleLines := strings.Split(strings.TrimRight(m.View().Content, "\n"), "\n")

	m.state.SetActivity(session.Activity{Kind: session.ActivityThinking, StartedAt: time.Unix(100, 0)})
	m.busy = true
	m.spinnerFrame = "⠋"
	m.refreshViewport()
	busyLines := strings.Split(strings.TrimRight(m.View().Content, "\n"), "\n")

	if len(busyLines) != 30 {
		t.Fatalf("busy view height = %d, want fixed terminal height 30", len(busyLines))
	}
	if len(idleLines) != 30 {
		t.Fatalf("idle view height = %d, want fixed terminal height 30", len(idleLines))
	}
	if idleLines[0] != busyLines[0] {
		t.Fatalf("transcript top frame moved:\nidle: %q\nbusy: %q", idleLines[0], busyLines[0])
	}
	inputTop := 30 - m.inputAreaRows() - statusLineRows
	if !strings.Contains(stripANSI(busyLines[inputTop]), "▍") {
		t.Fatalf("input area missing state bar; line %d = %q", inputTop, busyLines[inputTop])
	}
}

func TestViewFitsTerminalSizesSingleColumn(t *testing.T) {
	sizes := [][2]int{{80, 24}, {100, 30}, {120, 40}}
	for _, size := range sizes {
		m := newViewTestModel(t, size[0], size[1])
		m.state.AddMessage(session.RoleUser, strings.Repeat("wide input ", 30), session.ContentTypePlain)
		m.state.AddMessage(session.RoleAssistant, strings.Repeat("wide answer ", 30), session.ContentTypeMarkdown)
		m.refreshViewport()
		view := m.View().Content
		lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
		if len(lines) > size[1] {
			t.Fatalf("view has %d lines for height %d", len(lines), size[1])
		}
		for _, line := range lines {
			if visibleRunes(line) > size[0] {
				t.Fatalf("line exceeds width %d (%d): %q", size[0], visibleRunes(line), line)
			}
		}
	}
}

func TestProviderErrorShowsInlineNotFullScreen(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.state.SetProviderError(errors.New("connection refused"))
	m.lastTranscriptHash = 0
	m.refreshViewport()
	view := m.View().Content

	if !strings.Contains(view, "✗") || !strings.Contains(view, "provider: connection refused") {
		t.Fatalf("provider error not rendered inline:\n%s", view)
	}
	if !strings.Contains(view, "hello") {
		t.Fatal("provider error must not hide the transcript")
	}
}

func TestResizeComputesSingleColumnGeometry(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	if m.viewport.Width() != 100 {
		t.Fatalf("viewport.Width = %d, want 100 (full terminal width, borderless transcript)", m.viewport.Width())
	}
	wantHeight := 30 - m.inputAreaRows() - m.turnSpinnerRows() - statusLineRows
	if m.viewport.Height() != wantHeight {
		t.Fatalf("viewport.Height = %d, want %d", m.viewport.Height(), wantHeight)
	}
}

func TestInputBarPulsesTealOnSuccess(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.successPulse = true
	teal := m.renderInputArea()
	m.successPulse = false
	neutral := m.renderInputArea()
	if teal == neutral {
		t.Fatal("success pulse bar should differ from the default focused bar")
	}
	if !strings.Contains(stripANSI(teal), "▍") {
		t.Fatalf("input area missing state bar:\n%s", stripANSI(teal))
	}
}

func TestInputAreaHasNoBackgroundFill(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	out := m.renderInputArea()
	// panelBg 235 must never be emitted as a fill anymore.
	if strings.Contains(out, "48;5;235") || strings.Contains(out, ";235m") {
		t.Fatalf("input area still emits panel background fill:\n%q", out)
	}
	if !strings.Contains(stripANSI(out), "❯") {
		t.Fatalf("input area missing prompt:\n%q", stripANSI(out))
	}
}

func TestInputBarColorReflectsFocus(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	focused := m.renderInputArea()
	m.input.Blur()
	blurred := m.renderInputArea()
	if focused == blurred {
		t.Fatal("focused and blurred input should have different bar colors")
	}
	if focused == stripANSI(focused) {
		t.Fatalf("focused input area has no ANSI styling:\n%q", focused)
	}
}

func TestInputAreaHasNoBorderBox(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	out := stripANSI(m.renderInputArea())
	for _, glyph := range []string{"╭", "╰", "│"} {
		if strings.Contains(out, glyph) {
			t.Fatalf("input area still box-bordered (%q):\n%s", glyph, out)
		}
	}
	if !strings.Contains(out, "▍❯") {
		t.Fatalf("input area missing ▍❯ prompt:\n%s", out)
	}
}

func TestLongInputExpandsToMultipleRows(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	singleLineRows := m.inputAreaRows()

	longInput := strings.Repeat("wrap me ", 15)
	updated, _ := m.Update(tea.KeyPressMsg{Text: longInput})
	m = updated.(Model)

	if m.inputAreaRows() <= singleLineRows {
		t.Fatalf("input area rows = %d, want more than single-line rows %d", m.inputAreaRows(), singleLineRows)
	}

	for _, line := range strings.Split(stripANSI(m.renderInputArea()), "\n") {
		if visibleRunes(line) > m.width {
			t.Fatalf("input line exceeds terminal width %d (%d): %q", m.width, visibleRunes(line), line)
		}
	}
}

func TestMultilineInputAlignsContinuationLines(t *testing.T) {
	m := newViewTestModel(t, 80, 24)

	// Type a line long enough to soft-wrap at the textarea's text width
	// (80 - 2 reserved - 2 prompt = 76 text columns). Use 90 chars.
	longInput := strings.Repeat("a", 90)
	updated, _ := m.Update(tea.KeyPressMsg{Text: longInput})
	m = updated.(Model)

	// Check the raw textarea view (before box rendering) for alignment.
	rawView := stripANSI(m.input.View())
	rawLines := strings.Split(strings.TrimRight(rawView, "\n"), "\n")

	if len(rawLines) < 2 {
		t.Fatalf("expected at least 2 lines in textarea view, got %d:\n%s", len(rawLines), rawView)
	}

	// Line 0 should start with "❯ " (prompt for first line).
	if !strings.HasPrefix(rawLines[0], "❯ ") {
		t.Fatalf("first line should start with ❯ , got: %q", rawLines[0])
	}

	// Continuation lines should start with "  " (2-space indent).
	for i := 1; i < len(rawLines); i++ {
		line := rawLines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("continuation line %d should start with 2-space indent, got: %q", i, line)
		}
		// The first 'a' on the continuation line should be at the same
		// column as the first 'a' on the prompt line (after "❯ ").
		promptRunes := []rune(rawLines[0])
		contRunes := []rune(line)
		promptTextCol := -1
		for j, r := range promptRunes {
			if r == 'a' {
				promptTextCol = j
				break
			}
		}
		contTextCol := -1
		for j, r := range contRunes {
			if r == 'a' {
				contTextCol = j
				break
			}
		}
		if promptTextCol < 0 || contTextCol < 0 {
			t.Fatalf("could not find 'a' in lines\nline0=%q\nline%d=%q", rawLines[0], i, line)
		}
		if promptTextCol != contTextCol {
			t.Fatalf("text column mismatch: prompt line 'a' at rune index %d, continuation line %d 'a' at rune index %d\nline0=%q\nline%d=%q",
				promptTextCol, i, contTextCol, rawLines[0], i, line)
		}
	}
}

func TestInputAreaHasNoBlankRowsWhenIdle(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.input.SetValue("hello")
	out := stripANSI(m.renderInputArea())
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	if len(lines) < 1 {
		t.Fatalf("expected at least 1 line, got %d:\n%s", len(lines), out)
	}

	promptCount := 0
	for _, line := range lines {
		if strings.Contains(line, "▍❯") {
			promptCount++
		}
	}

	if promptCount != 1 {
		t.Fatalf("expected exactly 1 line with ▍❯, got %d:\n%s", promptCount, out)
	}
}

func TestInputWrapsBeforeTerminalWidth(t *testing.T) {
	for _, w := range []int{80, 100} {
		m := newViewTestModel(t, w, 24)
		m.input.SetValue(strings.Repeat("a", 400))
		out := stripANSI(m.renderInputArea())
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

		if len(lines) < 1 {
			t.Fatalf("width=%d: expected at least 1 line, got %d:\n%s", w, len(lines), out)
		}

		for _, line := range lines {
			if visibleRunes(line) > w {
				t.Fatalf("width=%d: rendered line exceeds terminal width %d (%d): %q", w, w, visibleRunes(line), line)
			}
		}

		promptRow := ""
		for _, line := range lines {
			if strings.Contains(line, "▍❯") {
				promptRow = line
				break
			}
		}
		if promptRow == "" {
			t.Fatalf("width=%d: no prompt row found:\n%s", w, out)
		}
		if !strings.Contains(promptRow, "a") {
			t.Fatalf("width=%d: prompt row has no text after ▍❯:\n%s", w, out)
		}
	}
}

func TestViewHasNoFooterRule(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	view := stripANSI(m.View().Content)
	for _, line := range strings.Split(view, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 20 && strings.Count(trimmed, "─") == len([]rune(trimmed)) {
			t.Fatalf("view still contains a full-width rule:\n%q", line)
		}
	}
}

// Mouse capture defaults on so the wheel scrolls the transcript out of the
// box — AltScreen leaves no terminal scrollback to fall back on.
func TestMouseCaptureOnByDefault(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("View().MouseMode = %v, want MouseModeCellMotion (wheel scrolling enabled)", got)
	}
}

// Turning it off hands the mouse back to the terminal for plain click-drag
// text selection.
func TestMouseCaptureDisabledByConfig(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.state.Config.TUI.MouseCapture = false
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("View().MouseMode = %v, want MouseModeNone (native selection enabled)", got)
	}
}

func TestTooSmallShowsResizeMessage(t *testing.T) {
	// Defense-in-depth: sets rawWidth/rawHeight directly (bypassing resize)
	// to confirm the gate condition still fires on the raw fields.
	m := newViewTestModel(t, 80, 24)
	m.rawWidth = 70
	m.rawHeight = 23
	view := m.View().Content
	if !strings.Contains(view, "Terminal too small") {
		t.Fatalf("too-small terminal should show resize message:\n%s", view)
	}
	if !strings.Contains(view, "80") || !strings.Contains(view, "24") {
		t.Fatalf("resize message should mention required dimensions:\n%s", view)
	}
}

func TestTooSmallViewUsesRawTerminalBounds(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.rawWidth = 20
	m.rawHeight = 3
	view := stripANSI(m.View().Content)
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > m.rawHeight {
		t.Fatalf("too-small view line count = %d, want <= raw height %d:\n%s", len(lines), m.rawHeight, view)
	}
	for _, line := range lines {
		if visibleRunes(line) > m.rawWidth {
			t.Fatalf("too-small view width = %d, want <= raw width %d: %q", visibleRunes(line), m.rawWidth, line)
		}
	}
}

func TestCompletionPopupANSITruncationStaysRenderable(t *testing.T) {
	m := newViewTestModelWithRegistry(t, 24, 24)
	m.input.SetValue("/pl")
	m.updateCompletionPopups()
	out := m.renderCompletionPopup()
	// Check that every \x1b[ escape is properly terminated with m (no
	// truncated sequences). The old heuristic of counting raw "m" chars
	// broke once the chrome panel added text content containing "m".
	if strings.Contains(out, "\x1b[") {
		// Find any \x1b[ not followed by a valid SGR terminator.
		if regexp.MustCompile(`\x1b\[[0-9;]*[^0-9;m]`).MatchString(out) {
			t.Fatalf("completion popup appears to contain truncated ANSI sequence: %q", out)
		}
	}
}

func TestTooSmallGateFiresOnActualResize(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.resize(60, 20) // user resizes to a small terminal — production path
	view := m.View().Content
	if !strings.Contains(strings.ToLower(view), "resize") {
		t.Fatalf("expected resize message after m.resize(60,20), got:\n%s", view)
	}
	if strings.Contains(view, "❯") {
		t.Fatalf("input prompt should not render in too-small terminal:\n%s", view)
	}
}

func TestNormalViewRendersAtMinSize(t *testing.T) {
	m := newViewTestModel(t, minTerminalWidth, minTerminalHeight)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.refreshViewport()
	view := m.View().Content
	if strings.Contains(view, "Terminal too small") {
		t.Fatalf("at min size %dx%d, normal view should render:\n%s", minTerminalWidth, minTerminalHeight, view)
	}
	if !strings.Contains(view, "❯") {
		t.Fatal("normal view at min size should show input prompt")
	}
}

// No full-screen takeover survives: with a dock panel open, the title
// bar, transcript, input, footer, and status line are all still present.
// TestNoFullScreenTakeovers asserts that opening a dock panel does not
// turn the TUI into a full-screen modal: the status line and the panel's
// title stay visible at the same time. Under the dock-sizing plan,
// settings and agents are FullFrame, so the transcript (and the welcome
// banner it carries) is hidden while the panel is open. The status line
// remains.
func TestNoFullScreenTakeovers(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.openSettingsBrowser("")
	out := stripANSI(m.viewString())
	for _, want := range []string{"Settings", "default"} {
		if !strings.Contains(out, want) {
			t.Errorf("frame missing %q while dock open", want)
		}
	}
	// Frame height is bounded, not exact: the FullFrame dock panel trims
	// internally and the status line has no trailing newline, so the
	// counted newline-based height may be one less than the requested
	// frame height. The invariant is "no bigger than the frame".
	if got := strings.Count(out, "\n") + 1; got > 40 {
		t.Errorf("frame height %d, want <= 40", got)
	}
}

func TestFullFrameDockPanelHidesTranscriptAndInput(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.state.AddMessage(session.RoleUser, "transcript marker text", session.ContentTypePlain)
	m.refreshViewport()

	m.dock.Open(settings.NewBrowser(config.Default(), filepath.Join(t.TempDir(), "config.toml"), ""))
	out := stripANSI(m.viewString())
	if !strings.Contains(out, "Settings") {
		t.Fatalf("expected the settings panel in the view, got:\n%s", out)
	}
	if strings.Contains(out, "transcript marker text") {
		t.Fatalf("transcript should be hidden while a FullFrame panel is open:\n%s", out)
	}
	if strings.Contains(out, "Ask Marshal...") {
		t.Fatalf("input area should be hidden while a FullFrame panel is open:\n%s", out)
	}

	m.dock.CloseNow()
	out = stripANSI(m.viewString())
	if !strings.Contains(out, "transcript marker text") {
		t.Fatalf("transcript should return after the panel closes:\n%s", out)
	}
}

// newViewTestModelWithRegistry builds a model with a small in-memory
// commands registry. Used by F18 completion tests that need a real
// /command source to fuzzy-filter against.
func newViewTestModelWithRegistry(t *testing.T, width, height int) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	mustRegister(t, reg, commands.Command{
		Name: "plan", Description: "Plan a task", Args: "<goal>",
		Handler: func(s *session.State, args []string) commands.Result { return commands.Text("") },
	})
	mustRegister(t, reg, commands.Command{
		Name: "help", Description: "Show help", Args: "",
		Handler: func(s *session.State, args []string) commands.Result { return commands.Text("") },
	})
	mustRegister(t, reg, commands.Command{
		Name: "tools", Description: "List tools", Args: "",
		Handler: func(s *session.State, args []string) commands.Result { return commands.Text("") },
	})
	m := New(state, WithCommandRegistry(reg))
	m.resize(width, height)
	m.refreshViewport()
	return m
}

// newViewTestModelWithFileIndex builds a model with a seeded repo file
// index for F18 @file completion tests. The paths are loaded eagerly via
// WithFileIndex; no DB is required.
func newViewTestModelWithFileIndex(t *testing.T, width, height int, paths []string) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state, WithFileIndex(paths))
	m.resize(width, height)
	m.refreshViewport()
	return m
}

func mustRegister(t *testing.T, reg *commands.Registry, c commands.Command) {
	t.Helper()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register %s: %v", c.Name, err)
	}
}

// newViewTestModelWithRegistryAndFileIndex builds a model with both a
// commands registry and a seeded repo file index — used for tests that
// exercise the mutual exclusion between the F18 cmd and file popups.
func newViewTestModelWithRegistryAndFileIndex(t *testing.T, width, height int, paths []string) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	mustRegister(t, reg, commands.Command{
		Name: "plan", Description: "Plan a task", Args: "<goal>",
		Handler: func(s *session.State, args []string) commands.Result { return commands.Text("") },
	})
	mustRegister(t, reg, commands.Command{
		Name: "help", Description: "Show help", Args: "",
		Handler: func(s *session.State, args []string) commands.Result { return commands.Text("") },
	})
	m := New(state, WithCommandRegistry(reg), WithFileIndex(paths))
	m.resize(width, height)
	m.refreshViewport()
	return m
}

func TestTodoPanelIsPinnedBelowTranscript(t *testing.T) {
	m := newTestModel(t)
	if err := m.state.SetTodos([]native.TodoItem{
		{Content: "scaffold parser", Status: native.TodoCompleted},
		{Content: "implement parser", Status: native.TodoInProgress},
		{Content: "add tests", Status: native.TodoPending},
	}); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	m.refreshViewport()

	frame := stripANSI(m.viewString())
	lines := strings.Split(frame, "\n")
	if len(lines) != m.height {
		t.Fatalf("frame height = %d, want %d", len(lines), m.height)
	}
	todoRow, inputRow := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "▸ implement parser") {
			todoRow = i
		}
		if strings.Contains(line, "▍") && strings.Contains(line, "❯") {
			inputRow = i
		}
	}
	if todoRow < 0 {
		t.Fatalf("todo panel missing from the frame:\n%s", frame)
	}
	if inputRow >= 0 && todoRow > inputRow {
		t.Fatalf("todo panel must render above the input row (todo=%d input=%d)", todoRow, inputRow)
	}
	// The transcript no longer carries todos.
	if strings.Contains(stripANSI(m.viewport.View()), "implement parser") {
		t.Fatal("todos must not be rendered into the transcript any more")
	}
}

func TestRunPanelDoesNotPushInputOffScreen(t *testing.T) {
	m := newTestModel(t)
	m.state.SetSDDProgress(session.SDDProgress{
		Active:      true,
		PlanName:    "test-plan",
		Branch:      "feat/test",
		CurrentTask: 2,
		DoneTasks:   1,
		TotalTasks:  5,
		Phase:       "implement",
		Detail:      "working on the parser",
		Tasks:       []string{"Scaffold", "Parse", "Evaluate", "Report", "Cleanup"},
		StartedAt:   m.now().Add(-time.Minute),
	})
	m.refreshViewport()

	frame := stripANSI(m.viewString())
	lines := strings.Split(frame, "\n")
	if len(lines) != m.height {
		t.Fatalf("frame height = %d, want %d (run panel pushed input off screen)\n%s", len(lines), m.height, frame)
	}

	// The run panel summary must be visible.
	panelRow := -1
	for i, line := range lines {
		if strings.Contains(line, "task 2/5") {
			panelRow = i
		}
	}
	if panelRow < 0 {
		t.Fatalf("run panel missing from the frame:\n%s", frame)
	}

	// The plan checklist renders task titles — the old SDD panel never did.
	if !strings.Contains(frame, "▸ 2 Parse") {
		t.Fatalf("run panel checklist missing from the frame:\n%s", frame)
	}

	// The input box must be visible (the ❯ prompt).
	inputRow := -1
	for i, line := range lines {
		if strings.Contains(line, "❯") {
			inputRow = i
		}
	}
	if inputRow < 0 {
		t.Fatalf("input box missing from the frame:\n%s", frame)
	}
	if inputRow >= m.height {
		t.Fatalf("input box at row %d is off-screen (height %d)\n%s", inputRow, m.height, frame)
	}
}

func TestTurnSpinnerReservedWhenIdle(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	if got := m.renderTurnSpinner(); got != "" {
		t.Fatalf("idle turn spinner should render blank, got %q", got)
	}
	if m.turnSpinnerRows() != 1 {
		t.Fatalf("turnSpinnerRows() = %d, want 1 (row always reserved)", m.turnSpinnerRows())
	}
}

func TestTurnSpinnerShowsElapsedDirectlyAboveInput(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.busy = true
	m.turnStartedAt = m.now().Add(-12 * time.Second)
	m.spinnerFrame = "⠋"

	row := stripANSI(m.renderTurnSpinner())
	if !strings.Contains(row, "⠋") || !strings.Contains(row, "12s") {
		t.Fatalf("turn spinner missing glyph/elapsed: %q", row)
	}
	if strings.Contains(row, "thinking") {
		t.Fatalf("turn spinner must not show phase label: %q", row)
	}

	lines := strings.Split(strings.TrimRight(stripANSI(m.View().Content), "\n"), "\n")
	inputTop := 30 - m.inputAreaRows() - statusLineRows
	if !strings.Contains(lines[inputTop], "▍") {
		t.Fatalf("input bar not where expected; line %d = %q", inputTop, lines[inputTop])
	}
	if !strings.Contains(lines[inputTop-1], "⠋") {
		t.Fatalf("turn spinner must sit directly above the input; line %d = %q", inputTop-1, lines[inputTop-1])
	}
}

func TestRenderActiveToolCallUsesRunningGutter(t *testing.T) {
	out := renderActiveToolCall(
		session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StartedAt: time.Unix(100, 0)},
		session.SandboxInfo{}, false, "⠻", time.Unix(104, 0), false, 80)
	plain := stripANSI(out)
	if !strings.HasPrefix(plain, " ▸ ") {
		t.Fatalf("running tool should use the ▸ gutter:\n%q", plain)
	}
}

func TestTranscriptEntriesSeparatedByOneBlankLine(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	// Two agent messages in a row: agent prose is the renderer that
	// previously emitted no trailing blank line, so this case only passes
	// once the caller owns separation.
	m.state.AddMessage(session.RoleAssistant, "first answer", session.ContentTypeMarkdown)
	m.state.AddMessage(session.RoleAssistant, "second answer", session.ContentTypeMarkdown)
	m.refreshViewport()

	lines := strings.Split(stripANSI(m.viewport.View()), "\n")
	first, second := -1, -1
	for i, l := range lines {
		if first == -1 && strings.Contains(l, "first answer") {
			first = i
		}
		if strings.Contains(l, "second answer") {
			second = i
		}
	}
	if first == -1 || second == -1 {
		t.Fatalf("transcript missing entries:\n%s", strings.Join(lines, "\n"))
	}
	if second != first+2 || strings.TrimSpace(lines[first+1]) != "" {
		t.Fatalf("entries must be separated by exactly one blank line (first=%d second=%d):\n%s",
			first, second, strings.Join(lines, "\n"))
	}
}

// TestTurnSpinnerSpansPhaseGaps is the whole point of the turn spinner: every
// agent phase resets Activity to ActivityIdle on exit, so a phase-driven row
// goes blank between phases — exactly when the user wants to know the agent
// has not hung.
func TestTurnSpinnerSpansPhaseGaps(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.busy = true
	m.turnStartedAt = m.now().Add(-24 * time.Second)
	m.spinnerFrame = "⠹"
	m.state.SetActivity(session.Activity{Kind: session.ActivityIdle})

	row := stripANSI(m.renderTurnSpinner())
	if !strings.Contains(row, "24s") {
		t.Errorf("spinner row = %q, want elapsed 24s while busy between phases", row)
	}
	if !strings.Contains(row, "⠹") {
		t.Errorf("spinner row = %q, want a spinner glyph", row)
	}
	if strings.Contains(row, "thinking") {
		t.Errorf("spinner row = %q, want no phase label", row)
	}
}

// TestTurnSpinnerBlankWhenIdleButStillReserved pins the reserved-row
// invariant: blank content, one row of budget.
func TestTurnSpinnerBlankWhenIdleButStillReserved(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.busy = false
	m.turnStartedAt = time.Time{}

	if row := m.renderTurnSpinner(); row != "" {
		t.Errorf("renderTurnSpinner() = %q, want empty when idle", row)
	}
	if got := m.turnSpinnerRows(); got != 1 {
		t.Errorf("turnSpinnerRows() = %d, want 1 even when idle", got)
	}
}

// During an SDD run the run panel owns the only spinner, so the pinned
// turn-spinner row collapses entirely — no blank reserved row.
func TestTurnSpinnerRowDroppedDuringSDD(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.SetSDDProgress(session.SDDProgress{Active: true})
	if got := m.turnSpinnerRows(); got != 0 {
		t.Errorf("turnSpinnerRows() = %d during SDD, want 0", got)
	}
	if row := m.renderTurnSpinner(); row != "" {
		t.Errorf("renderTurnSpinner() = %q during SDD, want empty", row)
	}
}

// TestTurnSpinnerGlyphGatedOnFirst200ms avoids a glyph flash on fast turns.
// TestTurnSpinnerSitsAboveTodos pins the row order: the spinner groups with
// the transcript whose progress it describes, and the todo list stays
// adjacent to the input.
func TestTurnSpinnerSitsAboveTodos(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	if err := m.state.SetTodos([]native.TodoItem{
		{Content: "first task", Status: "completed"},
		{Content: "second task", Status: "in_progress"},
		{Content: "third task", Status: "pending"},
		{Content: "fourth task", Status: "pending"},
	}); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	m.busy = true
	m.turnStartedAt = m.now().Add(-24 * time.Second)
	m.spinnerFrame = "⠹"
	m.refreshViewport()

	lines := strings.Split(stripANSI(m.viewString()), "\n")
	spinnerRow, firstTodoRow := -1, -1
	for i, l := range lines {
		if spinnerRow == -1 && strings.Contains(l, "24s") {
			spinnerRow = i
		}
		if firstTodoRow == -1 && strings.Contains(l, "second task") {
			firstTodoRow = i
		}
	}
	if spinnerRow == -1 || firstTodoRow == -1 {
		t.Fatalf("spinner row = %d, first todo row = %d; both must render", spinnerRow, firstTodoRow)
	}
	if spinnerRow > firstTodoRow {
		t.Errorf("spinner row %d is below first todo row %d, want above", spinnerRow, firstTodoRow)
	}
	if len(lines) != 30 {
		t.Errorf("frame = %d rows, want 30", len(lines))
	}
}

func TestTurnSpinnerGlyphGatedOnFirst200ms(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.busy = true
	m.spinnerFrame = "⠹"

	m.turnStartedAt = m.now().Add(-50 * time.Millisecond)
	if got := m.turnSpinnerFrame(); got != "" {
		t.Errorf("turnSpinnerFrame() = %q at 50ms, want empty", got)
	}
	m.turnStartedAt = m.now().Add(-time.Second)
	if got := m.turnSpinnerFrame(); got != "⠹" {
		t.Errorf("turnSpinnerFrame() = %q at 1s, want ⠹", got)
	}
}

// Rail panels paint no background: the ▍ rail carries the grouping, and a
// painted band renders as a light stripe around content on light/16-color
// terminal themes.
func TestChromeRailWidthPaintsNoBackground(t *testing.T) {
	out := chromeRailWidth(" row one\nrow two ", dimColor, 40)
	if strings.Contains(out, "48;") || strings.Contains(out, "[48;") {
		t.Fatalf("chromeRailWidth must not emit background SGR:\n%q", out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 railed lines, got %d:\n%q", len(lines), out)
	}
	for i, l := range lines {
		if !strings.Contains(stripANSI(l), "row") {
			t.Errorf("line %d lost its content: %q", i, l)
		}
	}
}

func TestChromeRailWidthTruncatesToWidth(t *testing.T) {
	out := chromeRailWidth(strings.Repeat("x", 50), dimColor, 20)
	plain := strings.TrimPrefix(ansi.Strip(out), glyph.Rail)
	if w := ansi.StringWidth(plain); w > 20 {
		t.Fatalf("content not truncated to width: %d > 20", w)
	}
}

// TestMouseToggleReleasesAndReclaims pins the Ctrl+S round trip. Mouse capture
// and native click-drag selection are mutually exclusive, and the default has
// flip-flopped in git history (39f0660 disabled capture for selection, 266f09e
// re-enabled it for wheel scrolling) because each side was only reachable by
// editing config and restarting. The toggle is what makes the default a
// preference rather than a decision, so it is worth guarding.
func TestMouseToggleReleasesAndReclaims(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("initial MouseMode = %v, want MouseModeCellMotion", got)
	}

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("Ctrl+S was not handled")
	}
	m2 := mm.(Model)
	if got := m2.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("after Ctrl+S MouseMode = %v, want MouseModeNone (native selection)", got)
	}

	mm, _, _ = m2.handleKeypress(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if got := mm.(Model).View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("after second Ctrl+S MouseMode = %v, want MouseModeCellMotion (wheel restored)", got)
	}
}
