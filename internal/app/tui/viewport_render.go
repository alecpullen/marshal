package tui

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/stack"
	"marshal/internal/app/tui/theme"
)

// cachedNode is a rendered top-level block. It is reusable while the node's
// payload version, the width, the model-side render inputs (sig) and the
// theme are all unchanged.
type cachedNode struct {
	version  uint64
	width    int
	sig      uint64
	themeSig uint64
	out      string
	subs     []subRegion
}

// nodeRenderHook is a test seam: when set it is told about every node the
// renderer actually draws (a cache miss), so tests can assert that a spinner
// tick re-renders only live nodes.
var nodeRenderHook func(stack.NodeID)

// invalidateTranscript forces the next refreshViewport to rebuild the
// viewport content. The render cache stays: it validates itself.
func (m *Model) invalidateTranscript() { m.lastTranscriptHash = 0 }

// transcriptSource resolves which session the transcript shows: the drilled
// subagent's child while drilling into a real child, the orchestrator's
// otherwise. The parent transcript is left untouched so popping back restores
// it as-is.
func (m *Model) transcriptSource() (state *session.State, drilling bool) {
	state = m.state
	drilled, ok := m.drilledInto()
	if ok && drilled.Child != nil {
		return drilled.Child, true
	}
	return state, false
}

// refreshViewport rebuilds the transcript viewport from the session.
//
// The transcript becomes a tree (stack.Build), each top-level block is
// rendered at most once per change and cached by node identity, and the
// viewport content is only replaced when something a block depends on moved.
// A spinner tick therefore costs rendering the live nodes, not the whole
// history.
func (m *Model) refreshViewport() {
	m.updateViewportHeight()
	transcriptState, drilling := m.transcriptSource()

	items := transcriptState.Transcript()
	inProgress := transcriptState.InProgress()
	active := transcriptState.ActiveToolCalls()
	snap := stack.Snapshot{
		Items:           items,
		Steps:           transcriptState.Steps(),
		Todos:           transcriptState.Todos(),
		ActiveTools:     active,
		InProgress:      inProgress,
		Busy:            m.busy || len(active) > 0 || len(inProgress.Reasoning) > 0,
		Drilled:         drilling,
		RunningSubagent: m.state.HasRunningSubagent(),
		Now:             m.now(),
	}
	turns := stack.Build(snap)
	m.todoStrip = nil
	if len(turns) > 0 && stack.DrivenByTodos(turns[len(turns)-1]) {
		m.todoStrip = snap.Todos
	}
	m.updateViewportHeight()

	width := m.viewport.Width()
	themeSig := themeFingerprint()
	queued := m.state.SteeringQueue()
	notice, noticeUp := m.state.Notice()
	reconnect := ""
	if act := transcriptState.Activity(); act.Kind == session.ActivityReconnecting && act.Label != "" {
		reconnect = act.Label
	}

	// Cheap signature of everything the content depends on, taken before any
	// rendering: if it matches the last refresh, nothing needs to move.
	sig := m.contentSignature(turns, width, themeSig, queued, notice, noticeUp, reconnect, hasConversationTurns(items))
	if sig == m.lastTranscriptHash {
		return
	}
	m.lastTranscriptHash = sig

	toolFrame := m.activeSpinnerFrame(session.ActivityTool)
	rctx := &stepRenderCtx{
		density:       m.densityOf,
		record:        m.recordDensity,
		foldTasks:     m.foldTasks,
		hasOverride:   func(id stack.NodeID) bool { _, ok := m.override(id); return ok },
		liveExpanded:  func(id stack.NodeID) bool { return m.isToolExpanded(id, true) },
		region:        m.regionView,
		noteRows:      m.noteRegionRows,
		callers:       func(id stack.NodeID) []string { return m.callers[id] },
		spinner:       m.spinnerFrame,
		toolSpinner:   toolFrame,
		thinkSpinner:  m.activeSpinnerFrame(session.ActivityThinking),
		thinkElapsed:  m.now().Sub(inProgress.StartedAt),
		now:           m.now(),
		routeModel:    transcriptState.ActiveRoute().Model,
		routeProvider: transcriptState.ActiveRoute().Provider,
		sandbox:       transcriptState.SandboxInfo(),
		allowNetwork:  transcriptState.Config.Tools.Shell.AllowNetwork,
	}

	blocks := make([]string, 0, len(items)+4)
	// tight[i] joins block i to the one before it with no blank line: the
	// folded rows of a todo stack read as one list, and back-to-back
	// narration reads as one text.
	var tight []bool
	nextTight := false
	var regions []nodeRegion
	lineCursor := 0
	// addBlock appends s to blocks (if non-empty) and records the
	// content-line range it occupies so a later click can find it (see
	// click.go). strings.Count is exact regardless of a block's internal
	// formatting, because it counts the same "\n" characters strings.Join
	// below will actually lay out on screen.
	addBlock := func(s string, target *clickTarget, subs []subRegion) {
		if s == "" {
			return
		}
		blocks = append(blocks, s)
		tight = append(tight, nextTight)
		nextTight = false
		n := strings.Count(s, "\n")
		if target != nil {
			regions = append(regions, nodeRegion{startLine: lineCursor, endLine: lineCursor + n, target: *target})
		}
		for _, sr := range subs {
			regions = append(regions, nodeRegion{
				startLine: lineCursor + sr.start,
				endLine:   lineCursor + sr.end,
				target:    clickTarget{node: sr.id, subagent: sr.subagent, isLiveRegion: sr.live},
			})
		}
		lineCursor += n + 1 // +1 for the blank separator between blocks
	}

	if !hasConversationTurns(items) {
		addBlock(renderWelcomeBanner(width), nil, nil)
	}
	seen := map[stack.NodeID]bool{}
	tree := map[stack.NodeID]*stack.Node{}
	var bitems []browseItem
	firstTurn := true
	for _, turn := range turns {
		// A separator precedes every user turn but the first, so the rule
		// always reads as "a new turn starts here" rather than as a header.
		if turn.ID.Key != "turn:pre" {
			if !firstTurn {
				addBlock(renderTurnSeparator(width), nil, nil)
			}
			firstTurn = false
		}
		turnFirst := true
		prevFolded, prevNarrationOnly := false, false
		for _, node := range turn.Children {
			collectSeen(node, seen)
			indexTree(node, tree)
			out, subs := m.renderNode(node, rctx, width, themeSig)
			if out != "" {
				folded := node.Kind == stack.KindTask && rctx.taskFolded(node)
				isTight := (prevFolded && (node.Kind == stack.KindTask || node.Kind == stack.KindQueue)) ||
					(prevNarrationOnly && node.Kind == stack.KindStep)
				if isTight && len(blocks) > 0 {
					lineCursor-- // no blank line before a tight block
					nextTight = true
				}
				prevFolded, prevNarrationOnly = folded, narrationOnly(node)
				bitems = collectBrowse(bitems, node, out, subs, lineCursor, turnFirst)
				turnFirst = false
			}
			addBlock(out, m.blockTarget(node), subs)
			nextTight = false // never leaks onto a later block, even if this one was empty
		}
	}
	if reconnect != "" {
		addBlock(renderReconnectNotice(reconnect, m.activeSpinnerFrame(session.ActivityReconnecting), width), nil, nil)
	}
	if noticeUp {
		addBlock(renderNotice(notice, width), nil, nil)
	}
	if len(queued) > 0 {
		addBlock(renderQueuedMessages(queued, width), nil, nil)
	}

	m.pruneRenderState(seen)
	m.nodeRegions = regions
	m.setBrowseItems(bitems, tree)
	m.taskStats = countTaskStats(turns)
	// Every block ends with exactly one newline; separation between blocks
	// is the caller's job — one blank line, none within a block.
	var sb strings.Builder
	for i, blk := range blocks {
		if i > 0 && !tight[i] {
			sb.WriteString("\n")
		}
		sb.WriteString(blk)
	}
	content := sb.String()
	if m.browsing {
		content = m.paintCursor(content)
	}
	m.viewport.SetContent(content)
	if m.viewportFollow {
		m.viewport.GotoBottom()
	}
}

// narrationOnly is a step that said something and ran nothing: the next step
// continues it instead of starting a new paragraph.
func narrationOnly(n *stack.Node) bool {
	return n.Kind == stack.KindStep && n.Step != nil && !n.Live && len(n.Step.Narration) > 0 &&
		len(n.Children) == 0 && len(n.Step.Thinking) == 0 && n.Step.LiveThinking == ""
}

// renderNode renders one top-level block, from the cache when it is still
// valid. Nodes that are live, or hold a live descendant, are never cached: their output changes with the clock and
// the spinner.
func (m *Model) renderNode(n *stack.Node, c *stepRenderCtx, width int, themeSig uint64) (string, []subRegion) {
	sig := m.nodeSig(n, c)
	live := n.AnyLive()
	if !live {
		if hit, ok := m.renderCache[n.ID]; ok && hit.version == n.Version && hit.width == width && hit.sig == sig && hit.themeSig == themeSig {
			return hit.out, hit.subs
		}
	}
	if nodeRenderHook != nil {
		nodeRenderHook(n.ID)
	}
	out, subs := m.drawNode(n, c, width)
	if !live {
		if m.renderCache == nil {
			m.renderCache = map[stack.NodeID]cachedNode{}
		}
		m.renderCache[n.ID] = cachedNode{version: n.Version, width: width, sig: sig, themeSig: themeSig, out: out, subs: subs}
	}
	return out, subs
}

// drawNode is the renderer proper: it dispatches on what the node holds.
func (m *Model) drawNode(n *stack.Node, c *stepRenderCtx, width int) (string, []subRegion) {
	switch {
	case n.Kind == stack.KindTask && n.Task != nil:
		return renderTask(n, c, width, m.density)
	case n.Kind == stack.KindReceipt && n.Receipt != nil:
		return renderReceipt(n.Receipt, width), nil
	case n.Kind == stack.KindQueue:
		// The waiting todos live in the pinned strip above the transcript.
		return "", nil
	case n.Kind == stack.KindStep && n.Step != nil:
		return renderStep(n, c, width, m.density)
	case n.Kind == stack.KindThinking && n.Step != nil:
		// Reasoning before any step has begun: the bounded live box on its own.
		rv := m.regionView(stack.LiveThinkingID)
		box := renderThinkingBox(n.Step.LiveThinking, c.thinkSpinner, c.thinkElapsed, rv, width)
		if cnt := strings.Count(box, "\n"); cnt > rv.minRows {
			m.noteRegionRows(stack.LiveThinkingID, cnt)
		}
		return box, nil
	case n.Kind == stack.KindTool && n.Active != nil:
		return renderActiveToolCall(*n.Active, c.sandbox, c.allowNetwork, c.toolSpinner, c.now, m.isToolExpanded(n.ID, true), width), nil
	case n.Item != nil:
		rv := m.regionView(n.ID)
		d := m.densityOf(n.ID, m.density)
		m.recordDensity(n.ID, d)
		out := renderTranscriptItem(*n.Item, d == densityFull, m.spinnerFrame, rv, m.callers[n.ID], width)
		// Record the tallest this region has been, so a later shrink in the
		// child's activity tail cannot shrink the card.
		if n.Kind == stack.KindSubagent {
			if cnt := strings.Count(out, "\n"); cnt > rv.minRows {
				m.noteRegionRows(n.ID, cnt)
			}
		}
		return out, nil
	}
	return "", nil
}

// blockTarget says what a click on a top-level block does. Steps and
// expandable items toggle; a subagent card with a child drills in; plain
// messages are not interactive.
func (m *Model) blockTarget(n *stack.Node) *clickTarget {
	switch {
	case n.Kind == stack.KindStep, n.Kind == stack.KindTask:
		return &clickTarget{node: n.ID}
	case n.Kind == stack.KindThinking && n.Step != nil:
		return &clickTarget{node: stack.LiveThinkingID, isLiveRegion: true}
	case n.Kind == stack.KindTool:
		return &clickTarget{node: n.ID}
	case n.Kind == stack.KindThinking:
		return &clickTarget{node: n.ID}
	case n.Kind == stack.KindSubagent && n.Item != nil && n.Item.Subagent != nil && n.Item.Subagent.Child != nil:
		return &clickTarget{node: n.ID, subagent: n.Item.Subagent, isLiveRegion: n.Item.Subagent.Status == session.SubagentRunning}
	case n.Kind == stack.KindMessage && n.Item != nil && n.Item.Message != nil &&
		n.Item.Message.ContentType == session.ContentTypeSkillAuto:
		return &clickTarget{node: n.ID}
	}
	return nil
}

func (m *Model) regionView(id stack.NodeID) regionView {
	return regionView{offset: m.regionOffset[id], minRows: m.regionRows[id]}
}

func (m *Model) noteRegionRows(id stack.NodeID, rows int) {
	if m.regionRows == nil {
		m.regionRows = map[stack.NodeID]int{}
	}
	if rows > m.regionRows[id] {
		m.regionRows[id] = rows
	}
}

// taskStat is what the Tasks panel shows per todo, taken from the same task
// nodes as the transcript headers so the two always agree.
type taskStat struct {
	steps int
	work  time.Duration
}

// countTaskStats sums the steps and working time under each task header, per
// todo ID (a task split into segments adds up).
func countTaskStats(turns []*stack.Node) map[string]taskStat {
	stats := map[string]taskStat{}
	for _, turn := range turns {
		for _, n := range turn.Children {
			if n.Kind == stack.KindTask && n.Task != nil {
				st := stats[n.Task.TodoID]
				st.steps += n.Task.Steps
				st.work += n.Task.Work
				stats[n.Task.TodoID] = st
			}
		}
	}
	return stats
}

// collectSeen records every node ID a block can address: the block, its rows,
// and a step's thinking rows.
func collectSeen(n *stack.Node, seen map[stack.NodeID]bool) {
	seen[n.ID] = true
	if n.Step != nil {
		for _, t := range n.Step.Thinking {
			seen[stack.ThinkingID(t)] = true
		}
		if n.Step.LiveThinking != "" {
			seen[stack.LiveThinkingID] = true
		}
	}
	for _, ev := range n.Tools {
		seen[stack.ToolID(ev)] = true
	}
	for _, ch := range n.Children {
		collectSeen(ch, seen)
	}
}

// pruneRenderState drops per-node state for nodes no longer rendered, so a
// finished subagent's scroll offset or a rewound turn's callers do not leak.
// Pruning callers is what makes rollback correct for free: a rewound audit
// event leaves the transcript, so its blast-radius cache goes with it rather
// than re-rendering stale callers at moved lines.
func (m *Model) pruneRenderState(seen map[stack.NodeID]bool) {
	for k := range m.regionOffset {
		if !seen[k] {
			delete(m.regionOffset, k)
		}
	}
	for k := range m.regionRows {
		if !seen[k] {
			delete(m.regionRows, k)
		}
	}
	for k := range m.callers {
		if !seen[k] {
			delete(m.callers, k)
			delete(m.callersAsked, k)
		}
	}
	for k := range m.renderCache {
		if !seen[k] {
			delete(m.renderCache, k)
		}
	}
}

// nodeSig hashes the model-side inputs a node's rendering depends on beyond
// its payload: expand overrides, caller lines, scroll offsets and high-water
// marks, and the active route a step's meta compares against.
func (m *Model) nodeSig(n *stack.Node, c *stepRenderCtx) uint64 {
	h := fnv.New64a()
	m.foldNodeSig(h, n, c)
	return h.Sum64()
}

type sigWriter interface{ Write([]byte) (int, error) }

func (m *Model) foldNodeSig(h sigWriter, n *stack.Node, c *stepRenderCtx) {
	// The node's own override (if any) is what varies per node; inherited
	// levels come from ancestors, which this fold also covers.
	ov, hasOv := m.override(n.ID)
	fmt.Fprintf(h, "%s|%v|%d|%d|%d|", n.ID.Key, hasOv, ov, m.regionOffset[n.ID], m.regionRows[n.ID])
	fmt.Fprintf(h, "d%d|", m.density)
	if n.Kind == stack.KindStep || n.Kind == stack.KindTask {
		fmt.Fprintf(h, "%s|%s|%v|", c.routeModel, c.routeProvider, m.foldTasks)
	}
	if lines, ok := m.callers[n.ID]; ok {
		fmt.Fprintf(h, "c%q|", lines)
	}
	if n.Step != nil {
		for _, t := range n.Step.Thinking {
			tov, thas := m.override(stack.ThinkingID(t))
			fmt.Fprintf(h, "t%v|%d|", thas, tov)
		}
	}
	for _, ch := range n.Children {
		m.foldNodeSig(h, ch, c)
	}
}

// contentSignature folds every block's identity, version and render inputs
// into one number. Live nodes also fold the spinner frame and the clock, so a
// running session changes the signature each tick while a settled one does
// not.
func (m *Model) contentSignature(turns []*stack.Node, width int, themeSig uint64, queued []string, notice session.Notice, noticeUp bool, reconnect string, hasTurns bool) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "w%d|t%d|g%d|f%v|", width, themeSig, m.density, m.foldTasks)
	fmt.Fprintf(h, "turns%v|", hasTurns)
	c := &stepRenderCtx{}
	if st, _ := m.transcriptSource(); st != nil {
		c.routeModel, c.routeProvider = st.ActiveRoute().Model, st.ActiveRoute().Provider
	}
	live := false
	var fold func(n *stack.Node)
	fold = func(n *stack.Node) {
		fmt.Fprintf(h, "%s|%d|", n.ID.Key, n.Version)
		if n.Live {
			live = true
		}
		for _, ch := range n.Children {
			fold(ch)
		}
	}
	for _, t := range turns {
		fmt.Fprintf(h, "%s|", t.ID.Key)
		for _, n := range t.Children {
			m.foldNodeSig(h, n, c)
			fold(n)
		}
	}
	// Entries for nodes that are gone must be pruned, so their count counts.
	fmt.Fprintf(h, "ro%d|rr%d|cl%d|", len(m.regionOffset), len(m.regionRows), len(m.callers))
	if live {
		fmt.Fprintf(h, "live|%s|%d|", m.spinnerFrame, m.now().Unix())
	}
	if m.browsing {
		fmt.Fprintf(h, "browse|%d|%s|", m.cursor.Kind, m.cursor.Key)
	}
	for _, q := range queued {
		fmt.Fprintf(h, "q%q|", q)
	}
	if noticeUp {
		fmt.Fprintf(h, "n%v|", notice)
	}
	if reconnect != "" {
		fmt.Fprintf(h, "rc%s|%s|", reconnect, m.spinnerFrame)
	}
	return h.Sum64()
}

// themeFingerprint identifies the active theme and depth, so a palette or
// depth change invalidates the render cache without anyone having to clear it.
func themeFingerprint() uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%v", theme.Current())
	return h.Sum64()
}
