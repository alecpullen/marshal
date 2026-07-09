package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
)

func newViewTestModel(t *testing.T, width, height int) Model {
	t.Helper()
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
	if !strings.HasPrefix(stripANSI(busyLines[inputTop]), "╭") {
		t.Fatalf("input box top moved; line %d = %q", inputTop, busyLines[inputTop])
	}
	activityRow := inputTop + activityStripRows
	if !strings.Contains(busyLines[activityRow], "thinking") {
		t.Fatalf("activity row moved; line %d = %q", activityRow, busyLines[activityRow])
	}
}

func TestViewFitsTerminalSizesSingleColumn(t *testing.T) {
	sizes := [][2]int{{40, 10}, {80, 24}, {100, 30}, {120, 40}}
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

	if !strings.Contains(view, "✘ provider: connection refused") {
		t.Fatalf("provider error not rendered inline:\n%s", view)
	}
	if !strings.Contains(view, "hello") {
		t.Fatal("provider error must not hide the transcript")
	}
}

func TestResizeComputesSingleColumnGeometry(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	if m.viewport.Width() != 98 {
		t.Fatalf("viewport.Width = %d, want 98 (width-2, borderless transcript)", m.viewport.Width())
	}
	wantHeight := 30 - transcriptFrameRows - m.inputAreaRows() - statusLineRows
	if m.viewport.Height() != wantHeight {
		t.Fatalf("viewport.Height = %d, want %d", m.viewport.Height(), wantHeight)
	}
}

func TestInputAreaHasNoBackgroundFill(t *testing.T) {
	m := newViewTestModel(t, 60, 20)
	out := m.renderInputArea()
	// panelBg 235 must never be emitted as a fill anymore.
	if strings.Contains(out, "48;5;235") || strings.Contains(out, ";235m") {
		t.Fatalf("input area still emits panel background fill:\n%q", out)
	}
	if !strings.Contains(stripANSI(out), "❯") {
		t.Fatalf("input area missing prompt:\n%q", stripANSI(out))
	}
}

func TestInputBorderColorReflectsFocus(t *testing.T) {
	m := newViewTestModel(t, 60, 20)
	if !strings.Contains(m.renderInputArea(), "209") {
		t.Fatal("focused input box should use coral (209) border")
	}
	m.input.Blur()
	if !strings.Contains(m.renderInputArea(), "245") {
		t.Fatal("blurred input box should use mauve (245) border")
	}
}

func TestLongInputExpandsToMultipleRows(t *testing.T) {
	m := newViewTestModel(t, 50, 20)
	singleLineRows := m.inputAreaRows()

	longInput := strings.Repeat("wrap me ", 10)
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
	m := newViewTestModel(t, 50, 20)

	// Type a line long enough to soft-wrap at the textarea's text width
	// (50 - 4 box frame - 2 prompt = 44 text columns). Use 60 chars.
	longInput := strings.Repeat("a", 60)
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
	m := newViewTestModel(t, 60, 20)
	m.input.SetValue("hello")
	out := stripANSI(m.renderInputArea())
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines (border + content + border), got %d:\n%s", len(lines), out)
	}

	contentStart := -1
	contentEnd := -1
	for i, line := range lines {
		if strings.Contains(line, "╭") {
			contentStart = i + 1
		}
		if strings.Contains(line, "╰") && contentEnd < 0 {
			contentEnd = i
		}
	}
	if contentStart < 0 || contentEnd < 0 || contentStart >= contentEnd {
		t.Fatalf("could not find content rows between borders:\n%s", out)
	}

	promptCount := 0
	blankCount := 0
	for i := contentStart; i < contentEnd; i++ {
		line := lines[i]
		if strings.Contains(line, "❯") {
			promptCount++
		}
		inner := strings.TrimPrefix(line, "│")
		inner = strings.TrimSuffix(inner, "│")
		if strings.TrimSpace(inner) == "" {
			blankCount++
		}
	}

	if promptCount != 1 {
		t.Fatalf("expected exactly 1 content row with ❯, got %d:\n%s", promptCount, out)
	}
	if blankCount != 0 {
		t.Fatalf("expected 0 blank content rows, got %d:\n%s", blankCount, out)
	}
}

func TestInputWrapsBeforeBoxContentWidth(t *testing.T) {
	for _, w := range []int{50, 60, 80, 100} {
		m := newViewTestModel(t, w, 20)
		m.input.SetValue(strings.Repeat("a", 400))
		out := stripANSI(m.renderInputArea())
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

		if len(lines) < 3 {
			t.Fatalf("width=%d: expected at least 3 lines, got %d:\n%s", w, len(lines), out)
		}

		contentStart := -1
		contentEnd := -1
		contentWidth := 0
		for i, line := range lines {
			if strings.Contains(line, "╭") {
				contentStart = i + 1
				contentWidth = visibleRunes(line) - 2
			}
			if strings.Contains(line, "╰") && contentEnd < 0 {
				contentEnd = i
			}
		}
		if contentStart < 0 || contentEnd < 0 || contentStart >= contentEnd {
			t.Fatalf("width=%d: could not find content rows between borders:\n%s", w, out)
		}

		promptOnOwnRow := false
		for i := contentStart; i < contentEnd; i++ {
			line := lines[i]
			inner := strings.TrimPrefix(line, "│")
			inner = strings.TrimSuffix(inner, "│")
			trimmed := strings.TrimSpace(inner)
			if trimmed == "❯" {
				promptOnOwnRow = true
			}
			if visibleRunes(inner) > contentWidth {
				t.Fatalf("width=%d: content row exceeds content width %d (%d): %q", w, contentWidth, visibleRunes(inner), line)
			}
		}

		if promptOnOwnRow {
			t.Fatalf("width=%d: ❯ is on its own row (prompt split from text):\n%s", w, out)
		}

		promptRow := ""
		for i := contentStart; i < contentEnd; i++ {
			if strings.Contains(lines[i], "❯") {
				promptRow = lines[i]
				break
			}
		}
		if !strings.Contains(promptRow, "a") {
			t.Fatalf("width=%d: prompt row has no text after ❯:\n%s", w, out)
		}
	}
}

func TestMouseCaptureDisabled(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("View().MouseMode = %v, want MouseModeNone (native selection enabled)", got)
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
		Handler: func(s *session.State, args []string) string { return "" },
	})
	mustRegister(t, reg, commands.Command{
		Name: "help", Description: "Show help", Args: "",
		Handler: func(s *session.State, args []string) string { return "" },
	})
	mustRegister(t, reg, commands.Command{
		Name: "tools", Description: "List tools", Args: "",
		Handler: func(s *session.State, args []string) string { return "" },
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
		Handler: func(s *session.State, args []string) string { return "" },
	})
	mustRegister(t, reg, commands.Command{
		Name: "help", Description: "Show help", Args: "",
		Handler: func(s *session.State, args []string) string { return "" },
	})
	m := New(state, WithCommandRegistry(reg), WithFileIndex(paths))
	m.resize(width, height)
	m.refreshViewport()
	return m
}
