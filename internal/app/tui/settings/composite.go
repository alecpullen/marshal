package settings

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// compositePane stacks a scalar form above a collection and one or more
// map editors, cycling focus with Tab. Section files compose it (MCP).
type compositePane struct {
	form       *scalarPane
	collection *collectionPane
	mapEditors []*mapEditor
	focusIdx   int
	width      int
}

func newCompositePane(form *scalarPane, collection *collectionPane, maps ...*mapEditor) *compositePane {
	return &compositePane{form: form, collection: collection, mapEditors: maps}
}

func (p *compositePane) AtFirstFocus() bool { return p.focusIdx == 0 }

func (p *compositePane) Init() tea.Cmd { return p.form.Init() }

func (p *compositePane) activeMap() *mapEditor {
	if p.focusIdx < 2 {
		return nil
	}
	return p.mapEditors[p.focusIdx-2]
}

func (p *compositePane) Update(msg tea.Msg) (sectionPane, tea.Cmd) {
	k, isKey := msg.(tea.KeyPressMsg)
	if isKey && !p.HasInnerFocus() {
		switch k.String() {
		case "tab":
			p.setFocus((p.focusIdx + 1) % (2 + len(p.mapEditors)))
			return p, nil
		case "shift+tab":
			if p.focusIdx > 0 {
				p.setFocus(p.focusIdx - 1)
				return p, nil
			}
			return p, nil
		}
	}
	switch p.focusIdx {
	case 0:
		updated, cmd := p.form.Update(msg)
		p.form = updated.(*scalarPane)
		return p, cmd
	case 1:
		updated, cmd := p.collection.Update(msg)
		if cp, ok := updated.(*collectionPane); ok {
			p.collection = cp
		}
		return p, cmd
	default:
		me := p.activeMap()
		if me == nil {
			return p, nil
		}
		if isKey {
			return p, me.Update(k)
		}
		return p, nil
	}
}

func (p *compositePane) setFocus(idx int) {
	p.focusIdx = idx
	for i, me := range p.mapEditors {
		me.Focus(i == idx-2)
	}
}

func (p *compositePane) SetWidth(w int) {
	p.width = w
	p.form.SetWidth(w)
	p.collection.SetWidth(w)
	for _, me := range p.mapEditors {
		me.SetWidth(w)
	}
}

func (p *compositePane) View(width int) string {
	parts := []string{p.form.View(width), p.collection.View(width)}
	for _, me := range p.mapEditors {
		parts = append(parts, me.View(width))
	}
	return strings.Join(parts, "\n\n")
}

func (p *compositePane) HasInnerFocus() bool {
	return p.collection.HasInnerFocus() ||
		(p.activeMap() != nil && p.activeMap().Editing())
}

func (p *compositePane) CloseInner() {
	switch p.focusIdx {
	case 1:
		p.collection.CloseInner()
	default:
		if me := p.activeMap(); me != nil {
			me.CancelEdit()
		}
	}
}

func (p *compositePane) FocusedFieldTitle() string {
	switch p.focusIdx {
	case 0:
		return p.form.FocusedFieldTitle()
	case 1:
		return p.collection.FocusedFieldTitle()
	default:
		if me := p.activeMap(); me != nil {
			return me.FocusedFieldTitle()
		}
	}
	return ""
}
