package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/viewmodel"
)

// compactDuration is "42s" or "6m40s": the form task headers and the turn
// receipt use, where formatElapsed's "6m 40s" costs a column.
func compactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// taskFolded applies the folding rule: a task collapses to one row when
// folding is on, its todo is completed (a dropped task: its last step has
// ended), nothing about it is unresolved or live, and the user has not
// overridden it with Enter or a click.
func (c *stepRenderCtx) taskFolded(n *viewmodel.Node) bool {
	t := n.Task
	if t == nil || !c.foldTasks || t.UnresolvedFailure || n.AnyLive() {
		return false
	}
	if c.hasOverride != nil && c.hasOverride(n.ID) {
		return false
	}
	if t.Dropped {
		if len(n.Children) == 0 {
			return false
		}
		last := n.Children[len(n.Children)-1]
		return last.Step != nil && !last.Step.Step.EndedAt.IsZero()
	}
	return t.Status == "completed"
}

// taskTitle is the todo's text, or for a dropped todo the first sentence of
// its first step's narration.
func taskTitle(t *viewmodel.TaskInfo) string {
	if !t.Dropped {
		return t.Content
	}
	if t.FirstNarration != "" {
		if h, _ := firstSentence(t.FirstNarration); h != "" {
			return stripEmphasis(h)
		}
	}
	return "task " + t.TodoID
}

func taskPosition(t *viewmodel.TaskInfo) string {
	if t.Dropped {
		return "dropped"
	}
	return fmt.Sprintf("%d/%d", t.Index, t.Total)
}

// taskElapsed is the time the task has been worked on, from its steps. Time
// between turns is not counted: a task carried over from yesterday reads as
// the minutes it ran, not as a day.
func taskElapsed(t *viewmodel.TaskInfo) string {
	if t.Work <= 0 {
		return ""
	}
	return compactDuration(t.Work)
}

// renderTask draws a task: either one folded row, or a rule header followed by
// its steps. Steps keep their own regions so a click lands on the step, not on
// the whole task.
func renderTask(n *viewmodel.Node, c *stepRenderCtx, width int, inherited density) (string, []subRegion) {
	td := c.level(n.ID, inherited)
	if c.taskFolded(n) {
		return renderFoldedTask(n, c, width), nil
	}
	var b strings.Builder
	var subs []subRegion
	b.WriteString(renderTaskHeader(n, c, width) + "\n")
	lines := 1
	first := true
	for _, ch := range n.Children {
		out, ssubs := renderStep(ch, c, width, td)
		if out == "" {
			continue
		}
		// The header sits right on its first step; later steps are set off
		// by a blank line, as at the top level.
		if !first {
			b.WriteString("\n")
			lines++
		}
		first = false
		n := strings.Count(out, "\n")
		subs = append(subs, subRegion{id: ch.ID, start: lines, end: lines + n})
		for _, s := range ssubs {
			s.start += lines
			s.end += lines
			subs = append(subs, s)
		}
		b.WriteString(out)
		lines += n
	}
	return b.String(), subs
}

// renderTaskHeader is the open task's rule: "─ 2/4 Wire the parser ──── 3m10s".
func renderTaskHeader(n *viewmodel.Node, c *stepRenderCtx, width int) string {
	t := n.Task
	th := theme.Current()
	rule := lipgloss.NewStyle().Foreground(th.BorderMuted)
	pos := taskPosition(t)
	posStyle := mutedStyle()
	if t.Dropped {
		posStyle = lipgloss.NewStyle().Foreground(th.StatusWarning)
	}
	g := glyph.OK
	gc := th.StatusSuccess
	switch {
	case n.AnyLive():
		g, gc = c.spinner, accentColor
		if g == "" {
			g = glyph.Running
		}
	case t.UnresolvedFailure:
		g, gc = glyph.Error, th.StatusError
	case t.Status == "in_progress":
		g, gc = glyph.Running, accentColor
	case t.Status != "completed":
		g, gc = glyph.Ambient, th.FGMuted
	}
	elapsed := taskElapsed(t)
	right := ""
	if elapsed != "" {
		right = " " + mutedStyle().Render(elapsed)
	}
	rightW := ansi.StringWidth(right)
	lead := gutterPrefix(g, gc)
	prefix := posStyle.Render(pos) + " "
	room := max(width-gutterWidth-rightW-ansi.StringWidth(pos)-1-3, 4)
	title := ansi.Truncate(taskTitle(t), room, "…")
	left := lead + prefix + lipgloss.NewStyle().Bold(true).Foreground(th.FGEmphasis).Render(title) + " "
	fill := max(width-ansi.StringWidth(left)-rightW, 0)
	return left + rule.Render(strings.Repeat("─", fill)) + right
}

// renderFoldedTask is a finished task as one row:
// "✓ 2/4 Wire the parser            5 steps · ✎ +40 −3 · 3m10s ▹".
func renderFoldedTask(n *viewmodel.Node, c *stepRenderCtx, width int) string {
	t := n.Task
	th := theme.Current()
	parts := []string{fmt.Sprintf("%d steps", t.Steps)}
	if t.Steps == 1 {
		parts[0] = "1 step"
	}
	if stat := taskDiffStat(n); stat != "" {
		parts = append(parts, glyph.Edit+" "+stat)
	} else if t.Tools > 0 {
		word := "tools"
		if t.Tools == 1 {
			word = "tool"
		}
		parts = append(parts, fmt.Sprintf("%d %s", t.Tools, word))
	}
	if d := taskElapsed(t); d != "" {
		parts = append(parts, d)
	}
	meta := strings.Join(parts, " · ") + " ▹"
	metaW := ansi.StringWidth(meta)
	avail := max(width-gutterWidth, 1)
	pos := taskPosition(t)
	headRoom := max(avail-metaW-2-ansi.StringWidth(pos)-1, 1)
	title := ansi.Truncate(taskTitle(t), headRoom, "…")
	left := mutedStyle().Render(pos) + " " + lipgloss.NewStyle().Foreground(th.FGEmphasis).Render(title)
	pad := max(avail-ansi.StringWidth(pos)-1-ansi.StringWidth(title)-metaW, 1)
	return gutterPrefix(glyph.OK, th.StatusSuccess) + left + strings.Repeat(" ", pad) + mutedStyle().Render(meta) + "\n"
}

// taskDiffStat sums the diff stats of the task's edit rows, "+a −r", or ""
// when no edit row carries a countable diff.
func taskDiffStat(n *viewmodel.Node) string {
	a, r := 0, 0
	for _, st := range n.Children {
		for _, row := range st.Children {
			for _, ev := range row.Tools {
				if isDiffTool(ev.ToolName) {
					da, dr := diffCounts(ev.ResultContent)
					a, r = a+da, r+dr
				}
			}
		}
	}
	if a == 0 && r == 0 {
		return ""
	}
	return fmt.Sprintf("+%d −%d", a, r)
}

// renderReceipt is the turn's closing line:
// "✓ done · 6m40s · 4 tasks · 11 steps · 19 tools · ±3 files · 212k tok".
func renderReceipt(r *viewmodel.ReceiptInfo, width int) string {
	th := theme.Current()
	g, gc, word := glyph.OK, th.StatusSuccess, "done"
	if r.Salvaged {
		g, gc, word = glyph.Warning, th.StatusWarning, "salvaged"
	}
	parts := []string{word}
	if r.Duration > 0 {
		parts = append(parts, compactDuration(r.Duration))
	}
	if r.Tasks > 0 {
		parts = append(parts, pluralCount(r.Tasks, "task", "tasks"))
	}
	parts = append(parts, pluralCount(r.Steps, "step", "steps"), pluralCount(r.Tools, "tool", "tools"))
	if r.Files > 0 {
		parts = append(parts, fmt.Sprintf("±%d files", r.Files))
		if r.Files == 1 {
			parts[len(parts)-1] = "±1 file"
		}
	}
	if r.Usage != "" {
		parts = append(parts, r.Usage)
	}
	text := strings.Join(parts, " · ")
	text = ansi.Truncate(text, max(width-gutterWidth, 1), "…")
	return gutterPrefix(g, gc) + mutedStyle().Render(text) + "\n"
}

// pluralCount is "1 step" / "3 steps".
func pluralCount(n int, one, many string) string {
	return fmt.Sprintf("%d %s", n, plural(n, one, many))
}
