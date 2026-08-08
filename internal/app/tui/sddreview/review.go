// Package sddreview renders the candidate plan review dock panel shown after
// /sdd new finishes authoring. It lets the user accept the candidate (which
// leaves the artifact in place for a later /sdd) or discard it. It never
// starts a pipeline runner itself.
package sddreview

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
	"marshal/internal/sddauthor"
)

// AcceptMsg is emitted when the user accepts the reviewed candidate.
type AcceptMsg struct{}

// CancelMsg is emitted when the user discards the candidate.
type CancelMsg struct{}

// Panel is a dock.Panel that reviews one authored plan candidate.
type Panel struct {
	result sddauthor.Result
}

var _ dock.Panel = (*Panel)(nil)

// New returns a review panel for the given authoring result.
func New(result sddauthor.Result) *Panel {
	return &Panel{result: result}
}

// CandidatePath returns the authored plan artifact path, for discard.
func (p *Panel) CandidatePath() string {
	return p.result.Request.PlanPath
}

// Sizing keeps the review panel docked under the default height cap.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

// hasErrors reports whether the candidate inspection has error diagnostics.
func (p *Panel) hasErrors() bool {
	return p.result.Inspection == nil || p.result.Inspection.HasErrors()
}

// Update handles Enter (accept when clean) and Esc (discard).
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	switch k := msg.(type) {
	case tea.KeyPressMsg:
		switch k.String() {
		case "enter":
			if p.hasErrors() {
				return nil
			}
			return func() tea.Msg { return AcceptMsg{} }
		case "esc":
			return func() tea.Msg { return CancelMsg{} }
		}
	}
	return nil
}

// View renders the candidate path, task count, deterministic/fallback ops,
// estimated calls, and diagnostics, clipped to the dock height.
func (p *Panel) View(width, maxHeight int) string {
	th := theme.Current()
	pw := layout.PanelWidth(width)

	if maxHeight < 3 {
		return mutedStyle().Render("Review plan")
	}

	var lines []string
	insp := p.result.Inspection
	if insp == nil {
		lines = append(lines, errorStyle().Render("  no inspection available for this candidate"))
	} else {
		lines = append(lines, mutedStyle().Render("candidate: "+insp.Plan.Path))
		lines = append(lines, mutedStyle().Render(fmt.Sprintf(
			"%d task(s) · %d deterministic ops · %d fallback ops · est. %d model calls",
			len(insp.IR.Tasks), insp.Report.Total.DetOps, insp.Report.Total.AgentOps, insp.Report.Total.EstCalls)))
		lines = append(lines, mutedStyle().Render("strategy: "+string(insp.EffectiveStrategy)))
		if len(insp.Diagnostics) > 0 {
			lines = append(lines, "")
			for _, d := range insp.Diagnostics {
				line := fmt.Sprintf("  task %d: %s", d.TaskN, d.Message)
				if d.Severity == "error" {
					lines = append(lines, errorStyle().Render(line))
				} else {
					lines = append(lines, warnStyle().Render(line))
				}
			}
		}
	}

	listH := maxHeight - 1
	if listH < 1 {
		listH = 1
	}
	body := chrome.ClipLines(lines, 0, listH, th)

	hints := "enter accept · esc discard"
	if p.hasErrors() {
		hints = "esc discard · edit the plan, then run /sdd"
	}
	ph := min(lipgloss.Height(body)+1, maxHeight)
	return chrome.PanelWithHints("Review plan", hints, body, pw, ph, true, th)
}

func isMono() bool {
	_, ok := theme.Current().FGDefault.(lipgloss.NoColor)
	return ok
}

func mutedStyle() lipgloss.Style {
	if isMono() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().FGMuted)
}

func errorStyle() lipgloss.Style {
	if isMono() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().StatusError)
}

func warnStyle() lipgloss.Style {
	if isMono() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().StatusWarning)
}

var _ = strings.TrimSpace
