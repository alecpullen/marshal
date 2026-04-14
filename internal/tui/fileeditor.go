// internal/tui/fileeditor.go
// Inline file editor: loads a file into a textarea, replaces the main panel.
// ctrl+s saves in place; esc closes (prompts if unsaved changes exist).
// The sidebar remains visible throughout.
//
// For full vim embedding (pty + VT100 emulation) a future iteration can
// replace this model with a PtyEditorModel using creack/pty + a vt100 state
// machine — the app.go overlay wiring stays identical.

package tui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Messages ──────────────────────────────────────────────────────────────────

type fileEditorCloseMsg struct{}

// ── Model ─────────────────────────────────────────────────────────────────────

type FileEditorModel struct {
	path    string
	rawText string // kept so we can re-parse after a resize

	textarea textarea.Model

	modified     bool
	saved        bool
	closed       bool
	saveErr      error
	confirmClose bool // waiting for y/n when there are unsaved changes
}

// newFileEditorModel creates the model. initialWidth must be the actual
// render width (main panel width) so that textarea parses long lines
// correctly from the start — SetValue uses the current width for wrapping,
// so calling it at width=0 mangles any file with lines > terminal width.
func newFileEditorModel(path string, initialWidth int) (FileEditorModel, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return FileEditorModel{}, err
	}

	// Normalise line endings (handles \r\n from Windows editors / git CRLF).
	text := strings.ReplaceAll(string(content), "\r\n", "\n")

	ta := textarea.New()
	ta.CharLimit = 0           // unlimited — must be set BEFORE SetValue
	ta.ShowLineNumbers = false // visual (wrapped) line numbers ≠ file line numbers
	if initialWidth > 0 {
		ta.SetWidth(initialWidth) // must be set BEFORE SetValue
	}
	ta.SetValue(text)
	ta.Focus()

	return FileEditorModel{
		path:     path,
		rawText:  text,
		textarea: ta,
	}, nil
}

// saveFile writes the current textarea content to disk.
func (m *FileEditorModel) saveFile() {
	m.saveErr = os.WriteFile(m.path, []byte(m.textarea.Value()), 0644)
	if m.saveErr == nil {
		m.modified = false
		m.saved = true
	}
}

func (m FileEditorModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m FileEditorModel) Update(msg tea.Msg) (FileEditorModel, tea.Cmd) {
	// On resize, re-parse the content at the new width so long lines wrap
	// correctly. We store the current value so edits aren't lost.
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		newW := ws.Width - sidebarWidth - 1
		if newW > 0 {
			current := m.textarea.Value()
			m.textarea.SetWidth(newW)
			m.textarea.SetValue(current)
		}
		return m, nil
	}

	// Unsaved-changes confirmation dialog
	if m.confirmClose {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			switch strings.ToLower(keyMsg.String()) {
			case "y":
				m.closed = true
				return m, nil
			case "n", "esc":
				m.confirmClose = false
				return m, nil
			}
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+s":
			m.saveFile()
			return m, nil

		case "esc":
			if m.modified {
				m.confirmClose = true
			} else {
				m.closed = true
			}
			return m, nil
		}
	}

	prev := m.textarea.Value()
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	if m.textarea.Value() != prev {
		m.modified = true
		m.saved = false
	}
	return m, cmd
}

// View renders the editor into the main panel area (w × h).
func (m FileEditorModel) View(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}

	// ── Header ────────────────────────────────────────────────────────────────
	filename := filepath.Base(m.path)
	var statusStr string
	switch {
	case m.saveErr != nil:
		statusStr = " [error: " + m.saveErr.Error() + "]"
	case m.modified:
		statusStr = " ●"
	case m.saved:
		statusStr = " ✓"
	}

	nameStyle := lipgloss.NewStyle().Foreground(colTx).Bold(true).Background(colBg2)
	pathStyle := lipgloss.NewStyle().Foreground(colTx3).Background(colBg2)

	left := nameStyle.Render(filename + statusStr)
	right := pathStyle.Render(filepath.Dir(m.path))
	fillW := w - lipgloss.Width(left) - lipgloss.Width(right)
	if fillW < 1 {
		fillW = 1
	}
	fill := lipgloss.NewStyle().Foreground(colBr3).Background(colBg2).Render(strings.Repeat(" ", fillW))
	header := lipgloss.NewStyle().Width(w).Background(colBg2).Render(left + fill + right)

	// ── Footer / hint ─────────────────────────────────────────────────────────
	var hint string
	switch {
	case m.confirmClose:
		hint = "  unsaved changes · y close anyway · n cancel"
	case m.saveErr != nil:
		hint = "  save failed · ctrl+s retry · esc close"
	default:
		hint = "  ctrl+s save · ctrl+e open in editor · esc close"
	}
	sep := stylePromptSep.Render(strings.Repeat("─", w))
	footer := stylePromptHint.Width(w).Render(hint)

	// ── Textarea ──────────────────────────────────────────────────────────────
	const fixedRows = 3 // header + sep + footer
	editorH := h - fixedRows
	if editorH < 1 {
		editorH = 1
	}
	m.textarea.SetWidth(w)
	m.textarea.SetHeight(editorH)

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		m.textarea.View(),
		sep,
		footer,
	)
}
