package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// maxCallersShown caps how many references a row names before collapsing
// the rest into a count.
const maxCallersShown = 3

// callersMsg carries a completed reference lookup back to the model. A nil
// callers is an answer, not a missing one: no callers line, and no second ask.
type callersMsg struct {
	key     nodeID
	callers []string // nil: no callers line, and no second ask
}

// callerQueryCmds returns a batched command for every call row the wire flags
// for a callers lookup that has not been asked yet.
//
// This is deliberately not called from refreshViewport: that runs on every
// spinner tick and returns no commands. Callers come from the tick and
// turn-finished handlers instead, so a lookup costs at most one tick of
// latency and never sits on the render path.
//
// While the view is drilled into a subagent the index holds the child's
// transcript, so nothing is asked: a row that settles during a drill-in is
// asked on the first tick after the view returns.
func (m *Model) callerQueryCmds() tea.Cmd {
	if m.sess == nil || len(m.viewStack) > 0 {
		return nil
	}
	if m.callersAsked == nil {
		m.callersAsked = map[nodeID]bool{}
	}
	var cmds []tea.Cmd
	for id, w := range m.wire {
		if w.Tool == nil || !w.Tool.Callers {
			continue
		}
		key := nodeID{Kind: acpwire.KindTool, Key: id}
		if m.callersAsked[key] {
			continue
		}
		m.callersAsked[key] = true
		cmds = append(cmds, queryCallersCmd(m.lspCtx, m.sess, key))
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// queryCallersCmd asks the session what references the symbol the row changed.
// An error is an empty answer: the row renders without a callers line.
func queryCallersCmd(ctx context.Context, sess acpclient.Session, key nodeID) tea.Cmd {
	return func() tea.Msg {
		refs, err := sess.Callers(ctx, key.Key)
		if err != nil {
			refs = nil
		}
		return callersMsg{key: key, callers: refs}
	}
}

// handleCallers stores a completed lookup. A nil answer is stored so the key
// is present and the query is not retried.
func (m Model) handleCallers(msg callersMsg) (Model, tea.Cmd) {
	if m.callers == nil {
		m.callers = map[nodeID][]string{}
	}
	m.callers[msg.key] = msg.callers
	m.invalidateTranscript()
	m.refreshViewport()
	return m, nil
}
