package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// THE test for this task. Change only the offset and assert the rendered
// viewport content differs. Without the hash change this fails: the
// early-return swallows the repaint and scrolling does nothing on screen.
func TestRegionScrollRepaintsViewport(t *testing.T) {
	m := newTestModel(t)
	child := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	child.BeginStreaming()
	for i := 0; i < 40; i++ {
		child.AppendThinking(fmt.Sprintf("reasoning line %d that is long enough to be distinct\n", i))
	}
	v := m.state.RegisterSubagent("reviewer", child)

	m.refreshViewport()
	before := m.viewport.GetContent()

	key := itemKey{ts: v.StartedAt, kind: session.KindSubagent}
	m.regionOffset = map[itemKey]int{key: 5}
	m.refreshViewport()
	after := m.viewport.GetContent()

	if before == after {
		t.Fatal("changing a region offset did not repaint: transcriptHash is not folding in regionOffset")
	}
}

// The hash must be stable across repeated calls with identical offsets.
// Go randomises map iteration order, so an unsorted loop makes this flaky
// rather than failing outright — run it enough times to catch that.
func TestTranscriptHashStableAcrossIdenticalOffsets(t *testing.T) {
	offsets := map[itemKey]int{}
	base := time.Now()
	for i := 0; i < 12; i++ {
		offsets[itemKey{ts: base.Add(time.Duration(i) * time.Second), kind: session.KindSubagent}] = i
	}
	first := transcriptHash(nil, 0, false, 80, nil, nil, "", session.ActiveToolCall{}, session.Notice{}, false, offsets, nil)
	for i := 0; i < 200; i++ {
		if got := transcriptHash(nil, 0, false, 80, nil, nil, "", session.ActiveToolCall{}, session.Notice{}, false, offsets, nil); got != first {
			t.Fatalf("hash unstable across identical offsets (iteration %d) — sort the keys before hashing", i)
		}
	}
}

func TestTranscriptHashChangesWithOffset(t *testing.T) {
	k := itemKey{ts: time.Now(), kind: session.KindSubagent}
	a := transcriptHash(nil, 0, false, 80, nil, nil, "", session.ActiveToolCall{}, session.Notice{}, false, map[itemKey]int{k: 0}, nil)
	b := transcriptHash(nil, 0, false, 80, nil, nil, "", session.ActiveToolCall{}, session.Notice{}, false, map[itemKey]int{k: 1}, nil)
	if a == b {
		t.Fatal("hash must change when a region offset changes")
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
	gone := itemKey{ts: time.Now().Add(-time.Hour), kind: session.KindSubagent}
	m.regionOffset = map[itemKey]int{gone: 3}
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
	key := itemKey{ts: v.StartedAt, kind: session.KindSubagent}
	m.regionOffset = map[itemKey]int{key: 0}
	for i := 0; i < 5; i++ {
		m.scrollLiveRegionAt(tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelDown})
	}
	if got := m.regionOffset[key]; got < 0 {
		t.Fatalf("offset went negative: %d", got)
	}
	_ = strings.TrimSpace("")
}

// Full-path test: a wheel event routed through Update over a live region
// must change the region's offset AND repaint the viewport, without
// scrolling the transcript underneath. This exercises the wheel→offset→
// repaint chain that TestRegionScrollRepaintsViewport only covers by
// setting the offset directly.
func TestWheelOverLiveRegionScrollsRegionAndRepaints(t *testing.T) {
	m := newTestModel(t)
	child := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	child.BeginStreaming()
	for i := 0; i < 40; i++ {
		child.AppendThinking(fmt.Sprintf("reasoning line %d that is long enough to be distinct\n", i))
	}
	v := m.state.RegisterSubagent("reviewer", child)
	m.refreshViewport()

	key := itemKey{ts: v.StartedAt, kind: session.KindSubagent}
	var region clickRegion
	found := false
	for _, r := range m.clickRegions {
		if r.target.key == key && r.target.isLiveRegion {
			region, found = r, true
		}
	}
	if !found {
		t.Fatal("expected a live-region click region for the running subagent")
	}

	// Aim the wheel at the middle of the region's content lines.
	top := m.scrollHintRows()
	y := top + region.startLine + (region.endLine-region.startLine)/2 - m.viewport.YOffset()

	before := m.viewport.GetContent()
	out, _ := m.Update(tea.MouseWheelMsg{X: 1, Y: y, Button: tea.MouseWheelUp})
	mm := asModel(t, out)

	if got := mm.regionOffset[key]; got != 1 {
		t.Fatalf("region offset = %d, want 1 after one wheel-up", got)
	}
	if after := mm.viewport.GetContent(); after == before {
		t.Fatal("scrolling a live region did not repaint the viewport")
	}
}
