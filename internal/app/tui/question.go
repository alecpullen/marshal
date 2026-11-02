package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/huhtheme"
)

// questionOtherSentinel is the internal huh value of the appended
// custom-answer row. It is deliberately not derived from any supplied label,
// so a listed option called "other" (or "custom") stays a listed option.
const questionOtherSentinel = "custom"

// questionCustomLabel is the rendered key of the appended custom-answer row.
// It says what it is, so a supplied "Other…" label can be told apart from it.
const questionCustomLabel = "Other… (custom answer)"

// questionOptionPrefix namespaces generated option identities.
const questionOptionPrefix = "option:"

// questionOptionValue is the internal huh value of the supplied option at
// index. Generated identities never reach a recorded answer: finalize decodes
// them back to the supplied label with questionOptionLabel, so the answer
// carries what the user saw and the wire format is unchanged.
func questionOptionValue(index int) string {
	return questionOptionPrefix + strconv.Itoa(index)
}

// questionOptionLabel decodes a generated option identity back to its
// original label. It accepts only a canonical, in-range identity, so a
// malformed or out-of-range value is never emitted as an answer.
func questionOptionLabel(options []acpwire.QuestionOption, value string) (string, bool) {
	if !strings.HasPrefix(value, questionOptionPrefix) {
		return "", false
	}
	digits := strings.TrimPrefix(value, questionOptionPrefix)
	index, err := strconv.Atoi(digits)
	if err != nil || strconv.Itoa(index) != digits {
		// Not the canonical decimal form questionOptionValue produces, so it
		// is not an identity this model generated.
		return "", false
	}
	if index < 0 || index >= len(options) {
		return "", false
	}
	return options[index].Label, true
}

// questionModel wraps a *huh.Form that captures answers for one or more
// clarifying questions presented in a single round-trip. It renders inline
// inside the chat input area. For each Question:
//   - Options empty          → huh.NewInput (free-text)
//   - Options + Multi        → huh.NewMultiSelect
//   - Options (single or multi) → a select plus an 'Other…' sentinel; every
//     options question always gets the sentinel and a custom-answer input in
//     its own huh.Group, hidden unless the sentinel is picked.
//
// The View renders each question's text itself (markdown-rendered and
// word-wrapped, with the ? gutter) plus the focused huh field's own View —
// that field View is what makes free-text answers echo as they are typed
// and option lists navigable. huh field titles stay empty so the question
// text is not duplicated. Every line wears the ▍ chrome rail in violet.
//
// Pressing Esc skips the focused question and every question after it:
// earlier questions, which the form has already advanced past, keep the
// answers the user submitted, and the skipped ones are recorded as
// acpwire.AnswerUnanswered. Live input on the focused question (a
// highlighted default, typed text, an unsubmitted toggle) is not an answer
// and is not copied.
type questionModel struct {
	form    *huh.Form
	q       acpwire.QuestionRequest
	width   int
	done    bool
	aborted bool
	initCmd tea.Cmd

	answers []acpwire.Answer
	inputs  []*string
	selects []*string
	multis  []*[]string
	others  []*string
	// fields[i] lists the huh fields belonging to question i (a select plus
	// its "Other" input, or a single input/multi-select).
	fields [][]huh.Field
}

// unansweredAnswers is one AnswerUnanswered answer per question, in order.
func unansweredAnswers(questions []acpwire.Question) []acpwire.Answer {
	answers := make([]acpwire.Answer, len(questions))
	for i, q := range questions {
		answers[i] = acpwire.Answer{Question: q.Question, Answer: acpwire.AnswerUnanswered}
	}
	return answers
}

func newQuestionModel(q acpwire.QuestionRequest, width int) *questionModel {
	qm := &questionModel{
		q:       q,
		width:   max(width, 30),
		answers: unansweredAnswers(q.Questions),
		inputs:  make([]*string, len(q.Questions)),
		selects: make([]*string, len(q.Questions)),
		multis:  make([]*[]string, len(q.Questions)),
		others:  make([]*string, len(q.Questions)),
		fields:  make([][]huh.Field, len(q.Questions)),
	}
	qm.buildForm()
	return qm
}

// buildForm constructs the huh form groups for the question surface.
func (qm *questionModel) buildForm() {

	// One huh.Group per question field. The custom-answer input must live in
	// its own group so it can be hidden independently: by the time huh
	// evaluates the next group's hide func, the previous field's bound value
	// is committed, so a user who picked a listed option never gets walked
	// into the free-text input.
	var groups []*huh.Group
	for i, qst := range qm.q.Questions {
		hasOptions := len(qst.Options) > 0
		switch {
		case hasOptions && qst.Multi:
			ms := make([]string, 0)
			qm.multis[i] = &ms
			multi := huh.NewMultiSelect[string]().
				Options(buildQuestionOptions(qst.Options)...).
				Value(qm.multis[i]).
				Height(8)
			groups = append(groups, huh.NewGroup(multi))
			qm.fields[i] = append(qm.fields[i], multi)

			fv := ""
			qm.others[i] = &fv
			other := huh.NewInput().
				Title("Custom answer (since you picked Other)").
				Prompt("❯ ").
				Value(qm.others[i])
			idx := i
			groups = append(groups, huh.NewGroup(other).WithHideFunc(func() bool {
				if qm.multis[idx] == nil {
					return true
				}
				for _, p := range *qm.multis[idx] {
					if p == questionOtherSentinel {
						return false
					}
				}
				return true
			}))
			qm.fields[i] = append(qm.fields[i], other)
		case hasOptions:
			v := ""
			qm.selects[i] = &v
			sel := huh.NewSelect[string]().
				Options(buildQuestionOptions(qst.Options)...).
				Value(qm.selects[i])
			groups = append(groups, huh.NewGroup(sel))
			qm.fields[i] = append(qm.fields[i], sel)

			fv := ""
			qm.others[i] = &fv
			other := huh.NewInput().
				Title("Custom answer (since you picked Other)").
				Prompt("❯ ").
				Value(qm.others[i])
			idx := i
			groups = append(groups, huh.NewGroup(other).WithHideFunc(func() bool {
				return qm.selects[idx] == nil || *qm.selects[idx] != questionOtherSentinel
			}))
			qm.fields[i] = append(qm.fields[i], other)
		default:
			v := ""
			qm.inputs[i] = &v
			in := huh.NewInput().
				Prompt("❯ ").
				Value(qm.inputs[i])
			groups = append(groups, huh.NewGroup(in))
			qm.fields[i] = append(qm.fields[i], in)
		}
	}

	km := huh.NewDefaultKeyMap()
	// Esc skips the focused question and every question after it; the ones
	// the form already advanced past keep their submitted answers (see
	// skipRemaining). Ctrl+C is intercepted by the parent before reaching
	// the form.
	km.Quit = key.NewBinding(key.WithKeys())
	km.Input.Submit = key.NewBinding(key.WithKeys("enter"))
	km.Select.Submit = key.NewBinding(key.WithKeys("enter"))

	qm.form = huh.NewForm(groups...).
		WithTheme(huhtheme.WarmSunset()).
		WithWidth(questionFieldWidth(qm.width)).
		WithShowHelp(false).
		WithKeyMap(km)

	qm.initCmd = qm.form.Init()
}

// questionFieldWidth is the width given to the huh form: the panel width
// minus the ▍ rail cell and the 3-cell indent the View gives field content.
func questionFieldWidth(panelWidth int) int {
	return max(panelWidth-4, 30)
}

// buildQuestionOptions constructs a huh option list for an options
// question. Every supplied option carries a generated identity
// (questionOptionValue) rather than its label, and the appended
// custom-answer row carries questionOtherSentinel, so the two namespaces can
// never collide — even when a supplied label is "other", "custom" or
// "option:0". The custom-answer input is revealed by that input group's
// WithHideFunc when this row is picked.
// When an option carries a description, it is folded into the rendered
// key (huh.Option has no description field); the identity is unaffected, so
// recorded answers never carry the description.
func buildQuestionOptions(in []acpwire.QuestionOption) []huh.Option[string] {
	keys := questionOptionKeys(in)
	opts := make([]huh.Option[string], 0, len(in)+1)
	for i := range in {
		opts = append(opts, huh.NewOption(keys[i], questionOptionValue(i)))
	}
	opts = append(opts, huh.NewOption(questionCustomLabel, questionOtherSentinel))
	return opts
}

// questionOptionKeys renders the visible key of every supplied option,
// disambiguating rows that would render the same text. huh's multi-select
// tracks selections by displayed key, so two identical keys would be toggled
// together; a duplicate also hides which row the user picked. Colliding rows
// gain their one-based source position, repeated with a further suffix if
// that is still not unique. The custom-answer row's own key counts as a
// collision because it shares the list.
func questionOptionKeys(in []acpwire.QuestionOption) []string {
	keys := make([]string, len(in))
	for i, o := range in {
		keys[i] = o.Label
		if o.Description != "" {
			keys[i] = o.Label + " — " + o.Description
		}
	}
	for pass := 0; pass <= len(keys); pass++ {
		counts := make(map[string]int, len(keys)+1)
		counts[questionCustomLabel]++
		for _, k := range keys {
			counts[k]++
		}
		collided := false
		for i, k := range keys {
			if counts[k] > 1 {
				keys[i] = k + " (option " + strconv.Itoa(i+1) + ")"
				collided = true
			}
		}
		if !collided {
			break
		}
	}
	return keys
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
		qm.skipRemaining()
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
	for i := range qm.q.Questions {
		qm.finalizeAnswer(i)
	}
}

// finalizeAnswer encodes question i's bound value into qm.answers[i],
// preserving the recorded answer shape: a listed option contributes its
// original label, the custom row its trimmed text, and a free-text question
// its trimmed input. A blank or invalid value leaves whatever
// qm.answers[i].Answer already holds — the unanswered sentinel on a skip —
// rather than inventing an answer.
func (qm *questionModel) finalizeAnswer(i int) {
	qst := qm.q.Questions[i]
	hasOptions := len(qst.Options) > 0
	switch {
	case hasOptions && qst.Multi:
		if qm.multis[i] != nil && len(*qm.multis[i]) > 0 {
			picks := make([]string, 0, len(*qm.multis[i]))
			for _, p := range *qm.multis[i] {
				if p == questionOtherSentinel {
					if qm.others[i] != nil {
						if custom := strings.TrimSpace(*qm.others[i]); custom != "" {
							picks = append(picks, custom)
						}
					}
					continue
				}
				// Only a generated, in-range identity names a supplied
				// option; anything else is dropped rather than recorded
				// as if the user had typed it.
				if label, ok := questionOptionLabel(qst.Options, p); ok {
					picks = append(picks, label)
				}
			}
			if len(picks) > 0 {
				qm.answers[i].Answer = strings.Join(picks, ", ")
			}
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
			} else if label, ok := questionOptionLabel(qst.Options, pick); ok {
				qm.answers[i].Answer = label
			}
		}
	default:
		if qm.inputs[i] != nil {
			if text := strings.TrimSpace(*qm.inputs[i]); text != "" {
				qm.answers[i].Answer = text
			}
		}
	}
}

// focusedQuestionIndex reports which question the form is asking now, using
// the fields mapping. It returns false when no focused field can be mapped,
// which the skip path treats as "nothing is complete".
func (qm *questionModel) focusedQuestionIndex() (int, bool) {
	if qm.form == nil {
		return 0, false
	}
	focused := qm.form.GetFocusedField()
	if focused == nil {
		return 0, false
	}
	for i, fields := range qm.fields {
		if focusedFieldFor(focused, fields) != nil {
			return i, true
		}
	}
	return 0, false
}

// skipRemaining implements Escape: questions the form has already advanced
// past keep their submitted answers, and the focused question and everything
// after it are recorded as unanswered. Only the prefix is finalised because
// huh writes a field's bound value while the user navigates and types, before
// submission — copying the focused question's live value would answer with an
// untouched default or half-typed text.
func (qm *questionModel) skipRemaining() {
	qm.answers = unansweredAnswers(qm.q.Questions)
	if idx, ok := qm.focusedQuestionIndex(); ok {
		for i := 0; i < idx; i++ {
			qm.finalizeAnswer(i)
		}
	}
	qm.done = true
	qm.aborted = true
}

// renderQuestionText renders the question body as markdown wrapped to
// width, falling back to plain wrapped text when glamour is unavailable.
func renderQuestionText(text string, width int) string {
	if out, ok := renderMarkdown(text, width); ok {
		return strings.Trim(out, "\n")
	}
	return ansi.Wrap(text, width, WrapBreakpoints)
}

func (qm *questionModel) View() string {
	if qm.form == nil {
		return ""
	}
	gutter := gutterPrefix("?", violetColor)
	indent := strings.Repeat(" ", 3)
	// contentWidth leaves room for the 3-cell gutter.
	contentWidth := max(qm.width-3, 1)
	focused := qm.form.GetFocusedField()

	var b strings.Builder
	for i, qs := range qm.q.Questions {
		if ans := qm.answers[i].Answer; ans != "" && ans != acpwire.AnswerUnanswered {
			for j, line := range strings.Split(ansi.Wrap(qs.Question+" · "+ans, contentWidth, WrapBreakpoints), "\n") {
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
	// No rail: the console draws the gutter around this section and its
	// header carries the keys.
	return b.String()
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

func (qm *questionModel) Answers() []acpwire.Answer { return qm.answers }
func (qm *questionModel) Aborted() bool             { return qm.aborted }
func (qm *questionModel) IsDone() bool              { return qm.done }
