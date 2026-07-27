package sidepanel

import (
	"fmt"

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
		counts := ""
		if f.Added > 0 {
			counts += fmt.Sprintf(" +%d", f.Added)
		}
		if f.Removed > 0 {
			counts += fmt.Sprintf(" -%d", f.Removed)
		}
		// Reserve the status glyph, a space, and the counts; the path gets
		// the rest and is middle-truncated so the basename survives.
		pathWidth := max(width-3-ansi.StringWidth(counts), 6)
		rows = append(rows, fmt.Sprintf(" %c %s%s",
			f.Status, strutil.TruncateMiddle(f.Path, pathWidth), counts))
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], width, "…")
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
