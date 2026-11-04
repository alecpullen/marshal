package tui

import (
	"fmt"
	"hash/fnv"
	"strings"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/theme"
)

// cachedNode is a rendered top-level block. It is reusable while the node's
// payload version, the width, the model-side render inputs (sig) and the
// theme are all unchanged.
type cachedNode struct {
	version  string
	width    int
	sig      uint64
	themeSig uint64
	out      string
	subs     []subRegion
}

// nodeRenderHook is a test seam: when set it is told about every node the
// renderer actually draws (a cache miss), so tests can assert that a spinner
// tick re-renders only live nodes.
var nodeRenderHook func(nodeID)

// viewFacts is what the transcript's renderers read about the session the
// view shows, beyond its tree: the session's own, or the drilled-in child's.
type viewFacts struct {
	workdir       string // the active root a completion row elides
	routeModel    string // the route a beat's model is compared against
	routeProvider string
	reconnect     string          // the reconnect notice's label; "" when none
	sandbox       acpwire.Sandbox // a running shell row's isolation line
}

// viewFacts resolves what the transcript's renderers read about the session
// the view shows, beyond its tree: the session's own, or the drilled-in
// child's.
func (m *Model) viewFacts() viewFacts {
	st := m.sess.Status()
	var f viewFacts
	drilled, drilling := m.drilledInto()
	if !drilling {
		f.workdir = st.Workspace.ActiveRoot
		f.routeModel, f.routeProvider = st.ActiveRoute.Model, st.ActiveRoute.Provider
		if st.Activity.Kind == string(session.ActivityReconnecting) && st.Activity.Label != "" {
			f.reconnect = st.Activity.Label
		}
		// Only the root state records sandbox info, so a drilled view leaves
		// it zero instead of reporting the parent's.
		f.sandbox = st.Sandbox
		return f
	}
	// The stack holds the view as it was at drill-in; the facts follow the
	// live view each refresh, falling back to that snapshot.
	v := drilled
	for _, cur := range st.Subagents {
		if cur.ID == drilled.ID {
			v = cur
			break
		}
	}
	if v.Drill != nil {
		f.workdir = v.Drill.ActiveRoot
		f.routeModel, f.routeProvider = v.Drill.ActiveRoute.Model, v.Drill.ActiveRoute.Provider
		if v.Drill.Activity.Kind == string(session.ActivityReconnecting) && v.Drill.Activity.Label != "" {
			f.reconnect = v.Drill.Activity.Label
		}
	}
	// A drilled view reads no sandbox info: only the root state has it.
	return f
}

// invalidateTranscript forces the next refreshViewport to rebuild the
// viewport content. The render cache stays: it validates itself.
func (m *Model) invalidateTranscript() { m.lastTranscriptHash = 0 }

// refreshViewport rebuilds the transcript viewport from the session.
//
// The tree is the session's Stack: the snapshot a client outside the process
// receives. Each top-level block is rendered at most once per change and
// cached by node identity, and the viewport content is only replaced when
// something a block depends on moved. A spinner tick therefore costs
// rendering the live nodes, not the whole history.
func (m *Model) refreshViewport() {
	// A selection cannot outlive the mouse that paints it, and neither
	// can a press waiting for its release. The signature check below can
	// short-circuit the rebuild — and with it the revalidation at the
	// end — so capture lost without a content change (Ctrl+S, or a
	// config reload turning tui.mouse_capture off) is cleared here.
	if (m.sel != nil || m.press != nil) && !m.selectionEnabled() {
		m.clearSelection()
	}
	m.updateViewportHeight()

	snap := m.sess.Stack(m.drilledSheetID(), m.busy)
	m.wire = wireIndex(snap.Nodes)
	// The band is derived once, here, so the now bar and the frame height
	// both read the same value.
	m.band = bandOf(snap.Nodes)
	m.pruneDetails()
	m.completions = m.completionIndex(snap.Roots)
	m.rowBeat = m.rowBeats(snap.Roots)
	m.updateViewportHeight()

	width := m.viewport.Width()
	themeSig := themeFingerprint()
	status := m.sess.Status()
	queued := status.Steering.Queue
	notice, noticeUp := acpwire.Notice{}, false
	if status.Notice != nil {
		notice, noticeUp = *status.Notice, true
	}
	facts := m.viewFacts()
	hasTurns := m.hasConversationTurns(snap.Roots)

	// Cheap signature of everything the content depends on, taken before any
	// rendering: if it matches the last refresh, nothing needs to move.
	sig := m.contentSignature(snap.Roots, facts, width, themeSig, queued, notice, noticeUp, hasTurns)
	if sig == m.lastTranscriptHash {
		return
	}
	m.lastTranscriptHash = sig

	toolFrame := m.activeSpinnerFrame(session.ActivityTool)
	rctx := &stepRenderCtx{
		expanded:      m.expandedUnder,
		liveExpanded:  func(id nodeID) bool { return m.isToolExpanded(id, true) },
		region:        m.regionView,
		noteRows:      m.noteRegionRows,
		callers:       func(id nodeID) []string { return m.callers[id] },
		detail:        m.wantDetail,
		pointer:       workPointer(m.workSnapshot.Items),
		spinner:       m.spinnerFrame,
		toolSpinner:   toolFrame,
		thinkSpinner:  m.activeSpinnerFrame(session.ActivityThinking),
		now:           m.now(),
		routeModel:    facts.routeModel,
		routeProvider: facts.routeProvider,
		sandbox:       facts.sandbox,
		workdir:       facts.workdir,
	}

	blocks := make([]string, 0, len(m.wire)+4)
	// tight[i] joins block i to the one before it with no blank line:
	// back-to-back narration reads as one text.
	var tight []bool
	nextTight := false
	var regions []nodeRegion
	var spans []blockSpan
	lineCursor := 0
	// addBlock appends s to blocks (if non-empty) and records the
	// content-line range it occupies so a later click can find it (see
	// click.go). strings.Count is exact regardless of a block's internal
	// formatting, because it counts the same "\n" characters strings.Join
	// below will actually lay out on screen.
	addBlock := func(key blockKey, s string, target *clickTarget, subs []subRegion) {
		if s == "" {
			return
		}
		blocks = append(blocks, s)
		tight = append(tight, nextTight)
		nextTight = false
		n := strings.Count(s, "\n")
		spans = append(spans, blockSpan{key: key, start: lineCursor, end: lineCursor + n})
		if target != nil {
			regions = append(regions, nodeRegion{startLine: lineCursor, endLine: lineCursor + n, target: *target})
		}
		for _, sr := range subs {
			regions = append(regions, nodeRegion{
				startLine: lineCursor + sr.start,
				endLine:   lineCursor + sr.end,
				target:    clickTarget{node: sr.id, isLiveRegion: sr.live},
			})
		}
		lineCursor += n + 1 // +1 for the blank separator between blocks
	}

	if !hasTurns {
		addBlock(blockKey{name: "banner"}, renderWelcomeBanner(width), nil, nil)
	}
	seen := map[nodeID]bool{}
	var bitems []browseItem
	firstTurn := true
	for _, id := range snap.Roots {
		turn := m.wire[id]
		if turn == nil {
			continue
		}
		// A separator precedes every user turn but the first, so the rule
		// always reads as "a new turn starts here" rather than as a header.
		if turn.ID != "turn:pre" {
			if !firstTurn {
				addBlock(blockKey{name: "sep:" + turn.ID}, renderTurnSeparator(width), nil, nil)
			}
			firstTurn = false
		}
		turnFirst := true
		prevNarrationOnly := false
		for _, w := range m.wireChildren(turn) {
			m.collectSeen(w, seen)
			out, subs := m.renderNode(w, rctx, width, themeSig)
			if out != "" {
				isTight := prevNarrationOnly && w.Kind == acpwire.KindStep && (w.Step == nil || w.Step.Todo == nil)
				if isTight && len(blocks) > 0 {
					lineCursor-- // no blank line before a tight block
					nextTight = true
				}
				prevNarrationOnly = narrationOnly(w)
				bitems = collectBrowse(bitems, w, out, subs, lineCursor, turnFirst)
				turnFirst = false
			}
			addBlock(blockKey{node: wireID(w)}, out, m.blockTarget(w), subs)
			nextTight = false // never leaks onto a later block, even if this one was empty
		}
	}
	if facts.reconnect != "" {
		addBlock(blockKey{name: "reconnect"}, renderReconnectNotice(facts.reconnect, m.activeSpinnerFrame(session.ActivityReconnecting), width), nil, nil)
	}
	if noticeUp {
		addBlock(blockKey{name: "notice"}, renderNotice(notice, width), nil, nil)
	}
	if len(queued) > 0 {
		addBlock(blockKey{name: "queued"}, renderQueuedMessages(queued, width), nil, nil)
	}

	m.pruneRenderState(seen)
	m.nodeRegions = regions
	m.blockSpans = spans
	m.setBrowseItems(bitems)
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
	m.contentLines = strings.Split(content, "\n")
	// A drag holds the view still: following new output would move the
	// text under the pointer.
	if m.viewportFollow && !m.dragging() {
		m.viewport.GotoBottom()
	}
	m.revalidateSelection()
}

// narrationOnly is a step that said something and ran nothing: the next step
// continues it instead of starting a new paragraph.
func narrationOnly(w *acpwire.WireNode) bool {
	return w != nil && w.Kind == acpwire.KindStep && w.Step != nil && !w.Live && len(w.Step.Narration) > 0 &&
		len(w.Children) == 0 && len(w.Step.Thoughts) == 0
}

// renderNode renders one top-level block, from the cache when it is still
// valid. Nodes that are live, or hold a live descendant, are never cached: their output changes with the clock and
// the spinner.
func (m *Model) renderNode(w *acpwire.WireNode, c *stepRenderCtx, width int, themeSig uint64) (string, []subRegion) {
	if w == nil {
		return "", nil
	}
	id := wireID(w)
	sig := m.nodeSig(w, c)
	live := m.anyLive(w)
	if !live {
		if hit, ok := m.renderCache[id]; ok && hit.version == w.Version && hit.width == width && hit.sig == sig && hit.themeSig == themeSig {
			return hit.out, hit.subs
		}
	}
	if nodeRenderHook != nil {
		nodeRenderHook(id)
	}
	out, subs := m.drawNode(w, c, width)
	if !live {
		if m.renderCache == nil {
			m.renderCache = map[nodeID]cachedNode{}
		}
		m.renderCache[id] = cachedNode{version: w.Version, width: width, sig: sig, themeSig: themeSig, out: out, subs: subs}
	}
	return out, subs
}

// drawNode is the renderer proper: it dispatches on what the wire node holds.
func (m *Model) drawNode(w *acpwire.WireNode, c *stepRenderCtx, width int) (string, []subRegion) {
	if w == nil {
		return "", nil
	}
	id := wireID(w)
	switch {
	case w.Receipt != nil:
		return renderReceipt(w.Receipt, width), nil
	case w.Step != nil:
		return renderStep(w, m.wireChildren(w), c, width, m.expandAll)
	case w.ID == acpwire.LiveThinkingID && w.Thinking != nil:
		// Reasoning before any step has begun: the bounded live box on its own.
		rv := m.regionView(liveThinkingID)
		box := renderThinkingBox(w.Thinking.Text, c.thinkSpinner, c.now.Sub(acpwire.Time(w.Thinking.StartedAt)), rv, width)
		if cnt := strings.Count(box, "\n"); cnt > rv.minRows {
			m.noteRegionRows(liveThinkingID, cnt)
		}
		return box, nil
	case w.Completion != nil:
		expanded := m.expandedUnder(id, m.expandAll)
		// d is the completion's whole output when the wire cut it and the row
		// is open. Until the answer lands the report draws the wire's text.
		var d *acpwire.CompletionDetail
		if expanded && w.Completion.Truncated {
			if det := c.detailOf(w.ID, w.Version); det != nil {
				d = det.Completion
			}
		}
		return renderCompletion(w.Completion, d, expanded, width, c.workdir), nil
	case w.Tool != nil && w.Tool.Running != nil:
		return renderActiveToolCall(w.Tool, c.sandbox, c.toolSpinner, c.now, m.isToolExpanded(id, true), width), nil
	case w.Message != nil || w.Thinking != nil || w.RunEvent != nil:
		expanded := m.expandedUnder(id, m.expandAll)
		// d is the row's whole body or reasoning when the wire cut it and the
		// row is open. Until the answer lands the row draws the wire's text.
		var d *acpwire.NodeDetail
		if expanded && ((w.RunEvent != nil && w.RunEvent.Truncated) || (w.Message != nil && w.Message.Truncated)) {
			if det := c.detailOf(w.ID, w.Version); det != nil {
				d = det
			}
		}
		return renderItem(w, d, expanded, width), nil
	}
	return "", nil
}

// wireIndex keeps nodes by ID, leaving the band out. The pointers address
// nodes' slice.
func wireIndex(nodes []acpwire.WireNode) map[string]*acpwire.WireNode {
	out := make(map[string]*acpwire.WireNode, len(nodes))
	for i := range nodes {
		if nodes[i].Kind == acpwire.KindBand {
			continue
		}
		out[nodes[i].ID] = &nodes[i]
	}
	return out
}

// bandOf is the band node's payload, or the zero band when nodes hold none.
func bandOf(nodes []acpwire.WireNode) acpwire.WireBand {
	for i := range nodes {
		if nodes[i].Kind == acpwire.KindBand && nodes[i].Band != nil {
			return *nodes[i].Band
		}
	}
	return acpwire.WireBand{}
}

// wireChildren is w's children's wire nodes, in order, skipping any the index
// lacks.
func (m *Model) wireChildren(w *acpwire.WireNode) []*acpwire.WireNode {
	out := make([]*acpwire.WireNode, 0, len(w.Children))
	for _, key := range w.Children {
		if c, ok := m.wire[key]; ok {
			out = append(out, c)
		}
	}
	return out
}

// anyLive reports whether w or any node under it re-renders on every tick.
func (m *Model) anyLive(w *acpwire.WireNode) bool {
	if w.Live {
		return true
	}
	for _, c := range m.wireChildren(w) {
		if m.anyLive(c) {
			return true
		}
	}
	return false
}

// rowBeats maps every row inside a beat to that beat.
func (m *Model) rowBeats(roots []string) map[nodeID]nodeID {
	out := map[nodeID]nodeID{}
	for _, id := range roots {
		turn := m.wire[id]
		if turn == nil {
			continue
		}
		for _, n := range m.wireChildren(turn) {
			if n.Kind != acpwire.KindStep {
				continue
			}
			for _, row := range m.wireChildren(n) {
				out[wireID(row)] = wireID(n)
			}
		}
	}
	return out
}

// completionIndex indexes the completion events by WorkKey, so a held row can
// take its outcome and summary from the same event history draws.
func (m *Model) completionIndex(roots []string) map[string]completionRef {
	out := map[string]completionRef{}
	var walk func(w *acpwire.WireNode)
	walk = func(w *acpwire.WireNode) {
		if w.Kind == acpwire.KindCompletion && w.Completion != nil && w.Completion.WorkKey != "" {
			out[w.Completion.WorkKey] = completionRef{node: wireID(w), info: w.Completion}
		}
		for _, c := range m.wireChildren(w) {
			walk(c)
		}
	}
	for _, id := range roots {
		turn := m.wire[id]
		if turn == nil {
			continue
		}
		for _, w := range m.wireChildren(turn) {
			walk(w)
		}
	}
	return out
}

// hasConversationTurns reports whether the tree holds a user turn or
// assistant text, which ends the welcome banner (decision 10).
func (m *Model) hasConversationTurns(roots []string) bool {
	for _, id := range roots {
		if id != "turn:pre" {
			return true
		}
	}
	var walk func(w *acpwire.WireNode) bool
	walk = func(w *acpwire.WireNode) bool {
		switch w.Kind {
		case acpwire.KindMessage, acpwire.KindFinal:
			if w.Message != nil && w.Message.Role == string(session.RoleAssistant) {
				return true
			}
		case acpwire.KindStep:
			if w.Step != nil && len(w.Step.Narration) > 0 {
				return true
			}
		}
		for _, c := range m.wireChildren(w) {
			if walk(c) {
				return true
			}
		}
		return false
	}
	for _, id := range roots {
		turn := m.wire[id]
		if turn == nil {
			continue
		}
		for _, w := range m.wireChildren(turn) {
			if walk(w) {
				return true
			}
		}
	}
	return false
}

// blockTarget says what a click on a top-level block does. Steps, tools,
// completion events and thinking rows toggle; plain messages are not
// interactive.
func (m *Model) blockTarget(w *acpwire.WireNode) *clickTarget {
	if w == nil {
		return nil
	}
	switch {
	case w.Kind == acpwire.KindStep:
		return &clickTarget{node: wireID(w)}
	case w.ID == acpwire.LiveThinkingID:
		return &clickTarget{node: liveThinkingID, isLiveRegion: true}
	case w.Kind == acpwire.KindTool:
		return &clickTarget{node: wireID(w)}
	case w.Completion != nil:
		return &clickTarget{node: wireID(w)}
	case w.Kind == acpwire.KindThinking:
		return &clickTarget{node: wireID(w)}
	case w.Message != nil &&
		session.ContentType(w.Message.ContentType) == session.ContentTypeSkillAuto:
		return &clickTarget{node: wireID(w)}
	}
	return nil
}

func (m *Model) regionView(id nodeID) regionView {
	return regionView{offset: m.regionOffset[id], minRows: m.regionRows[id]}
}

func (m *Model) noteRegionRows(id nodeID, rows int) {
	if m.regionRows == nil {
		m.regionRows = map[nodeID]int{}
	}
	if rows > m.regionRows[id] {
		m.regionRows[id] = rows
	}
}

// collectSeen records every node ID a block can address, the block and every
// node under it.
func (m *Model) collectSeen(w *acpwire.WireNode, seen map[nodeID]bool) {
	seen[wireID(w)] = true
	for _, c := range m.wireChildren(w) {
		m.collectSeen(c, seen)
	}
}

// pruneRenderState drops per-node state for nodes no longer rendered, so a
// finished subagent's scroll offset or a rewound turn's callers do not leak.
// Pruning callers is what makes rollback correct for free: a rewound audit
// event leaves the transcript, so its blast-radius cache goes with it rather
// than re-rendering stale callers at moved lines.
func (m *Model) pruneRenderState(seen map[nodeID]bool) {
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
func (m *Model) nodeSig(w *acpwire.WireNode, c *stepRenderCtx) uint64 {
	h := fnv.New64a()
	m.foldNodeSig(h, w, c)
	return h.Sum64()
}

type sigWriter interface{ Write([]byte) (int, error) }

func (m *Model) foldNodeSig(h sigWriter, w *acpwire.WireNode, c *stepRenderCtx) {
	// The node's own choice (if any) is what varies per node; inherited
	// states come from ancestors, which this fold also covers.
	id := wireID(w)
	ov, hasOv := m.ownExpanded(id)
	fmt.Fprintf(h, "%s|%s|l%v|%v|%v|%d|%d|", w.ID, w.Version, w.Live, hasOv, ov, m.regionOffset[id], m.regionRows[id])
	fmt.Fprintf(h, "x%v|", m.expandAll)
	if w.Kind == acpwire.KindStep {
		fmt.Fprintf(h, "%s|%s|", c.routeModel, c.routeProvider)
	}
	if lines, ok := m.callers[id]; ok {
		fmt.Fprintf(h, "c%q|", lines)
	}
	if d, ok := m.details[w.ID]; ok {
		fmt.Fprintf(h, "d%q|%v|", d.version, d.failed)
	}
	for _, ch := range m.wireChildren(w) {
		m.foldNodeSig(h, ch, c)
	}
}

// contentSignature folds every block's identity, version and render inputs
// into one number. Live nodes also fold the spinner frame and the clock, so a
// running session changes the signature each tick while a settled one does
// not.
func (m *Model) contentSignature(roots []string, facts viewFacts, width int, themeSig uint64, queued []string, notice acpwire.Notice, noticeUp bool, hasTurns bool) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "w%d|t%d|x%v|", width, themeSig, m.expandAll)
	fmt.Fprintf(h, "turns%v|", hasTurns)
	c := &stepRenderCtx{routeModel: facts.routeModel, routeProvider: facts.routeProvider}
	live := false
	for _, id := range roots {
		fmt.Fprintf(h, "%s|", id)
		turn := m.wire[id]
		if turn == nil {
			continue
		}
		for _, w := range m.wireChildren(turn) {
			m.foldNodeSig(h, w, c)
			if m.anyLive(w) {
				live = true
			}
		}
	}
	// Entries for nodes that are gone must be pruned, so their count counts.
	fmt.Fprintf(h, "ro%d|rr%d|cl%d|", len(m.regionOffset), len(m.regionRows), len(m.callers))
	if live {
		fmt.Fprintf(h, "live|%s|%d|", m.spinnerFrame, m.now().Unix())
	}
	if m.browsing {
		fmt.Fprintf(h, "browse|%s|%s|", m.cursor.Kind, m.cursor.Key)
	}
	for _, q := range queued {
		fmt.Fprintf(h, "q%q|", q)
	}
	if noticeUp {
		fmt.Fprintf(h, "n%v|", notice)
	}
	if facts.reconnect != "" {
		fmt.Fprintf(h, "rc%s|%s|", facts.reconnect, m.spinnerFrame)
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
