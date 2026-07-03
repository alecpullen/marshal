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

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.Type {
	case tea.KeyEsc:
		return m, func() tea.Msg { return ClosedMsg{} }
	case tea.KeyUp:
		m.moveCursor(-1)
		return m, nil
	case tea.KeyDown:
		m.moveCursor(1)
		return m, nil
	}

	switch keyMsg.String() {
	case "k":
		m.moveCursor(-1)
	case "j":
		m.moveCursor(1)
	case "c":
		m.setConfidence(db.MemoryConfidenceConfirmed)
	case "s":
		m.setConfidence(db.MemoryConfidenceStale)
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
