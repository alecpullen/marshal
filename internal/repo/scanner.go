package repo

import (
	"io/fs"
	"path/filepath"

	"marshal/internal/db"
)

type Config struct {
	Root    string
	Ignore  []string
	// IncludeGitignore is intentionally declared here and will be wired to
	// .gitignore parsing in Task 2.
	IncludeGitignore bool
}

type Scanner struct {
	config Config
}

func NewScanner(config Config) *Scanner {
	return &Scanner{config: config}
}

func (s *Scanner) Scan() ([]db.FileIndex, error) {
	root := s.config.Root
	if root == "" {
		root = "."
	}

	var files []db.FileIndex
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		// Unreadable paths are intentionally skipped during indexing so that
		// one bad path does not abort the whole scan.
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipDir(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if s.isIgnored(rel) {
			return nil
		}
		files = append(files, db.FileIndex{Path: rel})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func shouldSkipDir(rel string) bool {
	name := filepath.Base(rel)
	switch name {
	case ".git", ".idea", ".superpowers", ".worktrees", ".agent", ".claude",
		"node_modules", "vendor", "dist", "build", "tmp":
		return true
	default:
		return false
	}
}

func (s *Scanner) isIgnored(rel string) bool {
	for _, pattern := range s.config.Ignore {
		if matched, _ := filepath.Match(pattern, rel); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, filepath.Base(rel)); matched {
			return true
		}
	}
	return false
}
