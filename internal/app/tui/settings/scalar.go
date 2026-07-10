package settings

import (
	"fmt"
	"strconv"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"marshal/internal/app/tui/huhtheme"
)

type scalarPane struct {
	form  *huh.Form
	build func() *huh.Form
	width int
}

func newScalarPane(build func() *huh.Form) *scalarPane {
	p := &scalarPane{build: build}
	p.form = build()
	if c := p.form.Init(); c != nil {
		_ = c()
	}
	return p
}

func settingsKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c"))
	km.Input.Submit = key.NewBinding(key.WithKeys("ctrl+s"))
	km.Input.Next = key.NewBinding(key.WithKeys("enter", "tab", "down"))
	km.Input.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"))
	km.Confirm.Submit = key.NewBinding(key.WithKeys("ctrl+s"))
	km.Confirm.Next = key.NewBinding(key.WithKeys("enter", "tab", "down"))
	km.Confirm.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"))
	km.Select.Submit = key.NewBinding(key.WithKeys("ctrl+s"))
	km.Text.Submit = key.NewBinding(key.WithKeys("ctrl+s"))
	km.Text.Next = key.NewBinding(key.WithKeys("enter", "tab", "down"))
	km.Text.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"))
	// h/l are sidebar-return keys in the two-pane layout; keep arrows+space
	// for confirm toggling.
	km.Confirm.Toggle = key.NewBinding(key.WithKeys("right", "left", "space"))
	return km
}

// newSectionForm builds a huh form with the shared settings theme + keymap.
func newSectionForm(fields ...huh.Field) *huh.Form {
	return huh.NewForm(huh.NewGroup(fields...)).
		WithTheme(huhtheme.WarmSunset()).
		WithShowHelp(false).
		WithKeyMap(settingsKeyMap())
}

func numField(label string, value *string, min int, set func(int)) *huh.Input {
	return huh.NewInput().
		Key(label).
		Title(label).
		Value(value).
		Validate(func(s string) error {
			v, err := strconv.Atoi(s)
			if err != nil {
				return fmt.Errorf("must be a number")
			}
			if min != 0 && v < min {
				v = min
			}
			*value = strconv.Itoa(v)
			set(v)
			return nil
		})
}

func (p *scalarPane) Init() tea.Cmd { return nil }

func (p *scalarPane) Update(msg tea.Msg) (sectionPane, tea.Cmd) {
	updated, cmd := p.form.Update(msg)
	if f, ok := updated.(*huh.Form); ok {
		p.form = f
	}
	if p.form.State == huh.StateCompleted || p.form.State == huh.StateAborted {
		// A pane form never "finishes": rebuild so editing can continue.
		p.form = p.build()
		p.form.WithWidth(p.width)
		if c := p.form.Init(); c != nil {
			_ = c()
		}
	}
	return p, cmd
}

func (p *scalarPane) View(width int) string { return p.form.View() }

func (p *scalarPane) SetWidth(w int) {
	p.width = w
	p.form.WithWidth(w)
}

func (p *scalarPane) HasInnerFocus() bool { return false }
func (p *scalarPane) CloseInner()         {}
func (p *scalarPane) AtFirstFocus() bool  { return true }

func (p *scalarPane) FocusedFieldTitle() string {
	if f := p.form.GetFocusedField(); f != nil {
		return f.GetKey()
	}
	return ""
}
