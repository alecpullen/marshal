package inspector

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// DetailView is a read-only, scrollable body of text with its own scroll state
// and follow flag.
//
// It is deliberately independent of the list behind it: a reader scrolling a
// diff must not move the list's cursor, and moving the list's cursor must not
// move the diff. Two pieces of navigation state that can be confused for one
// another is how a reader loses their place.
//
// It holds TEXT, not a rendered view: the caller renders (with syntax
// highlighting and width-aware wrapping) and hands the result here. That keeps
// this type free of any rendering dependency, which is what makes it testable
// without a terminal.
type DetailView struct {
	content string
	// width, height are the last recorded viewport. Zero means "not measured
	// yet", and View falls back to rendering everything so a caller that never
	// resized still sees its content rather than an empty panel.
	width, height int
	// scroll is the index of the first visible line.
	scroll int
	// follow pins the view to the end, so streaming content stays visible.
	// It is cleared by any explicit scroll, because the reader has taken
	// control — the same rule the transcript follows.
	follow bool
	// truncatedBySource records that the SOURCE text was capped (the fetch cap
	// or the render cap), as opposed to merely being longer than the viewport.
	// The two must not be confused: "scroll down for more" and "there IS no
	// more" are different statements, and only one of them is true.
	truncatedBySource bool
	// noLongerChanged labels a detail whose path has stopped being changed.
	// Empty when the file is still in the changed set.
	noLongerChanged bool
	// label is the heading rendered above the body.
	//
	// It is STORED, not just passed to View, because it is a row of the BUDGET:
	// the content height and the scroll bound both have to know whether that row
	// is being spent. A caller sharing a column with this view must be able to
	// ask how many rows it will emit BEFORE rendering, and it has no way to hand
	// the label over a second time — so the label lives here, set via SetLabel
	// (or by View), and every height derivation reads it.
	label string
}

// NewDetailView returns an empty detail view that is following.
func NewDetailView() *DetailView {
	return &DetailView{follow: true}
}

// SetLabel records the heading the body renders under.
//
// It re-clamps, because the label is a row: adding one shortens the content
// window, and a scroll offset that was legal before can be past the end
// afterwards.
func (d *DetailView) SetLabel(label string) {
	if d.label == label {
		return
	}
	d.label = label
	d.clamp()
}

// Label reports the recorded heading.
func (d *DetailView) Label() string { return d.label }

// SetContent replaces the body.
//
// While following, the reader stays pinned to the end. While not following, the
// scroll offset is preserved (clamped), because new content arriving in a diff
// the reader is studying must not move them.
func (d *DetailView) SetContent(text string, truncated bool) {
	d.content = text
	d.truncatedBySource = truncated
	d.clamp()
	if d.follow {
		d.scroll = d.maxScroll()
	}
}

// SetNoLongerChanged marks the detail as describing a path that is no longer
// in the changed set. It is a statement about the file, not about the view, so
// it is kept separately from the truncation flag.
func (d *DetailView) SetNoLongerChanged(v bool) { d.noLongerChanged = v }

// NoLongerChanged reports whether the path stopped being changed.
func (d *DetailView) NoLongerChanged() bool { return d.noLongerChanged }

// Resize records the available area. It never loses the reader's place: the
// scroll offset is clamped to the new bounds rather than reset.
func (d *DetailView) Resize(width, height int) {
	d.width = max(width, 0)
	d.height = max(height, 0)
	d.clamp()
}

// Scroll moves by delta lines. Any nonzero delta takes the reader off follow:
// they have taken control, and a later SetContent must not yank them back.
func (d *DetailView) Scroll(delta int) {
	if delta == 0 {
		return
	}
	d.follow = false
	d.scroll += delta
	d.clamp()
}

// Page moves by whole viewports. A zero or unmeasured height pages by one line
// rather than doing nothing, so a page key pressed before the first resize is
// not silently ignored.
func (d *DetailView) Page(delta int) {
	h := max(d.height, 1)
	d.Scroll(delta * h)
}

// Top jumps to the first line and leaves follow.
func (d *DetailView) Top() {
	d.follow = false
	d.scroll = 0
}

// Bottom jumps to the last line and resumes follow.
func (d *DetailView) Bottom() {
	d.follow = true
	d.scroll = d.maxScroll()
}

// Follow reports whether the view is pinned to the end.
func (d *DetailView) Follow() bool { return d.follow }

// Scroll reports the current offset, for a caller that renders its own chrome.
func (d *DetailView) ScrollOffset() int { return d.scroll }

// Truncated reports whether the SOURCE was capped. A caller must say so, and
// must not suggest the rest is available.
func (d *DetailView) Truncated() bool { return d.truncatedBySource }

// Empty reports whether there is no content at all.
func (d *DetailView) Empty() bool { return strings.TrimSpace(d.content) == "" }

// Content returns the raw body, which is what a copy action must put on the
// clipboard.
//
// It is the SOURCE text, not the rendered view: the rendered form carries ANSI
// escape sequences for colour, and copying those would silently corrupt the
// bytes — invisible on screen, broken on paste.
func (d *DetailView) Content() string { return d.content }

// lines splits the content into lines. An empty content has no lines, so the
// renderer shows its empty state rather than an empty first line.
func (d *DetailView) lines() []string {
	if d.content == "" {
		return nil
	}
	return strings.Split(d.content, "\n")
}

// detailPlan is the exact row plan for one render: what will be written, and
// how much of the content is shown.
//
// It exists because the budget and the emission MUST be computed from one place.
// The previous code derived the content window from a "chrome rows" estimate and
// then emitted the chrome ON TOP of it, so the two could disagree — Resize(40, 5)
// produced 8 rows, and the surplus escaped into whatever column the panel was
// being joined against. A plan that both View and BodyHeight read makes that
// class of disagreement impossible rather than merely fixed.
type detailPlan struct {
	// labelRow is whether the heading is written.
	labelRow bool
	// contentStart and contentRows are the visible content window.
	contentStart, contentRows int
	// scrollNote is whether the "more to scroll to" footer is written.
	scrollNote bool
	// truncatedNote is whether the "source was capped" footer is written.
	truncatedNote bool
	// emptyNote is whether the "Nothing to show." line stands in for content.
	emptyNote bool
	// unmeasured records that no height has been recorded, so the plan is
	// unbounded and every conditional row is rendered.
	unmeasured bool
}

// Rows is the total number of lines this plan writes.
func (p detailPlan) Rows() int {
	rows := 0
	if p.labelRow {
		rows++
	}
	if p.emptyNote {
		// plan() has already established that this fits alongside the label.
		rows++
		return rows
	}
	rows += p.contentRows
	if p.scrollNote {
		rows++
	}
	if p.truncatedNote {
		rows++
	}
	return rows
}

// plan computes the row plan for the current content, height and label.
//
// The reservation order is deliberate and is the whole fix:
//
//  1. The label, because it names what the reader is looking at.
//  2. The truncation note, because "the source was capped" is a claim about
//     completeness that must not be silently dropped — a reader who cannot see
//     that footer has no way to know the body is a prefix.
//  3. The content window.
//  4. The "scroll for more" note, but ONLY out of rows the content did not need.
//     It is a hint about what is below, so it yields to content; if there is no
//     room for it, the reader simply sees fewer lines, which that note would
//     only have told them anyway.
//
// Each step draws from one shrinking budget, so the total cannot exceed the
// height. A step that does not fit is skipped rather than overflowing.
func (d *DetailView) plan() detailPlan {
	var p detailPlan
	lines := d.lines()

	p.labelRow = d.label != "" || d.noLongerChanged
	p.unmeasured = d.height <= 0
	if len(lines) == 0 {
		// The empty note is a content row, so it obeys the same budget content
		// does: it is emitted only when it fits alongside the label. Emitting
		// both on a one-row panel would put the panel one row over, which is the
		// defect this plan exists to make impossible.
		if !p.unmeasured && p.labelRow && d.height < 2 {
			return p
		}
		p.emptyNote = true
		return p
	}
	if p.unmeasured {
		// Unmeasured: the view renders everything rather than nothing, the same
		// rule clampLine follows for width.
		p.contentStart = min(max(d.scroll, 0), max(len(lines)-1, 0))
		p.contentRows = len(lines) - p.contentStart
		p.truncatedNote = d.truncatedBySource
		return p
	}

	// The ROW COUNT first, then the position: a following view is positioned
	// relative to the rows it can show, so computing the start before the count
	// is what made the two circular.
	budget := d.height
	if p.labelRow {
		if budget < 1 {
			// Not even the label fits. A degenerate panel emits nothing rather
			// than a row it does not have.
			p.labelRow = false
			return p
		}
		budget--
	}
	if d.truncatedBySource && budget >= 1 {
		p.truncatedNote = true
		budget--
	}
	p.contentRows = min(len(lines), budget)

	// Now the start. Following means "end at the last line", and it is resolved
	// here rather than read off a stored offset, because the offset can be stale
	// with respect to content that has since arrived or a height that has since
	// changed.
	if d.follow {
		p.contentStart = max(len(lines)-p.contentRows, 0)
	} else {
		p.contentStart = min(max(d.scroll, 0), max(len(lines)-1, 0))
	}

	if len(lines)-p.contentStart <= p.contentRows {
		// Everything from here fits, so there is nothing to hint at.
		p.contentRows = len(lines) - p.contentStart
		return p
	}
	// The content does not fit below the start. The hint is worth one row when
	// there is one to spare; when there is not, the content keeps it.
	if p.contentRows >= 2 {
		p.scrollNote = true
		p.contentRows--
		if d.follow {
			// Reserving the hint row moves the window down by nothing — the
			// window still ends at the last line — but its START must follow,
			// or the last line would be the one that got dropped.
			p.contentStart = max(len(lines)-p.contentRows, 0)
		}
	}
	return p
}

// BodyHeight reports the number of rows View will actually emit, so a caller
// that shares a column with this view can budget for it BEFORE rendering.
//
// Asking after the fact is too late — the rows are already spent — and that is
// exactly how a panel ends up taller than the frame it is joined into. It is
// derived from the same plan View renders, so the two cannot disagree.
//
// The label must have been set (SetLabel) for this to be exact, since View
// renders the recorded one.
// It is a pure function of the view's state: it does not move the scroll, so
// asking it before a render cannot change what that render shows.
func (d *DetailView) BodyHeight() int {
	return d.plan().Rows()
}

// maxScroll is the largest valid offset for the current bounds.
//
// It is derived from the plan rather than from a separate height subtraction, so
// the furthest the reader can scroll is exactly the point at which the last line
// is on screen. plan() is pure with respect to the scroll offset, so this cannot
// feed back into itself.
func (d *DetailView) maxScroll() int {
	p := d.plan()
	return max(len(d.lines())-p.contentRows, 0)
}

// clamp keeps the offset inside the scrollable range.
func (d *DetailView) clamp() {
	if d.scroll < 0 {
		d.scroll = 0
	}
	if max := d.maxScroll(); d.scroll > max {
		d.scroll = max
	}
}

// ScrollToMatch scrolls to the first line containing needle, so a caller can
// jump the reader to a search hit without owning the view's geometry.
//
// It reports false when there is no match, leaving the position untouched —
// a search that found nothing must not move the reader.
func (d *DetailView) ScrollToMatch(needle string) bool {
	if needle == "" {
		return false
	}
	for i, line := range d.lines() {
		if strings.Contains(line, needle) {
			d.follow = false
			d.scroll = i
			d.clamp()
			return true
		}
	}
	return false
}

// View renders the visible window under a label.
//
// The label is RECORDED before rendering, because it is a row of the budget and
// the content window has already been derived from it. Passing a label here that
// differs from the one BodyHeight was asked about would make the two disagree,
// so this assignment is the single point that keeps them in step.
//
// Every row is accounted for before it is written: the label, the content window
// and the notes all come out of the budget chromeRows reserved. The total emitted
// line count never exceeds the recorded height (when one has been recorded), which
// is what stops this body overflowing the column it is joined into — the way a
// long diff used to push the status line off the bottom of the screen.
//
// The footer distinguishes the two kinds of "more", because conflating them is
// how a reader believes they have seen a whole patch when they have seen a
// prefix:
//
//   - not at the bottom and not truncated: there is more to scroll to.
//   - truncated at the source: there is nothing more to reach, and the reader
//     must be told that plainly.
//
// A FOLLOWING view is anchored to the END at render time rather than to whatever
// offset happens to be stored. The stored offset is a snapshot of a moment:
// content that arrived after the last SetContent, or a height recorded after it,
// would leave the view showing a window that is no longer the end — so a reader
// who is following would see stale lines with a "scroll for more" note under
// them, which is the opposite of following. The plan resolves that anchor from
// the content and the height it is rendering WITH, which is what makes
// "following" mean the last line is on screen.
func (d *DetailView) View(label string) string {
	if label != "" {
		d.SetLabel(label)
	}

	// ONE plan, rendered. Every row that follows is one the plan accounted for,
	// which is what bounds the output by the recorded height.
	p := d.plan()

	var b strings.Builder
	if p.labelRow {
		header := d.label
		if d.noLongerChanged {
			// The path left the changed set. Say so at the top, where a reader
			// looks first, rather than letting them wonder why the diff is stale.
			header += "  " + mutedDetailNote("(no longer changed)")
		}
		b.WriteString(d.clampLine(header))
		b.WriteString("\n")
	}

	if p.emptyNote {
		b.WriteString(d.note("Nothing to show."))
		return b.String()
	}

	lines := d.lines()
	end := min(p.contentStart+p.contentRows, len(lines))
	for _, line := range lines[p.contentStart:end] {
		b.WriteString(line)
		b.WriteString("\n")
	}

	if p.scrollNote {
		// More of the body remains below. This is not truncation: the reader can
		// reach it.
		b.WriteString(d.note("… scroll for more"))
		b.WriteString("\n")
	}
	if p.truncatedNote {
		// The wording is deliberately not patch-specific. This body is shared
		// by the Changes, Agents and Context tabs, and "captured patch" on a
		// context section would name the wrong thing — a reader who is told
		// their patch was cut when they were reading a pack section has been
		// given a fact about something else entirely.
		//
		// What survives is the part that is true for all three: the source was
		// capped, so what is on screen is a prefix and the rest is NOT
		// reachable by scrolling. That distinction — "scroll for more" versus
		// "there is no more" — is the whole reason this footer exists.
		b.WriteString(d.note("… incomplete; more content exists and is not shown"))
		b.WriteString("\n")
	}
	return b.String()
}

// note renders a footer note, in the muted styling, clamped to the width.
func (d *DetailView) note(s string) string { return d.clampLine(mutedDetailNote(s)) }

// clampLine bounds a line to the recorded width.
//
// Everything this view emits goes through it — the caller-supplied label and
// its own footer notes. Both are unbounded: a pack section's title is author
// text, and the notes are longer than the narrowest panel the rail is allowed
// to open at (tui.side_panel.min_cols, default 30). An unclamped line wraps and
// pushes the panel's chrome off the bottom, so the reader loses the body in
// order to read a sentence about it.
//
// An UNMEASURED view (width 0) is not clamped at all, following the same rule
// as contentHeight: before the first resize the view renders everything rather
// than nothing, and clamping to a width nobody measured would replace every line
// with an ellipsis.
func (d *DetailView) clampLine(s string) string {
	if d.width <= 0 {
		return s
	}
	return ansi.Truncate(s, d.width, "…")
}

// mutedDetailNote is the detail view's one styling hook. It is kept as a
// function so tests can strip it the same way they strip the rest of the view.
func mutedDetailNote(s string) string { return "[" + s + "]" }
