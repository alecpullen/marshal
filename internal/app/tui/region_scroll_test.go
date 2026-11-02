package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// THE regression test for the reported bug: a running card must not resize
// when the child's activity tail flips from streamed reasoning to audit
// summaries.
//
// The card now shows exactly one activity line whatever the source, so its
// height no longer tracks how much the child has said. (A child with nothing
// to report yet is one row shorter; that is the design's "no blank padding"
// rule, not a shrink.) This guards against anyone reintroducing a multi-line
// body without also restoring the padding that made it stable.
func TestSubagentCardDoesNotResizeWhenTailSourceFlips(t *testing.T) {
	m := newTestModel(t)
	child := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	// Phase 1: the child streams several lines of reasoning.
	for i := 0; i < 10; i++ {
		child.AppendThinking(fmt.Sprintf("reasoning line %d\n", i))
	}
	m.state.RegisterSubagent("reviewer", child)
	m.refreshViewport()
	tall := strings.Count(m.viewport.GetContent(), "\n")

	// Phase 2: reasoning is cleared and the tail falls back to audit
	// summaries, a source with a different line count. The card may gain a
	// row (its metrics appear), but it must not lose one: a transcript that
	// jumps up under the reader is the bug this guards.
	child.BeginStreaming()
	child.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(200, 0), ToolName: "file.read", ResultSummary: "read a.go"})
	m.invalidateTranscript()
	m.refreshViewport()
	if got := strings.Count(m.viewport.GetContent(), "\n"); got < tall {
		t.Fatalf("transcript shrank from %d to %d rows when the tail source flipped", tall, got)
	}
}

func TestRegionRowsArePruned(t *testing.T) {
	m := newTestModel(t)
	gone := nodeID{Kind: acpwire.KindSubagent, Key: "sub:999"}
	m.regionRows = map[nodeID]int{gone: 6}
	m.refreshViewport()
	if _, still := m.regionRows[gone]; still {
		t.Fatal("high-water marks for regions no longer rendered must be pruned")
	}
}

func TestRegionScrollRepaintsViewport(t *testing.T) {
	m := newTestModel(t)
	// The in-progress thinking box: a live region with room to scroll.
	// A running subagent card no longer scrolls (one body line), so it can
	// no longer carry this test.
	m.state.BeginStreaming()
	for i := 0; i < 40; i++ {
		m.state.AppendThinking(fmt.Sprintf("reasoning line %d that is long enough to be distinct\n", i))
	}
	m.refreshViewport()
	before := m.viewport.GetContent()

	m.regionOffset = map[nodeID]int{liveThinkingID: 5}
	m.refreshViewport()
	after := m.viewport.GetContent()

	if before == after {
		t.Fatal("changing a region offset did not repaint: the content signature is not folding in regionOffset")
	}
}

// Horizontal wheel events stay swallowed even over a live region. This is
// the regression UX batch 1's UX-10 guard exists to prevent; the region
// branch must sit AFTER that guard, not before it.
func TestHorizontalWheelOverRegionStillSwallowed(t *testing.T) {
	for _, btn := range []tea.MouseButton{tea.MouseWheelLeft, tea.MouseWheelRight} {
		m := newTestModel(t)
		child := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
		child.BeginStreaming()
		child.AppendThinking("some reasoning\n")
		m.state.RegisterSubagent("reviewer", child)
		m.refreshViewport()
		before := m.viewport.XOffset()
		out, _ := m.Update(tea.MouseWheelMsg{X: 1, Y: 1, Button: btn})
		got := out.(Model)
		if got.viewport.XOffset() != before {
			t.Fatalf("%v panned the viewport horizontally: %d -> %d", btn, before, got.viewport.XOffset())
		}
	}
}

// A wheel event that is not over a region must still scroll the transcript.
func TestWheelOutsideRegionScrollsTranscript(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 200; i++ {
		m.state.AddMessage(session.RoleSystem, "filler line", session.ContentTypePlain)
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	before := m.viewport.YOffset()
	out, _ := m.Update(tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelUp})
	got := out.(Model)
	if got.viewport.YOffset() == before {
		t.Fatal("wheel outside a live region must still scroll the transcript")
	}
}

func TestStaleRegionOffsetsArePruned(t *testing.T) {
	m := newTestModel(t)
	gone := nodeID{Kind: acpwire.KindSubagent, Key: "sub:999"}
	m.regionOffset = map[nodeID]int{gone: 3}
	m.refreshViewport()
	if _, still := m.regionOffset[gone]; still {
		t.Fatal("offsets for regions no longer rendered must be pruned")
	}
}

func TestRegionOffsetNeverGoesNegative(t *testing.T) {
	m := newTestModel(t)
	child := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	child.BeginStreaming()
	child.AppendThinking("line\n")
	v := m.state.RegisterSubagent("reviewer", child)
	m.refreshViewport()
	key := nodeID{Kind: acpwire.KindSubagent, Key: fmt.Sprintf("sub:%d", v.ID)}
	m.regionOffset = map[nodeID]int{key: 0}
	for i := 0; i < 5; i++ {
		m.scrollLiveRegionAt(tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelDown})
	}
	if got := m.regionOffset[key]; got < 0 {
		t.Fatalf("offset went negative: %d", got)
	}
	_ = strings.TrimSpace("")
}

// screenRowForNode returns the screen row of a node's first visible line.
func screenRowForNode(m *Model, id nodeID) (int, bool) {
	top := m.scrollHintRows() + m.breadcrumbRows()
	for _, r := range m.nodeRegions {
		if r.target.node != id {
			continue
		}
		line := r.startLine - m.viewport.YOffset()
		if line >= 0 && line < m.viewport.Height() {
			return top + line, true
		}
	}
	return 0, false
}

// A wheel event over the in-progress reasoning region scrolls that region, not
// the transcript underneath it.
func TestWheelOverTheLiveReasoningRegionScrollsIt(t *testing.T) {
	m := newTestModel(t)
	m.state.BeginStreaming()
	for i := 0; i < 40; i++ {
		m.state.AppendThinking(fmt.Sprintf("reasoning line %d that is long enough to be distinct\n", i))
	}
	m.refreshViewport()

	y, ok := screenRowForNode(&m, liveThinkingID)
	if !ok {
		t.Fatalf("the live reasoning region is not on screen:\n%s", stripANSI(m.viewport.GetContent()))
	}
	before := m.regionOffset[liveThinkingID]
	out, _ := m.Update(tea.MouseWheelMsg{X: 1, Y: y, Button: tea.MouseWheelUp})
	got := out.(Model)
	if got.regionOffset[liveThinkingID] == before {
		t.Fatalf("the wheel did not scroll the live region (offset %d)", before)
	}
}

// A click on a running call row expands it through the real mouse path, not by
// calling toggleExpanded directly.
func TestClickOnARunningCallRowExpandsIt(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Running the suite.")
	m.state.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StartedAt: time.Now(), StepID: id, ToolCallID: "run"})
	m.state.AppendActiveToolCallOutput("run", "PASS pkg/first\nPASS pkg/b\nPASS pkg/c\nPASS pkg/d\nPASS pkg/e\nPASS pkg/f\nPASS pkg/g\nPASS pkg/h\nPASS pkg/last")
	m.busy, m.turnStartedAt = true, time.Now()
	m.expandAll = true // ctrl+g on: settled rows open, running rows stay closed
	m.invalidateTranscript()
	m.refreshViewport()

	row := nodeID{Kind: acpwire.KindTool, Key: fmt.Sprintf("tool:%d:run", id)}
	y, ok := screenRowForNode(&m, row)
	if !ok {
		t.Fatalf("the running call row is not on screen:\n%s", stripANSI(m.viewport.GetContent()))
	}
	if strings.Contains(stripANSI(m.viewport.GetContent()), "PASS pkg/first") {
		t.Fatal("a running call starts collapsed")
	}
	m = sendMsg(m, tea.MouseClickMsg{X: 2, Y: y, Button: tea.MouseLeft})
	m = sendMsg(m, tea.MouseReleaseMsg{X: 2, Y: y, Button: tea.MouseLeft})
	if !strings.Contains(stripANSI(m.viewport.GetContent()), "PASS pkg/first") {
		t.Fatalf("a click on the running row must expand its output tail:\n%s", stripANSI(m.viewport.GetContent()))
	}
}
