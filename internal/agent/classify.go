package agent

import "strings"

// editKeywords are checked before commandKeywords so a goal like "fix the
// tests that run too slowly" classifies as an edit, not a command — editing
// is the higher-commitment action and should win ties.
var editKeywords = []string{
	"fix", "add", "implement", "refactor", "update", "change",
	"remove", "delete", "rename", "write", "create", "patch",
}

var commandKeywords = []string{
	"run", "test", "build", "execute", "install", "lint",
}

func Classify(goal string) TaskClass {
	lower := strings.ToLower(goal)
	for _, kw := range editKeywords {
		if strings.Contains(lower, kw) {
			return ClassEdit
		}
	}
	for _, kw := range commandKeywords {
		if strings.Contains(lower, kw) {
			return ClassCommand
		}
	}
	return ClassQuestion
}
