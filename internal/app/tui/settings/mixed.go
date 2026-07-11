package settings

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// focusableList is the small contract mixedPane needs from each list-like
// widget it embeds: listStrings, mapEditor, and mapIntEditor all satisfy
// it. Methods mirror the listStrings surface; updateKey forwards a key
// press directly to the widget's own Update so the widget decides how to
// route to its text inputs.
type focusableList interface {
	Focus(bool)
	Focused() bool
	Editing() bool
	CancelEdit()
	View(int) string
	Label() string
	updateKey(tea.KeyPressMsg) tea.Cmd
}

// mixedPane stacks a scalar form above one or more list-like widgets,
// cycling focus with Tab. Section files compose it (shell, sandbox,
// indexing, commands, swarm). The list slot is polymorphic via
// focusableList, so list strings and map editors are interchangeable.
type mixedPane struct {
	form     *scalarPane
	lists    []focusableList
	focusIdx int // 0 = form, 1..n = lists[focusIdx-1]
}

func newMixedPane(form *scalarPane, lists ...focusableList) *mixedPane {
	return &mixedPane{form: form, lists: lists}
}

func (p *mixedPane) activeList() focusableList {
	if p.focusIdx == 0 {
		return nil
	}
	return p.lists[p.focusIdx-1]
}

func (p *mixedPane) Init() tea.Cmd { return p.form.Init() }

func (p *mixedPane) Update(msg tea.Msg) (sectionPane, tea.Cmd) {
	k, isKey := msg.(tea.KeyPressMsg)
	if isKey && !p.HasInnerFocus() {
		switch k.String() {
		case "tab":
			p.setFocus((p.focusIdx + 1) % (len(p.lists) + 1))
			return p, nil
		case "shift+tab":
			if p.focusIdx > 0 {
				p.setFocus(p.focusIdx - 1)
				return p, nil
			}
			return p, nil
		}
	}
	if l := p.activeList(); l != nil {
		if isKey {
			return p, l.updateKey(k)
		}
		return p, nil
	}
	updated, cmd := p.form.Update(msg)
	p.form = updated.(*scalarPane)
	return p, cmd
}

func (p *mixedPane) setFocus(idx int) {
	p.focusIdx = idx
	for i, l := range p.lists {
		l.Focus(i == idx-1)
	}
}

func (p *mixedPane) View(width int) string {
	parts := []string{p.form.View(width)}
	for _, l := range p.lists {
		parts = append(parts, l.View(width))
	}
	return strings.Join(parts, "\n\n")
}

func (p *mixedPane) SetWidth(w int) { p.form.SetWidth(w) }

func (p *mixedPane) HasInnerFocus() bool {
	l := p.activeList()
	return l != nil && l.Editing()
}

func (p *mixedPane) CloseInner() {
	if l := p.activeList(); l != nil {
		l.CancelEdit()
	}
}

func (p *mixedPane) FocusedFieldTitle() string {
	if l := p.activeList(); l != nil {
		return l.Label()
	}
	return p.form.FocusedFieldTitle()
}

func (p *mixedPane) AtFirstFocus() bool { return p.focusIdx == 0 }
