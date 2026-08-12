package tui

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
	"marshal/internal/strutil"
)

// ansiRe matches SGR (and empty) escape sequences that lipgloss emits.
// Shared between production rendering helpers and tests that need to
// inspect visible runes without the styling the borders now carry.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

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
	// textarea's MaxHeight. The todo panel still takes priority over the
	// transcript when space is tight — the pinned todo list is most useful
	// while the agent is working — but the floor is a readable window rather
	// than a single row: the todo panel, SDD panel, live strip, and completion
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
	dockView := m.dock.View(m.leftWidth, m.height)
	m.updateViewportHeight()

	// The SDD run panel is a full-width top bar rendered above the left
	// column and the side rail. It owns the only spinner on screen during a
	// run, so the turn spinner row collapses entirely (see turnSpinnerRows).
	topBar := m.renderRunPanel()

	var left string
	if m.dock.FullFrameOpen() {
		// A FullFrame panel owns everything above the status line: the
		// transcript, todo panel, run panel, live strip, and input area are hidden.
		left = dockView
	} else {
		rows := []string{m.renderTranscriptFrame()}
		// The spinner groups with the transcript whose progress it
		// describes, keeping the todo list adjacent to the input. During
		// an SDD run the top bar owns the only spinner, so this row
		// collapses entirely (see turnSpinnerRows). An idle spinner
		// renders "", which JoinVertical would pad into a blank row
		// above the todo panel — skip it instead.
		if spinner := m.renderTurnSpinner(); spinner != "" {
			rows = append(rows, spinner)
		}
		if todo := m.renderTodoPanel(); todo != "" {
			rows = append(rows, todo)
		}
		if strip := m.renderLiveStrip(); strip != "" {
			rows = append(rows, strip)
		}
		if dockView != "" {
			rows = append(rows, dockView)
		}
		rows = append(rows, m.renderInputArea())
		left = lipgloss.JoinVertical(lipgloss.Left, rows...)
	}
	// Hard invariant: the left column must never be taller than the frame
	// minus the status line and any top bar. Every panel is budgeted (see
	// the *Rows helpers), but a budget miscount in any state used to push
	// the input area and status footer off the bottom of the screen. Clip
	// surplus rows from the top — the transcript is the topmost block and
	// is scrollable, so nothing the user must always see is lost.
	leftHeight := m.height - statusLineRows
	if topBar != "" {
		leftHeight -= lipgloss.Height(topBar)
	}
	left = clipLeftColumn(left, leftHeight)
	if m.railEnabled() {
		railHeight := m.height - statusLineRows
		if rv := m.rail.View(m.railData(), m.railWidth, railHeight); rv != "" {
			left = lipgloss.JoinHorizontal(lipgloss.Top, left, rv)
		}
	}
	if topBar != "" {
		return lipgloss.JoinVertical(lipgloss.Left, topBar, left, m.renderStatusLine(m.width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, left, m.renderStatusLine(m.width))
}

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
	lines := strings.Split(s, "\n")
	return strings.Join(lines[height-maxRows:], "\n")
}

func (m Model) renderTranscriptFrame() string {
	vpWidth := max(m.leftWidth, 1)
	vpHeight := max(m.viewport.Height(), 1)
	content := lipgloss.NewStyle().Width(vpWidth).Height(vpHeight).Render(m.viewport.View())
	if m.scrollHintRows() > 0 {
		hint := mutedStyle().Render("↑ scrolled — End to follow")
		content = lipgloss.JoinVertical(lipgloss.Left, hint, content)
	}
	if v, ok := m.drilledInto(); ok {
		label := v.Label
		if v.Model != "" {
			if v.Provider != "" {
				label += fmt.Sprintf(" · %s @ %s", v.Model, v.Provider)
			} else {
				label += " · " + v.Model
			}
		}
		if v.Fallback {
			label += " (fallback)"
		}
		crumb := lipgloss.NewStyle().Foreground(accentColor).Render(glyph.Brand+" orchestrator") +
			mutedStyle().Render(" "+glyph.Running+" ") +
			lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(glyph.Agent+" "+label) +
			mutedStyle().Render("  (↑/Esc to go back)")
		content = lipgloss.JoinVertical(lipgloss.Left, crumb, content)
	}
	return content
}

// renderTurnSpinner renders the pinned spinner row directly above the todo
// panel. It answers one question — is the agent still running? — and so is
// driven by the turn-level busy flag rather than session.Activity, which
// resets to ActivityIdle between phases. Elapsed time only: the phase detail
// belongs to the transcript's live blocks.
//
// The row is always reserved (see turnSpinnerRows); it renders blank while
// idle so the transcript frame does not shift when a turn starts.
func (m Model) renderTurnSpinner() string {
	if !m.busy || m.turnStartedAt.IsZero() {
		return ""
	}
	elapsed := m.now().Sub(m.turnStartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	text := spinnerLabel(m.turnSpinnerFrame(), formatElapsed(elapsed))
	return statusBusyStyle().Render(" " + strutil.Truncate(text, max(m.leftWidth-1, 1), false))
}

func (m Model) renderInputArea() string {
	inputInnerWidth := max(m.leftWidth-4, 1)
	rows := make([]string, 0, 4)

	if q := m.state.PendingQuestion(); q != nil {
		if m.questionModel != nil {
			rows = append(rows, m.questionModel.View())
		} else {
			rows = append(rows, renderQuestionPanel(q, inputInnerWidth))
		}
		// The main textarea is deliberately NOT rendered here: while a
		// question is pending every keypress routes to the question form
		// (handleQuestion), so a visible textarea would be a dead input
		// that swallows nothing yet appears typable.
	} else if tc, _ := m.pendingApprovalDisplay(); tc != nil {
		switch {
		case isModeElevationApproval(tc):
			// The mode-elevation dock picker owns this decision; rendering
			// the approve/deny panel here too showed both UIs at once.
		case m.editingCommand:
			rows = append(rows, m.gutteredInput())
		case m.approvalModel != nil:
			rows = append(rows, m.approvalModel.View())
		default:
			rows = append(rows, renderApprovalPanel(tc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, inputInnerWidth))
		}
	} else {
		if m.state.SDDProgress().Active {
			rows = append(rows, mutedStyle().Render("SDD running — /stop to cancel, wait for completion to resume typing"))
		}
		if popup := m.renderCompletionPopup(); popup != "" {
			rows = append(rows, popup)
		}
		rows = append(rows, m.gutteredInput())
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// inputBarColor picks the ▍ state-bar color. This is the input box's old
// border-color semantics compressed into one cell (spec: "state moves to
// the ▍❯ prompt").
func (m Model) inputBarColor() color.Color {
	switch {
	case m.successPulse:
		return successColor
	case m.state.PendingQuestion() != nil:
		return violetColor
	case m.hasPendingApproval():
		return warningColor
	case m.state.SDDProgress().Active, !m.input.Focused():
		return dimColor
	default:
		return accentColor
	}
}

// gutteredInput renders the textarea with the ▍ state bar prepended to
// every display line, and overlays the active next-prompt suggestion as
// grey ghost text immediately after the cursor on the cursor line.
func (m Model) gutteredInput() string {
	bar := lipgloss.NewStyle().Foreground(m.inputBarColor()).Render(glyph.Rail)
	view := m.input.View()
	ghost := m.suggestionGhost()
	if ghost != "" {
		view = insertGhostAfterCursor(view, ghost)
	}
	lines := strings.Split(view, "\n")
	for i := range lines {
		lines[i] = bar + lines[i]
	}
	return strings.Join(lines, "\n")
}

// suggestionGhost returns the styled ghost text to overlay on the input, or
// "" when no suggestion should be shown. The ghost is fish-style: when the
// typed value prefixes the suggestion, only the untyped suffix is shown;
// otherwise the full suggestion appears only when the input is empty. It is
// rendered in the theme's muted style and truncated to the remaining width
// of the cursor row so it never wraps onto the next line.
func (m Model) suggestionGhost() string {
	if m.suggestion == "" || m.suggestionDismissed || m.busy {
		return ""
	}
	// The completion popup, approval, and question panels all hide the
	// textarea; a ghost would be a visual conflict, so suppress it.
	if m.activeCompletionPopup() != nil || m.hasPendingApproval() || m.state.PendingQuestion() != nil {
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
	// Truncate to the remaining width of the cursor row (fish-shell style;
	// no wrapping onto the next row). The ▍ rail and ❯ prompt reserve
	// cells, so budget against the input width.
	remaining := max(m.input.Width()-len([]rune(value)), 1)
	ghost = ansi.Truncate(ghost, remaining, "…")
	return mutedStyle().Render(ghost)
}

// insertGhostAfterCursor inserts ghost (already styled) into the textarea
// view immediately after the cursor's reverse-video marker on the cursor
// line. The cursor is rendered by bubbles as "\x1b[7;<color>m<char>\x1b[m";
// we locate that sequence and splice the ghost in after it. If the cursor
// marker cannot be found (e.g. placeholder view), the ghost is appended to
// the first line instead.
func insertGhostAfterCursor(view, ghost string) string {
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		// The cursor's reverse-video sequence always starts with "\x1b[7;".
		idx := strings.Index(line, "\x1b[7;")
		if idx < 0 {
			continue
		}
		// Find the end of the cursor's SGR reset ("\x1b[m") after the char.
		rest := line[idx:]
		end := strings.Index(rest, "\x1b[m")
		if end < 0 {
			continue
		}
		insertAt := idx + end + len("\x1b[m")
		lines[i] = line[:insertAt] + ghost + line[insertAt:]
		return strings.Join(lines, "\n")
	}
	// Fallback: no cursor marker found — append to the first line.
	if len(lines) > 0 {
		lines[0] += ghost
	}
	return strings.Join(lines, "\n")
}

// chromeRail prefixes every line of s with the ▍ state bar in color c — the
// same rail the input box wears — so stacked panels (approval, question,
// todos) read as one contained unit with the input. A trailing newline on s
// is preserved; empty input stays empty.
func chromeRail(s string, c color.Color) string {
	return chromeRailWidth(s, c, 0)
}

// chromeRailWidth is chromeRail with lines truncated to w cells. The ▍
// rail carries the panel grouping on its own — a painted surface band
// renders as a light stripe around content on light terminal themes and
// in the 16-color palette (BGSurface = ANSI bright black), so panels paint
// no background. w <= 0 skips truncation.
func chromeRailWidth(s string, c color.Color, w int) string {
	if s == "" {
		return ""
	}
	trailing := strings.HasSuffix(s, "\n")
	body := strings.TrimRight(s, "\n")
	bar := lipgloss.NewStyle().Foreground(c).Render(glyph.Rail)
	lines := strings.Split(body, "\n")
	for i := range lines {
		if w > 0 {
			lines[i] = ansi.Truncate(lines[i], w, "…")
		}
		lines[i] = bar + lines[i]
	}
	out := strings.Join(lines, "\n")
	if trailing {
		out += "\n"
	}
	return out
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

// renderCompletionPopup renders the active F18 completion popup as a
// guttered panel above the input area, using the same chrome.Panel dressing
// as settings, pickers, and docked panels. Returns "" when no popup is
// visible. Capped at completionPopupMax match rows. The popup scrolls so the
// selected item (p.index) is always visible.
func (m Model) renderCompletionPopup() string {
	p := m.activeCompletionPopup()
	if p == nil {
		return ""
	}
	matches := p.matches()
	if len(matches) == 0 {
		return ""
	}
	width := max(m.leftWidth-4, 1)
	inner := max(width-2, 1) // chrome.Panel reserves 2 cells for the ▍ gutter
	rows := make([]string, 0, completionPopupMax)
	limit := completionPopupMax
	if len(matches) < limit {
		limit = len(matches)
	}
	// The popup's reconcileOffset() is the single source of truth for
	// scroll position. The renderer only reads p.viewOffset.
	offset := p.viewOffset
	for i := 0; i < limit; i++ {
		mi := offset + i
		marker := "  "
		style := mutedStyle()
		if mi == p.index {
			marker = "▸ "
			style = promptPrefixStyle()
		}
		display := matches[mi].Text
		if matches[mi].Kind == completionCommand && !strings.HasPrefix(display, "/") {
			display = "/" + display
		}
		row := marker + highlightMatches(display, matches[mi].matchedIdxs)
		if matches[mi].Description != "" {
			row += "  " + matches[mi].Description
		}
		row = ansi.Cut(row, 0, inner)
		rows = append(rows, style.Render(row))
	}
	title := "Commands"
	if p == m.filePopup {
		title = "Files"
	} else if p == m.setPopup {
		title = "Settings"
	}
	return chrome.PanelWithHints(title, "↑↓ select · ↵ accept · esc dismiss",
		strings.Join(rows, "\n"), width, limit+1, true, theme.Current())
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
