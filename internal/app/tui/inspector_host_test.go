package tui

import (
	"testing"

	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/inspector"
)

// TestNewInspectorHostIsClosed pins the default: the inspector is not on
// screen until something opens it, so a session starts with the conversation
// unencumbered.
func TestNewInspectorHostIsClosed(t *testing.T) {
	h := newInspectorHost()
	if h.isOpen() {
		t.Fatal("a fresh inspector host reports open")
	}
	if h.placement() != inspectorClosed {
		t.Fatalf("placement = %v, want closed", h.placement())
	}
	if h.model == nil || h.adapter == nil {
		t.Fatal("a fresh host has no model/adapter")
	}
}

// TestExplicitOpenWorksBelowTheThreshold is one of the plan's explicit rules:
// an explicit open must work even when the side rail cannot be shown, because
// a user who asked for the inspector did not ask for the rail. It falls back
// to the dock rather than refusing.
func TestExplicitOpenWorksBelowTheThreshold(t *testing.T) {
	const sideAvailable = false

	h := newInspectorHost()
	if !h.open(inspector.TabOverview, sideAvailable) {
		t.Fatal("open() refused when the side rail was unavailable")
	}
	if !h.isOpen() {
		t.Fatal("the inspector did not open below the threshold")
	}
	if h.placement() != inspectorDock {
		t.Fatalf("placement = %v, want dock when the side is unavailable", h.placement())
	}
}

// TestExplicitOpenUsesTheSideWhenAvailable pins the preferred placement: with
// room for a second column the inspector sits beside the conversation, so the
// reader keeps the draft and the transcript in view.
func TestExplicitOpenUsesTheSideWhenAvailable(t *testing.T) {
	h := newInspectorHost()
	h.open(inspector.TabOverview, true)
	if h.placement() != inspectorSide {
		t.Fatalf("placement = %v, want side", h.placement())
	}
}

// TestOpenSelectsTheRequestedTabOnlyWhenVisible pins that /inspect cannot open
// a tab that has no implementation yet. An empty panel is indistinguishable
// from a broken one, so the tab has to stay unselectable until it is real.
func TestOpenSelectsTheRequestedTabOnlyWhenVisible(t *testing.T) {
	h := newInspectorHost()
	h.open(inspector.TabOverview, true)
	if got := h.model.SelectedTab(); got != inspector.TabOverview {
		t.Fatalf("selected tab = %q, want overview", got)
	}

	// TabChanges is part of the product set but not yet populated, so
	// opening it must not switch to it.
	h.open(inspector.TabChanges, true)
	if got := h.model.SelectedTab(); got == inspector.TabChanges {
		t.Fatalf("opened the unimplemented tab %q", got)
	}
	for _, visible := range inspector.VisibleTabs() {
		if visible == inspector.TabChanges {
			t.Fatal("TabChanges is advertised as visible; it has no implementation yet")
		}
	}
}

// TestToggleOpensThenCloses pins the Ctrl+B cycle.
func TestToggleOpensThenCloses(t *testing.T) {
	h := newInspectorHost()

	h.toggle(true)
	if !h.isOpen() {
		t.Fatal("first toggle did not open the inspector")
	}
	h.toggle(true)
	if h.isOpen() {
		t.Fatal("second toggle did not close the inspector")
	}
}

// TestToggleFromSuspendedRestoresInsteadOfReopening pins the recovery rule: a
// user whose inspector was suspended by a competing panel presses Ctrl+B to
// get it back, and that must restore what they had open — not reset it to the
// default tab with the stack discarded.
func TestToggleFromSuspendedRestoresInsteadOfReopening(t *testing.T) {
	h := newInspectorHost()
	h.open(inspector.TabOverview, true)
	h.model.SetState(inspector.TabOverview, inspector.TabState{Scroll: 7})
	h.suspend()

	// Suspension is "open but not rendering", not "closed": the user still
	// has their inspector and Ctrl+B must give it back rather than start a
	// fresh one.
	if h.isSuspended() != true {
		t.Fatal("suspend() did not record a suspension")
	}
	if h.isRendering() {
		t.Fatal("a suspended inspector is still rendering")
	}
	if !h.isOpen() {
		t.Fatal("suspend() reported the inspector closed; it is the user's inspector, borrowed not dismissed")
	}

	h.toggle(true)
	if !h.isOpen() {
		t.Fatal("toggling from suspended did not restore the inspector")
	}
	if h.isSuspended() {
		t.Fatal("the suspension outlived its restore")
	}
	if got := h.model.State(inspector.TabOverview).Scroll; got != 7 {
		t.Fatalf("scroll = %d after restore, want 7: the suspension lost state", got)
	}
}

// TestCloseIsNotASuspension pins the difference between the two: closing is
// the user saying "go away", so the next open starts fresh rather than
// resurrecting a stack they dismissed.
func TestCloseIsNotASuspension(t *testing.T) {
	h := newInspectorHost()
	h.open(inspector.TabOverview, true)
	h.model.SetState(inspector.TabOverview, inspector.TabState{Scroll: 5})
	h.close()

	if h.isOpen() || h.isSuspended() {
		t.Fatalf("close() left the host open=%v suspended=%v", h.isOpen(), h.isSuspended())
	}

	h.open(inspector.TabOverview, true)
	if got := h.model.State(inspector.TabOverview).Scroll; got != 5 {
		t.Fatalf("scroll = %d, want the per-tab state to survive a close/reopen (it is navigation state, not a request)", got)
	}
	if h.model.Depth() != 0 {
		t.Fatalf("detail depth = %d after reopening, want the dismissed stack gone", h.model.Depth())
	}
}

// TestSuspensionPreservesTabScrollAndStack is the state-continuity rule: a
// modal panel taking the dock must not cost the inspector its place, because
// the suspension is the app's doing, not the user's.
func TestSuspensionPreservesTabScrollAndStack(t *testing.T) {
	h := newInspectorHost()
	h.open(inspector.TabOverview, true)
	h.model.SetState(inspector.TabOverview, inspector.TabState{Cursor: 3, Filter: "go", Scroll: 11})
	h.model.OpenTarget(inspector.Target{
		Kind: inspector.TargetChangedFile, Scope: "s1", ID: "internal/app/tui/view.go",
	})

	before := h.model.SelectedTab()
	depth := h.model.Depth()
	h.suspend()
	h.restore()

	if h.model.SelectedTab() != before {
		t.Fatalf("tab = %q after restore, want %q", h.model.SelectedTab(), before)
	}
	if got := h.model.State(inspector.TabOverview); got.Cursor != 3 || got.Filter != "go" || got.Scroll != 11 {
		t.Fatalf("state = %+v after restore, want the saved cursor/filter/scroll", got)
	}
	if h.model.Depth() != depth {
		t.Fatalf("detail depth = %d after restore, want %d", h.model.Depth(), depth)
	}
}

// TestBodyExpandedReplacesOnlyTheBody pins the body-expanded contract: it must
// not hide the composer. A mode that takes the draft away to show a read-only
// panel is a mode that loses work.
func TestBodyExpandedReplacesOnlyTheBody(t *testing.T) {
	h := newInspectorHost()
	h.open(inspector.TabOverview, true)

	h.expandBody()
	if h.placement() != inspectorBodyExpanded {
		t.Fatalf("placement = %v, want body-expanded", h.placement())
	}
	if !h.replacesBodyOnly() {
		t.Fatal("body-expanded does not report that it replaces only the body")
	}
	// The mechanism behind "the draft stays visible" is that the inspector
	// never asks the dock for FullFrame, which is the mode that hides the
	// composer. Asserting the mechanism rather than a hand-written constant
	// means this test fails if someone makes the adapter request FullFrame,
	// which is the change that would actually break the promise.
	if h.adapter.Sizing() == dock.FullFrame {
		t.Fatal("the inspector requests the dock's FullFrame mode, which hides the composer")
	}

	// Esc leaves body-expanded and restores the conversation placement, per
	// the plan, without closing the inspector.
	if !h.leaveBodyExpanded() {
		t.Fatal("leaveBodyExpanded() reported nothing to do")
	}
	if h.placement() != inspectorSide {
		t.Fatalf("placement = %v after Esc, want the side placement restored", h.placement())
	}
	if !h.isOpen() {
		t.Fatal("leaving body-expanded closed the inspector")
	}
	if h.leaveBodyExpanded() {
		t.Fatal("leaveBodyExpanded() reported work when not body-expanded")
	}
}

// TestEscFallsThroughWhenThereIsNoDetail pins the escape contract the plan
// relies on: the inspector consumes Esc only when it has something to back out
// of. Swallowing it unconditionally would take Esc away from the composer's
// own meanings.
func TestEscFallsThroughWhenThereIsNoDetail(t *testing.T) {
	h := newInspectorHost()
	h.open(inspector.TabOverview, true)

	if h.esc() {
		t.Fatal("esc() consumed the key at the root with no detail open")
	}

	h.model.OpenTarget(inspector.Target{
		Kind: inspector.TargetChangedFile, Scope: "s1", ID: "x.go",
	})
	if !h.esc() {
		t.Fatal("esc() did not back out of an open detail")
	}
	if h.model.Depth() != 0 {
		t.Fatalf("depth = %d after esc, want 0", h.model.Depth())
	}
}

// TestResizeAcrossSizesKeepsTheInspectorReachable is the plan's headline
// continuity test, driven through the host: the same inspector state must be
// reachable at every size without losing what the user had open.
func TestResizeAcrossSizesKeepsTheInspectorReachable(t *testing.T) {
	sizes := [][2]int{{80, 24}, {120, 40}, {200, 60}}
	h := newInspectorHost()
	h.open(inspector.TabOverview, true)
	h.model.SetState(inspector.TabOverview, inspector.TabState{Scroll: 4, Filter: "main"})
	h.model.OpenTarget(inspector.Target{
		Kind: inspector.TargetChangedFile, Scope: "s1", ID: "internal/app/tui/model.go",
	})
	wantTab := h.model.SelectedTab()
	wantTarget, _ := h.model.ActiveTarget()
	wantDepth := h.model.Depth()

	for i := 0; i < 20; i++ {
		w, ht := sizes[i%len(sizes)][0], sizes[i%len(sizes)][1]
		h.model.Resize(w, ht)
		h.resize(w, ht)

		if h.model.SelectedTab() != wantTab {
			t.Fatalf("iteration %d at %dx%d: tab = %q, want %q", i, w, ht, h.model.SelectedTab(), wantTab)
		}
		if got := h.model.State(inspector.TabOverview); got.Scroll != 4 || got.Filter != "main" {
			t.Fatalf("iteration %d at %dx%d: state = %+v, want scroll 4 filter %q", i, w, ht, got, "main")
		}
		if got, ok := h.model.ActiveTarget(); !ok || got != wantTarget {
			t.Fatalf("iteration %d at %dx%d: active target = %+v/%v, want %+v", i, w, ht, got, ok, wantTarget)
		}
		if h.model.Depth() != wantDepth {
			t.Fatalf("iteration %d at %dx%d: depth = %d, want %d", i, w, ht, h.model.Depth(), wantDepth)
		}
		if !h.isOpen() {
			t.Fatalf("iteration %d at %dx%d: the inspector became unreachable", i, w, ht)
		}
	}
}

// TestOpenTargetRefusesATargetWhoseTabIsNotImplemented pins the current-state
// contract, and is deliberate rather than incidental.
//
// Routing already resolves a target to its owning tab (via
// inspector.TabForTarget), but in this task none of those tabs is populated —
// only Overview is — so openTarget must refuse rather than push a target into a
// panel that cannot show it. The alternative, opening Overview and silently
// doing nothing with the target, is the failure this asserts against: the user
// asked to see a specific file and would be shown an unrelated panel with no
// explanation.
//
// Tasks 7, 8 and 10 make Changes, Agents and Context real; this test should be
// replaced by one asserting successful routing at that point, not deleted.
func TestOpenTargetRefusesATargetWhoseTabIsNotImplemented(t *testing.T) {
	for _, kind := range []inspector.TargetKind{
		inspector.TargetChangedFile,
		inspector.TargetAgent,
		inspector.TargetBlock,
	} {
		tab, ok := inspector.TabForTarget(inspector.Target{Kind: kind})
		if !ok {
			t.Fatalf("no tab maps to target kind %q", kind)
		}
		implemented := false
		for _, visible := range inspector.VisibleTabs() {
			if visible == tab {
				implemented = true
			}
		}
		if implemented {
			continue // this one routes for real; covered once its task lands
		}

		h := newInspectorHost()
		h.open(inspector.TabOverview, true)
		before := h.model.SelectedTab()

		target := inspector.Target{Kind: kind, Scope: "s1", ID: "internal/app/tui/keypress.go"}
		if h.openTarget(target, true) {
			t.Fatalf("openTarget() accepted a %q target whose tab %q is not implemented", kind, tab)
		}
		if _, ok := h.model.ActiveTarget(); ok {
			t.Fatalf("a %q target was recorded despite its tab being unimplemented", kind)
		}
		if h.model.Depth() != 0 {
			t.Fatalf("depth = %d after a refused target, want 0", h.model.Depth())
		}
		if h.model.SelectedTab() != before {
			t.Fatalf("tab = %q after a refused target, want %q unchanged", h.model.SelectedTab(), before)
		}
	}
}

// TestSuspendedInspectorDoesNotOwnTheDock pins that suspension really yields:
// while a competing panel is up, the inspector must not report itself as the
// dock's occupant, or the two would fight over the same slot.
func TestSuspendedInspectorDoesNotOwnTheDock(t *testing.T) {
	h := newInspectorHost()
	h.open(inspector.TabOverview, false) // dock placement
	if !h.dockOwned() {
		t.Fatal("a dock-placed inspector does not report owning the dock")
	}
	h.suspend()
	if h.dockOwned() {
		t.Fatal("a suspended inspector still claims the dock slot")
	}
	h.restore()
	if !h.dockOwned() {
		t.Fatal("restoring did not reclaim the dock slot")
	}
}

// TestSidePlacementDoesNotOwnTheDock pins the converse: a side-placed
// inspector leaves the dock free for other panels, which is the whole point of
// having two placements.
func TestSidePlacementDoesNotOwnTheDock(t *testing.T) {
	h := newInspectorHost()
	h.open(inspector.TabOverview, true)
	if h.dockOwned() {
		t.Fatal("a side-placed inspector claims the dock slot, which would block other panels")
	}
}
