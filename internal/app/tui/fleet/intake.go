package fleet

// intake.go is decision 8's intake prompt: the two lines an intake item in
// Needs you opens, and the two answers it takes.

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"marshal/web/bridge/contract"
)

// openIntake opens the prompt of the intake item under the Needs you cursor
// (decision 8). An agent item, an empty queue or another panel's focus opens
// nothing. While the connection is down it refuses, as every other action does
// (decision 9).
func (m Model) openIntake() (tea.Model, tea.Cmd) {
	if m.focus != focusNeedsYou {
		return m, nil
	}
	item, ok := m.intakeItem()
	if !ok {
		return m, nil
	}
	if m.refusedWhileDown() {
		return m, nil
	}
	m.modal = newIntakePrompt(item.Submission)
	return m, nil
}

// intakeItem is the intake item under the Needs you cursor, and whether the
// cursor is on one.
func (m Model) intakeItem() (contract.InboxItem, bool) {
	queue := m.state.Inbox.Queue
	if m.needsCursor < 0 || m.needsCursor >= len(queue) {
		return contract.InboxItem{}, false
	}
	item := queue[m.needsCursor]
	if item.Kind != contract.InboxItemIntake || item.Submission == nil {
		return contract.InboxItem{}, false
	}
	return item, true
}

// intakePrompt is decision 8's two-line prompt: whose submission it is and
// what it is titled, and the keys that settle it.
type intakePrompt struct {
	id    string // the submission to settle
	title string // what the operator decides about
}

// newIntakePrompt builds the prompt of one submission.
func newIntakePrompt(sub *contract.PendingSubmission) *intakePrompt {
	p := &intakePrompt{}
	if sub != nil {
		p.id, p.title = sub.ID, sub.Title
	}
	return p
}

// update applies one key: y approves the submission, n denies it, and esc
// closes the prompt without answering.
func (p *intakePrompt) update(m Model, msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		return p.answer(m, true)
	case "n":
		return p.answer(m, false)
	case "esc":
		m.modal = nil
	}
	return m, nil
}

// answer settles the submission in a command and closes the prompt.
func (p *intakePrompt) answer(m Model, approve bool) (Model, tea.Cmd) {
	if m.refusedWhileDown() {
		return m, nil
	}
	m.modal = nil
	return m, m.intakeCmd(p, approve)
}

// intakeCmd approves or denies the submission, bounded by actionTimeout. An
// approval selects the agent it spawns once the next state names it (decision
// 8).
func (m Model) intakeCmd(p *intakePrompt, approve bool) tea.Cmd {
	f, id, title := m.fleet, p.id, p.title
	seq := m.nextActionSeq()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		if !approve {
			return actionDoneMsg{what: "deny", name: title, err: f.DenyIntake(ctx, id), seq: seq}
		}
		res, err := f.ApproveIntake(ctx, id)
		return actionDoneMsg{what: "approve", name: title, err: err, selectID: res.AgentID, seq: seq}
	}
}

// view draws the prompt over the right panel, as the question modal is drawn:
// the title, and the keys under it — two lines of prompt.
func (p *intakePrompt) view(m Model, width, height int) string {
	inner := max(width-4, 1)
	body := []string{
		m.plain(cut("approve "+p.title+"?", inner)),
		m.plain(cut("[y]es [n]o, deny", inner)),
	}
	return m.frame("Intake", "y approve · n deny · esc close", body, width, height, true)
}
