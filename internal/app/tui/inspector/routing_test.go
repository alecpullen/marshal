package inspector

import "testing"

// TestTabForTargetResolvesEveryKind pins the exported routing lookup the host
// needs: given a target, which tab shows it. An unexported helper was not
// enough — the placement host lives in another package and must not duplicate
// the mapping, or the two would disagree about where a changed file opens.
func TestTabForTargetResolvesEveryKind(t *testing.T) {
	cases := []struct {
		kind TargetKind
		want Tab
		ok   bool
	}{
		{TargetChangedFile, TabChanges, true},
		{TargetAgent, TabAgents, true},
		{TargetBlock, TabContext, true},
		{TargetKind("nonsense"), "", false},
		{TargetKind(""), "", false},
	}
	for _, tc := range cases {
		got, ok := TabForTarget(Target{Kind: tc.kind, Scope: "s1", ID: "x"})
		if ok != tc.ok {
			t.Errorf("TabForTarget(%q) ok = %v, want %v", tc.kind, ok, tc.ok)
			continue
		}
		if got != tc.want {
			t.Errorf("TabForTarget(%q) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

// TestClearStackEmptiesTheDetailStack pins the operation close() relies on.
//
// It is deliberately NOT Back-until-empty: Back pops one level and reports
// whether it did, which is right for a key press and wrong for "dismiss this
// view" — a close that popped one level would leave the user one level deep in
// a panel they just closed.
func TestClearStackEmptiesTheDetailStack(t *testing.T) {
	m := New()
	m.OpenTarget(Target{Kind: TargetChangedFile, Scope: "s1", ID: "a.go"})
	m.OpenTarget(Target{Kind: TargetChangedFile, Scope: "s1", ID: "b.go"})
	if m.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 before clearing", m.Depth())
	}

	m.ClearStack()

	if m.Depth() != 0 {
		t.Fatalf("depth = %d after ClearStack, want 0", m.Depth())
	}
	if _, ok := m.ActiveTarget(); ok {
		t.Fatal("an active target survived ClearStack")
	}
	if m.Back() {
		t.Fatal("Back() reported work after ClearStack")
	}
}

// TestClearStackPreservesTabState pins that clearing the stack is not a reset:
// the tab and its scroll/filter are where the user was reading, and closing a
// detail is not a request to forget that.
func TestClearStackPreservesTabState(t *testing.T) {
	m := New()
	m.SetState(TabOverview, TabState{Scroll: 9, Filter: "app"})
	m.OpenTarget(Target{Kind: TargetChangedFile, Scope: "s1", ID: "a.go"})

	m.ClearStack()

	if m.SelectedTab() != TabOverview {
		t.Fatalf("tab = %q after ClearStack, want overview", m.SelectedTab())
	}
	if got := m.State(TabOverview); got.Scroll != 9 || got.Filter != "app" {
		t.Fatalf("state = %+v after ClearStack, want scroll 9 filter app", got)
	}
}

// TestClearStackOnAnEmptyStackIsSafe pins idempotence: close can be called
// from any state.
func TestClearStackOnAnEmptyStackIsSafe(t *testing.T) {
	m := New()
	m.ClearStack()
	m.ClearStack()
	if m.Depth() != 0 {
		t.Fatalf("depth = %d, want 0", m.Depth())
	}
}
