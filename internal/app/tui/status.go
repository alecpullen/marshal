package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
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

// renderStatusLine is the single row of persistent chrome below the input:
// left cluster identifies the session (mode · model @ provider · locality
// · ctx usage), right cluster shows what the agent is doing right now.
func (m Model) renderStatusLine(width int) string {
	segs := m.statusLeftSegments()

	var right string
	if indicator := m.statusRightSegment(); indicator != "" {
		// An approval, warning/error, or transient toast replaces the hint
		// cluster and is never shed: it is the reason the row is interesting.
		// The left chrome yields to make room, then the indicator itself is
		// cut only if the row still cannot hold it.
		right = indicator
		for len(segs) > 1 && visibleRunes(joinSegs(segs))+visibleRunes(right)+statusHorizontalPadding+statusMinGap > width {
			idx := worstStatusSeg(segs)
			// Stop when nothing is droppable. The mode cue and the untrusted
			// warning are both priority 0, so a narrow row showing an
			// indicator can reach a state where no segment may go — and
			// dropStatusSeg(segs, -1) is a deliberate no-op, so retrying
			// would spin forever on an unchanged row. The final ansi.Cut
			// below clips what is left.
			if idx < 0 {
				break
			}
			segs = dropStatusSeg(segs, idx)
		}
		right = ansi.Cut(right, 0, max(width-visibleRunes(joinSegs(segs))-statusHorizontalPadding-statusMinGap, 1))
	} else {
		hints := help.FooterParts(m.footerHints())
		segs, hints = shedStatusToFit(segs, hints, width)
		right = renderHints(hints)
	}

	left := joinSegs(segs)
	gap := width - visibleRunes(left) - visibleRunes(right) - statusHorizontalPadding
	if gap < statusMinGap {
		gap = statusMinGap
	}
	line := " " + left + strings.Repeat(" ", gap) + right + " "
	out := statusBarStyle().Width(max(width, 1)).MaxWidth(max(width, 1)).Render(ansi.Cut(line, 0, width))
	return chrome.PaintBand(out, width, theme.Current().ChromeBG())
}

// The status line sheds content to fit one row. Left segments and footer
// hints share a single priority scale so the row can drop whichever is
// globally least important, rather than dropping one whole cluster and then
// the other.
//
// This is the fix for "essential help being lost at 80 columns". The hint
// cluster used to be one string: too narrow for all of it meant too narrow
// for any of it, so "clear queue" and "? help" disappeared together while
// the model name stayed. Now a convenience reminder goes before the identity
// of the session, and an action that gets the user out of the state they are
// in is the last thing to leave the row.
//
// The two ends of the scale are the load-bearing part. Essential hints (1)
// outrank every droppable segment, so a stuck user always keeps the key that
// resolves their state. Optional hints (12) outrank even the browser URL
// (9), which is what stops "Tab mode" from outliving the swarm's browser
// segment — the failure mode is easy to hit and looks like the URL vanished
// for no reason. Likely sits at 6, tied with the worktree rather than with
// the branch: Ctrl+S is reachable and the worktree name is not more
// important than it, but the branch answers "where am I".
const (
	// statusPriorityHintEssential keeps essential action hints alongside the
	// mode cue and the route.
	statusPriorityHintEssential = 1
	// statusPriorityHintLikely sits with the worktree: reachable, but not
	// worth dropping the branch or the session identity for.
	statusPriorityHintLikely = 6
	// statusPriorityHintOptional outranks every droppable segment: pure
	// convenience, shed before anything that identifies the session.
	statusPriorityHintOptional = 12
)

// statusHintPriority places a hint on the shared drop scale. Lower survives
// longer; the mapping is what makes an essential hint outrank an optional
// identity segment.
func statusHintPriority(h help.Hint) int {
	switch h.Priority {
	case help.PriorityEssential:
		return statusPriorityHintEssential
	case help.PriorityLikely:
		return statusPriorityHintLikely
	default:
		return statusPriorityHintOptional
	}
}

// worstStatusSeg returns the index of the least important droppable left
// segment, or -1 when every segment is protected. Priority 0 segments (the
// mode cue, the untrusted warning) are never dropped: they answer "where am
// I" and "is this safe", which no amount of width pressure makes optional.
// The rightmost of equal priorities wins, so the row trims from its right
// edge the way a reader expects.
func worstStatusSeg(segs []statusSeg) int {
	worst := -1
	for i := 1; i < len(segs); i++ {
		if segs[i].priority <= 0 {
			continue
		}
		if worst < 0 || segs[i].priority >= segs[worst].priority {
			worst = i
		}
	}
	return worst
}

// worstHint returns the index of the least important hint, or -1 when there
// are none.
func worstHint(hints []help.Hint) int {
	worst := -1
	for i := range hints {
		if worst < 0 || statusHintPriority(hints[i]) >= statusHintPriority(hints[worst]) {
			worst = i
		}
	}
	return worst
}

// dropStatusSeg removes the segment at idx. A negative index is a no-op.
func dropStatusSeg(segs []statusSeg, idx int) []statusSeg {
	if idx < 0 || idx >= len(segs) {
		return segs
	}
	return append(segs[:idx], segs[idx+1:]...)
}

// dropHint removes the hint at idx. A negative index is a no-op.
func dropHint(hints []help.Hint, idx int) []help.Hint {
	if idx < 0 || idx >= len(hints) {
		return hints
	}
	return append(hints[:idx], hints[idx+1:]...)
}

// shedStatusToFit removes the globally least important item until the row
// fits. On an equal priority it sheds a left segment before a hint: an
// action the user can take is worth more than a second identity token.
func shedStatusToFit(segs []statusSeg, hints []help.Hint, width int) ([]statusSeg, []help.Hint) {
	fits := func() bool {
		return visibleRunes(joinSegs(segs))+visibleRunes(renderHints(hints))+
			statusHorizontalPadding+statusMinGap <= width
	}
	for !fits() {
		segIdx := worstStatusSeg(segs)
		hintIdx := worstHint(hints)
		switch {
		case segIdx < 0 && hintIdx < 0:
			// Nothing left to drop; the final ansi.Cut clips the row.
			return segs, hints
		case hintIdx < 0:
			segs = dropStatusSeg(segs, segIdx)
		case segIdx < 0:
			hints = dropHint(hints, hintIdx)
		case segs[segIdx].priority >= statusHintPriority(hints[hintIdx]):
			segs = dropStatusSeg(segs, segIdx)
		default:
			hints = dropHint(hints, hintIdx)
		}
	}
	return segs, hints
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
	if m.hasPendingApproval() {
		return "approval"
	}
	if m.state.PendingSkillGate() != nil {
		return "skill gate"
	}
	if m.state.PendingQuestion() != nil {
		return "answering"
	}
	mode := string(m.approvalMode)
	if mode == "" {
		mode = "default"
	}
	// System access is a modifier on the mode, not a mode of its own, so it
	// rides the mode cue rather than taking a segment of its own.
	if m.state.SystemAccess() {
		mode += " · system"
	}
	return mode
}

// statusLeftSegments returns the left-side status segments with priorities.
// Priorities (lower = higher priority, kept first when collapsing):
//
//	mode=0, untrusted=0, route=1, local=2, ctx=3, think=4, gen=4,
//	branch=5, dir=5, swarm tokens=6, jobs=7, queued=8
func (m Model) statusLeftSegments() []statusSeg {
	segs := []statusSeg{
		{text: modeStyle().Render(m.modeSegment()), priority: 0},
	}

	// Focus marker. It sits immediately after the mode cue because it answers
	// the same question — "what will this key do?" — and it carries a glyph
	// and a label, not just a color: the input bar's color shift was the only
	// cue that a surface other than the composer owned the keys, which is
	// invisible under NO_COLOR and to a color-blind reader.
	//
	// Priority 0 keeps it: a user who moved focus needs to see where it went
	// more than they need the model name.
	if f := m.effectiveFocus(); f != FocusComposer {
		segs = append(segs, statusSeg{
			text:     keyHintStyle().Render(f.marker()),
			priority: 0,
		})
	}

	if !m.state.Trusted() {
		segs = append(segs, statusSeg{text: untrustedStyle().Render("untrusted"), priority: 0})
	}

	// The reading surfaces report themselves here, at priority 2, because the
	// question they answer — "what will this key do, and what is this app
	// tracking for me?" — is nearer to the reader than the session's identity.
	// They outrank the route name, the context counter and the directory, and
	// they sit BELOW the priority-0 cues so a reader who has moved focus or is
	// on an untrusted tree still sees those.
	//
	// Priority 2 is deliberately not 1: 1 is the essential-hint slot, and a
	// hint that gets a stuck user out of a modal state must survive longer than
	// a search counter. Both are still shed after the mode cue and the
	// untrusted warning.
	if s := m.findStatus(); s != "" {
		segs = append(segs, statusSeg{text: warningStyle().Render(glyph.Search + " " + s), priority: 2})
	}
	if s := m.selectionStatus(); s != "" {
		segs = append(segs, statusSeg{text: dimStyle().Render(s), priority: 2})
	}

	route := m.state.ActiveRoute()
	if route.Active {
		segs = append(segs, statusSeg{text: identityStyle().Render(fmt.Sprintf("%s @ %s", route.Model, route.Provider)), priority: 1})
		if route.LocalOnly {
			segs = append(segs, statusSeg{text: dimStyle().Render("local"), priority: 2})
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
		// omitted rather than advertising a value that is never sent.
		if route.ReasoningCapable {
			effort := route.Thinking
			if effort == "" {
				effort = "default"
			}
			segs = append(segs, statusSeg{text: dimStyle().Render("think " + effort), priority: 4})
		}
	} else {
		segs = append(segs, statusSeg{text: identityStyle().Render("no model"), priority: 1})
		if !m.state.Config.Privacy.RemoteProvidersAllowed {
			segs = append(segs, statusSeg{text: dimStyle().Render("local"), priority: 2})
		}
	}

	// Live context use: tokens of the parent's latest model call vs the
	// resolved model window. Subagent usage is recorded on the child's own
	// session state (see buildSubagentFactory) and never folded in here, so
	// the number cannot inflate past the window.
	if used, window := m.state.TurnUsage(); window > 0 {
		segs = append(segs, statusSeg{text: dimStyle().Render(fmt.Sprintf("ctx %s/%s",
			strutil.CompactTokens(used), strutil.CompactTokens(window))), priority: 3})
	}

	// Generation > 0 means the session has compacted at least once. Show
	// it so a user who notices the agent "forgetting" can see that a
	// handoff happened rather than guessing.
	if gen := m.state.Generation(); gen.Seq > 0 {
		segs = append(segs, statusSeg{
			text:     dimStyle().Render(fmt.Sprintf("gen %d", gen.Seq)),
			priority: 4,
		})
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
		segs = append(segs, statusSeg{text: identityStyle().Render(filepath.Base(wd)), priority: 5})
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
		// The run panel owns task counts and phase; the status line keeps
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

	if m.diagnosticCount > 0 {
		segs = append(segs, statusSeg{
			text:     warningStyle().Render(fmt.Sprintf(glyph.Warning+" %d config issues · /doctor", m.diagnosticCount)),
			priority: 10,
		})
	}

	return segs
}

func browserStatusText(bi session.BrowserInfo) string {
	g := browserGlyphStyle().Render(glyph.Web)
	url := truncateURL(bi.URL, 20)
	if url == "" {
		url = bi.Mode
	}
	return g + " " + url
}

func (m Model) statusRightSegment() string {
	if m.hasPendingApproval() {
		return warningStyle().Render(glyph.Warning + " approval")
	}
	if n, ok := m.state.Notice(); ok {
		if n.Severity == session.SeverityWarn {
			return warningStyle().Render(glyph.Warning + " warning")
		}
		return errorStyle().Render("✘ error")
	}
	if t := m.toastText(); t != "" {
		return statusBusyStyle().Render(t)
	}
	return ""
}

// renderHints joins hint pairs with the dim separator, matching the
// keybinding footer's styling.
func renderHints(hints []help.Hint) string {
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		parts = append(parts, helpPair(h.Key, h.Label))
	}
	return strings.Join(parts, dimSeparator)
}

// noticeVisible reports whether a session notice is currently up. The
// error indicator in statusRightSegment is never shed for width, so the
// hint-cluster drop logic must skip it when a notice is visible.
func (m Model) noticeVisible() bool {
	_, ok := m.state.Notice()
	return ok
}

// footerHints snapshots everything the hint cluster needs.
//
// The key-driven hints are resolved from the same action context the
// dispatcher reads (see actions.go). That is the fix for the footer printing
// Ctrl+X twice with two different verbs: there is now one resolution, and the
// footer renders its result rather than re-deriving the decision from
// overlapping booleans.
func (m Model) footerHints() help.FooterHints {
	ctx := m.actionSnapshot()
	return help.FooterHints{
		Busy:             m.busy,
		EditingCommand:   m.editingCommand,
		ApprovalPending:  m.hasPendingApproval(),
		QuestionPending:  m.state.PendingQuestion() != nil,
		SkillGatePending: m.state.PendingSkillGate() != nil,
		PopupOpen:        m.activeCompletionPopup() != nil,
		Actions:          ctx.footerActionHints(),
	}
}

// footerActionHints lists the key-bound actions that are actionable right
// now, in the order the footer renders them. An action with no footer verb
// (F6, F2, Tab mode) is omitted: the footer is the L0 surface and stays
// minimal, while the palette and /help carry the full set.
func (ctx actionContext) footerActionHints() []help.Hint {
	var out []help.Hint
	for _, a := range resolveActions(ctx) {
		if a.KeyHint == "" || a.HintVerb == "" || a.Disabled {
			continue
		}
		if a.KeyHint == "Ctrl+X" {
			// Exactly one Ctrl+X action is the live one; the other is
			// suppressed rather than printed beside it.
			if id, ok := ctx.ctrlXID(); !ok || id != a.ID {
				continue
			}
		}
		out = append(out, help.Hint{Key: a.KeyHint, Label: a.HintVerb, Priority: a.Priority})
	}
	return out
}

// helpPair renders one key/verb hint pair, sharing the help package's
// styling so the footer and the status line cannot drift apart visually.
func helpPair(key, label string) string {
	return help.Pair(key, label)
}

// hasRunningSubagent reports whether any registered subagent is currently
// running, which makes the Ctrl+F drill-in hint actionable.
func (m Model) hasRunningSubagent() bool {
	for _, v := range m.state.Subagents() {
		if v.Status == session.SubagentRunning {
			return true
		}
	}
	return false
}
