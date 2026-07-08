package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/huhtheme"
)

// questionModel wraps a *huh.Form that captures the user's free-text answer
// to a clarifying question. It renders inline inside the chat input area.
type questionModel struct {
	form    *huh.Form
	q       *session.PendingQuestion
	answer  string
	width   int
	done    bool
	aborted bool
}

func newQuestionModel(q *session.PendingQuestion, width int) *questionModel {
	qm := &questionModel{q: q, width: width}

	in := huh.NewInput().
		Title(q.Question).
		Prompt("❯ ").
		Value(&qm.answer)

	group := huh.NewGroup(in)

	km := huh.NewDefaultKeyMap()
	// Esc declines (aborts the form → parent sends ""). Ctrl+C is
	// intercepted by the parent before reaching the form.
	km.Quit = key.NewBinding(key.WithKeys())
	km.Input.Submit = key.NewBinding(key.WithKeys("enter"))

	qm.form = huh.NewForm(group).
		WithTheme(huhtheme.WarmSunset()).
		WithWidth(max(width, 30)).
		WithShowHelp(false).
		WithKeyMap(km)

	if cmd := qm.form.Init(); cmd != nil {
		_ = cmd()
	}
	return qm
}

func (qm *questionModel) SetSize(width int) {
	qm.width = width
	if qm.form != nil {
		qm.form.WithWidth(max(width, 30))
	}
}

func (qm *questionModel) Update(msg tea.Msg) (*questionModel, tea.Cmd) {
	if qm.form == nil || qm.done {
		return qm, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" {
		qm.done = true
		qm.aborted = true
		return qm, nil
	}
	updated, cmd := qm.form.Update(msg)
	if f, ok := updated.(*huh.Form); ok {
		qm.form = f
	}
	if qm.form.State == huh.StateCompleted {
		qm.done = true
		return qm, nil
	}
	if qm.form.State == huh.StateAborted {
		qm.done = true
		qm.aborted = true
		return qm, nil
	}
	return qm, cmd
}

func (qm *questionModel) View() string {
	if qm.form == nil {
		return ""
	}
	return qm.form.View()
}

func (qm *questionModel) Answer() string { return qm.answer }
func (qm *questionModel) Aborted() bool  { return qm.aborted }
func (qm *questionModel) IsDone() bool   { return qm.done }
