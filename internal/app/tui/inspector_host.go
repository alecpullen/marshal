package tui

import (
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/inspector"
)

// inspectorPlacement is where the inspector is rendering.
//
// Placement is the host's business, not the inspector's: the same
// inspector.Model is shown beside the conversation, in the dock slot, or over
// the body, and the state it holds must not depend on which. That is what
// makes moving between placements free.
type inspectorPlacement int

const (
	// inspectorClosed: the user is not inspecting anything, so nothing is
	// rendered and the dock slot is free.
	inspectorClosed inspectorPlacement = iota
	// inspectorSide: the inspector owns the second column beside the
	// conversation. The transcript and the draft both stay visible.
	inspectorSide
	// inspectorDock: no room for a second column, so the inspector takes the
	// dock slot above the composer. Narrower, but it keeps the transcript and
	// the draft visible too.
	inspectorDock
	// inspectorBodyExpanded: the inspector replaces the body only. The
	// composer and footer stay exactly where they are.
	inspectorBodyExpanded
	// inspectorSuspended: open, but yielding the dock to a competing modal
	// panel. The state is kept; nothing is rendered.
	inspectorSuspended
)

func (p inspectorPlacement) String() string {
	switch p {
	case inspectorSide:
		return "side"
	case inspectorDock:
		return "dock"
	case inspectorBodyExpanded:
		return "body-expanded"
	case inspectorSuspended:
		return "suspended"
	default:
		return "closed"
	}
}

// inspectorHost owns the inspector's placement and the transition rules
// between placements.
//
// It exists so the placement rules live in one reviewable place instead of
// being scattered through View, Update and the key handler, where "does the
// inspector own the dock right now?" would get three different answers.
type inspectorHost struct {
	model   *inspector.Model
	adapter *inspector.DockAdapter

	place inspectorPlacement
	// sideAvailable is whether the terminal currently has room for a second
	// column. It is recorded rather than recomputed so open() can honour an
	// explicit open below the threshold without having to re-derive the
	// geometry, and so a resize can move a side-placed inspector to the dock
	// and back without the caller re-deciding.
	sideAvailable bool
	// wantSide remembers whether the user's inspector was side-placed, so a
	// resize that forces it into the dock can restore the intent when the
	// room comes back rather than demoting it permanently.
	wantSide bool
}

func newInspectorHost() *inspectorHost {
	m := inspector.New()
	return &inspectorHost{
		model:   m,
		adapter: inspector.NewDockAdapter(m),
		place:   inspectorClosed,
	}
}

// isOpen reports whether the user HAS an inspector: it is wanted, whether or
// not it is currently being rendered.
//
// Suspension is deliberately "open". A suspended inspector is one the user
// asked for and a modal panel borrowed the space from; reporting it as closed
// would make Ctrl+B reopen a fresh one instead of giving back the one they
// had. Use isRendering for "is it on screen right now".
func (h *inspectorHost) isOpen() bool {
	return h.place != inspectorClosed
}

// isRendering reports whether the inspector is actually drawn. This is the
// distinction suspension creates: open but not rendering.
func (h *inspectorHost) isRendering() bool {
	return h.place == inspectorSide || h.place == inspectorDock || h.place == inspectorBodyExpanded
}

func (h *inspectorHost) isSuspended() bool { return h.place == inspectorSuspended }

func (h *inspectorHost) placement() inspectorPlacement { return h.place }

// replacesBodyOnly reports whether the inspector takes the body without taking
// the composer. Body-expanded is the one placement that replaces content
// rather than sharing it.
func (h *inspectorHost) replacesBodyOnly() bool {
	return h.place == inspectorBodyExpanded
}

// dockOwned reports whether the inspector currently occupies the dock slot.
// A suspended or closed inspector does not, which is what lets a modal panel
// take the slot without a fight.
func (h *inspectorHost) dockOwned() bool { return h.place == inspectorDock }

// open shows the inspector on a tab.
//
// An explicit open ALWAYS succeeds. Below the side threshold it falls back to
// the dock rather than refusing: a user who typed /inspect asked for the
// inspector, not for the rail, and silently doing nothing is the worst
// possible answer to an explicit request.
//
// It reports false only for a tab that has no implementation yet, so the
// caller can say so instead of rendering an empty panel that looks broken.
func (h *inspectorHost) open(tab inspector.Tab, sideAvailable bool) bool {
	selectable := false
	for _, visible := range inspector.VisibleTabs() {
		if visible == tab {
			selectable = true
			break
		}
	}
	if !selectable {
		// The tab is not real yet. Do not switch to it, and do not open the
		// panel on some other tab as a consolation: the user asked for a
		// specific view and must be told it does not exist.
		return false
	}

	h.sideAvailable = sideAvailable
	h.wantSide = sideAvailable
	h.model.Open(tab)
	if sideAvailable {
		h.place = inspectorSide
	} else {
		h.place = inspectorDock
	}
	return true
}

// openTarget shows the inspector on the tab that owns a target and opens the
// target itself. It reports false when the target's tab is not available yet.
func (h *inspectorHost) openTarget(target inspector.Target, sideAvailable bool) bool {
	tab, ok := inspector.TabForTarget(target)
	if !ok || !h.open(tab, sideAvailable) {
		return false
	}
	h.model.OpenTarget(target)
	return true
}

// close dismisses the inspector and clears the detail stack.
//
// The per-tab navigation state deliberately survives a close: scroll position
// and filter are where the user was reading, not something they asked to
// discard, and reopening a view at the top when they had scrolled is the kind
// of small betrayal that makes a panel feel unreliable.
func (h *inspectorHost) close() {
	h.place = inspectorClosed
	h.model.ClearStack()
}

// toggle is Ctrl+B. From suspended it RESTORES rather than reopening, because
// the suspension was the app's doing — a modal borrowed the slot — and the
// user pressing the key is asking for their inspector back, not for a fresh
// one.
func (h *inspectorHost) toggle(sideAvailable bool) {
	switch h.place {
	case inspectorClosed:
		h.open(h.model.SelectedTab(), sideAvailable)
	case inspectorSuspended:
		h.restore()
	case inspectorBodyExpanded:
		// Ctrl+B from body-expanded returns to the shared placement rather
		// than closing: the inspector is still wanted, it just was not wanted
		// full-body.
		h.wantSide = sideAvailable
		h.sideAvailable = sideAvailable
		if sideAvailable {
			h.place = inspectorSide
		} else {
			h.place = inspectorDock
		}
	default:
		h.close()
	}
}

// suspend yields the dock slot to a competing modal panel without losing the
// inspector's state.
//
// This is the difference that matters: a full-frame settings browser and the
// inspector both want the space above the composer, and one of them has to
// lose. Losing the RENDER is fine; losing the state is not, because the
// suspension is the app's decision, not the user's.
func (h *inspectorHost) suspend() {
	if h.place == inspectorClosed || h.place == inspectorSuspended {
		return
	}
	h.place = inspectorSuspended
}

// restore undoes a suspension, returning the inspector to the placement it
// had. It is a no-op when nothing is suspended, so callers can call it
// unconditionally as a competing panel closes.
func (h *inspectorHost) restore() {
	if h.place != inspectorSuspended {
		return
	}
	if h.wantSide && h.sideAvailable {
		h.place = inspectorSide
	} else {
		h.place = inspectorDock
	}
}

// expandBody moves the inspector to the body-expanded placement, replacing the
// conversation but not the composer.
func (h *inspectorHost) expandBody() {
	if h.place == inspectorClosed {
		// The tab the inspector would open on is the one it is showing, which
		// is Changes for a fresh one. Naming Overview here would open a
		// body-expanded telemetry view, which is the opposite of what a reader
		// who pressed the "expand this" key asked for.
		h.open(h.model.SelectedTab(), h.sideAvailable)
	}
	if h.place == inspectorClosed || h.place == inspectorSuspended {
		return
	}
	h.place = inspectorBodyExpanded
}

// leaveBodyExpanded returns from body-expanded to the shared placement. It
// reports whether there was anything to do, so Esc can fall through to its
// other meanings when the inspector is not expanded.
func (h *inspectorHost) leaveBodyExpanded() bool {
	if h.place != inspectorBodyExpanded {
		return false
	}
	if h.wantSide && h.sideAvailable {
		h.place = inspectorSide
	} else {
		h.place = inspectorDock
	}
	return true
}

// esc handles Esc while the inspector owns focus. It backs out of the deepest
// thing first — body-expanded, then the Context tab's child scope, then the
// detail stack — and reports whether it consumed the key.
//
// The child scope is handled HERE rather than in the model's per-tab key
// handler, and that placement is load-bearing. Esc is consumed by the keypress
// router before any tab handler runs (keypress.go's Esc branch calls esc() and,
// when it returns false but the inspector is rendering, falls into the focus
// move). A child-scope case in the tab handler was therefore unreachable: the
// reader who scoped to a child from Agents had no key that returned them to the
// conversation's own context.
//
// Falling through matters: Esc also cancels a turn and dismisses popups, and an
// inspector that ate the key unconditionally would break those.
func (h *inspectorHost) esc() bool {
	if h.place == inspectorBodyExpanded {
		return h.leaveBodyExpanded()
	}
	if h.model.ClearChildContext() {
		return true
	}
	return h.model.Back()
}

// resize records the available area.
//
// It also re-decides the placement: a terminal narrowed below the side
// threshold cannot keep a second column, and the inspector moves to the dock
// rather than vanishing. The user's side preference is remembered so widening
// restores it.
func (h *inspectorHost) resize(width, height int) {
	h.model.Resize(width, height)
	if h.place == inspectorClosed || h.place == inspectorSuspended {
		return
	}
	switch {
	case width <= 0:
		// Nothing is renderable; leave the state alone and let the renderer
		// fall back.
	case h.wantSide && h.sideAvailable && h.place != inspectorBodyExpanded:
		h.place = inspectorSide
	case h.place == inspectorSide && !h.sideAvailable:
		h.place = inspectorDock
	case h.place == inspectorDock && h.sideAvailable && h.wantSide:
		h.place = inspectorSide
	}
}

// setSideAvailable records whether the terminal currently has room for a
// second column, and moves a visible inspector accordingly.
func (h *inspectorHost) setSideAvailable(available bool) {
	if h.sideAvailable == available {
		return
	}
	h.sideAvailable = available
	if h.place == inspectorClosed || h.place == inspectorBodyExpanded || h.place == inspectorSuspended {
		return
	}
	if available && h.wantSide {
		h.place = inspectorSide
	} else if !available {
		h.place = inspectorDock
	}
}

// installInDock puts the inspector into the dock slot when it is dock-placed,
// and yields the slot when it is not.
//
// It is called from the render path rather than from the placement mutators
// because the dock is a single slot shared with every other panel: the moment
// the inspector stops wanting it, the slot must be free for whoever wants it
// next, and the renderer is the one place that knows what is actually being
// drawn this frame.
//
// The comparison is on the interface value, so a DIFFERENT panel in the slot is
// never disturbed.
func (h *inspectorHost) installInDock(host *dock.Host) {
	// Yielding is unconditional and comes first: whatever we are about to do,
	// a slot we should not hold must be released before anything else looks at
	// it.
	// The comparison is against h.adapter — the inspector's identity in a dock
	// slot — and NOT against panelFor(), which deliberately returns nil once
	// the inspector stops owning the dock. Comparing against panelFor() here
	// would make the release impossible to detect and the slot would stay held
	// by a closed inspector forever.
	if !h.dockOwned() {
		if host.Panel() == dock.Panel(h.adapter) {
			host.CloseNow()
		}
		return
	}
	if host.Panel() == nil {
		host.Open(h.adapter)
	}
}
