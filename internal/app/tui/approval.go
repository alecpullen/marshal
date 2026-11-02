package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/huhtheme"
	"marshal/internal/diffview"
	"marshal/internal/strutil"
)

// approvalChoice is the set of actions the inline approval chooser offers.
type approvalChoice string

const (
	choiceApprove      approvalChoice = "approve"
	choiceDeny         approvalChoice = "deny"
	choiceEdit         approvalChoice = "edit"
	choiceAlways       approvalChoice = "always"
	choiceSessionAllow approvalChoice = "session_allow"
	choiceRollback     approvalChoice = "rollback"
)

var choiceLabels = map[approvalChoice]string{
	choiceApprove:      "allow",
	choiceAlways:       "always",
	choiceSessionAllow: "session",
	choiceEdit:         "edit",
	choiceDeny:         "deny",
	choiceRollback:     "rollback",
}

// choiceMnemonic maps each choice to its single-key shortcut (shown in the
// interactive panel so the user knows the keys work). Matches mnemonicChoice.
var choiceMnemonic = map[approvalChoice]string{
	choiceApprove:      "a",
	choiceAlways:       "A",
	choiceSessionAllow: "s",
	choiceEdit:         "e",
	choiceDeny:         "d",
}

func indexOf(s []approvalChoice, v approvalChoice) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// approvalModel wraps a *huh.Form that lets the user choose how to respond
// to a pending tool-call approval. It renders inline inside the chat input
// area (the transcript stays visible above). The form's select title carries
// the command/risk/sandbox summary that renderApprovalPanel used to show.
type approvalModel struct {
	form   *huh.Form
	a      acpwire.ApprovalRequest
	choice approvalChoice
	// review is the bounded, pageable window over the sanitized operation the
	// prompt is asking the user to authorise.
	review promptReview
	// why is who asked and, in the model's own words, why.
	why approvalWhy
	// bodyHeight is how many display rows the review may occupy. The parent
	// Model computes it from the frame budget; zero means the body does not
	// fit, and the prompt is then not actionable.
	bodyHeight int
	// candidates is the ordered list of approval choices the user can pick.
	// Tracked locally so we can update am.choice from h/l navigation
	// without forwarding to huh (which we don't, because we intercept
	// Enter to drive the explicit two-step submit flow).
	candidates    []approvalChoice
	selected      int
	width         int
	submitPending bool
	// done is set once the form reaches a terminal state, so the parent can
	// read Choice without re-dispatching the form.
	done bool
	// pendingDenyAt is set to time.Now() on the first Esc press. A second
	// Esc within 1.5s confirms the deny. Any other key resets it to zero.
	pendingDenyAt time.Time
	sb            acpwire.Sandbox
}

// mnemonicChoice maps the mnemonic keys advertised in approvalActionRow
// (transcript.go) to their approval choices. Returns ok=false when the
// key is not a mnemonic.
func mnemonicChoice(key string) (approvalChoice, bool) {
	switch key {
	case "a":
		return choiceApprove, true
	case "A":
		return choiceAlways, true
	case "s":
		return choiceSessionAllow, true
	case "e":
		return choiceEdit, true
	case "d":
		return choiceDeny, true
	default:
		return "", false
	}
}

// approvalWhy is the context an approval prompt shows about who is asking and
// why: the owner of the requesting step (empty for the orchestrator) and the
// first sentence of that step's narration. Both are empty when unknown. The
// why line is only ever the agent's own words — never an inferred headline.
type approvalWhy struct {
	owner string
	why   string
}

// approvalWhyOf is the who-and-why an approval prompt shows, from the wire.
func approvalWhyOf(a acpwire.ApprovalRequest) approvalWhy {
	return approvalWhy{owner: a.Actor, why: a.Why}
}

// ownerLine is "<owner> wants to run a command", or "… to <tool>".
func (w approvalWhy) ownerLine(a acpwire.ApprovalRequest) string {
	if w.owner == "" {
		return ""
	}
	owner := strutil.ShowControls(w.owner)
	if a.Tool == "shell.run" {
		return owner + " wants to run a command"
	}
	return owner + " wants to " + strings.ToLower(a.ToolLabel)
}

// whyLine quotes the step's narration, which the model wrote, so its
// controls are shown like the command's (see shownApproval).
func (w approvalWhy) whyLine() string {
	if w.why == "" {
		return ""
	}
	return "why  \"" + strutil.ShowControls(w.why) + "\""
}

// shownApproval is a as an approval prompt displays it. The prompt is what
// the user authorises, and every text field in it is model-written — the
// command, the arguments, the patch behind the diff — or can quote it, so a
// control character in any of them (ESC, CR, a bidi override) could erase,
// overwrite or reorder what the user reads. Each one is shown as a visible
// escape instead (strutil.ShowControls). Only the renderers read the copy:
// the edit prefill and the decision use the request itself.
func shownApproval(a acpwire.ApprovalRequest) acpwire.ApprovalRequest {
	a.Args = strutil.ShowControls(a.Args)
	a.Command = strutil.ShowControls(a.Command)
	a.Risk = strutil.ShowControls(a.Risk)
	a.Reason = strutil.ShowControls(a.Reason)
	a.Diff = strutil.ShowControls(a.Diff)
	a.Schema = strutil.ShowControls(a.Schema)
	return a
}

func newApprovalModel(a acpwire.ApprovalRequest, sb acpwire.Sandbox, hasBackup bool, width int) *approvalModel {
	opts := []huh.Option[approvalChoice]{
		huh.NewOption("Approve", choiceApprove),
		huh.NewOption("Deny", choiceDeny),
		huh.NewOption("Edit command/args", choiceEdit),
	}
	if !a.Forced {
		opts = append(opts,
			huh.NewOption("Always allow (save to config)", choiceAlways),
			huh.NewOption("Allow this session", choiceSessionAllow),
		)
	}
	if hasBackup {
		opts = append(opts, huh.NewOption("Rollback last change", choiceRollback))
	}
	candidates := make([]approvalChoice, len(opts))
	for i, o := range opts {
		candidates[i] = o.Value
	}
	am := &approvalModel{
		a:          a,
		width:      width,
		choice:     choiceApprove,
		candidates: candidates,
		selected:   0,
		sb:         sb,
		why:        approvalWhyOf(a),
		bodyHeight: defaultApprovalBodyHeight,
	}

	summary := approvalSummary(a, sb, width)

	sel := huh.NewSelect[approvalChoice]().
		Title(summary).
		Height(12).
		Options(opts...).
		Value(&am.choice)

	group := huh.NewGroup(sel)

	km := huh.NewDefaultKeyMap()
	// Esc/Ctrl+C must NOT quit the app from here: Ctrl+C is intercepted by
	// the parent before reaching the form, and Esc should deny (abort the
	// form → StateAborted → parent sends a deny decision). huh's default
	// Quit binding is ctrl+c; leave it so the parent's top-of-Update guard
	// handles the global quit. Esc is mapped to the select's clear-filter
	// by default; override the form's Quit to a no-op so Esc falls through
	// to our explicit handling in the parent.
	km.Quit = key.NewBinding(key.WithKeys())
	km.Select.Submit = key.NewBinding(key.WithKeys("enter"))
	// Enter is handled in approvalModel.Update: first Enter arms the explicit
	// submit step, second Enter confirms the selected action.

	am.form = huh.NewForm(group).
		WithTheme(huhtheme.WarmSunset()).
		WithWidth(max(width, 30)).
		WithShowHelp(false).
		WithKeyMap(km)

	// Eagerly init so the first field renders focused.
	if cmd := am.form.Init(); cmd != nil {
		_ = cmd()
	}
	return am
}

func (am *approvalModel) SetSize(width int) {
	am.width = width
	if am.form != nil {
		am.form.WithWidth(max(width, 30))
	}
}

func (am *approvalModel) Update(msg tea.Msg) (*approvalModel, tea.Cmd) {
	if am.form == nil || am.done {
		return am, nil
	}
	// Esc requires a second press within 1.5s to deny. Ctrl+C is
	// intercepted by the parent.
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if k.String() != "esc" {
			// Any non-Esc key resets the pending deny.
			am.pendingDenyAt = time.Time{}
		}
		switch k.String() {
		case "esc":
			// Starting a denial cancels an armed confirmation. Enter arms the
			// action and a second Enter confirms it; without this, a user who
			// pressed Escape (meaning to cancel) and then pressed Enter for
			// any reason would confirm the action they had just refused.
			am.submitPending = false
			if am.pendingDenyAt.IsZero() {
				am.pendingDenyAt = time.Now()
				return am, nil
			}
			if time.Since(am.pendingDenyAt) <= 1500*time.Millisecond {
				am.done = true
				am.choice = choiceDeny
				return am, nil
			}
			// Expired; treat as a new first press.
			am.pendingDenyAt = time.Now()
			return am, nil
		case "enter":
			if am.submitPending {
				am.choice = am.candidates[am.selected]
				am.done = true
				return am, nil
			}
			am.submitPending = true
			return am, nil
		case "left", "h":
			if am.selected > 0 {
				am.selected--
				am.choice = am.candidates[am.selected]
				am.submitPending = false
			}
			return am, nil
		case "right", "l":
			if am.selected < len(am.candidates)-1 {
				am.selected++
				am.choice = am.candidates[am.selected]
				am.submitPending = false
			}
			return am, nil
		default:
			// The mnemonic row (approvalActionRow, transcript.go) has always
			// advertised a/A/s/e/d; until now only esc/enter/arrows were
			// bound, so every label in that row was decorative.
			if c, ok := mnemonicChoice(k.String()); ok {
				for i, cand := range am.candidates {
					if cand == c {
						am.selected, am.choice = i, c
						am.submitPending = false
						break
					}
				}
				return am, nil
			}
		}
	}
	updated, cmd := am.form.Update(msg)
	if f, ok := updated.(*huh.Form); ok {
		am.form = f
	}
	if am.form.State == huh.StateCompleted {
		am.done = true
		return am, nil
	}
	if am.form.State == huh.StateAborted {
		am.done = true
		am.choice = choiceDeny
		return am, nil
	}
	return am, cmd
}

// defaultApprovalBodyHeight is the review height a prompt gets before the
// parent Model has measured the frame: the eager build during New, and the
// prompts tests construct directly. syncPromptReviewHeights replaces it with
// the real budget on the first render.
const defaultApprovalBodyHeight = 12

// approvalControlRows is how many pinned rows an approval prompt renders below
// its review body: the overflow hint, the compact action selector and the
// arm/deny notice.
//
// The hint row is reserved even when nothing is omitted (it then renders
// blank) and the notice row is reserved even when empty, so paging, arming and
// opening the deny window cannot change the prompt's height — and therefore
// cannot make the row budget disagree with what is drawn.
func approvalControlRows() int { return 3 }

// SetBodyHeight records how many review rows the frame can give this prompt.
// Zero means the body does not fit: the prompt renders its pinned controls
// only and accepts no decision (see Model.promptActionable).
func (am *approvalModel) SetBodyHeight(height int) {
	am.bodyHeight = max(height, 0)
}

// contentWidth is the width the review and its pinned rows wrap to: the
// console already spends gutterWidth cells on the prompt's own gutter, and a
// row wider than what is left would be truncated — which is exactly how a
// selector's counter goes missing.
func (am *approvalModel) contentWidth() int {
	return max(am.width-gutterWidth, 1)
}

// reviewLines builds the sanitized operation the prompt asks the user to
// authorise, as logical lines. Every field comes from shownApproval, the
// display safety boundary: control characters in a model-written command,
// argument, patch or reason are shown as visible escapes instead of being
// executed by the terminal.
func (am *approvalModel) reviewLines() []string {
	a := shownApproval(am.a)
	lines := make([]string, 0, 12)
	if line := am.why.ownerLine(a); line != "" {
		lines = append(lines, line)
	}
	if a.Tool == "shell.run" {
		lines = append(lines, "Agent wants to run:")
		if a.Command != "" {
			lines = append(lines, a.Command)
		}
	} else {
		lines = append(lines, "Agent wants to call tool: "+a.Tool)
		if a.Schema != "" {
			lines = append(lines, "Description: "+a.Schema)
		}
		if a.Args != "" {
			lines = append(lines, "Arguments: "+a.Args)
		}
	}
	if line := am.why.whyLine(); line != "" {
		lines = append(lines, line)
	}
	// Risk and Reason are separate facts and are shown as separate fields:
	// riskText falls back to Reason, so a prompt with both would otherwise
	// hide the sentence that says why the guardrail or policy fired.
	if risk := riskText(a); risk != "" {
		lines = append(lines, "Risk: "+risk)
	}
	if iso := sandboxIsolationText(am.sb); iso != "" && (a.Tool == "shell.run" || a.Tool == "test.run") {
		lines = append(lines, iso)
	}
	if a.Reason != "" {
		lines = append(lines, "Reason: "+a.Reason)
	}
	if a.Diff != "" {
		// The whole diff, never truncated: the review wraps long lines, so a
		// patch line keeps its suffix instead of losing it to an ellipsis.
		diff := diffview.Render(a.Diff, diffview.Options{
			Width:     am.contentWidth(),
			Mode:      diffview.ModeAuto,
			Highlight: true,
		})
		lines = append(lines, strings.Split(strings.TrimRight(diff, "\n"), "\n")...)
	}
	return lines
}

// reviewOmits reports whether the review window leaves rows unseen, i.e.
// whether the "rows x–y of n" indicator (or, for a one-row body, the pinned
// hint) is telling the user to page.
func (am *approvalModel) reviewOmits(total int) bool {
	switch {
	case am.bodyHeight <= 0:
		return total > 0
	case am.bodyHeight == 1:
		return total > 1
	default:
		return total > am.bodyHeight-1
	}
}

// overflowHintRow is the pinned row that tells the user the body continues. It
// is reserved unconditionally and renders blank when everything is visible.
func (am *approvalModel) overflowHintRow(total int) string {
	if !am.reviewOmits(total) {
		return ""
	}
	return mutedStyle().Render("PgUp/PgDn to review the rest")
}

// selectorRow renders the selected action alone, with its position in the
// list. Joining every label wraps in the console's column and clips the choice
// the user is about to confirm; the arrows, h/l and the mnemonics still reach
// every candidate.
func (am *approvalModel) selectorRow() string {
	if am.selected < 0 || am.selected >= len(am.candidates) {
		return ""
	}
	ch := am.candidates[am.selected]
	label := choiceLabels[ch]
	if mk, ok := choiceMnemonic[ch]; ok {
		label = mk + " " + label
	}
	head := "▸ " + label
	counter := fmt.Sprintf("%d/%d", am.selected+1, len(am.candidates))
	width := am.contentWidth()
	gap := width - ansi.StringWidth(head) - ansi.StringWidth(counter)
	if gap < 1 {
		head = ansi.Truncate(head, max(width-ansi.StringWidth(counter)-1, 1), "…")
		gap = max(width-ansi.StringWidth(head)-ansi.StringWidth(counter), 1)
	}
	selected := lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(head)
	return selected + strings.Repeat(" ", gap) + mutedStyle().Render(counter)
}

// noticeRow is the pinned arm/deny notice. It is reserved even when empty so
// that arming or opening the deny window never reflows the body.
func (am *approvalModel) noticeRow() string {
	if !am.pendingDenyAt.IsZero() {
		return warningStyle().Render("Press Esc again to deny · any other key cancels")
	}
	if am.submitPending {
		return promptPrefixStyle().Render("Press Enter again to confirm")
	}
	return ""
}

func (am *approvalModel) View() string {
	if am.form == nil {
		return ""
	}
	rows := promptReviewRows(am.reviewLines(), am.contentWidth())
	lines := am.review.Window(rows, am.bodyHeight)
	// Pinned rows below the body, top to bottom: the overflow hint (blank when
	// nothing is omitted), the selected action, then the arm/deny notice.
	// Reserving each of them keeps the prompt's height constant as the user
	// pages, arms an action or opens the deny window.
	lines = append(lines, am.overflowHintRow(len(rows)), am.selectorRow(), am.noticeRow())

	// No rail of its own: the console draws "│ … │" around this section, and
	// the first rendered row carries the warning glyph so the prompt still
	// reads as one block.
	var b strings.Builder
	for i, line := range lines {
		if i == 0 {
			b.WriteString(gutterPrefix(glyph.Warning, warningColor))
		} else {
			b.WriteString(gutterPrefix(" ", warningColor))
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func (am *approvalModel) Choice() approvalChoice { return am.choice }
func (am *approvalModel) IsDone() bool           { return am.done }

// approvalSummary builds the multi-line title shown above the select. It
// mirrors the body of renderApprovalPanel (command/description/arguments,
// risk, sandbox isolation) but as plain titled text the select can render.
func approvalSummary(a acpwire.ApprovalRequest, sb acpwire.Sandbox, width int) string {
	w := approvalWhyOf(a)
	a = shownApproval(a)
	titleStyle := lipgloss.NewStyle().Foreground(warningColor).Bold(true)
	muted := mutedStyle()
	text := lipgloss.NewStyle()

	var b strings.Builder
	b.WriteString(titleStyle.Render(glyph.Warning + " Approval needed (←/→, enter to select)"))
	b.WriteString("\n")
	if line := w.ownerLine(a); line != "" {
		b.WriteString(muted.Render(line))
		b.WriteString("\n")
	}

	if a.Tool == "shell.run" {
		b.WriteString(muted.Render("Agent wants to run:"))
		b.WriteString("\n")
		b.WriteString(text.Render(a.Command))
	} else {
		b.WriteString(muted.Render("Agent wants to call tool: ") + toolNameStyle().Render(a.Tool))
		b.WriteString("\n")
		if a.Schema != "" {
			b.WriteString(muted.Render("Description: ") + text.Render(a.Schema))
			b.WriteString("\n")
		}
		b.WriteString(muted.Render("Arguments: ") + text.Render(a.Args))
	}
	if line := w.whyLine(); line != "" {
		b.WriteString("\n")
		b.WriteString(muted.Render(ansi.Wrap(line, max(width, 30), WrapBreakpoints)))
	}
	b.WriteString("\n\n")
	b.WriteString(riskLabelStyle().Render("Risk: "))
	b.WriteString(text.Render(riskText(a)))
	if iso := sandboxIsolationText(sb); iso != "" && (a.Tool == "shell.run" || a.Tool == "test.run") {
		b.WriteString("\n")
		b.WriteString(muted.Render(iso))
	}
	// Reason says which mechanism fired — policy rule, guardrail, copilot's
	// environment confirm, or the default-secure fallback. Without it the
	// prompt states what is being asked but never why, and the cost-consent
	// prompt's actual sentence is hidden behind its Risk label.
	//
	// Rendered verbatim: these strings are matched elsewhere by prefix and
	// substring (isEnvironmentConfirm, isGuardrailDeny), so rewording them
	// breaks control flow.
	if a.Reason != "" {
		b.WriteString("\n")
		wrapWidth := width
		if wrapWidth < 30 {
			wrapWidth = 80
		}
		b.WriteString(mutedStyle().Render(ansi.Wrap(a.Reason, wrapWidth, WrapBreakpoints)))
	}
	return b.String()
}
