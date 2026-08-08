package app

import (
	"os"
	"path/filepath"
	"strings"
)

// instructionFileCandidates is the priority order for repo-level agent
// instruction files. The first present file wins; later files are not read
// so overlapping guidance is not duplicated in the system prompt.
var instructionFileCandidates = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"GEMINI.md",
	".cursorrules",
}

// loadRepoInstructions reads the first present instruction file from
// workingDir (per instructionFileCandidates) and returns its trimmed
// content. Returns ("", false) when none exist — a missing file is not an
// error, it just means no repo-level addendum applies.
func loadRepoInstructions(workingDir string) (string, bool) {
	for _, name := range instructionFileCandidates {
		path := filepath.Join(workingDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if content := strings.TrimSpace(string(data)); content != "" {
			return content, true
		}
	}
	return "", false
}

// composeAddendum prepends repo instructions to a custom-agent addendum so
// repo-level guidance is always present and the custom agent's prompt gets
// the final word. Either or both may be empty.
func composeAddendum(repoInstructions, customAddendum string) string {
	repoInstructions = strings.TrimSpace(repoInstructions)
	customAddendum = strings.TrimSpace(customAddendum)
	switch {
	case repoInstructions == "" && customAddendum == "":
		return ""
	case repoInstructions == "":
		return customAddendum
	case customAddendum == "":
		return repoInstructions
	default:
		var b strings.Builder
		b.WriteString("## Repository Instructions\n\n")
		b.WriteString(repoInstructions)
		b.WriteString("\n\n## Agent Instructions\n\n")
		b.WriteString(customAddendum)
		return b.String()
	}
}
