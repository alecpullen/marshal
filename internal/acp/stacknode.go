package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// StackNodeParams is the session/stack_node request body.
type StackNodeParams struct {
	SessionID  string `json:"sessionId"`
	NodeID     string `json:"nodeId"`
	SubagentID int64  `json:"subagentId,omitempty"`
}

// executionDetail projects a stored execution record onto the detail
// transport. It returns nil for a record that carries nothing at all, so an
// empty payload is omitted rather than sent as an object a client has to
// special-case.
//
// The free-text fields travel through the same scrubber the wire projection
// uses for a card's command: they are display text (a working directory, a
// stop cause an actor supplied) and a control sequence in one of them would
// otherwise reach a client's renderer verbatim.
func executionDetail(ex registry.CommandExecution) *acpwire.ExecutionDetail {
	if ex == (registry.CommandExecution{}) {
		return nil
	}
	return &acpwire.ExecutionDetail{
		Version:         ex.Version,
		ExecutionID:     ex.ExecutionID,
		DisplayID:       ex.DisplayID,
		ConversationID:  ex.ConversationID,
		Actor:           ex.Actor,
		TurnID:          ex.TurnID,
		StepID:          ex.StepID,
		ToolCallID:      ex.ToolCallID,
		Cwd:             viewmodel.ScrubText(ex.Cwd),
		Generation:      ex.Generation,
		PID:             ex.PID,
		State:           ex.State,
		Outcome:         ex.Outcome,
		Attachment:      string(ex.Attachment),
		Wait:            ex.Wait,
		DetachReason:    viewmodel.ScrubText(ex.DetachReason),
		DetachedAt:      unixMs(ex.DetachedAt),
		StopCause:       viewmodel.ScrubText(ex.StopCause),
		StopRequestedAt: unixMs(ex.StopRequestedAt),
		Exit:            copyExitCode(ex.ExitCode),
		Truncated:       ex.Truncated,
		StartedAt:       unixMs(ex.StartedAt),
		EndedAt:         unixMs(ex.EndedAt),
	}
}

// copyExitCode copies an exit-code pointer so the detail never aliases a
// caller's memory.
func copyExitCode(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func indentJSON(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") == nil {
		return buf.String()
	}
	return string(raw)
}

func callDetail(ev registry.AuditEvent) acpwire.CallDetail {
	c := acpwire.CallDetail{
		CallID: ev.ToolCallID, StepID: ev.StepID, ToolName: ev.ToolName,
		Args: indentJSON(ev.Args), Error: ev.Error, ExitCode: ev.CommandExitCode,
		Model: ev.Model, FinishReason: ev.FinishReason, Rewritten: ev.Rewritten,
	}
	// The hooks trail travels as registry.HookMetadata's own JSON: its
	// failed_open key is not a wire-field name, so it stays raw bytes.
	if len(ev.Hooks) > 0 {
		c.Hooks, _ = json.Marshal(ev.Hooks)
	}
	if ev.Rewritten {
		c.OriginalArgs = indentJSON(ev.OriginalArgs)
	}
	output := ev.ResultContent
	if output == "" {
		output = ev.ResultSummary
	}
	if isDiffToolName(ev.ToolName) {
		c.Diff = ev.ResultContent
	} else {
		c.Output = output
	}
	if raw, err := json.Marshal(ev.Sandbox); err == nil && (ev.Sandbox.Enabled || ev.Sandbox.Backend != "") {
		c.Sandbox = raw
	}
	for _, s := range ev.Symbols {
		c.Symbols = append(c.Symbols, acpwire.SymbolDetail{File: s.File, Name: s.Name, Kind: s.Kind})
	}
	if ev.Notice != nil {
		c.Notice = &acpwire.NoticeDetail{Kind: ev.Notice.Kind, Text: ev.Notice.Text}
		// The structured execution record rides in the notice's Data, but it is
		// not a notice: it is the machine-readable evidence a client explains a
		// wait, a handoff or a stop with. Exposing it as its own typed field
		// keeps that data out of the coaching text and keeps the wire shape
		// additive for clients that ignore it.
		if ex, ok := ev.Notice.Execution(); ok {
			c.Execution = executionDetail(ex)
		}
	}
	return c
}

func isDiffToolName(name string) bool {
	return name == "file.write_patch" || name == "file.edit" || name == "patch.apply"
}

func activeCallDetail(a *session.ActiveToolCall) acpwire.CallDetail {
	return acpwire.CallDetail{CallID: a.ToolCallID, StepID: int64(a.StepID), ToolName: a.Name, Args: indentJSON([]byte(a.Args)), Output: a.Output}
}

func unixMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func todoDetail(st *session.State, id string) *acpwire.TodoDetail {
	if id == "" {
		return nil
	}
	for _, td := range st.Todos() {
		if td.ID == id {
			return &acpwire.TodoDetail{ID: td.ID, Content: td.Content, Status: td.Status,
				StartedAt: unixMs(td.StartedAt), CompletedAt: unixMs(td.CompletedAt)}
		}
	}
	return nil
}

func keysOf(ids []viewmodel.NodeID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.Key)
	}
	return out
}

// treeFor builds the full view-model tree of a session or subagent.
func (m *TurnManager) treeFor(sessionID string, subagentID int64) (*TurnRuntime, *session.State, []*viewmodel.Node, error) {
	rt, ok := m.lookup(sessionID)
	if !ok || rt.State == nil {
		return nil, nil, nil, serverErrorf("unknown session: %s", sessionID)
	}
	src, drilled, err := m.stackSource(rt, subagentID)
	if err != nil {
		return nil, nil, nil, err
	}
	busy := m.stackTurnBusy(sessionID)
	if drilled {
		if v, ok := rt.State.Subagent(subagentID); ok {
			busy = v.Status == session.SubagentRunning
		}
	}
	return rt, src, viewmodel.Build(viewmodel.SnapshotOf(src, busy, time.Now())), nil
}

// StackNode handles session/stack_node: one node's wire form plus the detail
// the snapshot caps or omits.
func (m *TurnManager) StackNode(ctx context.Context, params json.RawMessage) (any, error) {
	var p StackNodeParams
	if err := decodeParams(params, &p, "session/stack_node"); err != nil {
		return nil, err
	}
	if p.SessionID == "" || p.NodeID == "" {
		return nil, invalidParamsError("session/stack_node requires sessionId and nodeId")
	}
	_, state, tree, err := m.treeFor(p.SessionID, p.SubagentID)
	if err != nil {
		return nil, err
	}
	n, parent := viewmodel.Find(tree, p.NodeID)
	if n == nil {
		return nil, invalidParamsError("unknown node: %s", p.NodeID)
	}
	var detail acpwire.NodeDetail
	switch {
	case n.Active != nil:
		detail.Calls = append(detail.Calls, activeCallDetail(n.Active))
	case len(n.Tools) > 0:
		for _, ev := range n.Tools {
			detail.Calls = append(detail.Calls, callDetail(ev))
		}
	case n.Kind == viewmodel.KindStep && n.Step != nil:
		for _, msg := range n.Step.Narration {
			detail.Narration = append(detail.Narration, msg.Content)
		}
		for _, t := range n.Step.Thinking {
			detail.Thinking = append(detail.Thinking, acpwire.ThoughtDetail{Text: t.Text, DurationMs: t.Duration.Milliseconds()})
		}
		detail.Todo = todoDetail(state, n.Step.TodoID)
	}
	if rel, ok := viewmodel.ResolveBeats(viewmodel.Relations(state.Transcript()), tree)[n.ID]; ok {
		detail.Relations = &acpwire.RelationDetail{CausedBy: keysOf(rel.CausedBy), FixedBy: keysOf(rel.FixedBy)}
	}
	node := viewmodel.Project([]*viewmodel.Node{n}, state.Workspace().ActiveRoot).Nodes[0]
	if parent != nil {
		node.Parent = parent.ID.Key
	}
	return acpwire.StackNodeResult{Detail: detail, Node: node}, nil
}
