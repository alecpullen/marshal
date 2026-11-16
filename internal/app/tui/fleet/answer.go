package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/textfield"
	"marshal/web/bridge/contract"
)

// actionTimeout bounds one action's call to the fleet (decision 2).
const actionTimeout = 30 * time.Second

// statusFor is how long a status line's message lasts.
const statusFor = 4 * time.Second

// questionFieldWidth is the typed answer's field width, in cells: wide enough
// for a sentence on the fleet screen's right panel, narrow enough that its
// prompt still fits beside the label.
const questionFieldWidth = 40

// setStatus puts text on the footer's status line, for statusFor on the
// model's clock.
func (m *Model) setStatus(text string) {
	m.status = statusLine{text: text, until: m.now().Add(statusFor)}
}

// statusText is the status line's message, or "" once it has expired.
func (m Model) statusText() string {
	if m.status.text == "" || !m.now().Before(m.status.until) {
		return ""
	}
	return m.status.text
}

// refusedWhileDown reports whether the connection is anything but connected:
// when it is, every action is refused with the status line and calls nothing
// (decision 9).
func (m *Model) refusedWhileDown() bool {
	if m.state.Conn == acpclient.FleetConnected {
		return false
	}
	m.setStatus("not connected to " + m.state.Server)
	return true
}

// actionDone applies one action's answer (decision 3): a success clears the
// status line and reports its warning, an answer settled elsewhere says so in
// Events alone, and any other failure shows its sentence on the status line
// and in Events.
func (m *Model) actionDone(msg actionDoneMsg) {
	if m.staleAction(msg.seq) {
		// A newer action was dispatched since this one: its answer describes
		// the screen, so this one must not clear the newer message or move
		// the selection. A failure still reaches Events.
		if msg.err != nil && !errors.Is(msg.err, acpclient.ErrSettled) {
			m.addEvent(msg.what + " " + msg.name + " failed: " + msg.err.Error())
		}
		return
	}
	switch {
	case msg.err == nil:
		m.status = statusLine{}
		if msg.warning != "" {
			m.addEvent(msg.warning)
		}
		if msg.selectID != "" {
			m.selectRow(msg.selectID)
		}
	case errors.Is(msg.err, acpclient.ErrSettled):
		m.addEvent(msg.name + ": already answered")
	default:
		text := msg.what + " " + msg.name + " failed: " + msg.err.Error()
		m.setStatus(text)
		m.addEvent(text)
	}
}

// selectRow selects id, and moves the cursor onto it when the row is there
// already.
func (m *Model) selectRow(id string) {
	m.selected = id
	if _, rows := m.agentLines(); len(rows) > 0 {
		if idx, ok := rowIndex(rows, id); ok {
			m.agentCursor = idx
		}
	}
}

// openSelected opens the selected agent's pending request: an approval inline
// (decision 4), or a question in its modal (decision 5). An agent that is not
// parked on one opens nothing.
func (m Model) openSelected() (tea.Model, tea.Cmd) {
	if m.refusedWhileDown() {
		return m, nil
	}
	a, ok := m.pendingAgent()
	if !ok {
		return m, nil
	}
	switch a.Pending.Kind {
	case contract.PendingApproval:
		m.answer = a.ID
		// The request's prompt is drawn by the Activity tab.
		m.tab = 1
	case contract.PendingQuestion:
		m.modal = newQuestionModal(a)
	}
	return m, nil
}

// pendingAgent is the selected agent when it is parked on a request.
func (m Model) pendingAgent() (contract.AgentStatus, bool) {
	row, ok := m.selectedRow()
	if !ok || row.session {
		return contract.AgentStatus{}, false
	}
	a, ok := m.agentByID(row.id)
	if !ok || a.Pending == nil {
		return contract.AgentStatus{}, false
	}
	return a, true
}

// answerKey applies one key while an approval is open inline: y and n answer
// it and esc closes it (decision 4). Any other key falls through to the
// screen, so the cursor and the panels keep working.
func (m Model) answerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "y":
		return m.decide(true)
	case "n":
		return m.decide(false)
	case "esc":
		m.answer = ""
		return m, nil, true
	}
	return m, nil, false
}

// decide answers the open approval with approved. A typed access request is
// shown but not answered here: y and n are ignored, and the prompt stays open.
func (m Model) decide(approved bool) (tea.Model, tea.Cmd, bool) {
	a, ok := m.agentByID(m.answer)
	if !ok || a.Pending == nil {
		// The request went away: there is nothing left to answer.
		m.answer = ""
		return m, nil, true
	}
	if accessOnly(a.Pending) {
		return m, nil, true
	}
	if m.refusedWhileDown() {
		// The prompt stays open: a refusal is not an answer.
		return m, nil, true
	}
	m.answer = ""
	return m, m.answerPermissionCmd(a, contract.Decision{Approved: approved}), true
}

// answerPermissionCmd answers a pending approval in a command, bounded by
// actionTimeout.
func (m Model) answerPermissionCmd(a contract.AgentStatus, d contract.Decision) tea.Cmd {
	f, id, name := m.fleet, a.Pending.ID, agentName(a)
	seq := m.nextActionSeq()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		return actionDoneMsg{what: "answer", name: name, err: f.AnswerPermission(ctx, id, d), seq: seq}
	}
}

// answerQuestionCmd answers a pending question in a command, bounded by
// actionTimeout.
func (m Model) answerQuestionCmd(name, id string, a contract.Answers) tea.Cmd {
	f := m.fleet
	seq := m.nextActionSeq()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		return actionDoneMsg{what: "answer", name: name, err: f.AnswerQuestion(ctx, id, a), seq: seq}
	}
}

// approvalParams is the part of an approval's payload the prompt reads. Every
// field is optional: a payload of another shape decodes to the zero value.
type approvalParams struct {
	ToolName   string          `json:"toolName"`
	Command    string          `json:"command"`
	Risk       string          `json:"risk"`
	Reason     string          `json:"reason"`
	ModeAccess json.RawMessage `json:"modeAccess"`
}

// approvalOf reads a pending request's approval payload, verbatim.
func approvalOf(p *contract.PendingRequest) approvalParams {
	var params approvalParams
	if p == nil || len(p.Params) == 0 {
		return approvalParams{}
	}
	if err := json.Unmarshal(p.Params, &params); err != nil {
		return approvalParams{}
	}
	return params
}

// accessOnly reports whether an approval is a typed access request, which the
// fleet screen shows but does not answer (decision 4's non-goal).
func accessOnly(p *contract.PendingRequest) bool {
	params := approvalOf(p)
	return len(params.ModeAccess) > 0 && string(params.ModeAccess) != "null"
}

// questionItem is one question of a question request's payload.
type questionItem struct {
	text    string
	options []questionOption
	multi   bool
}

// questionOption is one option: its label is what an answer carries.
type questionOption struct {
	label string
	desc  string
}

// parseQuestions reads a question request's questions. A payload of another
// shape gives none, and the modal says the question could not be read.
func parseQuestions(p *contract.PendingRequest) []questionItem {
	if p == nil || len(p.Params) == 0 {
		return nil
	}
	var params struct {
		Questions []struct {
			Question string            `json:"question"`
			Options  []json.RawMessage `json:"options"`
			Multi    bool              `json:"multi"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(p.Params, &params); err != nil {
		return nil
	}
	out := make([]questionItem, 0, len(params.Questions))
	for _, q := range params.Questions {
		item := questionItem{text: q.Question, multi: q.Multi}
		for _, raw := range q.Options {
			item.options = append(item.options, parseOption(raw))
		}
		out = append(out, item)
	}
	return out
}

// parseOption reads one option, which is an object carrying its label or, in
// the legacy form, the label alone.
func parseOption(raw json.RawMessage) questionOption {
	var label string
	if err := json.Unmarshal(raw, &label); err == nil {
		return questionOption{label: label}
	}
	var opt struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &opt); err != nil {
		return questionOption{}
	}
	return questionOption{label: opt.Label, desc: opt.Description}
}

// questionModal is a question request's modal (decision 5): the questions in
// order, each with its options, or a text field when it has none.
type questionModal struct {
	agent string // the agent the question belongs to
	id    string // the request to answer
	qs    []questionItem
	// at is the question being answered, and cursor the option under the
	// cursor within it.
	at     int
	cursor int
	// toggled is the current question's toggled options, by index, and
	// answers the answers given so far, one per question answered.
	toggled map[int]bool
	answers []contract.Answer
	// field is the typed answer of a question without options.
	field textfield.Model
}

// newQuestionModal builds the modal of the selected agent's question.
func newQuestionModal(a contract.AgentStatus) modal {
	q := &questionModal{agent: a.ID, field: textfield.New()}
	q.field.Prompt = ""
	q.field.SetWidth(questionFieldWidth)
	if a.Pending != nil {
		q.id = a.Pending.ID
		q.qs = parseQuestions(a.Pending)
	}
	q.focus()
	return q
}

// cur is the question being answered, and whether there is one.
func (q *questionModal) cur() (questionItem, bool) {
	if q.at < 0 || q.at >= len(q.qs) {
		return questionItem{}, false
	}
	return q.qs[q.at], true
}

// textual reports whether the question being answered takes a typed answer.
func (q *questionModal) textual() bool {
	cur, ok := q.cur()
	return ok && len(cur.options) == 0
}

// focus points the text field at the question under the cursor: focused when
// it takes a typed answer, blurred otherwise.
func (q *questionModal) focus() {
	if q.textual() {
		q.field.Focus()
		return
	}
	q.field.Blur()
}

// update applies one key to the modal. esc closes it without answering, enter
// picks the option under the cursor or submits after the last question, j and
// k move the cursor, space toggles an option of a multi question, and d
// declines the whole request. A question without options hands every other key
// to its text field, d included, so a typed answer can carry any letter.
func (q *questionModal) update(m Model, msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if msg.Code == tea.KeyEsc {
		m.modal = nil
		return m, nil
	}
	cur, ok := q.cur()
	if !ok {
		return m, nil
	}
	if len(cur.options) == 0 {
		if msg.Code == tea.KeyEnter {
			return q.pick(m)
		}
		field, cmd := q.field.Update(msg)
		q.field = field
		return m, cmd
	}
	switch {
	case msg.Code == tea.KeyEnter:
		return q.pick(m)
	case msg.Code == tea.KeyDown || msg.String() == "j":
		q.cursor = clamp(q.cursor+1, 0, len(cur.options)-1)
	case msg.Code == tea.KeyUp || msg.String() == "k":
		q.cursor = clamp(q.cursor-1, 0, len(cur.options)-1)
	case msg.Code == tea.KeySpace || msg.Text == " ":
		if cur.multi {
			q.toggle(q.cursor)
		}
	case msg.String() == "d":
		return q.answer(m, contract.Answers{Declined: true})
	}
	return m, nil
}

// pick records the question's answer and moves to the next one, or submits
// after the last.
func (q *questionModal) pick(m Model) (Model, tea.Cmd) {
	cur, ok := q.cur()
	if !ok {
		return m, nil
	}
	q.answers = append(q.answers, contract.Answer{Question: cur.text, Answer: q.chosen(cur)})
	if q.at+1 < len(q.qs) {
		q.at++
		q.cursor = 0
		q.toggled = nil
		q.field.SetValue("")
		q.focus()
		return m, nil
	}
	return q.answer(m, contract.Answers{Answers: q.answers})
}

// chosen is the answer to one question: the typed text, the toggled labels in
// their order, or the option under the cursor.
func (q *questionModal) chosen(cur questionItem) string {
	switch {
	case len(cur.options) == 0:
		return strings.TrimSpace(q.field.Value())
	case cur.multi:
		labels := make([]string, 0, len(cur.options))
		for i, opt := range cur.options {
			if q.toggled[i] {
				labels = append(labels, opt.label)
			}
		}
		return strings.Join(labels, ", ")
	default:
		return cur.options[clamp(q.cursor, 0, len(cur.options)-1)].label
	}
}

// toggle flips one option of a multi question.
func (q *questionModal) toggle(i int) {
	if q.toggled == nil {
		q.toggled = map[int]bool{}
	}
	if q.toggled[i] {
		delete(q.toggled, i)
		return
	}
	q.toggled[i] = true
}

// answer sends the request's answers in a command and closes the modal.
func (q *questionModal) answer(m Model, a contract.Answers) (Model, tea.Cmd) {
	if m.refusedWhileDown() {
		// The modal stays open with its answers: a refusal is not a reason to
		// throw away what the user typed.
		return m, nil
	}
	m.modal = nil
	name := shortID(q.agent)
	if agent, ok := m.agentByID(q.agent); ok {
		name = agentName(agent)
	}
	return m, m.answerQuestionCmd(name, q.id, a)
}

// view draws the modal over the right panel: the question being answered, its
// options with the cursor, or the field that takes a typed answer.
func (q *questionModal) view(m Model, width, height int) string {
	inner := max(width-4, 1)
	title := "Question"
	if n := len(q.qs); n > 1 {
		title = fmt.Sprintf("Question %d/%d", q.at+1, n)
	}
	cur, ok := q.cur()
	if !ok {
		return m.frame(title, "esc close", []string{m.plain(cut("the question could not be read", inner))}, width, height, true)
	}
	// The hints name the keys that do something for this question: a
	// question with no options takes d as text, so it does not decline.
	hints := "enter answer · d decline · esc close"
	if len(cur.options) == 0 {
		hints = "enter answer · esc close"
	} else if cur.multi {
		hints = "space toggle · enter answer · d decline · esc close"
	}
	body := []string{m.plain(cut(cur.text, inner)), ""}
	if len(cur.options) == 0 {
		// One cell goes to the input's cursor past its width.
		q.field.SetWidth(clamp(inner-len("answer: ")-1, 8, questionFieldWidth))
		body = append(body, m.plain("answer: ")+q.field.View())
	} else {
		for i := range cur.options {
			body = append(body, q.optionLine(m, i, inner))
		}
	}
	return m.frame(title, hints, body, width, height, true)
}

// optionLine draws one option: its marker, its label and its description. The
// option under the cursor carries the selection band.
func (q *questionModal) optionLine(m Model, i, inner int) string {
	cur, _ := q.cur()
	opt := cur.options[i]
	mark := "( )"
	switch {
	case cur.multi && q.toggled[i]:
		mark = "[x]"
	case cur.multi:
		mark = "[ ]"
	case i == q.cursor:
		mark = "(•)"
	}
	text := cut(mark+" "+opt.label, inner-2)
	if opt.desc != "" {
		text = cut(text+"  "+opt.desc, inner-2)
	}
	if i == q.cursor {
		return "  " + chrome.SelectionStyle().Render(text)
	}
	return "  " + m.plain(text)
}
