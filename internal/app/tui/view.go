package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/livebox"
	"marshal/internal/app/tui/theme"
	"marshal/internal/viewmodel"
)

// stripANSI removes SGR escape sequences so tests can inspect visible runes
// without the styling the borders now carry.
func stripANSI(s string) string { return theme.ANSIRe.ReplaceAllString(s, "") }

const (
	transcriptFrameRows = 0
	// statusLineRows aliases layout.StatusLineRows. The source of truth
	// lives in internal/app/tui/layout; the alias keeps in-package call
	// sites (model.go, view_test.go, input_area_test.go) referencing the
	// unqualified name. The dock package duplicates the value because tui
	// imports dock but not vice versa; see dock/dock_test.go for the
	// guard test that pins the duplicate.
	statusLineRows     = layout.StatusLineRows
	completionPopupMax = 8
	// completionPanelChromeRows is the number of rows the chrome.Panel title
	// adds above the completion popup's match rows.
	completionPanelChromeRows = 1
	// minTranscriptRows is the transcript floor reserved when budgeting the
	// textarea's MaxHeight. The now bar still takes priority over the
	// transcript when space is tight — the progress row is most useful
	// while the agent is working — but the floor is a readable window rather
	// than a single row: the now bar, dock panel, and completion
	// popup all stack above the input, and a busy session could squeeze the
	// transcript to one line.
	minTranscriptRows = 4
)

// View assembles the full-screen frame. Alt screen and mouse mode are
// declared here — in Bubble Tea v2 they are View fields rather than
// program options.
func (m Model) View() tea.View {
	v := tea.NewView(m.viewString())
	v.AltScreen = true
	// Mouse capture is a trade, so it is configurable (tui.mouse_capture,
	// default on). With MouseModeCellMotion the wheel scrolls the
	// transcript — necessary because AltScreen means there is no terminal
	// scrollback to fall back on — and native click-drag text selection
	// needs the terminal's override modifier (Option/Alt in iTerm2, Ghostty,
	// Kitty, Terminal.app). With MouseModeNone the terminal owns the mouse
	// entirely and scrolling is keyboard-only (PgUp/PgDn/Ctrl+U/Ctrl+D/End).
	// Ctrl+S flips this per session without touching config, so a user who
	// wants to copy a block of output can release the mouse and take it back
	// again without editing a TOML file or restarting.
	if m.mouseReleased || (m.state != nil && !m.state.Config.TUI.MouseCapture) {
		v.MouseMode = tea.MouseModeNone
	} else {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func (m *Model) viewString() string {
	if m.width == 0 || m.height == 0 {
		return m.fallbackView()
	}
	if m.rawWidth < minTerminalWidth || m.rawHeight < minTerminalHeight {
		return m.tooSmallView()
	}
	// The prompt's own rows are part of the budget livebox.Plan computes, so
	// the review body's height must be settled before the tray is planned.
	m.syncPromptReviewHeights()
	m.reconcileWorkExpansion()
	layout := livebox.Plan(m.boxInput())
	m.boxMemo = &layout
	// The sheet's data func must see this frame's model, not the copy it was
	// opened from, or it would render stale values.
	if m.sheetPanel != nil {
		m.sheetPanel.SetData(m.sheetDataFor)
	}
	dockView := m.dock.View(m.leftWidth, m.height)
	m.dockFramed = chrome.IsFramed(dockView)
	m.updateViewportHeight()

	var left string
	if m.dock.FullFrameOpen() {
		// A FullFrame panel owns everything above the status line: the
		// transcript, now bar, and input area are hidden. It is padded to
		// the rows it owns, because a panel whose content is shorter than
		// the frame (the inspector on a short node) would otherwise leave
		// the frame short of the terminal height.
		left = padRows(dockView, m.height-statusLineRows)
	} else {
		rows := []string{m.renderTranscriptFrame()}
		// The tray pins the running work, the finished work held until the
		// next prompt, and the plan just above the input, planned once and
		// painted from that plan.
		if box := livebox.Render(layout, m.leftWidth); box != "" {
			rows = append(rows, box)
		}
		// A docked panel is an open frame. Under a framed tray it joins that
		// outline with tees instead of opening a second one inside it.
		if dockView != "" {
			if layout.Framed() {
				dockView = chrome.JoinTop(dockView)
			}
			rows = append(rows, dockView)
		}
		// An empty console is not appended at all: it happens when the access
		// panel owns the dock (the panel is the decision UI, so there is no
		// composer to frame), and appending "" would contribute one blank row
		// that no budget counted, pushing a transcript row out of the frame.
		if in := m.renderInputArea(); in != "" {
			rows = append(rows, in)
		}
		left = lipgloss.JoinVertical(lipgloss.Left, rows...)
	}
	// Hard invariant: the left column must never be taller than the frame
	// minus the status line. Every panel is budgeted (see the *Rows
	// helpers), but a budget miscount in any state used to push the input
	// area and status footer off the bottom of the screen. Clip surplus
	// rows from the top — the transcript is the topmost block and is
	// scrollable, so nothing the user must always see is lost.
	left = clipLeftColumn(left, m.height-statusLineRows)
	// The status row closes the console's outline. While a full-frame panel
	// is open that panel owns a closed frame of its own, so the row stays
	// plain rather than drawing a second bottom border under it.
	if m.dock.FullFrameOpen() {
		return lipgloss.JoinVertical(lipgloss.Left, left, m.renderStatusLine(m.width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, left, m.renderStatusBorder(m.width, m.consoleColor()))
}

// reconcileWorkExpansion clears the expanded card when its item has settled or
// disappeared: an expansion is a property of live work, so once the work is no
// longer live the next frame returns to the collapsed default. It never touches
// the composer or prompt focus, and it never stops any work.
func (m *Model) reconcileWorkExpansion() {
	if m.workExpanded == "" {
		return
	}
	for _, it := range m.workSnapshot.Items {
		if it.TaskKey == m.workExpanded && viewmodel.WorkActive(it) {
			return
		}
	}
	m.workExpanded = ""
}

// padRows grows s to exactly rows lines with blank ones, so a block that
// renders shorter than the space it owns still fills it.
func padRows(s string, rows int) string {
	if rows < 1 {
		return s
	}
	if h := lipgloss.Height(s); h < rows {
		s += strings.Repeat("\n", rows-h)
	}
	return s
}

// clipLeftColumnHook is a test seam: set, it is told how many rows
// clipLeftColumn trimmed. A non-zero trim means a height budget is wrong.
var clipLeftColumnHook func(trimmed int)

// clipLeftColumn trims s to at most maxRows, dropping surplus lines from
// the top so bottom chrome (input area, status line) stays on screen even
// if a panel budget miscounts. Under-height columns are returned as-is.
func clipLeftColumn(s string, maxRows int) string {
	if maxRows < 1 {
		maxRows = 1
	}
	height := lipgloss.Height(s)
	if height <= maxRows {
		return s
	}
	if clipLeftColumnHook != nil {
		clipLeftColumnHook(height - maxRows)
	}
	lines := strings.Split(s, "\n")
	return strings.Join(lines[height-maxRows:], "\n")
}

func (m Model) renderTranscriptFrame() string {
	vpWidth := max(m.leftWidth, 1)
	vpHeight := max(m.viewport.Height(), 1)
	if v, ok := m.drilledInto(); ok {
		return m.renderDrillFrame(v)
	}
	content := lipgloss.NewStyle().Width(vpWidth).Height(vpHeight).Render(m.paintSelection(m.viewport.View()))
	if m.scrollHintRows() > 0 {
		hint := mutedStyle().Render("↑ scrolled — End to follow")
		content = lipgloss.JoinVertical(lipgloss.Left, hint, content)
	}
	return chrome.PaintBand(content, m.leftWidth, theme.Current().TranscriptBG())
}

// renderInputArea draws the console: the input area's framed sections, closed
// below by the status border. The sections and their headers live in
// console.go.
func (m Model) renderInputArea() string {
	return renderConsole(m.inputSections(), m.consoleAbove(), m.leftWidth)
}

// suggestionGhost returns the unstyled ghost text to overlay on the input,
// or "" when no suggestion should be shown. The ghost is fish-style: when
// the typed value prefixes the suggestion, only the untyped suffix is
// shown; otherwise the full suggestion appears only when the input is
// empty. It is truncated to the remaining width of the cursor's display
// row so it never wraps onto the next row. Styling is applied by
// renderGhost, which needs the first rune on its own.
func (m Model) suggestionGhost() string {
	if m.suggestion == "" || m.suggestionDismissed || m.busy {
		return ""
	}
	// The composer owns the input: a ghost overlaying the base editor would
	// suggest text that Enter would not submit.
	if m.steeringComposer != nil {
		return ""
	}
	// The completion popup, approval, question, and skill-gate panels all
	// hide the textarea; a ghost would be a visual conflict, so suppress it.
	p := m.sess.Pending()
	if m.activeCompletionPopup() != nil || m.hasPendingApproval() || p.Question != nil || p.SkillGate != nil || p.Sudo != nil {
		return ""
	}
	// The ghost is spliced into a single rendered row. A newline would add
	// a row and break the input box's rectangle, so refuse rather than
	// corrupt the frame. (The LLM fallback only trims its result; it does
	// not guarantee a single line.)
	if strings.ContainsRune(m.suggestion, '\n') {
		return ""
	}
	// Fish-style: the ghost only appears when the cursor is at the end of
	// the input. The ghost suffix is computed from the full typed value,
	// while ghostPosition returns the cursor's column; the two agree only
	// at end-of-input, so drawing anywhere else would overwrite typed text.
	if !m.cursorAtEndOfInput() {
		return ""
	}
	value := m.input.Value()
	var ghost string
	if value == "" {
		ghost = m.suggestion
	} else if strings.HasPrefix(m.suggestion, value) {
		ghost = m.suggestion[len(value):]
	} else {
		return ""
	}
	// Budget against the cells left on the cursor's display row.
	// input.Width() is the text width, with the prompt's reserved cells
	// already subtracted, and CharOffset is the cursor's cell offset
	// within its row — so the two are in the same coordinate space.
	remaining := max(m.input.Width()-m.input.LineInfo().CharOffset, 1)
	return ansi.Truncate(ghost, remaining, "…")
}

// ghostPosition returns the display row and column, within the textarea's
// rendered view, at which the suggestion ghost should be drawn.
//
// The position comes from textarea model state, never from the rendered
// bytes. bubbles renders the virtual cursor as a reverse-video cell only
// on blink-on frames, so the previous approach — searching the frame for
// "\x1b[7;" — found the cursor on some frames and not others, and the
// ghost alternated between the cursor column and the far right edge at
// the blink rate.
//
// ok is false when the position cannot be determined, and the caller then
// draws nothing.
func (m Model) ghostPosition() (row, col int, ok bool) {
	// A ghost is only ever drawn when the typed value is empty or prefixes
	// a single-line suggestion, so the cursor is always on the first
	// logical line. Anything else is outside the contract: textarea
	// exposes no way to count the display rows of preceding logical lines,
	// and guessing is what produced the jump.
	if m.input.Line() != 0 {
		return 0, 0, false
	}
	info := m.input.LineInfo()
	// CharOffset, despite the name, is the cursor's display-cell offset
	// within its wrapped row; ColumnOffset is a rune count. textarea's own
	// Cursor() computes its x offset from CharOffset plus the prompt width
	// the same way.
	return info.RowOffset, inputPromptWidth + info.CharOffset, true
}

// renderGhost styles plain ghost text for overlay. Its first cell wears
// the textarea's cursor block — reverse video in the accent colour,
// matching what bubbles' virtual cursor renders on a blink-on frame — so
// the cursor sits *on* the ghost's first character, which is the fish
// appearance. The rest is muted. Because this block is static, the cursor
// stops blinking while a ghost is shown; that is the point, since the
// blink is what the ghost used to chase.
func renderGhost(text string) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	cursor := lipgloss.NewStyle().Foreground(accentColor).Reverse(true).Render(string(runes[0]))
	if len(runes) == 1 {
		return cursor
	}
	return cursor + mutedStyle().Render(string(runes[1:]))
}

// overlayGhost draws ghost onto the given display row of view starting at
// display column col, overwriting the cells it covers — including the
// cursor cell, which is why renderGhost styles the ghost's first rune as
// the cursor block. Overwriting (rather than inserting) keeps the row's
// total display width unchanged, so the input box keeps its rectangle.
//
// Cuts are ANSI-aware: ansi.Truncate and ansi.TruncateLeft cut on cell
// boundaries and re-emit the styling in force, so no byte index ever
// lands inside an escape sequence. An out-of-range row or column returns
// view unchanged.
func overlayGhost(view string, row, col int, ghost string) string {
	lines := strings.Split(view, "\n")
	if row < 0 || row >= len(lines) {
		return view
	}
	line := lines[row]
	if col < 0 || col >= ansi.StringWidth(line) {
		return view
	}
	head := ansi.Truncate(line, col, "")
	tail := ansi.TruncateLeft(line, col+ansi.StringWidth(ghost), "")
	lines[row] = head + ghost + tail
	return strings.Join(lines, "\n")
}

// highlightMatches bolds runes at the given byte indices using the
// active theme's AccentPrimary color. The indices are byte positions in
// the (ASCII-dominated) text. For non-ASCII text the highlight may
// misalign on multi-byte runes — acceptable for file paths/commands.
func highlightMatches(text string, idxs []int) string {
	if len(idxs) == 0 {
		return text
	}
	iSet := make(map[int]bool, len(idxs))
	for _, i := range idxs {
		iSet[i] = true
	}
	var b strings.Builder
	hl := lipgloss.NewStyle().Bold(true).Foreground(activeTheme.AccentPrimary)
	for i, r := range []rune(text) {
		if iSet[i] {
			b.WriteString(hl.Render(string(r)))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (m Model) tooSmallView() string {
	boxW := max(m.rawWidth, 1)
	boxH := max(m.rawHeight, 1)
	msg := fmt.Sprintf("Terminal too small\nResize to at least %d×%d", minTerminalWidth, minTerminalHeight)
	wrapped := ansi.Wrap(msg, boxW, WrapBreakpoints)
	trimmedLines := strings.Split(wrapped, "\n")
	if len(trimmedLines) > boxH {
		trimmedLines = trimmedLines[:boxH]
	}
	for i, line := range trimmedLines {
		trimmedLines[i] = ansi.Cut(line, 0, boxW)
	}
	return lipgloss.Place(boxW, boxH, lipgloss.Center, lipgloss.Center,
		mutedStyle().Render(strings.Join(trimmedLines, "\n")),
	)
}

func (m Model) fallbackView() string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		mutedStyle().Render("Marshal — waiting for terminal resize..."),
	)
}
