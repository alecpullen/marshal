package agent

import (
	"os"
	"regexp"

	"marshal/internal/app/session"
	"marshal/internal/contextpack"
)

// atFileRe matches @path tokens anywhere in the user goal. The
// leading boundary (start-of-string or whitespace) ensures "@" inside
// an email address (e.g. "user@example.com") is ignored. The path
// itself is captured as a conservative [A-Za-z0-9._/-]+ run so shell
// metacharacters, "..", and empty paths are excluded at the regex
// level. Path safety is then enforced again at read time via
// safeWorkspacePath so a crafted path cannot escape the workspace.
var atFileRe = regexp.MustCompile(`(?:^|\s)@([A-Za-z0-9._/\-]+)`)

// extractPinnedFiles scans the goal for @path tokens, resolves each
// against the repo file index for the project's database, and returns
// the matching files' content as FileSnippets. Unknown paths and
// unreadable files are silently skipped (rather than erroring the turn)
// so a stray @reference in the user's input never blocks execution.
func extractPinnedFiles(goal string, state *session.State, projectID int64) []contextpack.FileSnippet {
	if state == nil {
		return nil
	}
	matches := atFileRe.FindAllStringSubmatch(goal, -1)
	if len(matches) == 0 {
		return nil
	}

	db := state.DB()
	if db == nil {
		return nil
	}
	index, err := db.GetFileIndex(projectID)
	if err != nil {
		return nil
	}
	known := make(map[string]struct{}, len(index))
	for _, f := range index {
		known[f.Path] = struct{}{}
	}

	workingDir := state.WorkingDir
	seen := make(map[string]struct{}, len(matches))
	var out []contextpack.FileSnippet
	for _, m := range matches {
		path := m[1]
		if path == "" {
			continue
		}
		if _, ok := known[path]; !ok {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		// Defensive containment check — even with the tightened regex,
		// a path like "valid/../../../etc" could still escape via ".."
		// within the allowed character class. safeWorkspacePath rejects
		// any path that resolves outside the working directory.
		abs, err := safeWorkspacePath(workingDir, path)
		if err != nil {
			continue
		}
		content, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		out = append(out, contextpack.FileSnippet{
			Path:    path,
			Content: string(content),
		})
	}
	return out
}
