package inspector

import (
	"fmt"
	"strings"

	"marshal/internal/app/tui/changedfiles"
	"marshal/internal/diffview"
)

// sideBySideThreshold is diffview's own width floor for the two-column layout.
//
// It is mirrored here rather than exported from diffview because the DECISION
// belongs to this view — the mode is chosen from the detail's width, and
// diffview applies whatever mode it is given. diffview's ModeAuto uses the same
// number, and a test pins the two together so they cannot drift.
const sideBySideThreshold = 120

// changeRow is one row of the Changes list. The list is a slice rather than a
// listpanel model because these rows are read-only and carry no editing
// semantics; the cursor is a single index, and the row's identity is its PATH.
type changeRow struct {
	file changedfiles.File
}

// changesState is the Changes tab's own navigation state.
type changesState struct {
	snapshot changedfiles.Snapshot
	rows     []changeRow
	cursor   int
	// selected is the path the cursor was last on. It is the SOURCE OF TRUTH
	// for selection, not the cursor index: git reorders its output between
	// refreshes, and an index-based selection would silently move to a
	// different file.
	selected string
	// vanished records that the selected path left the changed set. The reader
	// must be told, because the diff on screen no longer describes a pending
	// change.
	vanished bool

	// loading is true while a diff for the current selection is in flight.
	loading bool
	// pending is the request the caller has not been asked for yet. It is
	// cleared when handed out, so asking twice cannot spawn a second git
	// process for the same selection.
	pending    DiffRequest
	hasPending bool
	// currentReq is the id of the request whose reply is still wanted. A reply
	// with any other id is superseded.
	currentReq uint64
	// currentPath is the path that request was for.
	currentPath string

	// patch is the FETCHED patch text for currentPath, before rendering, and
	// patchTruncated records that the fetch was capped.
	//
	// The diff on screen is not what a copy must put on the clipboard: it
	// carries diffview's own markers, separators and colour, none of which are
	// patch bytes. Keeping the fetched text is what lets "copy the patch" mean
	// the patch.
	patch          string
	patchTruncated bool
	// currentDiff is the reply the patch came from, so a copy can explain a
	// file that has no patch text (binary, deleted, untracked) without a
	// second read.
	currentDiff changedfiles.Diff
	// loaded records that a reply has landed for the current request. It is
	// what distinguishes "the read is still in flight" from "the read came
	// back and there are no bytes": without it, a copy taken mid-load would
	// have to guess, and guessing produces either the previous file's patch or
	// a confident "no changes".
	loaded bool
}

// ChangesSnapshot returns the snapshot the list came from.
func (m *Model) ChangesSnapshot() changedfiles.Snapshot { return m.changes.snapshot }

// SelectionVanished reports whether the previously selected path stopped being
// changed, so a caller can label the detail as describing a stale change.
func (m *Model) SelectionVanished() bool { return m.changes.vanished }

// SelectedPath returns the selected changed path, if any.
func (m *Model) SelectedPath() (string, bool) {
	if m.changes.selected == "" {
		return "", false
	}
	for _, row := range m.changes.rows {
		if row.file.Path == m.changes.selected {
			return m.changes.selected, true
		}
	}
	return m.changes.selected, false
}

// ChangesLoading reports whether a diff is in flight for the current selection.
func (m *Model) ChangesLoading() bool { return m.changes.loading }

// SetChanges replaces the list without disturbing a selection that still exists.
//
// The rules, in order:
//
//  1. A selection whose path is still present is KEPT, whatever index it now
//     occupies. Reordering must not move the reader's cursor.
//  2. A selection whose path is gone falls to the item now at the same index
//     (clamped), and is FLAGGED. Silently selecting a neighbour is how a reader
//     ends up studying the wrong diff.
//  3. With nothing selected before, the first row is selected so Enter does
//     something predictable.
//
// An in-flight load is NOT cancelled here: a refresh is not a navigation, and
// dropping the reply would leave the loading row on screen forever.
func (m *Model) SetChanges(snap changedfiles.Snapshot) {
	prev := m.changes.selected
	m.changes.snapshot = snap

	rows := make([]changeRow, 0, len(snap.Files))
	for _, f := range snap.Files {
		rows = append(rows, changeRow{file: f})
	}
	m.changes.rows = rows

	switch {
	case len(rows) == 0:
		m.changes.selected = ""
		m.changes.cursor = 0
		m.changes.vanished = false
	case prev == "":
		m.changes.cursor = 0
		m.changes.selected = rows[0].file.Path
		m.changes.vanished = false
	default:
		if idx := m.rowIndexForPath(prev); idx >= 0 {
			m.changes.cursor = idx
			m.changes.selected = prev
			m.changes.vanished = false
			return
		}
		// The selected path is gone.
		idx := min(m.changes.cursor, len(rows)-1)
		idx = max(idx, 0)
		m.changes.cursor = idx
		m.changes.selected = rows[idx].file.Path
		m.changes.vanished = true
	}
}

// rowIndexForPath returns the row index holding path, or -1.
func (m *Model) rowIndexForPath(path string) int {
	for i, row := range m.changes.rows {
		if row.file.Path == path {
			return i
		}
	}
	return -1
}

// selectRow moves the cursor to an index and records the path it lands on.
func (m *Model) selectRow(idx int) {
	if len(m.changes.rows) == 0 {
		return
	}
	idx = min(max(idx, 0), len(m.changes.rows)-1)
	m.changes.cursor = idx
	m.changes.selected = m.changes.rows[idx].file.Path
	// Moving to a real row clears the stale label: the reader is now on a file
	// that IS changed.
	m.changes.vanished = false
}

// MoveChangesSelection moves the cursor by delta rows.
func (m *Model) MoveChangesSelection(delta int) {
	if delta == 0 || len(m.changes.rows) == 0 {
		return
	}
	m.selectRow(m.changes.cursor + delta)
}

// EnterSelected opens the detail for the selected path.
//
// It STAMPS the request; the caller then collects it with PendingDiffRequest.
// It deliberately does not also return the request, because two ways to obtain
// the same request is two ways to run the same git read twice — the caller
// would have to know which one consumes it.
//
// It reports false when there is nothing to open, so a key handler can fall
// through to a different Enter meaning rather than swallowing the key.
func (m *Model) EnterSelected() bool {
	if len(m.changes.rows) == 0 {
		return false
	}
	path, _ := m.SelectedPath()
	if path == "" {
		return false
	}
	m.requestDiff(path)
	return true
}

// requestDiff stamps a new request for path. Any earlier request id becomes
// stale by construction, because currentReq moves on.
func (m *Model) requestDiff(path string) {
	m.changes.currentReq = m.NextRequest()
	m.changes.currentPath = path
	m.changes.loading = true
	// The previous file's patch is discarded here rather than left in place
	// until the reply lands. Keeping it would mean a copy taken during the load
	// grabbed the PREVIOUS file's bytes while labelled with the new path — the
	// worst kind of wrong, because it looks right.
	m.changes.patch = ""
	m.changes.patchTruncated = false
	m.changes.currentDiff = changedfiles.Diff{}
	m.changes.loaded = false
	m.changes.pending = DiffRequest{
		Scope:   m.Scope(),
		Request: m.changes.currentReq,
		Path:    path,
	}
	m.changes.hasPending = true
}

// PendingDiffRequest hands out the request the caller should run, once.
//
// It is nil-on-repeat on purpose: a key repeat or a click that lands twice must
// not start two git processes for the same file, and the caller has no other way
// to know it has already been asked.
func (m *Model) PendingDiffRequest() (DiffRequest, bool) {
	if !m.changes.hasPending {
		return DiffRequest{}, false
	}
	req := m.changes.pending
	m.changes.hasPending = false
	return req, true
}

// DiffLoadedMsg carries one finished per-file diff read.
type DiffLoadedMsg struct {
	// Scope is the session/state identity the request was issued under.
	Scope string
	// Request is the id returned by PendingDiffRequest.
	Request uint64
	// Path is the file the diff describes.
	Path string
	// Diff is the result, including its own failure status.
	Diff changedfiles.Diff
}

// DiffRequest is what the caller must run to satisfy a selection.
type DiffRequest struct {
	Scope   string
	Request uint64
	Path    string
}

// ApplyDiffLoaded applies a finished diff read. It reports whether the reply was
// accepted.
//
// Two replies are refused: one whose id is not the current request (a superseded
// read whose selection has moved on), and one from a different scope (a session
// the user has left). Applying either would show a diff for a file the reader is
// not looking at.
func (m *Model) ApplyDiffLoaded(msg DiffLoadedMsg) bool {
	if msg.Request != m.changes.currentReq || m.changes.currentReq == 0 {
		return false
	}
	if msg.Scope != m.Scope() {
		return false
	}
	if msg.Path != m.changes.currentPath {
		return false
	}

	m.changes.loading = false
	m.changes.patch = msg.Diff.Patch
	m.changes.patchTruncated = msg.Diff.Truncated
	m.changes.currentDiff = msg.Diff
	m.changes.loaded = true
	m.applyDiff(msg.Path, msg.Diff)
	return true
}

// HasDiff reports whether a diff is on screen for the current selection.
//
// The key router reads it to decide whether the paging keys mean "move through
// the patch" or "scroll the tab body": two things are scrollable in this tab,
// and a key that silently moved the wrong one is how a reader loses their
// place in a long diff.
func (m *Model) HasDiff() bool { return m.changes.currentReq != 0 }

// PageDetail moves the diff body by whole viewports. Positive is forward.
func (m *Model) PageDetail(delta int) { m.detail.Page(delta) }

// DetailScroll reports the diff body's scroll offset, so a caller can tell
// whether a key actually moved the patch it was looking at.
func (m *Model) DetailScroll() int { return m.detail.ScrollOffset() }

// DetailTop jumps the diff body to its first line.
func (m *Model) DetailTop() { m.detail.Top() }

// DetailBottom jumps the diff body to its last line and resumes following.
func (m *Model) DetailBottom() { m.detail.Bottom() }

// CapturedPatch returns the patch text as FETCHED, with the note explaining
// what it is, and whether the fetch was capped.
//
// This — not the rendered body — is what "copy the patch" must put on the
// clipboard. The rendered body carries markers, separators and colour that are
// not patch bytes, and putting those on the clipboard produces something that
// no longer applies.
//
// It reports false when nothing has been fetched, so the caller does not
// silently copy an empty string.
func (m *Model) CapturedPatch() (text string, label string, truncated bool, ok bool) {
	// Nothing has been fetched for this selection yet. Reporting "nothing to
	// copy" is the only honest answer: there ARE bytes coming, but a copy taken
	// before they arrive would either be empty or, worse, be the previous
	// file's.
	if m.changes.currentReq == 0 || m.changes.currentPath == "" || !m.changes.loaded {
		return "", "", false, false
	}
	body := m.changes.patch
	if strings.TrimSpace(body) == "" {
		// No patch text, but the reason is on the Diff. Copying the
		// explanation is more useful than copying nothing, and it is honest
		// about why there is no patch.
		body = changeAbsenceNote(m.changes.currentDiff)
	}
	if strings.TrimSpace(body) == "" {
		return "", "", false, false
	}
	label = "Copy patch: " + m.changes.currentPath
	if m.changes.patchTruncated {
		// Say so in the label, because the paste will need finishing by hand
		// and the reader must not discover that in another program.
		label += " (captured prefix only)"
	}
	return body, label, m.changes.patchTruncated, true
}

// CapturePath returns the selected file's path, for a copy.
func (m *Model) CapturePath() (string, bool) {
	path, _ := m.SelectedPath()
	if path == "" {
		return "", false
	}
	return path, true
}

// applyDiff renders one file's diff into the detail view.
//
// The render happens HERE rather than in View because it is the expensive part
// (parsing hunks, syntax highlighting) and its result depends only on the diff
// and the width, both of which are known at this point. Rendering in View would
// re-parse every frame.
func (m *Model) applyDiff(path string, diff changedfiles.Diff) {
	title := path
	if diff.Renamed && diff.OldPath != "" {
		title = diff.OldPath + " → " + path
	}

	body := diff.Patch
	if body == "" {
		// No patch text, but a status that explains why. Saying nothing would
		// look like a bug; saying "binary" or "deleted" tells the reader what
		// they are looking at (or why there is nothing).
		body = changeAbsenceNote(diff)
	}

	width := m.width
	if width <= 0 {
		width = 80
	}
	rendered := diffview.RenderResult(body, diffview.Options{
		Width:     width,
		Mode:      diffViewMode(width),
		Highlight: true,
	})

	// Truncation reaches the reader if EITHER the fetch was capped or the
	// renderer capped its output: both mean the bytes on screen are a prefix,
	// and neither may be presented as the whole patch.
	m.detail.SetNoLongerChanged(m.changes.vanished)
	m.detail.SetContent(rendered.Text, diff.Truncated || rendered.Truncated)
	// A patch the reader asked to open starts at its FIRST line.
	//
	// The detail view follows by default, which is right for a stream that
	// grows underneath the reader and wrong here: a diff is fetched whole, and
	// opening it at the bottom hides the hunk header that says what changed.
	// It is also what makes the paging keys work at all — a view already pinned
	// to the end has nowhere to page forward to, so PageDown would look broken.
	m.detail.Top()
	m.detailLabel = title
}

// diffViewMode picks the layout for a width. Side-by-side needs the room; below
// the threshold a two-column diff wraps into unreadable slivers.
func diffViewMode(width int) diffview.Mode {
	if width >= sideBySideThreshold {
		return diffview.ModeSideBySide
	}
	return diffview.ModeUnified
}

// changeAbsenceNote explains a diff with no patch text.
func changeAbsenceNote(diff changedfiles.Diff) string {
	switch {
	case diff.Binary:
		return "Binary file — no textual diff is available.\n"
	case diff.Deleted:
		return "File deleted — no textual diff was captured.\n"
	case diff.Untracked:
		return "Untracked file — no committed version to diff against.\n"
	case diff.Status != "" && diff.Status != changedfiles.StatusOK:
		return "Could not read this file's diff: " + string(diff.Status) + ".\n"
	case diff.Err != nil:
		return "Could not read this file's diff: " + diff.Err.Error() + "\n"
	default:
		return "No changes to show for this file.\n"
	}
}

// viewChanges renders the Changes tab.
func (m *Model) viewChanges() string {
	// A failed or unavailable read is reported BEFORE anything else, because
	// the list below it would otherwise be empty and read as "nothing changed"
	// — the exact claim the reader cannot verify and must not be given.
	if note, ok := changeStatusNote(m.changes.snapshot); ok {
		return note
	}

	var b strings.Builder
	if len(m.changes.rows) == 0 {
		b.WriteString("No changes against " + baseLabel(m.changes.snapshot) + ".\n")
		return b.String()
	}

	b.WriteString("Changed against " + baseLabel(m.changes.snapshot) + ":\n")
	b.WriteString("\n")

	// An UNMEASURED panel renders everything. That is the same rule the detail
	// body follows for an unmeasured width: a caller that has not laid its frame
	// out yet gets the full content rather than a window computed from a height
	// nobody has measured. A window of one row would be a lie about the content.
	if m.height <= 0 {
		for _, row := range m.changes.rows {
			b.WriteString("  ")
			b.WriteString(changeRowText(row.file))
			b.WriteString("\n")
		}
		if m.changes.vanished {
			b.WriteString("\n")
			b.WriteString("The file you were on is no longer changed; showing its nearest neighbour.\n")
		}
		if m.changes.loading {
			b.WriteString("\n")
			b.WriteString("Loading diff…\n")
		}
		return b.String()
	}

	// The tab is BUDGETED end to end, because the panel is joined into the frame
	// as a second column and a join pads the shorter column to the taller one.
	// Anything this function emits beyond m.height therefore escapes into the
	// frame and pushes the status line and composer off the bottom — which is
	// the defect, and why the budget is computed here rather than estimated.
	rows := m.height

	// Fixed chrome: the header pair.
	rows -= 2

	// A trailing note about a vanished selection, and the loading row. Both are
	// single lines plus their separator.
	if m.changes.vanished {
		rows -= 2
	}
	if m.changes.loading {
		rows -= 2
	}

	// The list and the detail body SHARE what is left, split evenly when a body
	// is open. Half each is the rule because neither is more important than the
	// other: a reader with a diff open is still choosing files, and a reader with
	// a long file list is still reading one of them. The split is what stops
	// either one starving the other, which is how a long diff used to leave no
	// list at all.
	bodyRows := 0
	if !m.changes.loading && m.changes.currentReq != 0 {
		bodyRows = max(rows/2, 1)
		rows -= bodyRows
	}
	listRows := max(rows, 1)

	// The window is asked to reserve its own note rows, so the notes ride inside
	// the list's share rather than being added on top of it. Adding them
	// afterwards is precisely how a budgeted panel ends up taller than its frame.
	w := windowList(len(m.changes.rows), listRows, m.changes.cursor, m.State(TabChanges).Scroll, 2)
	if note := aboveNote(w.Above(), m.width); note != "" {
		b.WriteString(note)
		b.WriteString("\n")
	}
	for i := w.Start; i < w.End; i++ {
		cursor := "  "
		if i == m.changes.cursor {
			cursor = "▸ "
		}
		b.WriteString(cursor)
		b.WriteString(changeRowText(m.changes.rows[i].file))
		b.WriteString("\n")
	}
	if note := belowNote(w.Below(), m.width); note != "" {
		b.WriteString(note)
		b.WriteString("\n")
	}

	if m.changes.vanished {
		// The reader's file stopped being changed. Say so rather than letting
		// them study a diff that no longer describes a pending change.
		b.WriteString("\n")
		b.WriteString("The file you were on is no longer changed; showing its nearest neighbour.\n")
	}

	if m.changes.loading {
		b.WriteString("\n")
		b.WriteString("Loading diff…\n")
		return b.String()
	}

	if m.changes.currentReq != 0 {
		// The body is resized to ITS share, so it windows its own content to
		// what it was given rather than to the whole panel.
		m.detail.Resize(m.width, bodyRows)
		b.WriteString("\n")
		b.WriteString(m.detail.View(m.detailLabel))
	}
	return b.String()
}

// changeRowText renders one row: marker, path, and truthful counts.
//
// An unknown count renders as NOTHING. The alternative is to print a number,
// and a fabricated count on screen is indistinguishable from a real one — which
// is precisely the defect this work removed from the reader underneath.
func changeRowText(f changedfiles.File) string {
	var b strings.Builder
	b.WriteString(string(markerFor(f)))
	b.WriteString(" ")
	b.WriteString(f.Path)
	if f.Kind == changedfiles.FileRenamed && f.OldPath != "" {
		b.WriteString("  (from " + f.OldPath + ")")
	}
	if f.CountsKnown {
		plus, minus := "", ""
		if f.Added > 0 {
			plus = fmt.Sprintf(" +%d", f.Added)
		}
		if f.Removed > 0 {
			minus = fmt.Sprintf(" -%d", f.Removed)
		}
		b.WriteString(plus + minus)
	} else {
		// No number at all: the count is unknown, and the row says so in words
		// rather than in digits.
		b.WriteString("  " + unknownCountNote(f))
	}
	return b.String()
}

// unknownCountNote describes why a count is unknown, so the absence of a number
// reads as information rather than as a rendering bug.
func unknownCountNote(f changedfiles.File) string {
	switch f.Kind {
	case changedfiles.FileUntracked:
		return "(untracked — no counts)"
	case changedfiles.FileBinary:
		return "(binary — no counts)"
	default:
		return "(counts unknown)"
	}
}

// markerFor picks the row's status marker from the KIND, falling back to the
// status letter git reported.
func markerFor(f changedfiles.File) rune {
	switch f.Kind {
	case changedfiles.FileAdded:
		return 'A'
	case changedfiles.FileDeleted:
		return 'D'
	case changedfiles.FileRenamed:
		return 'R'
	case changedfiles.FileUntracked:
		return '?'
	case changedfiles.FileBinary:
		return 'B'
	}
	if f.Status != 0 {
		return f.Status
	}
	return 'M'
}

// changeStatusNote renders the reason a read produced no list, or reports false
// when the read succeeded.
//
// Every non-OK status gets its own words because the reader's next action
// differs: a missing base ref is fixed by fetching, a timeout by retrying, a
// non-repo by changing directory.
func changeStatusNote(snap changedfiles.Snapshot) (string, bool) {
	switch snap.Status {
	case changedfiles.StatusOK:
		return "", false
	case changedfiles.StatusMissingBase:
		return "Could not compare against " + baseLabel(snap) + ": that revision is not in this repository.\n", true
	case changedfiles.StatusNotARepo:
		return "Not a git repository, so there is nothing to compare.\n", true
	case changedfiles.StatusTimeout:
		return "Timed out reading the working tree; the list is unavailable.\n", true
	case changedfiles.StatusCancelled:
		return "The read was cancelled; the list is unavailable.\n", true
	case changedfiles.StatusFailed:
		if snap.Err != nil {
			return "Could not read the working tree: " + snap.Err.Error() + "\n", true
		}
		return "Could not read the working tree.\n", true
	}
	// An unset status means nothing has been read yet. Saying "no changes" here
	// would be a claim made before any evidence.
	if snap.Status == "" {
		return "Reading the working tree…\n", true
	}
	return "Could not read the working tree (" + string(snap.Status) + ").\n", true
}

// baseLabel names what the comparison was against, preferring the resolved OID
// so the label cannot drift as the ref moves.
func baseLabel(snap changedfiles.Snapshot) string {
	switch {
	case snap.BaseOID != "" && snap.BaseRef != "":
		return snap.BaseRef + " (" + shortOID(snap.BaseOID) + ")"
	case snap.BaseOID != "":
		return shortOID(snap.BaseOID)
	case snap.BaseRef != "":
		return snap.BaseRef
	default:
		return "the base revision"
	}
}

// shortOID abbreviates a commit id for a label.
func shortOID(oid string) string {
	if len(oid) <= 8 {
		return oid
	}
	return oid[:8]
}
