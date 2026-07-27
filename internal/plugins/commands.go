package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"marshal/internal/skills"
)

// CommandSpec is a user-defined slash command parsed from a plugin's
// commands/<name>.md file. Body is the prompt submitted when the command
// runs. The plugins package deliberately does not import
// internal/commands — the app layer converts specs into registered
// commands.
type CommandSpec struct {
	Name        string
	Description string
	Body        string
}

var commandNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// parseCommandSpecs reads commands/*.md under dir. Files use the same
// TOML frontmatter as skills (name, description); the body is the prompt
// text. Malformed files, names that don't lowercase to
// ^[a-z0-9][a-z0-9-]*$, and empty bodies are skipped, matching the skill
// loader's philosophy. A missing commands/ directory yields nil, nil.
func parseCommandSpecs(dir string) ([]CommandSpec, error) {
	commandsDir := filepath.Join(dir, "commands")
	entries, err := os.ReadDir(commandsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read commands directory: %w", err)
	}
	var specs []CommandSpec
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(commandsDir, entry.Name()))
		if err != nil {
			continue
		}
		parsed, err := skills.Parse(string(raw))
		if err != nil {
			continue
		}
		name := strings.ToLower(parsed.Name)
		if !commandNameRE.MatchString(name) || parsed.Body == "" {
			continue
		}
		specs = append(specs, CommandSpec{Name: name, Description: parsed.Description, Body: parsed.Body})
	}
	return specs, nil
}
