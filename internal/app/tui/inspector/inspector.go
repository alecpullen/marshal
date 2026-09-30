// Package inspector holds the conversation inspector's presentation state:
// which tab is on display, each tab's own navigation state, the detail stack,
// the active target, and the scoped request sequence that rejects stale async
// replies.
//
// It is deliberately independent of where it is rendered. The same *Model
// backs the side column and the docked panel, so switching placement never
// loses the user's tab, cursor, scroll, or detail stack. It never calls Git,
// the DB, or the provider: the root hands it a Data snapshot on refresh.
package inspector

import (
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/sidepanel"
)

// Tab names one inspector view.
type Tab string

const (
	TabOverview Tab = "overview"
	TabChanges  Tab = "changes"
	TabAgents   Tab = "agents"
	TabContext  Tab = "context"
)

// allTabs is the full tab bar, in display order: Changes, Agents, Context,
// Overview. Changes leads because it is the tab a developer opens the
// inspector for; Overview is last because it is the retained telemetry view
// rather than a working surface.
//
// The order is the product contract, not an implementation detail: it is what
// Tab/Shift+Tab walks and what the rendered tab bar shows, so the two cannot
// drift.
var allTabs = []Tab{TabChanges, TabAgents, TabContext, TabOverview}

// visibleTabs is the subset a user may actually open right now. An
// unimplemented tab must not be offered, because selecting it would present an
// empty panel the user cannot distinguish from a bug.
//
// This task lands the last of the four, so the visible set is now the whole
// product set. The two lists stay separate because the distinction is still
// the rule — a future tab lands here before it lands in allTabs — and because
// Tests assert on VisibleTabs() to mean "what the user can reach".
var visibleTabs = []Tab{TabChanges, TabAgents, TabContext, TabOverview}

// AllTabs is the full product set, in display order. The returned slice is a
// copy; callers may keep or mutate it freely.
func AllTabs() []Tab { return append([]Tab(nil), allTabs...) }

// VisibleTabs is the tabs a user may actually open right now. The returned
// slice is a copy.
func VisibleTabs() []Tab { return append([]Tab(nil), visibleTabs...) }

// TargetKind classifies what a Target points at.
type TargetKind string

const (
	TargetChangedFile TargetKind = "changed-file"
	TargetAgent       TargetKind = "agent"
	TargetBlock       TargetKind = "block"
)

// Target is a scoped, stable identity for something an inspector can open.
//
// It is comparable, so callers can compare two targets with ==.
type Target struct {
	Kind TargetKind
	// Scope is the session/state identity the ID belongs to. Two different
	// sessions both have "msg:1"; without the scope, opening a target from a
	// closed session would resolve to an unrelated item in the new one.
	Scope string
	ID    string
}

// TabForTarget reports which tab displays a target, so a caller in another
// package can route without duplicating the mapping.
//
// The lookup is exported rather than duplicated in the placement host: two
// copies of "which tab shows a changed file" would drift, and the symptom would
// be a click that opens the wrong panel.
func TabForTarget(target Target) (Tab, bool) {
	return tabForKind(target.Kind)
}

// tabForKind reports which tab owns a target kind.
func tabForKind(k TargetKind) (Tab, bool) {
	switch k {
	case TargetChangedFile:
		return TabChanges, true
	case TargetAgent:
		return TabAgents, true
	case TargetBlock:
		return TabContext, true
	}
	return "", false
}

// TabState is per-tab navigation state. Each tab keeps its own, so switching
// tabs and returning never loses where the user was.
type TabState struct {
	// Scroll is the first row the tab shows.
	//
	// It is the ONLY field, and that is deliberate. TabState used to carry
	// Cursor and Filter as well, and nothing read or wrote either of them in
	// production: each tab with a cursor keeps it in its own state
	// (changesState.cursor, agentsState.cursor, contextState.cursor), because a
	// cursor is keyed by something the tab knows — a path, a runtime ID — and a
	// bare int here could not express that. Fields that LOOK like a tab's
	// navigation state but never participate in it are worse than none: they
	// invite the next reader to update the wrong one and then wonder why nothing
	// moves.
	Scroll int
}

// Data is the snapshot the inspector renders from, handed in by the root on
// refresh. The inspector never calls Git, the DB, or the provider itself.
type Data struct {
	Side sidepanel.Data
	// Hidden is the set of hidden sidepanel section IDs, preserved as-is.
	// Rendering reads it and never writes to it: a render that mutated its
	// input would show up as a spurious config write.
	Hidden map[string]bool
}

// Model is the inspector's state, independent of where it is rendered.
type Model struct {
	tab    Tab
	perTab map[Tab]TabState

	// stack is the detail stack, outermost first. active mirrors its last
	// element so ActiveTarget is O(1) and hasActive distinguishes "no detail
	// open" from "a detail open on the zero Target".
	stack     []Target
	active    Target
	hasActive bool

	width, height int

	// data is the most recent snapshot. The dock adapter has no data
	// parameter in its View signature, so the model has to hold one.
	data Data

	// changes is the Changes tab's own state: its list, its selection (keyed by
	// path), its in-flight request, and whether a read has landed.
	changes changesState
	// agents is the Agents tab's own state: the copied runtime roster, its
	// selection (keyed by runtime ID), and whether that selection has left.
	agents agentsState
	// context is the Context tab's own state: its scope selection, the copied
	// pack and request snapshots, and the explicitly-scoped child's context.
	context contextState
	// detail is the scrollable body a tab opens to show one thing in full. It
	// lives on the Model rather than inside a tab so switching tabs does not
	// discard what the reader was studying, and so the Agents tab can use the
	// same body without duplicating the scroll/follow rules.
	detail *DetailView
	// detailLabel is the heading the detail renders under.
	detailLabel string

	// scope and seq implement stale-reply rejection. seq is the id of the
	// most recently issued request; a reply is applicable only when it
	// carries the current scope and that exact id. SetScope resets seq, so an
	// id issued under a previous scope can never be accepted again.
	scope string
	seq   uint64
}

// New returns an inspector showing the first visible tab, which is Changes.
//
// The Context tab starts on the "current pack" scope rather than on the last
// request: the pack exists before the first request does, so a fresh session
// opens on content rather than on an empty state the reader has to interpret.
func New() *Model {
	return &Model{
		tab:    visibleTabs[0],
		perTab: map[Tab]TabState{},
		detail: NewDetailView(),
		context: contextState{
			scope:  ContextScopePack,
			cursor: map[string]int{},
		},
	}
}

// SelectedTab reports the tab on display.
func (m *Model) SelectedTab() Tab { return m.tab }

// Open switches to a tab, without touching the detail stack. A tab that is not
// currently visible is ignored: the plan's rule is that unimplemented tabs are
// not offered, and silently switching to one would present an empty panel.
func (m *Model) Open(tab Tab) {
	if !m.visible(tab) {
		return
	}
	m.tab = tab
}

// open switches to a tab unconditionally. It exists so tests and future tasks
// can exercise per-tab state for a tab that is not yet offered.
func (m *Model) open(tab Tab) { m.tab = tab }

// visible reports whether tab is in the currently offered set.
func (m *Model) visible(tab Tab) bool {
	for _, t := range visibleTabs {
		if t == tab {
			return true
		}
	}
	return false
}

// OpenTarget switches to the tab that owns target.Kind and pushes it onto the
// detail stack.
//
// The switch is skipped when the owning tab is not visible yet (Changes,
// Agents, and Context are not, in this task): the target is still recorded, so
// the stack and the active target are correct the moment the tab lands, but
// the user is not dropped into an empty panel.
func (m *Model) OpenTarget(target Target) {
	if tab, ok := tabForKind(target.Kind); ok && m.visible(tab) {
		m.tab = tab
	}
	// Re-opening the target already on top is a no-op rather than a second
	// history level: otherwise Esc would need two presses to leave a view the
	// user entered once.
	if m.hasActive && m.active == target {
		return
	}
	m.stack = append(m.stack, target)
	m.active, m.hasActive = target, true
}

// ActiveTarget reports the innermost open target, if any.
func (m *Model) ActiveTarget() (Target, bool) { return m.active, m.hasActive }

// ClearStack dismisses every open detail at once, leaving the tab selection and
// per-tab navigation state untouched.
//
// Closing the inspector needs this rather than repeated Back: Back pops one
// level and reports whether it did, which is the right contract for a key press
// and the wrong one for "dismiss this view" — a close that popped a level would
// leave the user inside a panel they had just closed. The tab and its scroll
// position survive because they describe where the user was reading, not what
// they asked to discard.
func (m *Model) ClearStack() {
	m.stack = nil
	m.active, m.hasActive = Target{}, false
}

// Back pops one level of the detail stack. It reports false when already at
// the root, so the caller can fall through to a different Esc meaning rather
// than swallowing the key.
func (m *Model) Back() bool {
	if len(m.stack) == 0 {
		return false
	}
	m.stack = m.stack[:len(m.stack)-1]
	if n := len(m.stack); n > 0 {
		m.active, m.hasActive = m.stack[n-1], true
	} else {
		m.active, m.hasActive = Target{}, false
	}
	return true
}

// Depth reports how deep the detail stack is (0 at the root).
func (m *Model) Depth() int { return len(m.stack) }

// Resize records the available area. It must never lose tab/cursor/scroll/
// stack state: resizing is not navigation.
func (m *Model) Resize(width, height int) {
	m.width, m.height = width, height
}

// Size reports the last recorded area.
func (m *Model) Size() (width, height int) { return m.width, m.height }

// State returns a tab's saved navigation state. An unknown tab yields the zero
// TabState.
func (m *Model) State(tab Tab) TabState { return m.perTab[tab] }

// SetState saves a tab's navigation state.
func (m *Model) SetState(tab Tab, s TabState) {
	if m.perTab == nil {
		m.perTab = map[Tab]TabState{}
	}
	m.perTab[tab] = s
}

// SetData records the snapshot the dock adapter renders from. The side
// placement passes its snapshot straight to View instead.
func (m *Model) SetData(d Data) { m.data = d }

// SetScope records the session/state identity this inspector is bound to and
// resets the request sequence. Any reply issued under the previous scope is
// rejected from here on, even if the sequence were to restart at the same id.
//
// It is IDEMPOTENT for an unchanged scope, and that is load-bearing. Callers
// re-stamp the scope on every refresh so a session swap is picked up without
// having to remember to tell the inspector; if re-stamping an unchanged scope
// reset the sequence, request ids would restart at 1 on every refresh. Two
// requests for the same path would then carry the same id, and a superseded
// reply would be accepted as the current one — the exact defect the id exists
// to prevent.
func (m *Model) SetScope(scope string) {
	if scope == m.scope {
		return
	}
	m.scope = scope
	m.seq = 0
	// The detail stack describes the conversation that was replaced. Leaving
	// it standing would show a child transcript from a session the user has
	// left, with nothing on screen to say so — the same failure the request-id
	// reset exists to prevent for a diff.
	m.ClearStack()
	// The per-tab navigation state goes with it, for the same reason: a cursor
	// is a position in a list that belonged to the old conversation. The
	// selection is re-derived from the new roster, which arrives with the next
	// refresh.
	m.agents = agentsState{}
	// The Context tab's data and detail belong to the conversation that was
	// replaced, so both go. The SCOPE selection survives: it is a preference
	// about which question the reader is asking, not a position in a list that
	// no longer exists, and resetting it would move them without their asking.
	// (The child scope does NOT survive: it names an agent of the conversation
	// that just ended.)
	contextScope := m.context.scope
	m.context = contextState{scope: contextScope, cursor: map[string]int{}}
}

// Scope reports the identity recorded by SetScope.
func (m *Model) Scope() string { return m.scope }

// NextRequest issues the next request id for this scope. Ids start at 1, so
// zero is always stale.
func (m *Model) NextRequest() uint64 {
	m.seq++
	return m.seq
}

// AcceptReply reports whether an async reply may be applied. It is true only
// for a reply carrying the current scope and the most recently issued request
// id, so a superseded reply and a reply from a closed session are both
// rejected.
func (m *Model) AcceptReply(scope string, reqID uint64) bool {
	return reqID != 0 && scope == m.scope && reqID == m.seq
}

// NextTab cycles forward among VisibleTabs only.
func (m *Model) NextTab() { m.cycle(1) }

// PrevTab cycles backward among VisibleTabs only.
func (m *Model) PrevTab() { m.cycle(-1) }

// cycle moves the selection by delta within the visible set. When the current
// tab is not visible — a tab that has not landed yet, or one that lost
// visibility — it lands on the first visible tab rather than staying stranded
// on a panel the user cannot see.
func (m *Model) cycle(delta int) {
	if len(visibleTabs) == 0 {
		return
	}
	idx := -1
	for i, t := range visibleTabs {
		if t == m.tab {
			idx = i
			break
		}
	}
	if idx < 0 {
		m.tab = visibleTabs[0]
		return
	}
	m.tab = visibleTabs[(idx+delta+len(visibleTabs))%len(visibleTabs)]
}

// View renders the selected tab at the recorded size. It returns "" when the
// recorded area has no room, so a degenerate size can never panic or emit a
// row wider than the frame it is joined into.
func (m *Model) View(data Data) string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	switch m.tab {
	case TabOverview:
		return m.viewOverview(data)
	case TabChanges:
		// The Changes tab renders its own list and detail rather than going
		// through the generic per-tab scroll offset: its navigation is a cursor
		// over PATHS plus a separate body, and flattening that into one scroll
		// number would lose which file the reader is on.
		//
		// The shared body is NOT sized here: each tab decides its own share of
		// the panel AFTER budgeting its list, and sizing it to the whole panel
		// here would make it claim rows the tab had already promised to the
		// list — which is exactly how the emission came out taller than the
		// height it was given.
		return m.viewChanges()
	case TabAgents:
		// The Agents tab shares that shape and that shared body: a cursor over
		// runtime IDs plus the child's conversation. It gets the same treatment
		// for the same reason.
		return m.viewAgents()
	case TabContext:
		// The Context tab shares that shape a third time: a cursor over the
		// scope's rows plus the shared body.
		return m.viewContext()
	default:
		// A tab with no renderer yet. Returning "" is honest: the tab is not
		// offered, so this is only reachable through the unexported open.
		return ""
	}
}

// scrollBy moves the selected tab's scroll offset by delta, clamped to the
// content.
func (m *Model) scrollBy(delta int) { m.setScroll(m.State(m.tab).Scroll + delta) }

// setScroll records an absolute scroll offset for the selected tab.
//
// The clamp is per-TAB, because the tabs do not scroll the same thing: the
// Overview scrolls a document, while Changes, Agents and Context scroll a LIST of
// rows whose length their own state holds. Clamping a list tab against the
// Overview's row count (the sole clamp this used to apply) would snap every list
// scroll back to zero, so a reader who scrolled a long file list and returned to
// it would find it at the top — and would have no way to tell that from the key
// not working.
//
// The list tabs are additionally clamped to keep their CURSOR on screen, since
// the cursor is what the list is navigated by; a scroll that hid the cursor would
// look like the window lost the reader's place.
func (m *Model) setScroll(v int) {
	switch m.tab {
	case TabChanges:
		v = min(max(v, 0), max(len(m.changes.rows)-1, 0))
	case TabAgents:
		v = min(max(v, 0), max(len(m.agents.roster)-1, 0))
	case TabContext:
		v = min(max(v, 0), max(len(m.context.rows)-1, 0))
	default:
		v = min(max(v, 0), m.maxScroll(m.data))
	}
	s := m.State(m.tab)
	s.Scroll = v
	m.SetState(m.tab, s)
}

// page is the row count one PageUp/PageDown moves.
func (m *Model) page() int { return max(1, m.height-1) }

// CloseMsg asks the host to close the inspector. It is emitted when Esc has
// nothing left to back out of.
type CloseMsg struct{}

// DockAdapter presents a Model as a dock.Panel plus dock.MessageOwner, so the
// inspector can live in the dock without the dock knowing what it is.
type DockAdapter struct{ m *Model }

// NewDockAdapter wraps m for the dock host.
func NewDockAdapter(m *Model) *DockAdapter { return &DockAdapter{m: m} }

// Model returns the wrapped inspector.
func (a *DockAdapter) Model() *Model { return a.m }

// Sizing reports the dock height budget. The inspector's body-expanded
// presentation is its own mode, not dock.FullFrame, whose semantics hide the
// composer.
func (a *DockAdapter) Sizing() dock.Sizing { return dock.Docked }

// View renders the inspector at the dock's dimensions, recording them so the
// scroll bound matches what is on screen.
func (a *DockAdapter) View(width, maxHeight int) string {
	a.m.Resize(width, maxHeight)
	return a.m.View(a.m.data)
}

// OwnsMsg reports whether the adapter claims msg as its own async result. Only
// CloseMsg is claimed: keypresses and pastes are forwarded to the active panel
// unconditionally by the dock host, and every other message must keep flowing
// to the main model's handlers so background work continues.
func (a *DockAdapter) OwnsMsg(msg tea.Msg) bool {
	_, ok := msg.(CloseMsg)
	return ok
}

// Update routes a message to the inspector.
func (a *DockAdapter) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		return a.handleKey(k)
	}
	return nil
}

// handleKey maps the inspector's keys onto model operations. Tab/Shift+Tab
// cycle tabs, Enter opens the selected row (a later task's concern), and Esc
// backs out of the detail stack before it means "close".
func (a *DockAdapter) handleKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "up", "k":
		a.m.scrollBy(-1)
	case "down", "j":
		a.m.scrollBy(1)
	case "pgup":
		a.m.scrollBy(-a.m.page())
	case "pgdown":
		a.m.scrollBy(a.m.page())
	case "home", "g":
		a.m.setScroll(0)
	case "end", "G":
		a.m.setScroll(a.m.maxScroll(a.m.data))
	case "tab":
		a.m.NextTab()
	case "shift+tab":
		a.m.PrevTab()
	case "esc":
		if a.m.Back() {
			return nil
		}
		return func() tea.Msg { return CloseMsg{} }
	}
	return nil
}
