package inspector

import (
	"strings"
	"testing"

	"marshal/internal/app/tui/changedfiles"
	"marshal/internal/diffview"
)

// stripANSIForTest removes SGR sequences so a test can assert on what the reader
// actually sees rather than on the escape codes around it.
func stripANSIForTest(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !((s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z')) {
				j++
			}
			i = min(j+1, len(s))
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// changesSnapshot builds a snapshot from paths, in order.
func changesSnapshot(status changedfiles.Status, paths ...string) changedfiles.Snapshot {
	files := make([]changedfiles.File, 0, len(paths))
	for _, p := range paths {
		files = append(files, changedfiles.File{
			Path: p, Status: 'M', Added: 1, Removed: 1, CountsKnown: true,
		})
	}
	return changedfiles.Snapshot{
		Status: status, BaseRef: "HEAD", BaseOID: "abc123", Files: files,
	}
}

// --- selection identity ------------------------------------------------

// TestChangesSelectionIsKeyedByPathNotIndex is the rule that makes a refresh
// bearable: git reorders its output, and a reader who selected a file must still
// have that file selected afterwards. Index-keyed selection silently moves the
// cursor to a different file.
func TestChangesSelectionIsKeyedByPathNotIndex(t *testing.T) {
	m := New()
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go", "c.go"))
	m.MoveChangesSelection(1) // b.go

	path, ok := m.SelectedPath()
	if !ok || path != "b.go" {
		t.Fatalf("selection = %q/%v, want b.go", path, ok)
	}

	// The same three files, reordered.
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "c.go", "b.go", "a.go"))

	path, ok = m.SelectedPath()
	if !ok || path != "b.go" {
		t.Fatalf("selection after a reorder = %q/%v, want b.go still selected", path, ok)
	}
}

// TestChangesVanishedSelectionTakesTheNearestNeighbourAndSaysSo pins the
// honest degradation: when the selected file stops being changed, the cursor
// must land somewhere sensible AND the reader must be told the file they were
// on is gone. Silently selecting a different file is how someone reads the
// wrong diff.
func TestChangesVanishedSelectionTakesTheNearestNeighbourAndSaysSo(t *testing.T) {
	m := New()
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go", "c.go"))
	// Select the last one, so clamping has a boundary to respect.
	m.MoveChangesSelection(2)
	if got, _ := m.SelectedPath(); got != "c.go" {
		t.Fatalf("precondition: selected %q, want c.go", got)
	}

	// c.go is no longer changed.
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go"))

	got, ok := m.SelectedPath()
	if !ok {
		t.Fatal("no selection after the previous one vanished")
	}
	if got == "c.go" {
		t.Fatal("the selection still names a path that is no longer changed")
	}
	if got != "b.go" {
		t.Fatalf("selection = %q, want the nearest surviving neighbour b.go", got)
	}
	if !m.SelectionVanished() {
		t.Fatal("the vanished selection was not reported; the reader cannot tell the file is gone")
	}
}

// TestChangesSurvivingSelectionClearsTheVanishedFlag pins the converse: a
// refresh that keeps the selected file must clear the notice, or the label
// would stick after the file came back.
func TestChangesSurvivingSelectionClearsTheVanishedFlag(t *testing.T) {
	m := New()
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go"))
	m.MoveChangesSelection(1)

	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go")) // b.go vanishes
	if !m.SelectionVanished() {
		t.Fatal("a vanished selection was not reported")
	}

	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go")) // b.go returns
	// The selection is now a.go (the neighbour), so selecting b.go again is what
	// clears the notice.
	m.MoveChangesSelection(1)
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go"))
	if m.SelectionVanished() {
		t.Fatal("the vanished flag stuck after the selection survived a refresh")
	}
}

// --- truthful counts ---------------------------------------------------

// TestChangesRowsNeverInventACount pins the rule Task 6 established at the data
// layer and this view must not undo: an unknown count renders as nothing, not
// as a number. A wrong number on screen is indistinguishable from a right one.
func TestChangesRowsNeverInventACount(t *testing.T) {
	snap := changedfiles.Snapshot{
		Status: changedfiles.StatusOK,
		Files: []changedfiles.File{
			{Path: "untracked.txt", Status: 'A', CountsKnown: false},
			{Path: "binary.dat", Status: 'M', Kind: changedfiles.FileBinary, CountsKnown: false},
			{Path: "known.go", Status: 'M', Added: 4, Removed: 2, CountsKnown: true},
		},
	}
	m := New()
	m.SetChanges(snap)
	view := m.viewChanges()

	if strings.Contains(view, "+1") {
		t.Fatalf("the rendered rows contain a fabricated +1:\n%s", view)
	}
	if !strings.Contains(view, "untracked.txt") {
		t.Fatalf("the untracked file is missing from the list:\n%s", view)
	}
	if !strings.Contains(view, "+4") || !strings.Contains(view, "-2") {
		t.Fatalf("a known count was not rendered:\n%s", view)
	}
}

// --- clean versus failed -----------------------------------------------

// TestChangesFailedReadNeverRendersAsClean is one of the plan's explicit
// acceptance criteria, and the defect this whole task exists to remove: the old
// reader returned nil on every error, so an unreadable repository rendered as
// "nothing changed".
func TestChangesFailedReadNeverRendersAsClean(t *testing.T) {
	clean := New()
	clean.SetChanges(changedfiles.Snapshot{Status: changedfiles.StatusOK})
	cleanText := clean.viewChanges()
	if !strings.Contains(strings.ToLower(cleanText), "no ") && !strings.Contains(strings.ToLower(cleanText), "clean") {
		t.Fatalf("a clean tree does not say so:\n%s", cleanText)
	}

	for _, status := range []changedfiles.Status{
		changedfiles.StatusMissingBase,
		changedfiles.StatusNotARepo,
		changedfiles.StatusTimeout,
		changedfiles.StatusCancelled,
		changedfiles.StatusFailed,
	} {
		m := New()
		m.SetChanges(changedfiles.Snapshot{Status: status, Err: errFor(status)})
		got := m.viewChanges()

		if got == cleanText {
			t.Fatalf("status %q renders identically to a clean tree:\n%s", status, got)
		}
		// It must not claim there are no changes, because it does not know.
		lower := strings.ToLower(got)
		if strings.Contains(lower, "no changes") {
			t.Fatalf("status %q renders as \"no changes\":\n%s", status, got)
		}
		// And it must say something went wrong, not just show an empty list.
		if !strings.Contains(lower, "could not") && !strings.Contains(lower, "failed") &&
			!strings.Contains(lower, "unavailable") && !strings.Contains(lower, "not a") &&
			!strings.Contains(lower, "timed out") && !strings.Contains(lower, "cancelled") {
			t.Fatalf("status %q explains nothing to the reader:\n%s", status, got)
		}
	}
}

// --- loading ------------------------------------------------------------

// TestChangesShowsALoadingRowImmediately pins the plan's step: a selection that
// needs a diff must show a loading row BEFORE any reply arrives, so a slow git
// read looks like work rather than a frozen panel.
func TestChangesShowsALoadingRowImmediately(t *testing.T) {
	m := New()
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go"))
	m.Resize(80, 20)

	if _, ok := m.PendingDiffRequest(); ok {
		t.Fatal("a diff was requested before anything was selected")
	}

	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused a selection that exists")
	}
	req, ok := m.PendingDiffRequest()
	if !ok {
		t.Fatal("EnterSelected stamped no request")
	}
	if req.Path != "a.go" {
		t.Fatalf("request path = %q, want a.go", req.Path)
	}
	if !m.ChangesLoading() {
		t.Fatal("ChangesLoading is false immediately after a selection")
	}
	view := m.viewChanges()
	if !strings.Contains(strings.ToLower(view), "loading") {
		t.Fatalf("no loading row while a diff is in flight:\n%s", view)
	}
}

// TestPendingDiffRequestIsIdempotent pins the storm guard: a keystroke must not
// be able to spawn repeated git processes for the same selection.
func TestPendingDiffRequestIsIdempotent(t *testing.T) {
	m := New()
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go"))
	m.Resize(80, 20)
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused the selection")
	}

	first, ok := m.PendingDiffRequest()
	if !ok {
		t.Fatal("no pending request after a selection")
	}
	second, ok := m.PendingDiffRequest()
	if ok {
		t.Fatalf("a second pending request was offered (%+v); one selection must yield one request", second)
	}

	// Moving the cursor alone requests nothing: a diff is read on Enter, not on
	// every keystroke. Only a NEW selection that is opened does.
	m.MoveChangesSelection(1)
	if _, ok := m.PendingDiffRequest(); ok {
		t.Fatal("moving the cursor alone started a read; browsing must not spawn git processes")
	}
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused the new selection")
	}
	if next, ok := m.PendingDiffRequest(); !ok || next.Path == first.Path {
		t.Fatalf("opening a different file produced %+v/%v, want a request for the new path", next, ok)
	}
}

// --- stale replies ------------------------------------------------------

// TestChangesRejectsStaleAndForeignDiffReplies is the ordering rule: an older
// completion must never replace the newer selection's content, and a reply from
// a closed session must be dropped entirely.
func TestChangesRejectsStaleAndForeignDiffReplies(t *testing.T) {
	m := New()
	m.SetScope("s1")
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go"))
	m.Resize(80, 20)

	// Select a.go, then b.go: two requests, and only the second is current.
	// The request is handed out by PendingDiffRequest rather than returned by
	// EnterSelected, because the caller may consume it asynchronously.
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused the first selection")
	}
	firstReq, ok := m.PendingDiffRequest()
	if !ok || firstReq.Path != "a.go" {
		t.Fatalf("first request = %+v/%v, want a.go", firstReq, ok)
	}
	m.MoveChangesSelection(1)
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused the second selection")
	}
	secondReq, ok := m.PendingDiffRequest()
	if !ok || secondReq.Path != "b.go" {
		t.Fatalf("second request = %+v/%v, want b.go", secondReq, ok)
	}
	if !m.ApplyDiffLoaded(DiffLoadedMsg{
		Scope: secondReq.Scope, Request: secondReq.Request, Path: secondReq.Path,
		Diff: changedfiles.Diff{Path: "b.go", Patch: "+++ b/b.go\n+second\n"},
	}) {
		t.Fatal("the current reply was rejected")
	}
	if m.ChangesLoading() {
		t.Fatal("a valid reply left the loading state set")
	}

	// The FIRST reply arrives late. It must be dropped, not applied over b.go.
	if m.ApplyDiffLoaded(DiffLoadedMsg{
		Scope: firstReq.Scope, Request: firstReq.Request, Path: firstReq.Path,
		Diff: changedfiles.Diff{Path: "a.go", Patch: "+++ b/a.go\n+stale\n"},
	}) {
		t.Fatal("a superseded reply was accepted")
	}
	if got := m.detail.View(""); strings.Contains(got, "stale") {
		t.Fatalf("a superseded reply replaced the newer content:\n%s", got)
	}

	// A reply from another scope must be rejected too.
	if m.ApplyDiffLoaded(DiffLoadedMsg{
		Scope: "other-session", Request: secondReq.Request, Path: "b.go",
		Diff: changedfiles.Diff{Path: "b.go", Patch: "+++ b/b.go\n+foreign\n"},
	}) {
		t.Fatal("a reply from a different scope was accepted")
	}
}

// --- rendering rules ----------------------------------------------------

// TestChangesRejectsAStaleReplyForThePathStillSelected is the case the request id
// actually exists for, and it was MISSING.
//
// TestChangesRejectsStaleAndForeignDiffReplies above sends the late reply with
// Path "a.go" while the current selection is "b.go", so the PATH guard rejects it
// and the request-id guard is never exercised. Mutation testing confirmed the
// gap: deleting the `msg.Request != m.changes.currentReq` check from
// ApplyDiffLoaded left the whole TUI suite passing.
//
// The unguarded case is select a.go → move to b.go → come back to a.go. Now the
// late reply's path MATCHES the current selection, so only the request id can
// stop it — and it must, because that reply describes a read issued for the
// reader's FIRST visit to a.go, not the one they are on.
func TestChangesRejectsAStaleReplyForThePathStillSelected(t *testing.T) {
	m := New()
	m.SetScope("s1")
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go"))
	m.Resize(80, 20)

	// Visit 1: a.go.
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused a.go")
	}
	staleReq, ok := m.PendingDiffRequest()
	if !ok || staleReq.Path != "a.go" {
		t.Fatalf("first request = %+v/%v, want a.go", staleReq, ok)
	}

	// Move away to b.go and ask for it.
	m.MoveChangesSelection(1)
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused b.go")
	}
	if _, ok := m.PendingDiffRequest(); !ok {
		t.Fatal("no request for b.go")
	}

	// Come BACK to a.go and ask again. The path is the same as visit 1; the
	// request id is not.
	m.MoveChangesSelection(-1)
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused the second a.go visit")
	}
	freshReq, ok := m.PendingDiffRequest()
	if !ok || freshReq.Path != "a.go" {
		t.Fatalf("second a.go request = %+v/%v, want a.go", freshReq, ok)
	}
	if freshReq.Request == staleReq.Request {
		t.Fatalf("both a.go requests carry the id %d, so no guard could tell them apart",
			freshReq.Request)
	}

	// The CURRENT a.go reply lands.
	if !m.ApplyDiffLoaded(DiffLoadedMsg{
		Scope: freshReq.Scope, Request: freshReq.Request, Path: freshReq.Path,
		Diff: changedfiles.Diff{Path: "a.go", Patch: "+++ b/a.go\n+fresh\n"},
	}) {
		t.Fatal("the current reply was rejected")
	}

	// Now visit 1's reply arrives late. Same scope, same PATH, superseded id —
	// the path guard cannot help, so this asserts the id guard on its own.
	if m.ApplyDiffLoaded(DiffLoadedMsg{
		Scope: staleReq.Scope, Request: staleReq.Request, Path: staleReq.Path,
		Diff: changedfiles.Diff{Path: "a.go", Patch: "+++ b/a.go\n+stale\n"},
	}) {
		t.Error("a superseded reply for the SAME path was accepted — the request-id guard is not doing its job")
	}
	if got := m.detail.View(""); strings.Contains(got, "stale") {
		t.Fatalf("the stale reply replaced the current content:\n%s", got)
	}
	if !strings.Contains(m.detail.View(""), "fresh") {
		t.Fatalf("the current content is no longer on screen:\n%s", m.detail.View(""))
	}
}

// TestChangesDiffRenderingUsesTheDetailWidth pins the layout rule: unified by
// default, side-by-side only when the actual detail width meets diffview's
// threshold. The width used must be the DETAIL's, not the inspector's whole
// column.
func TestChangesDiffRenderingUsesTheDetailWidth(t *testing.T) {
	m := New()
	m.SetScope("s1")
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go"))
	patch := "--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n-old\n+new\n"

	m.Resize(80, 20)
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused the selection")
	}
	req, _ := m.PendingDiffRequest()
	m.ApplyDiffLoaded(DiffLoadedMsg{Scope: req.Scope, Request: req.Request, Path: "a.go",
		Diff: changedfiles.Diff{Path: "a.go", Patch: patch}})

	narrow := stripANSIForTest(m.detail.View(""))
	// diffview renders a unified line as "+ " + content, so the text appears
	// after the marker rather than glued to it.
	if !strings.Contains(narrow, "new") {
		t.Fatalf("the diff body is not rendered at all:\n%s", narrow)
	}
	// Unified renders the marker prefixes inline; side-by-side uses a separator
	// column. The narrow render must be the unified one.
	if strings.Contains(narrow, "│") {
		t.Fatalf("a narrow detail rendered side-by-side:\n%s", narrow)
	}

	// The mode is chosen from the width, and the threshold must be diffview's
	// own — not a second copy of the number that can drift away from it.
	if diffViewMode(sideBySideThreshold) != diffview.ModeSideBySide {
		t.Fatal("the threshold does not select side-by-side")
	}
	if diffViewMode(sideBySideThreshold-1) != diffview.ModeUnified {
		t.Fatal("a width just below the threshold does not select unified")
	}
}

// TestChangesTruncatedFetchIsDisclosed pins the honesty rule at the view level:
// when the DIFF FETCH was capped, the reader must be told, and the view must not
// suggest the remaining bytes are reachable.
func TestChangesTruncatedFetchIsDisclosed(t *testing.T) {
	m := New()
	m.SetScope("s1")
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "big.bin"))
	m.Resize(80, 20)
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused the selection")
	}
	req, _ := m.PendingDiffRequest()
	m.ApplyDiffLoaded(DiffLoadedMsg{Scope: req.Scope, Request: req.Request, Path: "big.bin",
		Diff: changedfiles.Diff{Path: "big.bin", Patch: "+++ b/big.bin\n+partial\n", Truncated: true}})

	if !m.detail.Truncated() {
		t.Fatal("a truncated fetch did not reach the detail view")
	}
	view := m.detail.View("")
	if !strings.Contains(view, "incomplete") {
		t.Fatalf("the truncated fetch is not disclosed:\n%s", view)
	}
}

// --- copying ------------------------------------------------------------

// TestCapturedPatchIsTheFetchedBytesNotTheRenderedView is the copy-integrity
// rule: the clipboard must receive the patch as fetched, not the rendered,
// coloured, marker-prefixed body the reader sees. Putting the rendered form on
// the clipboard produces something that no longer applies.
func TestCapturedPatchIsTheFetchedBytesNotTheRenderedView(t *testing.T) {
	patch := "--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n-old line\n+new line\n"
	m := New()
	m.SetScope("s1")
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go"))
	m.Resize(80, 20)
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused the selection")
	}
	req, _ := m.PendingDiffRequest()
	if !m.ApplyDiffLoaded(DiffLoadedMsg{Scope: req.Scope, Request: req.Request, Path: "a.go",
		Diff: changedfiles.Diff{Path: "a.go", Patch: patch}}) {
		t.Fatal("the reply was rejected")
	}

	text, label, truncated, ok := m.CapturedPatch()
	if !ok {
		t.Fatal("CapturedPatch reported nothing to copy after a diff was loaded")
	}
	if text != patch {
		t.Fatalf("captured patch =\n%q\nwant the fetched bytes\n%q", text, patch)
	}
	if truncated {
		t.Fatal("an un-truncated fetch was labelled truncated")
	}
	if !strings.Contains(label, "a.go") {
		t.Fatalf("label %q does not name the file it copied", label)
	}
	if strings.Contains(text, "\x1b") {
		t.Fatalf("the captured patch carries ANSI escapes, which are not patch bytes:\n%q", text)
	}
}

// TestCapturedPatchSaysWhenTheFetchWasCapped pins the honesty rule for a copy:
// a patch that is a prefix must be labelled as one, because the reader finds
// out otherwise when they paste it somewhere it does not apply.
func TestCapturedPatchSaysWhenTheFetchWasCapped(t *testing.T) {
	m := New()
	m.SetScope("s1")
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "big.go"))
	m.Resize(80, 20)
	m.EnterSelected()
	req, _ := m.PendingDiffRequest()
	m.ApplyDiffLoaded(DiffLoadedMsg{Scope: req.Scope, Request: req.Request, Path: "big.go",
		Diff: changedfiles.Diff{Path: "big.go", Patch: "--- a/big.go\n+partial\n", Truncated: true}})

	_, label, truncated, ok := m.CapturedPatch()
	if !ok {
		t.Fatal("nothing to copy")
	}
	if !truncated {
		t.Fatal("a capped fetch was reported as complete")
	}
	lower := strings.ToLower(label)
	if !strings.Contains(lower, "prefix") && !strings.Contains(lower, "truncat") && !strings.Contains(lower, "partial") {
		t.Fatalf("the label %q does not disclose the cap", label)
	}
}

// TestCapturedPatchIsEmptyUntilAReadLands pins that a copy cannot grab the
// PREVIOUS file's bytes. Selecting a new file clears the captured patch, so a
// copy taken mid-load copies nothing rather than the wrong file.
func TestCapturedPatchIsEmptyUntilAReadLands(t *testing.T) {
	m := New()
	m.SetScope("s1")
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go"))
	m.Resize(80, 20)

	m.EnterSelected()
	req, _ := m.PendingDiffRequest()
	m.ApplyDiffLoaded(DiffLoadedMsg{Scope: req.Scope, Request: req.Request, Path: "a.go",
		Diff: changedfiles.Diff{Path: "a.go", Patch: "--- a/a.go\n+a\n"}})
	if _, _, _, ok := m.CapturedPatch(); !ok {
		t.Fatal("precondition: a.go's patch must be captured")
	}

	// Move to b.go and open it: the read is now in flight.
	m.MoveChangesSelection(1)
	m.EnterSelected()
	if text, _, _, ok := m.CapturedPatch(); ok {
		t.Fatalf("a copy during the load would have grabbed a.go's bytes: %q", text)
	}
	if path, ok := m.CapturePath(); !ok || path != "b.go" {
		t.Fatalf("CapturePath = %q/%v during the load, want b.go to stay copyable", path, ok)
	}
}

// TestCapturePathIsTheSelectedChangedFile pins that the path copy follows the
// cursor rather than a stale selection.
func TestCapturePathIsTheSelectedChangedFile(t *testing.T) {
	m := New()
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go"))
	m.Resize(80, 20)

	if got, _ := m.CapturePath(); got != "a.go" {
		t.Fatalf("CapturePath = %q, want the first row", got)
	}
	m.MoveChangesSelection(1)
	if got, _ := m.CapturePath(); got != "b.go" {
		t.Fatalf("CapturePath = %q after moving, want b.go", got)
	}

	empty := New()
	if _, ok := empty.CapturePath(); ok {
		t.Fatal("CapturePath offered a path with nothing selected")
	}
}

// TestChangesBinaryDeletedRenamedUntrackedHaveDistinctCopy pins that each file
// kind says something specific. "Binary", "deleted", "renamed" and "untracked"
// need different words, because the reader's next action differs for each.
func TestChangesBinaryDeletedRenamedUntrackedHaveDistinctCopy(t *testing.T) {
	snap := changedfiles.Snapshot{
		Status: changedfiles.StatusOK,
		Files: []changedfiles.File{
			{Path: "bin.dat", Status: 'M', Kind: changedfiles.FileBinary, CountsKnown: false},
			{Path: "gone.go", Status: 'D', Kind: changedfiles.FileDeleted, CountsKnown: true, Removed: 3},
			{Path: "moved.go", OldPath: "was.go", Status: 'R', Kind: changedfiles.FileRenamed, CountsKnown: true},
			{Path: "fresh.txt", Status: 'A', Kind: changedfiles.FileUntracked, CountsKnown: false},
		},
	}
	m := New()
	m.SetChanges(snap)
	view := m.viewChanges()

	for _, want := range []string{"bin.dat", "gone.go", "moved.go", "fresh.txt"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the list omits %q:\n%s", want, view)
		}
	}
	// A rename must show where it came from — that is the whole point of
	// recording OldPath.
	if !strings.Contains(view, "was.go") {
		t.Fatalf("a rename does not show its old path:\n%s", view)
	}
}

// errFor gives a distinct error per status so the message can be asserted to
// mention the problem rather than merely differing.
func errFor(status changedfiles.Status) error {
	return &statusError{status}
}

type statusError struct{ status changedfiles.Status }

func (e *statusError) Error() string { return "git: " + string(e.status) }
