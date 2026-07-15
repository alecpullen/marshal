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
	// IdleRollbackEligible is true when a backup exists and the user is idle.
	IdleRollbackEligible bool
	// ThinkingVisible reflects the current thinking-block visibility toggle.
	ThinkingVisible bool
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
		if h.IdleRollbackEligible {
			segs = append(segs, pair("Ctrl+R", "rollback"))
		}
		if h.ThinkingVisible {
			segs = append(segs, pair("Ctrl+G", "hide thinking"))
		} else {
			segs = append(segs, pair("Ctrl+G", "show thinking"))
		}
	}
	if showHelpHint {
		segs = append(segs, pair("?", "help"))
	}
	sepStr := sep.Render("")
	return strings.Join(segs, sepStr)
}

const keyColumnWidth = 20

// table is the keybinding table rendered by Overlay. Defined at package
// level to avoid re-allocation on every call.
var table = [][]string{
	{"Enter", "send message / accept"},
	{"Shift+Enter", "newline in input"},
	{"", ""},
	{"/", "command completion"},
	{"@", "file completion"},
	{"↑↓", "choose completion"},
	{"PgUp/PgDn", "scroll transcript"},
	{"Ctrl+U/Ctrl+D", "half-page scroll"},
	{"End", "jump to bottom"},
	{"", ""},
	{"Tab", "cycle mode (auto→ask→edit) · accept completion"},
	{"Shift+Tab", "cycle mode backward"},
	{"Alt+M", "cycle model"},
	{"Alt+Shift+M", "cycle model backward"},
	{"Esc", "cancel turn · dismiss popup · deny approval"},
	{"", ""},
	{"Ctrl+O", "settings"},
	{"Ctrl+P", "model picker"},
	{"Ctrl+K", "memory browser"},
	{"Ctrl+G", "toggle thinking"},
	{"Ctrl+R", "rollback last change"},
	{"Ctrl+X", "clear steering queue (while busy)"},
	{"", ""},
	{"?", "this help"},
	{"Ctrl+C", "quit"},
}

// Overlay returns the full-screen help panel shown when ? is pressed.
func Overlay(width, height int) string {
	overlayKeyStyle := lipgloss.NewStyle().Bold(true).Width(keyColumnWidth)
	descStyle := lipgloss.NewStyle().Width(max(width-keyColumnWidth-4, 20))
	rows := make([]string, 0, len(table)+4)
	rows = append(rows, "marshal keys", "")
	for _, r := range table {
		if r[0] == "" && r[1] == "" {
			rows = append(rows, "")
			continue
		}
		rows = append(rows, overlayKeyStyle.Render(r[0])+"  "+descStyle.Render(r[1]))
	}
	rows = append(rows, "", "Press ? or Esc to close.")
	body := strings.Join(rows, "\n")
	return lipgloss.NewStyle().Width(width).Height(height).Align(lipgloss.Center, lipgloss.Center).Render(body)
}
