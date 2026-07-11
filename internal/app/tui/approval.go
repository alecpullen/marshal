package tui

import (
	"strings"

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

// approvalModel wraps a *huh.Form that lets the user choose how to respond
// to a pending tool-call approval. It renders inline inside the chat input
// area (the transcript stays visible above). The form's select title carries
// the command/risk/sandbox summary that renderApprovalPanel used to show.
type approvalModel struct {
	form          *huh.Form
	tc            *session.PendingToolCall
	choice        approvalChoice
	width         int
	submitPending bool
	// done is set once the form reaches a terminal state, so the parent can
	// read Choice without re-dispatching the form.
	done bool
}

func newApprovalModel(tc *session.PendingToolCall, sb session.SandboxInfo, allowNetwork, hasBackup bool, width int) *approvalModel {
	am := &approvalModel{tc: tc, width: width}

	summary := approvalSummary(tc, sb, allowNetwork)

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
	// Esc denies (aborts the form). Ctrl+C is intercepted by the parent.
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			am.done = true
			am.choice = choiceDeny
			return am, nil
		case "enter":
			if am.submitPending {
				am.done = true
				return am, nil
			}
			am.submitPending = true
			return am, nil
		case "up", "down", "j", "k":
			am.submitPending = false
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
	if am.form == nil {
		return ""
	}
	var b strings.Builder
	if am.tc != nil && am.tc.Diff != "" && am.width > 0 {
		diff := diffview.Render(am.tc.Diff, diffview.Options{
			Width:     am.width,
			Mode:      diffview.ModeAuto,
			Highlight: true,
		})
		b.WriteString(diff)
		b.WriteString("\n")
	}
	b.WriteString(am.form.View())
	if am.submitPending {
		b.WriteString("\n")
		b.WriteString(promptPrefixStyle.Render("▸ Submit selected action"))
	} else {
		b.WriteString("\n")
		b.WriteString(mutedStyle.Render("  Submit selected action"))
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
	muted := mutedStyle
	text := lipgloss.NewStyle()

	var b strings.Builder
	b.WriteString(titleStyle.Render("⚠ Approval needed (j/k, enter to select)"))
	b.WriteString("\n")

	if tc.Name == "shell.run" {
		b.WriteString(muted.Render("Agent wants to run:"))
		b.WriteString("\n")
		b.WriteString(text.Render(tc.Command))
	} else {
		b.WriteString(muted.Render("Agent wants to call tool: ") + toolNameStyle.Render(tc.Name))
		b.WriteString("\n")
		if tc.Schema != "" {
			b.WriteString(muted.Render("Description: ") + text.Render(tc.Schema))
			b.WriteString("\n")
		}
		b.WriteString(muted.Render("Arguments: ") + text.Render(tc.Args))
	}
	b.WriteString("\n\n")
	b.WriteString(riskLabelStyle.Render("Risk: "))
	b.WriteString(text.Render(riskText(tc)))
	if iso := sandboxIsolationText(sb, allowNetwork); iso != "" && (tc.Name == "shell.run" || tc.Name == "test.run") {
		b.WriteString("\n")
		b.WriteString(muted.Render(iso))
	}
	return b.String()
}
