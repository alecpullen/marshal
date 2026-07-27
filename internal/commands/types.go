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

type Handler func(state *session.State, args []string) Result

// Result is a command's output: plain Text for the transcript, or a
// structured Doc the TUI renders as a scrollable panel.
type Result struct {
	Text string
	Doc  *Doc
}

// Text builds a plain-text Result.
func Text(s string) Result { return Result{Text: s} }

// Panel builds a structured Result. fullFrame requests the full-frame dock
// budget (transcript hidden); see dock.Sizing.
func Panel(title string, fullFrame bool, rows []Row) Result {
	return Result{Doc: &Doc{Title: title, FullFrame: fullFrame, Rows: rows}}
}

// Doc is structured panel content. Content generation lives here, in
// commands; rendering lives in the TUI — this package must not import
// lipgloss.
type Doc struct {
	Title     string
	FullFrame bool
	Rows      []Row
	// Footer is muted explanatory text rendered under the rows.
	Footer string
}

// Row is one entry in a Doc. Exactly one shape applies:
//   - Header set: a section header row.
//   - Action set: Enter runs the action (ActionLabel is the right cell).
//   - Children set: Enter drills one level into the child rows.
//   - otherwise: a read-only title/detail row; Desc describes it.
type Row struct {
	Header      string
	Text        string
	Detail      string
	Desc        string
	ActionLabel string
	Action      func(state *session.State) Result
	Children    []Row
}

// PlainText renders the result as flat text for headless callers.
func (r Result) PlainText() string {
	if r.Doc == nil {
		return r.Text
	}
	var b strings.Builder
	if r.Doc.Title != "" {
		b.WriteString(r.Doc.Title + "\n")
	}
	var walk func(rows []Row, indent string)
	walk = func(rows []Row, indent string) {
		for _, row := range rows {
			switch {
			case row.Header != "":
				b.WriteString(indent + row.Header + "\n")
			default:
				line := indent + row.Text
				if row.Detail != "" {
					line += "  " + row.Detail
				}
				b.WriteString(line + "\n")
			}
			if len(row.Children) > 0 {
				walk(row.Children, indent+"  ")
			}
		}
	}
	walk(r.Doc.Rows, "  ")
	if r.Doc.Footer != "" {
		b.WriteString(r.Doc.Footer + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

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
	Handler    Handler
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
