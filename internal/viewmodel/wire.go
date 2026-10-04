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

	Step     *WireStep     `json:"step,omitempty"`
	Tool     *WireTool     `json:"tool,omitempty"`
	Message  *WireMessage  `json:"message,omitempty"`
	Thinking *WireThinking `json:"thinking,omitempty"`
	Subagent *WireSubagent `json:"subagent,omitempty"`
	RunEvent *WireRunEvent `json:"runEvent,omitempty"`
	JobExit  *WireJobExit  `json:"jobExit,omitempty"`
	Task     *WireTask     `json:"task,omitempty"`
	Receipt  *WireReceipt  `json:"receipt,omitempty"`
}

// WireStep is a step's header and reasoning. Times are Unix milliseconds;
// zero means unset.
type WireStep struct {
	Headline     string        `json:"headline"`
	Rest         string        `json:"rest,omitempty"`
	Inferred     bool          `json:"inferred,omitempty"`
	Owner        string        `json:"owner,omitempty"`
	RoleWord     string        `json:"roleWord,omitempty"`
	Role         string        `json:"role,omitempty"`
	Model        string        `json:"model,omitempty"`
	Provider     string        `json:"provider,omitempty"`
	TodoID       string        `json:"todoId,omitempty"`
	Heuristic    bool          `json:"heuristic,omitempty"`
	StartedAt    int64         `json:"startedAt,omitempty"`
	EndedAt      int64         `json:"endedAt,omitempty"`
	Thoughts     []WireThought `json:"thoughts,omitempty"`
	LiveThinking string        `json:"liveThinking,omitempty"`
}

// WireThought is one finished reasoning block inside a step.
type WireThought struct {
	Text       string `json:"text"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// WireTool is a tool row: one call, a merged run of same-tool calls, or a
// call still running.
type WireTool struct {
	Name    string       `json:"name"`
	Display string       `json:"display"`
	Calls   []WireCall   `json:"calls,omitempty"`
	Running *WireRunning `json:"running,omitempty"`
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

// WireSubagent is a subagent card.
type WireSubagent struct {
	Label       string `json:"label"`
	Status      string `json:"status"`
	Role        string `json:"role,omitempty"`
	Model       string `json:"model,omitempty"`
	Provider    string `json:"provider,omitempty"`
	ToolCalls   int    `json:"toolCalls,omitempty"`
	CurrentTool string `json:"currentTool,omitempty"`
	Tokens      int    `json:"tokens,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Error       string `json:"error,omitempty"`
	Salvaged    string `json:"salvaged,omitempty"`
	StartedAt   int64  `json:"startedAt,omitempty"`
	EndedAt     int64  `json:"endedAt,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
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

// WireJobExit is a background job's exit record.
type WireJobExit struct {
	JobID      string `json:"jobId"`
	Command    string `json:"command"`
	ExitCode   int    `json:"exitCode"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Output     string `json:"output,omitempty"`
	At         int64  `json:"at,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// WireTask is a task header.
type WireTask struct {
	TodoID            string `json:"todoId"`
	Content           string `json:"content"`
	Status            string `json:"status"`
	Index             int    `json:"index,omitempty"`
	Total             int    `json:"total,omitempty"`
	Dropped           bool   `json:"dropped,omitempty"`
	Steps             int    `json:"steps"`
	WorkMs            int64  `json:"workMs,omitempty"`
	Tools             int    `json:"tools"`
	Edits             int    `json:"edits"`
	UnresolvedFailure bool   `json:"unresolvedFailure,omitempty"`
	FirstNarration    string `json:"firstNarration,omitempty"`
	StartedAt         int64  `json:"startedAt,omitempty"`
	CompletedAt       int64  `json:"completedAt,omitempty"`
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
	KindTask:        "task",
	KindReceipt:     "receipt",
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
	case n.Task != nil:
		w.Task = wireTask(n.Task)
	case n.Receipt != nil:
		r := n.Receipt
		w.Receipt = &WireReceipt{DurationMs: r.Duration.Milliseconds(), Tasks: r.Tasks, Steps: r.Steps,
			Tools: r.Tools, Files: r.Files, Usage: r.Usage, Salvaged: r.Salvaged}
	case n.Kind == KindStep && n.Step != nil:
		w.Step = wireStep(n)
	case n.Kind == KindThinking && n.Step != nil:
		// The live reasoning region when no step could hold it.
		w.Thinking = &WireThinking{Text: n.Step.LiveThinking}
	case n.Active != nil:
		w.Tool = wireActive(n.Active)
	case len(n.Tools) > 0:
		w.Tool = wireTools(n.Tools)
	case n.Item != nil:
		wireItem(&w, n.Item)
	}
	return w
}

func wireStep(n *Node) *WireStep {
	si := n.Step
	head, rest, inferred := StepHeadline(si, n.Children)
	st := si.Step
	ws := &WireStep{
		Headline: head, Rest: rest, Inferred: inferred,
		Owner: StepOwner(st), RoleWord: StepRoleWord(st), Role: st.Actor.Role,
		Model: st.Actor.Model, Provider: st.Actor.Provider,
		TodoID: si.TodoID, Heuristic: si.Heuristic,
		StartedAt: ms(st.StartedAt), EndedAt: ms(st.EndedAt),
		LiveThinking: si.LiveThinking,
	}
	for _, t := range si.Thinking {
		ws.Thoughts = append(ws.Thoughts, WireThought{Text: t.Text, DurationMs: t.Duration.Milliseconds()})
	}
	return ws
}

func wireTools(evs []registry.AuditEvent) *WireTool {
	wt := &WireTool{Name: evs[0].ToolName, Display: DisplayToolName(evs[0].ToolName)}
	for _, ev := range evs {
		c := WireCall{
			CallID: ev.ToolCallID, Target: ToolTarget(ev), Summary: ev.ResultSummary,
			Error: ev.Error, ExitCode: ev.CommandExitCode, Failed: EventFailed(ev),
			Approval: string(ev.Approval), Risk: string(ev.Risk), Files: ev.FilesChanged,
			Role: ev.AgentRole, DurationMs: ev.Duration.Milliseconds(), At: ms(ev.Timestamp),
		}
		var cut1, cut2 bool
		c.Args, cut1 = capText(string(ev.Args))
		c.Output, cut2 = capText(ev.ResultContent)
		c.Truncated = cut1 || cut2
		if ev.Notice != nil {
			c.Notice = ev.Notice.Text
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
		w.Thinking = &WireThinking{Text: it.Thinking.Text, DurationMs: it.Thinking.Duration.Milliseconds()}
	case it.Subagent != nil:
		v := it.Subagent
		ws := &WireSubagent{
			Label: v.Label, Status: subagentStatus(v.Status), Role: string(v.Role), Model: v.Model,
			Provider: v.Provider, ToolCalls: v.ToolCalls, CurrentTool: v.CurrentTool, Tokens: v.TokensUsed,
			Error: v.Error, Salvaged: v.SalvagedReason, StartedAt: ms(v.StartedAt), EndedAt: ms(v.EndedAt),
		}
		ws.Summary, ws.Truncated = capText(v.Summary)
		w.Subagent = ws
	case it.RunEvent != nil:
		e := it.RunEvent
		wr := &WireRunEvent{Kind: runEventKind(e.Kind), TaskN: e.TaskN, Title: e.Title, Detail: e.Detail,
			Severity: e.Severity, At: ms(e.At)}
		wr.Body, wr.Truncated = capText(e.Body)
		w.RunEvent = wr
	case it.JobExit != nil:
		j := it.JobExit
		wj := &WireJobExit{JobID: j.ID, Command: j.Command, ExitCode: j.ExitCode,
			DurationMs: j.Duration.Milliseconds(), At: ms(j.At)}
		wj.Output, wj.Truncated = capTail(j.Output)
		w.JobExit = wj
	}
}

func wireTask(t *TaskInfo) *WireTask {
	return &WireTask{
		TodoID: t.TodoID, Content: t.Content, Status: t.Status, Index: t.Index, Total: t.Total,
		Dropped: t.Dropped, Steps: t.Steps, WorkMs: t.Work.Milliseconds(), Tools: t.Tools, Edits: t.Edits,
		UnresolvedFailure: t.UnresolvedFailure, FirstNarration: t.FirstNarration,
		StartedAt: ms(t.StartedAt), CompletedAt: ms(t.CompletedAt),
	}
}

func subagentStatus(s session.SubagentStatus) string {
	switch s {
	case session.SubagentRunning:
		return "running"
	case session.SubagentDone:
		return "done"
	case session.SubagentFailed:
		return "failed"
	}
	return "unknown"
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
