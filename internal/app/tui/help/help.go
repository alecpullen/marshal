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

// Hint priorities. The status line has one row and the terminal has a fixed
// width, so the hint cluster is shed segment by segment rather than as a
// unit: losing "clear queue" to keep a mouse-mode reminder was the wrong
// trade, and the cluster used to be one string that could only be kept whole
// or dropped whole.
const (
	// PriorityEssential hints are the actions the user needs to get out of
	// the state they are in (cancel, confirm, deny, clear the queue).
	PriorityEssential = 0
	// PriorityLikely hints are the actions most users reach for next.
	PriorityLikely = 1
	// PriorityOptional hints are conveniences (panel toggles, mode switches)
	// and are shed first.
	PriorityOptional = 2
)

// Hint is one resolved keybinding hint: a key, the verb it performs, and the
// priority that decides whether it survives a narrow terminal.
type Hint struct {
	Key      string
	Label    string
	Priority int
}

// FooterHints describes which mode-driven hints are currently actionable.
//
// The static composer and form hints live here; the key-driven UI actions
// (Ctrl+X, Ctrl+F, Esc cancel, Ctrl+R, Ctrl+T, Ctrl+B, Ctrl+S) arrive in
// Actions, resolved by the root from the same context snapshot its key
// dispatcher reads. That is what keeps the footer from advertising a key
// whose meaning has moved — most visibly Ctrl+X, which used to be printed
// twice with two different verbs while only one of them ran.
type FooterHints struct {
	Busy            bool
	EditingCommand  bool
	ApprovalPending bool
	QuestionPending bool
	PopupOpen       bool
	// SkillGatePending is true while a skill-load gate dialog is up, so
	// the footer shows the gate's navigation keys instead of the chat keys.
	SkillGatePending bool
	// Actions are the resolved UI actions bound to keys right now, in
	// display order. Unavailable actions have already been filtered out by
	// the resolver.
	Actions []Hint
}

var keyStyle = lipgloss.NewStyle().Bold(true)
var sep = lipgloss.NewStyle().Faint(true).SetString(" · ")

func pair(k, label string) string { return Pair(k, label) }

// Pair renders one key/verb hint pair. It is exported so the status line,
// which assembles the cluster itself in order to shed hints by priority,
// styles rows identically to the plain Footer.
func Pair(k, label string) string { return keyStyle.Render(k) + " " + label }

// Separator is the dim glyph string between hint pairs, exported for the
// same reason as Pair.
func Separator() string { return sep.Render("") }

// FooterParts returns the footer's hints in display order. The status line
// consumes this directly so it can shed one hint at a time by priority; the
// plain Footer below joins the same list for callers without a width budget.
func FooterParts(h FooterHints) []Hint {
	switch {
	case h.QuestionPending:
		return []Hint{
			{Key: "Enter", Label: "answer", Priority: PriorityEssential},
			{Key: "Esc", Label: "skip", Priority: PriorityEssential},
		}
	case h.SkillGatePending:
		return []Hint{
			{Key: "↑↓", Label: "choose", Priority: PriorityEssential},
			{Key: "1-4", Label: "jump", Priority: PriorityLikely},
			{Key: "Enter", Label: "confirm", Priority: PriorityEssential},
		}
	case h.ApprovalPending && !h.EditingCommand:
		// One hint per key: this used to emit Enter twice, labelled "arm" and
		// "submit", in the same row.
		return []Hint{
			{Key: "←→", Label: "choose", Priority: PriorityLikely},
			{Key: "Enter", Label: "confirm", Priority: PriorityEssential},
			{Key: "Esc", Label: "deny", Priority: PriorityEssential},
		}
	case h.EditingCommand:
		return []Hint{
			{Key: "Enter", Label: "save", Priority: PriorityEssential},
			{Key: "Esc", Label: "cancel edit", Priority: PriorityEssential},
		}
	case h.PopupOpen:
		return []Hint{
			{Key: "↑↓", Label: "choose", Priority: PriorityEssential},
			{Key: "Tab/Enter", Label: "accept", Priority: PriorityEssential},
			{Key: "Esc", Label: "dismiss", Priority: PriorityEssential},
		}
	}

	// Composer owns the keys: the static set depends on whether a turn is
	// running, then the resolved action hints follow.
	var out []Hint
	if h.Busy {
		out = append(out,
			Hint{Key: "Enter", Label: "send", Priority: PriorityEssential},
			Hint{Key: "Shift+Enter", Label: "newline", Priority: PriorityOptional},
		)
	} else {
		out = append(out,
			Hint{Key: "Tab", Label: "mode", Priority: PriorityOptional},
			Hint{Key: "/", Label: "cmd", Priority: PriorityOptional},
		)
	}
	out = append(out, h.Actions...)
	// ? only prints the cheatsheet when the input is empty and no form owns
	// the keyboard; here both hold, so the hint is always actionable. It is
	// essential, not optional: losing it was one half of the "essential help
	// lost at 80 columns" bug, and it is the only route to the full
	// keybinding list.
	out = append(out, Hint{Key: "?", Label: "help", Priority: PriorityEssential})
	return out
}

// Footer returns the single-row keybinding bar. It has no width budget: the
// status line's priority shedding is the width-aware path.
func Footer(h FooterHints) string {
	parts := FooterParts(h)
	segs := make([]string, 0, len(parts))
	for _, p := range parts {
		segs = append(segs, pair(p.Key, p.Label))
	}
	return strings.Join(segs, Separator())
}
