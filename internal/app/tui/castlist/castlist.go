// Package castlist renders the pre-flight cast list shown before /sdd and
// /swarm runs. It has no role-resolution logic; it only renders rows produced
// by routing.Cast.
package castlist

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
func (p *Panel) moveCursor(dir int) {
	if len(p.rows) == 0 {
		return
	}
	// Ensure cursor starts on a role row.
	if p.rows[p.cursor].Role == "" {
		p.cursor = p.nextRoleRow(p.cursor, dir)
		return
	}
	p.cursor = p.nextRoleRow(p.cursor, dir)
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

	var rows []string

	// Metadata lines.
	for _, line := range p.meta {
		rows = append(rows, theme.MutedStyle().Render(line))
	}
	if len(p.meta) > 0 {
		rows = append(rows, "")
	}

	// Strategy row.
	stratRow := Row{
		Title:  "execution strategy",
		Detail: p.strategy,
	}
	if reason := p.disabledReason(); reason != "" {
		stratRow.Err = reason
	}
	rows = append(rows, renderRow(stratRow, inner))
	// Explain disabled options that are not currently selected.
	for _, o := range p.strategyOptions {
		if o.Value == p.strategy || o.DisabledReason == "" {
			continue
		}
		rows = append(rows, theme.MutedStyle().Render("  "+o.Value+": "+o.DisabledReason))
	}

	// Cast rows.
	for i, r := range p.rows {
		if i == p.cursor && r.Role != "" && p.pick == nil {
			r.Title = "▸ " + r.Title
		}
		rows = append(rows, renderRow(r, inner))
	}

	// Blocked indicator.
	if p.blocked() {
		rows = append(rows, "")
		rows = append(rows, errorStyle().Render("  "+glyph.Warning+" fix errors above before starting"))
	}

	listH := maxHeight - 1
	if listH < 1 {
		listH = 1
	}
	body := chrome.ClipLines(rows, 0, listH, th)

	hints := "↵ start · o override"
	if p.blocked() {
		hints = "↵ blocked"
	}
	hints += " · Esc cancel"

	ph := min(lipgloss.Height(body)+1, maxHeight)
	return chrome.PanelWithHints(p.title, hints, body, pw, ph, true, th)
}

// renderRow renders a single cast row (or the strategy row) into a string,
// including any Err/Warn continuation lines.
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
		line += "\n" + errorStyle().Render("    "+r.Err)
	}
	if r.Warn != "" {
		line += "\n" + warnStyle().Render("    "+glyph.Warning+" "+r.Warn)
	}
	return line
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
