package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"

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
// The View renders each question's text itself (markdown-rendered and
// word-wrapped, with the ? gutter) plus the focused huh field's own View —
// that field View is what makes free-text answers echo as they are typed
// and option lists navigable. huh field titles stay empty so the question
// text is not duplicated. Every line wears the ▍ chrome rail in violet.
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
	// fields[i] lists the huh fields belonging to question i (a select plus
	// its "Other" input, or a single input/multi-select).
	fields [][]huh.Field
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
		fields:  make([][]huh.Field, len(q.Questions)),
	}

	var fields []huh.Field
	for i, qst := range q.Questions {
		hasOptions := len(qst.Options) > 0
		switch {
		case hasOptions && qst.Multi:
			ms := make([]string, 0)
			qm.multis[i] = &ms
			multi := huh.NewMultiSelect[string]().
				Options(buildQuestionOptions(qst.Options, false)...).
				Value(qm.multis[i]).
				Height(8)
			fields = append(fields, multi)
			qm.fields[i] = append(qm.fields[i], multi)
		case hasOptions:
			v := ""
			qm.selects[i] = &v
			sel := huh.NewSelect[string]().
				Options(buildQuestionOptions(qst.Options, qst.AllowOther)...).
				Value(qm.selects[i])
			fields = append(fields, sel)
			qm.fields[i] = append(qm.fields[i], sel)
			if qst.AllowOther {
				fv := ""
				qm.others[i] = &fv
				other := huh.NewInput().
					Title("Custom answer (since you picked Other)").
					Prompt("❯ ").
					Value(qm.others[i])
				fields = append(fields, other)
				qm.fields[i] = append(qm.fields[i], other)
			}
		default:
			v := ""
			qm.inputs[i] = &v
			in := huh.NewInput().
				Prompt("❯ ").
				Value(qm.inputs[i])
			fields = append(fields, in)
			qm.fields[i] = append(qm.fields[i], in)
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
		WithWidth(questionFieldWidth(qm.width)).
		WithShowHelp(false).
		WithKeyMap(km)

	qm.initCmd = qm.form.Init()
	return qm
}

// questionFieldWidth is the width given to the huh form: the panel width
// minus the ▍ rail cell and the 3-cell indent the View gives field content.
func questionFieldWidth(panelWidth int) int {
	return max(panelWidth-4, 30)
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
		qm.form.WithWidth(questionFieldWidth(max(width, 30)))
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

// renderQuestionText renders the question body as markdown wrapped to
// width, falling back to plain wrapped text when glamour is unavailable.
func renderQuestionText(text string, width int) string {
	if out, ok := renderMarkdown(text, width); ok {
		return strings.Trim(out, "\n")
	}
	return ansi.Wrap(text, width, "")
}

func (qm *questionModel) View() string {
	if qm.form == nil {
		return ""
	}
	gutter := gutterPrefix("?", violetColor)
	indent := strings.Repeat(" ", 3)
	// contentWidth leaves room for the ▍ rail cell plus the 3-cell gutter.
	contentWidth := max(qm.width-4, 1)
	focused := qm.form.GetFocusedField()

	var b strings.Builder
	for i, qs := range qm.q.Questions {
		if ans := qm.answers[i].Answer; ans != "" && ans != session.AnswerUnanswered {
			for j, line := range strings.Split(ansi.Wrap(qs.Question+" · "+ans, contentWidth, ""), "\n") {
				if j == 0 {
					b.WriteString(gutter)
				} else {
					b.WriteString(indent)
				}
				b.WriteString(mutedStyle().Render(line))
				b.WriteString("\n")
			}
			continue
		}
		// Glamour's document margin adds 2 cells beyond the wrap width, so
		// wrap 2 narrower to keep every line inside contentWidth.
		for j, line := range strings.Split(renderQuestionText(qs.Question, max(contentWidth-2, 1)), "\n") {
			if j == 0 {
				b.WriteString(gutter)
			} else {
				b.WriteString(indent)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		if field := focusedFieldFor(focused, qm.fields[i]); field != nil {
			for _, line := range strings.Split(strings.TrimRight(field.View(), "\n"), "\n") {
				b.WriteString(indent)
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
	}
	// Chrome rail: violet while a question is pending (see inputBarColor).
	return chromeRail(b.String(), violetColor)
}

// focusedFieldFor returns focused when it belongs to the given question's
// field list, nil otherwise. huh.Field implementations are pointers, so
// interface comparison is an identity check.
func focusedFieldFor(focused huh.Field, fields []huh.Field) huh.Field {
	for _, f := range fields {
		if f == focused {
			return f
		}
	}
	return nil
}

func (qm *questionModel) Answers() []session.Answer { return qm.answers }
func (qm *questionModel) Aborted() bool             { return qm.aborted }
func (qm *questionModel) IsDone() bool              { return qm.done }
