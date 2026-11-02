package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/docpanel"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/commands"
	"marshal/internal/strutil"
)

// openInspector opens the inspector on the cursor node. Only steps, tool rows
// and completion events have anything to inspect.
func (m *Model) openInspector() (tea.Model, tea.Cmd, bool) {
	d, ok := m.inspectorDetail(m.cursor)
	if !ok {
		cmd := m.setFlash("Nothing to inspect here")
		return *m, cmd, true
	}
	m.dock.Open(inspector.New(d))
	m.refreshViewport()
	return *m, nil, true
}

// inspectorDetail builds the inspector's view model for a step, tool row or
// completion event. It reads the node's fields from the wire index and its
// uncapped detail from the session: either missing means nothing to inspect.
func (m *Model) inspectorDetail(id nodeID) (inspector.Detail, bool) {
	w := m.wireNode(id)
	if w == nil {
		return inspector.Detail{}, false
	}
	tool := w.Kind == acpwire.KindTool && w.Tool != nil && len(w.Tool.Calls) > 0
	beat := w.Kind == acpwire.KindStep && w.Step != nil
	if !tool && !beat && w.Completion == nil {
		return inspector.Detail{}, false
	}
	res, ok := m.nodeDetail(id)
	if !ok {
		return inspector.Detail{}, false
	}
	switch {
	case tool:
		return m.toolDetail(w, res.Detail), true
	case beat:
		return m.stepDetail(w, res.Detail), true
	}
	return completionDetail(w.Completion, res.Detail.Completion), true
}

// completionDetail is a finished job or subagent in the inspector: every
// recorded fact, and the whole retained output rather than the report's tail.
// A nil d reads as empty.
func completionDetail(c *acpwire.WireCompletion, d *acpwire.CompletionDetail) inspector.Detail {
	det := inspector.Detail{Title: c.ID, Subject: c.Label}
	add := func(k, v string) {
		if v != "" {
			det.Fields = append(det.Fields, inspector.Field{Key: k, Value: v})
		}
	}
	add("outcome", c.Outcome)
	if c.ExitCode != nil {
		add("exit", fmt.Sprint(*c.ExitCode))
	}
	add("stop cause", c.StopCause)
	if c.StartedAt != 0 {
		add("started", acpwire.Time(c.StartedAt).Local().Format("15:04:05"))
	}
	if c.EndedAt != 0 {
		add("ended", acpwire.Time(c.EndedAt).Local().Format("15:04:05"))
	}
	if c.DurationMs > 0 {
		add("duration", formatElapsed(time.Duration(c.DurationMs)*time.Millisecond))
	}
	add("cwd", c.Cwd)
	add("execution", c.ExecutionID)
	add("work key", c.WorkKey)
	if c.ReportMessageID != 0 {
		add("report message", fmt.Sprint(c.ReportMessageID))
	}

	sec := func(name, body string) {
		if strings.TrimSpace(body) != "" {
			det.Sections = append(det.Sections, inspector.Section{Name: name, Body: body})
		}
	}
	var output, report string
	if d != nil {
		output, report = d.Output, d.Report
	}
	sec("output", strutil.ScrubBlock(output))
	sec("report", report)
	sec("error", c.Error)
	sec("command", c.Command)
	return det
}

// actorText is who spoke and with what: the owner label, then the model, with
// the provider after an @.
func actorText(owner, model, provider string) string {
	parts := []string{}
	if owner != "" {
		parts = append(parts, owner)
	}
	if model != "" {
		mp := model
		if provider != "" {
			mp += " @ " + provider
		}
		parts = append(parts, mp)
	}
	return strings.Join(parts, " · ")
}

func (m *Model) toolDetail(w *acpwire.WireNode, d acpwire.NodeDetail) inspector.Detail {
	call := w.Tool.Calls[0]
	var cd acpwire.CallDetail
	if len(d.Calls) > 0 {
		cd = d.Calls[0]
	}
	subject := w.Tool.Name
	if call.Target != "" {
		subject += " " + call.Target
	}
	det := inspector.Detail{Subject: subject}
	pos := ""
	if idx, total, ok := m.callPlace(w); ok {
		pos = fmt.Sprintf(" · tool %d/%d", max(idx, 1), max(total, 1))
	}
	det.Title = fmt.Sprintf("step %d%s", cd.StepID, pos)
	if cd.StepID == 0 {
		det.Title = "tool call" + pos
	}
	add := func(k, v string) {
		if v != "" {
			det.Fields = append(det.Fields, inspector.Field{Key: k, Value: v})
		}
	}
	if d.Step != nil {
		add("actor", actorText(d.Step.Owner, d.Step.Model, d.Step.Provider))
		add("why", d.Step.Why)
	} else {
		add("actor", call.Role)
	}
	if call.At != 0 {
		add("started", acpwire.Time(call.At).Format("15:04:05"))
	}
	if call.DurationMs > 0 {
		add("duration", compactDuration(time.Duration(call.DurationMs)*time.Millisecond))
	}
	if cd.ExitCode != nil {
		add("exit code", fmt.Sprint(*cd.ExitCode))
	}
	if len(cd.Sandbox) > 0 {
		var sb acpwire.SandboxDetail
		if json.Unmarshal(cd.Sandbox, &sb) == nil {
			txt := sb.Backend
			if sb.NetworkIsolated {
				txt += ", network off"
			}
			if sb.KilledReason != "" {
				txt += ", killed: " + sb.KilledReason
			}
			add("sandbox", strings.TrimPrefix(txt, ", "))
		}
	}
	if call.Approval != "" && call.Approval != acpwire.ApprovalNotRequired {
		add("approval", call.Approval)
	}
	// The wire carries the row's one-line hook hint; the detail carries the
	// hooks themselves, and each decision is worth naming.
	if call.HookHint != "" {
		var parts []string
		var hooks []acpwire.HookDetail
		if len(cd.Hooks) > 0 && json.Unmarshal(cd.Hooks, &hooks) == nil {
			for _, hk := range hooks {
				if hk.Decision != "" {
					parts = append(parts, hk.Event+": "+hk.Decision)
				}
			}
		}
		add("hooks", strings.TrimSpace(call.HookHint+" "+strings.Join(parts, ", ")))
	}
	if cd.Rewritten {
		add("rewritten", "yes — arguments changed after approval")
	}
	add("finish reason", cd.FinishReason)
	add("tool call id", call.CallID)
	if cd.Notice != nil {
		add("notice", cd.Notice.Text)
	}
	if call.Risk != "" {
		add("risk", call.Risk)
	}

	sec := func(name, body string) {
		if strings.TrimSpace(body) != "" {
			det.Sections = append(det.Sections, inspector.Section{Name: name, Body: body})
		}
	}
	sec("args", cd.Args)
	output := cd.Output
	if cd.Error != "" {
		output = strings.TrimRight(output, "\n") + "\n\nerror: " + cd.Error
	}
	if isDiffTool(w.Tool.Name) {
		sec("diff", cd.Diff)
		if cd.Error != "" {
			sec("output", "error: "+cd.Error)
		}
	} else {
		sec("output", output)
	}
	if cd.Rewritten {
		sec("original args", cd.OriginalArgs)
	}
	if len(det.Sections) == 0 {
		det.Sections = []inspector.Section{{Name: "output", Body: "(no output)"}}
	}
	return det
}

func (m *Model) stepDetail(w *acpwire.WireNode, d acpwire.NodeDetail) inspector.Detail {
	si := w.Step
	det := inspector.Detail{Subject: si.Headline}
	det.Title = "step"
	if d.Step != nil && d.Step.ID != 0 {
		det.Title = fmt.Sprintf("step %d", d.Step.ID)
	}
	add := func(k, v string) {
		if v != "" {
			det.Fields = append(det.Fields, inspector.Field{Key: k, Value: v})
		}
	}
	add("actor", actorText(si.Owner, si.Model, si.Provider))
	if d.Step != nil {
		add("why", d.Step.Why)
	}
	if si.StartedAt != 0 {
		add("started", acpwire.Time(si.StartedAt).Format("15:04:05"))
		if si.EndedAt != 0 {
			add("duration", compactDuration(acpwire.Time(si.EndedAt).Sub(acpwire.Time(si.StartedAt))))
		}
	}
	if d.Step != nil {
		add("task", d.Step.TodoID)
	}
	add("tools", fmt.Sprint(m.beatCalls(w)))
	var narr []string
	for _, s := range d.Narration {
		narr = append(narr, strings.TrimSpace(s))
	}
	if len(narr) > 0 {
		det.Sections = append(det.Sections, inspector.Section{Name: "narration", Body: strings.Join(narr, "\n\n")})
	}
	var think []string
	for _, t := range d.Thinking {
		think = append(think, strings.TrimSpace(t.Text))
	}
	if len(think) > 0 {
		det.Sections = append(det.Sections, inspector.Section{Name: "thinking", Body: strings.Join(think, "\n\n")})
	}
	var tools []string
	for _, key := range w.Children {
		row := m.wire[key]
		if row == nil || row.Tool == nil {
			continue
		}
		for _, call := range row.Tool.Calls {
			tools = append(tools, row.Tool.Name+" "+call.Target)
		}
	}
	if len(tools) > 0 {
		det.Sections = append(det.Sections, inspector.Section{Name: "tools", Body: strings.Join(tools, "\n")})
	}
	if len(det.Sections) == 0 {
		det.Sections = []inspector.Section{{Name: "narration", Body: "(nothing recorded)"}}
	}
	return det
}

// callPlace is a call row's first call's place among its beat's calls, and the
// beat's call count. ok is false outside a beat.
func (m *Model) callPlace(w *acpwire.WireNode) (index, total int, ok bool) {
	parent := m.wire[w.Parent]
	if parent == nil || parent.Step == nil {
		return 0, 0, false
	}
	for _, key := range parent.Children {
		if r := m.wire[key]; r != nil && r.Tool != nil {
			total += len(r.Tool.Calls)
		}
	}
	before := 0
	for _, key := range parent.Children {
		row := m.wire[key]
		if row == nil || row.Tool == nil {
			continue
		}
		if row.ID == w.ID {
			if len(row.Tool.Calls) == 0 {
				return 0, 0, false
			}
			return before + 1, total, true
		}
		before += len(row.Tool.Calls)
	}
	return 0, 0, false
}

// beatCalls counts a beat's calls, a running one included, as countToolCalls
// does.
func (m *Model) beatCalls(w *acpwire.WireNode) int {
	n := 0
	for _, key := range w.Children {
		row := m.wire[key]
		if row == nil || row.Tool == nil {
			continue
		}
		n += len(row.Tool.Calls)
		if row.Tool.Running != nil {
			n++
		}
	}
	return n
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
		if k != acpwire.KindStep && k != acpwire.KindTool {
			continue
		}
		m.moveCursorNoScroll(j)
		if d, ok := m.inspectorDetail(m.cursor); ok {
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
			row("Enter", "expand / collapse (drills into a subagent card)"),
			row("i", "inspect the step or tool call"),
			row("y", "copy its text (OSC 52)"),
			row("o", "open its file in $EDITOR"),
			row("f", "drill into the subagent"),
			row("s", "stop the running item at the cursor"),
			{Header: "Leave"},
			row("Esc", "back to the input"),
			row("any other key", "back to the input, and type it"),
		},
	}
	m.dock.Open(docpanel.New(doc, m.state))
	m.refreshViewport()
	return *m, nil, true
}
