package tui

import (
	"fmt"
	"image/color"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/accesspanel"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/help"
	"marshal/internal/app/tui/theme"
	"marshal/internal/interaction"
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
// higher numbers are dropped first. A pinned segment never sheds at all.
type statusSeg struct {
	text     string
	priority int
	pinned   bool
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

// modeSegmentStyle is modeStyle, but violet while browsing so the mode change
// is visible even where the word is clipped.
func (m Model) modeSegmentStyle() lipgloss.Style {
	if m.browsing && !m.hasPendingApproval() {
		return lipgloss.NewStyle().Foreground(theme.Current().AccentSecondary).Bold(true)
	}
	return modeStyle()
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

// turnStateSegment is the left-side "what the turn is doing" segment: the
// pinned turn spinner, the word working and the elapsed time. It is empty
// unless a turn is actually running.
//
// The current tool is deliberately absent: the tray's running row already
// names it, and repeating it here made the status line churn on every tool
// call. The spinner and clock alone answer "is it stuck?".
//
// The turn's own state used to live in the box's top border; the status line
// is where a user looks for the session's live state, so the segment sits
// beside the mode cue and survives the box collapsing.
func (m Model) turnStateSegment() string {
	if !m.busy || m.turnStartedAt.IsZero() {
		return ""
	}
	elapsed := formatElapsed(max(m.now().Sub(m.turnStartedAt), 0))
	state := "working " + elapsed
	// The glyph carries the primary accent so the beat reads first; the rest
	// stays dim, because the mode cue is the only bold cue on the line apart
	// from the warning cues.
	if spinner := m.turnSpinnerFrame(); spinner != "" {
		return lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary).Render(spinner) +
			" " + dimStyle().Render(state)
	}
	return dimStyle().Render(state)
}

// identityStyle renders the "where am I" cluster — working directory, git
// branch, worktree — at normal foreground weight.
//
// The status line can carry a dozen segments. Rendering all of them dim made
// it a uniform wall of text that had to be read rather than scanned, so the
// segments are split across three weights: the mode cue is bold accent,
// identity is normal, and metrics stay dim.
func identityStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().FGDefault)
}

// statusClusters computes the status row's two label clusters at the given
// width: the left identity cluster and the right activity cluster, after the
// hint fallbacks and the segment shedding have done their work. The plain row
// and the border form share it, each passing the width its own glyphs leave.
func (m Model) statusClusters(width int) (left, right string) {
	segs := m.statusLeftSegments()
	left = joinSegs(segs)
	right = m.statusRightSegment()

	// When the terminal is narrow, drop the button-hint cluster first so
	// that project path, worktree, and other identity segments remain
	// visible. Approval/error indicators are never dropped this way.
	if right != "" && !m.hasPendingApproval() && !m.noticeVisible() && m.ctrlCArmed() == ctrlCNone {
		fits := func(s string) bool {
			return visibleRunes(left)+visibleRunes(s)+statusHorizontalPadding+statusMinGap <= width
		}
		// Shed the optional mouse hint before sacrificing the whole
		// cluster: dropping "clear queue" to keep a mouse-mode reminder is
		// the wrong trade, and the cluster is one string that goes as a unit.
		if !fits(right) {
			hints := m.footerHints()
			hints.SuppressMouseHint = true
			right = help.Footer(hints)
		}
		if !fits(right) {
			hints := m.footerHints()
			hints.SuppressMouseHint = true
			hints.SuppressBrowseHint = true
			right = help.Footer(hints)
		}
		// While a turn runs, the stop key outranks every other hint: a
		// terminal too narrow for the cluster keeps the one pair that
		// unsticks the turn, and the segment shedding below makes room for
		// it out of the left cluster.
		if !fits(right) {
			if m.busy {
				hints := m.footerHints()
				hints.StopOnly = true
				right = help.Footer(hints)
			} else {
				right = ""
			}
		}
	}

	for len(segs) > 1 && visibleRunes(left)+visibleRunes(right)+statusHorizontalPadding+statusMinGap > width {
		// Drop the lowest-priority unpinned segment (highest priority
		// number), earliest first on a tie. The mode cue and the turn's live
		// state are pinned: neither ever sheds.
		worst := -1
		for i := range segs {
			if segs[i].pinned {
				continue
			}
			if worst < 0 || segs[i].priority > segs[worst].priority {
				worst = i
			}
		}
		if worst < 0 {
			break
		}
		segs = append(segs[:worst], segs[worst+1:]...)
		left = joinSegs(segs)
	}
	return left, right
}

// renderStatusLine is the plain single row of status chrome: left cluster
// identifies the session (mode · model @ provider · locality · ctx usage),
// right cluster shows what the agent is doing right now. It is drawn plain
// while a full-frame panel is open, where the console's outline is the
// panel's own closed frame rather than the status row.
func (m Model) renderStatusLine(width int) string {
	left, right := m.statusClusters(width)
	gap := width - visibleRunes(left) - visibleRunes(right) - statusHorizontalPadding
	if gap < statusMinGap {
		gap = statusMinGap
	}
	line := " " + left + strings.Repeat(" ", gap) + right + " "
	out := statusBarStyle().Width(max(width, 1)).MaxWidth(max(width, 1)).Render(ansi.Cut(line, 0, width))
	// No tint: the status line is the console's bottom border (or a plain row
	// while a full-frame panel is open), so it is drawn as part of the outline
	// rather than as a second, empty bar below the input area.
	// statusBarStyle pads to width, so the row stays full-width without
	// PaintBand's padding.
	return out
}

// renderStatusBorder draws the status clusters as the console's bottom border:
// ╰─ <left> ─…─ <right> ─╯ in c. The clusters are computed six cells narrower
// than the row, because the corner, the two label gaps and the fill glyphs
// take those cells.
func (m Model) renderStatusBorder(width int, c color.Color) string {
	left, right := m.statusClusters(max(width-6, 1))
	return chrome.BorderLine("╰─ ", left, right, "╯", width, c)
}

// joinSegs joins status segments with the dim separator.
func joinSegs(segs []statusSeg) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		parts = append(parts, s.text)
	}
	return strings.Join(parts, dimSeparator)
}

// modeSegment returns the persistent access label for the status line, read
// from a fresh status. Callers that already took a status snapshot pass it to
// modeSegmentFor instead, so one frame never composes two different status
// values.
func (m Model) modeSegment() string {
	return modeSegmentFor(m.sess.Status().Interaction)
}

// modeSegmentFor returns the persistent access label for access. It always
// includes every active modifier, so approval, browsing, answering and
// activity cues on the right can never conceal the effective permission state.
func modeSegmentFor(access acpwire.Interaction) string {
	label := "Ask"
	if access.Mode == string(interaction.ModeAct) {
		label = "Act"
	}
	parts := []string{label}
	if access.Auto {
		parts = append(parts, "Auto")
	}
	if access.System {
		parts = append(parts, "System")
	}
	return strings.Join(parts, dimSeparator)
}

// transientStatusSegment carries "what is happening right now" cues on the
// right side. They used to replace the mode label; keeping them separate is
// what lets the persistent access state survive approvals, browsing and
// questions.
//
// There is deliberately no "edit cmd" cue: an approval edit only exists while
// its approval is still pending, and statusRightSegment reports that pending
// decision first. Replacing it with the edit cue would hide the one persistent
// reminder that a decision is outstanding.
func (m Model) transientStatusSegment() string {
	switch {
	case m.sess.Status().SDD.Active:
		return "sdd"
	case m.activeCompletionPopup() != nil:
		return "completing"
	case m.state.PendingSkillGate() != nil:
		return "skill gate"
	case m.state.PendingQuestion() != nil:
		return "answering"
	case m.browsing:
		return "browse"
	}
	return ""
}

// statusLeftSegments returns the left-side status segments with priorities.
// Priorities (lower = higher priority, kept first when collapsing):
//
//	mode=0 (pinned), untrusted=0, turn state=0 (pinned), route=1,
//	send now=1, ctx=2, quota=2, local=3, ±files=4, think=4, gen=4,
//	branch=5, dir=5, swarm tokens=6, jobs=7, queued=8, browser=9,
//	config issues=10
//
// The locality cue drops before the consumable budgets, because "ctx 42k/200k"
// is the number a user acts on while "local" only restates the route already
// shown beside it.
//
// The send-now receipt outranks the identity and metric segments: it reports
// an action the user just took against the running turn, so it must survive
// the collapse that a long route or a narrow terminal forces.
func (m Model) statusLeftSegments() []statusSeg {
	st := m.sess.Status()
	segs := []statusSeg{
		{text: m.modeSegmentStyle().Render(modeSegmentFor(st.Interaction)), priority: 0, pinned: true},
	}

	if !st.Trusted {
		segs = append(segs, statusSeg{text: untrustedStyle().Render("untrusted"), priority: 0})
	}

	// The turn's live state never sheds: it is the answer to "is it stuck?".
	if seg := m.turnStateSegment(); seg != "" {
		segs = append(segs, statusSeg{text: seg, priority: 0, pinned: true})
	}

	active := st.ActiveRoute
	route := active
	running := st.TurnRoute != nil
	turn := acpwire.Route{}
	if running {
		turn = *st.TurnRoute
		route = turn
	}
	// The runner only applies an override whose provider and model match the
	// running turn's route (agent/effort.go sessionEffortTarget); anything
	// else is dropped at the next turn's start. Rendering such an override as
	// pending would advertise a change this turn will not make.
	override := st.Effort
	overrideApplies := override.Level != "" &&
		override.Provider == route.Provider && override.Model == route.Model
	if route.Active {
		segs = append(segs, statusSeg{text: identityStyle().Render(fmt.Sprintf("%s @ %s", route.Model, route.Provider)), priority: 1})
		if route.LocalOnly {
			segs = append(segs, statusSeg{text: dimStyle().Render("local"), priority: 3})
		}
		// The active route has moved on while a turn still runs: show where
		// it will land next, without hiding the turn's own model above.
		if running && (active.Preset != turn.Preset || active.Model != turn.Model) {
			segs = append(segs, statusSeg{text: dimStyle().Render(fmt.Sprintf("→ %s (next turn)", active.Model)), priority: 4})
		}
		// The thinking-effort flag rides next to the model identity: it is
		// route-shaped config (like local/remote), not a consumable counter,
		// so it collapses before branch/dir (priority 4) rather than after.
		// It renders for every effort-capable route — "think default" when
		// the preset leaves the effort unset — so the footer always answers
		// whether thinking is configured and at what level. When the provider
		// cannot accept an effort control (e.g. native Ollama, whose think
		// toggle is a separate wire mechanism), the chat path drops the field
		// (chat.go gates on Capabilities().Reasoning) and the segment is
		// omitted rather than advertising a value that is never sent. While a
		// turn runs it shows the turn's level and, when the override retargets
		// it, the pending level and when it takes effect.
		if route.ReasoningCapable {
			effort := route.Thinking
			if effort == "" {
				effort = "default"
			}
			text := "think " + effort
			switch {
			case running && overrideApplies && override.Level != turn.Thinking:
				text = fmt.Sprintf("think %s → %s", effort, override.Level)
				if (turn.Thinking == "off") != (override.Level == "off") {
					text += " (next turn)"
				} else {
					text += " (next step)"
				}
			case !running && overrideApplies:
				text = "think " + override.Level
			}
			segs = append(segs, statusSeg{text: dimStyle().Render(text), priority: 4})
		}
	} else {
		segs = append(segs, statusSeg{text: identityStyle().Render("no model"), priority: 1})
		if !m.state.Config.Privacy.RemoteProvidersAllowed {
			segs = append(segs, statusSeg{text: dimStyle().Render("local"), priority: 3})
		}
	}

	// Live context use: tokens of the parent's latest model call vs the
	// resolved model window. Subagent usage is recorded on the child's own
	// session state (see buildSubagentFactory) and never folded in here, so
	// the number cannot inflate past the window.
	if used, window := st.TurnUsed, st.TurnWindow; window > 0 {
		segs = append(segs, statusSeg{text: dimStyle().Render(fmt.Sprintf("ctx %s/%s",
			strutil.CompactTokens(used), strutil.CompactTokens(window))), priority: 2})
	}

	// Changed-file count against the base ref, so the working tree's state
	// stays visible now that the rail is gone. The session refreshes it on
	// turn boundaries and workspace moves, never during render.
	if n := st.ChangedFiles; n > 0 {
		word := "files"
		if n == 1 {
			word = "file"
		}
		segs = append(segs, statusSeg{text: dimStyle().Render(fmt.Sprintf("±%d %s", n, word)), priority: 4})
	}

	// Subscription quota from an OAuth-backed provider. Rendered as
	// percent-used plus a reset countdown, matching the spike's finding
	// that the endpoint reports percent-of-window rather than a
	// remaining-request count. Priority 2 keeps it beside the context
	// counter: both are consumable budgets that drop together on narrow
	// terminals.
	if q := st.Quota; q != nil {
		segs = append(segs, statusSeg{
			text:     dimStyle().Render(quotaSegment(q)),
			priority: 2,
		})
	}

	// Generation > 0 means the session has compacted at least once. Show
	// it so a user who notices the agent "forgetting" can see that a
	// handoff happened rather than guessing.
	if st.GenerationSeq > 0 {
		segs = append(segs, statusSeg{
			text:     dimStyle().Render(fmt.Sprintf("gen %d", st.GenerationSeq)),
			priority: 4,
		})
	}

	if leaves := st.Branches; len(leaves) > 1 {
		cur := st.LeafID
		idx := 1
		for i, id := range leaves {
			if id == cur {
				idx = i + 1
				break
			}
		}
		segs = append(segs, statusSeg{text: fmt.Sprintf("branch %d/%d", idx, len(leaves)), priority: 5})
	}

	if wd := st.WorkingDir; wd != "" {
		segs = append(segs, statusSeg{text: identityStyle().Render(filepath.Base(wd)), priority: 5})
	}

	// Git context: branch + worktree, inserted right after dir so cwd and
	// branch read as one cluster. Branch shares priority 5 with dir (drops
	// together on narrow widths); worktree is 6 (drops first).
	if st.Git.InRepo && st.Git.Branch != "" {
		segs = append(segs, statusSeg{text: branchStyle().Render("⎇ " + st.Git.Branch), priority: 5})
	}
	if st.Git.Worktree != "" {
		segs = append(segs, statusSeg{text: worktreeStyle().Render("wt:" + st.Git.Worktree), priority: 6})
	}

	if sp := st.Swarm; sp.Active && (sp.TokensMax > 0 || sp.TokensUsed > 0) {
		segs = append(segs, statusSeg{text: fmt.Sprintf("tokens %s/%s",
			strutil.CompactTokens(sp.TokensUsed),
			strutil.CompactTokens(sp.TokensMax)), priority: 6})
	}

	if sp := st.SDD; sp.Active {
		// The now bar owns task counts and phase; the status line keeps
		// only the mode cue (modeSegment) and the token budget.
		if sp.TokensMax > 0 || sp.TokensUsed > 0 {
			var seg string
			if sp.TokensMax > 0 {
				seg = fmt.Sprintf("sdd tokens %s/%s",
					strutil.CompactTokens(sp.TokensUsed),
					strutil.CompactTokens(sp.TokensMax))
			} else {
				// MaxTotalTokens == 0 means unlimited: show used only.
				seg = fmt.Sprintf("sdd tokens %s",
					strutil.CompactTokens(sp.TokensUsed))
			}
			segs = append(segs, statusSeg{text: seg, priority: 6})
		}
	}

	if n := st.RunningJobs; n > 0 {
		segs = append(segs, statusSeg{text: fmt.Sprintf("jobs %d", n), priority: 7})
	}

	if n := len(st.Steering.Queue); n > 0 {
		segs = append(segs, statusSeg{text: warningStyle().Render(fmt.Sprintf("queued %d", n)), priority: 8})
	}

	// Send now progress. It rides the status line rather than a transcript card:
	// the action is a property of the running turn, and the transcript must keep
	// rendering truthfully while an operation is being interrupted.
	if seg, active := m.steeringStatusSegment(st.Steering.Receipt); active {
		style := dimStyle()
		if seg == "send now: undelivered" || seg == "send now: waiting for a safe boundary" {
			style = warningStyle()
		}
		segs = append(segs, statusSeg{text: style.Render(seg), priority: 1})
	}

	if bi := st.Browser; bi.SessionOpen {
		segs = append(segs, statusSeg{
			text:     browserStatusText(bi),
			priority: 9,
		})
	}

	if m.diagnosticCount > 0 {
		segs = append(segs, statusSeg{
			text:     warningStyle().Render(fmt.Sprintf(glyph.Warning+" %d config issues · /doctor", m.diagnosticCount)),
			priority: 10,
		})
	}

	return segs
}

func browserStatusText(bi acpwire.Browser) string {
	g := browserGlyphStyle().Render(glyph.Web)
	url := truncateURL(bi.URL, 20)
	if url == "" {
		url = bi.Mode
	}
	return g + " " + url
}

func (m Model) statusRightSegment() string {
	switch m.ctrlCArmed() {
	case ctrlCStop:
		return warningStyle().Render("Ctrl+C again to stop the turn")
	case ctrlCQuit:
		return warningStyle().Render("Ctrl+C again to quit")
	}
	// A typed access request queued behind another dock panel is disclosed
	// here too, so a FullFrame panel that hides the input area cannot make the
	// request invisible. It is checked before the generic approval indicator
	// because a typed request *is* the pending approval — the generic one would
	// always shadow it — and before the notice indicator, because a decision
	// waiting on the user is the more actionable state. The interrupt
	// indicator is checked above both.
	if m.accessRequestQueued() {
		return warningStyle().Render(glyph.Warning + " Access waiting")
	}
	if m.hasPendingApproval() {
		return warningStyle().Render(glyph.Warning + " approval")
	}
	if n, ok := m.state.Notice(); ok {
		if n.Severity == session.SeverityWarn {
			return warningStyle().Render(glyph.Warning + " warning")
		}
		return errorStyle().Render("✘ error")
	}
	if m.flashActive() {
		return dimStyle().Render(m.flash)
	}
	if seg := m.transientStatusSegment(); seg != "" {
		return dimStyle().Render(seg)
	}
	return help.Footer(m.footerHints())
}

// accessRequestQueued reports whether a typed access request is waiting while
// another panel holds the dock. When the accesspanel itself owns the dock its
// own footer supplies the keys, so this indicator is only for the queued case.
func (m Model) accessRequestQueued() bool {
	if _, pending := m.state.AccessRequest(); !pending {
		return false
	}
	if _, isAccessPanel := m.dock.Panel().(*accesspanel.Panel); isAccessPanel {
		return false
	}
	return m.dock.IsOpen()
}

// noticeVisible reports whether a session notice is currently up. The
// error indicator in statusRightSegment is never shed for width, so the
// hint-cluster drop logic must skip it when a notice is visible.
func (m Model) noticeVisible() bool {
	_, ok := m.state.Notice()
	return ok
}

// footerHints snapshots the mode flags the hint cluster needs. This is
// the FooterHints construction that used to live in the dedicated
// footer row (deleted in the hairline-gutter redesign).
// quotaSegment renders the footer's quota text: "⚡ 50% 4h7m" for the
// primary window, with the plan tier appended when the provider reports one.
// The secondary window is deliberately omitted here — it is a 7-day budget
// that rarely moves, and the footer is the scarcest space in the UI; the
// /agents roster shows both.
func quotaSegment(q *acpwire.Quota) string {
	if q == nil {
		return ""
	}
	seg := fmt.Sprintf("⚡ %d%%", q.PrimaryUsedPercent)
	if q.PrimaryResetAfterMs > 0 {
		seg += " " + strutil.CompactDuration(time.Duration(q.PrimaryResetAfterMs)*time.Millisecond)
	}
	if q.PlanType != "" {
		seg += " " + q.PlanType
	}
	return seg
}

func (m Model) footerHints() help.FooterHints {
	st := m.sess.Status()
	return help.FooterHints{
		Busy:                 m.busy,
		EditingCommand:       m.editingApproval(),
		ApprovalPending:      m.hasPendingApproval(),
		QuestionPending:      m.state.PendingQuestion() != nil,
		SkillGatePending:     m.state.PendingSkillGate() != nil,
		PopupOpen:            m.activeCompletionPopup() != nil,
		IdleRollbackEligible: !m.busy && st.HasBackup,
		CtrlXHint:            m.ctrlXHint(st),
		MouseReleased:        m.mouseReleased || !m.state.Config.TUI.MouseCapture,
		RunActive:            st.SubagentRunning,
		Browsing:             m.browsing,
		// The interruption hint is only advertised when the runtime can honour
		// it: a busy turn with a supported, active steering snapshot.
		SendNowAvailable: m.busy && st.Steering.State.Active && st.Steering.State.Supported,
		// A pending prompt's keys are drawn in its console header, unless a
		// full-frame panel has replaced the console entirely.
		KeysInFrame: !m.dock.FullFrameOpen(),
	}
}

// ctrlXHint turns the TUI's single Ctrl+X decision into the one hint the
// footer may render. It is none whenever a surface in front of the global
// handler owns the keyboard: the decision is about what the global handler
// would do, not a promise that the key reaches it. Browse mode stays eligible
// because handleBrowseKey lets Ctrl chords through.
func (m Model) ctrlXHint(st acpwire.SessionStatus) help.CtrlXHint {
	if m.ctrlXOwned() {
		return help.CtrlXHintNone
	}
	switch m.ctrlXAction() {
	case ctrlXStopAgent:
		return help.CtrlXHintStopAgent
	case ctrlXClearQueue:
		// Clearing an empty queue is not an action: the hint would advertise
		// a key that does nothing. The check reads the same status snapshot
		// as the rest of the hint cluster.
		if len(st.Steering.Queue) == 0 {
			return help.CtrlXHintNone
		}
		return help.CtrlXHintClearQueue
	}
	return help.CtrlXHintNone
}

// ctrlXOwned reports whether an input owner ahead of the global handler has
// Ctrl+X right now: an open dock, a pending decision or prompt (including the
// privileged-auth field), the /doctor key field, the approval command editor,
// or the completion popup.
func (m Model) ctrlXOwned() bool {
	return m.dock.IsOpen() ||
		m.hasPendingApproval() ||
		m.state.PendingQuestion() != nil ||
		m.state.PendingSkillGate() != nil ||
		m.state.PendingSudoAuth() != nil ||
		m.doctorSecret != nil ||
		m.editingApproval() ||
		m.activeCompletionPopup() != nil
}

// drilledIntoRunningSubagent reports whether the top of the view stack
// is a running subagent, which makes the Ctrl+X stop-agent hint actionable.
func (m Model) drilledIntoRunningSubagent() bool {
	if len(m.viewStack) == 0 {
		return false
	}
	return m.viewStack[len(m.viewStack)-1].Status == session.SubagentRunning
}

// ctrlXAction is the TUI's private decision about what Ctrl+X means. It is
// shared by the keyboard handler and the footer construction so the hint and
// the key agree.
type ctrlXAction uint8

const (
	ctrlXNone ctrlXAction = iota
	ctrlXStopAgent
	ctrlXClearQueue
)

// ctrlXAction decides what Ctrl+X does right now: nothing while idle, a
// targeted stop while drilled into a running subagent, and the queue clear
// otherwise. The drilled-in check reads the view stack's snapshot status; the
// snapshot can go stale, so the stop itself is attempted separately and may
// fail.
func (m Model) ctrlXAction() ctrlXAction {
	if !m.busy {
		return ctrlXNone
	}
	if m.drilledIntoRunningSubagent() {
		return ctrlXStopAgent
	}
	return ctrlXClearQueue
}
