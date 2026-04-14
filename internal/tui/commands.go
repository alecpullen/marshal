// internal/tui/commands.go
// Vim-style `:command` registry, parser, and tab-completer.

package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/alecpullen/marshal/internal/config"
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
	{Name: "models", Aliases: []string{"m"}, Help: "list available Ollama models"},
	{Name: "pull", Aliases: []string{}, Help: "pull an Ollama model (:pull <model>)"},
	{Name: "setmodel", Aliases: []string{"sm"}, Help: "set model for a role (:setmodel <role> <model>)"},
}

// parseCommand strips the leading ":", resolves aliases, and returns the
// canonical command name plus any trailing arguments. Returns an error if the
// command token is unknown. Supports custom commands from config.
func parseCommand(raw string, cfg *config.Config) (name string, args []string, cmdType string, err error) {
	s := strings.TrimPrefix(raw, ":")
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil, "", fmt.Errorf("empty command")
	}

	parts := strings.Fields(s)
	token := parts[0]
	rest := parts[1:]

	// Check custom commands first (user overrides)
	if cfg != nil {
		if customCmd, ok := cfg.Commands[token]; ok {
			return token, rest, customCmd.Action, nil
		}
	}

	// Check built-in registry
	for _, c := range registry {
		if token == c.Name {
			return c.Name, rest, "builtin", nil
		}
		for _, a := range c.Aliases {
			if token == a {
				return c.Name, rest, "builtin", nil
			}
		}
	}

	// Build "did you mean?" suggestion
	matches := FindCompletions(token, cfg)
	suggestion := ""
	if len(matches) > 0 {
		suggestion = GetCompletionSuggestions(token, matches, 3)
	}

	if suggestion != "" {
		return "", nil, "", fmt.Errorf("unknown command: %q — did you mean: %s? (type :help for list)", token, suggestion)
	}
	return "", nil, "", fmt.Errorf("unknown command: %q — type :help for a list", token)
}

// completeCommand returns the longest unambiguous completion for a partial
// command token (without the leading ":"). exact is true when only one
// candidate matches. Returns "" if there are no matches or the input is empty.
// This is the legacy interface - prefer FindCompletions for new code.
func completeCommand(partial string) (suggestion string, exact bool) {
	if partial == "" {
		return "", false
	}

	matches := FindCompletions(partial, nil) // nil config = built-ins only
	ghost := GetGhostSuggestion(partial, matches)

	if ghost == "" {
		return "", len(matches) == 1
	}
	return partial + ghost, len(matches) == 1
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

// CompletionMatch represents a single command match for completion UI.
type CompletionMatch struct {
	Name      string   // canonical name
	Aliases   []string // aliases
	Help      string   // description
	IsBuiltin bool     // true if built-in command
	IsCustom  bool     // true if user-defined custom command
	Action    string   // "builtin", "shell", "view" (for custom commands)
}

// FindCompletions returns all commands (built-in + custom) matching the partial input.
// Results are sorted: exact matches first, then built-ins, then custom commands.
func FindCompletions(partial string, cfg *config.Config) []CompletionMatch {
	if partial == "" {
		return nil
	}

	var matches []CompletionMatch
	seen := make(map[string]bool) // track which canonical names we've added
	lowerPartial := strings.ToLower(partial)

	// Search built-in registry
	for _, c := range registry {
		match := CompletionMatch{
			Name:      c.Name,
			Aliases:   c.Aliases,
			Help:      c.Help,
			IsBuiltin: true,
			Action:    "builtin",
		}

		// Check if name matches
		if strings.HasPrefix(strings.ToLower(c.Name), lowerPartial) {
			if !seen[c.Name] {
				match.Name = c.Name // use canonical name
				matches = append(matches, match)
				seen[c.Name] = true
			}
			continue
		}

		// Check if any alias matches
		for _, alias := range c.Aliases {
			if strings.HasPrefix(strings.ToLower(alias), lowerPartial) {
				if !seen[c.Name] {
					matches = append(matches, match)
					seen[c.Name] = true
				}
				break
			}
		}
	}

	// Search custom commands from config (user commands override built-ins)
	if cfg != nil {
		for name, cmd := range cfg.Commands {
			if strings.HasPrefix(strings.ToLower(name), lowerPartial) {
				// Check if this overrides a built-in
				overrides := false
				for _, m := range matches {
					if m.Name == name {
						overrides = true
						break
					}
				}

				match := CompletionMatch{
					Name:      name,
					Help:      cmd.Help,
					IsBuiltin: false,
					IsCustom:  true,
					Action:    cmd.Action,
				}

				if overrides {
					// Replace the built-in match with custom
					for i, m := range matches {
						if m.Name == name {
							matches[i] = match
							break
						}
					}
				} else {
					matches = append(matches, match)
				}
			}
		}
	}

	// Sort: exact matches first, then alphabetically
	sort.Slice(matches, func(i, j int) bool {
		nameI := strings.ToLower(matches[i].Name)
		nameJ := strings.ToLower(matches[j].Name)

		// Exact match goes first
		if nameI == lowerPartial && nameJ != lowerPartial {
			return true
		}
		if nameJ == lowerPartial && nameI != lowerPartial {
			return false
		}

		// Then sort alphabetically
		return nameI < nameJ
	})

	return matches
}

// GetGhostSuggestion returns the inline ghost text suggestion for a partial input.
// Returns the full command name if single match, common prefix if multiple, or "" if none.
func GetGhostSuggestion(partial string, matches []CompletionMatch) string {
	if len(matches) == 0 {
		return ""
	}

	if len(matches) == 1 {
		// Single match: suggest the full canonical name
		suggestion := matches[0].Name
		if len([]rune(suggestion)) > len([]rune(partial)) {
			return suggestion[len([]rune(partial)):]
		}
		return ""
	}

	// Multiple matches: find longest common prefix of all names
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

	// Only suggest if prefix extends beyond current input
	if len([]rune(prefix)) > len([]rune(partial)) {
		return prefix[len([]rune(partial)):]
	}
	return ""
}

// GetCompletionSuggestions returns a formatted list of suggestions for "did you mean?" messages.
func GetCompletionSuggestions(partial string, matches []CompletionMatch, max int) string {
	if len(matches) == 0 {
		return ""
	}

	var names []string
	seen := make(map[string]bool)
	for _, m := range matches {
		if !seen[m.Name] && len(names) < max {
			names = append(names, m.Name)
			seen[m.Name] = true
		}
	}

	if len(names) == 0 {
		return ""
	}

	return strings.Join(names, ", ")
}
