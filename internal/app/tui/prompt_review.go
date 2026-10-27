package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// promptReview is a bounded, pageable window over the wrapped body of a
// prompt. Approval and sudo prompts can carry long commands, reasons or
// diffs; rendering every row grows the prompt past the terminal, so the body
// is clipped to a fixed physical height and paged instead.
//
// The review holds no rendered text: callers escape untrusted controls before
// passing rows here, and the indicator contains only generated counts.
type promptReview struct {
	offset int // first visible wrapped display row
}

// promptReviewRows wraps sanitized logical lines to width, preserving blank
// lines. The result is a flat slice of physical (display) rows: ansi.Wrap may
// split one logical line into several rows, and a blank logical line becomes
// exactly one empty row. Long lines wrap; they are never truncated.
func promptReviewRows(lines []string, width int) []string {
	rows := make([]string, 0, len(lines))
	for _, line := range lines {
		// Split before wrapping so an embedded newline in a logical line is
		// treated as a line break rather than as part of the last row.
		for _, logical := range strings.Split(line, "\n") {
			rows = append(rows, strings.Split(ansi.Wrap(logical, width, WrapBreakpoints), "\n")...)
		}
	}
	return rows
}

// bodyRows is the number of content rows a window of height may show. A
// one-row window spends that row on content; when the content already fits,
// every row is content and no indicator is needed; otherwise one row of the
// height is reserved for the indicator.
func (r *promptReview) bodyRows(total, height int) int {
	switch {
	case height <= 0:
		return 0
	case height == 1:
		return 1
	case total <= height:
		return total
	default:
		return height - 1
	}
}

// clampOffset pins offset to the range of representable windows for the
// current row count and height.
func (r *promptReview) clampOffset(total, height int) {
	maxOffset := total - r.bodyRows(total, height)
	if maxOffset < 0 {
		maxOffset = 0
	}
	if r.offset > maxOffset {
		r.offset = maxOffset
	}
	if r.offset < 0 {
		r.offset = 0
	}
}

// Window clamps offset and returns at most height body rows plus an
// indicator describing omitted rows. The indicator counts within height.
//
// A one-row window always shows content: the caller indicates overflow
// separately in its pinned hints, because an indicator row would push the
// only row of content out of the window.
func (r *promptReview) Window(rows []string, height int) []string {
	if height <= 0 {
		return nil
	}
	total := len(rows)
	r.clampOffset(total, height)
	body := r.bodyRows(total, height)
	end := r.offset + body
	if end > total {
		end = total
	}
	out := make([]string, 0, height)
	for _, row := range rows[r.offset:end] {
		out = append(out, row)
	}
	// total > body exactly when at least one row is omitted. With height == 1
	// the single row is content, so only taller windows carry the indicator.
	if height > 1 && total > body {
		out = append(out, promptReviewIndicator(r.offset, end, total))
	}
	return out
}

// Page moves by one visible body page, clamped to the current wrapped rows.
// direction is -1 (towards the start) or +1 (towards the end).
func (r *promptReview) Page(rows []string, height, direction int) {
	total := len(rows)
	if total == 0 || height <= 0 {
		return
	}
	r.clampOffset(total, height)
	page := r.bodyRows(total, height)
	if page < 1 {
		page = 1
	}
	if direction < 0 {
		r.offset -= page
	} else {
		r.offset += page
	}
	r.clampOffset(total, height)
}

// promptReviewIndicator labels the visible body rows, 1-based and inclusive,
// against the total. It is plain text: no styling, no user-supplied content,
// so it stays readable with colour disabled.
func promptReviewIndicator(start, end, total int) string {
	return fmt.Sprintf("rows %d–%d of %d · PgUp/PgDn", start+1, end, total)
}
