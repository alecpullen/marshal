package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
)

const (
	// statusHorizontalPadding is the leading and trailing single-space pad
	// rendered around the status line content.
	statusHorizontalPadding = 2
	// statusMinGap is the smallest number of spaces kept between the left
	// and right clusters when the terminal is too narrow to show everything.
	statusMinGap = 1
)

// renderStatusLine is the single row of persistent chrome below the input:
// left cluster identifies the session (mode · model @ provider · locality
// · ctx usage), right cluster shows what the agent is doing right now.
func (m Model) renderStatusLine(width int) string {
	left := strings.Join(m.statusLeftSegments(), dimSeparator)
	right := m.statusRightSegment()

	gap := width - visibleRunes(left) - visibleRunes(right) - statusHorizontalPadding
	if gap < statusMinGap {
		// Not enough room: prioritise the activity cluster. The truncation
		// budget reserves both padding cells plus the minimum inter-cluster gap.
		left = truncateRunes(left, max(width-visibleRunes(right)-statusHorizontalPadding-statusMinGap, 0))
		gap = max(width-visibleRunes(left)-visibleRunes(right)-statusHorizontalPadding, statusMinGap)
	}
	line := " " + left + strings.Repeat(" ", gap) + right + " "
	return statusBarStyle.Width(max(width, 1)).MaxWidth(max(width, 1)).Render(ansi.Cut(line, 0, width))
}

// modeSegment returns the current mode label for the status line.
// It prioritises transient UI modes that answer "what will Esc do?",
// falling back to the persistent mode (/ask, /edit, /auto).
func (m Model) modeSegment() string {
	if m.helpOpen {
		return "help open"
	}
	if m.editingCommand {
		return "edit cmd"
	}
	if m.activeCompletionPopup() != nil {
		return "completing"
	}
	mode := m.forceMode
	if mode == "" {
		mode = "auto"
	}
	return mode
}

func (m Model) statusLeftSegments() []string {
	segments := []string{m.modeSegment()}

	if !m.state.Trusted() {
		segments = append(segments, "untrusted")
	}

	route := m.state.ActiveRoute()
	if route.Active {
		segments = append(segments, fmt.Sprintf("%s @ %s", route.Model, route.Provider))
		if route.LocalOnly {
			segments = append(segments, "local")
		}
	} else {
		segments = append(segments, "no model")
		if !m.state.Config.Privacy.RemoteProvidersAllowed {
			segments = append(segments, "local")
		}
	}

	if pack := m.state.ContextPack(); !pack.IsEmpty() {
		segments = append(segments, fmt.Sprintf("ctx %s/%s",
			compactTokenCount(pack.TokenUsage.EstimatedTokens),
			compactTokenCount(pack.TokenUsage.MaxTokens)))
	}

	if used, window := m.state.TurnUsage(); window > 0 {
		segments = append(segments, fmt.Sprintf("turn %s/%s",
			compactTokenCount(used), compactTokenCount(window)))
	}

	if leaves := m.state.Branches(); len(leaves) > 1 {
		cur := m.state.LeafID()
		idx := 1
		for i, id := range leaves {
			if id == cur {
				idx = i + 1
				break
			}
		}
		segments = append(segments, fmt.Sprintf("branch %d/%d", idx, len(leaves)))
	}

	if sp := m.state.SwarmProgress(); sp.Active && (sp.TokensMax > 0 || sp.TokensUsed > 0) {
		segments = append(segments, fmt.Sprintf("tokens %s/%s",
			compactTokenCount(sp.TokensUsed),
			compactTokenCount(sp.TokensMax)))
	}

	if n := m.jobCount; m.jobBroker != nil {
		if n > 0 {
			segments = append(segments, fmt.Sprintf("jobs %d", n))
		}
	} else if n := m.state.RunningJobsCount(); n > 0 {
		segments = append(segments, fmt.Sprintf("jobs %d", n))
	}

	if n := m.queuedCount; n > 0 {
		segments = append(segments, statusWarnStyle.Render(fmt.Sprintf("queued %d", n)))
	}
	return segments
}

var (
	// Foreground-only styles. No background is applied at either render site
	// (the status strip and the in-transcript completed-tool line); both draw
	// on the terminal's default background.
	statusWarnStyle = lipgloss.NewStyle().Foreground(warningColor).Bold(true)
	statusErrStyle  = lipgloss.NewStyle().Foreground(errorColor).Bold(true)
	statusOkStyle   = lipgloss.NewStyle().Foreground(successColor)
	statusBusyStyle = lipgloss.NewStyle().Foreground(accentColor)
)

func (m Model) statusRightSegment() string {
	// Pending approvals are surfaced by the early return above the activity
	// switch, so the ActivityKind case below only needs to handle active work.
	if m.state.PendingApproval() != nil {
		return statusWarnStyle.Render("⚠ approval")
	}
	activity := m.state.Activity()
	spinner := m.activeSpinnerFrame(activity.Kind)
	switch activity.Kind {
	case session.ActivityThinking:
		return statusBusyStyle.Render(spinnerLabel(spinner, "thinking"))
	case session.ActivityTool:
		elapsed := m.now().Sub(activity.StartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		label := spinnerLabel(spinner, fmt.Sprintf("%s · %s", activity.Label, formatElapsed(elapsed)))
		if b := m.state.ToolBudget(); b.Max > 0 {
			label = fmt.Sprintf("%s · tools %d/%d", label, b.Used, b.Max)
		}
		return statusBusyStyle.Render(label)
	}
	if m.state.ProviderError() != nil {
		return statusErrStyle.Render("✘ error")
	}
	if m.lastActivityLabel != "" && m.now().Sub(m.lastActivityDone) < doneDisplayDuration {
		return statusOkStyle.Render("✔ " + m.lastActivityLabel)
	}
	return ""
}
