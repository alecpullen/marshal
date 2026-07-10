package settings

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// mapEditor is an inline key/value editor bound to a map[string]string in
// the working config. This is a stub provided by Task 11 so that the MCP
// composite can compile and test. Task 12 will replace this with the full
// implementation (key/value editor with per-row edit, new key prompts,
// sort, etc).
type mapEditor struct {
	title   string
	items   *map[string]string
	focused bool

	cursor     int
	adding     bool
	editingKey string
	keyInput   textinput.Model
	valInput   textinput.Model
	addErr     string
	width      int
}

func newMapEditor(title string, items *map[string]string) *mapEditor {
	ki := textinput.New()
	ki.Placeholder = "key"
	ki.SetVirtualCursor(true)
	vi := textinput.New()
	vi.Placeholder = "value"
	vi.SetVirtualCursor(true)
	return &mapEditor{title: title, items: items, keyInput: ki, valInput: vi}
}

func (m *mapEditor) Focus(on bool) { m.focused = on }

func (m *mapEditor) Editing() bool { return m.adding || m.editingKey != "" }

func (m *mapEditor) CancelEdit() {
	m.adding = false
	m.editingKey = ""
	m.addErr = ""
	m.keyInput.Blur()
	m.valInput.Blur()
}

func (m *mapEditor) SetWidth(w int) { m.width = w }

func (m *mapEditor) sortedKeys() []string {
	keys := make([]string, 0, len(*m.items))
	for k := range *m.items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (m *mapEditor) clamp() {
	n := len(m.sortedKeys())
	if n == 0 {
		m.cursor = 0
		return
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *mapEditor) Update(msg tea.Msg) tea.Cmd {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	if m.adding {
		switch kp.String() {
		case "enter":
			key := strings.TrimSpace(m.keyInput.Value())
			v := strings.TrimSpace(m.valInput.Value())
			if key == "" {
				m.addErr = "key cannot be empty"
				return nil
			}
			if _, exists := (*m.items)[key]; exists {
				m.addErr = "key already exists"
				return nil
			}
			(*m.items)[key] = v
			m.CancelEdit()
			return nil
		case "esc":
			m.CancelEdit()
			return nil
		case "tab":
			var cmd tea.Cmd
			m.valInput, cmd = m.valInput.Update(msg)
			return cmd
		}
		var cmd tea.Cmd
		m.keyInput, cmd = m.keyInput.Update(msg)
		return cmd
	}
	if m.editingKey != "" {
		switch kp.String() {
		case "enter":
			(*m.items)[m.editingKey] = strings.TrimSpace(m.valInput.Value())
			m.CancelEdit()
			return nil
		case "esc":
			m.CancelEdit()
			return nil
		}
		var cmd tea.Cmd
		m.valInput, cmd = m.valInput.Update(msg)
		return cmd
	}
	if !m.focused {
		return nil
	}
	switch kp.String() {
	case "up", "k":
		m.cursor--
		m.clamp()
	case "down", "j":
		m.cursor++
		m.clamp()
	case "a":
		m.adding = true
		m.addErr = ""
		m.keyInput.SetValue("")
		m.valInput.SetValue("")
		m.keyInput.Focus()
	case "enter", "e":
		keys := m.sortedKeys()
		if len(keys) > 0 {
			m.editingKey = keys[m.cursor]
			m.valInput.SetValue((*m.items)[m.editingKey])
			m.valInput.CursorEnd()
			m.valInput.Focus()
		}
	case "d":
		keys := m.sortedKeys()
		if len(keys) > 0 {
			delete(*m.items, keys[m.cursor])
			m.clamp()
		}
	}
	return nil
}

func (m *mapEditor) View(width int) string {
	var b strings.Builder
	b.WriteString(m.title + "\n")
	if m.adding {
		b.WriteString("▸ " + m.keyInput.View() + " = " + m.valInput.View() + "\n")
		if m.addErr != "" {
			b.WriteString(warnStyle.Render("⚠ "+m.addErr) + "\n")
		}
		return strings.TrimRight(b.String(), "\n")
	}
	if m.editingKey != "" {
		b.WriteString(m.editingKey + " = " + m.valInput.View() + "\n")
		return strings.TrimRight(b.String(), "\n")
	}
	keys := m.sortedKeys()
	if len(keys) == 0 {
		b.WriteString("  (empty — press a to add)\n")
	}
	for i, k := range keys {
		marker := "  "
		if m.focused && i == m.cursor {
			marker = "▸ "
		}
		b.WriteString(fmt.Sprintf("%s%s = %s\n", marker, k, (*m.items)[k]))
	}
	if m.focused {
		b.WriteString("  a add · e edit · d delete")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *mapEditor) FocusedFieldTitle() string {
	if m.adding {
		return m.title
	}
	if m.editingKey != "" {
		return m.title
	}
	return m.title
}
