package tui

import (
	"marshal/internal/app/session"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/contextpack"
)

// refreshInspectorContext hands the Context tab its snapshot.
//
// Every value is COPIED here, at the boundary, and the inspector never reaches
// back into the session for anything. That is what keeps the panel's scroll and
// selection meaningful: a panel reading live state would render whatever the
// pack happened to be at draw time, and the row the reader had selected would
// silently become a different row.
//
// The child scope is refreshed from the CHILD's own State, never from the
// parent's. On screen a parent's pack shown under a child's name is
// indistinguishable from the child's, so there is no fallback: with the child
// gone, the child scope reports that it is gone.
func (m *Model) refreshInspectorContext() {
	if m.inspector == nil || m.state == nil {
		return
	}
	m.inspector.model.SetContext(m.contextSnapshot(m.state))

	if name := m.inspector.model.ChildContextName(); name != "" {
		child, ok := m.inspectedChildContext()
		if !ok {
			// The child this scope named is no longer reachable. Returning to
			// the root is the honest move: leaving the reader inside a scope
			// whose data cannot be re-read would show them a stale child with
			// nothing on screen to say so.
			m.inspector.model.ClearChildContext()
			return
		}
		m.inspector.model.SetChildContext(name, m.contextSnapshot(child))
	}
}

// contextSnapshot converts one session's pack and last request into the
// inspector's presentation copies.
// The clock is read HERE, into the snapshot, rather than inside the renderer:
// the model's own injected clock means the panel's text does not depend on when
// it happened to be drawn. A render that called time.Now would produce
// different text on every frame, which makes the panel's own scroll position
// meaningless.
func (m *Model) contextSnapshot(state *session.State) inspector.ContextData {
	data := inspector.ContextData{
		Pack: contextPackFor(state),
		Now:  m.now(),
	}
	if req, ok := state.RequestInspection(); ok {
		data.Request = contextRequestFor(req)
	}
	return data
}

// contextPackFor copies a session's context pack for display.
//
// Truncation is INFERRED from the section's own Content/Full relationship,
// which is a fact the pack records: buildPackFromSections restores Content from
// Full before every budget pass and cuts it again, so Full differs from Content
// only where the budget actually bit. Nothing here guesses at a retrieval
// rationale or a file's freshness — neither is recorded anywhere Marshal can
// read, and inventing either would put a fabricated fact beside real ones.
func contextPackFor(state *session.State) inspector.ContextPack {
	pack := state.ContextPack()
	out := inspector.ContextPack{
		// "Known" means the pack has been through a budget pass, which is what
		// sets GeneratedAt (contextpack.resolvePackParams stamps it whenever it
		// is zero). It is deliberately NOT derived from len(Sections): an empty
		// pack and a never-built one are different facts, and Sections is empty
		// for both, so using it would present "nothing has been assembled" as
		// "the agent is carrying nothing".
		Known:           !pack.GeneratedAt.IsZero(),
		GeneratedAt:     pack.GeneratedAt,
		EstimatedTokens: pack.TokenUsage.EstimatedTokens,
		MaxTokens:       pack.TokenUsage.MaxTokens,
		Truncated:       pack.TokenUsage.Truncated,
	}
	out.Sections = make([]inspector.ContextSection, 0, len(pack.Sections))
	for _, s := range pack.Sections {
		out.Sections = append(out.Sections, inspector.ContextSection{
			Title:            s.Title,
			Kind:             string(s.Kind),
			Source:           s.Source,
			Priority:         s.Priority,
			EstimatedTokens:  s.EstimatedTokens,
			Content:          s.Content,
			ContentTruncated: sectionWasCut(s),
			OmittedTokens:    omittedTokens(s),
		})
	}
	return out
}

// sectionWasCut reports whether the pack budget cut a section's text.
//
// Full is the untruncated source and Content is what survived, so a difference
// between them IS the cut — no separate flag is needed, and none is invented.
func sectionWasCut(s contextpack.Section) bool {
	return s.Full != "" && s.Full != s.Content
}

// omittedTokens estimates what the budget dropped, so the reader can see how
// much of the section is missing rather than only that some is.
//
// It is an ESTIMATE for the same reason the section's own count is: the pack
// measures with contextpack.EstimateTokens, and a byte count here would be a
// second, disagreeing number about the same text.
func omittedTokens(s contextpack.Section) int {
	if !sectionWasCut(s) {
		return 0
	}
	omitted := contextpack.EstimateTokens(s.Full) - contextpack.EstimateTokens(s.Content)
	return max(omitted, 0)
}

// contextRequestFor copies one attempt's bounded inspection snapshot.
func contextRequestFor(req session.RequestInspection) inspector.ContextRequest {
	out := inspector.ContextRequest{
		Known:      true,
		AttemptID:  req.AttemptID,
		At:         req.At,
		Provider:   req.Provider,
		Model:      req.Model,
		Generation: req.Generation,
		Status:     string(req.Outcome.Status),
		Err:        req.Outcome.Err,

		Thinking:       req.Options.Thinking,
		Streaming:      req.Options.Streaming,
		ResponseFormat: req.Options.ResponseFormat,
		ToolChoice:     req.Options.ToolChoice,

		Truncated:       req.Truncated,
		OmittedBytes:    req.OmittedBytes,
		MessagesOmitted: req.MessagesOmitted,
		ToolsOmitted:    req.ToolsOmitted,

		PackTokens:    req.PackTokens,
		PackWindow:    req.PackWindow,
		PackTruncated: req.PackTruncated,
		PackSections:  req.PackSections,
		PackKnown:     req.PackKnown,
	}
	// nil and zero are different facts about an option: an unset temperature
	// and a temperature of 0 are different requests, and the pointer is what
	// tells them apart.
	if req.Options.MaxTokens != nil {
		out.MaxTokens, out.HasMaxTokens = *req.Options.MaxTokens, true
	}
	if req.Options.Temperature != nil {
		out.Temperature, out.HasTemperature = *req.Options.Temperature, true
	}

	out.Messages = make([]inspector.ContextMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		copied := inspector.ContextMessage{
			Role:         msg.Role,
			Content:      msg.Content,
			ToolCallID:   msg.ToolCallID,
			Truncated:    msg.Truncated,
			OmittedBytes: msg.OmittedBytes,
		}
		if len(msg.ToolCalls) > 0 {
			copied.ToolCalls = make([]inspector.ContextToolCall, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				copied.ToolCalls = append(copied.ToolCalls, inspector.ContextToolCall{
					ID:           tc.ID,
					Name:         tc.Name,
					Args:         tc.Args,
					Truncated:    tc.Truncated,
					OmittedBytes: tc.OmittedBytes,
				})
			}
		}
		out.Messages = append(out.Messages, copied)
	}
	out.Tools = make([]inspector.ContextTool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		out.Tools = append(out.Tools, inspector.ContextTool{
			Name:         tool.Name,
			Description:  tool.Description,
			Parameters:   tool.Parameters,
			Truncated:    tool.Truncated,
			OmittedBytes: tool.OmittedBytes,
		})
	}
	return out
}

// inspectedChildContext resolves the child session the Context tab is scoped
// to, or reports false when that child is no longer reachable.
//
// "Reachable" means the runtime still holds its State. The runtime releases a
// completed child's State after a bound (maxRetainedChildStates), and a scope
// naming a released child has nothing to re-read: reporting false lets the
// caller return the reader to the root rather than show them a snapshot of a
// conversation the app can no longer see.
func (m *Model) inspectedChildContext() (*session.State, bool) {
	if m.state == nil {
		return nil, false
	}
	name := m.inspector.model.ChildContextName()
	if name == "" {
		return nil, false
	}
	for _, v := range m.state.Subagents() {
		if agentContextLabel(v) != name {
			continue
		}
		if v.Child == nil {
			// A pipeline card never had a child state, and the runtime releases
			// a completed child's after a bound. Either way there is nothing to
			// re-read, and the caller returns the reader to the root.
			return nil, false
		}
		return v.Child, true
	}
	return nil, false
}

// enterInspectedChildContext scopes the Context tab to the SELECTED agent's own
// context and switches to that tab. It reports false when there is nothing to
// scope to, so a key handler can fall through rather than swallow the key.
//
// The child is read from the runtime by ID, never by roster index: a status
// change or a reorder between the key press and this call would otherwise scope
// the reader to a different agent.
func (m *Model) enterInspectedChildContext() bool {
	if m.inspector == nil || m.state == nil {
		return false
	}
	id := m.inspector.model.AgentIDSelected()
	if id == 0 {
		return false
	}
	for _, v := range m.state.Subagents() {
		if v.ID != id {
			continue
		}
		if v.Child == nil {
			// The runtime released this child's state, or it is a pipeline card
			// that never had one. Refusing is the honest answer: the alternative
			// is showing the PARENT's context under the child's name.
			return false
		}
		m.inspector.model.SetChildContext(agentContextLabel(v), m.contextSnapshot(v.Child))
		// The detail stack is cleared before switching tabs. The Agents tab
		// pushed an agent target when the reader opened that agent's
		// transcript, and leaving it standing would mean the next Esc silently
		// popped a level describing a panel that is no longer on screen —
		// while appearing, from the Context tab, to do nothing at all.
		m.inspector.model.ClearStack()
		m.inspector.model.Open(inspector.TabContext)
		return true
	}
	return false
}

// agentContextLabel is the name a child's context scope is titled with.
//
// It is derived from the runtime ID first, so the label is stable while the
// child runs and its status word changes; the human label is appended for
// readability. Deriving it from the label alone would make a renamed agent look
// like a different one.
func agentContextLabel(v session.SubagentView) string {
	label := "agent #" + itoa64(v.ID)
	if v.Label != "" {
		label += " " + v.Label
	}
	return label
}

// itoa64 renders a subagent's runtime ID.
//
// It mirrors the inspector's own helper rather than importing strconv for one
// call in a package that otherwise formats with fmt. The two produce identical
// text, which is load-bearing: the label this produces is compared against the
// label the inspector renders, and a mismatch would make every refresh look
// like the child had changed.
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
