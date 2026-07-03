package memory

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/db"
)

type Model struct {
	db        *db.DB
	projectID int64
	memories  []db.Memory
	cursor    int
	footer    string
	width     int
	height    int
	offset    int
}

func New(database *db.DB, projectID int64) Model {
	m := Model{db: database, projectID: projectID}
	memories, err := database.GetMemories(projectID)
	if err != nil {
		m.footer = "Load failed: " + err.Error()
		return m
	}
	m.memories = memories
	return m
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			return m, func() tea.Msg { return ClosedMsg{} }
		case tea.KeyUp:
			m.moveCursor(-1)
			return m, nil
		case tea.KeyDown:
			m.moveCursor(1)
			return m, nil
		}
		switch msg.String() {
		case "k":
			m.moveCursor(-1)
		case "j":
			m.moveCursor(1)
		case "c":
			m.setConfidence(db.MemoryConfidenceConfirmed)
		case "s":
			m.setConfidence(db.MemoryConfidenceStale)
		}
	}
	return m, nil
}

func (m *Model) moveCursor(delta int) {
	if len(m.memories) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.memories) {
		m.cursor = len(m.memories) - 1
	}
	m.keepCursorInView()
}

func (m *Model) keepCursorInView() {
	visible := m.visibleCount()
	if visible <= 0 {
		return
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
}

// visibleCount uses a value receiver because it does not mutate state.
func (m Model) visibleCount() int {
	// title(1) + separator(1) + footer/help(2) + borders(2)
	available := m.height - 6
	if available < 1 {
		return 1
	}
	return available
}

func (m *Model) setConfidence(confidence string) {
	if m.cursor < 0 || m.cursor >= len(m.memories) {
		return
	}
	selected := m.memories[m.cursor]
	now := time.Now().UTC()
	if err := m.db.SetMemoryConfidence(selected.ID, confidence, now); err != nil {
		m.footer = "Update failed: " + err.Error()
		return
	}
	selected.Confidence = confidence
	selected.UpdatedAt = now
	m.memories[m.cursor] = selected
	m.footer = ""
}

func (m Model) Footer() string {
	return m.footer
}
