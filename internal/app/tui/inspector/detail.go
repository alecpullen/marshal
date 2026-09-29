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
}

// NewDetailView returns an empty detail view that is following.
func NewDetailView() *DetailView {
	return &DetailView{follow: true}
}

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

// maxScroll is the largest valid offset for the current bounds.
func (d *DetailView) maxScroll() int {
	return max(len(d.lines())-d.viewportHeight(), 0)
}

// viewportHeight is the number of content lines the view can show. Before the
// first resize it is unbounded, so an unmeasured view renders everything rather
// than nothing.
func (d *DetailView) viewportHeight() int {
	if d.height <= 0 {
		return len(d.lines())
	}
	// One row is the label header.
	return max(d.height-1, 1)
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
// The footer distinguishes the two kinds of "more", because conflating them is
// how a reader believes they have seen a whole patch when they have seen a
// prefix:
//
//   - not at the bottom and not truncated: there is more to scroll to.
//   - truncated at the source: there is nothing more to reach, and the reader
//     must be told that plainly.
func (d *DetailView) View(label string) string {
	var b strings.Builder
	header := label
	if d.noLongerChanged {
		// The path left the changed set. Say so at the top, where a reader
		// looks first, rather than letting them wonder why the diff is stale.
		header += "  " + mutedDetailNote("(no longer changed)")
	}
	if header != "" {
		b.WriteString(d.clampLine(header))
		b.WriteString("\n")
	}

	lines := d.lines()
	if len(lines) == 0 {
		b.WriteString(d.note("Nothing to show."))
		return b.String()
	}

	height := d.viewportHeight()
	end := min(d.scroll+height, len(lines))
	for _, line := range lines[d.scroll:end] {
		b.WriteString(line)
		b.WriteString("\n")
	}

	if d.scroll+height < len(lines) {
		// More of the body remains to scroll to. This is not truncation.
		b.WriteString(d.note("… scroll for more"))
		b.WriteString("\n")
	}
	if d.truncatedBySource {
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
// as viewportHeight: before the first resize the view renders everything rather
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
