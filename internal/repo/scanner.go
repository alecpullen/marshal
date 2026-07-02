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
	// SkipGitignore, when true, prevents the scanner from loading and
	// applying the root .gitignore file. By default .gitignore is loaded.
	SkipGitignore bool
}

type Scanner struct {
	config    Config
	gitignore *Gitignore
}

func NewScanner(config Config) (*Scanner, error) {
	var g *Gitignore
	var err error
	if !config.SkipGitignore {
		root := config.Root
		if root == "" {
			root = "."
		}
		g, err = LoadGitignore(filepath.Join(root, ".gitignore"))
		if err != nil {
			return nil, err
		}
	}
	return &Scanner{config: config, gitignore: g}, nil
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
		if s.gitignore != nil && s.gitignore.Match(rel, entry.IsDir()) {
			if entry.IsDir() {
				return fs.SkipDir
			}
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
		if filepath.Base(rel) == ".gitignore" {
			return nil
		}
		if s.isIgnored(rel) {
			return nil
		}
		fullPath := path
		hash, size, hashErr := hashFile(fullPath)
		if hashErr != nil {
			return fmt.Errorf("hash file %q: %w", rel, hashErr)
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
