package agent

import (
	"os"
	"regexp"
	"sync"

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

// pendingFile is a (relative, absolute) path pair that passed the
// file-index membership and workspace-safety checks.
type pendingFile struct {
	rel string
	abs string
}

// extractPinnedFiles scans the goal for @path tokens, resolves each
// against the repo file index (cached on Runner), and returns the
// matching files' content as FileSnippets. Unknown paths and
// unreadable files are silently skipped (rather than erroring the turn)
// so a stray @reference in the user's input never blocks execution.
func extractPinnedFiles(goal string, r *Runner, projectID int64) []contextpack.FileSnippet {
	if r == nil || r.State == nil {
		return nil
	}
	matches := atFileRe.FindAllStringSubmatch(goal, -1)
	if len(matches) == 0 {
		return nil
	}

	// Use cached file index if available.
	known, ok := r.fileIndexCache.get(projectID)
	if !ok {
		db := r.State.DB()
		if db == nil {
			return nil
		}
		index, err := db.GetFileIndex(projectID, 0)
		if err != nil || len(index) == 0 {
			return nil
		}
		known = make(map[string]struct{}, len(index))
		for _, f := range index {
			known[f.Path] = struct{}{}
		}
		r.fileIndexCache.set(projectID, known)
	}

	workingDir := r.State.WorkingDir
	seen := make(map[string]struct{}, len(matches))
	var pending []pendingFile
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
		pending = append(pending, pendingFile{rel: path, abs: abs})
	}
	if len(pending) == 0 {
		return nil
	}

	// Parallel reads under a small semaphore.
	const maxParallel = 4
	sem := make(chan struct{}, maxParallel)
	type readResult struct {
		rel     string
		content string
	}
	results := make([]readResult, len(pending))
	var wg sync.WaitGroup
	for i, pf := range pending {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, pf pendingFile) {
			defer wg.Done()
			defer func() { <-sem }()
			data, err := os.ReadFile(pf.abs)
			if err != nil {
				return
			}
			results[i] = readResult{rel: pf.rel, content: string(data)}
		}(i, pf)
	}
	wg.Wait()

	out := make([]contextpack.FileSnippet, 0, len(results))
	for _, res := range results {
		if res.rel == "" {
			continue
		}
		out = append(out, contextpack.FileSnippet{
			Path:    res.rel,
			Content: res.content,
		})
	}
	return out
}
