package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
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

// callVerbs maps a tool-name prefix to the verb its row leads with. The first
// matching prefix wins, so write tools come before the broader file prefix.
var callVerbs = []struct{ prefix, verb string }{
	{"file.read", "read"},
	{"file.write_patch", "edit"},
	{"file.edit", "edit"},
	{"patch.", "edit"},
	{"file.write", "write"},
	{"shell.", "shell"},
	{"test.", "shell"},
	{"repo.search", "search"},
	{"codebase.search", "search"},
	{"codebase_search", "search"},
	{"search.", "search"},
	{"symbols", "search"},
	{"lsp.", "read"},
	{"agent.", "agent"},
	{"web.", "web"},
	{"browser.", "web"},
	{"todo.write", "plan"},
	{"mcp.", "mcp"},
}

// callVerb is the verb a call's row leads with. A tool outside the table
// uses the first segment of its name ("git.status" reads "git").
func callVerb(name string) string {
	for _, v := range callVerbs {
		if strings.HasPrefix(name, v.prefix) {
			return v.verb
		}
	}
	first, _, _ := strings.Cut(name, ".")
	return first
}

// fallbackSubject is a row's subject when the call's arguments name none:
// the rest of the tool's name after its first segment ("git.status" reads
// "git status", "agent.await" reads "agent await"), unless that only repeats
// the verb ("file.read" reads "read", not "read read").
func fallbackSubject(name string) string {
	_, rest, ok := strings.Cut(name, ".")
	if !ok || rest == callVerb(name) {
		return ""
	}
	return strings.ReplaceAll(rest, ".", " ")
}

// toolTarget returns the short human-facing subject of a tool call — the
// path it read, the command it ran, the query it searched for.
func toolTarget(event registry.AuditEvent) string { return viewmodel.ToolTarget(event) }

// callSubject is what a settled call acted on: the command it ran, the file
// or symbols it touched, the query it searched for, or the task it handed to
// a subagent. It is model-authored text, so it is scrubbed here.
func callSubject(ev registry.AuditEvent, workdir string) string {
	var s string
	switch {
	case viewmodel.IsShellFamily(ev.ToolName):
		s = viewmodel.ElideWorkdir(toolTarget(ev), workdir)
	case ev.ToolName == "agent.run":
		s = argString(ev.Args, "description")
	default:
		if sym := symbolSubject(ev); sym != "" && subjectFirstTool(ev.ToolName) {
			s = sym
		} else if q, ok := searchQualifier(ev.ToolName); ok {
			if t := searchTarget(ev); t != "" {
				s = strconv.Quote(t)
				// The verb already says "search"; any other qualifier tells
				// two kinds of search apart.
				if q != "search" {
					s = q + " " + s
				}
			}
		} else {
			s = toolTarget(ev)
		}
	}
	if s == "" {
		s = fallbackSubject(ev.ToolName)
	}
	return viewmodel.ScrubText(firstLine(s))
}

// callResult is a settled call's right-aligned result, styled: its error, a
// command's exit and duration, an edit's diff stat, the subagent it started,
// or its summary. A hook's decision leads.
func callResult(ev registry.AuditEvent) string {
	var parts []string
	style := mutedStyle()
	if h := hookIndicatorText(ev.Hooks); h != "" {
		parts = append(parts, h)
	}
	switch {
	case ev.Error != "":
		parts = append(parts, firstLine(ev.Error))
		style = errorStyle()
	case viewmodel.IsShellFamily(ev.ToolName) && ev.CommandExitCode != nil && ev.Sandbox.KilledReason == "":
		parts = append(parts, fmt.Sprintf("exit %d", *ev.CommandExitCode))
		if ev.Duration > 0 {
			parts = append(parts, formatElapsed(ev.Duration))
		}
		if *ev.CommandExitCode != 0 {
			style = errorStyle()
		}
	case (isDiffTool(ev.ToolName) || ev.ToolName == "file.write") && diffStat(ev.ResultContent) != "":
		parts = append(parts, diffStat(ev.ResultContent))
	default:
		if id := startedSubagent(ev); id > 0 {
			started := lipgloss.NewStyle().Foreground(theme.Current().AccentSecondary).Render(fmt.Sprintf("%s #%d", glyph.Agent, id)) +
				mutedStyle().Render(" started")
			if len(parts) > 0 {
				return mutedStyle().Render(viewmodel.ScrubText(strings.Join(parts, dimSeparator))+dimSeparator) + started
			}
			return started
		}
		if s := viewmodel.DisplaySummary(ev); s != "" {
			parts = append(parts, firstLine(s))
		}
	}
	return style.Render(viewmodel.ScrubText(strings.Join(parts, dimSeparator)))
}

// startedSubagent is the subagent an agent.run call started in the
// background, 0 when it started none. The notice's structured reference is
// read first; a record that predates it falls back to the summary the tool
// wrote ("started as subagent 3 — …").
func startedSubagent(ev registry.AuditEvent) int64 {
	if ev.ToolName != "agent.run" || ev.Error != "" {
		return 0
	}
	if ref, ok := ev.Notice.SubagentWork(); ok {
		return ref.AgentID
	}
	var id int64
	if _, err := fmt.Sscanf(ev.ResultSummary, "started as subagent %d", &id); err == nil {
		return id
	}
	return 0
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
func renderCallRow(ev registry.AuditEvent, expanded bool, callers []string, w int, workdir string) string {
	var b strings.Builder
	b.WriteString(spineHead(callVerb(ev.ToolName), callSubject(ev, workdir), callResult(ev), w) + "\n")
	dw := max(w-verbWidth, 1)
	detailLines(&b, callersDetail(callers, dw))
	switch {
	case expanded:
		detailLines(&b, toolBody(ev, true, dw))
	case viewmodel.EventFailed(ev):
		detailLines(&b, failedTail(ev, dw))
	}
	return b.String()
}

// failedTail is the last few lines of a failed call's output, shown under its
// collapsed row so the reason is visible without opening it. The tail is the
// useful end: a test run prints its failures last.
func failedTail(ev registry.AuditEvent, w int) []string {
	text := strings.TrimRight(ev.ResultContent, "\n")
	if text == "" {
		text = ev.Error
	}
	if text == "" {
		return nil
	}
	lines := strings.Split(viewmodel.ScrubBlock(text), "\n")
	if len(lines) > failedTailLines {
		lines = lines[len(lines)-failedTailLines:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, mutedStyle().Render(ansi.Truncate(strings.TrimRight(l, " \t\r"), w, "…")))
	}
	return out
}

// failedTailLines is how many output lines a failed row shows collapsed.
const failedTailLines = 3

// renderGroupRow is a merged run of same-tool calls: their subjects on one
// line and the count as the result. Expanded, one line per call follows with
// its subject and summary.
func renderGroupRow(evs []registry.AuditEvent, expanded bool, w int, workdir string) string {
	subjects := make([]string, 0, len(evs))
	for _, ev := range evs {
		if s := callSubject(ev, workdir); s != "" {
			subjects = append(subjects, s)
		}
	}
	var b strings.Builder
	b.WriteString(spineHead(callVerb(evs[0].ToolName), strings.Join(subjects, ", "), mutedStyle().Render(fmt.Sprintf("×%d", len(evs))), w) + "\n")
	if expanded {
		dw := max(w-verbWidth, 1)
		var lines []string
		for _, ev := range evs {
			line := callSubject(ev, workdir)
			if s := viewmodel.DisplaySummary(ev); s != "" {
				if line != "" {
					line += dimSeparator
				}
				line += viewmodel.ScrubText(firstLine(s))
			}
			lines = append(lines, mutedStyle().Render(ansi.Truncate(line, dw, "…")))
		}
		detailLines(&b, lines)
	}
	return b.String()
}

// renderJobRow is the row of a shell call that started a background job,
// settled in place by the job's exit: the command, then the job and its state
// as the result. Expanded, the job's output follows.
func renderJobRow(row *viewmodel.Node, spinner string, now time.Time, expanded bool, w int, workdir string) string {
	j := row.Job
	// The command is model-authored shell text, scrubbed here as well as at
	// the viewmodel boundary.
	cmd := viewmodel.ScrubText(firstLine(viewmodel.ElideWorkdir(j.Command, workdir)))
	id := lipgloss.NewStyle().Foreground(theme.Current().StatusInfo).Render(glyph.Job + " " + viewmodel.ScrubText(j.ID))
	state, style := jobState(row, spinner, now)
	var b strings.Builder
	b.WriteString(spineHead("shell", cmd, id+" "+style.Render(state), w) + "\n")
	if expanded && j.Output != "" {
		dw := max(w-verbWidth, 1)
		var lines []string
		// Scrubbed at the projection and again here, at this renderer's own
		// boundary; the session caps the output, so the second pass is cheap.
		for _, line := range strings.Split(strings.TrimRight(viewmodel.ScrubBlock(j.Output), "\n"), "\n") {
			for _, wl := range strings.Split(ansi.Wrap(line, dw, WrapBreakpoints), "\n") {
				lines = append(lines, mutedStyle().Render(wl))
			}
		}
		detailLines(&b, lines)
	}
	return b.String()
}

// jobState is a job row's state after its ID: running with its elapsed time,
// stopping, its outcome once settled, or why it has none.
func jobState(row *viewmodel.Node, spinner string, now time.Time) (string, lipgloss.Style) {
	j := row.Job
	var state string
	style := mutedStyle()
	switch {
	case j.Lost:
		// The session ended while the job ran: there is no exit code to
		// report, and the row must not claim one.
		state, style = "ended with session", warningStyle()
	case j.Status == string(native.StatusStopping):
		// The stop was accepted and the process is still going. Checked before
		// running: a stopping execution has no outcome yet.
		state, style = "stopping", warningStyle()
		if j.Duration > 0 {
			state += dimSeparator + formatElapsed(j.Duration)
		}
	case j.Running:
		state = "running"
		// JobStart, not Tools[0]: a caller-built tree can hold a running job
		// whose starting call is not attached, and the row still renders.
		if started := row.JobStart(); !started.IsZero() {
			state += dimSeparator + formatElapsed(max(now.Sub(started), 0))
		}
		if spinner != "" {
			state = spinner + " " + state
		}
	default:
		// Settled. An exit that was never observed is never success: a start-up
		// failure produces no code at all, and a legacy record spells "unknown"
		// as -1, so both render as unknown rather than as a clean exit.
		code := j.Exit
		if code == nil && j.ExitCode >= 0 {
			legacy := j.ExitCode
			code = &legacy
		}
		switch {
		case jobFailedStatus(j.Status) || (code != nil && *code != 0):
			style = errorStyle()
		case code == nil:
			style = warningStyle()
		}
		state = jobOutcomeWord(j, code)
		if j.Duration > 0 {
			state += dimSeparator + formatElapsed(j.Duration)
		}
	}
	if j.Truncated {
		state += dimSeparator + "truncated"
	}
	return state, style
}

// renderActiveCallRow is a call still running: what it acts on, never its
// raw arguments, with a spinner and its elapsed time as the result. A
// command's sandbox and the tail of its output follow; expanded, the whole
// captured output does.
func renderActiveCallRow(atc session.ActiveToolCall, c *stepRenderCtx, expanded bool, w int) string {
	subject := atc.Path
	if subject == "" {
		subject = firstLine(atc.Args)
	}
	if subject == "" {
		subject = fallbackSubject(atc.Name)
	}
	spin := c.toolSpinner
	if spin == "" {
		spin = spinnerFrames[0]
	}
	elapsed := formatElapsed(max(c.now.Sub(atc.StartedAt), 0))
	result := lipgloss.NewStyle().Foreground(accentColor).Render(spin + " " + elapsed)
	var b strings.Builder
	b.WriteString(spineHead(callVerb(atc.Name), viewmodel.ScrubText(subject), result, w) + "\n")
	dw := max(w-verbWidth, 1)
	var lines []string
	if viewmodel.IsShellFamily(atc.Name) {
		if iso := sandboxIsolationText(c.sandbox, c.allowNetwork); iso != "" {
			lines = append(lines, mutedStyle().Render(ansi.Truncate(iso, dw, "…")))
		}
	}
	if atc.Output != "" {
		out := strings.Split(strings.TrimRight(viewmodel.ScrubBlock(atc.Output), "\n"), "\n")
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
func renderPlanRow(p *viewmodel.PlanUpdate, w int) string {
	v := "plan" + strings.Repeat(" ", verbWidth-len("plan"))
	if p.Cleared {
		return mutedStyle().Render(v+"cleared") + "\n"
	}
	th := theme.Current()
	var marks string
	if p.Total() <= maxPlanMarks {
		parts := make([]string, 0, p.Total())
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
		marks = progressBlocks(p.Done(), p.Total(), 8)
	}
	pos, text := fmt.Sprintf("%d/%d", p.Done(), p.Total()), ""
	switch {
	case p.Active > 0:
		pos, text = fmt.Sprintf("%d/%d", p.Active, p.Total()), viewmodel.ScrubText(oneLine(p.ActiveText))
	case p.Done() == p.Total():
		text = "done"
	}
	line := mutedStyle().Render(v) + marks + "  " + mutedStyle().Render(pos)
	if text != "" {
		room := max(w-ansi.StringWidth(line)-2, 1)
		line += "  " + ansi.Truncate(text, room, "…")
	}
	return ansi.Truncate(line, w, "…") + "\n"
}
