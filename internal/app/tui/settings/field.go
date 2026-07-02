package settings

import (
	"fmt"
	"strings"

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
		f.onChange(f.input.Value())
	}
}

func (f *stringField) View(width int) string {
	return fmt.Sprintf("%s: %s", f.label, f.input.View())
}

type boolField struct {
	label    string
	value    *bool
	onChange func(bool)
}

func newBoolField(label string, desc string, value *bool, onChange func(bool)) *boolField {
	return &boolField{label: fmt.Sprintf("%s (%s)", label, desc), value: value, onChange: onChange}
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
	marker := " "
	if *f.value {
		marker = "x"
	}
	return fmt.Sprintf("[%s] %s", marker, f.label)
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
				f.onChange(f.options[f.selected])
			}
		case tea.KeyRight, tea.KeyDown, tea.KeyEnter, tea.KeySpace:
			if f.selected < len(f.options)-1 {
				f.selected++
				f.onChange(f.options[f.selected])
			}
		}
	}
}

func (f *selectField) View(width int) string {
	var parts []string
	for i, opt := range f.options {
		if i == f.selected {
			parts = append(parts, fmt.Sprintf(">%s<", opt))
		} else {
			parts = append(parts, opt)
		}
	}
	return fmt.Sprintf("%s: %s", f.label, strings.Join(parts, "  "))
}

func (f *selectField) Value() string {
	if len(f.options) == 0 {
		return ""
	}
	return f.options[f.selected]
}
