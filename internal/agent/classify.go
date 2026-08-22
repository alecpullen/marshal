package agent

import (
	"regexp"
	"strings"
)

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

// keywordPatterns are precompiled word-boundary regexes for each keyword.
// \b ensures "run" doesn't match "runtime" or "running".
var editPatterns = mustWordPatterns(editKeywords)
var commandPatterns = mustWordPatterns(commandKeywords)

func mustWordPatterns(keywords []string) []*regexp.Regexp {
	patterns := make([]*regexp.Regexp, len(keywords))
	for i, kw := range keywords {
		patterns[i] = regexp.MustCompile(`\b` + regexp.QuoteMeta(kw) + `\b`)
	}
	return patterns
}

func Classify(goal string) TaskClass {
	lower := strings.ToLower(goal)
	for _, p := range editPatterns {
		if p.MatchString(lower) {
			return ClassEdit
		}
	}
	for _, p := range commandPatterns {
		if p.MatchString(lower) {
			return ClassCommand
		}
	}
	return ClassQuestion
}
