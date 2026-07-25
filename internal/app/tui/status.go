package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/help"
	"marshal/internal/app/tui/theme"
	"marshal/internal/strutil"
)

const (
	// statusHorizontalPadding is the leading and trailing single-space pad
	// rendered around the status line content.
	statusHorizontalPadding = 2
	// statusMinGap is the smallest number of spaces kept between the left
	// and right clusters when the terminal is too narrow to show everything.
	statusMinGap = 1
)

// statusSeg is a single left-side status segment with a priority value.
// Lower priority numbers are kept first when the status line is too narrow;
// higher numbers are dropped first.
type statusSeg struct {
	text     string
	priority int
}

// branchStyle colors the git branch segment (accent.secondary / violet).
func branchStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentSecondary)
}

// worktreeStyle colors the worktree segment (accent.tertiary / gold).
func worktreeStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentTertiary)
}

// modeStyle colors the leftmost mode cue (accent.primary / coral, bold).
func modeStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary).Bold(true)
}

// untrustedStyle colors the untrusted warning segment.
func untrustedStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusWarning).Bold(true)
}

// dimStyle renders secondary metadata dim (fg.muted) so the colored
// segments above stand out.
func dimStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().FGMuted)
}

// renderStatusLine is the single row of persistent chrome below the input:
// left cluster identifies the session (mode · model @ provider · locality
// · ctx usage), right cluster shows what the agent is doing right now.
func (m Model) renderStatusLine(width int) string {
	segs := m.statusLeftSegments()
	left := joinSegs(segs)
	right := m.statusRightSegment()
	for len(segs) > 1 && visibleRunes(left)+visibleRunes(right)+statusHorizontalPadding+statusMinGap > width {
		// drop the lowest-priority segment (highest priority number), but
		// always preserve the first segment (the mode cue).
		worst := 1
		for i := 2; i < len(segs); i++ {
			if segs[i].priority > segs[worst].priority {
				worst = i
			}
		}
		segs = append(segs[:worst], segs[worst+1:]...)
		left = joinSegs(segs)
	}
	gap := width - visibleRunes(left) - visibleRunes(right) - statusHorizontalPadding
	if gap < statusMinGap {
		gap = statusMinGap
	}
	line := " " + left + strings.Repeat(" ", gap) + right + " "
	return statusBarStyle().Width(max(width, 1)).MaxWidth(max(width, 1)).Render(ansi.Cut(line, 0, width))
}

// joinSegs joins status segments with the dim separator.
func joinSegs(segs []statusSeg) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		parts = append(parts, s.text)
	}
	return strings.Join(parts, dimSeparator)
}

// modeSegment returns the current mode label for the status line.
// It prioritises transient UI modes that answer "what will Esc do?",
// falling back to the persistent mode (/ask, /edit, /auto).
func (m Model) modeSegment() string {
	if m.state.SDDProgress().Active {
		return "sdd"
	}
	if m.editingCommand {
		return "edit cmd"
	}
	if m.activeCompletionPopup() != nil {
		return "completing"
	}
	if m.state.PendingApproval() != nil {
		return "approval"
	}
	if m.state.PendingQuestion() != nil {
		return "answering"
	}
	mode := string(m.approvalMode)
	if mode == "" {
		mode = "default"
	}
	return mode
}

// statusLeftSegments returns the left-side status segments with priorities.
// Priorities (lower = higher priority, kept first when collapsing):
//
//	mode=0, untrusted=0, route=1, local=2, ctx=3, turn=4, branch=5, dir=5,
//	swarm tokens=6, jobs=7, queued=8
func (m Model) statusLeftSegments() []statusSeg {
	segs := []statusSeg{
		{text: modeStyle().Render(m.modeSegment()), priority: 0},
	}

	if !m.state.Trusted() {
		segs = append(segs, statusSeg{text: untrustedStyle().Render("untrusted"), priority: 0})
	}

	route := m.state.ActiveRoute()
	if route.Active {
		segs = append(segs, statusSeg{text: dimStyle().Render(fmt.Sprintf("%s @ %s", route.Model, route.Provider)), priority: 1})
		if route.LocalOnly {
			segs = append(segs, statusSeg{text: dimStyle().Render("local"), priority: 2})
		}
	} else {
		segs = append(segs, statusSeg{text: dimStyle().Render("no model"), priority: 1})
		if !m.state.Config.Privacy.RemoteProvidersAllowed {
			segs = append(segs, statusSeg{text: dimStyle().Render("local"), priority: 2})
		}
	}

	if pack := m.state.ContextPack(); !pack.IsEmpty() {
		segs = append(segs, statusSeg{text: dimStyle().Render(fmt.Sprintf("ctx %s/%s",
			strutil.CompactTokens(pack.TokenUsage.EstimatedTokens),
			strutil.CompactTokens(pack.TokenUsage.MaxTokens))), priority: 3})
	}

	if used, window := m.state.TurnUsage(); window > 0 {
		segs = append(segs, statusSeg{text: dimStyle().Render(fmt.Sprintf("turn %s/%s",
			strutil.CompactTokens(used), strutil.CompactTokens(window))), priority: 4})
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
		segs = append(segs, statusSeg{text: fmt.Sprintf("branch %d/%d", idx, len(leaves)), priority: 5})
	}

	if wd := m.state.WorkingDir; wd != "" {
		segs = append(segs, statusSeg{text: dimStyle().Render(filepath.Base(wd)), priority: 5})
	}

	// Git context: branch + worktree, inserted right after dir so cwd and
	// branch read as one cluster. Branch shares priority 5 with dir (drops
	// together on narrow widths); worktree is 6 (drops first).
	if m.gitInfo.InRepo && m.gitInfo.Branch != "" {
		segs = append(segs, statusSeg{text: branchStyle().Render("⎇ " + m.gitInfo.Branch), priority: 5})
	}
	if m.gitInfo.Worktree != "" {
		segs = append(segs, statusSeg{text: worktreeStyle().Render("wt:" + m.gitInfo.Worktree), priority: 6})
	}

	if sp := m.state.SwarmProgress(); sp.Active && (sp.TokensMax > 0 || sp.TokensUsed > 0) {
		segs = append(segs, statusSeg{text: fmt.Sprintf("tokens %s/%s",
			strutil.CompactTokens(sp.TokensUsed),
			strutil.CompactTokens(sp.TokensMax)), priority: 6})
	}

	if sp := m.state.SDDProgress(); sp.Active {
		segs = append(segs, statusSeg{text: fmt.Sprintf("task %d/%d", sp.DoneTasks, sp.TotalTasks), priority: 1})
		if sp.TokensMax > 0 || sp.TokensUsed > 0 {
			segs = append(segs, statusSeg{text: fmt.Sprintf("sdd tokens %s/%s",
				strutil.CompactTokens(sp.TokensUsed),
				strutil.CompactTokens(sp.TokensMax)), priority: 6})
		}
	}

	if n := m.jobCount; m.jobBroker != nil {
		if n > 0 {
			segs = append(segs, statusSeg{text: fmt.Sprintf("jobs %d", n), priority: 7})
		}
	} else if n := m.state.RunningJobsCount(); n > 0 {
		segs = append(segs, statusSeg{text: fmt.Sprintf("jobs %d", n), priority: 7})
	}

	if n := m.queuedCount; n > 0 {
		segs = append(segs, statusSeg{text: warningStyle().Render(fmt.Sprintf("queued %d", n)), priority: 8})
	}

	if m.ShouldShowStatusURL() {
		if bi := m.state.BrowserInfo(); bi.SessionOpen {
			segs = append(segs, statusSeg{
				text:     browserStatusText(bi),
				priority: 9,
			})
		}
	}
	return segs
}

func browserStatusText(bi session.BrowserInfo) string {
	glyph := browserGlyphStyle().Render("🌐")
	url := truncateURL(bi.URL, 20)
	if url == "" {
		url = bi.Mode
	}
	return glyph + " " + url
}

func (m Model) statusRightSegment() string {
	// Pending approvals are surfaced by the early return above the activity
	// switch, so the ActivityKind case below only needs to handle active work.
	if m.state.PendingApproval() != nil {
		return warningStyle().Render("⚠ approval")
	}
	activity := m.state.Activity()
	spinner := m.activeSpinnerFrame(activity.Kind)
	switch activity.Kind {
	case session.ActivityThinking:
		return statusBusyStyle().Render(spinnerLabel(spinner, "thinking"))
	case session.ActivityTool:
		elapsed := m.now().Sub(activity.StartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		label := spinnerLabel(spinner, fmt.Sprintf("%s · %s", activity.Label, formatElapsed(elapsed)))
		if b := m.state.ToolBudget(); b.Max > 0 {
			label = fmt.Sprintf("%s · tools %d/%d", label, b.Used, b.Max)
		}
		return statusBusyStyle().Render(label)
	}
	if m.state.ProviderError() != nil {
		return errorStyle().Render("✘ error")
	}
	if m.lastActivityLabel != "" && m.now().Sub(m.lastActivityDone) < doneDisplayDuration {
		return statusOkStyle().Render("✔ " + m.lastActivityLabel)
	}
	return help.Footer(m.footerHints())
}

// footerHints snapshots the mode flags the hint cluster needs. This is
// the FooterHints construction that used to live in the dedicated
// footer row (deleted in the hairline-gutter redesign).
func (m Model) footerHints() help.FooterHints {
	return help.FooterHints{
		Busy:                 m.busy,
		EditingCommand:       m.editingCommand,
		ApprovalPending:      m.state.PendingApproval() != nil,
		QuestionPending:      m.state.PendingQuestion() != nil,
		PopupOpen:            m.activeCompletionPopup() != nil,
		IdleRollbackEligible: !m.busy && m.state.HasBackup(),
		QueueNonEmpty:        m.queuedCount > 0 || len(m.state.SteeringQueue()) > 0,
		TodosActive:          len(m.state.Todos()) > 0,
	}
}
