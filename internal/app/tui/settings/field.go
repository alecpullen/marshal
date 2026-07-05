package settings

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type field interface {
	Label() string
	Focus()
	Blur()
	Update(msg tea.Msg) tea.Cmd
	View(width int) string
}

type intField struct {
	label    string
	input    textinput.Model
	value    *int
	onChange func(int)
}

func newIntField(label string, value *int, onChange func(int)) *intField {
	inp := textinput.New()
	inp.SetValue(strconv.Itoa(*value))
	inp.Prompt = ""
	return &intField{label: label, input: inp, value: value, onChange: onChange}
}

func (f *intField) Label() string { return f.label }

func (f *intField) Focus() {
	f.input.Focus()
}

func (f *intField) Blur() {
	f.input.Blur()
}

func (f *intField) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		var cmd tea.Cmd
		f.input, cmd = f.input.Update(msg)
		next := strings.TrimSpace(f.input.Value())
		if next == "" {
			*f.value = 0
			if f.onChange != nil {
				f.onChange(0)
			}
			return cmd
		}
		n, err := strconv.Atoi(next)
		if err != nil || n < 0 {
			f.input.SetValue(strconv.Itoa(*f.value))
			f.input.CursorEnd()
			return cmd
		}
		*f.value = n
		if f.onChange != nil {
			f.onChange(n)
		}
		return cmd
	}
	return nil
}

func (f *intField) View(width int) string {
	label := f.label + ": "
	available := width - len([]rune(label))
	if available < 1 {
		available = 1
	}
	f.input.Width = available
	return label + f.input.View()
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

func (f *stringField) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		var cmd tea.Cmd
		f.input, cmd = f.input.Update(msg)
		if f.onChange != nil {
			f.onChange(f.input.Value())
		}
		return cmd
	}
	return nil
}

func (f *stringField) View(width int) string {
	label := f.label + ": "
	available := width - len([]rune(label))
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

func (f *labelField) Update(msg tea.Msg) tea.Cmd { return nil }

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

func (f *boolField) Update(msg tea.Msg) tea.Cmd {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeySpace, tea.KeyEnter:
			*f.value = !*f.value
			if f.onChange != nil {
				f.onChange(*f.value)
			}
		}
	}
	return nil
}

func (f *boolField) View(width int) string {
	marker := " "
	if *f.value {
		marker = "x"
	}
	s := fmt.Sprintf("[%s] %s", marker, f.label)
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

func (f *selectField) Update(msg tea.Msg) tea.Cmd {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeyLeft:
			if f.selected > 0 {
				f.selected--
				if f.onChange != nil {
					f.onChange(f.options[f.selected])
				}
			}
		case tea.KeyRight, tea.KeyEnter, tea.KeySpace:
			if f.selected < len(f.options)-1 {
				f.selected++
				if f.onChange != nil {
					f.onChange(f.options[f.selected])
				}
			}
		}
	}
	return nil
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
	s := fmt.Sprintf("%s: %s", f.label, strings.Join(parts, "  "))
	return truncateRunes(s, width)
}

func (f *selectField) Value() string {
	if len(f.options) == 0 {
		return ""
	}
	return f.options[f.selected]
}
