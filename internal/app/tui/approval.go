package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/huhtheme"
	"marshal/internal/diffview"
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

func choiceKey(ch approvalChoice) string {
	switch ch {
	case choiceApprove:
		return "a"
	case choiceAlways:
		return "A"
	case choiceSessionAllow:
		return "s"
	case choiceEdit:
		return "e"
	case choiceDeny:
		return "d"
	case choiceRollback:
		return "r"
	}
	return ""
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
	tc     *session.PendingToolCall
	choice approvalChoice
	// candidates is the ordered list of approval choices the user can pick.
	// Tracked locally so we can update am.choice from j/k navigation
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
	sb            session.SandboxInfo
	allowNetwork  bool
}

func newApprovalModel(tc *session.PendingToolCall, sb session.SandboxInfo, allowNetwork, hasBackup bool, width int) *approvalModel {
	opts := []huh.Option[approvalChoice]{
		huh.NewOption("Approve", choiceApprove),
		huh.NewOption("Deny", choiceDeny),
		huh.NewOption("Edit command/args", choiceEdit),
		huh.NewOption("Always allow (save to config)", choiceAlways),
		huh.NewOption("Allow this session", choiceSessionAllow),
	}
	if hasBackup {
		opts = append(opts, huh.NewOption("Rollback last change", choiceRollback))
	}
	candidates := make([]approvalChoice, len(opts))
	for i, o := range opts {
		candidates[i] = o.Value
	}
	am := &approvalModel{
		tc:           tc,
		width:        width,
		choice:       choiceApprove,
		candidates:   candidates,
		selected:     0,
		sb:           sb,
		allowNetwork: allowNetwork,
	}

	summary := approvalSummary(tc, sb, allowNetwork)

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
		case "up", "k":
			if am.selected > 0 {
				am.selected--
				am.choice = am.candidates[am.selected]
				am.submitPending = false
			}
			return am, nil
		case "down", "j":
			if am.selected < len(am.candidates)-1 {
				am.selected++
				am.choice = am.candidates[am.selected]
				am.submitPending = false
			}
			return am, nil
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

func (am *approvalModel) View() string {
	if am.form == nil || am.tc == nil {
		return ""
	}
	var b strings.Builder
	if am.tc.Diff != "" && am.width > 0 {
		diff := diffview.Render(am.tc.Diff, diffview.Options{
			Width:     am.width,
			Mode:      diffview.ModeAuto,
			Highlight: true,
		})
		// Render the diff as a surface-tinted block.
		for _, line := range strings.Split(diff, "\n") {
			if line == "" {
				continue
			}
			b.WriteString(codeSurfaceStyle().Width(max(am.width-2, 1)).Render(line))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(gutterPrefix("⚠", warningColor))
	headParts := []string{}
	if am.tc.Name == "shell.run" {
		headParts = append(headParts, am.tc.Command)
	} else {
		headParts = append(headParts, am.tc.Name)
	}
	headParts = append(headParts, riskText(am.tc))
	if iso := sandboxIsolationText(am.sb, am.allowNetwork); iso != "" {
		headParts = append(headParts, iso)
	}
	b.WriteString(warningStyle().Render(strings.Join(headParts, dimSeparator)))
	b.WriteString("\n")

	var choices []string
	for _, ch := range am.candidates {
		label := choiceLabels[ch]
		selected := am.selected == indexOf(am.candidates, ch)
		style := mutedStyle()
		prefix := "  "
		if selected {
			style = lipgloss.NewStyle().Foreground(coralColor).Bold(true)
			prefix = "▸ "
			if am.submitPending {
				prefix = "▸ "
			}
		}
		choices = append(choices, style.Render(prefix+label))
	}
	b.WriteString(gutterPrefix(" ", warningColor)) // blank gutter for selector row
	b.WriteString(strings.Join(choices, mutedStyle().Render("  ")))
	b.WriteString("\n")

	if !am.pendingDenyAt.IsZero() {
		b.WriteString(gutterPrefix(" ", warningColor))
		b.WriteString(warningStyle().Render("Press Esc again to deny · any other key cancels"))
		b.WriteString("\n")
	} else if am.submitPending {
		b.WriteString(gutterPrefix(" ", warningColor))
		b.WriteString(promptPrefixStyle().Render("Press Enter again to confirm"))
		b.WriteString("\n")
	}
	return b.String()
}

func (am *approvalModel) Choice() approvalChoice { return am.choice }
func (am *approvalModel) IsDone() bool           { return am.done }

// approvalSummary builds the multi-line title shown above the select. It
// mirrors the body of renderApprovalPanel (command/description/arguments,
// risk, sandbox isolation) but as plain titled text the select can render.
func approvalSummary(tc *session.PendingToolCall, sb session.SandboxInfo, allowNetwork bool) string {
	titleStyle := lipgloss.NewStyle().Foreground(warningColor).Bold(true)
	muted := mutedStyle()
	text := lipgloss.NewStyle()

	var b strings.Builder
	b.WriteString(titleStyle.Render("⚠ Approval needed (j/k, enter to select)"))
	b.WriteString("\n")

	if tc.Name == "shell.run" {
		b.WriteString(muted.Render("Agent wants to run:"))
		b.WriteString("\n")
		b.WriteString(text.Render(tc.Command))
	} else {
		b.WriteString(muted.Render("Agent wants to call tool: ") + toolNameStyle().Render(tc.Name))
		b.WriteString("\n")
		if tc.Schema != "" {
			b.WriteString(muted.Render("Description: ") + text.Render(tc.Schema))
			b.WriteString("\n")
		}
		b.WriteString(muted.Render("Arguments: ") + text.Render(tc.Args))
	}
	b.WriteString("\n\n")
	b.WriteString(riskLabelStyle().Render("Risk: "))
	b.WriteString(text.Render(riskText(tc)))
	if iso := sandboxIsolationText(sb, allowNetwork); iso != "" && (tc.Name == "shell.run" || tc.Name == "test.run") {
		b.WriteString("\n")
		b.WriteString(muted.Render(iso))
	}
	return b.String()
}
