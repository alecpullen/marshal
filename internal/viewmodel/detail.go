package viewmodel

import (
	"bytes"
	"encoding/json"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// StackNodeOf finds nodeID in tree, st's built transcript, and returns the
// node's wire form, its parent's ID set, with the detail the projection caps
// or leaves out: what session/stack_node answers. found is false when tree
// has no node with that ID.
func StackNodeOf(st *session.State, tree []*Node, nodeID string) (res acpwire.StackNodeResult, found bool) {
	n, parent := Find(tree, nodeID)
	if n == nil {
		return acpwire.StackNodeResult{}, false
	}
	var detail acpwire.NodeDetail
	switch {
	case n.Active != nil:
		detail.Calls = append(detail.Calls, activeCallDetail(n.Active))
	case len(n.Tools) > 0:
		for _, ev := range n.Tools {
			detail.Calls = append(detail.Calls, callDetail(ev))
		}
		// A call row's step is its first call's: the one that opened the row.
		// It is absent when the call named no step or the state no longer
		// holds the record.
		if id := n.Tools[0].StepID; id != 0 {
			if rec, ok := st.Step(id); ok {
				detail.Step = stepDetailOf(st, rec)
			}
		}
	case n.Kind == KindStep && n.Step != nil:
		for _, msg := range n.Step.Narration {
			detail.Narration = append(detail.Narration, msg.Content)
		}
		for _, t := range n.Step.Thinking {
			detail.Thinking = append(detail.Thinking, acpwire.ThoughtDetail{Text: t.Text, DurationMs: t.Duration.Milliseconds()})
		}
		detail.Todo = todoDetail(st, n.Step.TodoID)
		// A beat's step record travels even when its ID is 0, which is a loose
		// beat: the client shows the actor and the stored todo regardless.
		detail.Step = stepDetailOf(st, n.Step.Step)
	case n.Completion != nil:
		detail.Completion = &acpwire.CompletionDetail{Output: n.Completion.Output, Report: n.Completion.Report,
			Truncated: n.Completion.Truncated}
	case n.Item != nil:
		// A message's reasoning travels as one thought, the way a beat's
		// thoughts do; a run event's body travels whole. A run event with no
		// body sets no detail at all, which reads the same as no answer: the
		// client falls back to the event's wire body.
		if m := n.Item.Message; m != nil && m.Reasoning != "" {
			detail.Thinking = []acpwire.ThoughtDetail{{Text: m.Reasoning, DurationMs: m.ThinkDuration.Milliseconds()}}
		}
		if e := n.Item.RunEvent; e != nil {
			detail.Body = e.Body
		}
	}
	if rel, ok := ResolveBeats(Relations(st.Transcript()), tree)[n.ID]; ok {
		detail.Relations = &acpwire.RelationDetail{CausedBy: nodeKeys(rel.CausedBy), FixedBy: nodeKeys(rel.FixedBy)}
	}
	node := Project([]*Node{n}, st.Workspace().ActiveRoot).Nodes[0]
	if parent != nil {
		node.Parent = parent.ID.Key
	}
	return acpwire.StackNodeResult{Detail: detail, Node: node}, true
}

// stepDetailOf is rec as the wire's StepDetail. A loose beat's record has no
// ID, and no narration to take a "why" line from.
func stepDetailOf(st *session.State, rec session.Step) *acpwire.StepDetail {
	d := &acpwire.StepDetail{
		ID:       int64(rec.ID),
		Owner:    StepOwner(rec),
		Model:    rec.Actor.Model,
		Provider: rec.Actor.Provider,
		TodoID:   rec.TodoID,
	}
	if rec.ID != 0 {
		if h, _ := FirstSentence(st.StepNarration(rec.ID)); h != "" {
			d.Why = StripEmphasis(h)
		}
	}
	return d
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
		Cwd:             ScrubText(ex.Cwd),
		Generation:      ex.Generation,
		PID:             ex.PID,
		State:           ex.State,
		Outcome:         ex.Outcome,
		Attachment:      string(ex.Attachment),
		Wait:            ex.Wait,
		DetachReason:    ScrubText(ex.DetachReason),
		DetachedAt:      ms(ex.DetachedAt),
		StopCause:       ScrubText(ex.StopCause),
		StopRequestedAt: ms(ex.StopRequestedAt),
		Exit:            copyExitCode(ex.ExitCode),
		Truncated:       ex.Truncated,
		StartedAt:       ms(ex.StartedAt),
		EndedAt:         ms(ex.EndedAt),
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
		Summary: ev.ResultSummary,
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
	if isDiffTool(ev.ToolName) {
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

func activeCallDetail(a *session.ActiveToolCall) acpwire.CallDetail {
	return acpwire.CallDetail{CallID: a.ToolCallID, StepID: int64(a.StepID), ToolName: a.Name, Args: indentJSON([]byte(a.Args)), Output: a.Output}
}

func todoDetail(st *session.State, id string) *acpwire.TodoDetail {
	if id == "" {
		return nil
	}
	for _, td := range st.Todos() {
		if td.ID == id {
			return &acpwire.TodoDetail{ID: td.ID, Content: td.Content, Status: td.Status,
				StartedAt: ms(td.StartedAt), CompletedAt: ms(td.CompletedAt)}
		}
	}
	return nil
}

// nodeKeys lists a node ID set's keys. It is not named keysOf: a test helper
// in this package already holds that name.
func nodeKeys(ids []NodeID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.Key)
	}
	return out
}
