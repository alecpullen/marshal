package inspector

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/changedfiles"
)

// keyPress builds the KeyPressMsg the dock adapter switches on.
func keyPress(name string) tea.KeyPressMsg {
	switch name {
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	}
	return tea.KeyPressMsg{}
}

// TestTabsDoNotCrosstalkThroughTheDetailBody is the regression for the review's
// finding that the three list tabs shared ONE DetailView.
//
// The reproduced sequence was: open a Context row (its body lands in the shared
// view), open a diff on Changes (its rendered text overwrites the shared view),
// view the Context tab once (it re-wraps its stored body back over the diff),
// then view the Changes tab — which does NOT re-assert its content, so it drew
// the CONTEXT body under the DIFF's path label. On screen that is a body
// labelled with the wrong file, which the reader has no way to detect, and the
// paging keys still moved the context body underneath it.
//
// Each tab now owns its body, so the sequence cannot mislabel either tab.
func TestTabsDoNotCrosstalkThroughTheDetailBody(t *testing.T) {
	m := New()
	m.Resize(80, 30)

	// (1) Open a Context row.
	d := packFixture()
	m.SetContext(d)
	if !m.OpenContextRowByLabel("repo map") {
		t.Fatal("could not open the repo-map section")
	}
	contextBody := m.contextDetail().Content()
	if !strings.Contains(contextBody, "internal/") {
		t.Fatalf("precondition: the context body is not where it was expected:\n%s", contextBody)
	}

	// (2) Open a diff on Changes. This writes the DIFF into the Changes body.
	// The scope is left at its zero value so the reply is accepted without a
	// session change that would discard the context state under test.
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go"))
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused a selection that exists")
	}
	req, _ := m.PendingDiffRequest()
	m.ApplyDiffLoaded(DiffLoadedMsg{
		Scope: req.Scope, Request: req.Request, Path: req.Path,
		Diff: changedfiles.Diff{Path: req.Path, Patch: "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n+DIFF_MARKER\n"},
	})
	if !strings.Contains(m.changesDetail().Content(), "DIFF_MARKER") {
		t.Fatalf("precondition: the diff did not reach the Changes body:\n%s", m.changesDetail().Content())
	}

	// (3) View the Context tab, which re-wraps its own stored body.
	m.Open(TabContext)
	contextView := m.viewContext()
	if !strings.Contains(contextView, "repo map") {
		t.Fatalf("the Context tab does not render its own row:\n%s", contextView)
	}

	// (4) View the Changes tab. It must show the DIFF, under the diff's label,
	// and must NOT show the context body.
	m.Open(TabChanges)
	changesView := m.viewChanges()
	if !strings.Contains(changesView, "DIFF_MARKER") {
		t.Fatalf("the Changes tab lost its diff after the Context tab was drawn:\n%s", changesView)
	}
	if strings.Contains(changesView, "internal/") {
		t.Fatalf("the Changes tab is showing the Context body under the diff's label:\n%s", changesView)
	}
	// The label must name the changed file, not the context row.
	changesDetail := m.changesDetail()
	if changesDetail.Label() != "a.go" {
		t.Fatalf("the Changes body label = %q, want the diff's path %q", changesDetail.Label(), "a.go")
	}
	// And the Context body must be untouched by all of the above.
	if got := m.contextDetail().Content(); got != contextBody {
		t.Fatalf("the Context body was overwritten by the diff:\n--- got ---\n%s\n--- want ---\n%s", got, contextBody)
	}
}

// TestActiveDetailFollowsTheSelectedTab pins the backward-compatible accessor
// the per-tab split adds: a caller holding only the Model can reach whichever
// body the tab on display owns.
func TestActiveDetailFollowsTheSelectedTab(t *testing.T) {
	m := New()
	m.Resize(80, 30)
	m.SetScope("s1")
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go"))
	m.SetAgents(agentsFixture(1))

	m.Open(TabChanges)
	if m.ActiveDetail() != m.changesDetail() {
		t.Fatal("ActiveDetail is not the Changes body while Changes is selected")
	}
	m.Open(TabAgents)
	if m.ActiveDetail() != m.agentsDetail() {
		t.Fatal("ActiveDetail is not the Agents body while Agents is selected")
	}
	m.Open(TabContext)
	if m.ActiveDetail() != m.contextDetail() {
		t.Fatal("ActiveDetail is not the Context body while Context is selected")
	}
	// Overview has no body of its own; the accessor still returns a usable view
	// rather than nil, so a caller need not special-case it.
	m.Open(TabOverview)
	if m.ActiveDetail() == nil {
		t.Fatal("ActiveDetail returned nil for the Overview tab")
	}
}

// TestDockAdapterArrowKeysMoveTheListCursor pins the review's M-7 finding.
//
// The adapter's up/down used to write TabState.Scroll, but the three list tabs
// are cursor-driven and never read that offset — so when the inspector was
// docked without focus, pressing down on Changes/Agents/Context silently did
// nothing. The keys now move the cursor, and the Overview keeps its scroll.
func TestDockAdapterArrowKeysMoveTheListCursor(t *testing.T) {
	m := New()
	m.Resize(80, 30)
	a := NewDockAdapter(m)
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go", "c.go"))

	m.Open(TabChanges)
	a.Update(keyPress("down"))
	if got, _ := m.SelectedPath(); got != "b.go" {
		t.Fatalf("down on Changes selected %q, want b.go", got)
	}
	a.Update(keyPress("up"))
	if got, _ := m.SelectedPath(); got != "a.go" {
		t.Fatalf("up on Changes selected %q, want a.go", got)
	}

	m.Open(TabAgents)
	m.SetAgents(agentsFixture(1, 2, 3))
	a.Update(keyPress("down"))
	if got := m.AgentIDSelected(); got != 2 {
		t.Fatalf("down on Agents selected %d, want 2", got)
	}

	m.Open(TabContext)
	m.SetContext(packFixture())
	a.Update(keyPress("down"))
	if got := m.ContextCursor(); got != 1 {
		t.Fatalf("down on Context moved the cursor to %d, want 1", got)
	}

	// The Overview still scrolls: it has no row cursor to move. It is driven
	// from a populated snapshot, because its scroll bound is derived from that
	// data (the adapter's own View/SetData contract) and an empty snapshot
	// clamps the scroll to zero.
	om := overviewModel(overviewData(overviewNow), 80, 24)
	oa := NewDockAdapter(om)
	oa.Update(keyPress("down"))
	if got := om.State(TabOverview).Scroll; got != 1 {
		t.Fatalf("down on Overview left Scroll at %d, want 1", got)
	}
}
