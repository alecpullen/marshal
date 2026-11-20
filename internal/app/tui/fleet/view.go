package fleet

import (
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpclient"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/layout"
	"marshal/web/bridge/contract"
)

// The screen's dimensions. Below them the screen says so and draws nothing
// else (fleet-cli §3.1).
const (
	minScreenWidth  = 80
	minScreenHeight = 24

	// footerRows is the single row the footer takes at the bottom.
	footerRows = 1

	// narrowLeft is the left column's width below layout.WideBreakpoint. From
	// it up the table takes the larger pane and the detail panel keeps
	// wideRight percent: the Agents table carries up to ten columns, and at
	// the other way round its fixed columns alone fill the pane.
	narrowLeft = 28
	wideRight  = 40

	// wideWidth is the terminal width from which the Agents panel adds
	// ACTIVITY, COST and CHECK.
	wideWidth = 140

	// contextCells is the Info tab's context bar width.
	contextCells = 20

	// labelWidth is the label column of the right panel's key/value lines.
	labelWidth = 10

	// maxNeedsRows is the Needs you panel's tallest content, in rows.
	maxNeedsRows = 5
	// eventRowsNarrow and eventRowsWide are the Events panel's content rows
	// below and from eventWideHeight rows of terminal height.
	eventRowsNarrow = 3
	eventRowsWide   = 6
	eventWideHeight = 30

	// tooSmallText is the whole screen when the terminal is below 80×24.
	tooSmallText = "terminal too small (80×24)"
)

// View draws the fleet screen. In Bubble Tea v2 the alternate screen and the
// mouse mode are view fields: the fleet screen asks for the alternate screen
// and nothing else, so the terminal's own selection keeps working (decision
// 14).
func (m Model) View() tea.View {
	v := tea.NewView(m.screen())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeNone
	return v
}

// screen renders the whole screen: the left column, the right panel and the
// footer, or the help overlay or the zoomed panel.
func (m Model) screen() string {
	if m.width < minScreenWidth || m.height < minScreenHeight {
		return tooSmallText
	}
	if m.help {
		return m.helpScreen()
	}
	if m.zoom {
		return m.zoomScreen()
	}

	avail := m.height - footerRows
	left, right := m.columnWidths()
	agentsH, needsH, eventsH := m.leftHeights(avail)

	agents, rows := m.panelAgents(left, agentsH)
	leftLines := splitLines(agents)
	leftLines = append(leftLines, splitLines(m.panelNeedsYou(left, needsH))...)
	leftLines = append(leftLines, splitLines(m.panelEvents(left, eventsH))...)
	rightLines := splitLines(m.panelRight(right, avail))
	if m.modal != nil {
		// A modal is drawn over the right panel: the left column and the
		// footer stay where they are.
		rightLines = splitLines(m.modal.view(m, right, avail))
	}

	body := joinColumns(leftLines, left, rightLines, right)
	lines := fitScreen(body, m.width, avail)
	lines = append(lines, m.footer(rows))
	return strings.Join(fitScreen(lines, m.width, m.height), "\n")
}

// columnWidths splits the terminal width between the left column and the
// right panel. The two columns share their boundary — no gap between them.
func (m Model) columnWidths() (left, right int) {
	if m.width < layout.WideBreakpoint {
		left = narrowLeft
	} else {
		left = m.width - m.width*wideRight/100
	}
	left = clamp(left, 1, m.width)
	return left, m.width - left
}

// leftHeights distributes the left column's height over its three panels.
// Each panel is a closed frame, so it costs two border rows; Agents takes what
// is left, and at least its top border.
func (m Model) leftHeights(avail int) (agents, needs, events int) {
	needsRows := clamp(len(m.state.Inbox.Queue), 1, maxNeedsRows)
	eventsRows := eventRowsNarrow
	if m.height >= eventWideHeight {
		eventsRows = eventRowsWide
	}
	needs = needsRows + 2
	events = eventsRows + 2
	agents = avail - needs - events
	if agents < 1 {
		agents = 1
	}
	return agents, needs, events
}

// frame draws one closed panel with its content padded to its height, so a
// stacked column lines up.
func (m Model) frame(title, hints string, content []string, outer, h int, focused bool) string {
	rows := max(h-2, 0)
	if len(content) > rows {
		content = content[:rows]
	}
	for len(content) < rows {
		content = append(content, "")
	}
	f := chrome.Frame{Title: title, Hints: hints, Closed: true, Focused: focused}
	return f.Render(strings.Join(content, "\n"), outer, h, m.th)
}

// panelAgents draws the Agents panel and reports the rows it drew, so the
// footer can tell whether ⏎ would open anything without rebuilding them.
func (m Model) panelAgents(outer, h int) (string, []agentRow) {
	focused := m.focus == focusAgents
	if !m.state.Loaded {
		return m.frame("Agents", "", []string{m.connecting()}, outer, h, focused), nil
	}
	lines, rows := m.agentLines()
	cols := m.agentColumns()
	widths := agentWidths(max(outer-4, 1), cols)
	body := make([]string, 0, len(lines))
	focusLine := 0
	for _, ln := range lines {
		if ln.header {
			body = append(body, m.plain(ln.text))
			continue
		}
		if ln.row < 0 || ln.row >= len(rows) {
			continue
		}
		row := rows[ln.row]
		selected := row.id == m.selected
		if selected {
			focusLine = len(body)
		}
		body = append(body, m.agentLine(row, cols, widths, selected))
	}
	if room := max(h-2, 0); room > 0 && len(body) > room {
		body = splitLines(chrome.ClipLines(body, focusLine, room, m.th))
	}
	return m.frame("Agents", strconv.Itoa(len(rows)), body, outer, h, focused), rows
}

// panelNeedsYou draws the needs-you queue, oldest first. A queue longer than
// the panel scrolls, so the row under the cursor stays visible.
func (m Model) panelNeedsYou(outer, h int) string {
	focused := m.focus == focusNeedsYou
	if !m.state.Loaded {
		return m.frame("Needs you", "", []string{m.connecting()}, outer, h, focused)
	}
	queue := m.state.Inbox.Queue
	inner := max(outer-4, 1)
	rows := max(h-2, 0)
	start := needsYouWindow(len(queue), m.needsCursor, rows)
	body := make([]string, 0, rows)
	for i := start; i < len(queue) && i-start < rows; i++ {
		text := cut(m.needsYouText(queue[i]), inner)
		if focused && i == m.needsCursor {
			text = chrome.SelectionStyle().Render(text)
		} else {
			text = m.plain(text)
		}
		body = append(body, text)
	}
	return m.frame("Needs you", strconv.Itoa(len(queue)), body, outer, h, focused)
}

// needsYouWindow is the index of the first queue row the panel shows. A queue
// longer than the panel scrolls around the cursor's row, so the row the user
// is on is never hidden.
func needsYouWindow(queue, cursor, rows int) int {
	if rows <= 0 || queue <= rows {
		return 0
	}
	start := cursor - rows/2
	if start < 0 {
		start = 0
	}
	if start > queue-rows {
		start = queue - rows
	}
	return start
}

// panelEvents draws the Events ring's newest lines, newest at the bottom.
func (m Model) panelEvents(outer, h int) string {
	focused := m.focus == focusEvents
	if !m.state.Loaded {
		return m.frame("Events", "", []string{m.connecting()}, outer, h, focused)
	}
	rows := max(h-2, 0)
	events := m.events
	if len(events) > rows {
		events = events[len(events)-rows:]
	}
	body := make([]string, 0, len(events))
	for _, e := range events {
		body = append(body, m.plain(e))
	}
	return m.frame("Events", strconv.Itoa(len(m.events)), body, outer, h, focused)
}

// panelRight draws the selected agent's Activity or Info tab, or a session's
// details.
func (m Model) panelRight(outer, h int) string {
	focused := m.focus == focusRight
	title := m.rightTitle()
	if !m.state.Loaded {
		return m.frame(title, "", []string{m.connecting()}, outer, h, focused)
	}
	row, ok := m.selectedRow()
	if !ok {
		return m.frame(title, "", []string{m.plain("no agent selected")}, outer, h, focused)
	}
	inner := max(outer-4, 1)
	rows := max(h-2, 0)
	var body []string
	switch {
	case row.session:
		body = m.sessionLines(row, inner)
	default:
		a, found := m.agentByID(row.id)
		if !found {
			body = []string{m.plain("no agent selected")}
			break
		}
		switch m.tab {
		case diffTab:
			body = m.diffLines(inner, rows)
		case 3:
			body = m.infoLines(a)
		default:
			body = m.activityLines(a, inner)
		}
	}
	return m.frame(title, "", body, outer, h, focused)
}

// rightTitle is the right panel's title: its tab strip, or Session for a
// session row. The Diff tab adds its name to the strip only while it is shown,
// so the strip the other two tabs draw keeps its exact text.
func (m Model) rightTitle() string {
	if row, ok := m.selectedRow(); ok && row.session {
		return "Session"
	}
	selected := lipgloss.NewStyle().Foreground(m.th.AccentSecondary)
	other := lipgloss.NewStyle().Foreground(m.th.FGMuted)
	if m.tab == diffTab {
		return other.Render("Activity") + other.Render("  ") +
			selected.Render("[Diff]") + other.Render("  ") + other.Render("Info")
	}
	if m.tab == 3 {
		return other.Render("Activity") + other.Render("  ") + selected.Render("[Info]")
	}
	return selected.Render("[Activity]") + other.Render("  ") + other.Render("Info")
}

// selectedRow is the row the selection names.
func (m Model) selectedRow() (agentRow, bool) {
	_, rows := m.agentLines()
	if i, ok := rowIndex(rows, m.selected); ok {
		return rows[i], true
	}
	return agentRow{}, false
}

// agentColumn is one Agents panel column: its title, its fixed width (or its
// share of the slack when flexible), and whether its values are right-aligned.
type agentColumn struct {
	title string
	width int
	flex  bool
	right bool
}

// agentColumns is the column set for the terminal's width band (fleet-cli
// §3.1).
func (m Model) agentColumns() []agentColumn {
	cols := []agentColumn{
		{title: "ST", width: 1},
		{title: "NAME", flex: true},
		{title: "CTX", width: 4, right: true},
		{title: "Δ", width: 3, right: true},
		{title: "AGE", width: 5, right: true},
	}
	if m.width >= layout.WideBreakpoint {
		cols = append(cols,
			agentColumn{title: "PROJECT", width: 12},
			agentColumn{title: "MODE", width: 6},
		)
	}
	if m.width >= wideWidth {
		cols = append(cols,
			agentColumn{title: "ACTIVITY", flex: true},
			agentColumn{title: "COST", width: 6, right: true},
			agentColumn{title: "CHECK", width: 4},
		)
	}
	return cols
}

// agentWidths gives every column its width: the fixed columns take their widest
// value, and NAME and ACTIVITY split what is left, one cell each at least.
func agentWidths(inner int, cols []agentColumn) []int {
	gaps := len(cols) - 1
	fixed, flex := 0, 0
	for _, c := range cols {
		if c.flex {
			flex++
			continue
		}
		fixed += c.width
	}
	slack := inner - fixed - gaps - flex
	if slack < 0 {
		slack = 0
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		if !c.flex {
			widths[i] = c.width
			continue
		}
		widths[i] = 1
		if flex > 0 {
			widths[i] += slack / flex
		}
	}
	// Below the widths the bands are drawn at, the fixed columns and their
	// gaps can still outgrow the pane. Shave the trailing fixed columns so
	// the row stays inside its frame.
	for total := columnTotal(widths, gaps); total > inner; total = columnTotal(widths, gaps) {
		shrunk := false
		for i := len(widths) - 1; i >= 0; i-- {
			if cols[i].flex || widths[i] <= 1 {
				continue
			}
			widths[i]--
			shrunk = true
			break
		}
		if !shrunk {
			break
		}
	}
	return widths
}

// columnTotal is a row's width: its cells and one space between them.
func columnTotal(widths []int, gaps int) int {
	total := gaps
	for _, w := range widths {
		total += w
	}
	return total
}

// agentLine renders one selectable agent row.
func (m Model) agentLine(r agentRow, cols []agentColumn, widths []int, selected bool) string {
	if r.session {
		return m.sessionLine(r, widths, selected)
	}
	a, _ := m.agentByID(r.id)
	segs := make([]string, 0, len(cols)*2-1)
	for i, c := range cols {
		if i > 0 {
			segs = append(segs, " ")
		}
		var text string
		switch c.title {
		case "ST":
			text = padTo(m.stateGlyph(r.id), widths[i])
		case "NAME":
			text = m.textCell(agentName(a), widths[i], false)
		case "CTX":
			text = m.textCell(fmt.Sprintf("%d%%", a.ContextPct), widths[i], true)
		case "Δ":
			text = m.textCell(strconv.Itoa(a.ChangedFiles), widths[i], true)
		case "AGE":
			text = m.textCell(m.age(a.UpdatedAt), widths[i], true)
		case "PROJECT":
			text = m.textCell(projectName(a.Project), widths[i], false)
		case "MODE":
			text = m.textCell(modeText(a), widths[i], false)
		case "ACTIVITY":
			text = m.textCell(nonEmpty(a.Activity), widths[i], false)
		case "COST":
			text = m.textCell(m.costText(a.ID), widths[i], true)
		case "CHECK":
			text = m.textCell(m.checkText(a.ID), widths[i], false)
		}
		segs = append(segs, text)
	}
	return m.joinSelection(segs, selected)
}

// sessionLine renders one daemon session: a muted dot, the project's base
// name, the session ID, its viewers and its age. Sessions have no inbox group,
// context, changed-file count, cost or gate.
func (m Model) sessionLine(r agentRow, widths []int, selected bool) string {
	inner := columnTotal(widths, len(widths)-1)
	viewers, last, known := m.sessionInfo(r.id)
	label := projectName(r.project)
	if id := shortID(r.id); id != "" {
		label += " " + id
	}
	if known {
		label += " " + viewersText(viewers)
	}
	age := "–"
	if known && last > 0 {
		age = m.age(time.UnixMilli(last))
	}
	dot := m.mutedCell(glyph.Ambient)
	head := dot + " " + label
	gap := inner - ansi.StringWidth(head) - ansi.StringWidth(age) - 1
	if gap < 1 {
		// Shrink the label so the age keeps its column.
		label = cut(label, max(inner-ansi.StringWidth(age)-3, 1))
		head = dot + " " + label
		gap = max(inner-ansi.StringWidth(head)-ansi.StringWidth(age), 1)
	}
	segs := []string{dot, " " + label, strings.Repeat(" ", gap), age}
	line := m.joinSelection(segs, selected)
	return padTo(cut(line, inner), inner)
}

// stateGlyph is the ST glyph for an agent, and its colour, from the inbox
// group the agent is in (decision 9). The glyph never stands alone: it always
// carries the group's colour, and none lacks one.
func (m Model) stateGlyph(id string) string {
	g, col := glyph.Ambient, m.th.FGMuted
	switch {
	case containsID(m.state.Inbox.Groups.NeedsYou, id):
		g, col = glyph.Warning, m.th.StatusWarning
	case containsID(m.state.Inbox.Groups.Running, id):
		g, col = glyph.Live, m.th.StatusInfo
	case containsID(m.state.Inbox.Groups.Ready, id):
		g, col = glyph.OK, m.th.StatusSuccess
	case containsID(m.state.Inbox.Groups.Earlier, id):
		if a, ok := m.agentByID(id); ok && a.Status == contract.AgentError {
			g, col = glyph.Error, m.th.StatusError
		}
	}
	if m.dimmed {
		// While the stream is down every panel's text is muted, the state
		// glyphs included.
		return lipgloss.NewStyle().Foreground(m.th.FGMuted).Render(g)
	}
	return lipgloss.NewStyle().Foreground(col).Render(g)
}

// mutedCell is a single muted glyph: the session row's dot.
func (m Model) mutedCell(g string) string {
	return lipgloss.NewStyle().Foreground(m.th.FGMuted).Render(g)
}

// needsYouText is one Needs you line: an agent's name and pending kind, or an
// intake's origin and title.
func (m Model) needsYouText(item contract.InboxItem) string {
	if item.Kind == contract.InboxItemIntake {
		if item.Submission == nil {
			return "intake"
		}
		if item.Submission.Origin == "" {
			return "intake: " + item.Submission.Title
		}
		return "intake " + item.Submission.Origin + ": " + item.Submission.Title
	}
	name := "–"
	kind := "–"
	if a, ok := m.agentByID(item.AgentID); ok {
		name = agentName(a)
		if a.Pending != nil {
			kind = string(a.Pending.Kind)
		}
	} else if item.AgentID != "" {
		name = shortID(item.AgentID)
	}
	return name + " " + kind
}

// activityLines is the Activity tab: the agent's status and activity, what it
// waits on, its context, changed files and age, and its latest gate.
func (m Model) activityLines(a contract.AgentStatus, inner int) []string {
	lines := []string{
		m.kv("status", statusText(a.Status)),
		m.kv("activity", nonEmpty(a.Activity)),
		m.kv("waiting", pendingKindText(a.Pending)),
	}
	if a.Pending != nil {
		lines = append(lines, m.kv("request", describePending(a.Pending)))
		if m.answer == a.ID {
			// The approval is answered here, inline under the status
			// (decision 4).
			lines = append(lines, m.approvalLines(a.Pending, inner)...)
		}
	}
	lines = append(lines,
		m.kv("context", fmt.Sprintf("%d%%", a.ContextPct)),
		m.kv("changed", strconv.Itoa(a.ChangedFiles)),
		m.kv("age", m.age(a.UpdatedAt)),
	)
	if text, ok := m.gateText(a.ID); ok {
		lines = append(lines, m.kv("gate", text+" · "+m.gateTime(a.ID)))
	}
	return lines
}

// infoLines is the Info tab (fleet-cli §3.6).
func (m Model) infoLines(a contract.AgentStatus) []string {
	mode := "–"
	if a.Access != nil {
		mode = nonEmpty(a.Access.Mode)
	}
	lines := []string{
		m.kv("mode", mode),
		m.kv("origin", nonEmpty(a.Origin)),
		m.kv("owner", nonEmpty(a.OwnerID)),
		m.kv("context", m.contextBar(a.ContextPct)),
		m.kv("cost", m.costCapText(a.ID)),
		m.kv("branch", branchText(a)),
	}
	if a.VersionWarning != "" {
		// The daemon's own sentence, as it arrived and in the warning
		// colour (decision 5).
		lines = append(lines, m.kvCell("version", m.warn(a.VersionWarning)))
	}
	if text, ok := m.gateText(a.ID); ok {
		lines = append(lines, m.kv("gate", text+" · "+m.gateTime(a.ID)))
	} else {
		lines = append(lines, m.kv("gate", "–"))
	}
	lines = append(lines,
		m.kv("workspace", workspaceText(a.Workspace)),
		m.kv("pr", nonEmpty(a.PRUrl)),
		m.kv("age", m.age(a.UpdatedAt)),
	)
	return lines
}

// sessionLines is the right panel for a daemon session: its project, session
// ID, viewers and last activity.
func (m Model) sessionLines(r agentRow, inner int) []string {
	viewers, last, known := m.sessionInfo(r.id)
	viewerText, lastText := "–", "–"
	if known {
		viewerText = viewersText(viewers)
		lastText = m.age(time.UnixMilli(last))
	}
	return []string{
		m.kv("project", projectName(r.project)),
		m.kv("session", nonEmpty(r.id)),
		m.kv("viewers", viewerText),
		m.kv("last", lastText),
	}
}

// kv renders one label and value of the right panel.
func (m Model) kv(label, value string) string {
	return m.kvCell(label, m.plain(value))
}

// kvCell renders one label and an already-styled value.
func (m Model) kvCell(label, value string) string {
	if len(label) >= labelWidth {
		label = label[:labelWidth-1]
	}
	return label + strings.Repeat(" ", labelWidth-len(label)) + value
}

// warn is text in the warning colour, muted like every other value while the
// fleet is disconnected.
func (m Model) warn(s string) string {
	if m.dimmed {
		return m.plain(s)
	}
	return lipgloss.NewStyle().Foreground(m.th.StatusWarning).Render(s)
}

// contextBar draws the 20-cell context bar and its percentage.
func (m Model) contextBar(pct int) string {
	pct = clamp(pct, 0, 100)
	filled := pct * contextCells / 100
	bar := strings.Repeat(glyph.ProgressFull, filled) +
		strings.Repeat(glyph.ProgressEmpty, contextCells-filled)
	return bar + fmt.Sprintf(" %d%%", pct)
}

// age is the time since t, on the model's clock: seconds under a minute,
// minutes under an hour, hours under a day, then days. A row with no time
// reads as –, like every other unknown.
func (m Model) age(t time.Time) string {
	if t.IsZero() {
		return "–"
	}
	d := m.now().Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	default:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
}

// sessionInfo finds a daemon session by ID: its viewers and last-active stamp.
func (m Model) sessionInfo(id string) (viewers int, lastActive int64, ok bool) {
	if m.state.Daemon == nil {
		return 0, 0, false
	}
	for _, child := range m.state.Daemon.Children {
		for _, s := range child.Sessions {
			if s.ID == id {
				return s.Viewers, s.LastActive, true
			}
		}
	}
	return 0, 0, false
}

// costText is the agent's spend against its budget, as $0.00, or – when the
// budgets are not loaded or the agent is not in the report.
func (m Model) costText(id string) string {
	spent, ok := m.agentSpend(id)
	if !ok {
		return "–"
	}
	return fmt.Sprintf("$%.2f", spent)
}

// costCapText is the spend against the per-agent cap, e.g. "$1.25 / $10.00".
func (m Model) costCapText(id string) string {
	spent, ok := m.agentSpend(id)
	if !ok {
		return "–"
	}
	return fmt.Sprintf("$%.2f / $%.2f", spent, m.state.Budgets.Budgets.PerAgentUSD)
}

// agentSpend looks an agent up in the budget report.
func (m Model) agentSpend(id string) (float64, bool) {
	if m.state.Budgets == nil || !m.state.Budgets.Loaded {
		return 0, false
	}
	for _, a := range m.state.Budgets.Agents {
		if a.AgentID == id {
			return a.SpentUSD, true
		}
	}
	return 0, false
}

// checkText is the latest gate: pass, fail or –. A skipped gate did not pass,
// so it is a fail here.
func (m Model) checkText(id string) string {
	text, ok := m.gateText(id)
	if !ok {
		return "–"
	}
	return text
}

// gateText is pass or fail for the agent's latest gate, and whether it has one.
func (m Model) gateText(id string) (string, bool) {
	rec, ok := m.state.Gates[id]
	if !ok {
		return "", false
	}
	if rec.Result != nil && rec.Result.OK {
		return "pass", true
	}
	return "fail", true
}

// gateTime is when the agent's latest gate ran.
func (m Model) gateTime(id string) string {
	rec, ok := m.state.Gates[id]
	if !ok {
		return "–"
	}
	return rec.At.Format("15:04")
}

// describePending is a one-line summary of what an agent is waiting on. It is a
// port of describePending (web/ui/src/lib/fleet.ts:119): the payload's shape
// varies by tool, so every field is probed defensively and the generic label is
// the floor. It never fails on a payload of another shape.
func describePending(p *contract.PendingRequest) string {
	fallback := "Answer required"
	if p.Kind == contract.PendingApproval {
		fallback = "Approval required"
	}
	if len(p.Params) == 0 {
		return fallback
	}
	var params map[string]any
	if err := json.Unmarshal(p.Params, &params); err != nil || params == nil {
		return fallback
	}
	if p.Kind == contract.PendingQuestion {
		questions, ok := params["questions"].([]any)
		if !ok || len(questions) == 0 {
			return fallback
		}
		first, ok := questions[0].(map[string]any)
		if !ok {
			return fallback
		}
		if text, ok := first["question"].(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
		return fallback
	}
	// An approval's command is the most informative thing when it is present:
	// that is what the operator is being asked to allow.
	if command, ok := params["command"].(string); ok && strings.TrimSpace(command) != "" {
		return command
	}
	if tool, ok := params["toolName"].(string); ok && strings.TrimSpace(tool) != "" {
		return tool
	}
	return fallback
}

// approvalLines is the open approval's prompt, drawn under the agent's status
// (decision 4): the request's text, its risk and its reason when present, and
// the two keys that answer it. Each is wrapped to the panel, so a long reason
// stays readable. A typed access request is shown but not answered here, so it
// offers no keys.
func (m Model) approvalLines(p *contract.PendingRequest, inner int) []string {
	width := max(inner-labelWidth, 8)
	var lines []string
	appendText := func(text string) {
		for _, ln := range wrapText(text, width) {
			lines = append(lines, m.plain(strings.Repeat(" ", labelWidth)+ln))
		}
	}
	if accessOnly(p) {
		appendText("open the agent to answer this access request")
		return lines
	}
	params := approvalOf(p)
	// The request's own text is the Activity tab's request row, directly
	// above: repeating it here would print the command twice.
	if params.Risk != "" {
		appendText("risk " + params.Risk)
	}
	if params.Reason != "" {
		appendText("reason " + params.Reason)
	}
	appendText("[y]es [n]o")
	return lines
}

// wrapText breaks plain text into lines of at most width cells, on its spaces.
// A word longer than a line is cut to fit.
func wrapText(s string, width int) []string {
	if width < 1 {
		return []string{s}
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		word = cut(word, width)
		switch {
		case line == "":
			line = word
		case ansi.StringWidth(line)+1+ansi.StringWidth(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// footer is the bottom row: the server and its connection dot, the focused
// panel's keys, and the global hints.
func (m Model) footer(rows []agentRow) string {
	dot, col := glyph.Waiting, m.th.StatusWarning
	switch m.state.Conn {
	case acpclient.FleetConnected:
		dot, col = glyph.Live, m.th.StatusSuccess
	case acpclient.FleetDown:
		dot, col = glyph.Error, m.th.StatusError
	}
	muted := lipgloss.NewStyle().Foreground(m.th.FGMuted)
	left := muted.Render(m.state.Server) + " " + lipgloss.NewStyle().Foreground(col).Render(dot)
	if m.state.Conn != acpclient.FleetConnected && m.state.ConnErr != "" {
		left += " " + muted.Render(m.state.ConnErr)
	}
	keys := ""
	switch m.focus {
	case focusAgents:
		keys = "j/k move a answer x cancel n spawn"
	case focusNeedsYou:
		// ⏎ settles an intake item here; on an agent item it opens, which
		// the ⏎ named below covers.
		keys = "j/k move n spawn"
		if _, ok := m.intakeItem(); ok {
			keys = "j/k move ⏎ intake n spawn"
		}
	case focusRight:
		keys = "1/3 tab n spawn"
		if m.tab == diffTab && m.shownAgentID() != "" {
			// The Diff tab's own keys, named only while it is shown, and only
			// on a row that has the tab: a Sessions row's right panel is the
			// session's details whatever tab is selected.
			keys = "j/k file J/K scroll 1/3 tab n spawn"
		}
	}
	// f and v are the finish flow's keys (decisions 2 and 3), named only where
	// they apply: f needs an idle agent, and v the Info tab of the right panel.
	if m.canFinish() {
		keys = addHint(keys, "f finish")
	}
	if m.focus == focusRight && m.tab == infoTab {
		keys = addHint(keys, "v gate")
	}
	// ⏎ is only worth naming while it would do something.
	if m.openTarget(rows) != (Target{}) {
		if keys == "" {
			keys = "⏎ open"
		} else {
			keys += "  ⏎ open"
		}
	}
	middle := ""
	if keys != "" {
		middle = "  " + chrome.TertiaryStyle().Render(keys)
	}
	hints := chrome.TertiaryStyle().Render("?help q quit")
	if status := m.statusText(); status != "" {
		// An action's failure, or a refusal while down, takes the hints'
		// place while it lasts, and the panel keys' room with it: the
		// sentence — the bridge's own reason — must not be clipped to make
		// room for keys nobody is pressing.
		hints = chrome.TertiaryStyle().Render(status)
		middle = ""
	}
	room := m.width - ansi.StringWidth(middle) - ansi.StringWidth(hints) - 2
	if room < 1 && m.statusText() != "" {
		// A sentence wider than the footer: it keeps every cell, rather than
		// yielding them to a one-cell stub of the server label.
		return padTo(cut(hints, m.width), m.width)
	}
	left = cut(left, max(room, 1))
	gap := m.width - ansi.StringWidth(left) - ansi.StringWidth(middle) - ansi.StringWidth(hints)
	line := left + middle + strings.Repeat(" ", max(gap, 1)) + hints
	return padTo(cut(line, m.width), m.width)
}

// helpScreen is the ? overlay: every key the screen answers, over the screen.
func (m Model) helpScreen() string {
	body := m.frame("Help", "", []string{
		"tab / shift-tab  focus the next or previous panel",
		"j / k            move the cursor in a list panel",
		"⏎                open the selected row, or settle an intake item",
		"a                answer the selected agent's request",
		"n                spawn an agent",
		"x                cancel the selected agent's turn",
		"f                finish the selected agent",
		"1 / 3            Activity or Info tab",
		"2                the Diff tab",
		"v                run the gate (Info tab)",
		"z                zoom the focused panel",
		"esc              unzoom, or clear the filter while it is open",
		"/                filter agents by name or project",
		"?                show or hide this help",
		"q / ctrl+c       quit",
	}, m.width, m.height-footerRows, true)
	lines := fitScreen(splitLines(body), m.width, m.height-footerRows)
	_, rows := m.agentLines()
	lines = append(lines, m.footer(rows))
	return strings.Join(fitScreen(lines, m.width, m.height), "\n")
}

// zoomScreen draws the focused panel over the whole screen above the footer.
func (m Model) zoomScreen() string {
	avail := m.height - footerRows
	var body string
	var rows []agentRow
	switch m.focus {
	case focusAgents:
		body, rows = m.panelAgents(m.width, avail)
	case focusNeedsYou:
		body = m.panelNeedsYou(m.width, avail)
	case focusEvents:
		body = m.panelEvents(m.width, avail)
	default:
		body = m.panelRight(m.width, avail)
	}
	if rows == nil {
		_, rows = m.agentLines()
	}
	lines := fitScreen(splitLines(body), m.width, avail)
	lines = append(lines, m.footer(rows))
	return strings.Join(fitScreen(lines, m.width, m.height), "\n")
}

// connecting is every panel's line before the first full read.
func (m Model) connecting() string {
	return m.plain("Connecting to " + m.state.Server + "…")
}

// plain mutes text while the fleet is not connected.
func (m Model) plain(s string) string {
	if !m.dimmed {
		return s
	}
	return lipgloss.NewStyle().Foreground(m.th.FGMuted).Render(s)
}

// textCell is one table cell: truncated, muted while disconnected, and padded
// to its column.
func (m Model) textCell(s string, w int, right bool) string {
	s = m.plain(cut(s, w))
	if right {
		return padLeft(s, w)
	}
	return padTo(s, w)
}

// joinSelection joins a row's segments, wrapping each in the selection band so
// an inner style's reset cannot cut the band short.
func (m Model) joinSelection(segs []string, selected bool) string {
	if !selected {
		return strings.Join(segs, "")
	}
	st := chrome.SelectionStyle()
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = st.Render(s)
	}
	return strings.Join(out, "")
}

// joinColumns lays the left column beside the right, each line padded to its
// column's width.
func joinColumns(leftLines []string, leftWidth int, rightLines []string, rightWidth int) []string {
	n := max(len(leftLines), len(rightLines))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		left, right := "", ""
		if i < len(leftLines) {
			left = leftLines[i]
		}
		if i < len(rightLines) {
			right = rightLines[i]
		}
		out = append(out, padTo(left, leftWidth)+padTo(right, rightWidth))
	}
	return out
}

// fitScreen truncates every line to w cells, pads it, and returns exactly h
// lines. The screen never overflows its terminal.
func fitScreen(lines []string, w, h int) []string {
	out := make([]string, 0, h)
	for i := 0; i < h; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		out = append(out, padTo(cut(line, w), w))
	}
	return out
}

// splitLines splits a rendered block into its rows.
func splitLines(s string) []string {
	return strings.Split(s, "\n")
}

// padTo appends spaces until s is w cells wide.
func padTo(s string, w int) string {
	if n := w - ansi.StringWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// padLeft prepends spaces until s is w cells wide.
func padLeft(s string, w int) string {
	if n := w - ansi.StringWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// cut truncates s to w cells, ending in an ellipsis when it does not fit.
func cut(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// containsID reports whether ids holds id.
func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// projectName is a project root's base name, or – when it is empty.
func projectName(root string) string {
	if root == "" {
		return "–"
	}
	return path.Base(root)
}

// modeText is the agent's access mode, or ? when the bridge cannot vouch for
// it.
func modeText(a contract.AgentStatus) string {
	if a.Access == nil {
		return "?"
	}
	return nonEmpty(a.Access.Mode)
}

// statusText is an agent's lifecycle state.
func statusText(s contract.AgentState) string {
	return nonEmpty(string(s))
}

// pendingKindText is what the agent is parked on, or – when it is not parked.
func pendingKindText(p *contract.PendingRequest) string {
	if p == nil {
		return "–"
	}
	return nonEmpty(string(p.Kind))
}

// branchText is the agent's branch and its target, e.g. "p5 → main".
func branchText(a contract.AgentStatus) string {
	if a.Branch == "" && a.TargetBranch == "" {
		return "–"
	}
	return nonEmpty(a.Branch) + " → " + nonEmpty(a.TargetBranch)
}

// workspaceText is the workspace's name and where it came from.
func workspaceText(w *contract.AgentWorkspace) string {
	if w == nil {
		return "–"
	}
	switch {
	case w.Name == "" && w.Source == "":
		return "–"
	case w.Name == "":
		return string(w.Source)
	case w.Source == "":
		return w.Name
	default:
		return w.Name + " (" + string(w.Source) + ")"
	}
}

// viewersText is a session's viewer count.
func viewersText(n int) string {
	if n == 1 {
		return "1 viewer"
	}
	return strconv.Itoa(n) + " viewers"
}

// nonEmpty is s, or an en dash when it is empty.
func nonEmpty(s string) string {
	if s == "" {
		return "–"
	}
	return s
}
