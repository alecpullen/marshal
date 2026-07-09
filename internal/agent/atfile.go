package agent

import (
	"os"
	"path/filepath"
	"regexp"

	"marshal/internal/app/session"
	"marshal/internal/contextpack"
)

// atFileRe matches @path tokens anywhere in the user goal. The leading
// boundary (start-of-string or whitespace) ensures "@" inside an email
// address (e.g. "user@example.com") or after another character is
// ignored. The path itself is captured as a non-whitespace run so paths
// with hyphens, dots, slashes, and underscores all match.
var atFileRe = regexp.MustCompile(`(?:^|\s)@(\S+)`)

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
		content, err := os.ReadFile(filepath.Join(workingDir, path))
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
