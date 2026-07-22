package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/huhtheme"
)

const questionOtherSentinel = "other"

// questionModel wraps a *huh.Form that captures answers for one or more
// clarifying questions presented in a single round-trip. It renders inline
// inside the chat input area. For each Question:
//   - Options empty                                → huh.NewInput (free-text)
//   - Options + !Multi                             → huh.NewSelect single-choice
//   - Options + Multi                              → huh.NewMultiSelect
//   - Options + AllowOther (single-choice only)    → huh.NewSelect with an
//     "Other" sentinel option followed by a NewInput that captures the
//     custom answer when the sentinel is picked.
//
// Pressing Esc on any question marks every remaining question as
// session.AnswerUnanswered.
type questionModel struct {
	form    *huh.Form
	q       *session.PendingQuestion
	width   int
	done    bool
	aborted bool
	initCmd tea.Cmd

	answers []session.Answer
	inputs  []*string
	selects []*string
	multis  []*[]string
	others  []*string
}

func newQuestionModel(q *session.PendingQuestion, width int) *questionModel {
	qm := &questionModel{
		q:       q,
		width:   max(width, 30),
		answers: session.UnansweredAnswers(q.Questions),
		inputs:  make([]*string, len(q.Questions)),
		selects: make([]*string, len(q.Questions)),
		multis:  make([]*[]string, len(q.Questions)),
		others:  make([]*string, len(q.Questions)),
	}

	var fields []huh.Field
	for i, qst := range q.Questions {
		hasOptions := len(qst.Options) > 0
		switch {
		case hasOptions && qst.Multi:
			ms := make([]string, 0)
			qm.multis[i] = &ms
			multi := huh.NewMultiSelect[string]().
				Title(qst.Question).
				Options(buildQuestionOptions(qst.Options, false)...).
				Value(qm.multis[i]).
				Height(8)
			fields = append(fields, multi)
		case hasOptions:
			v := ""
			qm.selects[i] = &v
			sel := huh.NewSelect[string]().
				Title(qst.Question).
				Options(buildQuestionOptions(qst.Options, qst.AllowOther)...).
				Value(qm.selects[i])
			fields = append(fields, sel)
			if qst.AllowOther {
				fv := ""
				qm.others[i] = &fv
				fields = append(fields, huh.NewInput().
					Title("Custom answer (since you picked Other)").
					Prompt("❯ ").
					Value(qm.others[i]))
			}
		default:
			v := ""
			qm.inputs[i] = &v
			in := huh.NewInput().
				Title(qst.Question).
				Prompt("❯ ").
				Value(qm.inputs[i])
			fields = append(fields, in)
		}
	}

	group := huh.NewGroup(fields...)

	km := huh.NewDefaultKeyMap()
	// Esc declines every remaining (unanswered) question. Ctrl+C is
	// intercepted by the parent before reaching the form.
	km.Quit = key.NewBinding(key.WithKeys())
	km.Input.Submit = key.NewBinding(key.WithKeys("enter"))
	km.Select.Submit = key.NewBinding(key.WithKeys("enter"))

	qm.form = huh.NewForm(group).
		WithTheme(huhtheme.WarmSunset()).
		WithWidth(max(width, 30)).
		WithShowHelp(false).
		WithKeyMap(km)

	qm.initCmd = qm.form.Init()
	return qm
}

// buildQuestionOptions constructs a huh option list. When allowOther is
// true, an extra "Other" sentinel option is appended; the user can then use
// the follow-up NewInput to type a custom answer.
func buildQuestionOptions(in []string, allowOther bool) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(in)+1)
	for _, o := range in {
		opts = append(opts, huh.NewOption(o, o))
	}
	if allowOther {
		opts = append(opts, huh.NewOption("Other…", questionOtherSentinel))
	}
	return opts
}

func (qm *questionModel) Init() tea.Cmd { return qm.initCmd }

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
		qm.finalizeAnswers()
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

func (qm *questionModel) finalizeAnswers() {
	for i, qst := range qm.q.Questions {
		hasOptions := len(qst.Options) > 0
		switch {
		case hasOptions && qst.Multi:
			if qm.multis[i] != nil && len(*qm.multis[i]) > 0 {
				qm.answers[i].Answer = strings.Join(*qm.multis[i], ", ")
			}
		case hasOptions:
			if qm.selects[i] != nil {
				pick := *qm.selects[i]
				if pick == questionOtherSentinel {
					if qm.others[i] != nil {
						custom := strings.TrimSpace(*qm.others[i])
						if custom != "" {
							qm.answers[i].Answer = custom
						}
					}
				} else if pick != "" {
					qm.answers[i].Answer = pick
				}
			}
		default:
			if qm.inputs[i] != nil {
				qm.answers[i].Answer = strings.TrimSpace(*qm.inputs[i])
			}
		}
	}
}

func (qm *questionModel) View() string {
	if qm.form == nil {
		return ""
	}
	// Infer the focused field index from answer state (huh.Form does not
	// expose a public Fields() method).
	focusedIdx := 0
	for i, a := range qm.answers {
		if a.Answer == session.AnswerUnanswered || a.Answer == "" {
			focusedIdx = i
			break
		}
	}

	gutter := gutterPrefix("?", violetColor)
	var b strings.Builder
	for i, qs := range qm.q.Questions {
		if i == focusedIdx {
			b.WriteString(gutter)
			b.WriteString(lipgloss.NewStyle().Foreground(violetColor).Bold(true).Render(qs.Question))
		} else if qm.answers[i].Answer != "" && qm.answers[i].Answer != session.AnswerUnanswered {
			b.WriteString(gutter)
			b.WriteString(mutedStyle().Render(qs.Question + " · " + qm.answers[i].Answer))
		} else {
			b.WriteString(gutter)
			b.WriteString(mutedStyle().Render(qs.Question))
		}
		b.WriteString("\n")
		if i == focusedIdx && len(qs.Options) > 0 {
			opts := "(" + strings.Join(qs.Options, " / ") + ")"
			b.WriteString(strings.Repeat(" ", 3))
			b.WriteString(mutedStyle().Render(opts))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (qm *questionModel) Answers() []session.Answer { return qm.answers }
func (qm *questionModel) Aborted() bool             { return qm.aborted }
func (qm *questionModel) IsDone() bool              { return qm.done }
