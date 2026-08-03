// Package help renders the persistent keybinding footer for the main
// marshal chat view. The footer always shows the 3-5 most actionable
// shortcuts for the current mode (progressive disclosure L0); the full
// keybinding/command cheatsheet (L1/L2) is printed to the transcript by
// the /help command (triggered directly, or via ? on an empty textarea)
// rather than rendered as a full-screen overlay here.
package help

import (
	"strings"

	"charm.land/lipgloss/v2"
)

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
	// QueueNonEmpty is true when the steering queue has items waiting.
	QueueNonEmpty bool
	// TodosActive is true when the session has a non-empty todo list, so
	// the Ctrl+T panel toggle is actionable.
	TodosActive bool
	// RailEnabled is true when the side rail is configured and the terminal
	// is wide enough, so the Ctrl+B toggle is actionable.
	RailEnabled bool
	// MouseReleased reports that the mouse currently belongs to the terminal
	// (Ctrl+S). The hint's label flips on it: a user who has released the
	// mouse needs to be told how to get wheel scrolling back, and a user who
	// has not needs to be told selection is one key away.
	MouseReleased bool
	// SuppressMouseHint drops the Ctrl+S hint. The status line renders the
	// hint cluster all-or-nothing — it is one right-aligned string, dropped
	// whole when it will not fit — so an unconditional extra hint pushes the
	// entire cluster (including "clear queue") off a narrow terminal. The
	// caller retries with this set when the full cluster overflows, which
	// makes the mouse hint the first thing shed rather than the last.
	SuppressMouseHint bool
}

var keyStyle = lipgloss.NewStyle().Bold(true)
var sep = lipgloss.NewStyle().Faint(true).SetString(" · ")

func pair(k, label string) string { return keyStyle.Render(k) + " " + label }

// Footer returns the single-row keybinding bar.
func Footer(h FooterHints) string {
	// ? is only appended when it actually triggers /help (which prints the
	// cheatsheet to the transcript). During approval/question/popup/edit
	// forms ? is consumed by the form itself.
	showHelpHint := !h.QuestionPending && !h.ApprovalPending && !h.PopupOpen && !h.EditingCommand

	// The mouse hint is only actionable while idle, but it is appended after
	// the queue hint rather than inside the idle branch: the footer is
	// truncated to the frame width, and losing "clear queue" to a mouse-mode
	// reminder is the wrong trade.
	showMouseHint := false

	var segs []string
	if h.QuestionPending {
		segs = append(segs, pair("Enter", "answer"), pair("Esc", "skip"))
	} else if h.ApprovalPending && !h.EditingCommand {
		// One hint per key: this used to emit Enter twice, labelled "arm" and
		// "submit", in the same row.
		segs = append(segs,
			pair("←→", "choose"),
			pair("Enter", "confirm"),
			pair("Esc", "deny"),
		)
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
			pair("/", "cmd"),
		)
		if h.IdleRollbackEligible {
			segs = append(segs, pair("Ctrl+R", "rollback"))
		}
		if h.TodosActive {
			segs = append(segs, pair("Ctrl+T", "tasks"))
		}
		if h.RailEnabled {
			segs = append(segs, pair("Ctrl+B", "rail"))
		}
		showMouseHint = true
	}
	if h.QueueNonEmpty {
		segs = append(segs, pair("Ctrl+X", "clear queue"))
	}
	if showMouseHint && !h.SuppressMouseHint {
		if h.MouseReleased {
			segs = append(segs, pair("Ctrl+S", "scroll"))
		} else {
			segs = append(segs, pair("Ctrl+S", "select"))
		}
	}
	if showHelpHint {
		segs = append(segs, pair("?", "help"))
	}
	sepStr := sep.Render("")
	return strings.Join(segs, sepStr)
}
