package settings

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// mapEditor is an inline key/value editor bound to a map[string]T in the
// working config. The value type T is parameterised so the same widget
// powers both string-valued maps (diagnostics commands) and int-valued
// maps (swarm tool iters). parse converts user-typed strings into T;
// format renders T back to a string for display. For T == string the
// built-in stringMapParse / stringMapFormat helpers are used.
//
// It supports per-row delete, in-place value editing, and a two-phase
// add (key first, then value) so the user can edit the value of a row
// without the original value getting preloaded into the input.
type mapEditor[T any] struct {
	title    string
	values   *map[string]T
	parse    func(string) (T, error)
	format   func(T) string
	keys     []string
	cursor   int
	adding   bool
	editing  bool
	phase    int // 0 = key, 1 = value (during add)
	keyInput textinput.Model
	valInput textinput.Model
	errMsg   string
	focused  bool
	width    int
}

func stringMapParse(s string) (string, error) { return s, nil }
func stringMapFormat(s string) string         { return s }

func intMapParse(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}
func intMapFormat(v int) string { return strconv.Itoa(v) }

func newMapEditor[T any](title string, values *map[string]T, parse func(string) (T, error), format func(T) string) *mapEditor[T] {
	ki := textinput.New()
	ki.SetVirtualCursor(true)
	vi := textinput.New()
	vi.SetVirtualCursor(true)
	m := &mapEditor[T]{title: title, values: values, parse: parse, format: format, keyInput: ki, valInput: vi}
	m.refreshKeys()
	return m
}

// newMapStringEditor is a thin wrapper over newMapEditor for the
// string-valued case (diagnostics commands). Kept for call-site clarity.
func newMapStringEditor(title string, values *map[string]string) *mapEditor[string] {
	return newMapEditor(title, values, stringMapParse, stringMapFormat)
}

// newMapIntEditor is a thin wrapper over newMapEditor for the int-valued
// case (swarm tool iters). Kept for call-site clarity.
func newMapIntEditor(title string, values *map[string]int) *mapEditor[int] {
	return newMapEditor(title, values, intMapParse, intMapFormat)
}

func (m *mapEditor[T]) refreshKeys() {
	m.keys = make([]string, 0, len(*m.values))
	for k := range *m.values {
		m.keys = append(m.keys, k)
	}
	sort.Strings(m.keys)
	m.clamp()
}

func (m *mapEditor[T]) clamp() {
	if m.cursor >= len(m.keys) {
		m.cursor = len(m.keys) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *mapEditor[T]) Focus(on bool)  { m.focused = on }
func (m *mapEditor[T]) Focused() bool  { return m.focused }
func (m *mapEditor[T]) Editing() bool  { return m.adding || m.editing }
func (m *mapEditor[T]) SetWidth(w int) { m.width = w }

func (m *mapEditor[T]) CancelEdit() {
	m.adding = false
	m.editing = false
	m.phase = 0
	m.errMsg = ""
	m.keyInput.Blur()
	m.valInput.Blur()
}

func (m *mapEditor[T]) FocusedFieldTitle() string {
	if m.Editing() {
		if m.adding && m.phase == 0 {
			return m.title + " key"
		}
		return m.title + " value"
	}
	return m.title
}

func (m *mapEditor[T]) Label() string                       { return m.title }
func (m *mapEditor[T]) updateKey(k tea.KeyPressMsg) tea.Cmd { return m.Update(k) }

func (m *mapEditor[T]) Update(msg tea.Msg) tea.Cmd {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	if m.Editing() {
		return m.updateEdit(kp)
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
		m.phase = 0
		m.errMsg = ""
		m.valInput.Blur()
		m.keyInput.SetValue("")
		m.keyInput.Focus()
	case "enter", "e":
		if len(m.keys) > 0 {
			m.editing = true
			m.valInput.SetValue("")
			m.valInput.CursorEnd()
			m.valInput.Focus()
		}
	case "d":
		if len(m.keys) > 0 {
			delete(*m.values, m.keys[m.cursor])
			m.refreshKeys()
		}
	}
	return nil
}

func (m *mapEditor[T]) updateEdit(msg tea.KeyPressMsg) tea.Cmd {
	if m.editing {
		switch msg.String() {
		case "enter":
			v, err := m.parse(strings.TrimSpace(m.valInput.Value()))
			if err != nil {
				m.errMsg = "value must be a number"
				return nil
			}
			(*m.values)[m.keys[m.cursor]] = v
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
	// adding
	switch msg.String() {
	case "enter":
		if m.phase == 0 {
			key := strings.TrimSpace(m.keyInput.Value())
			if key == "" {
				m.errMsg = "key cannot be empty"
				return nil
			}
			if _, exists := (*m.values)[key]; exists {
				m.errMsg = "key already exists"
				return nil
			}
			m.phase = 1
			m.keyInput.Blur()
			m.valInput.SetValue("")
			m.valInput.Focus()
			return nil
		}
		key := strings.TrimSpace(m.keyInput.Value())
		v, err := m.parse(strings.TrimSpace(m.valInput.Value()))
		if err != nil {
			m.errMsg = "value must be a number"
			return nil
		}
		if key == "" {
			m.errMsg = "key cannot be empty"
			return nil
		}
		(*m.values)[key] = v
		m.refreshKeys()
		m.CancelEdit()
		return nil
	case "esc":
		m.CancelEdit()
		return nil
	}
	var cmd tea.Cmd
	if m.phase == 0 {
		m.keyInput, cmd = m.keyInput.Update(msg)
	} else {
		m.valInput, cmd = m.valInput.Update(msg)
	}
	return cmd
}

func (m *mapEditor[T]) View(width int) string {
	var b strings.Builder
	b.WriteString(m.title + "\n")
	if len(m.keys) == 0 && !m.adding {
		b.WriteString("  (empty — press a to add)\n")
	}
	for i, k := range m.keys {
		marker := "  "
		if m.focused && i == m.cursor {
			marker = "▸ "
		}
		if m.editing && i == m.cursor {
			b.WriteString(fmt.Sprintf("%s%s = %s\n", marker, k, m.valInput.View()))
			continue
		}
		b.WriteString(fmt.Sprintf("%s%s = %s\n", marker, k, m.format((*m.values)[k])))
	}
	if m.adding {
		if m.phase == 0 {
			b.WriteString("▸ key: " + m.keyInput.View() + "\n")
		} else {
			b.WriteString("  " + m.keyInput.Value() + " = " + m.valInput.View() + "\n")
		}
	}
	if m.errMsg != "" {
		b.WriteString(warnStyle.Render("⚠ "+m.errMsg) + "\n")
	}
	if m.focused && !m.Editing() {
		b.WriteString("  a add · e edit value · d delete")
	}
	return strings.TrimRight(b.String(), "\n")
}

// mapPane is a sectionPane adapter that wraps a single *mapEditor. Used
// for top-level sections that are nothing but a key/value map (e.g.
// Diagnostics -> Commands).
type mapPane struct{ m *mapEditor[string] }

func newMapPane(m *mapEditor[string]) *mapPane { return &mapPane{m: m} }

func (p *mapPane) Init() tea.Cmd      { p.m.Focus(true); return nil }
func (p *mapPane) AtFirstFocus() bool { return true }
func (p *mapPane) Update(msg tea.Msg) (sectionPane, tea.Cmd) {
	return p, p.m.Update(msg)
}
func (p *mapPane) View(width int) string     { return p.m.View(width) }
func (p *mapPane) SetWidth(w int)            { p.m.SetWidth(w) }
func (p *mapPane) HasInnerFocus() bool       { return p.m.Editing() }
func (p *mapPane) CloseInner()               { p.m.CancelEdit() }
func (p *mapPane) FocusedFieldTitle() string { return p.m.FocusedFieldTitle() }
