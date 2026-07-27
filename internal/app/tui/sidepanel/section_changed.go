package sidepanel

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/strutil"
)

// ChangedSection lists the working tree's modified files with line
// counts. The status line has no room for this at all.
type ChangedSection struct{}

func (ChangedSection) ID() string      { return "changed" }
func (ChangedSection) Title() string   { return "CHANGED" }
func (ChangedSection) Priority() int   { return 2 }
func (ChangedSection) Clippable() bool { return true }

func (ChangedSection) Relevant(d Data) bool { return len(d.Changed) > 0 }

func (ChangedSection) Render(d Data, width, maxRows int) []string {
	rows := make([]string, 0, len(d.Changed))
	for _, f := range d.Changed {
		// Build the counts plain first: the path's width budget depends on
		// their visible width, and styling must not affect that math.
		plus, minus := "", ""
		if f.Added > 0 {
			plus = fmt.Sprintf(" +%d", f.Added)
		}
		if f.Removed > 0 {
			minus = fmt.Sprintf(" -%d", f.Removed)
		}
		counts := plus + minus

		// Reserve the status glyph, a space, and the counts; the path gets
		// the rest and is middle-truncated so the basename survives.
		pathWidth := max(width-3-ansi.StringWidth(counts), 6)
		path := strutil.TruncateMiddle(f.Path, pathWidth)

		// Truncate before styling so the ellipsis math sees plain text.
		row := ansi.Truncate(fmt.Sprintf(" %c %s%s", f.Status, path, counts), width, "…")

		// Re-apply color to whichever counts survived truncation.
		if plus != "" && strings.HasSuffix(StripANSI(row), counts) {
			row = strings.TrimSuffix(row, counts)
			if minus != "" {
				row += styleSuccess(plus) + styleError(minus)
			} else {
				row += styleSuccess(plus)
			}
		} else if minus != "" && strings.HasSuffix(StripANSI(row), counts) {
			row = strings.TrimSuffix(row, counts) + styleError(minus)
		}
		rows = append(rows, row)
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	return rows
}

func (ChangedSection) OneLine(d Data, width int) string {
	added, removed := 0, 0
	for _, f := range d.Changed {
		added += f.Added
		removed += f.Removed
	}
	return ansi.Truncate(fmt.Sprintf("± %d files · +%d -%d",
		len(d.Changed), added, removed), width, "…")
}
