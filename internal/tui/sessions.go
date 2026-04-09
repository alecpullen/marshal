// internal/tui/sessions.go
// Session browser overlay. Shows persisted sessions from SQLite store.
// Supports resume, diff, and delete with confirmation.

package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/alecpullen/marshal/internal/store"
)

// sessionItem represents a session in the list.
type sessionItem struct {
	session store.Session
	marked  bool // for delete confirmation
}

// SessionsModel is the session browser overlay.
type SessionsModel struct {
	sessions []sessionItem
	cursor   int
	width    int
	height   int

	// Sub-states
	confirmDelete bool
	confirmIdx    int

	// Store reference (for loading/reloading)
	store *store.Store

	// Selection result
	resumeSession *store.Session
}

// sessionsLoadedMsg is sent when sessions are loaded from store.
type sessionsLoadedMsg struct {
	sessions []store.Session
	err      error
}

func newSessionsModel(s *store.Store) SessionsModel {
	return SessionsModel{
		store:    s,
		sessions: []sessionItem{},
	}
}

func (m SessionsModel) Init() tea.Cmd {
	return m.loadSessions()
}

func (m SessionsModel) loadSessions() tea.Cmd {
	return func() tea.Msg {
		sessions, err := m.store.ListSessions(100)
		return sessionsLoadedMsg{sessions: sessions, err: err}
	}
}

func (m SessionsModel) Update(msg tea.Msg) (SessionsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case sessionsLoadedMsg:
		if msg.err != nil {
			// Keep empty list on error
			return m, nil
		}
		m.sessions = make([]sessionItem, len(msg.sessions))
		for i, s := range msg.sessions {
			m.sessions[i] = sessionItem{session: s}
		}
		if m.cursor >= len(m.sessions) {
			m.cursor = max(0, len(m.sessions)-1)
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		// Handle delete confirmation first
		if m.confirmDelete {
			switch msg.String() {
			case "y":
				return m, m.deleteSession(m.confirmIdx)
			case "n", "esc":
				m.confirmDelete = false
				return m, nil
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "esc":
			// Close overlay
			return m, func() tea.Msg { return closeSessionsMsg{} }

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil

		case "down", "j":
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
			}
			return m, nil

		case "enter":
			if m.cursor < len(m.sessions) {
				m.resumeSession = &m.sessions[m.cursor].session
				return m, func() tea.Msg { return resumeSessionMsg{session: *m.resumeSession} }
			}
			return m, nil

		case "d":
			// Open diff for selected session
			if m.cursor < len(m.sessions) {
				s := m.sessions[m.cursor].session
				return m, func() tea.Msg {
					// Use session ID for diff lookup
						return OpenDiffMsg{TaskID: s.ID, SHA: ""}
				}
			}

		case "x":
			// Initiate delete confirmation
			if m.cursor < len(m.sessions) {
				m.confirmDelete = true
				m.confirmIdx = m.cursor
			}
			return m, nil

		case "r":
			// Reload sessions
			return m, m.loadSessions()
		}
	}

	return m, nil
}

func (m SessionsModel) deleteSession(idx int) tea.Cmd {
	return func() tea.Msg {
		// Delete functionality requires store support
		// For now, just mark as deleted in UI
		if idx >= len(m.sessions) {
			return sessionDeletedMsg{err: fmt.Errorf("invalid index")}
		}
		return sessionDeletedMsg{idx: idx, err: nil}
	}
}

// closeSessionsMsg signals the overlay should close.
type closeSessionsMsg struct{}

// resumeSessionMsg signals a session should be resumed.
type resumeSessionMsg struct {
	session store.Session
}

// sessionDeletedMsg confirms deletion.
type sessionDeletedMsg struct {
	idx int
	err error
}

// HandleDeleteResult processes the delete result and updates state.
func (m SessionsModel) HandleDeleteResult(msg sessionDeletedMsg) SessionsModel {
	m.confirmDelete = false
	if msg.err == nil && msg.idx < len(m.sessions) {
		// Remove from list
		m.sessions = append(m.sessions[:msg.idx], m.sessions[msg.idx+1:]...)
		if m.cursor >= len(m.sessions) {
			m.cursor = max(0, len(m.sessions)-1)
		}
	}
	return m
}

func (m SessionsModel) View(w, h int) string {
	innerW := w - 8
	if innerW < 50 {
		innerW = 50
	}

	// Title with connection status dots
	title := lipgloss.NewStyle().
		Foreground(colTx).
		Bold(true).
		Render("sessions")

	// Header row
	headerCols := []string{
		lipgloss.NewStyle().Width(18).Render("ID"),
		lipgloss.NewStyle().Width(30).Render("Task"),
		lipgloss.NewStyle().Width(8).Render("Status"),
		lipgloss.NewStyle().Width(10).Render("When"),
	}
	headerRow := lipgloss.NewStyle().
		Background(colBg2).
		Foreground(colTx3).
		Width(innerW).
		Render(strings.Join(headerCols, ""))

	// List rows
	listH := h - 8 // header + title + hint + borders
	rows := m.renderRows(innerW, listH)

	// Hint / confirm bar
	var hint string
	if m.confirmDelete {
		task := ""
		if m.confirmIdx < len(m.sessions) {
			task = truncate(m.sessions[m.confirmIdx].session.Task, 30)
		}
		hint = lipgloss.NewStyle().
			Foreground(colRd).
			Render("delete \""+task+"\"? y/n")
	} else {
		hint = stylePromptHint.Render("↵ resume · d diff · x delete · r reload · q/esc close")
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		title,
		"",
		headerRow,
		styleRoundSep.Render(strings.Repeat("─", innerW)),
		rows,
		"",
		hint,
	)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBr3).
		Background(colBg2).
		Padding(1, 2).
		Width(w - 4).
		Height(h - 2).
		Render(content)
}

func (m SessionsModel) renderRows(w, h int) string {
	if len(m.sessions) == 0 {
		return lipgloss.NewStyle().
			Height(h).
			Foreground(colTx3).
			Italic(true).
			Render("  no sessions found")
	}

	var lines []string
	for i, item := range m.sessions {
		if i >= h {
			break
		}

		s := item.session
		style := lipgloss.NewStyle()
		if i == m.cursor {
			style = style.Background(colBl).Foreground(colBg)
		} else {
			style = style.Foreground(colTx2)
		}

		// Format columns
		id := truncate(s.ID, 16)
		task := truncate(s.Task, 28)
		status := m.renderStatus(s.Status, i == m.cursor)
		when := formatRelativeTime(s.UpdatedAt)

		cols := []string{
			lipgloss.NewStyle().Width(18).Render(id),
			lipgloss.NewStyle().Width(30).Render(task),
			lipgloss.NewStyle().Width(8).Render(status),
			lipgloss.NewStyle().Width(10).Foreground(colTx3).Render(when),
		}

		line := strings.Join(cols, "")
		lines = append(lines, style.Width(w).Render(line))
	}

	// Pad
	for len(lines) < h {
		lines = append(lines, "")
	}

	return strings.Join(lines[:h], "\n")
}

func (m SessionsModel) renderStatus(status string, active bool) string {
	switch status {
	case "SUCCESS":
		if active {
			return "PASS"
		}
		return lipgloss.NewStyle().Foreground(colGr).Render("PASS")
	case "FAIL":
		if active {
			return "FAIL"
		}
		return lipgloss.NewStyle().Foreground(colRd).Render("FAIL")
	case "RUNNING":
		if active {
			return "RUN"
		}
		return lipgloss.NewStyle().Foreground(colAm).Render("RUN")
	default:
		return status
	}
}

func formatRelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2")
	}
}
