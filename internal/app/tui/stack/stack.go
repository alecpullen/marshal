// Package stack turns the session transcript into a tree: turn → step → row.
//
// It holds structure only, with no styling. Grouping tool calls under the
// narration that explains them is done here, from the step IDs the runner
// stamped on each item, with a timestamp heuristic only for sessions that
// predate step IDs. The tui package renders the tree; keeping the two apart
// lets the grouping be tested without a terminal.
package stack

import (
	"fmt"
	"hash"
	"hash/fnv"
	"sort"
	"sync"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// Kind classifies a node.
type Kind int

const (
	KindTurn Kind = iota
	KindStep
	KindTool
	KindMessage
	KindFinal
	KindSubagent
	KindRunEvent
	KindJobExit
	KindThinking
	KindPassthrough
	KindTask
	KindReceipt
	KindQueue
)

// NodeID is a node's identity across rebuilds. The tree is rebuilt from the
// transcript on every refresh, so anything keyed per node (expand state,
// scroll offsets, the render cache) must survive that: keys are derived from
// IDs and timestamps that never change once an item exists.
type NodeID struct {
	Kind Kind
	Key  string
}

// Node is one element of the tree.
type Node struct {
	ID       NodeID
	Kind     Kind
	Children []*Node
	Live     bool   // re-render on every spinner tick
	Version  uint64 // hash of every payload field the renderer reads

	// Payload: exactly one is set, by Kind.
	Item    *session.TranscriptItem
	Step    *StepInfo
	Tools   []registry.AuditEvent // len > 1 = merged same-tool run within a step
	Active  *session.ActiveToolCall
	Task    *TaskInfo
	Receipt *ReceiptInfo
	Queue   *QueueInfo
}

// StepInfo is a step node's payload.
type StepInfo struct {
	Step         session.Step // zero ID for heuristic steps
	Narration    []*session.Message
	Thinking     []*session.ThinkingEntry
	LiveThinking string // in-progress reasoning when this is the live step
	Heuristic    bool

	// TodoID is the task the step renders under: the stored binding, or for a
	// narrated step whose only tool call was a todo.write, the todo that
	// write set in progress. Empty means no task.
	TodoID string
	// TodoWrites are the step's todo.write calls. They are never rows: the
	// task headers replace them.
	TodoWrites []registry.AuditEvent
}

// Snapshot is everything Build reads.
type Snapshot struct {
	Items       []session.TranscriptItem
	Steps       []session.Step
	ActiveTools []session.ActiveToolCall
	InProgress  session.InProgressMessage
	Busy        bool
	// Todos is the current task list; it names and orders the task headers.
	Todos []db.TodoItem
	// Drilled is true while showing a subagent's own transcript. Then its
	// agent.run audits render normally; otherwise the subagent card replaces
	// them.
	Drilled bool
	// RunningSubagent is true when a subagent card is rendering a running
	// child, so the parent's in-flight agent.run row is suppressed.
	RunningSubagent bool
	Now             time.Time
}

// toolCallKey scopes a call ID to its step: providers reuse IDs like call_0
// across responses, so the ID alone would give two rows one identity (and
// one expanded state, one callers lookup, one scroll offset).
func toolCallKey(step int64, id string) string {
	if step == 0 {
		return "tool:" + id
	}
	return fmt.Sprintf("tool:%d:%s", step, id)
}

// ToolKey is a tool row's node key: the step-scoped call ID when there is
// one, else the timestamp.
func ToolKey(ev registry.AuditEvent) string {
	if ev.ToolCallID != "" {
		return toolCallKey(ev.StepID, ev.ToolCallID)
	}
	return fmt.Sprintf("tool:%d", ev.Timestamp.UnixNano())
}

// ActiveToolID returns the NodeID of an in-flight call's row. It equals the
// ToolID of the audit that settles it, so expanded state carries over.
func ActiveToolID(atc session.ActiveToolCall) NodeID { return activeNode(&atc).ID }

// ThinkingKey is a thinking row's node key.
func ThinkingKey(t *session.ThinkingEntry) string {
	return fmt.Sprintf("think:%d", t.StartedAt.UnixNano())
}

// ToolID returns the NodeID of a single tool row.
func ToolID(ev registry.AuditEvent) NodeID { return NodeID{KindTool, ToolKey(ev)} }

// ThinkingID returns the NodeID of a thinking row.
func ThinkingID(t *session.ThinkingEntry) NodeID { return NodeID{KindThinking, ThinkingKey(t)} }

// AnyLive reports whether the node or any descendant needs per-tick
// re-rendering. A settled step can still hold a running subagent card or an
// in-flight call, so a cache must look below the top level.
func (n *Node) AnyLive() bool {
	if n.Live {
		return true
	}
	for _, c := range n.Children {
		if c.AnyLive() {
			return true
		}
	}
	return false
}

// LiveThinkingID is the in-progress reasoning region's identity.
var LiveThinkingID = NodeID{KindThinking, "think:live"}

// Mergeable reports whether an audit event may join a collapsed run of
// same-tool calls. Collapsing is a readability feature: the events most worth
// reading — failures, edits, hooked calls — never join a run. A
// symbol-carrying event keeps its own row because the blast-radius line is
// rendered per row.
func Mergeable(ev *registry.AuditEvent) bool {
	if ev == nil {
		return false
	}
	return !isDiffTool(ev.ToolName) && ev.Error == "" && len(ev.Hooks) == 0 && len(ev.Symbols) == 0
}

// isDiffTool is the set of tools whose result is a diff; edits always render
// their own diff row, so they never merge.
func isDiffTool(name string) bool {
	return name == "file.write_patch" || name == "patch.apply"
}

// Build groups a snapshot into turn nodes, plus a preamble turn for items
// before the first user turn.
func Build(s Snapshot) []*Node {
	items := make([]session.TranscriptItem, 0, len(s.Items))
	for _, it := range s.Items {
		if !s.Drilled && it.Kind == session.KindAudit && it.Audit != nil && it.Audit.ToolName == "agent.run" {
			continue
		}
		items = append(items, it)
	}

	stepByID := make(map[session.StepID]session.Step, len(s.Steps))
	for _, st := range s.Steps {
		stepByID[st.ID] = st
	}

	// Split into turns at user-turn messages.
	type turn struct {
		msg   *session.Message
		items []session.TranscriptItem
	}
	var turns []*turn
	cur := &turn{}
	for _, it := range items {
		if it.Kind == session.KindMessage && session.IsUserTurnMessage(it.Message) {
			if cur.msg != nil || len(cur.items) > 0 {
				turns = append(turns, cur)
			}
			cur = &turn{msg: it.Message}
		}
		cur.items = append(cur.items, it)
	}
	if cur.msg != nil || len(cur.items) > 0 {
		turns = append(turns, cur)
	}
	relocateToStepTurns(turns, stepByID, func(t *turn) (int64, *[]session.TranscriptItem) {
		if t.msg == nil {
			return 0, &t.items
		}
		return t.msg.ID, &t.items
	})

	// Live work (an in-flight call, streaming reasoning) needs a turn to hang
	// on even before the transcript has any items.
	if len(turns) == 0 {
		turns = append(turns, &turn{})
	}
	out := make([]*Node, 0, len(turns))
	for i, t := range turns {
		last := i == len(turns)-1
		key := "turn:pre"
		if t.msg != nil {
			key = fmt.Sprintf("turn:%d", t.msg.ID)
		}
		node := &Node{ID: NodeID{KindTurn, key}, Kind: KindTurn}
		var turnMsgID int64
		if t.msg != nil {
			turnMsgID = t.msg.ID
		}
		node.Children = buildTurn(t.items, stepByID, s, last, turnMsgID, key, t.msg)
		out = append(out, node)
	}
	return out
}

// itemStep is the step an item was stamped with, 0 when it carries none.
func itemStep(it session.TranscriptItem) session.StepID {
	switch {
	case it.Audit != nil:
		return it.Audit.StepID
	case it.Thinking != nil:
		return it.Thinking.StepID
	case it.Message != nil:
		return it.Message.StepID
	}
	return 0
}

// relocateToStepTurns moves every stepped item into the turn its step belongs
// to. Placing items by timestamp alone is fragile: saved timestamps lose
// sub-second precision, so on resume a step's last tool row can sort after the
// next user message and be filed under that turn, splitting the step in two.
func relocateToStepTurns[T any](turns []*T, stepByID map[session.StepID]session.Step, access func(*T) (int64, *[]session.TranscriptItem)) {
	index := map[int64]*T{}
	for _, t := range turns {
		if id, _ := access(t); id != 0 {
			index[id] = t
		}
	}
	for _, t := range turns {
		id, items := access(t)
		kept := (*items)[:0:0]
		for _, it := range *items {
			rec, ok := stepByID[itemStep(it)]
			if sid := itemStep(it); sid == 0 || !ok || rec.TurnMsgID == 0 || rec.TurnMsgID == id {
				kept = append(kept, it)
				continue
			}
			dest, ok := index[rec.TurnMsgID]
			if !ok {
				kept = append(kept, it)
				continue
			}
			_, destItems := access(dest)
			*destItems = append(*destItems, it)
		}
		*items = kept
	}
}

// block is a top-level child of a turn awaiting time ordering.
type block struct {
	ts   time.Time
	node *Node
}

type stepAcc struct {
	info   *StepInfo
	audits []registry.AuditEvent
	cards  []*Node
	first  time.Time
}

func buildTurn(items []session.TranscriptItem, stepByID map[session.StepID]session.Step, s Snapshot, lastTurn bool, turnMsgID int64, turnKey string, userMsg *session.Message) []*Node {
	var blocks []block
	steps := map[session.StepID]*stepAcc{}
	var stepOrder []session.StepID
	var heuristics []*stepAcc
	var curH *stepAcc

	idStep := func(id session.StepID) *stepAcc {
		if a, ok := steps[id]; ok {
			return a
		}
		rec := stepByID[id] // zero Step (ID 0) when the record is missing
		a := &stepAcc{info: &StepInfo{Step: rec}}
		steps[id] = a
		stepOrder = append(stepOrder, id)
		return a
	}
	newHeuristic := func(ts time.Time) *stepAcc {
		a := &stepAcc{info: &StepInfo{Heuristic: true}, first: ts}
		heuristics = append(heuristics, a)
		return a
	}
	touch := func(a *stepAcc, ts time.Time) {
		if a.first.IsZero() || (!ts.IsZero() && ts.Before(a.first)) {
			a.first = ts
		}
	}

	// A step the runner has just opened has no content yet, but it is still
	// the step its calls belong to: seed this turn's open steps so a call or
	// spinner lands in it rather than in whatever step-less item is nearby.
	// Steps that stay empty are omitted below.
	for _, st := range s.Steps {
		if st.TurnMsgID == turnMsgID && st.EndedAt.IsZero() {
			idStep(st.ID)
		}
	}

	var cards []session.TranscriptItem
	for i := range items {
		it := items[i]
		switch it.Kind {
		case session.KindMessage:
			m := it.Message
			if m != nil && m.ContentType == session.ContentTypeNarration {
				if m.StepID > 0 {
					a := idStep(m.StepID)
					a.info.Narration = append(a.info.Narration, m)
					touch(a, it.Timestamp)
				} else {
					curH = newHeuristic(it.Timestamp)
					curH.info.Narration = append(curH.info.Narration, m)
				}
				continue
			}
			curH = nil
			blocks = append(blocks, block{it.Timestamp, passthrough(&items[i])})
		case session.KindAudit:
			if it.Audit == nil {
				continue
			}
			if it.Audit.StepID > 0 {
				a := idStep(it.Audit.StepID)
				a.audits = append(a.audits, *it.Audit)
				touch(a, it.Timestamp)
			} else {
				if curH == nil {
					curH = newHeuristic(it.Timestamp)
				}
				curH.audits = append(curH.audits, *it.Audit)
			}
		case session.KindThinking:
			if it.Thinking == nil {
				continue
			}
			if it.Thinking.StepID > 0 {
				a := idStep(it.Thinking.StepID)
				a.info.Thinking = append(a.info.Thinking, it.Thinking)
				touch(a, it.Timestamp)
			} else {
				if curH == nil {
					curH = newHeuristic(it.Timestamp)
				}
				curH.info.Thinking = append(curH.info.Thinking, it.Thinking)
			}
		case session.KindSubagent:
			cards = append(cards, it)
		default:
			blocks = append(blocks, block{it.Timestamp, passthrough(&items[i])})
		}
	}

	// Subagent cards go inside the orchestrator step that was running when
	// the card began; otherwise they stand at turn level.
	for i := range cards {
		it := cards[i]
		node := passthrough(&cards[i])
		var home *stepAcc
		for _, id := range stepOrder {
			a := steps[id]
			rec := a.info.Step
			if rec.ID == 0 || rec.Actor.Role != "" {
				continue
			}
			end := rec.EndedAt
			if end.IsZero() {
				end = s.Now
				if end.IsZero() {
					end = time.Now()
				}
			}
			if !it.Timestamp.Before(rec.StartedAt) && !it.Timestamp.After(end) {
				home = a
			}
		}
		if home != nil {
			home.cards = append(home.cards, node)
		} else {
			blocks = append(blocks, block{it.Timestamp, node})
		}
	}

	// Live step: the newest open step of the last turn, while busy.
	var live *stepAcc
	if s.Busy && lastTurn {
		var newest time.Time
		for _, id := range stepOrder {
			a := steps[id]
			if a.info.Step.ID != 0 && a.info.Step.EndedAt.IsZero() && !a.info.Step.StartedAt.Before(newest) {
				live, newest = a, a.info.Step.StartedAt
			}
		}
	}
	reasoning := ""
	if lastTurn && s.InProgress.Active && s.InProgress.Reasoning != "" {
		reasoning = s.InProgress.Reasoning
	}
	liveThinkingPlaced := false
	if live != nil && reasoning != "" {
		live.info.LiveThinking = reasoning
		liveThinkingPlaced = true
	}

	// Active (in-flight) tool calls become live rows of their step.
	var strayActive []session.ActiveToolCall
	activeByStep := map[session.StepID][]session.ActiveToolCall{}
	for _, atc := range s.ActiveTools {
		if !lastTurn {
			break
		}
		if atc.Name == "agent.run" && !s.Drilled && s.RunningSubagent {
			continue
		}
		if atc.StepID > 0 && steps[atc.StepID] != nil {
			activeByStep[atc.StepID] = append(activeByStep[atc.StepID], atc)
		} else {
			// Unstamped calls go to a heuristic live step if there is one,
			// else stand at turn level.
			strayActive = append(strayActive, atc)
		}
	}

	todoActivity := false
	maxWritten := 0 // the longest list any todo.write in this turn wrote
	finish := func(a *stepAcc, isLive bool, active []session.ActiveToolCall) *Node {
		node := &Node{Kind: KindStep, Step: a.info, Live: isLive}
		if a.info.Heuristic {
			node.ID = NodeID{KindStep, fmt.Sprintf("hstep:%d", a.first.UnixNano())}
		} else if a.info.Step.ID != 0 {
			node.ID = NodeID{KindStep, fmt.Sprintf("step:%d", a.info.Step.ID)}
		} else {
			node.ID = NodeID{KindStep, fmt.Sprintf("step:orphan:%d", a.first.UnixNano())}
		}
		var audits []registry.AuditEvent
		for _, ev := range a.audits {
			if ev.ToolName == todoWriteTool {
				a.info.TodoWrites = append(a.info.TodoWrites, ev)
				todoActivity = true
				maxWritten = max(maxWritten, todoWriteLen(ev))
				continue
			}
			audits = append(audits, ev)
		}
		node.Children = toolRows(audits, active)
		node.Children = append(node.Children, a.cards...)
		a.info.TodoID = effectiveTodoID(a.info, len(audits) == 0 && len(active) == 0, s.Todos)
		node.Version = versionOfStep(node)
		return node
	}

	for _, id := range stepOrder {
		a := steps[id]
		isLive := a == live
		n := finish(a, isLive, activeByStep[id])
		if empty(n) {
			continue
		}
		ts := a.info.Step.StartedAt
		if ts.IsZero() {
			ts = a.first
		}
		blocks = append(blocks, block{ts, n})
	}
	// A heuristic live step: the last heuristic step of the last turn while
	// busy and no ID-stamped step is open.
	for i, a := range heuristics {
		isLive := live == nil && s.Busy && lastTurn && i == len(heuristics)-1
		var active []session.ActiveToolCall
		if isLive {
			active = strayActive
			strayActive = nil
			if reasoning != "" {
				a.info.LiveThinking = reasoning
				liveThinkingPlaced = true
			}
		}
		n := finish(a, isLive, active)
		if empty(n) {
			continue
		}
		blocks = append(blocks, block{a.first, n})
	}

	// Anything still unplaced renders as before, at turn level.
	if reasoning != "" && !liveThinkingPlaced {
		blocks = append(blocks, block{s.InProgress.StartedAt, &Node{
			ID: LiveThinkingID, Kind: KindThinking, Live: true,
			Step: &StepInfo{LiveThinking: reasoning, Heuristic: true},
		}})
	}
	for i := range strayActive {
		atc := strayActive[i]
		blocks = append(blocks, block{atc.StartedAt, activeNode(&atc)})
	}

	sort.SliceStable(blocks, func(i, j int) bool {
		ti, tj := blocks[i].ts, blocks[j].ts
		if ti.IsZero() || tj.IsZero() {
			return !ti.IsZero() && tj.IsZero()
		}
		return ti.Before(tj)
	})
	out := make([]*Node, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, b.node)
	}
	// Whether the turn is task-driven is settled by the lists it wrote, not
	// only by the list as it stands now: a later rewrite to one item must not
	// flatten a finished turn after the fact.
	out = groupTasks(out, turnKey, s, lastTurn, maxWritten >= MinTaskTodos)
	if lastTurn {
		out = addQueue(out, turnKey, s, todoActivity)
	}
	if !(s.Busy && lastTurn) {
		if rc := receipt(turnKey, out, userMsg, s); rc != nil {
			out = append(out, rc)
		}
	}
	return out
}

func empty(n *Node) bool {
	return n.Step != nil && len(n.Step.Narration) == 0 && len(n.Step.Thinking) == 0 &&
		len(n.Children) == 0 && n.Step.LiveThinking == ""
}

// toolRows orders a step's calls by time, merging consecutive same-tool runs,
// and appends in-flight calls as live rows.
func toolRows(audits []registry.AuditEvent, active []session.ActiveToolCall) []*Node {
	sort.SliceStable(audits, func(i, j int) bool { return audits[i].Timestamp.Before(audits[j].Timestamp) })
	done := make(map[string]bool, len(audits))
	for _, a := range audits {
		if a.ToolCallID != "" {
			done[a.ToolCallID] = true
		}
	}
	var rows []*Node
	for i := range audits {
		ev := audits[i]
		if n := len(rows); n > 0 && Mergeable(&ev) && rows[n-1].Kind == KindTool && !rows[n-1].Live &&
			Mergeable(&rows[n-1].Tools[0]) && rows[n-1].Tools[0].ToolName == ev.ToolName {
			rows[n-1].Tools = append(rows[n-1].Tools, ev)
			continue
		}
		rows = append(rows, &Node{ID: ToolID(ev), Kind: KindTool, Tools: []registry.AuditEvent{ev}})
	}
	for i := range rows {
		// A merged run keeps its first call's ID, so a row the user expanded
		// stays expanded when a second same-tool call folds into it.
		rows[i].Version = versionOfTools(rows[i].Tools)
	}
	for i := range active {
		atc := active[i]
		if atc.ToolCallID != "" && done[atc.ToolCallID] {
			continue // already finished: its audit is the row
		}
		rows = append(rows, activeNode(&atc))
	}
	return rows
}

func activeNode(atc *session.ActiveToolCall) *Node {
	key := toolCallKey(atc.StepID, atc.ToolCallID)
	if atc.ToolCallID == "" {
		key = fmt.Sprintf("active:%s:%d", atc.Name, atc.StartedAt.UnixNano())
	}
	return &Node{ID: NodeID{KindTool, key}, Kind: KindTool, Live: true, Active: atc}
}

func passthrough(it *session.TranscriptItem) *Node {
	n := &Node{Item: it, Kind: KindPassthrough}
	switch it.Kind {
	case session.KindMessage:
		n.Kind = KindMessage
		if it.Message != nil {
			n.ID = NodeID{KindMessage, fmt.Sprintf("msg:%d", it.Message.ID)}
			if it.Message.Final && it.Message.Role == session.RoleAssistant {
				n.Kind = KindFinal
			}
		}
	case session.KindSubagent:
		n.Kind = KindSubagent
		if it.Subagent != nil {
			n.ID = NodeID{KindSubagent, fmt.Sprintf("sub:%d", it.Subagent.ID)}
			n.Live = it.Subagent.Status == session.SubagentRunning
		}
	case session.KindRunEvent:
		n.Kind = KindRunEvent
		n.ID = NodeID{KindRunEvent, fmt.Sprintf("run:%d", it.Timestamp.UnixNano())}
	case session.KindJobExit:
		n.Kind = KindJobExit
		if it.JobExit != nil {
			n.ID = NodeID{KindJobExit, "job:" + it.JobExit.ID}
		}
	case session.KindThinking:
		n.Kind = KindThinking
		if it.Thinking != nil {
			n.ID = ThinkingID(it.Thinking)
		}
	case session.KindAudit:
		n.Kind = KindTool
		if it.Audit != nil {
			n.ID = ToolID(*it.Audit)
			n.Tools = []registry.AuditEvent{*it.Audit}
		}
	}
	if n.ID.Key == "" {
		n.ID = NodeID{n.Kind, fmt.Sprintf("item:%d:%d", it.Kind, it.Timestamp.UnixNano())}
	}
	n.Version = versionOfItem(it)
	return n
}

// ---- versions ----------------------------------------------------------

func newHash() *hashBuf { return &hashBuf{h: fnv.New64a()} }

type hashBuf struct{ h hash.Hash64 }

func (b *hashBuf) f(format string, a ...any) { fmt.Fprintf(b.h, format, a...) }
func (b *hashBuf) sum() uint64               { return b.h.Sum64() }

// auditDigests memoises hashAudit. An audit event is immutable once logged,
// and its payload (args, up to MaxToolResultChars of output) is the one thing
// Build would otherwise re-hash in full on every refresh. The key is the
// event's identity plus the sizes of its variable parts, so a changed event
// misses rather than serving a stale digest.
var auditDigests = struct {
	sync.Mutex
	m map[auditKey]uint64
}{m: map[auditKey]uint64{}}

type auditKey struct {
	ts                            int64
	callID, tool, role, model     string
	step                          int64
	args, content, errLen         int
	summary, head, tail           string
	hooks, symbols, files, orig   int
	sbxMS                         int64
	rewritten, hasNotice, hasExit bool
}

const auditDigestCap = 8192

// edge is up to 32 bytes from one end of s: with the length, enough to tell
// apart any two payloads that share an identity.
func edge(s string, tail bool) string {
	if len(s) <= 32 {
		return s
	}
	if tail {
		return s[len(s)-32:]
	}
	return s[:32]
}

func keyOfAudit(e *registry.AuditEvent) auditKey {
	return auditKey{
		ts: e.Timestamp.UnixNano(), callID: e.ToolCallID, tool: e.ToolName, role: e.AgentRole, model: e.Model,
		step: e.StepID, args: len(e.Args), summary: e.ResultSummary, content: len(e.ResultContent), head: edge(e.ResultContent, false), tail: edge(e.ResultContent, true), errLen: len(e.Error),
		hooks: len(e.Hooks), symbols: len(e.Symbols), files: len(e.FilesChanged), orig: len(e.OriginalArgs),
		sbxMS: e.Sandbox.DurationMS, rewritten: e.Rewritten, hasNotice: e.Notice != nil, hasExit: e.CommandExitCode != nil,
	}
}

func hashAudit(b *hashBuf, e *registry.AuditEvent) {
	k := keyOfAudit(e)
	auditDigests.Lock()
	d, ok := auditDigests.m[k]
	auditDigests.Unlock()
	if !ok {
		hb := newHash()
		hashAuditFull(hb, e)
		d = hb.sum()
		auditDigests.Lock()
		if len(auditDigests.m) >= auditDigestCap {
			auditDigests.m = map[auditKey]uint64{}
		}
		auditDigests.m[k] = d
		auditDigests.Unlock()
	}
	b.f("a|%d|", d)
}

func hashAuditFull(b *hashBuf, e *registry.AuditEvent) {
	b.f("a|%d|%s|%s|%s|%s|%s|%s|%s|%s|%s|%d|%v|%v|%s|%d|%d|%d|%v|%s|%s|%d|",
		e.Timestamp.UnixNano(), e.ToolCallID, e.ToolName, e.Args, e.Risk, e.Approval,
		e.ResultSummary, e.ResultContent, e.Error, e.AgentRole, e.StepID, e.FilesChanged,
		e.CommandExitCode != nil, e.FinishReason, e.Duration, len(e.Hooks), len(e.Symbols),
		e.Rewritten, e.OriginalArgs, e.Model, e.Sandbox.DurationMS)
	if e.CommandExitCode != nil {
		b.f("x%d|", *e.CommandExitCode)
	}
	if e.Notice != nil {
		b.f("n%v|", *e.Notice)
	}
	for _, h := range e.Hooks {
		b.f("h%v|", h)
	}
	for _, s := range e.Symbols {
		b.f("s%v|", s)
	}
	b.f("sb%v|", e.Sandbox)
}

func versionOfTools(evs []registry.AuditEvent) uint64 {
	b := newHash()
	for i := range evs {
		hashAudit(b, &evs[i])
	}
	return b.sum()
}

func versionOfItem(it *session.TranscriptItem) uint64 {
	b := newHash()
	b.f("i|%d|%d|", it.Kind, it.Timestamp.UnixNano())
	switch {
	case it.Message != nil:
		m := it.Message
		b.f("m|%d|%s|%s|%s|%s|%v|%v|%s|%d|%s|%d|", m.ID, m.Role, m.ContentType, m.Content, m.Reasoning, m.Final, m.Salvaged, m.SalvageReason, m.ThinkDuration, m.Usage, m.StepID)
	case it.Audit != nil:
		hashAudit(b, it.Audit)
	case it.Thinking != nil:
		t := it.Thinking
		b.f("t|%s|%d|%d|", t.Text, t.Duration, t.StepID)
	case it.Subagent != nil:
		v := it.Subagent
		b.f("s|%d|%s|%v|%d|%d|%d|%s|%d|%s|%s|%s|%v|%s|", v.ID, v.Label, v.Status, v.StartedAt.UnixNano(), v.EndedAt.UnixNano(),
			v.ToolCalls, v.CurrentTool, v.TokensUsed, v.Summary, v.Role, v.Model, v.Fallback, v.Provider)
	case it.RunEvent != nil:
		b.f("r|%v|", *it.RunEvent)
	case it.JobExit != nil:
		b.f("j|%v|", *it.JobExit)
	}
	return b.sum()
}

func versionOfStep(n *Node) uint64 {
	b := newHash()
	st := n.Step
	r := st.Step
	b.f("td|%s|%d|", st.TodoID, len(st.TodoWrites))
	b.f("st|%d|%d|%s|%s|%s|%s|%d|%d|%v|%d|", r.ID, r.TurnMsgID, r.Actor.Role, r.Actor.Label, r.Actor.Model, r.Actor.Provider,
		r.StartedAt.UnixNano(), r.EndedAt.UnixNano(), st.Heuristic, len(st.LiveThinking))
	for _, m := range st.Narration {
		b.f("n|%d|%s|", m.ID, m.Content)
	}
	for _, t := range st.Thinking {
		b.f("t|%d|%s|%d|", t.StartedAt.UnixNano(), t.Text, t.Duration)
	}
	for _, c := range n.Children {
		b.f("c|%s|%d|", c.ID.Key, c.Version)
	}
	return b.sum()
}
