package sidepanel

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
)

// WorkingSetSection lists the files this session has touched, most recently
// first, distinguishing files the agent edited from ones it only read.
//
// It answers "what has the agent been working on" at a glance. The
// transcript answers it too, but only by scrolling back through every tool
// row; a long session buries the shape of the work under its detail.
type WorkingSetSection struct{}

func (WorkingSetSection) ID() string      { return "files" }
func (WorkingSetSection) Title() string   { return "FILES" }
func (WorkingSetSection) Priority() int   { return 3 }
func (WorkingSetSection) Clippable() bool { return true }

func (WorkingSetSection) Relevant(d Data) bool { return hasFileActivity(d.Audit) }

func (WorkingSetSection) Render(d Data, width, maxRows int) []string {
	stats := FileStats(d.Audit)
	if maxRows > 0 && len(stats) > maxRows {
		stats = stats[:maxRows]
	}
	rows := make([]string, 0, len(stats))
	for _, s := range stats {
		g, detail := glyph.File, "read"
		if s.Edits > 0 {
			g = glyph.Edit
			detail = "1 edit"
			if s.Edits > 1 {
				detail = fmt.Sprintf("%d edits", s.Edits)
			}
		}
		rows = append(rows, railRow(g,
			shortenPath(s.Path, railBudget(g, detail, width)), detail, width))
	}
	return rows
}

func (WorkingSetSection) OneLine(d Data, width int) string {
	stats := FileStats(d.Audit)
	edited := 0
	for _, s := range stats {
		if s.Edits > 0 {
			edited++
		}
	}
	return ansi.Truncate(
		fmt.Sprintf("%s %d files · %d edited", glyph.File, len(stats), edited), width, "…")
}
