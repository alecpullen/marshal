package sidepanel

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/strutil"
)

// WorktreesSection lists the agent-owned worktrees under
// .marshal/worktrees/ with their ahead/behind against the project root's
// HEAD, dirty state, and branch-tip age. The data arrives pre-computed in
// Data.Fleet — ListFleet shells out to git per worktree, so it is cached
// on turn boundaries and never computed during render.
type WorktreesSection struct{}

func (WorktreesSection) ID() string      { return "worktrees" }
func (WorktreesSection) Title() string   { return "WORKTREES" }
func (WorktreesSection) Priority() int   { return 2 }
func (WorktreesSection) Clippable() bool { return true }

func (WorktreesSection) Relevant(d Data) bool { return len(d.Fleet) > 0 }

func (WorktreesSection) Render(d Data, width, maxRows int) []string {
	rows := make([]string, 0, len(d.Fleet))
	for _, w := range d.Fleet {
		// Build the right column plain first: the branch label's width
		// budget depends on its visible width, and styling must not affect
		// that math. The age rides in the right column too — it is the
		// row's third fact after direction and dirty state.
		counts := fmt.Sprintf("↑%d ↓%d · %s", w.Ahead, w.Behind, strutil.HumanAge(w.Age))

		marker := ""
		if w.Dirty {
			marker = "●"
		}
		branch := shortenPath(w.Branch, railBudget(marker, counts, width))

		// Style only after the layout is fixed, so the escape sequences
		// never participate in the width math. The styled column must have
		// exactly the same visible text as counts, or the budget math above
		// is wrong; only the counts part takes color, the age stays plain.
		styled := fmt.Sprintf("↑%d ↓%d", w.Ahead, w.Behind)
		switch {
		case w.Ahead > 0 && w.Behind > 0:
			styled = styleWarning(fmt.Sprintf("↑%d", w.Ahead)) + " " + styleError(fmt.Sprintf("↓%d", w.Behind))
		case w.Ahead > 0:
			styled = styleWarning(fmt.Sprintf("↑%d", w.Ahead))
		case w.Behind > 0:
			styled = styleError(fmt.Sprintf("↓%d", w.Behind))
		}
		styled += " · " + strutil.HumanAge(w.Age)

		rows = append(rows, railRow(marker, branch, styled, width))
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	return rows
}

func (WorktreesSection) OneLine(d Data, width int) string {
	dirty := 0
	for _, w := range d.Fleet {
		if w.Dirty {
			dirty++
		}
	}
	return ansi.Truncate(fmt.Sprintf("%d worktrees · %d dirty", len(d.Fleet), dirty), width, "…")
}
