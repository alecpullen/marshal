package viewmodel

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

// ProjectBand is the band as a wire node. Its ID is fixed so every patch
// updates the same node rather than accumulating copies.
func ProjectBand(b BandInfo) acpwire.WireNode {
	wb := &acpwire.WireBand{Todos: b.Todos, Done: b.Done, Total: b.Total, Active: strutil.ScrubText(b.Active), Stale: b.Stale,
		StaleAgeMs: b.StaleAge.Milliseconds(), StaleSince: ms(b.StaleSince)}
	for _, n := range b.Next {
		wb.Next = append(wb.Next, strutil.ScrubText(n))
	}
	for _, t := range b.Tasks {
		wb.Tasks = append(wb.Tasks, acpwire.WireTask{ID: t.ID, Content: strutil.ScrubText(t.Content), Status: t.Status,
			StartedAt: ms(t.StartedAt), CompletedAt: ms(t.CompletedAt), Steps: t.Steps, WorkMs: t.Work.Milliseconds(), Ref: t.First.Key})
	}
	for _, e := range b.Entities {
		wb.Entities = append(wb.Entities, acpwire.WireBandEntity{Ref: e.Ref.Key, Label: strutil.ScrubText(e.Label), Detail: strutil.ScrubText(e.Detail), StartedAt: ms(e.Started)})
	}
	return acpwire.WireNode{ID: acpwire.BandID, Kind: acpwire.KindBand, Band: wb}
}

var kindNames = [...]string{
	KindTurn:        acpwire.KindTurn,
	KindStep:        acpwire.KindStep,
	KindTool:        acpwire.KindTool,
	KindMessage:     acpwire.KindMessage,
	KindFinal:       acpwire.KindFinal,
	KindSubagent:    acpwire.KindSubagent, // no node carries it; band refs do
	KindRunEvent:    acpwire.KindRunEvent,
	KindJobExit:     acpwire.KindJobExit, // no node carries it; exits become completions
	KindThinking:    acpwire.KindThinking,
	KindPassthrough: acpwire.KindPassthrough,
	KindReceipt:     acpwire.KindReceipt,
	KindCompletion:  acpwire.KindCompletion,
	KindPlanUpdate:  acpwire.KindPlanUpdate,
}

// String is the kind's wire name.
func (k Kind) String() string {
	if int(k) >= 0 && int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// Project flattens a built tree for the wire. workdir is the active root a
// call's subject elides.
func Project(turns []*Node, workdir string) acpwire.WireTree {
	t := acpwire.WireTree{Roots: make([]string, 0, len(turns))}
	var walk func(n *Node, parent string)
	walk = func(n *Node, parent string) {
		w := wireNode(n, workdir)
		w.Parent = parent
		t.Nodes = append(t.Nodes, w)
		for _, c := range n.Children {
			walk(c, n.ID.Key)
		}
	}
	for _, n := range turns {
		t.Roots = append(t.Roots, n.ID.Key)
		walk(n, "")
	}
	return t
}

func wireNode(n *Node, workdir string) acpwire.WireNode {
	w := acpwire.WireNode{ID: n.ID.Key, Kind: n.Kind.String(), Live: n.Live, Version: versionHex(n.Version)}
	for _, c := range n.Children {
		w.Children = append(w.Children, c.ID.Key)
	}
	switch {
	case n.Receipt != nil:
		r := n.Receipt
		w.Receipt = &acpwire.WireReceipt{DurationMs: r.Duration.Milliseconds(), Tasks: r.Tasks, Steps: r.Steps,
			Tools: r.Tools, Files: r.Files, Usage: r.Usage, Salvaged: r.Salvaged}
	case n.Kind == KindStep && n.Step != nil:
		w.Step = wireStep(n)
	case n.Kind == KindThinking && n.Step != nil:
		// The live reasoning region when no step could hold it.
		w.Thinking = &acpwire.WireThinking{Text: n.Step.LiveThinking, StartedAt: ms(n.Step.LiveSince)}
	case n.Completion != nil:
		c := n.Completion
		wc := &acpwire.WireCompletion{
			Source: c.Source, ID: c.ID, SubagentID: c.SubagentID, Label: c.Label, Outcome: c.Outcome,
			Failed: c.Failed, StopCause: c.StopCause, StartedAt: ms(c.StartedAt), EndedAt: ms(c.EndedAt),
			DurationMs: c.Duration.Milliseconds(), Measure: c.Measure, Summary: c.Summary,
			Command: c.Command, Cwd: c.Cwd, Report: c.Report, Error: c.Error,
			WorkKey: c.WorkKey, ExecutionID: c.ExecutionID, ReportMessageID: c.ReportMessageID,
		}
		if c.ExitCode != nil {
			code := *c.ExitCode
			wc.ExitCode = &code
		}
		var cut1, cut2 bool
		wc.Output, cut1 = capTail(c.Output)
		wc.Report, cut2 = capText(c.Report)
		wc.Truncated = cut1 || cut2 || c.Truncated
		w.Completion = wc
	case n.PlanUpdate != nil:
		p := n.PlanUpdate
		w.Plan = &acpwire.WirePlan{Status: p.Status, Done: p.Done(), Total: p.Total(), Active: p.Active,
			ActiveText: strutil.ScrubText(p.ActiveText), Cleared: p.Cleared}
	case n.Active != nil:
		w.Tool = wireActive(n.Active)
	case len(n.Tools) > 0:
		w.Tool = wireTools(n.Tools, workdir)
		w.Tool.WorkKey = n.WorkKey
		if len(n.Tools) == 1 {
			w.Tool.SubagentID = StartedSubagent(n.Tools[0])
			if _, ok := CallersSymbol(n.Tools[0]); ok {
				w.Tool.Callers = true
			}
		}
	case n.Item != nil:
		wireItem(&w, n.Item)
	}
	return w
}

func wireStep(n *Node) *acpwire.WireStep {
	si := n.Step
	head, rest := StepHeadline(si)
	st := si.Step
	ws := &acpwire.WireStep{
		Headline: head, Rest: rest,
		Owner: StepOwner(st), RoleWord: StepRoleWord(st), Role: st.Actor.Role,
		Model: st.Actor.Model, Provider: st.Actor.Provider,
		TodoID: si.TodoID, StepIDs: si.StepIDs,
		StartedAt: ms(st.StartedAt), EndedAt: ms(st.EndedAt),
	}
	for _, t := range si.Thinking {
		ws.Thoughts = append(ws.Thoughts, acpwire.WireThought{Text: strutil.ScrubBlock(t.Text), DurationMs: t.Duration.Milliseconds()})
	}
	for _, m := range si.Narration {
		ws.Narration = append(ws.Narration, strings.TrimSpace(m.Content))
	}
	if l := si.Todo; l != nil {
		ws.Todo = &acpwire.WireTodoLabel{ID: l.ID, Index: l.Index, Total: l.Total, Content: strutil.ScrubText(l.Content)}
	}
	return ws
}

func wireTools(evs []registry.AuditEvent, workdir string) *acpwire.WireTool {
	wt := &acpwire.WireTool{Name: evs[0].ToolName, Display: DisplayToolName(evs[0].ToolName), Verb: CallVerb(evs[0].ToolName)}
	for _, ev := range evs {
		c := acpwire.WireCall{
			CallID: ev.ToolCallID, Target: strutil.ScrubText(ToolTarget(ev)), Summary: strutil.ScrubText(DisplaySummary(ev)),
			Error: strutil.ScrubBlock(ev.Error), ExitCode: ev.CommandExitCode, Failed: EventFailed(ev),
			Approval: string(ev.Approval), Risk: string(ev.Risk), Role: ev.AgentRole,
			DurationMs: ev.Duration.Milliseconds(), At: ms(ev.Timestamp),
			Subject: CallSubject(ev, workdir), SummaryLine: SummaryLine(ev), DiffStat: CallDiffStat(ev),
			HookHint: HookHint(ev.Hooks), KilledReason: strutil.ScrubText(ev.Sandbox.KilledReason), JobID: JobStartID(ev),
		}
		if EventFailed(ev) {
			c.Tail = FailedTail(ev)
		}
		for _, f := range ev.FilesChanged {
			c.Files = append(c.Files, strutil.ScrubText(f))
		}
		var cut1, cut2 bool
		c.Args, cut1 = capText(string(ev.Args))
		c.Output, cut2 = capText(ev.ResultContent)
		c.Truncated = cut1 || cut2
		if ev.Notice != nil {
			c.Notice = strutil.ScrubText(ev.Notice.Text)
		}
		wt.Calls = append(wt.Calls, c)
	}
	return wt
}

func wireActive(a *session.ActiveToolCall) *acpwire.WireTool {
	target := a.Path
	if target == "" {
		target = a.Args
	}
	r := &acpwire.WireRunning{Target: target, StartedAt: ms(a.StartedAt),
		Subject: RunningSubject(*a), CallID: a.ToolCallID}
	var outCut, argCut bool
	r.Output, outCut = capTail(a.Output)
	r.Args, argCut = capText(a.Args)
	r.Truncated = outCut || argCut
	return &acpwire.WireTool{Name: a.Name, Display: DisplayToolName(a.Name), Verb: CallVerb(a.Name), Running: r}
}

func wireItem(w *acpwire.WireNode, it *session.TranscriptItem) {
	switch {
	case it.Message != nil:
		m := it.Message
		reasoning, cut := capText(m.Reasoning)
		w.Message = &acpwire.WireMessage{
			Role: string(m.Role), ContentType: string(m.ContentType), Content: m.Content,
			Final: m.Final, Usage: m.Usage, Salvaged: m.Salvaged, SalvageReason: m.SalvageReason,
			ThinkMs: m.ThinkDuration.Milliseconds(), At: ms(it.Timestamp), Reasoning: reasoning,
			Truncated: cut,
		}
	case it.Thinking != nil:
		w.Thinking = &acpwire.WireThinking{Text: strutil.ScrubBlock(it.Thinking.Text), DurationMs: it.Thinking.Duration.Milliseconds()}
	case it.RunEvent != nil:
		e := it.RunEvent
		wr := &acpwire.WireRunEvent{Kind: runEventKind(e.Kind), TaskN: e.TaskN, Title: e.Title, Detail: e.Detail,
			Severity: e.Severity, At: ms(e.At)}
		wr.Body, wr.Truncated = capText(e.Body)
		wr.BodyLines = bodyLines(e.Body)
		w.RunEvent = wr
	}
}

// bodyLines is how many lines a run-event body renders as: the count
// renderRunEvent takes, with the trailing newline a body usually ends in
// trimmed off. An empty body is no lines at all.
func bodyLines(body string) int {
	if body == "" {
		return 0
	}
	return len(strings.Split(strings.TrimRight(body, "\n"), "\n"))
}

var runEventKinds = [...]string{
	session.RunEventVerifyFailed: acpwire.RunEventVerifyFailed,
	session.RunEventGateSkipped:  acpwire.RunEventGateSkipped,
	session.RunEventReview:       acpwire.RunEventReview,
	session.RunEventCommit:       acpwire.RunEventCommit,
	session.RunEventRetry:        acpwire.RunEventRetry,
	session.RunEventConcern:      acpwire.RunEventConcern,
	session.RunEventTaskDone:     acpwire.RunEventTaskDone,
}

func runEventKind(k session.RunEventKind) string {
	if int(k) >= 0 && int(k) < len(runEventKinds) {
		return runEventKinds[k]
	}
	return "unknown"
}

// ms is t as Unix milliseconds, 0 for the zero time.
func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// versionHex is a payload hash as a node's wire version, "" when the node
// carries none: the turn nodes and the running calls.
func versionHex(v uint64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatUint(v, 16)
}

// capText keeps the first acpwire.WireTextCap bytes of s, cut on a rune
// boundary.
func capText(s string) (string, bool) {
	if len(s) <= acpwire.WireTextCap {
		return s, false
	}
	cut := acpwire.WireTextCap
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// capTail keeps the last acpwire.WireTextCap bytes of s, for output whose end
// is the part worth reading (a running command, a job's exit).
func capTail(s string) (string, bool) {
	if len(s) <= acpwire.WireTextCap {
		return s, false
	}
	cut := len(s) - acpwire.WireTextCap
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:], true
}
