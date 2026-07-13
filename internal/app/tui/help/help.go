// Package help renders the persistent keybinding footer and the ? help
// overlay for the main marshal chat view. The footer always shows the 3-5
// most actionable shortcuts for the current mode (progressive disclosure L0);
// the overlay (triggered by ?) lists every binding (L1/L2).
package help

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Rows is the vertical budget the persistent footer occupies in the main
// layout; the transcript viewport shrinks by this amount.
const Rows = 1

// FooterHints describes which mode-driven hints are currently actionable.
// The footer shows the union of always-on hints plus the mode-specific ones
// so a user never sees a hint they can't act on right now.
type FooterHints struct {
	Busy            bool
	EditingCommand  bool
	ApprovalPending bool
	QuestionPending bool
	PopupOpen       bool
}

var keyStyle = lipgloss.NewStyle().Bold(true)
var sep = lipgloss.NewStyle().Faint(true).SetString(" · ")

func pair(k, label string) string { return keyStyle.Render(k) + " " + label }

// Footer returns the single-row keybinding bar.
func Footer(h FooterHints) string {
	// ? is only appended when it actually opens the help overlay. During
	// approval/question/popup/edit forms ? is consumed by the form itself.
	showHelpHint := !h.QuestionPending && !h.ApprovalPending && !h.PopupOpen && !h.EditingCommand

	var segs []string
	if h.QuestionPending {
		segs = append(segs, pair("Enter", "answer"), pair("Esc", "skip"))
	} else if h.ApprovalPending && !h.EditingCommand {
		segs = append(segs, pair("Enter×2", "approve"), pair("d", "deny"), pair("e", "edit"), pair("a", "always"), pair("Esc", "deny"))
	} else if h.EditingCommand {
		segs = append(segs, pair("Enter", "save"), pair("Esc", "cancel edit"))
	} else if h.PopupOpen {
		segs = append(segs, pair("↑↓", "choose"), pair("Tab/Enter", "accept"), pair("Esc", "dismiss"))
	} else if h.Busy {
		segs = append(segs,
			pair("Enter", "send"),
			pair("Shift+Enter", "newline"),
			pair("Esc", "cancel"),
			pair("Ctrl+X", "clear queue"),
		)
	} else {
		segs = append(segs,
			pair("Tab", "mode"),
			pair("Alt+M", "model"),
			pair("/", "command"),
			pair("@", "file"),
		)
	}
	if showHelpHint {
		segs = append(segs, pair("?", "help"))
	}
	sepStr := sep.Render("")
	return strings.Join(segs, sepStr)
}

// Overlay returns the full-screen help panel shown when ? is pressed.
func Overlay(width, height int) string {
	lines := []string{
		"marshal keys",
		"",
		"  Enter          send message / accept",
		"  Shift+Enter     newline in input",
		"  /              command completion",
		"  @              file completion",
		"  ↑↓             choose completion · PgUp/PgDn/Ctrl-U/Ctrl-D/End scroll",
		"  Tab            accept completion",
		"  Esc            cancel turn · dismiss popup · deny approval",
		"  Ctrl+O         settings",
		"  Ctrl+K         memory browser",
		"  Ctrl+G         toggle thinking",
		"  Ctrl+R         rollback last change",
		"  Ctrl+X         clear steering queue (while busy)",
		"  ?              this help",
		"  Ctrl+C         quit",
		"",
		"Press ? or Esc to close.",
	}
	body := strings.Join(lines, "\n")
	return lipgloss.NewStyle().Width(width).Height(height).Align(lipgloss.Center, lipgloss.Center).Render(body)
}
