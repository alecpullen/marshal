package presetflow

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/textfield"
)

// ConfirmResult reports what a keypress did to an active ConfirmState.
type ConfirmResult int

const (
	// ConfirmPending means the screen is still active — nothing to do yet.
	ConfirmPending ConfirmResult = iota
	// ConfirmDone means the user confirmed; the caller should read
	// ConfirmState.Limits and proceed.
	ConfirmDone
	// ConfirmCancelled means the user backed out of the whole screen.
	ConfirmCancelled
)

type editingField int

const (
	editingNone editingField = iota
	editingContext
	editingOutput
)

// ConfirmState is the interactive "confirm model limits" screen's mutable
// state, embeddable by any Bubble Tea model that wants this screen inline
// (see connect.Model and settings.BrowserPanel).
type ConfirmState struct {
	Limits  Limits
	editing editingField
	input   textfield.Model
	Err     string
}

// NewConfirmState starts a confirm screen showing lim.
func NewConfirmState(lim Limits) *ConfirmState {
	return &ConfirmState{Limits: lim}
}

// Editing reports whether a context/output field is currently being
// hand-edited, so the caller knows to route non-key messages (e.g. paste)
// into UpdateInput.
func (s *ConfirmState) Editing() bool { return s.editing != editingNone }

// UpdateInput forwards msg to the active edit's text field. No-op when
// Editing() is false.
func (s *ConfirmState) UpdateInput(msg tea.Msg) tea.Cmd {
	if s.editing == editingNone {
		return nil
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return cmd
}

func (s *ConfirmState) startEditingContext() {
	s.editing = editingContext
	s.Err = ""
	ti := textfield.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	if s.Limits.ContextWindow > 0 {
		ti.SetValue(fmt.Sprintf("%d", s.Limits.ContextWindow))
	}
	s.input = ti
}

func (s *ConfirmState) startEditingOutput() {
	s.editing = editingOutput
	s.Err = ""
	ti := textfield.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	if s.Limits.MaxOutputTokens > 0 {
		ti.SetValue(fmt.Sprintf("%d", s.Limits.MaxOutputTokens))
	}
	s.input = ti
}

func (s *ConfirmState) commitEdit() {
	v := strings.TrimSpace(s.input.Value())
	n, err := strconv.Atoi(v)
	if err != nil {
		s.Err = "must be a number"
		return
	}
	if n < 0 {
		s.Err = "must not be negative"
		return
	}
	switch s.editing {
	case editingContext:
		if n == 0 {
			s.Limits.ContextWindow, s.Limits.ContextSource = 0, SourceUnknown
		} else {
			s.Limits.ContextWindow, s.Limits.ContextSource = n, SourceEdited
		}
	case editingOutput:
		if n == 0 {
			s.Limits.MaxOutputTokens, s.Limits.OutputSource = 0, SourceUnknown
		} else {
			s.Limits.MaxOutputTokens, s.Limits.OutputSource = n, SourceEdited
		}
	}
	s.editing = editingNone
	s.Err = ""
}

// HandleKey processes one keypress. While a field is being edited, all keys
// go to the text input except Enter (commits the edit) and Esc (cancels
// just the edit). Otherwise: 'c'/'o' start editing context/max output,
// Enter returns ConfirmDone, Esc returns ConfirmCancelled.
func (s *ConfirmState) HandleKey(k tea.KeyPressMsg) (ConfirmResult, tea.Cmd) {
	ks := k.String()
	if s.editing != editingNone {
		switch ks {
		case "enter":
			s.commitEdit()
			return ConfirmPending, nil
		case "esc":
			s.editing = editingNone
			s.Err = ""
			return ConfirmPending, nil
		default:
			return ConfirmPending, s.UpdateInput(k)
		}
	}
	switch ks {
	case "c":
		s.startEditingContext()
		return ConfirmPending, nil
	case "o":
		s.startEditingOutput()
		return ConfirmPending, nil
	case "enter":
		return ConfirmDone, nil
	case "esc":
		return ConfirmCancelled, nil
	}
	return ConfirmPending, nil
}

func renderInput(ti textfield.Model, pw int) string {
	inner := pw - 2
	line := ti.View()
	if len(line) > inner {
		line = line[:inner]
	}
	return line
}

// Render draws the confirm screen's body at width pw.
func (s *ConfirmState) Render(pw int) string {
	var b strings.Builder
	ctxVal, ctxSrc := s.Limits.ContextWindow, s.Limits.ContextSource
	outVal, outSrc := s.Limits.MaxOutputTokens, s.Limits.OutputSource

	ctxStr := fmt.Sprintf("%d", ctxVal)
	if ctxVal == 0 {
		ctxStr = hintStyle().Render("unknown — set a budget")
	}
	outStr := fmt.Sprintf("%d", outVal)
	if outVal == 0 {
		outStr = hintStyle().Render("unknown — set a budget")
	}

	switch s.editing {
	case editingContext:
		b.WriteString(mutedStyle().Render("context window    ") + renderInput(s.input, pw) + "\n")
		b.WriteString(mutedStyle().Render("max output        ") + titleStyle().Render(outStr) + mutedStyle().Render("   "+string(outSrc)) + "\n")
	case editingOutput:
		b.WriteString(mutedStyle().Render("context window    ") + titleStyle().Render(ctxStr) + mutedStyle().Render("   "+string(ctxSrc)) + "\n")
		b.WriteString(mutedStyle().Render("max output        ") + renderInput(s.input, pw) + "\n")
	default:
		b.WriteString(mutedStyle().Render("context window    ") + titleStyle().Render(ctxStr) + mutedStyle().Render("   "+string(ctxSrc)) + "\n")
		b.WriteString(mutedStyle().Render("max output        ") + titleStyle().Render(outStr) + mutedStyle().Render("   "+string(outSrc)) + "\n")
	}

	if s.Limits.ToolCalling != nil {
		tc := "none"
		if *s.Limits.ToolCalling {
			tc = "native"
		}
		b.WriteString(mutedStyle().Render("tool calling      ") + titleStyle().Render(tc) + mutedStyle().Render("   "+string(s.Limits.ToolSource)) + "\n")
	}

	if s.Err != "" {
		b.WriteString(errStyle().Render(s.Err) + "\n")
	}
	return b.String()
}
