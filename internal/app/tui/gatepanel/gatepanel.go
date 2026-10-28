// Package gatepanel renders the question a plan-run subagent raised and
// collects the human's answer.
//
// It exists to replace a three-surface arrangement: the question was
// printed into the transcript, repeated as a warning row in the run panel,
// and answered through an input box that looked idle but was silently
// modal — enter resumed the run, esc killed it, and neither was advertised
// anywhere.
package gatepanel

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
)

// AnswerMsg is emitted when the user submits a non-empty answer.
type AnswerMsg struct{ Text string }

// StopMsg is emitted when the user presses Esc to abandon the run.
type StopMsg struct{}

// wrapBreakpoints mirrors tui.WrapBreakpoints. gatepanel cannot import tui
// (tui imports gatepanel), so the value is duplicated deliberately; keep the
// two in step.
const wrapBreakpoints = "/-:"

// Panel is a dock.Panel that asks the human a subagent's question.
type Panel struct {
	gate  acpwire.SDDGate
	input textfield.Model

	// offset is the first wrapped body row on screen and bodyRows is the
	// height of the scrollable body the last render had, which is how far a
	// page key moves. Both are normalized by View, so a resize or a longer
	// report can never leave the window scrolled past the text.
	offset   int
	bodyRows int
}

var _ dock.Panel = (*Panel)(nil)

// New creates a gate panel for the open gate.
func New(g acpwire.SDDGate) *Panel {
	ti := textfield.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	return &Panel{gate: g, input: ti}
}

// Update handles key events. Enter submits a non-empty answer; Esc stops
// the run. Paging belongs to the body; every other key, including the arrows
// and ordinary typing, goes to the answer field.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "enter":
			answer := strings.TrimSpace(p.input.Value())
			if answer == "" {
				return nil
			}
			return func() tea.Msg { return AnswerMsg{Text: answer} }
		case "esc":
			return func() tea.Msg { return StopMsg{} }
		case "pgup", "pgdown", "home", "end":
			p.page(k.String())
			return nil
		}
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

// page scrolls the body. The offset is clamped against the content in View, so
// overshooting either end here is safe.
func (p *Panel) page(k string) {
	switch k {
	case "pgup":
		p.offset = max(p.offset-max(p.bodyRows-1, 1), 0)
	case "pgdown":
		p.offset += max(p.bodyRows-1, 1)
	case "home":
		p.offset = 0
	case "end":
		p.offset = 1 << 30
	}
}

// Sizing keeps the gate panel under the default docked height cap.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

// View renders the question and the whole report as a bounded, scrollable body
// with the answer field pinned underneath it. The frame never exceeds
// maxHeight rows: the body gives up rows first, the input line never does.
func (p *Panel) View(width, maxHeight int) string {
	th := theme.Current()
	pw := layout.PanelWidth(width)
	inner := max(pw-3, 1)

	content := p.body(inner)

	// Rows inside the frame below its top border, and how many of them the
	// scrollable body may use once the answer field has its pinned row.
	avail := max(maxHeight-1, 0)
	scrollRows := max(avail-1, 0)

	rows := p.bodyWindow(content, scrollRows)
	if avail >= 1 {
		rows = append(rows, "  "+p.input.View())
	}

	hints := "↵ answer · esc stop the run"
	if len(content) > p.bodyRows {
		hints += " · ⇞⇟ page"
	}

	ph := min(len(rows)+1, maxHeight)
	if ph < 1 {
		ph = 1
	}
	return chrome.PanelWithHints(p.title(), hints, strings.Join(rows, "\n"), pw, ph, true, th)
}

// body wraps the question and the whole report into body rows: the question
// first, unmissable, then the report as supporting evidence. Nothing is capped
// or truncated — a gate the user cannot read is a gate they cannot answer.
func (p *Panel) body(inner int) []string {
	width := max(inner-2, 1) // every row spends two cells on its left margin
	var rows []string
	for _, line := range wrapLines(p.gate.Question, width) {
		rows = append(rows, "  "+line)
	}
	if report := strings.TrimSpace(p.gate.Report); report != "" {
		rows = append(rows, "")
		for _, line := range wrapLines(report, width) {
			rows = append(rows, mutedStyle().Render("  "+line))
		}
	}
	return rows
}

// wrapLines wraps text to width, keeping the text's own line breaks.
func wrapLines(s string, width int) []string {
	return strings.Split(ansi.Wrap(s, width, wrapBreakpoints), "\n")
}

// bodyWindow returns the body rows for the current scroll offset: a window of
// content plus the indicators that make the rest of it reachable. It returns at
// most scrollRows rows, so the pinned input row below it always fits, and it
// normalizes the stored offset to the window it just chose.
func (p *Panel) bodyWindow(content []string, scrollRows int) []string {
	total := len(content)
	if scrollRows < 1 || total < 1 {
		p.bodyRows = 0
		p.offset = 0
		return nil
	}
	if total <= scrollRows {
		p.bodyRows = total
		p.offset = 0
		return append([]string(nil), content...)
	}

	// Indicators are budgeted before content: they are what makes the rest of
	// the report reachable. The "more" line the body continues past comes
	// first, then the position line, then the second "more" line.
	budget := 0
	switch {
	case scrollRows >= 5:
		budget = 3
	case scrollRows >= 3:
		budget = 2
	case scrollRows >= 2:
		budget = 1
	}
	height := max(scrollRows-budget, 1)
	if budget >= 3 && p.offset == 0 {
		// Nothing is scrolled away above, so that row belongs to content.
		height++
	}
	start := min(max(p.offset, 0), max(total-height, 0))
	top := start > 0
	bottom := start+height < total

	used := height + boolInt(top) + boolInt(bottom)
	pos := budget >= 2 && used+1 <= scrollRows
	if pos {
		used++
	}
	// At the narrowest heights indicators are trimmed from the top of that
	// priority list down: the input row and at least one content row outrank
	// them, and the frame hint still advertises the paging they cannot.
	if used > scrollRows && pos {
		pos = false
		used--
	}
	if used > scrollRows && top {
		top = false
		used--
	}
	if used > scrollRows && bottom {
		bottom = false
	}

	p.bodyRows = height
	p.offset = start

	rows := make([]string, 0, used)
	if top {
		rows = append(rows, mutedStyle().Render("  ↑ more"))
	}
	rows = append(rows, content[start:start+height]...)
	if bottom {
		rows = append(rows, mutedStyle().Render("  ↓ more"))
	}
	if pos {
		rows = append(rows, mutedStyle().Render(positionLine(start+1, start+height, total)))
	}
	return rows
}

// positionLine labels the visible slice of the body: first–last/total.
func positionLine(from, last, total int) string {
	return fmt.Sprintf("  %d–%d/%d", from, last, total)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// title names the asking task so the question has a subject.
func (p *Panel) title() string {
	if p.gate.TaskTitle != "" {
		return fmt.Sprintf("Task %d needs an answer — %s", p.gate.TaskN, p.gate.TaskTitle)
	}
	return fmt.Sprintf("Task %d needs an answer", p.gate.TaskN)
}

func mutedStyle() lipgloss.Style { return theme.MutedStyle() }
