package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/strutil"
	"marshal/internal/tools/native"
)

const (
	// nowBarMaxRows caps the bar, summary and overflow rows included.
	nowBarMaxRows = 4
	// nowBarCompactHeight is the frame height below which the bar collapses
	// to a single summary row: on a short terminal every row comes out of
	// the transcript.
	nowBarCompactHeight = 30
	// nowBarMaxBlocks is the widest progress-block run.
	nowBarMaxBlocks = 10
)

// nowBarInput is everything planNowBar reads. It is a value so the plan is
// a pure function and tests can drive every row-selection case directly.
type nowBarInput struct {
	SDD    session.SDDProgress
	Swarm  session.SwarmProgress
	Todos  []native.TodoItem
	Agents []session.SubagentView // running, with a child, registry order
	// AgentHeadlines is each agent's latest narration headline, aligned with
	// Agents ("" when it has not narrated yet).
	AgentHeadlines []string
	Model          string // the parent's model, so a child on it adds nothing
	Provider       string // the parent's provider, to elide it on same-provider children

	// LiveHeadline is the live step's headline and LiveToolGlyph the category
	// glyph of its running tool. Set only while the viewport is scrolled away
	// from the live step, which is when the mirror row earns its place.
	LiveHeadline  string
	LiveToolGlyph string
	Browser       session.BrowserInfo

	// JobTexts and WatchTexts are pre-rendered, one row each.
	JobTexts   []string
	WatchTexts []string

	Busy          bool
	TurnStartedAt time.Time
	Spinner       string // turnSpinnerFrame; "" in the first 200ms of a turn
	ActivityLabel string // already filtered through spinnerShowsLabel

	Now    time.Time
	Width  int
	Height int
}

// nowBarPlan is the bar's content. The renderer and the height budget both
// read it, so they cannot disagree on how many rows the bar occupies.
type nowBarPlan struct {
	rows []string // rail-prefixed, unpainted
	// agents are the agent rows shown, in row order, and agentRowStart is
	// the row index of the first one. Clicks map back through them.
	agents        []session.SubagentView
	agentRowStart int
	// showsBrowser reports whether the browser session's URL made it onto
	// the bar (it can fold into `… N more`), so the status line knows
	// whether it must carry the URL itself.
	showsBrowser bool
}

// planNowBar selects the bar's rows: one progress row (SDD, else swarm,
// else todos), else a turn row while busy; then actors (subagents, the
// browser session, jobs, watches); then `… N more` past the row budget.
func planNowBar(in nowBarInput) nowBarPlan {
	inner := max(in.Width-1, 1)

	head, headText, elapsed := nowBarHead(in)
	actors, agentIdx, browserIdx := nowBarActors(in)
	mirror := nowBarMirror(in)

	if head == "" && len(actors) == 0 && mirror == "" {
		return nowBarPlan{}
	}

	if in.Height < nowBarCompactHeight {
		row := nowBarSummary(in, headText, elapsed, inner)
		return nowBarPlan{
			rows:         []string{nowBarRail(row, inner)},
			showsBrowser: in.Browser.SessionOpen && headText == "",
		}
	}

	var plan nowBarPlan
	if mirror != "" {
		plan.rows = append(plan.rows, mirror)
	}
	if head != "" {
		plan.rows = append(plan.rows, head)
	}
	room := nowBarMaxRows - len(plan.rows)
	shown := actors
	overflow := 0
	if len(actors) > room {
		shown = actors[:max(room-1, 0)]
		overflow = len(actors) - len(shown)
	}
	plan.agentRowStart = len(plan.rows)
	for i, a := range shown {
		if agentIdx[i] != nil {
			plan.agents = append(plan.agents, *agentIdx[i])
		}
		plan.rows = append(plan.rows, a)
	}
	plan.showsBrowser = browserIdx >= 0 && browserIdx < len(shown)
	if overflow > 0 {
		plan.rows = append(plan.rows, dimStyle().Render(fmt.Sprintf("… %d more", overflow)))
	}
	for i := range plan.rows {
		plan.rows[i] = nowBarRail(plan.rows[i], inner)
	}
	return plan
}

// nowBarRail prefixes the dim rail and guarantees the row is one screen
// line. Row text embeds agent- and user-supplied strings (todo content, job
// commands, page titles); a stray newline would split one plan row across
// several screen lines and make the height budget undercount.
func nowBarRail(row string, inner int) string {
	return chromeRailWidth(oneLine(row), dimColor, inner)
}

// oneLine flattens line breaks and tabs to single spaces.
func oneLine(s string) string {
	return nowBarFlatten.Replace(s)
}

var nowBarFlatten = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")

// nowBarHead builds the progress or turn row. It returns the finished row
// (with the elapsed time right-aligned when busy), the bare text the
// compact summary reuses, and the elapsed string. All three are "" when
// there is neither progress nor a running turn.
func nowBarHead(in nowBarInput) (row, text, elapsed string) {
	inner := max(in.Width-1, 1)
	g, gc := in.Spinner, accentColor
	if g == "" {
		g = glyph.Running
	}

	// Pick the progress source first: the budget the text must fit depends
	// on the glyph, the blocks and the elapsed clock sharing its row.
	const (
		srcNone = iota
		srcSDD
		srcSDDDone
		srcSwarm
		srcTodos
	)
	src, blocks, todoText := srcNone, "", ""
	switch {
	case in.SDD.Active:
		src = srcSDD
		blocks = progressBlocks(in.SDD.DoneTasks, in.SDD.TotalTasks, nowBarMaxBlocks)
	case in.SDD.Finished:
		src = srcSDDDone
		blocks = progressBlocks(in.SDD.DoneTasks, in.SDD.TotalTasks, nowBarMaxBlocks)
	case in.Swarm.Active:
		src = srcSwarm
	case in.Busy:
		// An unfinished list is only pinned while a turn runs. Idle, a list
		// the agent abandoned would otherwise hold a row for the rest of the
		// session; Ctrl+T still shows it.
		if done, inProgress := todoProgress(in.Todos); len(in.Todos) > 0 && done < len(in.Todos) {
			src = srcTodos
			blocks = progressBlocks(done, len(in.Todos), nowBarMaxBlocks)
			todoText = fmt.Sprintf("%d/%d", done, len(in.Todos))
			if c := nowBarTodoFocus(in.Todos, inProgress); c != "" {
				todoText += " · " + c
			}
		}
	}

	if src == srcNone {
		// No progress source: the turn row, only while busy.
		if !in.Busy || in.TurnStartedAt.IsZero() {
			return "", "", ""
		}
		text = spinnerLabel(in.Spinner, nowBarElapsed(in))
		if in.ActivityLabel != "" {
			text += " · " + oneLine(in.ActivityLabel)
		}
		text = statusBusyStyle().Render(strutil.Truncate(text, max(inner-1, 1), true))
		return " " + text, text, ""
	}

	if in.Busy && !in.TurnStartedAt.IsZero() {
		elapsed = nowBarElapsed(in)
	}
	// Cells the text may use: the row minus the " g " glyph cell group, the
	// blocks and their space, and the right-aligned clock and its gap.
	budget := inner - 3
	if blocks != "" {
		budget -= ansi.StringWidth(blocks) + 1
	}
	if elapsed != "" {
		budget -= ansi.StringWidth(elapsed) + 1
	}
	budget = max(budget, 1)

	switch src {
	case srcSDD:
		text = runPanelSummaryText(in.SDD, in.Now, budget)
	case srcSDDDone:
		g, gc, text = runPanelFinishedParts(in.SDD, budget)
	case srcSwarm:
		text = statusBusyStyle().Render(ansi.Truncate(swarmStripText(in.Swarm), budget, "…"))
	case srcTodos:
		text = ansi.Truncate(todoText, budget, "…")
	}

	left := " " + lipgloss.NewStyle().Foreground(gc).Render(g) + " "
	if blocks != "" {
		left += blocks + " "
	}
	return nowBarJustify(left+text, elapsed, inner), text, elapsed
}

func nowBarElapsed(in nowBarInput) string {
	return formatElapsed(max(in.Now.Sub(in.TurnStartedAt), 0))
}

// nowBarTodoFocus is the in-progress item's content, else the first
// pending one.
func nowBarTodoFocus(todos []native.TodoItem, inProgress int) string {
	if inProgress >= 0 {
		return oneLine(todos[inProgress].Content)
	}
	for _, t := range todos {
		if t.Status != native.TodoCompleted {
			return oneLine(t.Content)
		}
	}
	return ""
}

// nowBarJustify right-aligns right within width cells, truncating left so
// right is never clipped. With no right it just fits left.
func nowBarJustify(left, right string, width int) string {
	if right == "" {
		return ansi.Truncate(left, width, "…")
	}
	rw := ansi.StringWidth(right)
	left = ansi.Truncate(left, max(width-rw-1, 1), "…")
	gap := max(width-ansi.StringWidth(left)-rw, 1)
	return left + strings.Repeat(" ", gap) + mutedStyle().Render(right)
}

// nowBarActors renders the actor rows in order — subagents, browser, jobs,
// watches — and, aligned by index, a pointer to the subagent behind each
// agent row (nil for the rest).
func nowBarActors(in nowBarInput) (rows []string, agents []*session.SubagentView, browserIdx int) {
	browserIdx = -1
	inner := max(in.Width-1, 1)
	for i := range in.Agents {
		v := &in.Agents[i]
		label := fmt.Sprintf("#%d  %s", v.ID, oneLine(v.Label))
		if i < len(in.AgentHeadlines) && in.AgentHeadlines[i] != "" {
			label += "  " + oneLine(in.AgentHeadlines[i])
		}
		// The model is only news when it differs from the parent's route.
		if v.Model != "" && (v.Model != in.Model || (v.Provider != "" && v.Provider != in.Provider)) {
			label += dimSeparator + v.Model
			if v.Provider != "" && v.Provider != in.Provider {
				label += " @ " + v.Provider
			}
		}
		line := label + dimSeparator + formatElapsed(max(in.Now.Sub(v.StartedAt), 0))
		rows = append(rows, gutterPrefix(glyph.Agent, dimColor)+
			dimStyle().Render(strutil.Truncate(line, max(inner-3, 1), true)))
		agents = append(agents, v)
	}
	if in.Browser.SessionOpen {
		browserIdx = len(rows)
		rows = append(rows, " "+browserStripText(in.Browser, in.Spinner))
		agents = append(agents, nil)
	}
	for _, t := range in.JobTexts {
		rows = append(rows, " "+t)
		agents = append(agents, nil)
	}
	for _, t := range in.WatchTexts {
		rows = append(rows, " "+t)
		agents = append(agents, nil)
	}
	return rows, agents, browserIdx
}

// nowBarMirror is the live-mirror row: while the transcript is scrolled away
// from the running step, one line says what that step is doing, with the key
// that returns to it. "" when there is nothing to mirror.
func nowBarMirror(in nowBarInput) string {
	if in.LiveHeadline == "" || in.Height < nowBarCompactHeight {
		return ""
	}
	inner := max(in.Width-1, 1)
	g := in.Spinner
	if g == "" {
		g = glyph.Running
	}
	tail := g
	if in.LiveToolGlyph != "" {
		tail += " " + in.LiveToolGlyph
	}
	left := " " + lipgloss.NewStyle().Foreground(accentColor).Render(glyph.FollowDown) + " " +
		ansi.Truncate(oneLine(in.LiveHeadline), max(inner-ansi.StringWidth(tail)-12, 8), "…") +
		dimSeparator + dimStyle().Render(tail)
	return nowBarJustify(left, "End", inner)
}

// nowBarSummary is the one-row form for short frames:
// `<progress> · ⧉2 ┆1 ○1` with the elapsed time right-aligned. Zero counts
// are omitted.
func nowBarSummary(in nowBarInput, headText, elapsed string, inner int) string {
	var counts []string
	if n := len(in.Agents); n > 0 {
		counts = append(counts, fmt.Sprintf("%s%d", glyph.Agent, n))
	}
	if n := len(in.JobTexts); n > 0 {
		counts = append(counts, fmt.Sprintf("%s%d", glyph.Job, n))
	}
	if n := len(in.WatchTexts); n > 0 {
		counts = append(counts, fmt.Sprintf("%s%d", glyph.Watch, n))
	}
	tail := strings.Join(counts, " ")
	if headText == "" && in.Browser.SessionOpen {
		headText = browserStripText(in.Browser, in.Spinner)
	}
	var left string
	switch {
	case headText != "" && tail != "":
		// Truncate the progress text, never the counts.
		avail := max(inner-1-ansi.StringWidth(tail)-ansi.StringWidth(dimSeparator)-ansi.StringWidth(elapsed)-1, 1)
		left = " " + ansi.Truncate(headText, avail, "…") + dimSeparator + dimStyle().Render(tail)
	case headText != "":
		left = " " + headText
	default:
		left = " " + dimStyle().Render(tail)
	}
	return nowBarJustify(left, elapsed, inner)
}

// progressBlocks draws done/total as ▰▰▱▱ scaled to min(total, maxCells)
// cells. It rounds rather than truncates (see formatPercent), so 3/7 is not
// shown a cell short.
func progressBlocks(done, total, maxCells int) string {
	cells := min(total, maxCells)
	if cells <= 0 {
		return ""
	}
	filled := int(math.Round(float64(done) / float64(total) * float64(cells)))
	filled = min(max(filled, 0), cells)
	return lipgloss.NewStyle().Foreground(accentColor).Render(strings.Repeat(glyph.ProgressFull, filled)) +
		theme.MutedStyle().Render(strings.Repeat(glyph.ProgressEmpty, cells-filled))
}

// renderNowBar joins the plan's rows and paints them as a full-width band.
func renderNowBar(p nowBarPlan, width int) string {
	if len(p.rows) == 0 {
		return ""
	}
	return chrome.PaintBand(strings.Join(p.rows, "\n"), width, theme.Current().ChromeBG())
}

// nowBarInput snapshots the model for planNowBar.
func (m Model) nowBarInput() nowBarInput {
	now := m.now()
	in := nowBarInput{
		SDD:           m.state.SDDProgress(),
		Swarm:         m.state.SwarmProgress(),
		Todos:         m.viewedTodos(),
		Provider:      m.state.ActiveRoute().Provider,
		Browser:       m.state.BrowserInfo(),
		Busy:          m.busy && !m.turnStartedAt.IsZero(),
		TurnStartedAt: m.turnStartedAt,
		Spinner:       m.turnSpinnerFrame(),
		Now:           now,
		Width:         max(m.leftWidth, 1),
		Height:        m.height,
	}
	in.Browser.Title = oneLine(in.Browser.Title)
	if act := m.state.Activity(); spinnerShowsLabel(act.Kind) && act.Label != "" {
		in.ActivityLabel = m.state.PinnedSpinnerLabel(act)
	}
	in.Model = m.state.ActiveRoute().Model
	for _, v := range m.state.Subagents() {
		if v.Status == session.SubagentRunning && v.Child != nil {
			in.Agents = append(in.Agents, v)
			in.AgentHeadlines = append(in.AgentHeadlines, subagentHeadline(v.Child))
		}
	}
	if !m.viewportFollow && m.busy {
		in.LiveHeadline, in.LiveToolGlyph = m.liveStepSummary()
	}
	width := in.Width
	for _, j := range m.runningJobs() {
		line := fmt.Sprintf("%s  %s  %s",
			j.ID,
			strutil.Truncate(oneLine(j.Command), max(width/2, 12), true),
			formatElapsed(max(now.Sub(j.StartedAt), 0)))
		in.JobTexts = append(in.JobTexts, dimStyle().Render(glyph.Job+" "+line))
	}
	for _, w := range m.runningWatches() {
		in.WatchTexts = append(in.WatchTexts,
			dimStyle().Render(glyph.Watch+" "+fmt.Sprintf("%s  %s  %s", oneLine(w.Name), w.Kind, w.State)))
	}
	return in
}

// nowBarPlan returns this frame's plan. viewString plans once up front and
// memoizes it on its model copy, because the height budget, the renderer and
// the status line all read the plan within one frame; outside a frame (key
// handling, clicks) it plans fresh.
func (m Model) nowBarPlan() nowBarPlan {
	if m.nowBarMemo != nil {
		return *m.nowBarMemo
	}
	return planNowBar(m.nowBarInput())
}

// nowBarRows is the bar's height for the frame budget, read from the same
// plan the renderer uses.
func (m Model) nowBarRows() int { return len(m.nowBarPlan().rows) }
