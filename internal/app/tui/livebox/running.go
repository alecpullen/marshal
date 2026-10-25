package livebox

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/progress"
	"marshal/internal/viewmodel"
)

// edge draws the running row's left edge. The glyph package has no constant
// for it: the edge is not a glyph vocabulary item, it is the running row's
// boundary.
const edge = "▌"

// idCells is the id column's width, and so the column the title starts at.
const idCells = 7

// kindStyle maps a work item's kind to its glyph and colour: subagents in
// AccentSecondary, commands in StatusInfo, watches muted, and anything else in
// the primary accent.
func kindStyle(kind string) (g string, c color.Color) {
	switch kind {
	case "agent":
		return glyph.Agent, theme.Current().AccentSecondary
	case "command":
		return glyph.Job, theme.Current().StatusInfo
	case "watch":
		return glyph.Watch, theme.Current().FGMuted
	default:
		return glyph.Running, theme.Current().AccentPrimary
	}
}

// itemID is the row's identity: a subagent's #N, else the display id, else
// the source id, cut to six cells so it never eats the title's room.
func itemID(it viewmodel.WorkItem) string {
	id := it.DisplayID
	if it.Kind == "agent" {
		id = "#" + it.SourceID
	}
	if id == "" {
		id = it.SourceID
	}
	return ansi.Truncate(id, 6, "…")
}

// rowPrefix is the edge, kind glyph and id column every work row starts with.
func rowPrefix(g string, c color.Color, id string) string {
	style := lipgloss.NewStyle().Foreground(c)
	idField := id + strings.Repeat(" ", max(idCells-ansi.StringWidth(id), 0))
	return style.Render(edge + g + " " + idField)
}

// runningRow is one live work item's row: the edge, kind glyph and id in the
// kind's colour, the title, the measurement, and the elapsed clock with the
// spinner right-aligned. It is pure — every string is cleaned and cut on a
// cell boundary, and the producer controls none of the colour or layout.
func runningRow(it viewmodel.WorkItem, in Input, w int) Row {
	w = max(w, 1)
	g, c := kindStyle(it.Kind)
	style := lipgloss.NewStyle().Foreground(c)
	prefix := rowPrefix(g, c, itemID(it))

	// The right-hand side is the elapsed clock and the turn spinner. The
	// clock stays plain so the row has one coloured anchor.
	elapsed := time.Duration(0)
	if !it.StartedAt.IsZero() {
		elapsed = max(in.Now.Sub(it.StartedAt), 0)
	}
	spinner := in.Spinner
	if spinner == "" {
		spinner = glyph.Running
	}
	right := theme.MutedStyle().Render(formatElapsed(elapsed)) + " " + style.Render(spinner)

	// The title is the item's label, else its id, cut to a share of the row so
	// the measurement keeps the rest.
	title := clean(it.Label, 120)
	if title == "" {
		title = clean(itemID(it), 60)
	}
	title = ansi.Truncate(title, min(ansi.StringWidth(title), max((w-22)*2/5, 8)), "…")

	room := w - ansi.StringWidth(prefix) - ansi.StringWidth(title) - 2 - ansi.StringWidth(right) - 1
	text := prefix + title
	if m := measure(headlined(it, in.Headlines), in.Previous[it.TaskKey], in.Now, max(room, 0)); m != "" {
		text += "  " + m
	}
	gap := max(w-ansi.StringWidth(text)-ansi.StringWidth(right), 1)
	text = text + strings.Repeat(" ", gap) + right

	return Row{
		Text:   ansi.Truncate(text, w, "…"),
		Target: Target{Kind: TargetWork, Key: it.TaskKey},
		Tint:   true,
	}
}

// heldRow is a finished item's row, held until the next prompt: the same edge,
// glyph and id as a running row, the title muted, the outcome word, and the
// completion event's summary. On the right, the duration and the outcome
// glyph. It is not tinted: the work is over.
func heldRow(h HeldItem, in Input, w int) Row {
	w = max(w, 1)
	g, c := kindStyle(h.Item.Kind)
	prefix := rowPrefix(g, c, itemID(h.Item))

	right := theme.MutedStyle().Render(formatElapsed(h.Duration)) + " " + outcomeStyle(h.Outcome).Render(outcomeGlyph(h.Outcome))

	title := clean(h.Item.Label, 120)
	if title == "" {
		title = clean(itemID(h.Item), 60)
	}
	text := prefix + muted(title)
	if h.Outcome != "" {
		text += "  " + outcomeStyle(h.Outcome).Render(h.Outcome)
	}
	if h.Summary != "" {
		text += " · " + muted(clean(h.Summary, 120))
	}
	gap := max(w-ansi.StringWidth(text)-ansi.StringWidth(right), 1)
	text = text + strings.Repeat(" ", gap) + right

	return Row{
		Text:   ansi.Truncate(text, w, "…"),
		Target: Target{Kind: TargetWork, Key: h.Item.TaskKey},
	}
}

// runRow is the plan-run row: an active SDD run or swarm, laid out as a
// running row with the run's title and summary, and the turn's clock. It is
// tinted and has no target: it is information, not a browse stop.
func runRow(r RunRow, in Input, w int) Row {
	w = max(w, 1)
	style := lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary)
	prefix := rowPrefix(glyph.Running, theme.Current().AccentPrimary, "run")

	elapsed := time.Duration(0)
	if !r.StartedAt.IsZero() {
		elapsed = max(in.Now.Sub(r.StartedAt), 0)
	}
	spinner := in.Spinner
	if spinner == "" {
		spinner = glyph.Running
	}
	right := theme.MutedStyle().Render(formatElapsed(elapsed)) + " " + style.Render(spinner)

	title := clean(r.Title, 120)
	title = ansi.Truncate(title, min(ansi.StringWidth(title), max((w-22)*2/5, 8)), "…")

	room := w - ansi.StringWidth(prefix) - ansi.StringWidth(title) - 2 - ansi.StringWidth(right) - 1
	text := prefix + title
	if s := clean(r.Summary, 200); s != "" {
		text += "  " + cut(s, max(room, 0))
	}
	gap := max(w-ansi.StringWidth(text)-ansi.StringWidth(right), 1)
	text = text + strings.Repeat(" ", gap) + right

	return Row{
		Text: ansi.Truncate(text, w, "…"),
		Tint: true,
	}
}

// outcomeStyle is the colour of a held row's outcome word and glyph: success
// for done, warning for an outcome the tray could not learn, error otherwise.
func outcomeStyle(outcome string) lipgloss.Style {
	th := theme.Current()
	switch outcome {
	case "done":
		return lipgloss.NewStyle().Foreground(th.StatusSuccess)
	case "outcome unknown":
		return lipgloss.NewStyle().Foreground(th.StatusWarning)
	}
	return lipgloss.NewStyle().Foreground(th.StatusError)
}

// outcomeGlyph is the mark a held row ends with.
func outcomeGlyph(outcome string) string {
	switch outcome {
	case "done":
		return glyph.OK
	case "outcome unknown":
		return glyph.Warning
	}
	return glyph.Error
}

// headlined folds the child agent's newest narration headline into the item's
// activity, so the measurement reads one field whether the headline came from
// the projection or the turn's live narration.
func headlined(it viewmodel.WorkItem, headlines map[string]string) viewmodel.WorkItem {
	if it.Kind != "agent" {
		return it
	}
	if h := headlines[it.TaskKey]; h != "" {
		it.Activity = h
	}
	return it
}

// measure is the running item's measurement: the one line that says how far
// the work has got, in the most honest form its producer supports. First
// match wins.
func measure(it viewmodel.WorkItem, prev *progress.Snapshot, now time.Time, room int) string {
	return measureText(it, prev, now, room, true)
}

// measureText is measure with the bar suppressible: the expanded card draws
// its own full-width bar, so its measurement line carries the text alone.
func measureText(it viewmodel.WorkItem, prev *progress.Snapshot, now time.Time, room int, withBar bool) string {
	if room <= 0 {
		return ""
	}
	switch {
	case it.State == "blocked":
		return cut(warn("⚠ needs "+promptWord(it.Prompt)), room)
	case it.Attention != "":
		return cut(warn("⚠ "+clean(it.Attention, 120)), room)
	}
	// A subagent's measurement is its narration: the newest headline, else its
	// running call.
	if it.Kind == "agent" {
		if act := clean(it.Activity, 120); act != "" {
			return cut(act, room)
		}
	}
	if p := it.Progress; p != nil {
		if s := progressText(it, p, prev, now, room, withBar); s != "" {
			return cut(s, room)
		}
	}
	if tail := lastLine(it.OutputTail); tail != "" {
		return cut(tail, room)
	}
	return cut(clean(it.Activity, 120), room)
}

// progressText renders a progress-bearing item's measurement: the go test
// JSON counts, a bar with its percentage and count, an untotalled metric, or a
// bare phase label. "" when the snapshot carries none of them.
func progressText(it viewmodel.WorkItem, p *progress.Snapshot, prev *progress.Snapshot, now time.Time, room int, withBar bool) string {
	m, ok := primaryMetric(p)
	est := progress.EstimateFor(prev, p, m.ID, now, it.State == "stopping")
	switch {
	case isGoTestJSON(it):
		return goCounts(p)
	case ok && est.Fraction != nil:
		return fractionText(it, p, m, est, now, room, withBar)
	case ok:
		return metricText(m, est)
	case p.Phase != nil:
		return phaseLabel(p.Phase)
	}
	return ""
}

// isGoTestJSON reports whether a snapshot came from the go test JSON source,
// which reports observed counts only: there is no suite denominator to invent,
// so it never renders a percentage.
func isGoTestJSON(it viewmodel.WorkItem) bool {
	return it.ProgressConfig != nil && it.ProgressConfig.Source == "go-test-json"
}

// goCounts renders the go test JSON aggregate as `N passed · N failed`, plus
// the skip count when there is one. The failed count is in the error colour
// when nonzero; a zero failure count stays plain rather than painting success
// the row did not report.
func goCounts(p *progress.Snapshot) string {
	if p == nil || len(p.Metrics) == 0 {
		return ""
	}
	passed := goMetric(p, "passed")
	failed := goMetric(p, "failed")
	skipped := goMetric(p, "skipped")
	text := number(passed) + " passed · "
	if failed > 0 {
		text += errText(number(failed) + " failed")
	} else {
		text += number(failed) + " failed"
	}
	if skipped > 0 {
		text += " · " + number(skipped) + " skipped"
	}
	return text
}

// goMetric returns one go test JSON count by its label (or its id).
func goMetric(p *progress.Snapshot, label string) float64 {
	for _, m := range p.Metrics {
		if strings.EqualFold(m.Label, label) || strings.EqualFold(m.ID, label) {
			return m.Current
		}
	}
	return 0
}

// fractionText renders a metric with a known total: the phase prefix when the
// phase has one, the bar and percentage, the count, and then the stale signal,
// the reported-done notice or the ETA and rate.
func fractionText(it viewmodel.WorkItem, p *progress.Snapshot, m progress.Metric, est progress.Estimate, now time.Time, room int, withBar bool) string {
	_, c := kindStyle(it.Kind)
	frac := *est.Fraction
	pct := fmt.Sprintf("%.0f%%", frac*100)
	text := ""
	if p.Phase != nil && p.Phase.Total > 0 {
		text = fmt.Sprintf("%d/%d %s ", p.Phase.Index, p.Phase.Total, phaseLabel(p.Phase))
	}
	if withBar {
		text += Bar(frac, BarCells, c) + " " + pct
	} else {
		text += pct
	}
	text += "  " + metricCount(m)
	switch {
	case est.Stale && p.Sequence > 0:
		// The bar holds at its last measurement, but rate and ETA hide: they
		// would describe work the producer has stopped reporting.
		text += " · " + warn("⚠ no update for "+formatElapsed(max(now.Sub(p.ObservedAt), 0)))
	case frac >= 1 && it.State == "running":
		text += " · reported done · process still running"
	default:
		if est.ETA != nil {
			text += " · ~" + formatElapsed(*est.ETA)
		}
		if est.Rate != nil && room-ansi.StringWidth(text) >= 20 {
			text += " · " + rateText(m, *est.Rate) + "/s"
		}
	}
	return text
}

// metricText renders a metric with no total: the observed count, the measured
// rate when there is one, and an honest `size unknown` for a byte metric.
func metricText(m progress.Metric, est progress.Estimate) string {
	text := metricCount(m)
	if est.Rate != nil {
		text += " · " + rateText(m, *est.Rate) + "/s"
	}
	if m.Unit == "bytes" {
		text += " · size unknown"
	}
	return text
}

// phaseLabel is the phase's label, else its id.
func phaseLabel(ph *progress.Phase) string {
	label := clean(ph.Label, 80)
	if label == "" {
		label = clean(ph.ID, 64)
	}
	return label
}

// promptWord names what a blocked item is waiting for, by the request's kind.
func promptWord(p *viewmodel.WorkPrompt) string {
	if p == nil {
		return "input"
	}
	switch p.Kind {
	case "approval":
		return "approval"
	case "question":
		return "an answer"
	case "access":
		return "access"
	case "skill":
		return "a skill decision"
	case "sudo":
		return "a password"
	}
	return "input"
}

// card is the expanded card for one running item: at most CardRows rows, the
// header row first, then the full-width bar, the measurement, the detail or
// checklist, and the actions. Every row carries the item's target, so a click
// anywhere on the card acts on the same work.
func card(it viewmodel.WorkItem, in Input, w int) []Row {
	w = max(w, 1)
	target := Target{Kind: TargetWork, Key: it.TaskKey}
	it = headlined(it, in.Headlines)
	prev := in.Previous[it.TaskKey]

	rows := []Row{runningRow(it, in, w)}
	add := func(text string) {
		if text == "" || len(rows) >= CardRows {
			return
		}
		rows = append(rows, Row{Text: ansi.Truncate(text, w, "…"), Tint: true, Target: target})
	}

	if p := it.Progress; p != nil {
		if m, ok := primaryMetric(p); ok {
			if est := progress.EstimateFor(prev, p, m.ID, in.Now, it.State == "stopping"); est.Fraction != nil {
				_, c := kindStyle(it.Kind)
				frac := *est.Fraction
				add(Bar(frac, max(w-10, 1), c) + " " + fmt.Sprintf("%.0f%%", frac*100))
			}
		}
	}
	add(measureText(it, prev, in.Now, w, false))
	add(cardDetail(it, in))
	add(muted(cardActions(it)))
	return rows
}

// cardDetail is the card's detail row: the producer's detail, else the
// checklist, else the last useful output line.
func cardDetail(it viewmodel.WorkItem, in Input) string {
	if p := it.Progress; p != nil {
		if d := clean(p.Detail, 120); d != "" {
			return d
		}
		if line := cardChecklist(p, in); line != "" {
			return line
		}
	}
	return lastLine(it.OutputTail)
}

// cardChecklist renders at most four producer rows on one line: the done
// glyph, the spinner for running work, and the ambient dot otherwise.
func cardChecklist(p *progress.Snapshot, in Input) string {
	spinner := in.Spinner
	if spinner == "" {
		spinner = glyph.Running
	}
	parts := make([]string, 0, 4)
	for _, r := range p.Rows {
		if len(parts) >= 4 {
			break
		}
		g := glyph.Ambient
		switch r.State {
		case "done":
			g = glyph.OK
		case "running":
			g = spinner
		}
		label := clean(r.Label, 80)
		if label == "" {
			label = clean(r.ID, 64)
		}
		if label == "" {
			continue
		}
		parts = append(parts, g+" "+label)
	}
	return strings.Join(parts, "  ")
}

// cardActions is the card's action line, each action only when it applies.
func cardActions(it viewmodel.WorkItem) string {
	parts := []string{"↵ collapse"}
	if it.Capabilities.Stop {
		parts = append(parts, "s stop")
	}
	if it.Kind == "agent" {
		parts = append(parts, "f open")
	}
	parts = append(parts, "/work list")
	return strings.Join(parts, " · ")
}

// cut truncates already-cleaned text to room cells.
func cut(s string, room int) string {
	if room <= 0 {
		return ""
	}
	return ansi.Truncate(s, room, "…")
}

// warn paints a measurement in the warning colour.
func warn(s string) string {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusWarning).Render(s)
}

// errText paints a count in the error colour.
func errText(s string) string {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusError).Render(s)
}

// muted paints supporting text (the card's action line).
func muted(s string) string {
	return theme.MutedStyle().Render(s)
}
