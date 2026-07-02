package repo

import (
	"fmt"
	"io/fs"
	"path/filepath"

	"marshal/internal/db"
)

type Config struct {
	Root             string
	Ignore           []string
	IncludeGitignore bool
}

type Scanner struct {
	config    Config
	gitignore *Gitignore
	loadErr   error
}

func NewScanner(config Config) *Scanner {
	root := config.Root
	if root == "" {
		root = "."
	}

	s := &Scanner{config: config}
	if !config.IncludeGitignore {
		g, err := LoadGitignore(filepath.Join(root, ".gitignore"))
		if err != nil {
			s.loadErr = err
		} else {
			s.gitignore = g
		}
	}
	return s
}

func (s *Scanner) Scan() ([]db.FileIndex, error) {
	root := s.config.Root
	if root == "" {
		root = "."
	}
	if s.loadErr != nil {
		return nil, s.loadErr
	}

	var files []db.FileIndex
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return fmt.Errorf("rel %s: %w", path, relErr)
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if s.gitignore != nil && s.gitignore.Match(rel, entry.IsDir()) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Base(rel) == ".gitignore" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			skip, ignoreErr := s.isIgnored(rel)
			if ignoreErr != nil {
				return ignoreErr
			}
			if shouldSkipDir(rel) || skip {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		skip, ignoreErr := s.isIgnored(rel)
		if ignoreErr != nil {
			return ignoreErr
		}
		if skip {
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

// shouldSkipDir reports whether rel is a known tooling or output directory
// that should always be skipped during scanning.
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

// isIgnored reports whether rel matches any configured ignore pattern.
// Patterns are matched against both the relative path and the basename
// using filepath.Match. An error is returned if any pattern is invalid.
func (s *Scanner) isIgnored(rel string) (bool, error) {
	for _, pattern := range s.config.Ignore {
		matched, err := filepath.Match(pattern, rel)
		if err != nil {
			return false, fmt.Errorf("invalid ignore pattern %q: %w", pattern, err)
		}
		if matched {
			return true, nil
		}
		matched, err = filepath.Match(pattern, filepath.Base(rel))
		if err != nil {
			return false, fmt.Errorf("invalid ignore pattern %q: %w", pattern, err)
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}
