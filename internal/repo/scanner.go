// Package repo provides file system scanning and indexing for project
// repositories. It walks directory trees, computes file hashes, detects
// languages, applies gitignore rules, and skips symlinks for security and
// correctness.
package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"marshal/internal/db"
)

type Config struct {
	Root   string
	Ignore []string
	// MaxIndexableFileBytes caps the size of files that will be hashed and
	// indexed during scanning. Files larger than this threshold are skipped
	// and recorded in Skipped(). Zero means no limit.
	MaxIndexableFileBytes int64
}

// SkipEntry records a file or directory that was skipped during scanning.
type SkipEntry struct {
	Path   string
	Reason string
}

type Scanner struct {
	config         Config
	gitignoreStack *GitignoreStack
	loadErr        error
	skipped        []SkipEntry
	warnings       []string
}

// defaultIgnoredDirs is the set of directory names that should always be
// skipped during repository scanning and search.
var defaultIgnoredDirs = map[string]bool{
	".git": true, ".idea": true, ".superpowers": true, ".worktrees": true,
	".agent": true, ".claude": true,
	// .marshal holds agent state: spill files and pipeline/session
	// worktrees containing full repo checkouts. Walking it pollutes
	// search results and sends agents debugging stale copies of the
	// same packages.
	".marshal":     true,
	"node_modules": true, "vendor": true, "dist": true, "build": true, "tmp": true,
}

// IsDefaultIgnoredDir reports whether name is a directory that should always
// be skipped.
func IsDefaultIgnoredDir(name string) bool {
	return defaultIgnoredDirs[name]
}

func NewScanner(config Config) *Scanner {
	root := config.Root
	if root == "" {
		root = "."
	}

	config.Root = root
	s := &Scanner{config: config}
	g, err := LoadGitignore(filepath.Join(root, ".gitignore"))
	if err != nil {
		s.loadErr = err
	}
	// Always initialize the stack, even on root gitignore load failure, so
	// downstream walk code can call PopTo/Match without nil checks.
	s.gitignoreStack = NewGitignoreStack(g)
	return s
}

// walk performs the directory walk, calling fn for each regular file that
// passes the filter chain. fn receives the absolute path and relative path,
// and returns the FileIndex, content bytes, and an optional per-file error.
// Unlike WalkDir errors, per-file errors from fn are NOT fatal — they are
// logged in Warnings() and recorded as ScannedFile.ReadErr so the caller
// can inspect them without aborting the entire scan.
func (s *Scanner) walk(ctx context.Context, fn func(path, rel string) (db.FileIndex, []byte, error)) ([]ScannedFile, error) {
	s.warnings = nil
	root := s.config.Root
	if s.loadErr != nil {
		s.warnings = append(s.warnings, "gitignore: "+s.loadErr.Error())
	}

	var results []ScannedFile
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return fmt.Errorf("rel %s: %w", path, relErr)
		}
		rel = filepath.ToSlash(rel)
		// Symlinks are deliberately skipped. A symlink inside the workspace
		// may point outside it, which would let the scanner read arbitrary
		// files (security) or follow circular structures (correctness).
		// Users who need indexed symlinks should use hard links or bind
		// mounts instead.
		if entry.Type()&os.ModeSymlink != 0 {
			s.skipped = append(s.skipped, SkipEntry{Path: rel, Reason: "symlink"})
			return nil
		}
		if rel == "." {
			return nil
		}
		if s.gitignoreStack != nil && s.gitignoreStack.Match(rel, entry.IsDir()) {
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
			// Push per-directory .gitignore onto the stack (root was loaded in NewScanner).
			s.gitignoreStack.PopTo(rel)
			giPath := filepath.Join(root, rel, ".gitignore")
			if gi, giErr := LoadGitignore(giPath); giErr != nil {
				s.warnings = append(s.warnings, "gitignore: "+giErr.Error())
			} else {
				s.gitignoreStack.Push(rel, gi)
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

		// Skip files that exceed the configurable size cap.
		if s.config.MaxIndexableFileBytes > 0 {
			if info, infoErr := entry.Info(); infoErr == nil && info.Size() > s.config.MaxIndexableFileBytes {
				s.skipped = append(s.skipped, SkipEntry{Path: rel, Reason: fmt.Sprintf("file too large (%d bytes)", info.Size())})
				return nil
			}
		}

		fi, content, fnErr := fn(path, rel)
		if fnErr != nil {
			s.warnings = append(s.warnings, fnErr.Error())
		}
		results = append(results, ScannedFile{
			FileIndex: fi,
			Content:   content,
			ReadErr:   fnErr,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// ScanDetailed scans the repo root, computing file hashes and capturing each file's
// content so callers can use it without re-reading from disk.
// Per-file read failures are non-fatal: the returned ScannedFile will have a
// nil Content and a non-nil ReadErr. The scan continues to other files.
func (s *Scanner) ScanDetailed(ctx context.Context) ([]ScannedFile, error) {
	return s.walk(ctx, func(path, rel string) (db.FileIndex, []byte, error) {
		content, err := os.ReadFile(path)
		if err != nil {
			return db.FileIndex{
				Path:     rel,
				Language: DetectLanguage(rel),
			}, nil, fmt.Errorf("read %s: %w", rel, err)
		}
		hash, size, hashErr := hashBytes(content)
		if hashErr != nil {
			return db.FileIndex{
				Path:     rel,
				Language: DetectLanguage(rel),
			}, content, fmt.Errorf("hash %s: %w", rel, hashErr)
		}
		return db.FileIndex{
			Path:      rel,
			Language:  DetectLanguage(rel),
			Hash:      hash,
			SizeBytes: size,
		}, content, nil
	})
}

// Skipped returns a list of entries that were skipped during the most recent
// Scan. The caller must not modify the returned slice.
func (s *Scanner) Skipped() []SkipEntry {
	return s.skipped
}

// Warnings returns any non-fatal diagnostics accumulated during Scan, such as
// gitignore parse errors. The caller must not modify the returned slice.
func (s *Scanner) Warnings() []string {
	return s.warnings
}

// hashBytes computes the SHA-256 hash of content and returns the hex-encoded
// hash along with the content length.
func hashBytes(content []byte) (string, int64, error) {
	h := sha256.New()
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil)), int64(len(content)), nil
}

// shouldSkipDir reports whether rel is a known tooling or output directory
// that should always be skipped during scanning.
func shouldSkipDir(rel string) bool {
	return IsDefaultIgnoredDir(filepath.Base(rel))
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
