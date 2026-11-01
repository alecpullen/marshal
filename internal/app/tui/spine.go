package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/livebox"
	"marshal/internal/app/tui/theme"
	"marshal/internal/viewmodel"
)

// The spine ties a beat's calls to the narration above them. Each row hangs
// off the beat's head: ├ for every row but the last, └ for the last, and │
// carrying the spine past an expanded row's detail. A row leads with a verb
// in a fixed column, then its subject, with its result right-aligned:
//
//	● Pointing the eval config at qwen3-coder.             thought 2s · ▸ 2/4
//	  ├ edit    evals/config.toml                                     +3 −3
//	  └ agent   review the config change                       ⧉ #1 started

const (
	// spineWidth is where a row's text starts: the connector sits under the
	// narration's first letter, followed by one space.
	spineWidth = gutterWidth + 2
	// verbWidth is the verb column: the longest verb ("search") and two
	// spaces. A row's detail hangs at the same indent, under its subject.
	verbWidth = 8
	// liveTailLines is how many lines of a running call's output show while
	// it is collapsed.
	liveTailLines = 6
	// maxPlanMarks is the longest list the plan line draws item by item; a
	// longer one draws the plan blocks instead.
	maxPlanMarks = 12
)

// callResult is a settled call's right-aligned result, styled: its error, a
// command's exit and duration, an edit's diff stat, the subagent it started,
// or its summary. A hook's decision leads.
func callResult(t *acpwire.WireTool, c acpwire.WireCall) string {
	var parts []string
	style := mutedStyle()
	if c.HookHint != "" {
		parts = append(parts, c.HookHint)
	}
	switch {
	case c.Error != "":
		parts = append(parts, firstLine(c.Error))
		style = errorStyle()
	case viewmodel.IsShellFamily(t.Name) && c.ExitCode != nil && c.KilledReason == "":
		parts = append(parts, fmt.Sprintf("exit %d", *c.ExitCode))
		if c.DurationMs > 0 {
			parts = append(parts, formatElapsed(time.Duration(c.DurationMs)*time.Millisecond))
		}
		if *c.ExitCode != 0 {
			style = errorStyle()
		}
	case c.DiffStat != "":
		parts = append(parts, c.DiffStat)
	case c.JobID != "":
		started := lipgloss.NewStyle().Foreground(theme.Current().StatusInfo).Render(glyph.Job+" "+c.JobID) +
			mutedStyle().Render(" started")
		if len(parts) > 0 {
			return mutedStyle().Render(viewmodel.ScrubText(strings.Join(parts, dimSeparator))+dimSeparator) + started
		}
		return started
	default:
		if t.SubagentID > 0 {
			started := lipgloss.NewStyle().Foreground(theme.Current().AccentSecondary).Render(fmt.Sprintf("%s #%d", glyph.Agent, t.SubagentID)) +
				mutedStyle().Render(" started")
			if len(parts) > 0 {
				return mutedStyle().Render(viewmodel.ScrubText(strings.Join(parts, dimSeparator))+dimSeparator) + started
			}
			return started
		}
		if c.SummaryLine != "" {
			parts = append(parts, c.SummaryLine)
		}
	}
	return style.Render(viewmodel.ScrubText(strings.Join(parts, dimSeparator)))
}

// spineHead lays out a row's first line in w cells: the verb in its column,
// the subject truncated to what the result leaves, and the styled result
// right-aligned. A result takes at most half the room after the verb.
func spineHead(verb, subject, result string, w int) string {
	v := verb + strings.Repeat(" ", max(verbWidth-ansi.StringWidth(verb), 1))
	room := max(w-ansi.StringWidth(v), 1)
	result = ansi.Truncate(result, max(room/2, 1), "…")
	rw := ansi.StringWidth(result)
	subRoom := room
	if rw > 0 {
		subRoom = max(room-rw-2, 1)
	}
	subject = ansi.Truncate(oneLine(subject), subRoom, "…")
	line := mutedStyle().Render(v) + lipgloss.NewStyle().Foreground(theme.Current().FGDefault).Render(subject)
	if rw > 0 {
		line += strings.Repeat(" ", max(room-ansi.StringWidth(subject)-rw, 2)) + result
	}
	return line
}

// spineBlock hangs a rendered row off the spine: the connector on its first
// line, the spine's continuation on the rest.
func spineBlock(block string, last bool) string {
	conn, cont := glyph.SpineMid, glyph.Spine
	if last {
		conn, cont = glyph.SpineEnd, " "
	}
	style := lipgloss.NewStyle().Foreground(dimColor)
	pad := strings.Repeat(" ", gutterWidth)
	var b strings.Builder
	for i, l := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
		g := cont
		if i == 0 {
			g = conn
		}
		b.WriteString(pad + style.Render(g) + " " + l + "\n")
	}
	return b.String()
}

// detailLines indents a row's detail under its subject, one line each.
func detailLines(b *strings.Builder, lines []string) {
	pad := strings.Repeat(" ", verbWidth)
	for _, l := range lines {
		b.WriteString(pad + l + "\n")
	}
}

// renderCallRow is a settled call's row. Collapsed it is one line, plus the
// callers of a symbol it changed and, when it failed, the last lines of its
// output. Expanded, its diff, result or error follow under the subject.
//
// t always carries the calls its node was built from: renderStep reaches this
// row only through spineRow, so a running, grouped or plan shape never gets
// here, and t.Calls[0] is the call that opened the row.
func renderCallRow(t *acpwire.WireTool, d *acpwire.CallDetail, expanded bool, callers []string, w int) string {
	call := t.Calls[0]
	var b strings.Builder
	b.WriteString(spineHead(t.Verb, call.Subject, callResult(t, call), w) + "\n")
	dw := max(w-verbWidth, 1)
	detailLines(&b, callersDetail(callers, dw))
	switch {
	case expanded:
		detailLines(&b, toolBody(t.Name, call, d, dw))
	case call.Failed:
		detailLines(&b, failedTail(call.Tail, dw))
	}
	return b.String()
}

// failedTail is the last few lines of a failed call's output, shown under its
// collapsed row so the reason is visible without opening it. The tail is the
// useful end: a test run prints its failures last.
func failedTail(lines []string, w int) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, mutedStyle().Render(ansi.Truncate(l, w, "…")))
	}
	return out
}

// failedTailLines is how many output lines a failed row shows collapsed.
const failedTailLines = viewmodel.FailedTailLines

// renderGroupRow is a merged run of same-tool calls: their subjects on one
// line and the count as the result. Expanded, one line per call follows with
// its subject and summary.
func renderGroupRow(t *acpwire.WireTool, expanded bool, w int) string {
	subjects := make([]string, 0, len(t.Calls))
	for _, c := range t.Calls {
		if c.Subject != "" {
			subjects = append(subjects, c.Subject)
		}
	}
	var b strings.Builder
	b.WriteString(spineHead(t.Verb, strings.Join(subjects, ", "), mutedStyle().Render(fmt.Sprintf("×%d", len(t.Calls))), w) + "\n")
	if expanded {
		dw := max(w-verbWidth, 1)
		var lines []string
		for _, c := range t.Calls {
			line := c.Subject
			if c.SummaryLine != "" {
				if line != "" {
					line += dimSeparator
				}
				line += c.SummaryLine
			}
			lines = append(lines, mutedStyle().Render(ansi.Truncate(line, dw, "…")))
		}
		detailLines(&b, lines)
	}
	return b.String()
}

// pointerColor is the kind colour of a work pointer, read from the glyph that
// leads it: a subagent in AccentSecondary, a command in StatusInfo, a watch
// muted.
func pointerColor(p string) color.Color {
	th := theme.Current()
	switch {
	case strings.HasPrefix(p, glyph.Agent):
		return th.AccentSecondary
	case strings.HasPrefix(p, glyph.Job):
		return th.StatusInfo
	}
	return th.FGMuted
}

// renderActiveCallRow is a call still running: what it acts on, never its
// raw arguments, with a spinner and its elapsed time as the result. A
// command's sandbox and the tail of its output follow; expanded, the whole
// captured output does.
func renderActiveCallRow(t *acpwire.WireTool, c *stepRenderCtx, expanded bool, w int) string {
	r := t.Running
	spin := c.toolSpinner
	if spin == "" {
		spin = spinnerFrames[0]
	}
	elapsed := formatElapsed(max(c.now.Sub(acpwire.Time(r.StartedAt)), 0))
	result := lipgloss.NewStyle().Foreground(accentColor).Render(spin + " " + elapsed)
	// A call whose work already has a row in the box points at that row: the
	// box shows the work, so the row keeps only its sandbox line.
	ptr := ""
	if c.pointer != nil {
		ptr = c.pointer(r.CallID)
	}
	if ptr != "" {
		result = lipgloss.NewStyle().Foreground(pointerColor(ptr)).Render("→ " + ptr)
	}
	var b strings.Builder
	b.WriteString(spineHead(t.Verb, r.Subject, result, w) + "\n")
	dw := max(w-verbWidth, 1)
	var lines []string
	if viewmodel.IsShellFamily(t.Name) {
		if iso := sandboxIsolationText(c.sandbox, c.allowNetwork); iso != "" {
			lines = append(lines, mutedStyle().Render(ansi.Truncate(iso, dw, "…")))
		}
	}
	if ptr == "" && r.Output != "" {
		out := strings.Split(strings.TrimRight(viewmodel.ScrubBlock(r.Output), "\n"), "\n")
		if !expanded && len(out) > liveTailLines {
			out = out[len(out)-liveTailLines:]
		}
		for _, line := range out {
			for _, wl := range strings.Split(ansi.Wrap(line, dw, WrapBreakpoints), "\n") {
				lines = append(lines, mutedStyle().Render(wl))
			}
		}
	}
	detailLines(&b, lines)
	return b.String()
}

// renderPlanRow is a beat's plan line: one mark per todo (✓ done, ▸ in
// progress, · waiting), the active todo's position and its text. A list
// longer than maxPlanMarks draws the plan blocks instead of marks.
func renderPlanRow(p *acpwire.WirePlan, w int) string {
	v := "plan" + strings.Repeat(" ", verbWidth-len("plan"))
	if p.Cleared {
		return mutedStyle().Render(v+"cleared") + "\n"
	}
	th := theme.Current()
	var marks string
	if p.Total <= maxPlanMarks {
		parts := make([]string, 0, max(p.Total, 0))
		for _, st := range p.Status {
			switch st {
			case "completed":
				parts = append(parts, lipgloss.NewStyle().Foreground(th.StatusSuccess).Render(glyph.OK))
			case "in_progress":
				parts = append(parts, lipgloss.NewStyle().Foreground(accentColor).Render(glyph.Running))
			default:
				parts = append(parts, mutedStyle().Render(glyph.Ambient))
			}
		}
		marks = strings.Join(parts, " ")
	} else {
		marks = livebox.Blocks(p.Done, p.Total, 8)
	}
	pos, text := fmt.Sprintf("%d/%d", p.Done, p.Total), ""
	switch {
	case p.Active > 0:
		pos, text = fmt.Sprintf("%d/%d", p.Active, p.Total), viewmodel.ScrubText(oneLine(p.ActiveText))
	case p.Done == p.Total:
		text = "done"
	}
	line := mutedStyle().Render(v) + marks + "  " + mutedStyle().Render(pos)
	if text != "" {
		room := max(w-ansi.StringWidth(line)-2, 1)
		line += "  " + ansi.Truncate(text, room, "…")
	}
	return ansi.Truncate(line, w, "…") + "\n"
}

// oneLine flattens line breaks and tabs to single spaces.
func oneLine(s string) string {
	return lineFlatten.Replace(s)
}

var lineFlatten = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")
