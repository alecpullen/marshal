package commands

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"marshal/internal/app/session"
)

var (
	ErrInvalidCommand   = errors.New("invalid command")
	ErrDuplicateCommand = errors.New("duplicate command")
)

type Handler func(state *session.State, args []string) string

type Command struct {
	Name        string
	Description string
	Args        string // human-readable, e.g. "<model-name>" or "" for no args
	Group       string // group heading in /help listing, e.g. "Chat", "Models & providers"
	Hidden      bool   // when true, excluded from /help listing
	// TUIOnly marks a command whose logic is interactive and lives in the
	// TUI dispatch table (internal/app/tui/commands_dispatch.go). Such
	// commands carry no Handler; headless callers should treat them as
	// unavailable.
	TUIOnly bool
	// PromptBody, when non-empty, makes this a prompt command: the TUI
	// submits PromptBody as a user turn (steering when the agent is busy)
	// instead of invoking a Go handler. Used for plugin-defined commands;
	// such commands carry no Handler and are inert headless.
	PromptBody string
	Handler Handler
}

type Registry struct {
	commands map[string]Command
}

func New() *Registry {
	return &Registry{commands: make(map[string]Command)}
}

func (r *Registry) Register(cmd Command) error {
	name := strings.TrimSpace(cmd.Name)
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidCommand)
	}
	if cmd.Handler == nil && !cmd.TUIOnly && cmd.PromptBody == "" {
		return fmt.Errorf("%w: handler is required for %q", ErrInvalidCommand, name)
	}
	if _, exists := r.commands[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateCommand, name)
	}
	r.commands[name] = cmd
	return nil
}

func (r *Registry) Lookup(name string) (Command, bool) {
	cmd, ok := r.commands[strings.ToLower(name)]
	return cmd, ok
}

func (r *Registry) List() []Command {
	cmds := make([]Command, 0, len(r.commands))
	for _, cmd := range r.commands {
		if cmd.Hidden {
			continue
		}
		cmds = append(cmds, cmd)
	}
	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].Name < cmds[j].Name
	})
	return cmds
}

// ListAll returns every registered command, sorted — including Hidden ones.
// Hidden scopes a command out of the /help listing only; type-ahead
// completion should still offer every runnable command.
func (r *Registry) ListAll() []Command {
	cmds := make([]Command, 0, len(r.commands))
	for _, cmd := range r.commands {
		cmds = append(cmds, cmd)
	}
	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].Name < cmds[j].Name
	})
	return cmds
}
