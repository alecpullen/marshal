package settings

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

// collectionEntry is one row in a collection pane's list.
type collectionEntry interface {
	Title() string
	Key() string
}

// collectionSpec describes a map-keyed section's list+sub-form behavior.
type collectionSpec struct {
	heading   string
	keyPrompt string
	entries   func(s *state) []collectionEntry
	add       func(s *state, key string) error
	editForm  func(s *state, key string) (form *huh.Form, onSubmit func())
	delete    func(s *state, key string)
}

// collectionPane is the generic list + sub-form editor. It owns three
// interaction states: the entry list, a name-prompt (for add), and a huh
// sub-form (for edit). Sub-forms edit a local copy and commit on submit so
// Esc is a true discard.
type collectionPane struct {
	spec       collectionSpec
	s          *state
	cursor     int
	adding     bool
	addErr     string
	nameInput  textinput.Model
	form       *huh.Form
	editingKey string
	onSubmit   func()
	width      int
}

func newCollectionPane(s *state, spec collectionSpec) *collectionPane {
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	return &collectionPane{spec: spec, s: s, nameInput: ti}
}

func (p *collectionPane) Init() tea.Cmd { return nil }

func (p *collectionPane) AtFirstFocus() bool { return true }

func (p *collectionPane) sortedEntries() []collectionEntry {
	es := p.spec.entries(p.s)
	sort.Slice(es, func(i, j int) bool { return es[i].Key() < es[j].Key() })
	return es
}

func (p *collectionPane) clamp() {
	n := len(p.sortedEntries())
	if n == 0 {
		p.cursor = 0
		return
	}
	if p.cursor >= n {
		p.cursor = n - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

func (p *collectionPane) Update(msg tea.Msg) (sectionPane, tea.Cmd) {
	k, isKey := msg.(tea.KeyPressMsg)
	if !isKey {
		if p.form != nil {
			updated, cmd := p.form.Update(msg)
			if f, ok := updated.(*huh.Form); ok {
				p.form = f
			}
			for i := 0; i < 8 && cmd != nil; i++ {
				next := cmd()
				if next == nil {
					break
				}
				updated, cmd = p.form.Update(next)
				if f, ok := updated.(*huh.Form); ok {
					p.form = f
				}
			}
			p.checkFormDone()
			return p, cmd
		}
		return p, nil
	}

	// Name-prompt state.
	if p.adding {
		switch k.String() {
		case "enter":
			key := strings.TrimSpace(p.nameInput.Value())
			if err := p.spec.add(p.s, key); err != nil {
				p.addErr = err.Error()
				return p, nil
			}
			p.adding = false
			p.addErr = ""
			p.cursor = len(p.sortedEntries()) - 1
			return p, nil
		case "esc":
			p.adding = false
			p.addErr = ""
			return p, nil
		}
		var cmd tea.Cmd
		p.nameInput, cmd = p.nameInput.Update(k)
		return p, cmd
	}

	// Sub-form state.
	if p.form != nil {
		if k.String() == "esc" {
			p.form = nil
			p.editingKey = ""
			p.onSubmit = nil
			return p, nil
		}
		updated, cmd := p.form.Update(k)
		if f, ok := updated.(*huh.Form); ok {
			p.form = f
		}
		// Drain huh's NextField/NextGroup messages so the form reaches a
		// terminal state within a single Update call.
		for i := 0; i < 8 && cmd != nil; i++ {
			msg := cmd()
			if msg == nil {
				break
			}
			updated, cmd = p.form.Update(msg)
			if f, ok := updated.(*huh.Form); ok {
				p.form = f
			}
		}
		p.checkFormDone()
		return p, cmd
	}

	// List state.
	switch k.String() {
	case "up", "k":
		p.cursor--
		p.clamp()
	case "down", "j":
		p.cursor++
		p.clamp()
	case "a":
		p.adding = true
		p.addErr = ""
		p.nameInput.SetValue("")
		p.nameInput.Focus()
	case "enter", "e":
		es := p.sortedEntries()
		if len(es) > 0 {
			p.editingKey = es[p.cursor].Key()
			p.form, p.onSubmit = p.spec.editForm(p.s, p.editingKey)
			p.form.WithWidth(p.width)
			if c := p.form.Init(); c != nil {
				_ = c()
			}
		}
	case "d":
		es := p.sortedEntries()
		if len(es) > 0 {
			p.spec.delete(p.s, es[p.cursor].Key())
			p.clamp()
		}
	}
	return p, nil
}

// checkFormDone closes the sub-form when huh reaches a terminal state.
// On submit, the optional onSubmit callback commits the local copy into
// the working copy; on abort (Esc), it is not called, so the local copy
// is discarded.
func (p *collectionPane) checkFormDone() {
	if p.form == nil {
		return
	}
	if p.form.State == huh.StateCompleted {
		if p.onSubmit != nil {
			p.onSubmit()
		}
		p.form = nil
		p.editingKey = ""
		p.onSubmit = nil
		return
	}
	if p.form.State == huh.StateAborted {
		p.form = nil
		p.editingKey = ""
		p.onSubmit = nil
	}
}

func (p *collectionPane) SetWidth(w int) {
	p.width = w
	if p.form != nil {
		p.form.WithWidth(w)
	}
}

func (p *collectionPane) HasInnerFocus() bool { return p.adding || p.form != nil }

func (p *collectionPane) CloseInner() {
	if p.adding {
		p.adding = false
		p.addErr = ""
		return
	}
	p.form = nil
	p.editingKey = ""
}

func (p *collectionPane) FocusedFieldTitle() string {
	if p.adding {
		return p.spec.keyPrompt
	}
	if p.form != nil {
		if f := p.form.GetFocusedField(); f != nil {
			return f.GetKey()
		}
		return p.spec.heading
	}
	return p.spec.heading
}

func (p *collectionPane) View(width int) string {
	if p.form != nil {
		return p.form.View()
	}
	if p.adding {
		return p.spec.keyPrompt + "\n▸ " + p.nameInput.View() +
			renderAddErr(p.addErr)
	}
	var b strings.Builder
	b.WriteString(p.spec.heading + "\n")
	es := p.sortedEntries()
	if len(es) == 0 {
		b.WriteString("  (empty — press a to add)\n")
	}
	for i, e := range es {
		marker := "  "
		if i == p.cursor {
			marker = "▸ "
		}
		b.WriteString(fmt.Sprintf("%s%s\n", marker, e.Title()))
	}
	b.WriteString("  a add · e edit · d delete")
	return strings.TrimRight(b.String(), "\n")
}

func renderAddErr(err string) string {
	if err == "" {
		return ""
	}
	return "\n" + warnStyle.Render("⚠ "+err)
}
