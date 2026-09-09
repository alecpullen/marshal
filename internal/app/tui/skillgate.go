package tui

import (
	"strings"

	"charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
)

// skillGateOptions is the ordered option list of the skill-gate dialog.
var skillGateOptions = []struct {
	choice session.SkillGateChoice
	label  string
	detail string
}{
	{session.SkillGateAllowOnce, "Allow once", "load this skill for this call"},
	{session.SkillGateAllowSkill, "Allow always (this skill)", "session-scope: no more prompts for this skill"},
	{session.SkillGateAllowAll, "Allow always (all skills)", "session-scope: turns the gate off"},
	{session.SkillGateDeny, "Deny", "tell the model it does not need it now"},
}

// skillGateModel renders the dedicated skill-load gate dialog inline in
// the input area. Four options, navigated with up/down or j/k, jumped to
// with 1-4, confirmed with Enter. Esc moves the selection to Deny (it does
// not respond — an accidental Esc must not record a sticky deny).
type skillGateModel struct {
	sg       *session.PendingSkillGate
	selected int
	width    int
	done     bool
}

func newSkillGateModel(sg *session.PendingSkillGate, width int) *skillGateModel {
	return &skillGateModel{sg: sg, width: max(width, 30)}
}

func (m *skillGateModel) SetSize(width int) { m.width = max(width, 30) }

func (m *skillGateModel) Update(msg tea.Msg) (*skillGateModel, tea.Cmd) {
	if m.done {
		return m, nil
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "up", "k":
		m.selected = (m.selected + len(skillGateOptions) - 1) % len(skillGateOptions)
	case "down", "j":
		m.selected = (m.selected + 1) % len(skillGateOptions)
	case "1", "2", "3", "4":
		m.selected = int(k.String()[0] - '1')
	case "enter":
		m.done = true
	case "esc":
		m.selected = len(skillGateOptions) - 1 // Deny; user still confirms with Enter
	}
	return m, nil
}

func (m *skillGateModel) Choice() session.SkillGateChoice {
	return skillGateOptions[m.selected].choice
}

func (m *skillGateModel) IsDone() bool { return m.done }

func (m *skillGateModel) View() string {
	gutter := gutterPrefix(glyph.Question, violetColor)
	indent := strings.Repeat(" ", 3)
	contentWidth := max(m.width-4, 1)

	var b strings.Builder
	title := "Load skill '" + m.sg.Skill + "'?"
	for j, line := range strings.Split(ansi.Wrap(title, contentWidth, WrapBreakpoints), "\n") {
		if j == 0 {
			b.WriteString(gutter)
		} else {
			b.WriteString(indent)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if m.sg.Description != "" {
		for _, line := range strings.Split(ansi.Wrap(m.sg.Description, contentWidth, WrapBreakpoints), "\n") {
			b.WriteString(indent)
			b.WriteString(mutedStyle().Render(line))
			b.WriteString("\n")
		}
	}
	for _, line := range strings.Split(ansi.Wrap(m.sg.Reason, contentWidth, WrapBreakpoints), "\n") {
		b.WriteString(indent)
		b.WriteString(mutedStyle().Render(line))
		b.WriteString("\n")
	}
	for i, opt := range skillGateOptions {
		row := opt.label + " — " + opt.detail
		if i == m.selected {
			row = "❯ " + row
		} else {
			row = "  " + row
		}
		b.WriteString(indent)
		b.WriteString(row)
		b.WriteString("\n")
	}
	return chromeRail(b.String(), violetColor)
}

// renderSkillGatePanel is the fallback static panel shown before the
// interactive model is built (mirrors renderQuestionPanel's role).
func renderSkillGatePanel(sg *session.PendingSkillGate, width int) string {
	m := &skillGateModel{sg: sg, width: max(width, 30)}
	return m.View()
}
