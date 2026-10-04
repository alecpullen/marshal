package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

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

// NodeDetail is the uncapped detail behind one stack node.
type NodeDetail struct {
	Calls     []CallDetail    `json:"calls,omitempty"`
	Narration []string        `json:"narration,omitempty"`
	Thinking  []ThoughtDetail `json:"thinking,omitempty"`
	Todo      *TodoDetail     `json:"todo,omitempty"`
	Relations *RelationDetail `json:"relations,omitempty"`
}

// CallDetail is one tool call with nothing capped.
type CallDetail struct {
	CallID       string                  `json:"callId,omitempty"`
	StepID       int64                   `json:"stepId,omitempty"`
	ToolName     string                  `json:"toolName"`
	Args         string                  `json:"args,omitempty"`
	OriginalArgs string                  `json:"originalArgs,omitempty"`
	Output       string                  `json:"output,omitempty"`
	Diff         string                  `json:"diff,omitempty"`
	Error        string                  `json:"error,omitempty"`
	ExitCode     *int                    `json:"exitCode,omitempty"`
	Model        string                  `json:"model,omitempty"`
	FinishReason string                  `json:"finishReason,omitempty"`
	Rewritten    bool                    `json:"rewritten,omitempty"`
	Hooks        []registry.HookMetadata `json:"hooks,omitempty"`
	Sandbox      json.RawMessage         `json:"sandbox,omitempty"`
	Symbols      []SymbolDetail          `json:"symbols,omitempty"`
	Notice       *NoticeDetail           `json:"notice,omitempty"`
}

// SymbolDetail is a symbol a call touched.
type SymbolDetail struct {
	File string `json:"file"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

// NoticeDetail is a tool-UX notice attached to a call.
type NoticeDetail struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// ThoughtDetail is one finished reasoning block.
type ThoughtDetail struct {
	Text       string `json:"text"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// TodoDetail is the todo a step or task belongs to.
type TodoDetail struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	Status      string `json:"status"`
	StartedAt   int64  `json:"startedAt,omitempty"`
	CompletedAt int64  `json:"completedAt,omitempty"`
}

// RelationDetail lists node keys of related steps.
type RelationDetail struct {
	CausedBy []string `json:"causedBy,omitempty"`
	FixedBy  []string `json:"fixedBy,omitempty"`
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

func callDetail(ev registry.AuditEvent) CallDetail {
	c := CallDetail{
		CallID: ev.ToolCallID, StepID: ev.StepID, ToolName: ev.ToolName,
		Args: indentJSON(ev.Args), Error: ev.Error, ExitCode: ev.CommandExitCode,
		Model: ev.Model, FinishReason: ev.FinishReason, Rewritten: ev.Rewritten, Hooks: ev.Hooks,
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
		c.Symbols = append(c.Symbols, SymbolDetail{File: s.File, Name: s.Name, Kind: s.Kind})
	}
	if ev.Notice != nil {
		c.Notice = &NoticeDetail{Kind: ev.Notice.Kind, Text: ev.Notice.Text}
	}
	return c
}

func isDiffToolName(name string) bool { return name == "file.write_patch" || name == "patch.apply" }

func activeCallDetail(a *session.ActiveToolCall) CallDetail {
	return CallDetail{CallID: a.ToolCallID, StepID: int64(a.StepID), ToolName: a.Name, Args: indentJSON([]byte(a.Args)), Output: a.Output}
}

func unixMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func todoDetail(st *session.State, id string) *TodoDetail {
	if id == "" {
		return nil
	}
	for _, td := range st.Todos() {
		if td.ID == id {
			return &TodoDetail{ID: td.ID, Content: td.Content, Status: td.Status,
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
	return rt, src, viewmodel.Build(stackSnapshotOf(src, busy, drilled, time.Now())), nil
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
	var detail NodeDetail
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
			detail.Thinking = append(detail.Thinking, ThoughtDetail{Text: t.Text, DurationMs: t.Duration.Milliseconds()})
		}
		detail.Todo = todoDetail(state, n.Step.TodoID)
	case n.Kind == viewmodel.KindTask && n.Task != nil:
		detail.Todo = todoDetail(state, n.Task.TodoID)
	}
	if rel, ok := viewmodel.Relations(state.Transcript())[n.ID]; ok {
		detail.Relations = &RelationDetail{CausedBy: keysOf(rel.CausedBy), FixedBy: keysOf(rel.FixedBy)}
	}
	node := viewmodel.Project([]*viewmodel.Node{n}).Nodes[0]
	if parent != nil {
		node.Parent = parent.ID.Key
	}
	return map[string]any{"node": node, "detail": detail}, nil
}
