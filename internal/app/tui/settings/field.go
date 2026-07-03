package settings

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type field interface {
	Label() string
	Focus()
	Blur()
	Update(msg tea.Msg)
	View(width int) string
}

type stringField struct {
	label    string
	input    textinput.Model
	onChange func(string)
}

func newStringField(label, value string, onChange func(string)) *stringField {
	inp := textinput.New()
	inp.SetValue(value)
	inp.Prompt = ""
	return &stringField{label: label, input: inp, onChange: onChange}
}

func (f *stringField) Label() string { return f.label }

func (f *stringField) Focus() {
	f.input.Focus()
}

func (f *stringField) Blur() {
	f.input.Blur()
}

func (f *stringField) Update(msg tea.Msg) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		f.input, _ = f.input.Update(msg)
		if f.onChange != nil {
			f.onChange(f.input.Value())
		}
	}
}

func (f *stringField) View(width int) string {
	label := f.label + ": "
	available := width - len([]rune(label)) - 2 // cursor / focus prefix
	if available < 1 {
		available = 1
	}
	f.input.Width = available
	return label + f.input.View()
}

type labelField struct {
	label string
	value string
}

func newLabelField(label, value string) *labelField {
	return &labelField{label: label, value: value}
}

func (f *labelField) Label() string { return f.label }

func (f *labelField) Focus() {}

func (f *labelField) Blur() {}

func (f *labelField) Update(msg tea.Msg) {}

func (f *labelField) View(width int) string {
	return truncateRunes(f.label+": "+f.value, width)
}

type boolField struct {
	label       string
	description string
	value       *bool
	onChange    func(bool)
}

func newBoolField(label string, desc string, value *bool, onChange func(bool)) *boolField {
	return &boolField{label: label, description: desc, value: value, onChange: onChange}
}

func (f *boolField) Label() string { return f.label }

func (f *boolField) Focus() {}

func (f *boolField) Blur() {}

func (f *boolField) Update(msg tea.Msg) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeySpace, tea.KeyEnter:
			*f.value = !*f.value
			if f.onChange != nil {
				f.onChange(*f.value)
			}
		}
	}
}

func (f *boolField) View(width int) string {
	val := "false"
	if *f.value {
		val = "true"
	}
	s := fmt.Sprintf("%s: %s", f.label, val)
	if f.description != "" {
		s += " (" + f.description + ")"
	}
	return truncateRunes(s, width)
}

func (f *boolField) Value() bool { return *f.value }

type selectField struct {
	label    string
	options  []string
	selected int
	onChange func(string)
}

func newSelectField(label string, options []string, current string, onChange func(string)) *selectField {
	selected := 0
	for i, opt := range options {
		if opt == current {
			selected = i
			break
		}
	}
	return &selectField{label: label, options: options, selected: selected, onChange: onChange}
}

func (f *selectField) Label() string { return f.label }

func (f *selectField) Focus() {}

func (f *selectField) Blur() {}

func (f *selectField) Update(msg tea.Msg) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeyLeft, tea.KeyUp:
			if f.selected > 0 {
				f.selected--
				if f.onChange != nil {
					f.onChange(f.options[f.selected])
				}
			}
		case tea.KeyRight, tea.KeyDown, tea.KeyEnter, tea.KeySpace:
			if f.selected < len(f.options)-1 {
				f.selected++
				if f.onChange != nil {
					f.onChange(f.options[f.selected])
				}
			}
		}
	}
}

func (f *selectField) View(width int) string {
	s := f.label + ": " + f.Value()
	return truncateRunes(s, width)
}

func (f *selectField) Value() string {
	if len(f.options) == 0 {
		return ""
	}
	return f.options[f.selected]
}
