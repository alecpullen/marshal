// Package castlist renders the pre-flight cast list shown before /sdd and
// /swarm runs. It has no role-resolution logic; it only renders rows produced
// by routing.Cast.
package castlist

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/theme"
	"marshal/internal/llm/routing"
)

// Row is one cast member to display.
type Row struct {
	Title  string
	Detail string
	Badge  string
	Err    string
	// Warn is a non-blocking caution rendered under the row: something the
	// user should see before starting, but not a reason to refuse.
	Warn string
	// Role identifies which cast role this row is, when it is one. Empty
	// for the strategy and verify-gate rows.
	Role routing.AgentRole
}

// StartMsg is emitted when the user presses Enter and no row has an error.
type StartMsg struct {
	Strategy  string
	Overrides map[routing.AgentRole]string
}

// CancelMsg is emitted when the user presses Esc.
type CancelMsg struct{}

// Panel is a dock.Panel that renders a pre-flight cast list.
type Panel struct {
	title    string
	rows     []Row
	meta     []string
	strategy string
	// strategyOptions, when non-empty, replaces the default three-strategy
	// cycle. Options with a non-empty DisabledReason are skipped when cycling
	// and block the run if selected.
	strategyOptions []StrategyOption
	cursor          int // index into rows, over role rows only
	overrides       map[routing.AgentRole]string
	// originals stores the pre-override Detail/Badge for each role so
	// clearing an override restores the row to its original state.
	originals   map[routing.AgentRole]rowOriginal
	pick        *picker.Model // non-nil when the in-panel picker is open
	pickerItems []picker.Item // model presets offered by the in-panel picker

	// offset is the first physical body line shown; bodyRows is the number of
	// content lines the last render had room for (page keys use it, and it
	// bounds paging before the next render clamps it). followSel is set by
	// moveCursor so the next View scrolls the newly selected role into the
	// window, and is consumed by that View.
	offset    int
	bodyRows  int
	followSel bool
}

// rowOriginal captures a row's Detail and Badge before an override was
// applied, so clearing the override restores them.
type rowOriginal struct {
	detail string
	badge  string
}

// StrategyOption is one selectable execution strategy with an optional
// disabled reason. A non-empty DisabledReason marks the option unavailable.
type StrategyOption struct {
	Value          string
	DisabledReason string
}

var _ dock.Panel = (*Panel)(nil)

// New creates a cast list panel with the given title, cast rows, optional
// metadata lines, and initial execution strategy. The cursor starts on the
// first role row, if any.
func New(title string, rows []Row, meta []string, strategy string) *Panel {
	p := &Panel{title: title, rows: rows, meta: meta, strategy: strategy}
	for i, r := range rows {
		if r.Role != "" {
			p.cursor = i
			break
		}
	}
	return p
}

// SetVerifyRow updates the verify-gate row's detail after a proposal fills
// in commands, clearing its warning. It is a no-op when the panel has no
// verify-gate row.
func (p *Panel) SetVerifyRow(detail string) {
	for i := len(p.rows) - 1; i >= 0; i-- {
		if p.rows[i].Title == "verify gate" {
			p.rows[i].Warn = ""
			p.rows[i].Detail = detail
			return
		}
	}
}

// VerifyGateUnknown reports whether the verify-gate row is in the unknown
// state (a warning present and no detail), meaning the offer-to-fill flow
// should be offered.
func (p *Panel) VerifyGateUnknown() bool {
	for i := len(p.rows) - 1; i >= 0; i-- {
		if p.rows[i].Title == "verify gate" {
			return p.rows[i].Warn != "" && p.rows[i].Detail == ""
		}
	}
	return false
}

// SetStrategyOptions replaces the default strategy cycle with the given
// options. A panel with no options retains the current three-strategy cycle.
func (p *Panel) SetStrategyOptions(options []StrategyOption) {
	p.strategyOptions = options
}

// SetStrategy sets the selected strategy directly.
func (p *Panel) SetStrategy(strategy string) {
	p.strategy = strategy
}

// SelectedStrategy returns the currently selected strategy.
func (p *Panel) SelectedStrategy() string {
	return p.strategy
}

// strategyList returns the effective strategy list: the configured options
// when present, otherwise the default three-strategy cycle.
func (p *Panel) strategyList() []string {
	if len(p.strategyOptions) > 0 {
		out := make([]string, 0, len(p.strategyOptions))
		for _, o := range p.strategyOptions {
			out = append(out, o.Value)
		}
		return out
	}
	return strategies
}

// disabledReason returns the disabled reason for the selected strategy, or
// "" when it is selectable.
func (p *Panel) disabledReason() string {
	for _, o := range p.strategyOptions {
		if o.Value == p.strategy && o.DisabledReason != "" {
			return o.DisabledReason
		}
	}
	return ""
}

// blocked reports whether any row has a non-empty Err, or the selected
// strategy is disabled.
func (p *Panel) blocked() bool {
	for _, r := range p.rows {
		if r.Err != "" {
			return true
		}
	}
	return p.disabledReason() != ""
}

// Update handles key events. Enter emits StartMsg when unblocked; Esc emits
// CancelMsg. Left/Right cycles the execution strategy. Up/Down moves the
// cursor over role rows; "o" opens the in-panel override picker for the
// role under the cursor.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	// When the in-panel picker is open, forward all messages to it.
	if p.pick != nil {
		switch msg := msg.(type) {
		case picker.PickedMsg:
			role := p.rows[p.cursor].Role
			if msg.Value == "" || msg.Value == "(unset — use routed model)" {
				p.setOverride(role, "")
			} else {
				p.setOverride(role, msg.Value)
			}
			p.pick = nil
			return nil
		case picker.CancelledMsg:
			p.pick = nil
			return nil
		default:
			return p.pick.Update(msg)
		}
	}

	switch k := msg.(type) {
	case tea.KeyPressMsg:
		switch k.String() {
		case "enter":
			if p.blocked() {
				return nil
			}
			return func() tea.Msg { return StartMsg{Strategy: p.strategy, Overrides: p.Overrides()} }
		case "o":
			// Open the override picker for the role under the cursor.
			if p.cursor < len(p.rows) && p.rows[p.cursor].Role != "" {
				p.openPicker(p.rows[p.cursor].Role)
			}
			return nil
		case "esc":
			return func() tea.Msg { return CancelMsg{} }
		case "right":
			p.cycleStrategy(1)
			return nil
		case "left":
			p.cycleStrategy(-1)
			return nil
		case "up":
			p.moveCursor(-1)
			return nil
		case "down":
			p.moveCursor(1)
			return nil
		case "pgup":
			p.scrollBy(-max(p.bodyRows-1, 1))
			return nil
		case "pgdown":
			p.scrollBy(max(p.bodyRows-1, 1))
			return nil
		case "home":
			p.scrollBy(-1 << 30)
			return nil
		case "end":
			p.scrollBy(1 << 30)
			return nil
		}
	}
	return nil
}

var strategies = []string{"agent", "adaptive", "strict"}

func (p *Panel) cycleStrategy(dir int) {
	list := p.strategyList()
	if len(list) == 0 {
		return
	}
	idx := 0
	for i, s := range list {
		if s == p.strategy {
			idx = i
			break
		}
	}
	// Skip disabled options when cycling.
	for n := 0; n < len(list); n++ {
		idx = (idx + dir + len(list)) % len(list)
		if p.optionDisabled(list[idx]) {
			continue
		}
		p.strategy = list[idx]
		return
	}
}

// optionDisabled reports whether the strategy value is disabled.
func (p *Panel) optionDisabled(value string) bool {
	for _, o := range p.strategyOptions {
		if o.Value == value && o.DisabledReason != "" {
			return true
		}
	}
	return false
}

// Overrides returns the per-run role→preset override map. Nil when no
// overrides have been set.
func (p *Panel) Overrides() map[routing.AgentRole]string {
	if len(p.overrides) == 0 {
		return nil
	}
	out := make(map[routing.AgentRole]string, len(p.overrides))
	for k, v := range p.overrides {
		out[k] = v
	}
	return out
}

// setOverride sets or clears an override for a role. An empty preset clears
// the entry and restores the row's original Detail/Badge.
func (p *Panel) setOverride(role routing.AgentRole, preset string) {
	if p.overrides == nil {
		p.overrides = make(map[routing.AgentRole]string)
	}
	if p.originals == nil {
		p.originals = make(map[routing.AgentRole]rowOriginal)
	}
	if preset == "" {
		delete(p.overrides, role)
	} else {
		// Save originals on first set, before overwriting.
		if _, saved := p.originals[role]; !saved {
			for _, r := range p.rows {
				if r.Role == role {
					p.originals[role] = rowOriginal{detail: r.Detail, badge: r.Badge}
					break
				}
			}
		}
		p.overrides[role] = preset
	}
	p.applyOverrideToRow(role)
}

// applyOverrideToRow updates a row's Detail and Badge to reflect the
// override, or restores the original values when the override has been
// cleared.
func (p *Panel) applyOverrideToRow(role routing.AgentRole) {
	for i, r := range p.rows {
		if r.Role == role {
			if override, ok := p.overrides[role]; ok {
				p.rows[i].Detail = override
				p.rows[i].Badge = "override"
			} else if orig, had := p.originals[role]; had {
				// Restore the original Detail/Badge.
				p.rows[i].Detail = orig.detail
				p.rows[i].Badge = orig.badge
				delete(p.originals, role)
			}
			return
		}
	}
}

// moveCursor moves the cursor by dir (+1 or -1), skipping rows with empty Role.
// It arms followSel so the next View brings the newly selected role into the
// window rather than leaving it scrolled out of sight.
func (p *Panel) moveCursor(dir int) {
	if len(p.rows) == 0 {
		return
	}
	p.cursor = p.nextRoleRow(p.cursor, dir)
	p.followSel = true
}

// scrollBy moves the body offset by delta without touching the selected role.
// The next View clamps the result against the real body height, so paging past
// either end simply settles at the end on render.
func (p *Panel) scrollBy(delta int) {
	p.followSel = false
	if delta < 0 {
		p.offset = max(p.offset+delta, 0)
		return
	}
	p.offset = min(p.offset+delta, 1<<30)
}

// nextRoleRow returns the index of the next role row in direction dir,
// wrapping. If no role rows exist, returns the current index.
func (p *Panel) nextRoleRow(from, dir int) int {
	n := len(p.rows)
	for i := 0; i < n; i++ {
		from = (from + dir + n) % n
		if p.rows[from].Role != "" {
			return from
		}
	}
	return p.cursor
}

// setRowErr sets an error message on the row matching the given role.
func (p *Panel) setRowErr(role routing.AgentRole, err string) {
	for i, r := range p.rows {
		if r.Role == role {
			p.rows[i].Err = err
			return
		}
	}
}

// SetPickerItems sets the model presets available for override selection.
// Must be called before the panel is shown so the in-panel picker can
// offer them.
func (p *Panel) SetPickerItems(items []picker.Item) {
	p.pickerItems = items
}

// openPicker opens the in-panel model picker for the given role.
func (p *Panel) openPicker(role routing.AgentRole) {
	items := make([]picker.Item, 0, len(p.pickerItems)+1)
	items = append(items, picker.Item{Label: "(unset — use routed model)", Value: ""})
	items = append(items, p.pickerItems...)
	p.pick = picker.New(
		"Override model for "+string(role),
		"↵ select · esc cancel · type provider/model for custom",
		items,
	)
	p.pick.SetAllowCustom(true)
}

// Sizing keeps the cast list docked under the default height cap.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

// View renders the cast list inside the dock height budget.
func (p *Panel) View(width, maxHeight int) string {
	// When the in-panel picker is open, render it instead of the cast list.
	if p.pick != nil {
		return p.pick.View(width, maxHeight)
	}

	th := theme.Current()

	pw := layout.PanelWidth(width)
	inner := pw - 3

	if maxHeight < 3 {
		return theme.MutedStyle().Render("Cast list")
	}

	var lines []string
	// roleFirst/roleLast record each row's first physical line and the line
	// just past its block, so the selected role's wrapped diagnostics can be
	// brought into view as a unit.
	roleFirst := make([]int, len(p.rows))
	roleLast := make([]int, len(p.rows))
	for i := range roleFirst {
		roleFirst[i] = -1
	}

	// Metadata lines.
	for _, line := range p.meta {
		lines = append(lines, theme.MutedStyle().Render(line))
	}
	if len(p.meta) > 0 {
		lines = append(lines, "")
	}

	// Strategy row.
	stratRow := Row{
		Title:  "execution strategy",
		Detail: p.strategy,
	}
	if reason := p.disabledReason(); reason != "" {
		stratRow.Err = reason
	}
	lines = appendBlock(lines, renderRow(stratRow, inner))
	// Explain disabled options that are not currently selected.
	for _, o := range p.strategyOptions {
		if o.Value == p.strategy || o.DisabledReason == "" {
			continue
		}
		lines = appendBlock(lines, mutedBlock("  "+o.Value+": "+o.DisabledReason, inner-2))
	}

	// Cast rows.
	for i, r := range p.rows {
		if i == p.cursor && r.Role != "" && p.pick == nil {
			r.Title = "▸ " + r.Title
		}
		roleFirst[i] = len(lines)
		lines = appendBlock(lines, renderRow(r, inner))
		roleLast[i] = len(lines)
	}

	// Blocked indicator.
	if p.blocked() {
		lines = append(lines, "")
		lines = append(lines, errorStyle().Render("  "+glyph.Warning+" fix errors above before starting"))
	}

	// avail is what the frame leaves for the body: the panel border row takes
	// one of maxHeight, and the body must never render past it.
	avail := max(maxHeight-1, 1)

	// Reserve the last content row for the position indicator when the body
	// overflows and the budget can spare it.
	content := avail
	showPos := false
	if len(lines) > avail && avail >= 4 {
		content = avail - 1
		showPos = true
	}

	// Clamp the offset after a resize or a data change so the body cannot
	// stay scrolled past its end. At any offset above zero the "↑ more"
	// indicator costs a row, so the offset that still shows the final line
	// sits one line short of len(lines)-content.
	p.offset = clampOffset(p.offset, len(lines), content)
	if p.followSel {
		p.offset = p.followOffset(roleFirst, roleLast, len(lines), p.offset, content)
		p.followSel = false
	}
	p.offset = clampOffset(p.offset, len(lines), content)

	var body []string
	if len(lines) > content {
		n, top, bottom := visibleCount(len(lines), p.offset, content)
		p.bodyRows = n
		if top {
			body = append(body, theme.MutedStyle().Render("  ↑ more"))
		}
		end := min(p.offset+n, len(lines))
		body = append(body, lines[p.offset:end]...)
		if bottom {
			body = append(body, theme.MutedStyle().Render("  ↓ more"))
		}
		if showPos {
			first := p.offset + 1
			body = append(body, theme.MutedStyle().Render(
				fmt.Sprintf("  %d–%d/%d", first, first+n-1, len(lines))))
		}
	} else {
		p.bodyRows = len(lines)
		body = lines
	}

	hints := "↵ start · o override"
	if p.blocked() {
		hints = "↵ blocked"
	}
	hints += " · Esc cancel"
	if len(lines) > content {
		hints += " · ⇞⇟ page"
	}

	bodyText := strings.Join(body, "\n")
	ph := min(lipgloss.Height(bodyText)+1, maxHeight)
	return chrome.PanelWithHints(p.title, hints, bodyText, pw, ph, true, th)
}

// appendBlock appends a possibly multi-line rendered block, one physical line
// per entry, so the body can be windowed and paged by line.
func appendBlock(lines []string, block string) []string {
	return append(lines, strings.Split(block, "\n")...)
}

// mutedBlock wraps text to budget cells and renders every line muted, keeping
// indent on each line. It is the muted counterpart of renderRow's Err/Warn
// continuation lines.
func mutedBlock(text string, budget int) string {
	if budget < 1 {
		budget = 1
	}
	segments := strings.Split(ansi.Wrap(text, budget, ""), "\n")
	out := make([]string, 0, len(segments))
	for _, seg := range segments {
		out = append(out, theme.MutedStyle().Render(seg))
	}
	return strings.Join(out, "\n")
}

// clampOffset bounds offset to a value that still renders within a window of
// content lines over total physical lines.
func clampOffset(offset, total, content int) int {
	if total <= 0 {
		return 0
	}
	maxOff := 0
	if total > content {
		maxOff = max(total-content+1, 0)
	}
	return max(min(offset, maxOff), 0)
}

// visibleCount reports how many physical lines fit at offset within content
// rows, and whether the ↑/↓ more indicators take a row each.
func visibleCount(total, offset, content int) (n int, top, bottom bool) {
	top = offset > 0
	n = content
	if top {
		n--
	}
	if n < 1 {
		n = 1
	}
	bottom = offset+n < total
	if bottom {
		n--
	}
	if n < 1 {
		n = 1
		bottom = offset+n < total
	}
	return n, top, bottom
}

// followOffset moves offset as little as possible so the selected role's whole
// physical block — its wrapped error and warning lines included — is inside the
// window. A block taller than the window is scrolled to its first line instead.
func (p *Panel) followOffset(first, last []int, total, offset, content int) int {
	if p.cursor < 0 || p.cursor >= len(first) || first[p.cursor] < 0 {
		return offset
	}
	start, end := first[p.cursor], last[p.cursor]

	// shows reports whether the block's first line, and then the whole block,
	// land in the rows the window at off actually renders. The "↑ more"
	// indicator costs a row but does not shift the slice, so the window always
	// starts at off.
	shows := func(off int) (firstLine, whole bool) {
		n, _, _ := visibleCount(total, off, content)
		firstLine = start >= off && start < off+n
		return firstLine, firstLine && end <= off+n
	}

	if _, whole := shows(offset); whole {
		return offset
	}
	// Scroll the shortest distance in either direction that brings the block
	// into view whole, or at least its first line.
	best, bestScore := offset, 0
	for d := 1; d <= content && bestScore < 2; d++ {
		for _, cand := range []int{offset + d, offset - d} {
			if cand < 0 || cand > total {
				continue
			}
			firstLine, whole := shows(cand)
			score := 0
			if firstLine {
				score = 1
			}
			if whole {
				score = 2
			}
			if score > bestScore {
				best, bestScore = cand, score
			}
		}
	}
	if bestScore == 0 {
		// Too far away for the short scan: align the block to the top.
		best = max(start-1, 0)
	}
	return clampOffset(best, total, content)
}

// renderRow renders a single cast row (or the strategy row) into a string,
// including any Err/Warn continuation lines. Error and warning text wraps to
// the cells left inside the row instead of being truncated: a blocking reason
// cut off mid-sentence is not a reason the user can act on.
func renderRow(r Row, inner int) string {
	detail := ""
	if r.Detail != "" {
		detail = detailStyle().Render(r.Detail)
	}
	badge := ""
	if r.Badge != "" {
		badge = badgeStyle().Render(r.Badge)
	}
	line := chrome.Row("  ", r.Title, detail, badge, inner)

	if r.Err != "" {
		line += "\n" + wrapDiag(errorStyle(), "    ", "", r.Err, inner)
	}
	if r.Warn != "" {
		line += "\n" + wrapDiag(warnStyle(), "    ", glyph.Warning+" ", r.Warn, inner)
	}
	return line
}

// wrapDiag wraps text into the cells left after indent and prefix and renders
// every segment with style. The prefix is drawn on the first line only;
// continuation lines keep the same margin so the block stays aligned.
func wrapDiag(style lipgloss.Style, indent, prefix, text string, inner int) string {
	budget := inner - ansi.StringWidth(indent) - ansi.StringWidth(prefix)
	budget = max(budget, 1)
	segments := strings.Split(ansi.Wrap(text, budget, ""), "\n")
	pad := indent + strings.Repeat(" ", ansi.StringWidth(prefix))
	out := make([]string, 0, len(segments))
	for i, seg := range segments {
		lead := pad
		if i == 0 {
			lead = indent + prefix
		}
		out = append(out, style.Render(lead+seg))
	}
	return strings.Join(out, "\n")
}

func detailStyle() lipgloss.Style {
	if theme.IsMonochrome() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().FGMuted)
}

func badgeStyle() lipgloss.Style {
	if theme.IsMonochrome() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().StatusInfo)
}

func errorStyle() lipgloss.Style {
	if theme.IsMonochrome() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().StatusError)
}

func warnStyle() lipgloss.Style {
	if theme.IsMonochrome() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().StatusWarning)
}
