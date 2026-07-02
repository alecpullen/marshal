package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"marshal/internal/db"
)

type Config struct {
	Root   string
	Ignore []string
	// SkipGitignore controls whether .gitignore files are ignored.
	// When false (the default), .gitignore rules are applied.
	// When true, .gitignore files are skipped entirely.
	SkipGitignore bool
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

	config.Root = root
	s := &Scanner{config: config}
	if !config.SkipGitignore {
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
		hash, size, hashErr := hashFile(path)
		if hashErr != nil {
			return fmt.Errorf("hash %s: %w", rel, hashErr)
		}
		files = append(files, db.FileIndex{
			Path:      rel,
			Language:  DetectLanguage(rel),
			Hash:      hash,
			SizeBytes: size,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
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
