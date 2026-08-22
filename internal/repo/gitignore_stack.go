package repo

import "strings"

// GitignoreStack maintains a chain of parsed .gitignore files from root to
// the current directory during a directory walk. Deeper .gitignore files
// take precedence over shallower ones, matching git's semantics: the closest
// .gitignore to a file determines its ignore state, and within a single file
// the last matching pattern wins.
type GitignoreStack struct {
	levels []gitignoreLevel
}

type gitignoreLevel struct {
	dir      string // relative directory path ("" for root, slash-separated)
	patterns *Gitignore
}

// NewGitignoreStack creates a stack seeded with the root .gitignore. If
// rootGitignore is nil the stack starts empty (nothing is ignored).
func NewGitignoreStack(rootGitignore *Gitignore) *GitignoreStack {
	s := &GitignoreStack{}
	if rootGitignore != nil && len(rootGitignore.patterns) > 0 {
		s.levels = []gitignoreLevel{{dir: "", patterns: rootGitignore}}
	}
	return s
}

// Push adds a new .gitignore level for the given directory. The directory
// is a slash-separated relative path from the repo root.
func (s *GitignoreStack) Push(dir string, g *Gitignore) {
	if g == nil || len(g.patterns) == 0 {
		return
	}
	s.levels = append(s.levels, gitignoreLevel{dir: dir, patterns: g})
}

// PopTo removes levels whose directory is not a prefix of dir. This is
// called when the walk exits a subtree. The dir argument is a
// slash-separated relative path from the repo root. It is safe to call
// on a nil stack.
func (s *GitignoreStack) PopTo(dir string) {
	if s == nil {
		return
	}
	keep := s.levels[:0]
	for _, lvl := range s.levels {
		if lvl.dir == "" || dir == lvl.dir || strings.HasPrefix(dir, lvl.dir+"/") {
			keep = append(keep, lvl)
		}
	}
	s.levels = keep
}

// Match reports whether the given path should be ignored. The path is a
// slash-separated relative path from the repo root. The stack checks all
// levels from shallowest (root) to deepest. The deepest level that has a
// matching pattern determines the result. A nil stack matches nothing.
func (s *GitignoreStack) Match(path string, isDir bool) bool {
	if s == nil {
		return false
	}
	ignored := false
	for _, lvl := range s.levels {
		// Compute the path relative to this level's directory.
		rel := path
		if lvl.dir != "" {
			if !strings.HasPrefix(path, lvl.dir+"/") && path != lvl.dir {
				continue
			}
			rel = strings.TrimPrefix(path, lvl.dir+"/")
		}
		matched, lvlIgnored := lvl.patterns.MatchDetail(rel, isDir)
		if matched {
			ignored = lvlIgnored
		}
	}
	return ignored
}
