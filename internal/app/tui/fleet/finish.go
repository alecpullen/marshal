package fleet

// finish.go is the finish flow (decisions 2 to 8): v runs the selected agent's
// gate from the Info tab, f opens the modal on an idle agent, and the modal
// merges, pushes or discards its work and says where the work went.

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/textfield"
	"marshal/web/bridge/contract"
)

// infoTab is the right panel's Info tab, as plan 3 reserved 2 for the Diff tab.
// v runs the gate only there (decision 2).
const infoTab = 3

// finishFieldWidth is the message, the reason and the typed name's width in
// cells, as spawnFieldWidth is the spawn form's.
const finishFieldWidth = 40

// blockedOutputLines is how much of a failed gate's output a blocked push shows
// (decision 7): its tail, which is where the failure is stated.
const blockedOutputLines = 20

// blockedReserved is how many rows the blocked step keeps below the gate's
// output: the blank line and the reason field, and one for its refusal.
const blockedReserved = 3

// patchText is decision 5's sentence for a read-only git agent: its work leaves
// as a patch, not as a push.
const patchText = "read-only: this agent's work leaves as a patch to apply elsewhere"

// emptyReasonText is the reason field's refusal of an empty override: the
// bridge's own sentence for it (web/bridge/exit.go).
const emptyReasonText = "a gate override requires a reason"

// finishStep is where the finish modal is (decision 4).
type finishStep int

const (
	finishMessage finishStep = iota // the commit message, then ⏎ ships
	finishDiscard                   // the agent's name, typed to confirm a discard
	finishBlocked                   // a blocked push: the gate, then the override's reason
	finishResult                    // the outcome: any key closes it
)

// finishModal is the finish flow (decision 4). It implements plan 5a's modal.
type finishModal struct {
	agent string // the agent being finished
	name  string // its name, as the screen writes it
	dest  string // destination(a): "merge", "push" or "patch"
	step  finishStep
	// message is the commit message, pre-filled from the draft; reason is the
	// blocked push's override reason; typed is the discard's confirmation.
	message, reason, typed textfield.Model
	// gate is the blocked push's failed gate, and result its outcome's lines.
	gate   *contract.GateResult
	result []string
	// refusal is the sentence under the step's fields: a merge's refusal, an
	// empty override reason, or a name typed wrong.
	refusal string
	// busy is true while one of the modal's calls is out, so one step ships
	// once.
	busy bool
}

// newFinishModal builds the modal for a. The message starts empty: the draft
// read fills it when its answer lands (decision 4).
func newFinishModal(a contract.AgentStatus) *finishModal {
	f := &finishModal{
		agent:   a.ID,
		name:    agentName(a),
		dest:    destination(a),
		message: textfield.New(),
		reason:  textfield.New(),
		typed:   textfield.New(),
	}
	f.message.SetWidth(finishFieldWidth)
	f.message.Placeholder = "the commit message"
	f.reason.SetWidth(finishFieldWidth)
	f.reason.Placeholder = "why this push is safe"
	f.typed.SetWidth(finishFieldWidth)
	f.message.Focus()
	return f
}

// destination is decision 5's rule over a row, the web UI's exitDestination:
// a local agent merges, a read-only git agent leaves a patch, any other git
// agent pushes.
func destination(a contract.AgentStatus) string {
	if a.SourceKind != "git" {
		return "merge"
	}
	if a.ReadOnly {
		return "patch"
	}
	return "push"
}

// mergeRefusal is decision 6's sentence for a refused merge: each reason has
// its own remedy, so a generic "merge failed" would be useless.
func mergeRefusal(r contract.MergeResult) string {
	switch r.Reason {
	case "dirty":
		return "the agent has uncommitted changes: give a commit message and ship again"
	case "project_dirty":
		return "your project checkout has uncommitted changes: commit or stash them, then ship"
	case "target_moved":
		return "the project is no longer on " + nonEmpty(r.Target) +
			", which this agent branched from: switch back to it, then ship"
	case "conflicts":
		return "merging " + nonEmpty(r.Branch) + " into " + nonEmpty(r.Target) +
			" conflicts in " + conflictList(r.Conflicts) +
			" — nothing was changed, resolve it in your editor"
	}
	if r.Reason == "" {
		return "merge refused"
	}
	return "merge refused (" + r.Reason + ")"
}

// conflictList is a refusal's conflicted files, or the generic count when the
// bridge named none.
func conflictList(conflicts []string) string {
	if len(conflicts) == 0 {
		return "one or more files"
	}
	return strings.Join(conflicts, ", ")
}

// addHint appends one key hint to the footer's keys, which the footer groups
// with two spaces, as it separates ⏎ open.
func addHint(keys, hint string) string {
	if keys == "" {
		return hint
	}
	return keys + "  " + hint
}

// openFinish opens the finish modal on the selected agent (decision 3), with the
// commit draft's read armed. Only an idle agent can finish: one that is running
// or waiting is told to cancel its turn first.
func (m Model) openFinish() (tea.Model, tea.Cmd) {
	a, ok := m.selectedAgent()
	if !ok {
		return m, nil
	}
	if !idleAgent(a.Status) {
		m.setStatus("finish needs " + agentName(a) + " idle: cancel its turn first (x)")
		return m, nil
	}
	if m.refusedWhileDown() {
		return m, nil
	}
	m.modal = newFinishModal(a)
	return m, m.finishDraftCmd(a.ID)
}

// selectedAgent is the selected row's agent. A Sessions row, an intake item and
// an empty selection have none.
func (m Model) selectedAgent() (contract.AgentStatus, bool) {
	row, ok := m.selectedRow()
	if !ok || row.session {
		return contract.AgentStatus{}, false
	}
	return m.agentByID(row.id)
}

// idleAgent reports whether an agent can finish: it is neither running a turn
// nor parked on a request (decision 3).
func idleAgent(s contract.AgentState) bool {
	switch s {
	case contract.AgentRunning, contract.AgentAwaitingApproval, contract.AgentAwaitingQuestion:
		return false
	}
	return true
}

// canFinish reports whether f applies to the selection: an idle agent.
func (m Model) canFinish() bool {
	a, ok := m.selectedAgent()
	return ok && idleAgent(a.Status)
}

// verifySelected runs the selected agent's gate (decision 2): the status line
// reads "verifying…" while the call is out, the result shows through the gate
// delta, and a refused call is an action failure.
func (m Model) verifySelected() (tea.Model, tea.Cmd) {
	if m.focus != focusRight || m.tab != infoTab {
		return m, nil
	}
	a, ok := m.selectedAgent()
	if !ok || m.refusedWhileDown() {
		return m, nil
	}
	m.setStatus("verifying…")
	return m, m.verifyCmd(a)
}

// verifyCmd runs the gate in a command, bounded by actionTimeout.
func (m Model) verifyCmd(a contract.AgentStatus) tea.Cmd {
	f, id, name := m.fleet, a.ID, agentName(a)
	seq := m.nextActionSeq()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		_, err := f.Verify(ctx, id)
		return actionDoneMsg{what: "verify", name: name, err: err, seq: seq}
	}
}

// finishDraftMsg is the commit draft's answer (decision 4).
type finishDraftMsg struct {
	agentID string
	draft   contract.CommitDraft
	err     error
}

// finishDoneMsg is one finish action's answer: a merge's, an exit's or a
// discard's (decisions 6 to 8).
type finishDoneMsg struct {
	agentID string
	what    string // the Events line's verb: "merge", "push", "discard"
	name    string
	merge   contract.MergeResult
	exit    contract.ExitResult
	err     error
}

// finishDraftCmd reads the agent's commit draft in a command, bounded by
// actionTimeout.
func (m Model) finishDraftCmd(agentID string) tea.Cmd {
	f := m.fleet
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		draft, err := f.CommitDraft(ctx, agentID)
		return finishDraftMsg{agentID: agentID, draft: draft, err: err}
	}
}

// finishMergeCmd merges the agent's branch into its project's, with message.
func (m Model) finishMergeCmd(a contract.AgentStatus, message string) tea.Cmd {
	f, id, name := m.fleet, a.ID, agentName(a)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		res, err := f.Merge(ctx, id, contract.MergeRequest{CommitMessage: message})
		return finishDoneMsg{agentID: id, what: "merge", name: name, merge: res, err: err}
	}
}

// finishExitCmd commits, verifies and pushes the agent, with override when one
// was given.
func (m Model) finishExitCmd(a contract.AgentStatus, message string, override *contract.GateOverride) tea.Cmd {
	f, id, name := m.fleet, a.ID, agentName(a)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		res, err := f.Exit(ctx, id, contract.ExitRequest{CommitMessage: message, Override: override})
		return finishDoneMsg{agentID: id, what: "push", name: name, exit: res, err: err}
	}
}

// finishDiscardCmd throws the agent's work away.
func (m Model) finishDiscardCmd(a contract.AgentStatus) tea.Cmd {
	f, id, name := m.fleet, a.ID, agentName(a)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		return finishDoneMsg{agentID: id, what: "discard", name: name, err: f.Discard(ctx, id)}
	}
}

// handleFinishDraft applies the draft's answer (decision 4). A read that failed
// or says nothing leaves the message as it is, and words already typed are
// never overwritten by a slow answer.
func (m Model) handleFinishDraft(msg finishDraftMsg) (tea.Model, tea.Cmd) {
	f, ok := m.modal.(*finishModal)
	if !ok || f.agent != msg.agentID || f.step != finishMessage {
		return m, nil
	}
	text := strings.TrimSpace(msg.draft.Message)
	if msg.err != nil || text == "" || f.message.Value() != "" {
		return m, nil
	}
	f.message.SetValue(text)
	f.message.CursorEnd()
	return m, nil
}

// handleFinishDone routes one finish action's answer to the open modal. A
// failure is an action failure, as plan 5a shows them, and an answer that
// arrives after the modal closed has nothing left to draw on.
func (m Model) handleFinishDone(msg finishDoneMsg) (tea.Model, tea.Cmd) {
	f, ok := m.modal.(*finishModal)
	if !ok || f.agent != msg.agentID {
		if msg.err != nil {
			m.actionDone(actionDoneMsg{what: msg.what, name: msg.name, err: msg.err})
		}
		return m, nil
	}
	f.busy = false
	if msg.err != nil {
		m.modal = nil
		m.actionDone(actionDoneMsg{what: msg.what, name: msg.name, err: msg.err})
		return m, nil
	}
	switch msg.what {
	case "merge":
		m = f.merge(m, msg.merge)
	case "push":
		m = f.exit(m, msg.exit)
	case "discard":
		m = f.discarded(m)
	}
	return m, nil
}

// update applies one key to the modal. esc closes it at any step without
// acting, the result step is closed by any key, and otherwise the keys go to
// the step's own fields.
func (f *finishModal) update(m Model, msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if msg.Code == tea.KeyEsc {
		m.modal = nil
		return m, nil
	}
	if f.step == finishResult {
		// Any key closes a result (decision 8).
		m.modal = nil
		return m, nil
	}
	if f.busy {
		// One call is out: a second key would ship twice.
		return m, nil
	}
	switch f.step {
	case finishDiscard:
		if msg.Code == tea.KeyEnter {
			return f.discard(m)
		}
		field, cmd := f.typed.Update(msg)
		f.typed = field
		return m, cmd
	case finishBlocked:
		if msg.Code == tea.KeyEnter {
			return f.override(m)
		}
		field, cmd := f.reason.Update(msg)
		f.reason = field
		return m, cmd
	}
	// The message step: ctrl+d asks for the discard's confirmation, ⏎ ships,
	// and every other key is the message's.
	if msg.String() == "ctrl+d" {
		f.toDiscard()
		return m, nil
	}
	if msg.Code == tea.KeyEnter {
		return f.ship(m)
	}
	field, cmd := f.message.Update(msg)
	f.message = field
	return m, cmd
}

// toDiscard moves to the confirmation: the agent's name must be typed exactly
// for the discard to run (decision 4).
func (f *finishModal) toDiscard() {
	f.step = finishDiscard
	f.refusal = ""
	f.message.Blur()
	f.reason.Blur()
	f.typed.SetValue("")
	f.typed.Focus()
}

// ship sends the work to the destination the row names (decision 5). The
// destination is never a choice: a merge merges, and everything else exits.
func (f *finishModal) ship(m Model) (Model, tea.Cmd) {
	if m.refusedWhileDown() {
		return m, nil
	}
	a, ok := m.agentByID(f.agent)
	if !ok {
		// The agent went away while the modal was open.
		m.modal = nil
		return m, nil
	}
	message := strings.TrimSpace(f.message.Value())
	f.refusal = ""
	f.busy = true
	if f.dest == "merge" {
		return m, m.finishMergeCmd(a, message)
	}
	return m, m.finishExitCmd(a, message, nil)
}

// override exits again with the reason that overrides the failed gate. An empty
// reason is refused in the field, as the bridge would refuse it (decision 7).
func (f *finishModal) override(m Model) (Model, tea.Cmd) {
	reason := strings.TrimSpace(f.reason.Value())
	if reason == "" {
		f.refusal = emptyReasonText
		return m, nil
	}
	if m.refusedWhileDown() {
		return m, nil
	}
	a, ok := m.agentByID(f.agent)
	if !ok {
		m.modal = nil
		return m, nil
	}
	f.refusal = ""
	f.busy = true
	override := &contract.GateOverride{Reason: reason}
	return m, m.finishExitCmd(a, strings.TrimSpace(f.message.Value()), override)
}

// discard throws the agent's work away, once its name is typed exactly.
func (f *finishModal) discard(m Model) (Model, tea.Cmd) {
	if f.typed.Value() != f.name {
		f.refusal = "type " + f.name + " to discard"
		return m, nil
	}
	if m.refusedWhileDown() {
		return m, nil
	}
	a, ok := m.agentByID(f.agent)
	if !ok {
		m.modal = nil
		return m, nil
	}
	f.refusal = ""
	f.busy = true
	return m, m.finishDiscardCmd(a)
}

// merge applies a merge's answer (decisions 6 and 8): a refusal says what to do
// and leaves the modal on the message step, and a merge names the branch and
// the target it landed on.
func (f *finishModal) merge(m Model, res contract.MergeResult) Model {
	if !res.Merged {
		f.step = finishMessage
		f.refusal = mergeRefusal(res)
		event := "merge " + f.name + " refused"
		if res.Reason != "" {
			event += " (" + res.Reason + ")"
		}
		m.addEvent(event)
		return m
	}
	branch, target := res.Branch, res.Target
	if a, ok := m.agentByID(f.agent); ok {
		if branch == "" {
			branch = a.Branch
		}
		if target == "" {
			target = a.TargetBranch
		}
	}
	return f.show(m, "merged "+nonEmpty(branch)+" into "+nonEmpty(target))
}

// exit applies an exit's answer (decisions 5, 7 and 8): a blocked push offers
// the override, and the result names the destination that took the work.
func (f *finishModal) exit(m Model, res contract.ExitResult) Model {
	if res.Blocked {
		f.step = finishBlocked
		f.refusal = ""
		f.gate = res.Verify
		// The keys go to the reason that would override the gate.
		f.message.Blur()
		f.reason.Focus()
		event := "push " + f.name + " blocked"
		if res.Verify != nil && res.Verify.FailedCommand != "" {
			event += ": " + res.Verify.FailedCommand
		}
		m.addEvent(event)
		return m
	}
	switch res.Destination {
	case "patch":
		// A read-only agent's work leaves as a patch, whatever was asked.
		return f.show(m, patchText)
	case "merge":
		target := ""
		if a, ok := m.agentByID(f.agent); ok {
			target = a.TargetBranch
		}
		return f.show(m, "merged "+nonEmpty(res.Branch)+" into "+nonEmpty(target))
	}
	lines := []string{"pushed " + nonEmpty(res.Branch)}
	if res.PRUrl != "" {
		lines = append(lines, "pull request "+res.PRUrl)
	}
	return f.show(m, lines...)
}

// discarded is a discard's answer: the work is gone, so the result says so.
func (f *finishModal) discarded(m Model) Model {
	return f.show(m, "discarded "+f.name+"'s work and its branch")
}

// show makes lines the result and moves the modal to it (decision 8): the
// result names where the work went, and every line is an Events line too.
func (f *finishModal) show(m Model, lines ...string) Model {
	f.step = finishResult
	f.refusal = ""
	f.gate = nil
	f.result = lines
	for _, line := range lines {
		m.addEvent(line)
	}
	return m
}

// view draws the step over the right panel.
func (f *finishModal) view(m Model, width, height int) string {
	inner := max(width-4, 1)
	switch f.step {
	case finishDiscard:
		return f.discardView(m, inner, width, height)
	case finishBlocked:
		return f.blockedView(m, inner, width, height)
	case finishResult:
		return f.resultView(m, inner, width, height)
	}
	return f.messageView(m, inner, width, height)
}

// messageView is the modal's first step: where the work goes, and the message
// that ships it. A refusal sits under the field it can be cleared from.
func (f *finishModal) messageView(m Model, inner, width, height int) string {
	// The text input renders one cell past its width for the cursor.
	f.message.SetWidth(clamp(inner-labelWidth-3, 8, finishFieldWidth))
	body := []string{
		m.kv("agent", f.name),
		m.kv("ship", f.dest),
		m.kv("branch", f.branchText(m)),
		m.plain(padTo("message", labelWidth)) + f.message.View(),
	}
	body = append(body, f.refusalLines(m, inner)...)
	return m.frame("Finish", "⏎ ship · ctrl+d discard · esc close", body, width, height, true)
}

// blockedView is a blocked push (decision 7): the failed command, the tail of
// its output, and the reason that would override the gate.
func (f *finishModal) blockedView(m Model, inner, width, height int) string {
	f.reason.SetWidth(clamp(inner-labelWidth-3, 8, finishFieldWidth))
	body := []string{m.plain("push was blocked")}
	if f.gate != nil {
		if command := strings.TrimSpace(f.gate.FailedCommand); command != "" {
			for _, line := range wrapText("failed command: "+command, inner) {
				body = append(body, m.plain(line))
			}
		}
		output := lastLines(f.gate.Output, blockedOutputLines)
		// The panel keeps the reason field, so a longer output loses its head
		// rather than the field that overrides it.
		if room := max(height-2, 0) - len(body) - blockedReserved; len(output) > room {
			output = output[len(output)-max(room, 0):]
		}
		for _, line := range output {
			body = append(body, m.plain(cut(line, inner)))
		}
	}
	body = append(body, "", m.plain(padTo("reason", labelWidth))+f.reason.View())
	body = append(body, f.refusalLines(m, inner)...)
	return m.frame("Push blocked", "⏎ push anyway · esc close", body, width, height, true)
}

// discardView is the confirmation (decision 4): the agent's name, typed
// exactly, is what makes the discard run.
func (f *finishModal) discardView(m Model, inner, width, height int) string {
	f.typed.SetWidth(clamp(inner-labelWidth-3, 8, finishFieldWidth))
	body := []string{}
	for _, line := range wrapText("discard "+f.name+"'s work and its branch? this cannot be undone", inner) {
		body = append(body, m.plain(line))
	}
	body = append(body, "")
	for _, line := range wrapText("type "+f.name+" to confirm", inner) {
		body = append(body, m.plain(line))
	}
	body = append(body, m.plain(padTo("name", labelWidth))+f.typed.View())
	body = append(body, f.refusalLines(m, inner)...)
	return m.frame("Discard", "⏎ discard · esc close", body, width, height, true)
}

// resultView is the outcome: where the work went, and the key that closes it.
func (f *finishModal) resultView(m Model, inner, width, height int) string {
	body := make([]string, 0, len(f.result))
	for _, line := range f.result {
		for _, wrapped := range wrapText(line, inner) {
			body = append(body, m.plain(wrapped))
		}
	}
	return m.frame("Finish", "any key closes", body, width, height, true)
}

// refusalLines is the modal's refusal, wrapped under the step's fields.
func (f *finishModal) refusalLines(m Model, inner int) []string {
	if f.refusal == "" {
		return nil
	}
	lines := []string{""}
	for _, line := range wrapText(f.refusal, inner) {
		lines = append(lines, m.plain(line))
	}
	return lines
}

// branchText is the agent's branch and its target, or – when the state no
// longer names the agent.
func (f *finishModal) branchText(m Model) string {
	a, ok := m.agentByID(f.agent)
	if !ok {
		return nonEmpty("")
	}
	return branchText(a)
}

// lastLines is at most the last n lines of s, without the trailing newline a
// command's output usually ends on.
func lastLines(s string, n int) []string {
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(s) == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
