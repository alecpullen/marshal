package tui

import "strings"

var toolDisplayNames = map[string]string{
	"file.read":        "Read file",
	"file.write_patch": "Edit file",
	"patch.apply":      "Apply patch",
	"shell.run":        "Run command",
	"test.run":         "Run tests",
	"repo.search":      "Search repo",
	"codebase.search":  "Codebase search",
	"web.fetch":        "Fetch page",
	"web.search":       "Search web",
	"git.status":       "Git status",
	"git.diff":         "Git diff",
	"symbols.find":     "Find symbols",
	"todos":            "Update todos",
	"mode.request":     "Switch mode",
	"question.ask":     "Ask question",
	"ask_user":         "Ask user",
	"agent.run":        "Run subagent",
}

// DisplayToolName returns a human-readable label for a tool. Unknown tools
// get a title-cased, dot-to-space transformation.
func DisplayToolName(name string) string {
	if label, ok := toolDisplayNames[name]; ok {
		return label
	}
	parts := strings.Split(name, ".")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}
