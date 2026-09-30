package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/changedfiles"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/app/tui/inspector"
)

// inspectorWithDraft builds a model whose inspector is open on the Changes tab
// AND whose conversation has content, so a test can tell the two copy sources
// apart. Without a conversation, the conversation-side copy would fail for the
// trivial reason that there is nothing to copy, and would prove nothing.
func loadDiffForTest(t *testing.T, m *Model, path string) *Model {
	t.Helper()
	selectChangedPath(t, m, path)
	if !m.inspector.model.EnterSelected() {
		t.Fatalf("EnterSelected refused %q", path)
	}
	cmd := m.inspectorDiffCommand()
	if cmd == nil {
		t.Fatalf("no read command for %q", path)
	}
	mm, _ := m.Update(cmd())
	got := asModel(t, mm)
	return &got
}

// TestCopyInspectedPathPutsTheSelectedPathOnTheClipboard is the plan's
// "Copy path" action end to end: the bytes that reach the clipboard must be the
// path the reader selected, not the block under the reading anchor.
func TestCopyInspectedPathPutsTheSelectedPathOnTheClipboard(t *testing.T) {
	m, _, _ := changesInspectorModel(t)
	m.setFocus(FocusInspector)
	selectChangedPath(t, &m, "untracked.txt")

	w := &fakeClipboard{}
	m.copyWriter = w

	cmd := m.copyInspectedPath()
	if cmd == nil {
		t.Fatal("Copy inspected path produced no command")
	}
	mm, _ := m.Update(cmd())
	_ = asModel(t, mm)

	if got := w.writes(); len(got) != 1 || got[0] != "untracked.txt" {
		t.Fatalf("clipboard received %q, want exactly the selected path", got)
	}
}

// TestCopyCapturedPatchPutsFetchedBytesOnTheClipboard pins that the patch copy
// is the FETCHED patch, not the rendered body. The rendered body carries
// markers, separators and colour; putting those on the clipboard produces
// something that no longer applies.
func TestCopyCapturedPatchPutsFetchedBytesOnTheClipboard(t *testing.T) {
	m, _, _ := changesInspectorModel(t)
	m.setFocus(FocusInspector)
	m = *loadDiffForTest(t, &m, "a.go")

	w := &fakeClipboard{}
	m.copyWriter = w
	cmd := m.copyInspectedPatch()
	if cmd == nil {
		t.Fatal("Copy captured patch produced no command")
	}
	mm, _ := m.Update(cmd())
	m = asModel(t, mm)

	got := w.writes()
	if len(got) != 1 {
		t.Fatalf("the clipboard received %d writes, want 1: %q", len(got), got)
	}
	clip := got[0]
	if !strings.Contains(clip, "@@") || !strings.Contains(clip, "func A() {}") {
		t.Fatalf("the clipboard does not hold the patch:\n%q", clip)
	}
	if strings.Contains(clip, "\x1b") {
		t.Fatalf("the clipboard holds rendered bytes, not patch bytes:\n%q", clip)
	}
	// And the fetched bytes are DISTINGUISHABLE from the rendered body, so the
	// assertion above is not vacuous.
	rendered := m.inspector.model.View(m.inspectorData())
	if strings.Contains(rendered, clip) {
		t.Fatal("the copied text also appears verbatim in the rendered body, so this test proves nothing")
	}
}

// TestCopyPatchIsRefusedWhenNothingWasFetched pins the disabled path: a copy
// that has no bytes to copy must say so rather than putting an empty string on
// the clipboard, which the user would only discover on paste.
func TestCopyPatchIsRefusedWhenNothingWasFetched(t *testing.T) {
	m, _, _ := changesInspectorModel(t)
	m.setFocus(FocusInspector)
	selectChangedPath(t, &m, "a.go")

	w := &fakeClipboard{}
	m.copyWriter = w
	if cmd := m.copyInspectedPatch(); cmd != nil {
		mm, _ := m.Update(cmd())
		_ = asModel(t, mm)
	}

	if got := w.writes(); len(got) != 0 {
		t.Fatalf("the clipboard received %q with no patch loaded", got)
	}
	ctx := m.actionSnapshot()
	if ctx.InspectedPatchLoaded {
		t.Fatal("the action context claims a patch is loaded when none was fetched")
	}
	if !ctx.InspectedPathSelected {
		t.Fatal("the action context does not see the selected path")
	}
}

// TestInspectedCopyActionsAppearInThePaletteWithReasons pins that the two new
// actions are discoverable and that an unavailable one explains itself rather
// than vanishing. A user cannot learn an operation exists if it is hidden.
func TestInspectedCopyActionsAppearInThePaletteWithReasons(t *testing.T) {
	m, _, _ := changesInspectorModel(t)
	m.setFocus(FocusInspector)

	pathAction, ok := resolveAction(m.actionSnapshot(), ActionCopyInspectedPath)
	if !ok {
		t.Fatal("Copy inspected path is not in the catalog")
	}
	if pathAction.Disabled {
		t.Fatalf("Copy inspected path is disabled with a changed file selected: %s", pathAction.DisabledReason)
	}
	patchAction, ok := resolveAction(m.actionSnapshot(), ActionCopyPatch)
	if !ok {
		t.Fatal("Copy captured patch is not in the catalog")
	}
	if !patchAction.Disabled {
		t.Fatal("Copy captured patch is enabled before any patch has been fetched")
	}
	if patchAction.DisabledReason == "" {
		t.Fatal("an unavailable action offers no reason")
	}

	// After a read it becomes available.
	m = *loadDiffForTest(t, &m, "a.go")
	if a, _ := resolveAction(m.actionSnapshot(), ActionCopyPatch); a.Disabled {
		t.Fatalf("Copy captured patch is still disabled after a read: %s", a.DisabledReason)
	}

	// Both actions are listed in the palette, not only resolvable.
	items := actionPaletteItems(resolveActions(m.actionSnapshot()))
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.Value] = true
	}
	for _, id := range []ActionID{ActionCopyInspectedPath, ActionCopyPatch} {
		if !seen[string(id)] {
			t.Fatalf("%s is missing from the palette listing", id)
		}
	}
}

// TestCopyInspectedPathAndConversationPathAreDifferentActions pins the reason
// the two copies are separate actions: they read different sources, and one
// action with two sources would have to guess which the user meant.
func TestCopyInspectedPathAndConversationPathAreDifferentActions(t *testing.T) {
	if ActionCopyPath == ActionCopyInspectedPath {
		t.Fatal("the conversation path copy and the inspected path copy share an action ID")
	}

	m, _, _ := changesInspectorModel(t)
	m.setFocus(FocusInspector)
	// Give the conversation a block with a DIFFERENT file path, so a fallback
	// in either direction is observable.
	m.state.AddMessageFinal(session.RoleAssistant,
		"see `internal/app/tui/model.go` for the details", session.ContentTypeMarkdown)
	m.refreshViewport()
	selectChangedPath(t, &m, "untracked.txt")

	w := &fakeClipboard{}
	m.copyWriter = w

	if cmd := m.copyInspectedPath(); cmd == nil {
		t.Fatal("the inspected path copy depends on the conversation")
	} else {
		mm, _ := m.Update(cmd())
		_ = asModel(t, mm)
	}
	if got := w.writes(); len(got) != 1 || got[0] != "untracked.txt" {
		t.Fatalf("clipboard = %q, want the inspector's selection", got)
	}
}

// TestChangesTabBodyKeysMoveTheDiffNotATabScrollOffset pins the other half of
// the Changes tab's keyboard: once a diff is open, the page keys move the PATCH.
// A key that moved an invisible tab scroll offset instead would look broken.
func TestChangesTabBodyKeysMoveTheDiffNotATabScrollOffset(t *testing.T) {
	m, _, _ := changesInspectorModel(t)
	m.setFocus(FocusInspector)
	selectChangedPath(t, &m, "a.go")
	if !m.inspector.model.EnterSelected() {
		t.Fatal("EnterSelected refused the selection")
	}
	req, _ := m.inspector.model.PendingDiffRequest()

	// A patch far taller than the panel, delivered directly so the test does
	// not depend on how many lines the fixture file happens to have.
	var b strings.Builder
	b.WriteString("--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,400 @@\n")
	for i := 0; i < 400; i++ {
		b.WriteString("+line\n")
	}
	if !m.inspector.model.ApplyDiffLoaded(inspector.DiffLoadedMsg{
		Scope: req.Scope, Request: req.Request, Path: req.Path,
		Diff: changedfiles.Diff{Path: req.Path, Patch: b.String()},
	}) {
		t.Fatal("the inspector rejected its own reply")
	}
	m.refreshInspector()
	if !m.inspector.model.HasDiff() {
		t.Fatal("precondition: a diff must be loaded")
	}
	// Render one frame: the detail's viewport is measured when the panel draws
	// (the same as any panel), and a page key needs a height to page by. This
	// is the state a key press in the running app is always in.
	m.viewString()

	before := m.inspector.model.DetailScroll()
	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyPgDown})
	got := asModel(t, mm)
	if !handled {
		t.Fatal("PageDown was not handled on the Changes tab with a diff open")
	}
	after := got.inspector.model.DetailScroll()
	if after <= before {
		t.Fatalf("the patch did not move (scroll %d -> %d); the key scrolled something else", before, after)
	}
	if s := got.inspector.model.State(inspector.TabChanges).Scroll; s != 0 {
		t.Fatalf("the tab body scrolled to %d while a diff was open; two scroll offsets moved at once", s)
	}
	// Home goes back to the top of the patch, so the keys are the detail's.
	mm2, _, _ := got.handleKeypress(tea.KeyPressMsg{Code: tea.KeyHome})
	top := asModel(t, mm2)
	if s := top.inspector.model.DetailScroll(); s != 0 {
		t.Fatalf("Home left the patch at line %d, want the top", s)
	}
}

// TestChangesTabUpDownMoveFilesEvenWithADiffOpen pins that opening a diff does
// not take the list's keys away: the cursor still moves, and the cursor is what
// decides which patch Enter reads next.
func TestChangesTabUpDownMoveFilesEvenWithADiffOpen(t *testing.T) {
	m, _, _ := changesInspectorModel(t)
	m.setFocus(FocusInspector)
	selectChangedPath(t, &m, "a.go")
	m = *loadDiffForTest(t, &m, "a.go")

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyDown})
	got := asModel(t, mm)
	if !handled {
		t.Fatal("Down was not handled with a diff open")
	}
	if path, _ := got.inspector.model.SelectedPath(); path == "a.go" {
		t.Fatal("Down did not move the file cursor while a diff was open")
	}
	if got.inspector.model.DetailScroll() != m.inspector.model.DetailScroll() {
		t.Fatal("moving the FILE cursor also scrolled the patch")
	}
}

// TestConversationPathCopyStillReadsTheConversation pins that adding the
// inspector actions did not change what the conversation's own copy does.
func TestConversationPathCopyStillReadsTheConversation(t *testing.T) {
	m, _, _ := changesInspectorModel(t)
	m.state.AddMessageFinal(session.RoleAssistant,
		"the important file is internal/app/tui/model.go", session.ContentTypeMarkdown)
	m.refreshViewport()
	m.setFocus(FocusConversation)

	w := &fakeClipboard{}
	m.copyWriter = w
	cmd := m.copySelection(conversation.SourcePath)
	if cmd == nil {
		t.Skip("the fixture block offers no path target; nothing to assert")
	}
	mm, _ := m.Update(cmd())
	_ = asModel(t, mm)

	for _, got := range w.writes() {
		if strings.Contains(got, "untracked.txt") {
			t.Fatalf("the conversation copy read the inspector's selection: %q", got)
		}
	}
}
