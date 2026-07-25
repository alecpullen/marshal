package sdd

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// yamlFenceRe matches the opening/closing ```yaml ... ``` fence.
var yamlFenceRe = regexp.MustCompile("(?s)```yaml\\n(.*?)```")

// ParseSpec reads a spec.md file, extracts the YAML frontmatter (via
// ParseSpecFrontmatter), then parses the fenced ```yaml block containing
// the tasks: list. Each task entry has id, title, deps, files, acceptance.
// Returns a DAG with SpecPath set.
func ParseSpec(specPath string) (*DAG, error) {
	data, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("sdd spec: read %s: %w", specPath, err)
	}
	content := string(data)
	dag := &DAG{SpecPath: specPath}

	// Validate the YAML frontmatter via P1's ParseSpecFrontmatter.
	if _, err := ParseSpecFrontmatter(content); err != nil {
		return nil, fmt.Errorf("sdd spec: parse frontmatter: %w", err)
	}

	// Extract the fenced ```yaml block.
	m := yamlFenceRe.FindStringSubmatch(content)
	if m == nil {
		// No tasks block — return empty DAG (the frontmatter is still valid).
		return dag, nil
	}
	yamlBody := m[1]
	dag.Tasks = parseYAMLTasks(yamlBody)
	return dag, nil
}

// parseYAMLTasks parses the `tasks:` list from the YAML body. Each entry is
// `- id: T1` followed by indented `title:`, `deps:`, `files:`, `acceptance:`
// lines. This is a minimal line-oriented parser (not full YAML) because the
// spec's task block is a fixed, simple shape.
func parseYAMLTasks(body string) []DAGTask {
	var tasks []DAGTask
	var current *DAGTask
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" || strings.TrimSpace(line) == "tasks:" {
			continue
		}
		// A new task starts with "- id:".
		if strings.HasPrefix(strings.TrimSpace(line), "- id:") {
			if current != nil {
				tasks = append(tasks, *current)
			}
			current = &DAGTask{Status: TaskPending}
			_, v, _ := splitKV(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- ")))
			current.ID = strings.TrimSpace(v)
			continue
		}
		if current == nil {
			continue
		}
		key, val, ok := splitKV(strings.TrimSpace(line))
		if !ok {
			continue
		}
		switch key {
		case "title":
			current.Title = strings.TrimSpace(val)
		case "deps":
			current.Deps = parseBracketList(val)
		case "files":
			current.Files = parseBracketList(val)
		case "acceptance":
			current.Acceptance = parseBracketList(val)
		}
	}
	if current != nil {
		tasks = append(tasks, *current)
	}
	return tasks
}
