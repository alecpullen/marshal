// internal/tui/commands.go
// Vim-style `:command` registry, parser, and tab-completer.

package tui

import (
	"fmt"
	"strings"
)

// cmd describes a single registered command.
type cmd struct {
	Name    string
	Aliases []string
	Help    string
}

var registry = []cmd{
	{Name: "quit", Aliases: []string{"q"}, Help: "exit marshal"},
	{Name: "new", Aliases: []string{"n"}, Help: "open task composer"},
	{Name: "diff", Aliases: []string{"d"}, Help: "open diff viewer for last task"},
	{Name: "config", Aliases: []string{"cfg", "c"}, Help: "open config overlay"},
	{Name: "clear", Aliases: []string{"cl"}, Help: "clear the log panel"},
	{Name: "cancel", Aliases: []string{"x"}, Help: "cancel the running task"},
	{Name: "retry", Aliases: []string{"r"}, Help: "retry the last completed task"},
	{Name: "sessions", Aliases: []string{"ls", "s"}, Help: "browse past sessions"},
	{Name: "help", Aliases: []string{"h", "?"}, Help: "list available commands (or press ?)"},
}

// parseCommand strips the leading ":", resolves aliases, and returns the
// canonical command name plus any trailing arguments. Returns an error if the
// command token is unknown.
func parseCommand(raw string) (name string, args []string, err error) {
	s := strings.TrimPrefix(raw, ":")
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil, fmt.Errorf("empty command")
	}

	parts := strings.Fields(s)
	token := parts[0]
	rest := parts[1:]

	for _, c := range registry {
		if token == c.Name {
			return c.Name, rest, nil
		}
		for _, a := range c.Aliases {
			if token == a {
				return c.Name, rest, nil
			}
		}
	}

	return "", nil, fmt.Errorf("unknown command: %q — type :help for a list", token)
}

// completeCommand returns the longest unambiguous completion for a partial
// command token (without the leading ":"). exact is true when only one
// candidate matches. Returns "" if there are no matches or the input is empty.
func completeCommand(partial string) (suggestion string, exact bool) {
	if partial == "" {
		return "", false
	}

	var matches []cmd
	for _, c := range registry {
		if strings.HasPrefix(c.Name, partial) {
			matches = append(matches, c)
			continue
		}
		for _, a := range c.Aliases {
			if strings.HasPrefix(a, partial) {
				matches = append(matches, c)
				break
			}
		}
	}

	switch len(matches) {
	case 0:
		return "", false
	case 1:
		return matches[0].Name, true
	default:
		// Return the longest common prefix of all candidate names.
		prefix := matches[0].Name
		for _, m := range matches[1:] {
			for i := 0; i < len(prefix) && i < len(m.Name); i++ {
				if prefix[i] != m.Name[i] {
					prefix = prefix[:i]
					break
				}
			}
			if len(m.Name) < len(prefix) {
				prefix = prefix[:len(m.Name)]
			}
		}
		return prefix, false
	}
}

// helpLines returns formatted help text as a slice of strings ready for
// streaming into the log as lineSystem messages.
func helpLines() []string {
	lines := []string{"  commands:"}
	for _, c := range registry {
		aliases := ""
		if len(c.Aliases) > 0 {
			aliases = "  (" + strings.Join(c.Aliases, ", ") + ")"
		}
		lines = append(lines, fmt.Sprintf("    :%s%s — %s", c.Name, aliases, c.Help))
	}
	return lines
}
