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

// CtrlXHint names the one action Ctrl+X takes in the current context, as
// decided by the caller. The footer renders at most one hint from it, so the
// bar can never advertise two different meanings for the same chord; deciding
// what the key means is the caller's job, not the footer's.
type CtrlXHint uint8

const (
	CtrlXHintNone CtrlXHint = iota
	CtrlXHintStopAgent
	CtrlXHintClearQueue
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
	// CtrlXHint is the single Ctrl+X action the global key handler will take
	// right now. It is CtrlXHintNone whenever an earlier input owner has the
	// key, and the caller leaves clear-queue at none while the queue is empty.
	CtrlXHint CtrlXHint
	// MouseReleased reports that the mouse currently belongs to the terminal
	// (Ctrl+S). The hint's label flips on it: a user who has released the
	// mouse needs to be told how to get wheel scrolling back, and a user who
	// has not needs to be told copying is a drag away.
	MouseReleased bool
	// SuppressMouseHint drops the mouse hint (drag copy while captured,
	// Ctrl+S scroll while released). The status line renders the
	// hint cluster all-or-nothing — it is one right-aligned string, dropped
	// whole when it will not fit — so an unconditional extra hint pushes the
	// entire cluster (including "clear queue") off a narrow terminal. The
	// caller retries with this set when the full cluster overflows, which
	// makes the mouse hint the first thing shed rather than the last.
	SuppressMouseHint bool
	// SuppressBrowseHint drops the Esc browse hint, the second thing shed
	// (after the mouse hint) when the cluster does not fit.
	SuppressBrowseHint bool
	// RunActive is true when a subagent is running, so the Ctrl+F drill-in
	// hint is actionable.
	RunActive bool
	// SkillGatePending is true while a skill-load gate dialog is up, so
	// the footer shows the gate's navigation keys instead of the chat keys.
	SkillGatePending bool
	// Browsing is true in browse mode (Esc): the footer shows its keys.
	Browsing bool
	// SendNowAvailable is true when the runtime reports a supported, active
	// turn the user can interrupt. It is what separates the two actions the
	// busy footer offers: Enter queues without interrupting, and Alt+Enter
	// interrupts the current operation and steers the same turn. Advertising
	// the second action when the runtime cannot honour it would promise an
	// interruption that never happens, so the hint is gated on it.
	SendNowAvailable bool
	// StopOnly is true while busy, when the footer is only the stop hint. A
	// narrow terminal cannot carry the whole busy cluster, and of everything
	// in it "stop" is the one key a stuck user needs; the caller sets this
	// when the full cluster does not fit.
	StopOnly bool
	// KeysInFrame is true when the pending prompt's keys are drawn in its
	// console header instead of the footer, so the footer keeps only the one
	// key the header cannot carry: stopping the turn.
	KeysInFrame bool
}

var keyStyle = lipgloss.NewStyle().Bold(true)
var sep = lipgloss.NewStyle().Faint(true).SetString(" · ")

func pair(k, label string) string { return keyStyle.Render(k) + " " + label }

// ctrlXPair renders the one Ctrl+X hint the caller asked for, or "" when the
// key has no action here. Every branch that shows it goes through this
// function, so the footer can never emit two labels for the same chord.
func ctrlXPair(h CtrlXHint) string {
	switch h {
	case CtrlXHintStopAgent:
		return pair("Ctrl+X", "stop agent")
	case CtrlXHintClearQueue:
		return pair("Ctrl+X", "clear queue")
	}
	return ""
}

// PromptKeys returns the key pairs the footer shows for a pending question,
// skill gate or approval, joined the way the footer joins them. It is empty
// when none of those prompts is pending, and while a command is being edited
// (where the approval keys do not apply). The console draws the same string in
// the prompt section's header.
func PromptKeys(h FooterHints) string {
	var segs []string
	switch {
	case h.QuestionPending:
		segs = append(segs, pair("Enter", "answer"), pair("Esc", "skip remaining"))
	case h.SkillGatePending:
		segs = append(segs, pair("↑↓", "choose"), pair("1-4", "jump"), pair("Enter", "confirm"))
	case h.ApprovalPending && !h.EditingCommand:
		// One hint per key: this used to emit Enter twice, labelled "arm" and
		// "submit", in the same row. Both gestures are two presses, and the
		// labels say so — a bare "Enter confirm" read as a single press that
		// appeared to do nothing.
		segs = append(segs,
			pair("←→", "choose"),
			pair("Enter", "twice confirm"),
			pair("Esc", "twice deny"),
		)
	default:
		return ""
	}
	if h.SendNowAvailable {
		segs = append(segs, pair("Alt+Enter", "send now"))
	}
	return strings.Join(segs, sep.Render(""))
}

// Footer returns the single-row keybinding bar.
func Footer(h FooterHints) string {
	// ? is only appended when it actually triggers /help (which prints the
	// cheatsheet to the transcript). During approval/question/skill-gate/
	// popup/edit forms ? is consumed by the form itself.
	showHelpHint := !h.QuestionPending && !h.ApprovalPending && !h.PopupOpen && !h.EditingCommand && !h.SkillGatePending

	// The mouse hint is only actionable while idle, but it is appended after
	// every other idle hint rather than first: the footer is truncated to the
	// frame width, and a mouse-mode reminder is the cheapest hint to lose.
	showMouseHint := false

	var segs []string
	if h.StopOnly && h.Busy {
		// Busy on a terminal too narrow for the whole cluster: the single key
		// that unsticks a turn is the only hint worth the space.
		return pair("Ctrl+C", "stop")
	}
	if h.KeysInFrame && PromptKeys(h) != "" {
		// The prompt's keys ride in its console header; the footer keeps only
		// the key that stops the turn, and nothing is actionable while idle.
		if h.Busy {
			return pair("Ctrl+C", "stop")
		}
		return ""
	}
	if h.Browsing && !h.QuestionPending && !h.SkillGatePending && !h.ApprovalPending {
		segs = append(segs,
			pair("j/k", "move"), pair("↵", "detail"), pair("i", "inspect"),
			pair("y", "copy"), pair("o", "open"), pair("esc", "back"))
		// Browse mode lets Ctrl chords through to the global handler, so the
		// one real Ctrl+X action is still actionable here.
		if p := ctrlXPair(h.CtrlXHint); p != "" {
			segs = append(segs, p)
		}
		return strings.Join(segs, sep.Render(""))
	} else if h.QuestionPending {
		segs = append(segs, PromptKeys(h))
	} else if h.SkillGatePending {
		segs = append(segs, PromptKeys(h))
	} else if h.ApprovalPending && !h.EditingCommand {
		segs = append(segs, PromptKeys(h))
	} else if h.EditingCommand {
		segs = append(segs, pair("Enter", "save"), pair("Esc", "cancel edit"))
	} else if h.PopupOpen {
		segs = append(segs, pair("↑↓", "choose"), pair("Tab/Enter", "accept"), pair("Esc", "dismiss"))
	} else if h.Busy {
		// Enter queues; Alt+Enter interrupts. The two are different promises,
		// so the labels must be too: "send" for Enter would read as the
		// interruption the second key exists to perform.
		segs = append(segs,
			pair("Enter", "queue"),
			pair("Shift+Enter", "newline"),
			pair("Ctrl+C", "stop"),
		)
		// One Ctrl+X hint, naming what the global handler will do with the
		// key right now: never both labels, and none when a prompt or panel
		// owns the key.
		if p := ctrlXPair(h.CtrlXHint); p != "" {
			segs = append(segs, p)
		}
		if h.SendNowAvailable {
			segs = append(segs, pair("Alt+Enter", "send now"))
		}
		if h.RunActive {
			segs = append(segs, pair("Ctrl+F", "inspect agent"))
		}
	} else {
		segs = append(segs,
			pair("Tab", "mode"),
			pair("/", "cmd"),
		)
		if !h.SuppressBrowseHint {
			segs = append(segs, pair("Esc", "browse"))
		}
		if h.IdleRollbackEligible {
			segs = append(segs, pair("Ctrl+R", "rollback"))
		}
		segs = append(segs, pair("Ctrl+B", "session"))
		showMouseHint = true
	}
	if showMouseHint && !h.SuppressMouseHint {
		if h.MouseReleased {
			segs = append(segs, pair("Ctrl+S", "scroll"))
		} else {
			segs = append(segs, pair("drag", "copy"))
		}
	}
	if showHelpHint {
		segs = append(segs, pair("?", "help"))
	}
	sepStr := sep.Render("")
	return strings.Join(segs, sepStr)
}
