package viewmodel

import (
	"time"
	"unicode/utf8"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// The wire projection is the tree as a client outside the process sees it.
// Clients cannot import this package (web/ is standard library only), so the
// projection is plain JSON: a flat node list in which each node names its
// parent and its children by ID. Headlines, targets and owners are worked
// out here so every client says the same thing as the TUI.

// WireTextCap bounds every large text field (tool output, arguments, job
// output, run-event bodies) in the projection. A capped field keeps its
// first WireTextCap bytes and sets the node's Truncated flag. Full payloads
// are a separate, on-demand request.
const WireTextCap = 4096

// WireTree is a whole stack: the turn IDs in order, and every node with
// parents listed before their children.
type WireTree struct {
	Roots []string   `json:"roots"`
	Nodes []WireNode `json:"nodes"`
}

// WireNode is one node. Exactly one payload pointer is set, chosen by Kind;
// turn nodes carry none.
type WireNode struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Parent   string   `json:"parent,omitempty"`
	Children []string `json:"children,omitempty"`
	Live     bool     `json:"live,omitempty"`

	Step       *WireStep       `json:"step,omitempty"`
	Tool       *WireTool       `json:"tool,omitempty"`
	Message    *WireMessage    `json:"message,omitempty"`
	Thinking   *WireThinking   `json:"thinking,omitempty"`
	RunEvent   *WireRunEvent   `json:"runEvent,omitempty"`
	Completion *WireCompletion `json:"completion,omitempty"`
	Plan       *WirePlan       `json:"plan,omitempty"`
	Receipt    *WireReceipt    `json:"receipt,omitempty"`
	Band       *WireBand       `json:"band,omitempty"`
}

// WireBand is the pinned band: todo progress with its stale signal, and the
// running cards. It travels as a node with ID "band" that is deliberately not
// in roots, so a transcript walk ignores it while the stack patch protocol
// carries it with no change.
type WireBand struct {
	Todos      bool   `json:"todos,omitempty"`
	Done       int    `json:"done"`
	Total      int    `json:"total"`
	Active     string `json:"active,omitempty"`
	Stale      bool   `json:"stale,omitempty"`
	StaleAgeMs int64  `json:"staleAgeMs,omitempty"`
	// StaleSince is when the stale clock started (unix ms), so a client can
	// keep the age current between patches.
	StaleSince int64    `json:"staleSince,omitempty"`
	Next       []string `json:"next,omitempty"`
	// Tasks is the whole list for the Tasks tab. Unlike todos it is sent
	// whenever the session has a list, idle or not.
	Tasks    []WireTask       `json:"tasks,omitempty"`
	Entities []WireBandEntity `json:"entities,omitempty"`
}

// WireTask is one todo in the Tasks tab, with its share of the transcript.
// Times are Unix milliseconds; zero means unset.
type WireTask struct {
	ID          string `json:"id,omitempty"`
	Content     string `json:"content"`
	Status      string `json:"status"`
	StartedAt   int64  `json:"startedAt,omitempty"`
	CompletedAt int64  `json:"completedAt,omitempty"`
	Steps       int    `json:"steps,omitempty"`
	WorkMs      int64  `json:"workMs,omitempty"`
	// Ref is the first beat bound to the todo, where a click jumps.
	Ref string `json:"ref,omitempty"`
}

// WireBandEntity is one running card the band lists.
type WireBandEntity struct {
	Ref       string `json:"ref"`
	Label     string `json:"label"`
	Detail    string `json:"detail,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"`
}

// ProjectBand is the band as a wire node. Its ID is fixed so every patch
// updates the same node rather than accumulating copies.
func ProjectBand(b BandInfo) WireNode {
	wb := &WireBand{Todos: b.Todos, Done: b.Done, Total: b.Total, Active: ScrubText(b.Active), Stale: b.Stale,
		StaleAgeMs: b.StaleAge.Milliseconds(), StaleSince: ms(b.StaleSince)}
	for _, n := range b.Next {
		wb.Next = append(wb.Next, ScrubText(n))
	}
	for _, t := range b.Tasks {
		wb.Tasks = append(wb.Tasks, WireTask{ID: t.ID, Content: ScrubText(t.Content), Status: t.Status,
			StartedAt: ms(t.StartedAt), CompletedAt: ms(t.CompletedAt), Steps: t.Steps, WorkMs: t.Work.Milliseconds(), Ref: t.First.Key})
	}
	for _, e := range b.Entities {
		wb.Entities = append(wb.Entities, WireBandEntity{Ref: e.Ref.Key, Label: ScrubText(e.Label), Detail: ScrubText(e.Detail), StartedAt: ms(e.Started)})
	}
	return WireNode{ID: "band", Kind: "band", Band: wb}
}

// WireStep is a step's header and reasoning. Times are Unix milliseconds;
// zero means unset.
type WireStep struct {
	Headline  string         `json:"headline"`
	Rest      string         `json:"rest,omitempty"`
	StepIDs   []int64        `json:"stepIds,omitempty"`
	Owner     string         `json:"owner,omitempty"`
	RoleWord  string         `json:"roleWord,omitempty"`
	Role      string         `json:"role,omitempty"`
	Model     string         `json:"model,omitempty"`
	Provider  string         `json:"provider,omitempty"`
	TodoID    string         `json:"todoId,omitempty"`
	Todo      *WireTodoLabel `json:"todo,omitempty"`
	StartedAt int64          `json:"startedAt,omitempty"`
	EndedAt   int64          `json:"endedAt,omitempty"`
	Thoughts  []WireThought  `json:"thoughts,omitempty"`
}

// WireTodoLabel marks the beat where work moves to a different todo.
type WireTodoLabel struct {
	ID      string `json:"id"`
	Index   int    `json:"index"`
	Total   int    `json:"total"`
	Content string `json:"content"`
}

// WireThought is one finished reasoning block inside a step.
type WireThought struct {
	Text       string `json:"text"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// WireTool is a tool row: one call, a merged run of same-tool calls, or a
// call still running.
type WireTool struct {
	Name    string `json:"name"`
	Display string `json:"display"`
	// WorkKey is the stable conversation-work identity this row dispatched, so
	// a client can pair the retained dispatch row with the pinned live card by
	// identity rather than by display text. Omitted when the record carries
	// none: a legacy record, or a row that dispatched no work.
	WorkKey string       `json:"workKey,omitempty"`
	Calls   []WireCall   `json:"calls,omitempty"`
	Running *WireRunning `json:"running,omitempty"`
	// SubagentID names the subagent this row started, 0 when it started none.
	SubagentID int64 `json:"subagentId,omitempty"`
}

// WireCompletion is one finished piece of background work at the moment it
// ended: a background job's exit or a subagent's end. Expanded, it is the
// issue report.
type WireCompletion struct {
	Source          string `json:"source"`
	ID              string `json:"id"`
	SubagentID      int64  `json:"subagentId,omitempty"`
	Label           string `json:"label"`
	Outcome         string `json:"outcome"`
	Failed          bool   `json:"failed,omitempty"`
	ExitCode        *int   `json:"exitCode,omitempty"`
	StopCause       string `json:"stopCause,omitempty"`
	StartedAt       int64  `json:"startedAt,omitempty"`
	EndedAt         int64  `json:"endedAt,omitempty"`
	DurationMs      int64  `json:"durationMs,omitempty"`
	Measure         string `json:"measure,omitempty"`
	Summary         string `json:"summary,omitempty"`
	Command         string `json:"command,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
	Output          string `json:"output,omitempty"`
	Report          string `json:"report,omitempty"`
	Error           string `json:"error,omitempty"`
	WorkKey         string `json:"workKey,omitempty"`
	ExecutionID     string `json:"executionId,omitempty"`
	ReportMessageID int64  `json:"reportMessageId,omitempty"`
	Truncated       bool   `json:"truncated,omitempty"`
}

// WirePlan is a beat's plan line: the todo list as the beat's last
// todo.write left it.
type WirePlan struct {
	// Status is each item's status in list order: "completed",
	// "in_progress" or "pending".
	Status     []string `json:"status,omitempty"`
	Done       int      `json:"done"`
	Total      int      `json:"total"`
	Active     int      `json:"active,omitempty"`
	ActiveText string   `json:"activeText,omitempty"`
	Cleared    bool     `json:"cleared,omitempty"`
}

// WireCall is one finished call.
type WireCall struct {
	CallID     string   `json:"callId,omitempty"`
	Target     string   `json:"target,omitempty"`
	Args       string   `json:"args,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Output     string   `json:"output,omitempty"`
	Error      string   `json:"error,omitempty"`
	ExitCode   *int     `json:"exitCode,omitempty"`
	Failed     bool     `json:"failed,omitempty"`
	Approval   string   `json:"approval,omitempty"`
	Risk       string   `json:"risk,omitempty"`
	Files      []string `json:"files,omitempty"`
	Role       string   `json:"role,omitempty"`
	Notice     string   `json:"notice,omitempty"`
	DurationMs int64    `json:"durationMs,omitempty"`
	At         int64    `json:"at,omitempty"`
	Truncated  bool     `json:"truncated,omitempty"`
}

// WireRunning is a call that has not finished.
type WireRunning struct {
	Target    string `json:"target,omitempty"`
	Output    string `json:"output,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// WireMessage is a user prompt, an assistant reply, or any other message the
// transcript shows on its own row.
type WireMessage struct {
	Role          string `json:"role"`
	ContentType   string `json:"contentType,omitempty"`
	Content       string `json:"content"`
	Final         bool   `json:"final,omitempty"`
	Usage         string `json:"usage,omitempty"`
	Salvaged      bool   `json:"salvaged,omitempty"`
	SalvageReason string `json:"salvageReason,omitempty"`
	ThinkMs       int64  `json:"thinkMs,omitempty"`
	At            int64  `json:"at,omitempty"`
}

// WireThinking is a reasoning row outside any step, finished or live.
type WireThinking struct {
	Text       string `json:"text"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// WireRunEvent is one plan-run event (verify failure, review, commit, …).
type WireRunEvent struct {
	Kind      string `json:"kind"`
	TaskN     int    `json:"taskN,omitempty"`
	Title     string `json:"title,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Body      string `json:"body,omitempty"`
	Severity  string `json:"severity,omitempty"`
	At        int64  `json:"at,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// WireReceipt is the summary line closing a finished turn.
type WireReceipt struct {
	DurationMs int64  `json:"durationMs"`
	Tasks      int    `json:"tasks"`
	Steps      int    `json:"steps"`
	Tools      int    `json:"tools"`
	Files      int    `json:"files"`
	Usage      string `json:"usage,omitempty"`
	Salvaged   bool   `json:"salvaged,omitempty"`
}

var kindNames = [...]string{
	KindTurn:        "turn",
	KindStep:        "step",
	KindTool:        "tool",
	KindMessage:     "message",
	KindFinal:       "final",
	KindSubagent:    "subagent",
	KindRunEvent:    "runEvent",
	KindJobExit:     "jobExit",
	KindThinking:    "thinking",
	KindPassthrough: "passthrough",
	KindReceipt:     "receipt",
	KindCompletion:  "completion",
	KindPlanUpdate:  "planUpdate",
}

// String is the kind's wire name.
func (k Kind) String() string {
	if int(k) >= 0 && int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// Project flattens a built tree for the wire.
func Project(turns []*Node) WireTree {
	t := WireTree{Roots: make([]string, 0, len(turns))}
	var walk func(n *Node, parent string)
	walk = func(n *Node, parent string) {
		w := wireNode(n)
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

func wireNode(n *Node) WireNode {
	w := WireNode{ID: n.ID.Key, Kind: n.Kind.String(), Live: n.Live}
	for _, c := range n.Children {
		w.Children = append(w.Children, c.ID.Key)
	}
	switch {
	case n.Receipt != nil:
		r := n.Receipt
		w.Receipt = &WireReceipt{DurationMs: r.Duration.Milliseconds(), Tasks: r.Tasks, Steps: r.Steps,
			Tools: r.Tools, Files: r.Files, Usage: r.Usage, Salvaged: r.Salvaged}
	case n.Kind == KindStep && n.Step != nil:
		w.Step = wireStep(n)
	case n.Kind == KindThinking && n.Step != nil:
		// The live reasoning region when no step could hold it.
		w.Thinking = &WireThinking{Text: n.Step.LiveThinking}
	case n.Completion != nil:
		c := n.Completion
		wc := &WireCompletion{
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
		w.Plan = &WirePlan{Status: p.Status, Done: p.Done(), Total: p.Total(), Active: p.Active,
			ActiveText: ScrubText(p.ActiveText), Cleared: p.Cleared}
	case n.Active != nil:
		w.Tool = wireActive(n.Active)
	case len(n.Tools) > 0:
		w.Tool = wireTools(n.Tools)
		w.Tool.WorkKey = n.WorkKey
		if len(n.Tools) == 1 {
			w.Tool.SubagentID = StartedSubagent(n.Tools[0])
		}
	case n.Item != nil:
		wireItem(&w, n.Item)
	}
	return w
}

func wireStep(n *Node) *WireStep {
	si := n.Step
	head, rest := StepHeadline(si)
	st := si.Step
	ws := &WireStep{
		Headline: head, Rest: rest,
		Owner: StepOwner(st), RoleWord: StepRoleWord(st), Role: st.Actor.Role,
		Model: st.Actor.Model, Provider: st.Actor.Provider,
		TodoID: si.TodoID, StepIDs: si.StepIDs,
		StartedAt: ms(st.StartedAt), EndedAt: ms(st.EndedAt),
	}
	for _, t := range si.Thinking {
		ws.Thoughts = append(ws.Thoughts, WireThought{Text: ScrubBlock(t.Text), DurationMs: t.Duration.Milliseconds()})
	}
	if l := si.Todo; l != nil {
		ws.Todo = &WireTodoLabel{ID: l.ID, Index: l.Index, Total: l.Total, Content: ScrubText(l.Content)}
	}
	return ws
}

func wireTools(evs []registry.AuditEvent) *WireTool {
	wt := &WireTool{Name: evs[0].ToolName, Display: DisplayToolName(evs[0].ToolName)}
	for _, ev := range evs {
		c := WireCall{
			CallID: ev.ToolCallID, Target: ScrubText(ToolTarget(ev)), Summary: ScrubText(DisplaySummary(ev)),
			Error: ScrubBlock(ev.Error), ExitCode: ev.CommandExitCode, Failed: EventFailed(ev),
			Approval: string(ev.Approval), Risk: string(ev.Risk), Role: ev.AgentRole,
			DurationMs: ev.Duration.Milliseconds(), At: ms(ev.Timestamp),
		}
		for _, f := range ev.FilesChanged {
			c.Files = append(c.Files, ScrubText(f))
		}
		var cut1, cut2 bool
		c.Args, cut1 = capText(string(ev.Args))
		c.Output, cut2 = capText(ev.ResultContent)
		c.Truncated = cut1 || cut2
		if ev.Notice != nil {
			c.Notice = ScrubText(ev.Notice.Text)
		}
		wt.Calls = append(wt.Calls, c)
	}
	return wt
}

func wireActive(a *session.ActiveToolCall) *WireTool {
	target := a.Path
	if target == "" {
		target = a.Args
	}
	r := &WireRunning{Target: target, StartedAt: ms(a.StartedAt)}
	r.Output, r.Truncated = capTail(a.Output)
	return &WireTool{Name: a.Name, Display: DisplayToolName(a.Name), Running: r}
}

func wireItem(w *WireNode, it *session.TranscriptItem) {
	switch {
	case it.Message != nil:
		m := it.Message
		w.Message = &WireMessage{
			Role: string(m.Role), ContentType: string(m.ContentType), Content: m.Content,
			Final: m.Final, Usage: m.Usage, Salvaged: m.Salvaged, SalvageReason: m.SalvageReason,
			ThinkMs: m.ThinkDuration.Milliseconds(), At: ms(it.Timestamp),
		}
	case it.Thinking != nil:
		w.Thinking = &WireThinking{Text: ScrubBlock(it.Thinking.Text), DurationMs: it.Thinking.Duration.Milliseconds()}
	case it.RunEvent != nil:
		e := it.RunEvent
		wr := &WireRunEvent{Kind: runEventKind(e.Kind), TaskN: e.TaskN, Title: e.Title, Detail: e.Detail,
			Severity: e.Severity, At: ms(e.At)}
		wr.Body, wr.Truncated = capText(e.Body)
		w.RunEvent = wr
	}
}

var runEventKinds = [...]string{
	session.RunEventVerifyFailed: "verifyFailed",
	session.RunEventGateSkipped:  "gateSkipped",
	session.RunEventReview:       "review",
	session.RunEventCommit:       "commit",
	session.RunEventRetry:        "retry",
	session.RunEventConcern:      "concern",
	session.RunEventTaskDone:     "taskDone",
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

// capText keeps the first WireTextCap bytes of s, cut on a rune boundary.
func capText(s string) (string, bool) {
	if len(s) <= WireTextCap {
		return s, false
	}
	cut := WireTextCap
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// capTail keeps the last WireTextCap bytes of s, for output whose end is the
// part worth reading (a running command, a job's exit).
func capTail(s string) (string, bool) {
	if len(s) <= WireTextCap {
		return s, false
	}
	cut := len(s) - WireTextCap
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:], true
}
