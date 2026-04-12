// Package commands parses slash-command input and dispatches to built-in
// actions or user-defined skill triggers.
package commands

import (
	"strings"

	"github.com/alec/marshal/internal/skills"
)

// Kind classifies a dispatched action.
type Kind int

const (
	// KindBuiltin is a built-in command (/ship, /undo, /revert, /skills, /help).
	KindBuiltin Kind = iota
	// KindSkill is a user-defined skill trigger found in the registry.
	KindSkill
	// KindUnknown is a /command that is not recognised.
	KindUnknown
)

// Action is the result of dispatching a slash command.
type Action struct {
	Kind Kind
	// Name is the built-in command name ("ship", "undo", etc.) for KindBuiltin,
	// or the unrecognised trigger text for KindUnknown.
	Name string
	// Arg is the first argument after the trigger (used by /revert <id>).
	Arg string
	// Skill is set for KindSkill.
	Skill *skills.Skill
	// Prompt is the remaining text after the trigger, trimmed.  For skill
	// commands this is the task text to pass to the engine.
	Prompt string
}

// Dispatch parses input as a slash command.
// Returns (action, true) when input starts with '/'.
// Returns (Action{}, false) otherwise.
func Dispatch(input string, reg *skills.Registry) (Action, bool) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return Action{}, false
	}

	// Split into trigger word and remainder.
	parts := strings.SplitN(input, " ", 2)
	trigger := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = strings.TrimSpace(parts[1])
	}

	// Built-ins take priority over skills so they cannot be shadowed.
	switch trigger {
	case "/ship":
		return Action{Kind: KindBuiltin, Name: "ship", Prompt: rest}, true
	case "/undo":
		return Action{Kind: KindBuiltin, Name: "undo"}, true
	case "/revert":
		return Action{Kind: KindBuiltin, Name: "revert", Arg: rest}, true
	case "/history":
		return Action{Kind: KindBuiltin, Name: "history"}, true
	case "/skills":
		return Action{Kind: KindBuiltin, Name: "skills"}, true
	case "/help":
		return Action{Kind: KindBuiltin, Name: "help"}, true
	}

	// Skill registry lookup.
	if s, ok := reg.Find(trigger); ok {
		return Action{Kind: KindSkill, Skill: s, Prompt: rest}, true
	}

	return Action{Kind: KindUnknown, Name: trigger, Prompt: rest}, true
}
