// internal/tui/main_panel.go
// The main panel owns the continuous scroll surface (task blocks) and the
// permanently anchored prompt bar. It is the primary interaction surface.
//
// Bubbles used:
//   viewport.Model  — scrollable log area (replaces manual slice/offset math)
//   textinput.Model — single-line prompt (replaces manual rune buffer + cursor)

package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Log line ──────────────────────────────────────────────────────────────────

type lineKind int

const (
	lineExec       lineKind = iota // executor output, first chunk line
	lineCritic                     // critic output, first chunk line
	lineCont                       // continuation / code / diff
	lineSuccess                    // starts with ✓
	lineError                      // starts with ✗
	lineWarning                    // warning
	lineThink                      // R1 think-block content
	lineQueued                     // "queued: <desc>" confirmation
	lineRoundSep                   // ── round N ──
	lineCursor                     // blank line holding streaming cursor
	lineSystem                     // :help output, muted italic
	lineCompaction                 // compaction summary displayed
)

// LogLine is the unit the loop engine appends to the panel.
type LogLine struct {
	Kind    lineKind
	Content string
	Round   int // only for lineRoundSep
}

// ── Task block ────────────────────────────────────────────────────────────────

type blockState int

const (
	blockQueued  blockState = iota
	blockRunning            // currently executing, no footer yet
	blockPass
	blockFail
)

type taskBlock struct {
	id             string
	description    string
	state          blockState
	lines          []LogLine
	sha            string // commit SHA on pass
	completedAt    time.Time
	thinkCollapsed bool   // per-block think-block visibility
	thinkContent   string // stored think-block content
}

// ── Main model ────────────────────────────────────────────────────────────────

type MainModel struct {
	blocks []taskBlock

	// Bubbles primitives
	viewport viewport.Model
	input    textinput.Model

	// Log streaming cursor (separate from textinput cursor)
	logCursorOn bool

	// Global think-block visibility toggle (t key)
	thinkCollapsed bool

	// Per-block think collapse states (for T key - toggle focused block)
	focusedBlockIdx int

	// Block line offsets — index of first viewport line for each block (parallel to blocks slice)
	blockLineOffsets []int

	// Prompt history
	promptHistory []string
	historyIdx    int

	openComposer  bool // signal to app.go
	scrolledAlert bool // show dot in statusbar

	// Persisted width from WindowSizeMsg; used by renderAllBlocks
	width int
}

// logCursorTickMsg drives the blinking cursor shown at the end of running blocks.
type logCursorTickMsg struct{}

func newMainModel() MainModel {
	ti := textinput.New()
	ti.Prompt = "" // prefix rendered separately
	ti.CharLimit = 0
	ti.TextStyle = stylePromptInput
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(colBl)
	ti.Focus()

	vp := viewport.New(0, 0)
	vp.Style = styleBlockBody

	return MainModel{
		viewport: vp,
		input:    ti,
	}
}

func (m MainModel) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		tea.Tick(530*time.Millisecond, func(t time.Time) tea.Msg { return logCursorTickMsg{} }),
	)
}

// IsCommandMode returns true when the prompt starts with ":".
func (m MainModel) IsCommandMode() bool {
	return strings.HasPrefix(m.input.Value(), ":")
}

// ── Content management ────────────────────────────────────────────────────────

// rebuildContent re-renders all blocks and pushes the result into the viewport.
// Must be called whenever blocks change. Uses m.width, so no-ops when width=0.
func (m *MainModel) rebuildContent() {
	if m.width <= 0 {
		return
	}
	m.blockLineOffsets = make([]int, len(m.blocks))
	var all []string
	for i, block := range m.blocks {
		m.blockLineOffsets[i] = len(all)
		all = append(all, m.renderBlock(block, m.width)...)
	}
	m.viewport.SetContent(strings.Join(all, "\n"))
}

// ── Public API called by app.go ───────────────────────────────────────────────

func (m *MainModel) Enqueue(description string) tea.Cmd {
	id := fmt.Sprintf("task-%d", len(m.blocks)+1)
	m.blocks = append(m.blocks, taskBlock{
		id:          id,
		description: description,
		state:       blockQueued,
	})
	m.rebuildContent()
	return nil
}

func (m *MainModel) SetRunning(id string) {
	for i := range m.blocks {
		if m.blocks[i].id == id {
			m.blocks[i].state = blockRunning
			m.rebuildContent()
			return
		}
	}
}

func (m *MainModel) SetComplete(id string, passed bool, sha string) {
	for i := range m.blocks {
		if m.blocks[i].id == id {
			if passed {
				m.blocks[i].state = blockPass
				m.blocks[i].sha = sha
			} else {
				m.blocks[i].state = blockFail
			}
			m.blocks[i].completedAt = time.Now()
			m.rebuildContent()
			m.viewport.GotoBottom()
			m.scrolledAlert = false
			return
		}
	}
}

func (m *MainModel) AppendLine(line LogLine) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].state == blockRunning {
			m.blocks[i].lines = append(m.blocks[i].lines, line)
			break
		}
	}
	wasAtBottom := m.viewport.AtBottom()
	m.rebuildContent()
	if wasAtBottom {
		m.viewport.GotoBottom()
		m.scrolledAlert = false
	} else {
		m.scrolledAlert = true
	}
}

// SetThinkBlock sets the think-block content for the currently running block.
func (m *MainModel) SetThinkBlock(content string) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].state == blockRunning {
			m.blocks[i].thinkContent = content
			break
		}
	}
	wasAtBottom := m.viewport.AtBottom()
	m.rebuildContent()
	if wasAtBottom {
		m.viewport.GotoBottom()
		m.scrolledAlert = false
	} else {
		m.scrolledAlert = true
	}
}

// ── Bubble Tea ────────────────────────────────────────────────────────────────

// ClearLogMsg clears all task blocks from the log panel.
type ClearLogMsg struct{}

func (m MainModel) Update(msg tea.Msg) (MainModel, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	// ── Window resize ─────────────────────────────────────────────────────────
	case tea.WindowSizeMsg:
		const headerH, statusH, promptH = 2, 2, 3
		mainW := msg.Width - sidebarWidth - 1
		logH := msg.Height - headerH - statusH - promptH
		if mainW < 0 {
			mainW = 0
		}
		if logH < 0 {
			logH = 0
		}
		m.width = mainW
		m.viewport.Width = mainW
		m.viewport.Height = logH
		m.rebuildContent()
		m.viewport.GotoBottom()
		return m, nil

	// ── Log streaming cursor ──────────────────────────────────────────────────
	case logCursorTickMsg:
		m.logCursorOn = !m.logCursorOn
		m.rebuildContent() // re-render to show/hide cursor glyph
		return m, tea.Tick(530*time.Millisecond, func(t time.Time) tea.Msg { return logCursorTickMsg{} })

	// ── Clear log ─────────────────────────────────────────────────────────────
	case ClearLogMsg:
		m.blocks = nil
		m.blockLineOffsets = nil
		m.viewport.SetContent("")
		m.scrolledAlert = false
		return m, nil

	// ── Jump to task block ────────────────────────────────────────────────────
	case JumpToTaskMsg:
		for i, b := range m.blocks {
			if b.id == msg.ID && i < len(m.blockLineOffsets) {
				m.viewport.SetYOffset(m.blockLineOffsets[i])
				m.scrolledAlert = !m.viewport.AtBottom()
			}
		}
		return m, nil

	// ── Keys ──────────────────────────────────────────────────────────────────
	case tea.KeyMsg:
		var skipInput bool

		switch msg.String() {

		// Scroll the viewport
		case "up", "down", "pgup", "pgdown":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			cmds = append(cmds, cmd)
			m.scrolledAlert = !m.viewport.AtBottom()
			skipInput = true

		// t: toggle all think-block visibility
		case "t":
			if !m.IsCommandMode() {
				m.thinkCollapsed = !m.thinkCollapsed
				// Update all blocks to match global state
				for i := range m.blocks {
					m.blocks[i].thinkCollapsed = m.thinkCollapsed
				}
				m.rebuildContent()
				skipInput = true
			}

		// T: toggle focused block's think-block visibility
		case "T":
			if !m.IsCommandMode() && len(m.blocks) > 0 {
				// Find which block is at the current viewport position
				offset := m.viewport.YOffset
				for i, blockOffset := range m.blockLineOffsets {
					if i < len(m.blocks) && offset >= blockOffset {
						m.focusedBlockIdx = i
					} else {
						break
					}
				}
				// Toggle the focused block
				if m.focusedBlockIdx < len(m.blocks) {
					m.blocks[m.focusedBlockIdx].thinkCollapsed = !m.blocks[m.focusedBlockIdx].thinkCollapsed
					m.rebuildContent()
				}
				skipInput = true
			}

		// Esc: clear prompt
		case "esc":
			m.input.SetValue("")
			skipInput = true

		// Enter: submit task or execute command
		case "enter":
			val := m.input.Value()
			if val != "" {
				m.promptHistory = append(m.promptHistory, val)
				m.historyIdx = len(m.promptHistory)
				m.input.SetValue("")

				if strings.HasPrefix(val, ":") {
					cmds = append(cmds, func() tea.Msg {
						name, args, err := parseCommand(val)
						if err != nil {
							return LogLineMsg{Line: LogLine{Kind: lineError, Content: err.Error()}}
						}
						return ExecCommandMsg{Name: name, Args: args}
					})
				} else {
					task := val
					cmds = append(cmds, func() tea.Msg { return SubmitTaskMsg{Task: task} })
				}
			}
			skipInput = true

		// Right arrow: accept ghost suggestion when at end in command mode
		case "right":
			val := m.input.Value()
			if m.IsCommandMode() && m.input.Position() == len([]rune(val)) {
				partial := strings.TrimPrefix(val, ":")
				if !strings.Contains(partial, " ") && partial != "" {
					if suggestion, _ := completeCommand(partial); suggestion != "" && suggestion != partial {
						m.input.SetValue(":" + suggestion)
						m.input.CursorEnd()
						skipInput = true
					}
				}
			}
		}

		if !skipInput {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			cmds = append(cmds, cmd)
		}

	// ── Everything else (cursor blink ticks, etc.) → textinput ───────────────
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m MainModel) View(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}

	promptH := 3 // separator + input + hint
	logH := h - promptH
	if logH < 0 {
		logH = 0
	}

	// Apply dimensions to local copy for this frame (persists via WindowSizeMsg).
	m.viewport.Width = w
	m.viewport.Height = logH

	parts := []string{m.viewport.View()}
	if m.scrolledAlert {
		parts = append(parts,
			lipgloss.NewStyle().Foreground(colTx3).Italic(true).Width(w).
				Render("  ↑ scrolled · end to jump to bottom"),
		)
	}
	parts = append(parts, m.renderPromptBar(w))
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m MainModel) renderPromptBar(w int) string {
	cmdMode := m.IsCommandMode()
	val := m.input.Value()

	// Compute inline ghost suggestion (command mode, cursor at end, unambiguous)
	ghost := ""
	if cmdMode && m.input.Position() == len([]rune(val)) {
		partial := strings.TrimPrefix(val, ":")
		if !strings.Contains(partial, " ") && partial != "" {
			if suggestion, _ := completeCommand(partial); suggestion != "" && suggestion != partial {
				ghost = suggestion[len([]rune(partial)):]
			}
		}
	}

	// Style prefix and input text based on mode
	var prefix string
	if cmdMode {
		prefix = lipgloss.NewStyle().Foreground(colAm).Render("› ")
		m.input.TextStyle = lipgloss.NewStyle().Foreground(colAm)
		m.input.Cursor.Style = lipgloss.NewStyle().Foreground(colAm)
	} else {
		prefix = stylePromptPrefix.Render()
		m.input.TextStyle = stylePromptInput
		m.input.Cursor.Style = lipgloss.NewStyle().Foreground(colBl)
	}

	ghostRendered := stylePromptGhost.Render(ghost)
	prefixW := lipgloss.Width(prefix)
	ghostW := lipgloss.Width(ghostRendered)
	inputW := w - prefixW - ghostW - 2 // 2 for bar padding
	if inputW < 0 {
		inputW = 0
	}
	m.input.Width = inputW

	sep := stylePromptSep.Render(strings.Repeat("─", w))
	inputLine := stylePromptBar.Width(w).Render(prefix + m.input.View() + ghostRendered)

	var hint string
	switch {
	case cmdMode && ghost != "":
		hint = "  tab/→ accept · esc cancel"
	case cmdMode:
		hint = "  esc cancel"
	case val == "":
		hint = "  ↵ run · tab focus · : command · esc clear"
	default:
		hint = ""
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		sep,
		inputLine,
		stylePromptHint.Width(w).Render(hint),
	)
}

// ── Block rendering (content fed into viewport) ───────────────────────────────

// blockBorderStyle returns a border style coloured by task state.
func blockBorderStyle(state blockState) lipgloss.Style {
	base := lipgloss.NewStyle().Background(colBg2)
	switch state {
	case blockRunning:
		return base.Foreground(colAm)
	case blockPass:
		return base.Foreground(colGr)
	case blockFail:
		return base.Foreground(colRd)
	default: // queued
		return base.Foreground(colBl)
	}
}

func (m MainModel) renderBlock(b taskBlock, w int) []string {
	lines := []string{}
	innerW := w - 4 // "║ " left + " ║" right account

	bs := blockBorderStyle(b.state)
	ts := lipgloss.NewStyle().Foreground(colTx).Background(colBg2) // title text

	// ── Header ────────────────────────────────────────────────────────────────
	// "╔ description ═══╗"  border chars colored by state, text always white
	desc := truncate(b.description, max(0, innerW-2))
	fillW := max(0, w-len([]rune(desc))-4) // w - ╔ - space - desc - space - ╗
	header := bs.Render("╔") +
		ts.Render(" "+desc+" ") +
		bs.Render(strings.Repeat("═", fillW)+"╗")
	lines = append(lines, header)

	// ── Body lines ────────────────────────────────────────────────────────────
	pipe := bs.Render("║")
	for i, l := range b.lines {
		rendered := m.renderLine(l, innerW)
		isLast := i == len(b.lines)-1
		if isLast && b.state == blockRunning && m.logCursorOn {
			rendered += styleCursor.Render("█")
		}
		lines = append(lines, pipe+" "+rendered)
	}

	// ── Think block (if present) ───────────────────────────────────────────────
	if b.thinkContent != "" {
		if b.thinkCollapsed {
			// Collapsed indicator
			hint := styleLogSystem.Render("‹reasoning hidden — T to show›")
			lines = append(lines, pipe+"  "+hint)
		} else {
			// Render the reasoning panel
			thinkLines := m.renderThinkBlock(b.thinkContent, innerW-2)
			for _, tl := range thinkLines {
				lines = append(lines, pipe+" "+tl)
			}
		}
	}

	if len(b.lines) == 0 && b.thinkContent == "" {
		lines = append(lines, pipe+" ")
	}

	// ── Footer ────────────────────────────────────────────────────────────────
	switch b.state {
	case blockRunning:
		lines = append(lines, pipe+" ")

	case blockPass:
		badge := styleBadgePass.Render("pass")
		fillW := max(0, w-lipgloss.Width(badge)-4)
		lines = append(lines, bs.Render("╚"+strings.Repeat("═", fillW)+" ")+badge+bs.Render(" ╝"))

	case blockFail:
		badge := styleBadgeFail.Render("fail")
		fillW := max(0, w-lipgloss.Width(badge)-4)
		lines = append(lines, bs.Render("╚"+strings.Repeat("═", fillW)+" ")+badge+bs.Render(" ╝"))

	case blockQueued:
		label := styleQueuedLine.Render("queued")
		fillW := max(0, w-lipgloss.Width(label)-4)
		lines = append(lines, bs.Render("╚"+" ")+label+bs.Render(" "+strings.Repeat("═", fillW)+"╝"))
	}

	lines = append(lines, "") // spacing between blocks
	return lines
}

func (m MainModel) renderThinkBlock(content string, w int) []string {
	if w < 10 {
		w = 10
	}

	// Style for the reasoning box
	boxStyle := lipgloss.NewStyle().
		Foreground(colPu).
		Background(colBg2)

	contentStyle := lipgloss.NewStyle().
		Foreground(colTx2).
		Background(colBg2).
		Italic(true)

	// Split content into lines and wrap
	lines := strings.Split(content, "\n")
	var wrapped []string
	for _, line := range lines {
		if len(line) <= w-4 {
			wrapped = append(wrapped, line)
		} else {
			// Simple wrapping
			for len(line) > w-4 {
				wrapped = append(wrapped, line[:w-4])
				line = line[w-4:]
			}
			if line != "" {
				wrapped = append(wrapped, line)
			}
		}
	}

	// Build the box
	var result []string
	label := " reasoning "
	borderW := w - 2
	topBorder := "┌" + strings.Repeat("─", borderW-len(label)) + label + "┐"
	result = append(result, boxStyle.Render(topBorder))

	for _, line := range wrapped {
		padded := line + strings.Repeat(" ", max(0, borderW-len(line)))
		result = append(result, boxStyle.Render("│")+contentStyle.Render(" "+padded+" "))
	}

	bottomBorder := "└" + strings.Repeat("─", borderW) + "┘"
	result = append(result, boxStyle.Render(bottomBorder))

	return result
}

func (m MainModel) renderLine(l LogLine, w int) string {
	switch l.Kind {
	case lineExec:
		return stylePrefixExec.Render("exec") +
			styleLogDefault.Render(truncate(l.Content, w-prefixWidth))
	case lineCritic:
		return stylePrefixCritic.Render("critic") +
			styleLogDefault.Render(truncate(l.Content, w-prefixWidth))
	case lineSuccess:
		return stylePrefixBlank.Render("") + styleLogSuccess.Render(l.Content)
	case lineError:
		return stylePrefixBlank.Render("") + styleLogError.Render(l.Content)
	case lineWarning:
		return stylePrefixBlank.Render("") + styleLogWarning.Render(l.Content)
	case lineThink:
		// Handled in renderBlock via block.thinkContent
		return ""
	case lineCompaction:
		return stylePrefixCritic.Render("compact") + styleLogDefault.Render(l.Content)
	case lineQueued:
		return stylePrefixBlank.Render("") + styleQueuedLine.Render(l.Content)
	case lineSystem:
		return stylePrefixBlank.Render("") + styleLogSystem.Render(l.Content)
	case lineRoundSep:
		label := fmt.Sprintf(" round %d ", l.Round)
		ruleW := max(0, w-prefixWidth-len(label))
		return stylePrefixBlank.Render("") +
			styleRoundSep.Render(strings.Repeat("─", ruleW)) +
			styleRoundLabel.Render(label)
	default: // lineCont
		return stylePrefixBlank.Render("") + styleLogDefault.Render(l.Content)
	}
}

// ── Status bar ────────────────────────────────────────────────────────────────

func (m MainModel) StatusSegments() []string {
	if len(m.blocks) == 0 {
		return []string{"ready", "main"}
	}
	for _, b := range m.blocks {
		if b.state == blockRunning {
			agent := styleStatusExec.Render("exec")
			branch := b.id
			round := fmt.Sprintf("round %d", m.currentRound(b))
			if m.scrolledAlert {
				return []string{round, agent, branch, styleStatusAlert.Render("↑ scrolled ●")}
			}
			return []string{round, agent, branch}
		}
	}
	last := m.blocks[len(m.blocks)-1]
	if last.state == blockPass {
		sha := last.sha
		if len(sha) > 7 {
			sha = sha[:7]
		}
		return []string{styleBadgePass.Render("pass") + " · committed " + sha, last.id}
	}
	if last.state == blockFail {
		return []string{styleBadgeFail.Render("fail") + " · reverted", last.id}
	}
	return []string{"ready"}
}

func (m MainModel) currentRound(b taskBlock) int {
	for _, l := range b.lines {
		if l.Kind == lineRoundSep {
			return l.Round
		}
	}
	return 1
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
