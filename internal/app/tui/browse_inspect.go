package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/docpanel"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/app/tui/stack"
	"marshal/internal/commands"
	"marshal/internal/tools/registry"
)

// openInspector opens the inspector on the cursor node. Only steps and tool
// rows have anything to inspect.
func (m *Model) openInspector() (tea.Model, tea.Cmd, bool) {
	d, ok := m.inspectorDetail(m.currentNode())
	if !ok {
		cmd := m.setFlash("Nothing to inspect here")
		return *m, cmd, true
	}
	m.dock.Open(inspector.New(d))
	m.refreshViewport()
	return *m, nil, true
}

// inspectorDetail builds the inspector's view model for a step or tool row.
func (m *Model) inspectorDetail(n *stack.Node) (inspector.Detail, bool) {
	if n == nil {
		return inspector.Detail{}, false
	}
	switch {
	case n.Kind == stack.KindTool && len(n.Tools) > 0:
		return m.toolDetail(n), true
	case n.Kind == stack.KindStep && n.Step != nil:
		return m.stepDetail(n), true
	}
	return inspector.Detail{}, false
}

func (m *Model) stepOf(id int64) (session.Step, bool) {
	st, _ := m.transcriptSource()
	if st == nil || id == 0 {
		return session.Step{}, false
	}
	return st.Step(id)
}

func actorText(st session.Step) string {
	parts := []string{}
	if o := stepOwner(st); o != "" {
		parts = append(parts, o)
	}
	if st.Actor.Model != "" {
		mp := st.Actor.Model
		if st.Actor.Provider != "" {
			mp += " @ " + st.Actor.Provider
		}
		parts = append(parts, mp)
	}
	return strings.Join(parts, " · ")
}

func (m *Model) whyText(stepID int64) string {
	state, _ := m.transcriptSource()
	if state == nil || stepID == 0 {
		return ""
	}
	if h, _ := firstSentence(state.StepNarration(stepID)); h != "" {
		return stripEmphasis(h)
	}
	return ""
}

func prettyJSON(raw []byte) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") == nil {
		return buf.String()
	}
	return string(raw)
}

func (m *Model) toolDetail(n *stack.Node) inspector.Detail {
	ev := n.Tools[0]
	d := inspector.Detail{Subject: toolSubject(ev)}
	pos := ""
	if parent := m.parentStep(n.ID); parent != nil {
		idx, total := 0, 0
		for _, row := range parent.Children {
			for _, t := range row.Tools {
				total++
				if t.ToolCallID == ev.ToolCallID && t.Timestamp.Equal(ev.Timestamp) {
					idx = total
				}
			}
		}
		pos = fmt.Sprintf(" · tool %d/%d", max(idx, 1), max(total, 1))
	}
	d.Title = fmt.Sprintf("step %d%s", ev.StepID, pos)
	if ev.StepID == 0 {
		d.Title = "tool call" + pos
	}
	add := func(k, v string) {
		if v != "" {
			d.Fields = append(d.Fields, inspector.Field{Key: k, Value: v})
		}
	}
	if st, ok := m.stepOf(ev.StepID); ok {
		add("actor", actorText(st))
	} else if ev.AgentRole != "" {
		add("actor", ev.AgentRole)
	}
	add("why", m.whyText(ev.StepID))
	if !ev.Timestamp.IsZero() {
		add("started", ev.Timestamp.Format("15:04:05"))
	}
	if ev.Duration > 0 {
		add("duration", compactDuration(ev.Duration))
	}
	if ev.CommandExitCode != nil {
		add("exit code", fmt.Sprint(*ev.CommandExitCode))
	}
	if ev.Sandbox.Enabled || ev.Sandbox.Backend != "" {
		sb := ev.Sandbox.Backend
		if ev.Sandbox.NetworkIsolated {
			sb += ", network off"
		}
		if ev.Sandbox.KilledReason != "" {
			sb += ", killed: " + ev.Sandbox.KilledReason
		}
		add("sandbox", strings.TrimPrefix(sb, ", "))
	}
	if ev.Approval != "" && ev.Approval != registry.ApprovalNotRequired {
		add("approval", string(ev.Approval))
	}
	if h := hookIndicatorText(ev.Hooks); h != "" {
		var parts []string
		for _, hk := range ev.Hooks {
			if hk.Decision != "" {
				parts = append(parts, hk.Event+": "+hk.Decision)
			}
		}
		add("hooks", strings.TrimSpace(h+" "+strings.Join(parts, ", ")))
	}
	if ev.Rewritten {
		add("rewritten", "yes — arguments changed after approval")
	}
	add("finish reason", ev.FinishReason)
	add("tool call id", ev.ToolCallID)
	if ev.Notice != nil {
		add("notice", ev.Notice.Text)
	}
	if ev.Risk != "" {
		add("risk", string(ev.Risk))
	}

	sec := func(name, body string) {
		if strings.TrimSpace(body) != "" {
			d.Sections = append(d.Sections, inspector.Section{Name: name, Body: body})
		}
	}
	sec("args", prettyJSON(ev.Args))
	output := ev.ResultContent
	if output == "" {
		output = ev.ResultSummary
	}
	if ev.Error != "" {
		output = strings.TrimRight(output, "\n") + "\n\nerror: " + ev.Error
	}
	if isDiffTool(ev.ToolName) {
		sec("diff", ev.ResultContent)
		if ev.Error != "" {
			sec("output", "error: "+ev.Error)
		}
	} else {
		sec("output", output)
	}
	if ev.Rewritten {
		sec("original args", prettyJSON(ev.OriginalArgs))
	}
	if len(d.Sections) == 0 {
		d.Sections = []inspector.Section{{Name: "output", Body: "(no output)"}}
	}
	return d
}

func (m *Model) stepDetail(n *stack.Node) inspector.Detail {
	si := n.Step
	head, _, _ := stepHeadline(si, n.Children)
	d := inspector.Detail{Subject: head}
	d.Title = "step"
	if si.Step.ID != 0 {
		d.Title = fmt.Sprintf("step %d", si.Step.ID)
	}
	add := func(k, v string) {
		if v != "" {
			d.Fields = append(d.Fields, inspector.Field{Key: k, Value: v})
		}
	}
	add("actor", actorText(si.Step))
	add("why", m.whyText(si.Step.ID))
	if !si.Step.StartedAt.IsZero() {
		add("started", si.Step.StartedAt.Format("15:04:05"))
		if !si.Step.EndedAt.IsZero() {
			add("duration", compactDuration(si.Step.EndedAt.Sub(si.Step.StartedAt)))
		}
	}
	if si.Step.TodoID != "" {
		add("task", si.Step.TodoID)
	}
	add("tools", fmt.Sprint(countToolCalls(n.Children)))
	var narr []string
	for _, msg := range si.Narration {
		narr = append(narr, strings.TrimSpace(msg.Content))
	}
	if len(narr) > 0 {
		d.Sections = append(d.Sections, inspector.Section{Name: "narration", Body: strings.Join(narr, "\n\n")})
	}
	var think []string
	for _, t := range si.Thinking {
		think = append(think, strings.TrimSpace(t.Text))
	}
	if len(think) > 0 {
		d.Sections = append(d.Sections, inspector.Section{Name: "thinking", Body: strings.Join(think, "\n\n")})
	}
	var tools []string
	for _, row := range n.Children {
		for _, ev := range row.Tools {
			tools = append(tools, ev.ToolName+" "+toolTarget(ev))
		}
	}
	if len(tools) > 0 {
		d.Sections = append(d.Sections, inspector.Section{Name: "tools", Body: strings.Join(tools, "\n")})
	}
	if len(d.Sections) == 0 {
		d.Sections = []inspector.Section{{Name: "narration", Body: "(nothing recorded)"}}
	}
	return d
}

// toolSubject is the row's headline: its command, path or query.
func toolSubject(ev registry.AuditEvent) string {
	if t := toolTarget(ev); t != "" {
		return ev.ToolName + " " + t
	}
	return ev.ToolName
}

// parentStep finds the step node a tool row sits under.
func (m *Model) parentStep(id stack.NodeID) *stack.Node {
	for _, n := range m.browseTree {
		if n.Kind != stack.KindStep {
			continue
		}
		for _, c := range n.Children {
			if c.ID == id {
				return n
			}
		}
	}
	return nil
}

// handleInspectorMsg routes the inspector's requests. It returns handled=false
// for messages that are not its own.
func (m *Model) handleInspectorMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch v := msg.(type) {
	case inspector.ClosedMsg:
		m.dock.CloseNow()
		m.invalidateTranscript()
		m.refreshViewport()
		m.scrollToCursor()
		return nil, true
	case inspector.NavigateMsg:
		m.stepInspector(v.Delta)
		return nil, true
	case inspector.CopyMsg:
		return m.copyText(v.Text), true
	case inspector.OpenMsg:
		return m.openCursorFile(), true
	}
	return nil, false
}

// stepInspector moves the browse cursor to the next or previous step or tool
// row and re-points the open inspector at it.
func (m *Model) stepInspector(delta int) {
	p, ok := m.dock.Panel().(*inspector.Panel)
	if !ok {
		return
	}
	i := m.cursorIndex()
	for j := i + delta; j >= 0 && j < len(m.browseItems); j += delta {
		k := m.browseItems[j].kind
		if k != stack.KindStep && k != stack.KindTool {
			continue
		}
		m.moveCursorNoScroll(j)
		if d, ok := m.inspectorDetail(m.currentNode()); ok {
			p.SetDetail(d)
		}
		return
	}
}

// openBrowseHelp shows the browse keys in a docked panel.
func (m *Model) openBrowseHelp() (tea.Model, tea.Cmd, bool) {
	row := func(k, d string) commands.Row { return commands.Row{Text: k, Detail: d} }
	doc := commands.Doc{
		Title: "Browse mode",
		Rows: []commands.Row{
			{Header: "Move"},
			row("j / ↓   k / ↑", "next / previous node"),
			row("J / ]   K / [", "next / previous task header or turn"),
			row("g   G", "first / last node"),
			row("PgUp PgDn  ^U ^D", "scroll a page"),
			{Header: "Act"},
			row("Enter", "cycle detail (drills into a subagent card)"),
			row("i", "inspect the step or tool call"),
			row("y", "copy its text (OSC 52)"),
			row("o", "open its file in $EDITOR"),
			row("f", "drill into the subagent"),
			row("z", "fold / unfold finished tasks"),
			{Header: "Leave"},
			row("Esc", "back to the input"),
			row("any other key", "back to the input, and type it"),
		},
	}
	m.dock.Open(docpanel.New(doc, m.state))
	m.refreshViewport()
	return *m, nil, true
}
