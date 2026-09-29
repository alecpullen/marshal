package inspector

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/sidepanel"
	"marshal/internal/app/tui/theme"
)

func TestMain(m *testing.M) {
	// Every renderer reads theme.Current(). Without a loaded theme each slot
	// is the zero color and the styled output is not what production emits.
	theme.Reload(theme.LoadFor(false, "xterm-256color"))
	os.Exit(m.Run())
}

// sameTarget compares the full scoped identity. Target is comparable, but a
// named helper keeps "the same thing" distinct from a field-by-field
// assertion, and it is the comparison the scoping tests are actually about.
func sameTarget(a, b Target) bool {
	return a.Kind == b.Kind && a.Scope == b.Scope && a.ID == b.ID
}

// Compile-time proof that the adapter satisfies both dock contracts. The
// inspector must be hostable without the dock knowing what it is.
var (
	_ dock.Panel        = (*DockAdapter)(nil)
	_ dock.MessageOwner = (*DockAdapter)(nil)
)

func TestAllTabsIsTheFullProductSet(t *testing.T) {
	want := []Tab{TabOverview, TabChanges, TabAgents, TabContext}
	if got := AllTabs(); !reflect.DeepEqual(got, want) {
		t.Errorf("AllTabs() = %v, want %v", got, want)
	}
}

// Only Overview is populated in this task. Offering an unimplemented tab
// would present an empty panel the user cannot distinguish from a bug.
func TestVisibleTabsOffersOnlyImplementedTabs(t *testing.T) {
	want := []Tab{TabOverview}
	if got := VisibleTabs(); !reflect.DeepEqual(got, want) {
		t.Errorf("VisibleTabs() = %v, want %v", got, want)
	}
}

// The returned slice is the caller's; mutating it must not change what the
// next caller sees.
func TestTabListsAreCopies(t *testing.T) {
	all := AllTabs()
	all[0] = TabChanges
	if AllTabs()[0] != TabOverview {
		t.Error("AllTabs() handed out the package's own slice")
	}
	vis := VisibleTabs()
	vis[0] = TabChanges
	if VisibleTabs()[0] != TabOverview {
		t.Error("VisibleTabs() handed out the package's own slice")
	}
}

func TestOpenIgnoresUnimplementedTab(t *testing.T) {
	m := New()
	if m.SelectedTab() != TabOverview {
		t.Fatalf("New() selected %q, want %q", m.SelectedTab(), TabOverview)
	}
	for _, tab := range []Tab{TabChanges, TabAgents, TabContext, Tab("nonsense")} {
		m.Open(tab)
		if got := m.SelectedTab(); got != TabOverview {
			t.Errorf("Open(%q) switched to %q; an unimplemented tab must not be offered", tab, got)
		}
	}
	m.Open(TabOverview)
	if got := m.SelectedTab(); got != TabOverview {
		t.Errorf("Open(TabOverview) selected %q, want %q", got, TabOverview)
	}
}

// Each tab keeps its own navigation state, so switching away and back never
// loses where the user was. Only Overview is visible today, so the switch is
// driven through the visibility-free helper Open guards.
func TestPerTabStateSurvivesTabSwitch(t *testing.T) {
	m := New()
	overview := TabState{Cursor: 3, Filter: "go", Scroll: 7}
	changes := TabState{Cursor: 1, Filter: "diff", Scroll: 2}
	m.SetState(TabOverview, overview)
	m.SetState(TabChanges, changes)

	m.open(TabChanges)
	if m.SelectedTab() != TabChanges {
		t.Fatalf("SelectedTab() = %q, want %q", m.SelectedTab(), TabChanges)
	}
	m.open(TabOverview)
	if got := m.State(TabOverview); got != overview {
		t.Errorf("Overview state after returning = %+v, want %+v", got, overview)
	}
	m.open(TabChanges)
	if got := m.State(TabChanges); got != changes {
		t.Errorf("Changes state after returning = %+v, want %+v", got, changes)
	}
}

// Resizing is not navigation. Twenty resizes across the three supported
// terminal sizes must leave tab, cursor, filter, scroll, and the detail stack
// exactly as they were.
func TestPerTabStateSurvivesRepeatedResize(t *testing.T) {
	m := New()
	want := TabState{Cursor: 4, Filter: "ctx", Scroll: 9}
	m.SetState(TabOverview, want)
	target := Target{Kind: TargetChangedFile, Scope: "s1", ID: "file:1"}
	m.OpenTarget(target)

	sizes := []struct{ w, h int }{{80, 24}, {120, 40}, {200, 60}}
	for i := 0; i < 20; i++ {
		s := sizes[i%len(sizes)]
		m.Resize(s.w, s.h)

		if got := m.State(TabOverview); got != want {
			t.Fatalf("resize %d (%dx%d): Overview state = %+v, want %+v", i, s.w, s.h, got, want)
		}
		if got := m.Depth(); got != 1 {
			t.Fatalf("resize %d (%dx%d): Depth = %d, want 1", i, s.w, s.h, got)
		}
		if got, ok := m.ActiveTarget(); !ok || !sameTarget(got, target) {
			t.Fatalf("resize %d (%dx%d): ActiveTarget = %+v/%v, want %+v", i, s.w, s.h, got, ok, target)
		}
		if got := m.SelectedTab(); got != TabOverview {
			t.Fatalf("resize %d (%dx%d): SelectedTab = %q, want %q", i, s.w, s.h, got, TabOverview)
		}
	}
}

func TestDetailStackPushPopAndDepth(t *testing.T) {
	m := New()
	if got := m.Depth(); got != 0 {
		t.Fatalf("New() Depth = %d, want 0", got)
	}
	if _, ok := m.ActiveTarget(); ok {
		t.Error("ActiveTarget() reported a target at the root")
	}
	if m.Back() {
		t.Error("Back() at the root reported true; the caller must be able to fall through to another Esc meaning")
	}
	if got := m.Depth(); got != 0 {
		t.Errorf("Back() at the root changed Depth to %d", got)
	}

	a := Target{Kind: TargetChangedFile, Scope: "s1", ID: "file:a"}
	b := Target{Kind: TargetAgent, Scope: "s1", ID: "agent:1"}

	m.OpenTarget(a)
	if got := m.Depth(); got != 1 {
		t.Fatalf("after OpenTarget(a) Depth = %d, want 1", got)
	}
	if got, ok := m.ActiveTarget(); !ok || !sameTarget(got, a) {
		t.Fatalf("ActiveTarget() = %+v/%v, want %+v", got, ok, a)
	}

	m.OpenTarget(b)
	if got := m.Depth(); got != 2 {
		t.Fatalf("after OpenTarget(b) Depth = %d, want 2", got)
	}
	if got, _ := m.ActiveTarget(); !sameTarget(got, b) {
		t.Fatalf("ActiveTarget() = %+v, want the innermost target %+v", got, b)
	}

	if !m.Back() {
		t.Fatal("Back() with a detail open reported false")
	}
	if got := m.Depth(); got != 1 {
		t.Fatalf("after Back() Depth = %d, want 1", got)
	}
	if got, _ := m.ActiveTarget(); !sameTarget(got, a) {
		t.Fatalf("after Back() ActiveTarget() = %+v, want %+v", got, a)
	}

	if !m.Back() {
		t.Fatal("Back() to the root reported false")
	}
	if got := m.Depth(); got != 0 {
		t.Fatalf("after Back() to the root Depth = %d, want 0", got)
	}
	if _, ok := m.ActiveTarget(); ok {
		t.Error("ActiveTarget() reported a target at the root")
	}
	if m.Back() {
		t.Error("Back() at the root reported true")
	}
}

// Re-opening the target already on top is a no-op, not a second history
// level: otherwise Esc would need two presses to leave a view the user
// entered once. Re-opening it after backing out does push again.
func TestOpenTargetSameTargetTwiceDoesNotDoublePush(t *testing.T) {
	m := New()
	a := Target{Kind: TargetChangedFile, Scope: "s1", ID: "file:a"}
	m.OpenTarget(a)
	m.OpenTarget(a)
	if got := m.Depth(); got != 1 {
		t.Errorf("opening the same target twice pushed %d levels, want 1", got)
	}

	b := Target{Kind: TargetAgent, Scope: "s1", ID: "agent:1"}
	m.OpenTarget(b)
	m.OpenTarget(a)
	if got := m.Depth(); got != 3 {
		t.Errorf("a, b, a pushed %d levels, want 3 (only the innermost repeat is a no-op)", got)
	}

	m.Back()
	m.Back()
	m.Back()
	m.OpenTarget(a)
	if got := m.Depth(); got != 1 {
		t.Errorf("re-opening after backing out pushed %d levels, want 1", got)
	}
}

// Two sessions both have "msg:1". Without the scope, a target opened in one
// session would resolve to an unrelated item in the other.
func TestTargetIdentityIsScoped(t *testing.T) {
	s1 := Target{Kind: TargetChangedFile, Scope: "s1", ID: "msg:1"}
	s2 := Target{Kind: TargetChangedFile, Scope: "s2", ID: "msg:1"}
	if sameTarget(s1, s2) {
		t.Fatal("targets with the same ID but different scopes compared equal")
	}
	if sameTarget(s1, Target{Kind: TargetAgent, Scope: "s1", ID: "msg:1"}) {
		t.Error("targets with the same scope and ID but different kinds compared equal")
	}

	m := New()
	m.OpenTarget(s1)
	if got, ok := m.ActiveTarget(); !ok || !sameTarget(got, s1) {
		t.Fatalf("ActiveTarget() = %+v/%v, want %+v", got, ok, s1)
	}
	m.OpenTarget(s2)
	got, ok := m.ActiveTarget()
	if !ok {
		t.Fatal("ActiveTarget() reported no target after OpenTarget")
	}
	if sameTarget(got, s1) {
		t.Errorf("ActiveTarget() = %+v; a target opened in scope s1 must not be reported as the active target for scope s2", got)
	}
	if !sameTarget(got, s2) {
		t.Errorf("ActiveTarget() = %+v, want %+v", got, s2)
	}
}

func TestAcceptReplyRejectsStaleAndForeignScope(t *testing.T) {
	m := New()
	m.SetScope("s1")

	first := m.NextRequest()
	if first == 0 {
		t.Fatal("NextRequest() returned 0; zero must always be stale")
	}
	if !m.AcceptReply("s1", first) {
		t.Error("a reply carrying the current scope and request id was rejected")
	}

	second := m.NextRequest()
	if second <= first {
		t.Fatalf("NextRequest() = %d after %d; ids must increase monotonically", second, first)
	}
	if m.AcceptReply("s1", first) {
		t.Error("a superseded request id was accepted")
	}
	if !m.AcceptReply("s1", second) {
		t.Error("the current request id was rejected")
	}
	if m.AcceptReply("s2", second) {
		t.Error("a reply from a different scope was accepted")
	}
	if m.AcceptReply("s1", 0) {
		t.Error("request id 0 was accepted; no request ever carries it")
	}

	// A new scope has issued no requests, so an id from the old scope can
	// never be accepted even if the sequence were to restart.
	m.SetScope("s2")
	if m.Scope() != "s2" {
		t.Fatalf("Scope() = %q, want %q", m.Scope(), "s2")
	}
	if m.AcceptReply("s2", second) {
		t.Error("an id issued under the previous scope was accepted after SetScope")
	}
	if m.AcceptReply("s1", second) {
		t.Error("a reply from the previous scope was accepted after SetScope")
	}
	fresh := m.NextRequest()
	if !m.AcceptReply("s2", fresh) {
		t.Error("a reply from the new scope was rejected")
	}
}

func TestNextPrevTabCycleVisibleTabsOnly(t *testing.T) {
	m := New()
	m.NextTab()
	if got := m.SelectedTab(); got != TabOverview {
		t.Errorf("NextTab() from the only visible tab selected %q, want %q", got, TabOverview)
	}
	m.PrevTab()
	if got := m.SelectedTab(); got != TabOverview {
		t.Errorf("PrevTab() from the only visible tab selected %q, want %q", got, TabOverview)
	}

	// A tab outside the visible set (forced here, as a future task's Open
	// would) must cycle back into it rather than staying stranded.
	m.open(TabChanges)
	m.NextTab()
	if got := m.SelectedTab(); !m.visible(got) {
		t.Errorf("NextTab() landed on %q, which is not visible", got)
	}
	m.open(TabChanges)
	m.PrevTab()
	if got := m.SelectedTab(); !m.visible(got) {
		t.Errorf("PrevTab() landed on %q, which is not visible", got)
	}
}

func TestDockAdapterPresentsTheModel(t *testing.T) {
	m := New()
	a := NewDockAdapter(m)
	if a.Model() != m {
		t.Error("Model() did not return the wrapped inspector")
	}
	if got := a.Sizing(); got != dock.Docked {
		t.Errorf("Sizing() = %v, want dock.Docked: the inspector's body-expanded presentation is its own mode, not dock.FullFrame", got)
	}
	if !a.OwnsMsg(CloseMsg{}) {
		t.Error("OwnsMsg(CloseMsg{}) = false; the adapter must claim its own async result")
	}
	if a.OwnsMsg(tea.KeyPressMsg{Code: tea.KeyDown}) {
		t.Error("OwnsMsg claimed a keypress; the dock host forwards those unconditionally")
	}
}

func TestDockAdapterKeysNavigate(t *testing.T) {
	m := New()
	m.Resize(80, 24)
	m.SetData(overviewData(time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)))
	a := NewDockAdapter(m)

	a.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.State(TabOverview).Scroll; got != 1 {
		t.Errorf("after down Scroll = %d, want 1", got)
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.State(TabOverview).Scroll; got != 0 {
		t.Errorf("after up Scroll = %d, want 0", got)
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.State(TabOverview).Scroll; got != 0 {
		t.Errorf("scrolling above the top gave Scroll = %d, want 0", got)
	}

	a.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	end := m.State(TabOverview).Scroll
	if end <= 0 {
		t.Fatalf("after end Scroll = %d, want the last page", end)
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.State(TabOverview).Scroll; got != end {
		t.Errorf("scrolling past the end gave Scroll = %d, want it clamped to %d", got, end)
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	if got := m.State(TabOverview).Scroll; got != 0 {
		t.Errorf("after home Scroll = %d, want 0", got)
	}

	a.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.SelectedTab(); got != TabOverview {
		t.Errorf("Tab selected %q, want %q", got, TabOverview)
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if got := m.SelectedTab(); got != TabOverview {
		t.Errorf("Shift+Tab selected %q, want %q", got, TabOverview)
	}
}

func TestDockAdapterEscBacksOutThenCloses(t *testing.T) {
	m := New()
	m.Resize(80, 24)
	a := NewDockAdapter(m)

	cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc at the root returned no command; the host must be able to close the inspector")
	}
	if _, ok := cmd().(CloseMsg); !ok {
		t.Errorf("Esc at the root produced %T, want CloseMsg", cmd())
	}

	m.OpenTarget(Target{Kind: TargetChangedFile, Scope: "s1", ID: "file:a"})
	cmd = a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Errorf("Esc with a detail open produced %T; it must pop the stack, not close", cmd())
	}
	if got := m.Depth(); got != 0 {
		t.Errorf("after Esc Depth = %d, want 0", got)
	}
}

func TestDockAdapterViewRendersAtTheDockSize(t *testing.T) {
	m := New()
	a := NewDockAdapter(m)
	m.SetData(overviewData(time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)))

	out := sidepanel.StripANSI(a.View(80, 24))
	if !strings.Contains(out, "CHANGED") || !strings.Contains(out, "internal/app/tui/model.go") {
		t.Errorf("adapter View did not render the Overview:\n%s", out)
	}
	if w, h := m.Size(); w != 80 || h != 24 {
		t.Errorf("adapter View recorded size %dx%d, want 80x24", w, h)
	}
}
