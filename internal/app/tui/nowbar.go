package tui

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/progress"
	"marshal/internal/strutil"
	"marshal/internal/tools/native"
	"marshal/internal/viewmodel"
)

const (
	// nowBarMaxRows caps the bar, summary and overflow rows included. The
	// band's own budget is one progress row, one checklist row and four
	// entity rows (design §4), plus the live-mirror row when the reader has
	// scrolled away. On a tall frame the checklist may take the entity rows
	// no card is using.
	nowBarMaxRows = 6
	// nowBarCompactHeight is the frame height below which the bar collapses
	// to a single summary row: on a short terminal every row comes out of
	// the transcript.
	nowBarCompactHeight = 30
	// nowBarTallHeight is the frame height from which the checklist opens
	// into one row per waiting item.
	nowBarTallHeight = 44
	// nowBarMaxBlocks is the widest progress-block run.
	nowBarMaxBlocks = 10
	// nowBarWorkMaxRows caps the live-work card plan. The head, checklist and
	// browser summary share it with the aggregate attention row, the compact
	// receipt summary, the live cards and the overflow row.
	nowBarWorkMaxRows = 10
	// nowBarWorkCardRows is a collapsed live card's height. An explicit
	// expansion opens one card to at most nowBarWorkCardExpandedRows; while it
	// is open every other card folds to a single row.
	nowBarWorkCardRows         = 2
	nowBarWorkCardExpandedRows = 6
)

// nowBarInput is everything planNowBar reads. It is a value so the plan is
// a pure function and tests can drive every row-selection case directly.
type nowBarInput struct {
	SDD   session.SDDProgress
	Swarm session.SwarmProgress
	// Band is the pinned band: todo progress with its stale signal, and the
	// running cards. viewmodel.BandOf builds it, so the TUI and the web
	// cannot disagree about the same session.
	Band   viewmodel.BandInfo
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

	// Work is the immutable live-work projection. A non-empty snapshot puts
	// the bar in live-work mode: the bounded work cards replace the legacy
	// owner rows and the plan reads no agent, job or watch owner.
	Work viewmodel.WorkSnapshot
	// WorkSelected names the item the reader picked, ordered first;
	// WorkExpanded names the single card the reader opened.
	WorkSelected string
	WorkExpanded string
	// WorkPrevious holds each item's previous progress observation, keyed by
	// TaskKey, so a card can derive a measured rate and ETA.
	WorkPrevious map[string]*progress.Snapshot

	Now    time.Time
	Width  int
	Height int
}

// workAction is the typed action a work hit carries. It is a renderer-side
// vocabulary only: the plan proposes an action, it never performs one, so a
// producer can never smuggle behaviour in through a label.
type workAction uint8

const (
	workActionNone workAction = iota
	workActionExpand
	workActionCollapse
	workActionStop
	workActionList
)

// workHit pairs a rendered work row with the item it names and the action a
// click performs. planWorkRows emits it beside the rows, from the same
// result, so the hit map can never drift from what was drawn.
type workHit struct {
	TaskKey string
	Action  workAction
	Row     int // index into nowBarPlan.rows
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
	// workHits are the typed click targets of the plan's work rows, in row
	// order. It is nil outside live-work mode.
	workHits []workHit
}

// planNowBar selects the bar's rows: one progress row (SDD, else swarm,
// else todos), else a turn row while busy; then actors (subagents, the
// browser session, jobs, watches); then `… N more` past the row budget.
func planNowBar(in nowBarInput) nowBarPlan {
	// A live-work snapshot owns the bar's work rows. It is read-only: the
	// planner below never touches the agent, job or watch owners, so the two
	// surfaces cannot disagree about the same command.
	if len(in.Work.Items) > 0 {
		return planWorkRows(in, workRowBudget(in))
	}

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
	plan.rows = append(plan.rows, nowBarChecklistRows(in, nowBarMaxRows-len(plan.rows)-len(actors))...)
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

// workRowBudget is planWorkRows' row budget. A short frame collapses to one
// summary row. Otherwise it is min(nowBarWorkMaxRows, height/4), further
// reduced by the fixed chrome so the transcript floor survives and the cards
// can never push the composer off the frame.
func workRowBudget(in nowBarInput) int {
	if in.Height < nowBarCompactHeight {
		return 1
	}
	budget := min(nowBarWorkMaxRows, in.Height/4)
	budget = min(budget, in.Height-minTranscriptRows-statusLineRows-1)
	return max(budget, 1)
}

// planWorkRows selects the live-work bar's rows and their click targets. It is
// the single planner for both: the rows and the workHits come from the same
// result, so the hit map can never name a row that was not drawn. budget is
// workRowBudget(in).
func planWorkRows(in nowBarInput, budget int) nowBarPlan {
	inner := max(in.Width-1, 1)
	if budget <= 1 {
		return nowBarPlan{rows: []string{nowBarRail(workSummaryRow(in, inner), inner)}}
	}

	// Every summary row counts inside the same budget: the head, the todo
	// checklist and the browser summary are placed first, exactly as on the
	// legacy bar, so the cards only ever take what is left.
	var plan nowBarPlan
	if m := nowBarMirror(in); m != "" {
		plan.rows = append(plan.rows, m)
	}
	head, headText, _ := nowBarHead(in)
	if head != "" {
		plan.rows = append(plan.rows, head)
	}
	if line := nowBarChecklist(in); line != "" && len(plan.rows) < budget {
		plan.rows = append(plan.rows, line)
	}
	if in.Browser.SessionOpen && headText == "" && len(plan.rows) < budget {
		plan.rows = append(plan.rows, " "+browserStripText(in.Browser, in.Spinner))
		plan.showsBrowser = true
	}

	live, attention, success, stopped := partitionWork(in.Work.Items, in.Now)
	orderWorkLive(live, in.WorkSelected)

	remaining := budget - len(plan.rows)
	if remaining < 1 {
		return railWorkPlan(plan, inner)
	}

	// One aggregate attention row and one compact receipt summary. A single
	// remaining row combines both rather than dropping one.
	receipts := success + stopped
	if remaining == 1 && (attention > 0 || receipts > 0) {
		plan.rows = append(plan.rows, workCombinedSummaryRow(attention, success, stopped, inner))
	} else {
		if attention > 0 {
			plan.rows = append(plan.rows, workAttentionRow(attention, inner))
		}
		if receipts > 0 {
			plan.rows = append(plan.rows, workReceiptRow(success, stopped, inner))
		}
	}

	avail := budget - len(plan.rows)
	if avail < 1 {
		return railWorkPlan(plan, inner)
	}

	expanded := in.WorkExpanded
	if !workExpandedLive(live, expanded) {
		// The expanded item settled or disappeared: clear the expansion
		// rather than opening a card that is no longer here.
		expanded = ""
	}

	need := make([]int, len(live))
	for i, it := range live {
		need[i] = workCardMinRows(it, expanded)
	}
	place := 0
	if sumInts(need) <= avail {
		place = len(live)
	} else {
		// One row is reserved for the overflow notice.
		used := 0
		for i := range live {
			if used+need[i]+1 > avail {
				break
			}
			used += need[i]
			place++
		}
	}

	for i := 0; i < place; i++ {
		it := live[i]
		action := workActionExpand
		if expanded == it.TaskKey {
			action = workActionCollapse
		} else if it.TaskKey == in.WorkSelected {
			action = workActionExpand
		}
		rows := workCardRows(it, in.WorkPrevious[it.TaskKey], in.Now, inner, need[i])
		start := len(plan.rows)
		plan.rows = append(plan.rows, rows...)
		for r := start; r < len(plan.rows); r++ {
			plan.workHits = append(plan.workHits, workHit{TaskKey: it.TaskKey, Action: action, Row: r})
		}
	}

	if hidden := len(live) - place; hidden > 0 {
		plan.rows = append(plan.rows, dimStyle().Render(fmt.Sprintf("… %d more live", hidden)))
		plan.workHits = append(plan.workHits, workHit{Action: workActionList, Row: len(plan.rows) - 1})
	}
	return railWorkPlan(plan, inner)
}

// railWorkPlan prefixes the rail on every planned row, exactly as the legacy
// plan does at the end.
func railWorkPlan(plan nowBarPlan, inner int) nowBarPlan {
	for i := range plan.rows {
		plan.rows[i] = nowBarRail(plan.rows[i], inner)
	}
	return plan
}

// workSummaryRow is the one-row live-work form for short frames: the progress
// or turn text with the live, attention and settled counts, the stale tag kept
// on the right exactly as the legacy summary does.
func workSummaryRow(in nowBarInput, inner int) string {
	_, headText, elapsed := nowBarHead(in)
	live, attention, success, stopped := partitionWork(in.Work.Items, in.Now)

	var counts []string
	if n := len(live); n > 0 {
		counts = append(counts, fmt.Sprintf("%s%d", glyph.Running, n))
	}
	if attention > 0 {
		counts = append(counts, fmt.Sprintf("%s%d", glyph.Warning, attention))
	}
	if n := success + stopped; n > 0 {
		counts = append(counts, fmt.Sprintf("%s%d", glyph.OK, n))
	}
	tail := strings.Join(counts, " ")
	right := nowBarRight(in, elapsed)
	var left string
	switch {
	case headText != "" && tail != "":
		avail := max(inner-1-ansi.StringWidth(tail)-ansi.StringWidth(dimSeparator)-ansi.StringWidth(right)-1, 1)
		left = " " + ansi.Truncate(headText, avail, "…") + dimSeparator + dimStyle().Render(tail)
	case headText != "":
		left = " " + headText
	case tail != "":
		left = " " + dimStyle().Render(tail)
	case in.Browser.SessionOpen:
		left = " " + browserStripText(in.Browser, in.Spinner)
	default:
		left = " " + dimStyle().Render("live work")
	}
	return nowBarJustifyStyled(left, right, inner)
}

// partitionWork splits a snapshot into its live cards and its aggregate
// settled counts. Active membership is viewmodel.WorkActive; a settled item
// that needs attention is counted, never carded, and a settled success or stop
// is a receipt only while its window is live.
func partitionWork(items []viewmodel.WorkItem, now time.Time) (live []viewmodel.WorkItem, attention, success, stopped int) {
	live = make([]viewmodel.WorkItem, 0, len(items))
	for _, it := range items {
		if viewmodel.WorkActive(it) {
			live = append(live, it)
			continue
		}
		if viewmodel.WorkNeedsAttention(it) {
			attention++
			continue
		}
		s, st := viewmodel.WorkReceipts(it, now)
		if s {
			success++
		}
		if st {
			stopped++
		}
	}
	return live, attention, success, stopped
}

// orderWorkLive orders the cards: the selected item first, blocked work next,
// then a stable StartedAt/TaskKey order so an unrelated progress update cannot
// reshuffle the bar.
func orderWorkLive(live []viewmodel.WorkItem, selected string) {
	sort.SliceStable(live, func(i, j int) bool {
		a, b := live[i], live[j]
		if as, bs := a.TaskKey == selected && selected != "", b.TaskKey == selected && selected != ""; as != bs {
			return as
		}
		if ab, bb := a.State == "blocked", b.State == "blocked"; ab != bb {
			return ab
		}
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.Before(b.StartedAt)
		}
		return a.TaskKey < b.TaskKey
	})
}

// workExpandedLive reports whether key still names an active card, so a stale
// expansion is dropped instead of opening nothing.
func workExpandedLive(live []viewmodel.WorkItem, key string) bool {
	if key == "" {
		return false
	}
	for _, it := range live {
		if it.TaskKey == key {
			return true
		}
	}
	return false
}

// workCardMinRows is the rows a card is planned to take: two collapsed, up to
// six while it is the one expanded card, and a single row for every other card
// while an expansion is open.
func workCardMinRows(item viewmodel.WorkItem, expanded string) int {
	if expanded == "" {
		return nowBarWorkCardRows
	}
	if item.TaskKey == expanded {
		return nowBarWorkCardExpandedRows
	}
	return 1
}

func sumInts(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}

// workAttentionRow is the one aggregate row for settled work that needs
// attention, so a burst of failures cannot take a card each.
func workAttentionRow(n, width int) string {
	text := fmt.Sprintf("  %s %d need attention", glyph.Warning, n)
	return warningStyle().Render(ansi.Truncate(text, width, "…"))
}

// workReceiptRow is the compact success/stopped receipt summary.
func workReceiptRow(success, stopped, width int) string {
	parts := make([]string, 0, 2)
	if success > 0 {
		parts = append(parts, fmt.Sprintf("%s %d done", glyph.OK, success))
	}
	if stopped > 0 {
		parts = append(parts, fmt.Sprintf("%s %d stopped", glyph.Ambient, stopped))
	}
	return dimStyle().Render(ansi.Truncate("  "+strings.Join(parts, dimSeparator), width, "…"))
}

// workCombinedSummaryRow is the single row a one-row budget allows, carrying
// both the settled and the attention counts.
func workCombinedSummaryRow(attention, success, stopped, width int) string {
	parts := make([]string, 0, 2)
	if n := success + stopped; n > 0 {
		parts = append(parts, fmt.Sprintf("%s %d settled", glyph.OK, n))
	}
	if attention > 0 {
		parts = append(parts, fmt.Sprintf("%s %d attention", glyph.Warning, attention))
	}
	return dimStyle().Render(ansi.Truncate("  "+strings.Join(parts, dimSeparator), width, "…"))
}

// workCardRows renders one live work card: a header (kind/title, owner, actual
// lifecycle and elapsed) and up to maxRows-1 detail rows. It is pure — the
// producer controls none of the colour, layout or actions, and every string is
// sanitized and cut on a cell boundary. previous is the same item's prior
// progress observation, used for a measured rate and approximate ETA; now is
// the injected clock; width is the cell budget including the rail gutter.
func workCardRows(item viewmodel.WorkItem, previous *progress.Snapshot, now time.Time, width, maxRows int) []string {
	if maxRows < 1 {
		return nil
	}
	content := max(width-2, 1)

	rows := make([]string, 0, maxRows)
	rows = append(rows, workHeader(item, now, content))
	if maxRows == 1 {
		return rows
	}
	for _, d := range workDetailRows(item, previous, now, content) {
		if len(rows) >= maxRows {
			break
		}
		rows = append(rows, d)
	}
	// A collapsed card is two rows: when the item has nothing to report, the
	// second row names where the work came from rather than leaving a bare
	// title.
	if len(rows) == 1 {
		detail := workFallbackDetail(item)
		if detail == "" {
			detail = "no detail yet"
		}
		rows = append(rows, dimStyle().Render(ansi.Truncate("  "+detail, content, "…")))
	}
	return rows
}

// workFallbackDetail is the second row of a card that has no progress,
// activity or output yet: the reference the item points at.
func workFallbackDetail(item viewmodel.WorkItem) string {
	switch {
	case item.Reference.ChildSessionID != "":
		return "session " + workClean(item.Reference.ChildSessionID, 40)
	case item.Origin.ToolCallID != "":
		return "tool " + workClean(item.Origin.ToolCallID, 40)
	case item.Reference.NodeKey != "":
		return workClean(item.Reference.NodeKey, 60)
	case item.DisplayID != "":
		return workClean(item.DisplayID, 60)
	case item.SourceID != "":
		return workClean(item.SourceID, 60)
	}
	return ""
}

// workHeader is the card's first row: the state glyph, the kind and title, the
// owner, and the actual lifecycle with elapsed time and exit code.
func workHeader(item viewmodel.WorkItem, now time.Time, width int) string {
	g, style := workStateGlyph(item.State)
	title := workTitle(item)
	left := " " + style.Render(g) + " " + title
	if owner := workClean(item.Owner, 60); owner != "" {
		left += dimSeparator + dimStyle().Render(owner)
	}
	return nowBarJustify(left, workHeaderRight(item, now), width)
}

// workHeaderRight is the actual lifecycle, elapsed time and exit code. It is
// plain text: the header's colour comes only from the state glyph.
func workHeaderRight(item viewmodel.WorkItem, now time.Time) string {
	state := item.State
	if state == "" {
		state = "work"
	}
	parts := []string{state}
	switch state {
	case "running", "stopping":
		if !item.StartedAt.IsZero() {
			parts = append(parts, formatElapsed(max(now.Sub(item.StartedAt), 0)))
		}
	default:
		if !item.StartedAt.IsZero() && !item.EndedAt.IsZero() {
			parts = append(parts, formatElapsed(max(item.EndedAt.Sub(item.StartedAt), 0)))
		}
	}
	if item.ExitCode != nil {
		// The actual exit is reported as observed; a reported failure with a
		// zero exit stays a zero exit and surfaces as attention instead.
		parts = append(parts, fmt.Sprintf("exit %d", *item.ExitCode))
	}
	return strings.Join(parts, dimSeparator)
}

// workTitle is the card's kind and title, falling back to the display or
// source identity so a card never renders blank.
func workTitle(item viewmodel.WorkItem) string {
	title := workClean(item.Label, 120)
	if title == "" {
		title = workClean(item.DisplayID, 60)
	}
	if title == "" {
		title = workClean(item.SourceID, 60)
	}
	kind := workClean(item.Kind, 24)
	if kind == "" {
		kind = "work"
	}
	if title == "" {
		return dimStyle().Render(kind)
	}
	return kind + " " + title
}

// workStateGlyph maps a lifecycle state to the one glyph that carries it. A
// state glyph always beats a category glyph.
func workStateGlyph(state string) (string, lipgloss.Style) {
	switch state {
	case "running", "stopping":
		return glyph.Running, lipgloss.NewStyle().Foreground(accentColor)
	case "watching":
		return glyph.Watch, dimStyle()
	case "blocked":
		return glyph.Question, lipgloss.NewStyle().Foreground(violetColor)
	case "completed":
		return glyph.OK, statusOkStyle()
	case "failed", "unknown":
		return glyph.Error, errorStyle()
	default:
		return glyph.Ambient, dimStyle()
	}
}

// workDetailRows is the card's body. It leads with attention, then the
// profile-selected progress, then activity and a bounded output tail, in that
// order of usefulness.
func workDetailRows(item viewmodel.WorkItem, previous *progress.Snapshot, now time.Time, width int) []string {
	var rows []string

	if a := workClean(item.Attention, 120); a != "" {
		rows = append(rows, warningStyle().Render(ansi.Truncate("  "+glyph.Warning+" "+a, width, "…")))
	}
	rows = append(rows, workProgressLines(item, previous, now, width)...)
	if act := workClean(item.Activity, 120); act != "" {
		rows = append(rows, dimStyle().Render(ansi.Truncate("  "+act, width, "…")))
	}
	if tail := lastWorkLine(item.OutputTail); tail != "" {
		rows = append(rows, mutedStyle().Render(ansi.Truncate("  "+tail, width, "…")))
	}
	return rows
}

// workProgressLines renders the primary metric or phase, any partial/stale/
// outdated warning and the bounded checklist.
func workProgressLines(item viewmodel.WorkItem, previous *progress.Snapshot, now time.Time, width int) []string {
	p := item.Progress
	if p == nil {
		return nil
	}
	var rows []string
	var est progress.Estimate
	if metric, ok := workPrimaryMetric(p); ok {
		est = progress.EstimateFor(previous, p, metric.ID, now, item.State == "stopping")
	}
	if item.ProgressConfig != nil && item.ProgressConfig.Source == "go-test-json" {
		// Go test JSON reports observed counts only: there is no suite
		// denominator to invent, so no percentage is rendered.
		if line := workGoCounts(p, width); line != "" {
			rows = append(rows, line)
		}
	} else if line := workMetricLine(item, p, est, width); line != "" {
		rows = append(rows, line)
	}
	if warn := workProgressWarning(p, est); warn != "" {
		rows = append(rows, warningStyle().Render(ansi.Truncate("  "+glyph.Warning+" "+warn, width, "…")))
	}
	rows = append(rows, workChecklist(p, width)...)
	return rows
}

// workMetricLine is the profile-selected primary metric with its phase-local
// label, measured rate and approximate ETA. An unknown total omits the percent
// and the ETA rather than inventing either.
func workMetricLine(item viewmodel.WorkItem, p *progress.Snapshot, est progress.Estimate, width int) string {
	phase := ""
	if p.Phase != nil {
		phase = workClean(p.Phase.Label, 80)
		if phase == "" {
			phase = workClean(p.Phase.ID, 64)
		}
	}
	metric, ok := workPrimaryMetric(p)
	if !ok {
		if phase == "" {
			return ""
		}
		// No metric: the phase-local label is the whole detail row.
		return dimStyle().Render(ansi.Truncate("  "+phase, width, "…"))
	}
	label := workClean(metric.Label, 80)
	if label == "" {
		label = workClean(metric.ID, 64)
	}

	parts := make([]string, 0, 5)
	if phase != "" {
		parts = append(parts, phase)
	}
	parts = append(parts, workMetricCount(metric))
	if est.Fraction != nil && metric.Total != nil {
		parts = append(parts, fmt.Sprintf("%.0f%%", *est.Fraction*100))
	}
	if est.Rate != nil {
		parts = append(parts, workRateText(metric, *est.Rate)+"/s")
	}
	if est.ETA != nil {
		parts = append(parts, "~"+formatElapsed(*est.ETA))
	}
	text := "  " + label + "  " + strings.Join(parts, dimSeparator)
	if est.Fraction != nil && *est.Fraction >= 1 && (item.State == "running" || item.State == "stopping") {
		text += dimSeparator + "reported work complete · process still running"
	}
	return dimStyle().Render(ansi.Truncate(text, width, "…"))
}

// workMetricCount is current/total in the metric's own unit, an observed count
// when the producer gave no total, and IEC bytes for a byte metric.
func workMetricCount(m progress.Metric) string {
	if m.Unit == "bytes" {
		if m.Total != nil {
			return workBytesIEC(m.Current) + "/" + workBytesIEC(*m.Total)
		}
		return workBytesIEC(m.Current)
	}
	if m.Total != nil {
		return fmt.Sprintf("%s/%s %s", workNumber(m.Current), workNumber(*m.Total), m.Unit)
	}
	return fmt.Sprintf("%s %s", workNumber(m.Current), m.Unit)
}

// workNumber renders a producer count without inventing a decimal it did not
// report.
func workNumber(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// workGoCounts renders the go test JSON aggregate as observed counts, never as
// a fraction of a denominator the producer never supplied.
func workGoCounts(p *progress.Snapshot, width int) string {
	if p == nil || len(p.Metrics) == 0 {
		return ""
	}
	parts := make([]string, 0, len(p.Metrics))
	for _, m := range p.Metrics {
		label := workClean(m.Label, 40)
		if label == "" {
			label = workClean(m.ID, 40)
		}
		parts = append(parts, fmt.Sprintf("%s %s", workNumber(m.Current), label))
	}
	return dimStyle().Render(ansi.Truncate("  "+strings.Join(parts, dimSeparator), width, "…"))
}

// workProgressWarning is the partial/stale/outdated signal. A producer warning
// wins; otherwise a partial snapshot, a stale measurement or a reported error
// is named.
func workProgressWarning(p *progress.Snapshot, est progress.Estimate) string {
	if p == nil {
		return ""
	}
	if p.Warning != "" {
		return workClean(p.Warning, 120)
	}
	if p.Partial {
		return "partial output"
	}
	if est.Stale && p.Sequence > 0 {
		return "stale measurement"
	}
	if p.ReportedError {
		return "reported error"
	}
	return ""
}

// workChecklist is the bounded checklist of producer rows, at most three.
func workChecklist(p *progress.Snapshot, width int) []string {
	if p == nil || len(p.Rows) == 0 {
		return nil
	}
	const maxWorkChecklistRows = 3
	rows := make([]string, 0, maxWorkChecklistRows)
	for i, r := range p.Rows {
		if i >= maxWorkChecklistRows {
			break
		}
		g, style := workChecklistGlyph(r.State)
		label := workClean(r.Label, 80)
		if label == "" {
			label = workClean(r.ID, 64)
		}
		rows = append(rows, style.Render(ansi.Truncate("  "+g+" "+label, width, "…")))
	}
	return rows
}

func workChecklistGlyph(state string) (string, lipgloss.Style) {
	switch state {
	case "done":
		return glyph.OK, statusOkStyle()
	case "error":
		return glyph.Error, errorStyle()
	case "running":
		return glyph.Running, lipgloss.NewStyle().Foreground(accentColor)
	default:
		return glyph.Ambient, dimStyle()
	}
}

// workPrimaryMetric resolves the profile-selected primary metric, falling back
// to the first observation and then to nothing.
func workPrimaryMetric(p *progress.Snapshot) (progress.Metric, bool) {
	if p == nil {
		return progress.Metric{}, false
	}
	if p.PrimaryMetricID != "" {
		for _, m := range p.Metrics {
			if m.ID == p.PrimaryMetricID {
				return m, true
			}
		}
	}
	if len(p.Metrics) > 0 {
		return p.Metrics[0], true
	}
	return progress.Metric{}, false
}

// workRateText renders a measured rate in the metric's own unit.
func workRateText(m progress.Metric, rate float64) string {
	if m.Unit == "bytes" {
		return workBytesIEC(rate)
	}
	return workNumber(rate) + " " + m.Unit
}

// workBytesIEC renders a byte count with IEC binary prefixes (KiB, MiB, …).
func workBytesIEC(v float64) string {
	const base = 1024
	if v < base {
		return fmt.Sprintf("%.0f B", v)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	value, i := v, -1
	for value >= base && i < len(units)-1 {
		value /= base
		i++
	}
	return fmt.Sprintf("%.1f %s", value, units[i])
}

// workClean is the display boundary for every producer string on a card. It
// strips escapes (so no producer can control colour or layout) and secrets,
// and folds control runes before anything is measured or rendered.
func workClean(s string, limit int) string {
	return progress.Clean(s, limit, false)
}

// lastWorkLine returns the last non-empty line of a bounded output tail, with
// control runes removed, so a card never renders several rows from one field.
func lastWorkLine(tail string) string {
	lines := strings.Split(tail, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := workClean(lines[i], 200); s != "" {
			return s
		}
	}
	return ""
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
		if in.Band.Todos {
			src = srcTodos
			blocks = progressBlocks(in.Band.Done, in.Band.Total, nowBarMaxBlocks)
			todoText = fmt.Sprintf("%d/%d", in.Band.Done, in.Band.Total)
			if c := oneLine(in.Band.Active); c != "" {
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
	// While a todo is in progress the clock is that task's, not the turn's.
	if src == srcTodos {
		for _, t := range in.Todos {
			// A todo carried over from an earlier turn would open this turn
			// with hours on the clock; only one started in this turn counts.
			if t.Status == "in_progress" && !t.StartedAt.IsZero() && !t.StartedAt.Before(in.TurnStartedAt) {
				elapsed = formatElapsed(max(in.Now.Sub(t.StartedAt), 0))
				break
			}
		}
	}
	right := nowBarRight(in, elapsed)
	// Cells the text may use: the row minus the " g " glyph cell group, the
	// blocks and their space, and the right-aligned clock and its gap.
	budget := inner - 3
	if blocks != "" {
		budget -= ansi.StringWidth(blocks) + 1
	}
	if right != "" {
		budget -= ansi.StringWidth(right) + 1
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
	return nowBarJustifyStyled(left+text, right, inner), text, elapsed
}

// nowBarRight is the painted right-hand side of the head row, and of the
// compact summary row that replaces it: the stale tag ahead of the clock. An
// unmaintained list says so because it is the one signal that the plan the
// agent is following has drifted from the work, so a short frame that drops
// the checklist row must still carry it. `elapsed` itself stays plain.
func nowBarRight(in nowBarInput, elapsed string) string {
	right := elapsed
	if right != "" {
		right = mutedStyle().Render(right)
	}
	if in.Band.Stale {
		tag := warningStyle().Render("stale " + formatElapsed(in.Band.StaleAge))
		if elapsed != "" {
			right = tag + dimSeparator + right
		} else {
			right = tag
		}
	}
	return right
}

func nowBarElapsed(in nowBarInput) string {
	return formatElapsed(max(in.Now.Sub(in.TurnStartedAt), 0))
}

// nowBarChecklist is the band's second line: the collapsed done count and the
// waiting items, on one line, with `+N` for the ones that did not fit.
//
// "" unless a running turn is working from a list. Idle, an abandoned list
// would otherwise hold a row for the rest of the session; Ctrl+T still shows
// the full list.
func nowBarChecklist(in nowBarInput) string {
	if !in.Band.Todos || !in.Busy {
		return ""
	}
	inner := max(in.Width-1, 1)
	head := "  " + glyph.OK + " " + fmt.Sprintf("%d done", in.Band.Done)
	if len(in.Band.Next) == 0 {
		return head
	}
	prefix := head + dimSeparator + glyph.Running + " next: "
	// Fit the waiting items one at a time; whatever does not fit becomes a
	// +N count, which is only drawn when it fits beside the items shown — a
	// clipped count is worse than no count.
	shown := 1
	for i := 2; i <= len(in.Band.Next); i++ {
		cand := strings.Join(in.Band.Next[:i], dimSeparator)
		if ansi.StringWidth(prefix)+ansi.StringWidth(cand) > inner {
			break
		}
		shown = i
	}
	text := prefix + ansi.Truncate(strings.Join(in.Band.Next[:shown], dimSeparator),
		max(inner-ansi.StringWidth(prefix), 1), "…")
	if rest := len(in.Band.Next) - shown; rest > 0 {
		if tail := fmt.Sprintf("  +%d", rest); ansi.StringWidth(text)+ansi.StringWidth(tail) <= inner {
			text += dimStyle().Render(tail)
		}
	}
	return text
}

// nowBarChecklistRows is the band's checklist. On a tall frame with spare
// rows it is one row per waiting item, the last counting any that did not
// fit; otherwise it is nowBarChecklist's single line. spare is the rows the
// bar has left once the actors are placed: the cards keep priority, so the
// list folds back to one line as cards start and reopens as they finish.
func nowBarChecklistRows(in nowBarInput, spare int) []string {
	line := nowBarChecklist(in)
	if line == "" {
		return nil
	}
	next := in.Band.Next
	// Opening only pays when it shows at least two items: with less room the
	// summary line, which packs several, says more.
	if in.Height < nowBarTallHeight || len(next) < 2 || spare < min(len(next), 3) {
		return []string{line}
	}
	inner := max(in.Width-1, 1)
	n := min(len(next), spare)
	rows := make([]string, 0, n)
	for i, item := range next[:n] {
		if i == n-1 && n < len(next) {
			rows = append(rows, dimStyle().Render(fmt.Sprintf("  … +%d more", len(next)-i)))
			break
		}
		rows = append(rows, mutedStyle().Render("  "+glyph.Ambient+" "+ansi.Truncate(oneLine(item), max(inner-4, 1), "…")))
	}
	return rows
}

// nowBarJustify right-aligns right within width cells, truncating left so
// right is never clipped. With no right it just fits left.
func nowBarJustify(left, right string, width int) string {
	if right == "" {
		return ansi.Truncate(left, width, "…")
	}
	return nowBarJustifyStyled(left, mutedStyle().Render(right), width)
}

// nowBarJustifyStyled is nowBarJustify for a right side that is already
// painted, so the caller can colour part of it (the stale tag) without the
// muted wrapper flattening it. Widths ignore SGR, so the layout is the same.
func nowBarJustifyStyled(left, right string, width int) string {
	if right == "" {
		return ansi.Truncate(left, width, "…")
	}
	rw := ansi.StringWidth(right)
	left = ansi.Truncate(left, max(width-rw-1, 1), "…")
	gap := max(width-ansi.StringWidth(left)-rw, 1)
	return left + strings.Repeat(" ", gap) + right
}

// nowBarActors renders the actor rows in order — subagents, browser, jobs,
// watches — and, aligned by index, a pointer to the subagent behind each
// agent row (nil for the rest).
func nowBarActors(in nowBarInput) (rows []string, agents []*session.SubagentView, browserIdx int) {
	browserIdx = -1
	inner := max(in.Width-1, 1)
	for i := range in.Agents {
		v := &in.Agents[i]
		// The label is the model's own description of the child, and the
		// headline is its narration: both are scrubbed before they reach a
		// terminal. These rows read the session directly rather than through
		// the viewmodel, so this is the only scrub they get.
		label := fmt.Sprintf("#%d  %s", v.ID, oneLine(viewmodel.ScrubText(v.Label)))
		if i < len(in.AgentHeadlines) && in.AgentHeadlines[i] != "" {
			label += "  " + oneLine(viewmodel.ScrubText(in.AgentHeadlines[i]))
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
	// A short frame has no checklist row, but the stale tag still rides on
	// the right-hand side, as on the head row it replaces.
	right := nowBarRight(in, elapsed)
	var left string
	switch {
	case headText != "" && tail != "":
		// Truncate the progress text, never the counts.
		avail := max(inner-1-ansi.StringWidth(tail)-ansi.StringWidth(dimSeparator)-ansi.StringWidth(right)-1, 1)
		left = " " + ansi.Truncate(headText, avail, "…") + dimSeparator + dimStyle().Render(tail)
	case headText != "":
		left = " " + headText
	default:
		left = " " + dimStyle().Render(tail)
	}
	return nowBarJustifyStyled(left, right, inner)
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
		Band:          m.band,
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
	if !m.viewportFollow && m.busy {
		in.LiveHeadline, in.LiveToolGlyph = m.liveStepSummary()
	}
	// The live-work projection owns the work rows. When it is present the bar
	// reads only its immutable snapshot and never the local agent, job or
	// watch owners, so one command cannot appear twice under two lifecycles.
	if !m.workLaneActive() {
		for _, v := range m.state.Subagents() {
			if v.Status == session.SubagentRunning && v.Child != nil {
				in.Agents = append(in.Agents, v)
				in.AgentHeadlines = append(in.AgentHeadlines, subagentHeadline(v.Child))
			}
		}
		width := in.Width
		for _, j := range m.runningJobs() {
			line := fmt.Sprintf("%s  %s  %s",
				j.ID,
				strutil.Truncate(oneLine(viewmodel.ScrubText(j.Command)), max(width/2, 12), true),
				formatElapsed(max(now.Sub(j.StartedAt), 0)))
			in.JobTexts = append(in.JobTexts, dimStyle().Render(glyph.Job+" "+line))
		}
		for _, w := range m.runningWatches() {
			in.WatchTexts = append(in.WatchTexts,
				dimStyle().Render(glyph.Watch+" "+fmt.Sprintf("%s  %s  %s", oneLine(w.Name), w.Kind, w.State)))
		}
	}
	in.Work = m.workSnapshot
	in.WorkSelected = m.workExpanded
	in.WorkExpanded = m.workExpanded
	in.WorkPrevious = m.workPrevious
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
