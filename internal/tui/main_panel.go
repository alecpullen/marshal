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

	"github.com/alecpullen/marshal/internal/config"
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
	lineMarshal                    // marshal planning/dispatching
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

	// Command completion state
	showCompletions    bool              // show completion dropdown
	completionMatches  []CompletionMatch // current completion candidates
	completionSelected int               // index of selected completion (-1 for none)
	config             *config.Config    // reference to config for completions
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

// SetConfig sets the config reference for command completion.
func (m *MainModel) SetConfig(cfg *config.Config) {
	m.config = cfg
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

		// Scroll the viewport (or navigate completions in command mode)
		case "up":
			if m.IsCommandMode() && m.showCompletions {
				if len(m.completionMatches) > 0 {
					m.completionSelected--
					if m.completionSelected < 0 {
						m.completionSelected = len(m.completionMatches) - 1
					}
				}
				skipInput = true
			} else {
				var cmd tea.Cmd
				m.viewport, cmd = m.viewport.Update(msg)
				cmds = append(cmds, cmd)
				m.scrolledAlert = !m.viewport.AtBottom()
				skipInput = true
			}

		case "down":
			if m.IsCommandMode() && m.showCompletions {
				if len(m.completionMatches) > 0 {
					m.completionSelected = (m.completionSelected + 1) % len(m.completionMatches)
				}
				skipInput = true
			} else {
				var cmd tea.Cmd
				m.viewport, cmd = m.viewport.Update(msg)
				cmds = append(cmds, cmd)
				m.scrolledAlert = !m.viewport.AtBottom()
				skipInput = true
			}

		case "pgup", "pgdown":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			cmds = append(cmds, cmd)
			m.scrolledAlert = !m.viewport.AtBottom()
			skipInput = true

		// ctrl+t: toggle all think-block visibility
		case "ctrl+t":
			if !m.IsCommandMode() {
				m.thinkCollapsed = !m.thinkCollapsed
				// Update all blocks to match global state
				for i := range m.blocks {
					m.blocks[i].thinkCollapsed = m.thinkCollapsed
				}
				m.rebuildContent()
				skipInput = true
			}

		// ctrl+shift+t: toggle focused block's think-block visibility
		case "ctrl+shift+t":
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

		// Esc: clear prompt or close completions
		case "esc":
			if m.showCompletions {
				m.showCompletions = false
				m.completionMatches = nil
				m.completionSelected = -1
			} else {
				m.input.SetValue("")
			}
			skipInput = true

		// Enter: submit task or execute command (or select completion)
		case "enter":
			val := m.input.Value()
			if val != "" {
				// If completions are showing with a selection, use that
				if m.showCompletions && m.completionSelected >= 0 && m.completionSelected < len(m.completionMatches) {
					selected := m.completionMatches[m.completionSelected]
					m.input.SetValue(":" + selected.Name + " ")
					m.input.CursorEnd()
					m.showCompletions = false
					m.completionMatches = nil
					m.completionSelected = -1
					skipInput = true
					break
				}

				m.promptHistory = append(m.promptHistory, val)
				m.historyIdx = len(m.promptHistory)
				m.input.SetValue("")
				m.showCompletions = false
				m.completionMatches = nil
				m.completionSelected = -1

				if strings.HasPrefix(val, ":") {
					cmds = append(cmds, func() tea.Msg {
						name, args, cmdType, err := parseCommand(val, m.config)
						if err != nil {
							return LogLineMsg{Line: LogLine{Kind: lineError, Content: err.Error()}}
						}
						return ExecCommandMsg{Name: name, Args: args, CmdType: cmdType}
					})
				} else {
					task := val
					cmds = append(cmds, func() tea.Msg { return SubmitTaskMsg{Task: task} })
				}
			}
			skipInput = true

		// Tab: accept ghost suggestion or navigate completions
		case "tab":
			val := m.input.Value()
			if m.IsCommandMode() {
				if m.showCompletions && len(m.completionMatches) > 0 {
					// Navigate to next completion
					m.completionSelected = (m.completionSelected + 1) % len(m.completionMatches)
					skipInput = true
				} else if m.input.Position() == len([]rune(val)) {
					// Accept ghost suggestion
					partial := strings.TrimPrefix(val, ":")
					if !strings.Contains(partial, " ") && partial != "" {
						suggestion, _ := completeCommand(partial)
						if suggestion != "" && suggestion != partial {
							m.input.SetValue(":" + suggestion)
							m.input.CursorEnd()
							skipInput = true
						}
					}
				}
			}

		// Shift+Tab: navigate to previous completion
		case "shift+tab":
			if m.IsCommandMode() && m.showCompletions && len(m.completionMatches) > 0 {
				m.completionSelected--
				if m.completionSelected < 0 {
					m.completionSelected = len(m.completionMatches) - 1
				}
				skipInput = true
			}

		// Right arrow: accept ghost suggestion when at end in command mode
		case "right":
			val := m.input.Value()
			if m.IsCommandMode() && m.input.Position() == len([]rune(val)) {
				partial := strings.TrimPrefix(val, ":")
				if !strings.Contains(partial, " ") && partial != "" {
					suggestion, _ := completeCommand(partial)
					if suggestion != "" && suggestion != partial {
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

	// Update completions after input changes (in command mode)
	if m.IsCommandMode() {
		val := m.input.Value()
		partial := strings.TrimPrefix(val, ":")
		// Only show completions if we're still typing the command name (no space yet)
		if !strings.Contains(partial, " ") && partial != "" {
			m.completionMatches = FindCompletions(partial, m.config)
			m.showCompletions = len(m.completionMatches) > 0
			// Reset selection when input changes
			if m.completionSelected >= len(m.completionMatches) {
				m.completionSelected = 0
			}
		} else {
			m.showCompletions = false
			m.completionMatches = nil
			m.completionSelected = -1
		}
	} else {
		m.showCompletions = false
		m.completionMatches = nil
		m.completionSelected = -1
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

	sep := stylePromptSep.Render(strings.Repeat("─", max(0, w-2)))
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

	// Render completion dropdown if showing
	var completionBox string
	if m.showCompletions && len(m.completionMatches) > 0 {
		completionBox = m.renderCompletionDropdown(w)
	}

	result := lipgloss.JoinVertical(lipgloss.Left,
		sep,
		inputLine,
		stylePromptHint.Width(w).Render(hint),
	)

	// Completion dropdown goes below the prompt bar
	if completionBox != "" {
		result = result + "\n" + completionBox
	}

	return result
}

// renderCompletionDropdown renders the command completion dropdown.
func (m MainModel) renderCompletionDropdown(w int) string {
	if len(m.completionMatches) == 0 {
		return ""
	}

	maxVisible := 5
	visible := m.completionMatches
	if len(visible) > maxVisible {
		visible = visible[:maxVisible]
	}

	var lines []string
	for i, match := range visible {
		isSelected := i == m.completionSelected

		// Build the line: :name — help (aliases)
		nameStyle := lipgloss.NewStyle().Foreground(colAm).Bold(true)
		if isSelected {
			nameStyle = nameStyle.Background(colBl).Foreground(colBg)
		}

		// Show custom indicator
		prefix := ""
		if match.IsCustom {
			prefix = "[custom] "
		}

		aliases := ""
		if len(match.Aliases) > 0 {
			aliases = fmt.Sprintf(" (%s)", strings.Join(match.Aliases, ", "))
		}

		line := fmt.Sprintf("  %s%s%s — %s",
			prefix,
			nameStyle.Render(":"+match.Name),
			lipgloss.NewStyle().Foreground(colTx3).Render(aliases),
			lipgloss.NewStyle().Foreground(colTx2).Render(match.Help))

		if isSelected {
			line = lipgloss.NewStyle().Background(colBg2).Render(line)
		}

		lines = append(lines, line)
	}

	// Add count indicator if truncated
	if len(m.completionMatches) > maxVisible {
		more := len(m.completionMatches) - maxVisible
		lines = append(lines, lipgloss.NewStyle().Foreground(colTx3).Italic(true).
			Render(fmt.Sprintf("  ... and %d more", more)))
	}

	// Wrap in a box
	content := strings.Join(lines, "\n")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colTx3).
		Width(w - 2).
		Render(content)
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

	// ── Think block (if present, shown before body lines) ────────────────────
	pipe := bs.Render("║")
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

	// ── Body lines ────────────────────────────────────────────────────────────
	for i, l := range b.lines {
		rendered := m.renderLineWrapped(l, innerW)
		isLast := i == len(b.lines)-1
		if isLast && b.state == blockRunning && m.logCursorOn {
			rendered[len(rendered)-1] += styleCursor.Render("█")
		}
		for _, rl := range rendered {
			lines = append(lines, pipe+" "+rl)
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

	// Think block renders inline within the block, with pipe border + "reasoning" label
	// and italic purple text. Not a separate box with its own borders.
	pipeStyle := lipgloss.NewStyle().Foreground(colPu)
	indent := 2 // spaces after pipe

	// Split content into lines and wrap to available width
	contentW := w - indent - 2 // -2 for "  " prefix
	if contentW < 4 {
		contentW = 4
	}

	lines := strings.Split(content, "\n")
	var wrapped []string
	for _, line := range lines {
		if len(line) == 0 {
			wrapped = append(wrapped, "")
			continue
		}
		if len(line) <= contentW {
			wrapped = append(wrapped, line)
		} else {
			// Break into chunks that fit
			for len(line) > contentW {
				wrapped = append(wrapped, line[:contentW])
				line = line[contentW:]
			}
			if line != "" {
				wrapped = append(wrapped, line)
			}
		}
	}

	// Render with pipe + indent prefix
	prefix := pipeStyle.Render("│") + lipgloss.NewStyle().Foreground(colTx3).Render(strings.Repeat(" ", indent))
	labelStyle := lipgloss.NewStyle().Foreground(colPu).Italic(true)
	contentStyle := lipgloss.NewStyle().Foreground(colPu).Italic(true)

	var result []string
	for i, line := range wrapped {
		if i == 0 {
			// First line: pipe + indent + "reasoning: " + first content
			result = append(result, prefix+labelStyle.Render("reasoning ")+contentStyle.Render(line))
		} else {
			// Continuation lines
			result = append(result, prefix+contentStyle.Render(line))
		}
	}

	return result
}

// renderLineWrapped renders a log line into one or more display lines.
// Lines whose content exceeds the available width wrap onto continuation lines
// with a blank prefix so the label only appears on the first line.
func (m MainModel) renderLineWrapped(l LogLine, w int) []string {
	switch l.Kind {
	case lineMarshal:
		contentW := w - prefixWidth
		if contentW < 1 {
			contentW = 1
		}
		chunks := wrapString(l.Content, contentW)
		out := make([]string, 0, len(chunks))
		for i, chunk := range chunks {
			if i == 0 {
				out = append(out, stylePrefixMarshal.Render("marshal")+styleLogDefault.Render(chunk))
			} else {
				out = append(out, stylePrefixBlank.Render("")+styleLogDefault.Render(chunk))
			}
		}
		return out
	}
	return []string{m.renderLine(l, w)}
}

// wrapString splits s into lines of at most w runes, breaking on spaces where possible.
func wrapString(s string, w int) []string {
	if w <= 0 {
		return []string{s}
	}
	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		if len([]rune(paragraph)) <= w {
			out = append(out, paragraph)
			continue
		}
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			if len([]rune(line))+1+len([]rune(word)) <= w {
				line += " " + word
			} else {
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func (m MainModel) renderLine(l LogLine, w int) string {
	switch l.Kind {
	case lineExec:
		return stylePrefixExec.Render("exec") +
			styleLogDefault.Render(truncate(l.Content, w-prefixWidth))
	case lineCritic:
		return stylePrefixCritic.Render("critic") +
			styleLogDefault.Render(truncate(l.Content, w-prefixWidth))
	case lineMarshal:
		return stylePrefixMarshal.Render("marshal") +
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

// AppendConversationMessage adds a conversation message to the display
func (m *MainModel) AppendConversationMessage(role, content string) {
	// For now, append as log lines. In the future, render as conversation bubbles.
	var prefix string
	switch role {
	case "user":
		prefix = "you"
	case "marshal":
		prefix = "marshal"
	default:
		prefix = role
	}

	// Add as a system-style message for now (can be styled better later)
	line := LogLine{
		Kind:    lineMarshal,
		Content: fmt.Sprintf("%s: %s", prefix, content),
	}

	// If no running block, create a chat block
	if len(m.blocks) == 0 || (m.blocks[len(m.blocks)-1].state != blockRunning && m.blocks[len(m.blocks)-1].state != blockQueued) {
		m.blocks = append(m.blocks, taskBlock{
			id:          fmt.Sprintf("chat-%d", len(m.blocks)),
			description: "conversation",
			state:       blockRunning,
			lines:       []LogLine{line},
		})
	} else {
		// Append to existing block
		m.blocks[len(m.blocks)-1].lines = append(m.blocks[len(m.blocks)-1].lines, line)
	}

	m.rebuildContent()
	m.viewport.GotoBottom()
	m.scrolledAlert = false
}

// ── Status bar ────────────────────────────────────────────────────────────────

func (m MainModel) StatusSegments() []string {
	if len(m.blocks) == 0 {
		return []string{"ready", "main"}
	}
	for _, b := range m.blocks {
		if b.state == blockRunning {
			agent := styleStatusMarshal.Render("marshal")
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
