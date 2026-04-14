// Package commands parses slash-command input and dispatches to built-in
// actions or user-defined skill triggers.
package commands

import (
	"strings"

	"github.com/alecpullen/marshal/internal/skills"
)

// Kind classifies a dispatched action.
type Kind int

const (
	// KindBuiltin is a built-in command (/ship, /undo, /revert, /skills, /help, etc.).
	KindBuiltin Kind = iota
	// KindSkill is a user-defined skill trigger found in the registry.
	KindSkill
	// KindUnknown is a /command that is not recognised.
	KindUnknown
)

// Command names for all built-in slash commands.
const (
	CmdShip             = "ship"
	CmdUndo             = "undo"
	CmdRevert           = "revert"
	CmdHistory          = "history"
	CmdSkills           = "skills"
	CmdHelp             = "help"
	CmdCommit           = "commit"
	CmdTokens           = "tokens"
	CmdRun              = "run"
	CmdTest             = "test"
	CmdGit              = "git"
	CmdMap              = "map"
	CmdMapRefresh       = "map-refresh"
	CmdSettings         = "settings"
	CmdWeb              = "web"
	CmdPaste            = "paste"
	CmdReadOnly         = "read-only"
	CmdReset            = "reset"
	CmdSave             = "save"
	CmdLoad             = "load"
	CmdCopy             = "copy"
	CmdCopyContext      = "copy-context"
	CmdEditor           = "editor"
	CmdEdit             = "edit"
	CmdThinkTokens      = "think-tokens"
	CmdReasoningEffort = "reasoning-effort"
	CmdMultilineMode    = "multiline-mode"
	CmdReport           = "report"
	CmdLint             = "lint"
	CmdAdd              = "add"
	CmdDrop             = "drop"
	CmdLs               = "ls"
	CmdDiff             = "diff"
	CmdQuit             = "quit"
	CmdClear            = "clear"
	CmdModel            = "model"
	CmdTask             = "task"
	CmdDiscard          = "discard"
	CmdSession          = "session"
	CmdVoice            = "voice"
	CmdWatch            = "watch"
	CmdUnwatch          = "unwatch"
)

// Action is the result of dispatching a slash command.
type Action struct {
	Kind Kind
	// Name is the built-in command name ("ship", "undo", etc.) for KindBuiltin,
	// or the unrecognised trigger text for KindUnknown.
	Name string
	// Arg is the first argument after the trigger (used by /revert <id>, /add <file>, etc.).
	Arg string
	// Args is all arguments split by spaces for commands that take multiple args.
	Args []string
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

	// Parse args for commands that need them.
	var args []string
	if rest != "" {
		args = strings.Fields(rest)
	}

	// Built-ins take priority over skills so they cannot be shadowed.
	switch trigger {
	// Core git workflow commands
	case "/ship":
		return Action{Kind: KindBuiltin, Name: CmdShip, Prompt: rest}, true
	case "/undo":
		return Action{Kind: KindBuiltin, Name: CmdUndo}, true
	case "/revert":
		return Action{Kind: KindBuiltin, Name: CmdRevert, Arg: rest, Args: args}, true
	case "/commit":
		return Action{Kind: KindBuiltin, Name: CmdCommit, Prompt: rest}, true

	// Info/display commands
	case "/history":
		return Action{Kind: KindBuiltin, Name: CmdHistory}, true
	case "/skills":
		return Action{Kind: KindBuiltin, Name: CmdSkills}, true
	case "/help":
		return Action{Kind: KindBuiltin, Name: CmdHelp}, true
	case "/tokens":
		return Action{Kind: KindBuiltin, Name: CmdTokens}, true
	case "/map":
		return Action{Kind: KindBuiltin, Name: CmdMap}, true
	case "/map-refresh":
		return Action{Kind: KindBuiltin, Name: CmdMapRefresh}, true
	case "/settings":
		return Action{Kind: KindBuiltin, Name: CmdSettings}, true
	case "/report":
		return Action{Kind: KindBuiltin, Name: CmdReport}, true

	// File management commands
	case "/add":
		return Action{Kind: KindBuiltin, Name: CmdAdd, Arg: rest, Args: args}, true
	case "/drop":
		return Action{Kind: KindBuiltin, Name: CmdDrop, Arg: rest, Args: args}, true
	case "/ls":
		return Action{Kind: KindBuiltin, Name: CmdLs}, true
	case "/diff":
		return Action{Kind: KindBuiltin, Name: CmdDiff}, true
	case "/read-only":
		return Action{Kind: KindBuiltin, Name: CmdReadOnly, Arg: rest, Args: args}, true

	// Shell/execution commands
	case "/run":
		return Action{Kind: KindBuiltin, Name: CmdRun, Prompt: rest}, true
	case "/test":
		return Action{Kind: KindBuiltin, Name: CmdTest, Prompt: rest}, true
	case "/git":
		return Action{Kind: KindBuiltin, Name: CmdGit, Prompt: rest}, true

	// External integration commands
	case "/web":
		return Action{Kind: KindBuiltin, Name: CmdWeb, Arg: rest}, true
	case "/paste":
		return Action{Kind: KindBuiltin, Name: CmdPaste}, true
	case "/voice":
		return Action{Kind: KindBuiltin, Name: CmdVoice}, true
	case "/watch":
		return Action{Kind: KindBuiltin, Name: CmdWatch, Arg: rest}, true
	case "/unwatch":
		return Action{Kind: KindBuiltin, Name: CmdUnwatch}, true

	// Session/state commands
	case "/save":
		return Action{Kind: KindBuiltin, Name: CmdSave, Arg: rest}, true
	case "/load":
		return Action{Kind: KindBuiltin, Name: CmdLoad, Arg: rest}, true
	case "/reset":
		return Action{Kind: KindBuiltin, Name: CmdReset}, true
	case "/clear":
		return Action{Kind: KindBuiltin, Name: CmdClear}, true
	case "/discard":
		return Action{Kind: KindBuiltin, Name: CmdDiscard}, true
	case "/session":
		return Action{Kind: KindBuiltin, Name: CmdSession}, true
	case "/task":
		return Action{Kind: KindBuiltin, Name: CmdTask, Prompt: rest}, true
	case "/quit":
		return Action{Kind: KindBuiltin, Name: CmdQuit}, true

	// Editor/context commands
	case "/copy":
		return Action{Kind: KindBuiltin, Name: CmdCopy, Arg: rest}, true
	case "/copy-context":
		return Action{Kind: KindBuiltin, Name: CmdCopyContext}, true
	case "/editor":
		return Action{Kind: KindBuiltin, Name: CmdEditor, Prompt: rest}, true
	case "/edit":
		return Action{Kind: KindBuiltin, Name: CmdEdit, Prompt: rest}, true

	// Model/configuration commands
	case "/model":
		return Action{Kind: KindBuiltin, Name: CmdModel, Arg: rest, Args: args}, true
	case "/think-tokens":
		return Action{Kind: KindBuiltin, Name: CmdThinkTokens, Arg: rest}, true
	case "/reasoning-effort":
		return Action{Kind: KindBuiltin, Name: CmdReasoningEffort, Arg: rest}, true
	case "/multiline-mode":
		return Action{Kind: KindBuiltin, Name: CmdMultilineMode}, true
	case "/lint":
		return Action{Kind: KindBuiltin, Name: CmdLint, Prompt: rest}, true
	}

	// Skill registry lookup.
	if s, ok := reg.Find(trigger); ok {
		return Action{Kind: KindSkill, Skill: s, Prompt: rest}, true
	}

	return Action{Kind: KindUnknown, Name: trigger, Prompt: rest}, true
}
