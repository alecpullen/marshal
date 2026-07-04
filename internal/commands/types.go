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
	Handler     Handler
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
	if cmd.Handler == nil {
		return fmt.Errorf("%w: handler is required for %q", ErrInvalidCommand, name)
	}
	if _, exists := r.commands[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateCommand, name)
	}
	r.commands[name] = cmd
	return nil
}

func (r *Registry) Lookup(name string) (Command, bool) {
	cmd, ok := r.commands[name]
	return cmd, ok
}

func (r *Registry) List() []Command {
	cmds := make([]Command, 0, len(r.commands))
	for _, cmd := range r.commands {
		cmds = append(cmds, cmd)
	}
	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].Name < cmds[j].Name
	})
	return cmds
}
