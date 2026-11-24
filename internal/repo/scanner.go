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
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"marshal/internal/db"
	"marshal/internal/limits"
)

type Config struct {
	Root   string
	Ignore []string
	// MaxIndexableFileBytes caps the size of files that will be hashed and
	// indexed during scanning. Files larger than this threshold are skipped
	// and recorded in Skipped(). Zero means no limit.
	MaxIndexableFileBytes int64
	// MaxTotalContentBytes caps the total file content one scan retains across
	// all files. A file beyond the budget is still hashed and indexed (path,
	// hash, language, size) but its Content is nil and
	// ScannedFile.ContentOmitted is set. Zero means
	// limits.MaxTotalContentBytes: a bound never disappears.
	MaxTotalContentBytes int64
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
func (s *Scanner) walk(ctx context.Context, fn func(path, rel string, retain bool) (db.FileIndex, []byte, error)) ([]ScannedFile, error) {
	s.warnings = nil
	root := s.config.Root
	if s.loadErr != nil {
		s.warnings = append(s.warnings, "gitignore: "+s.loadErr.Error())
	}

	var results []ScannedFile
	var totalContent int64
	budget := s.config.MaxTotalContentBytes
	if budget <= 0 {
		budget = limits.MaxTotalContentBytes
	}
	// exhausted latches the first file the budget rejects; unmeasured names the
	// first file whose size could not be read. Both are reported once, at the
	// path that caused them.
	exhausted := false
	unmeasured := ""
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

		// The cap and the budget both need the size, so stat once here rather
		// than once per bound. A failed stat is not fatal: the file is treated
		// as unretainable and hashed by streaming, which keeps a file we cannot
		// measure out of the retained working set. It is reported ContentOmitted
		// too, so the warning below names the first such file instead of
		// dropping it silently.
		size, sizeKnown := int64(0), false
		if info, infoErr := entry.Info(); infoErr == nil {
			size, sizeKnown = info.Size(), true
		}
		if !sizeKnown && unmeasured == "" {
			unmeasured = rel
			s.warnings = append(s.warnings, fmt.Sprintf(
				"content omitted: could not measure %s, so the file is indexed without content", rel))
		}

		// Skip files that exceed the configurable size cap.
		if s.config.MaxIndexableFileBytes > 0 && sizeKnown && size > s.config.MaxIndexableFileBytes {
			s.skipped = append(s.skipped, SkipEntry{Path: rel, Reason: fmt.Sprintf("file too large (%d bytes)", size)})
			return nil
		}

		// The budget is a total, so it can only be judged here, once per file,
		// after the per-file filters have run. exhausted latches the first file
		// it rejects: every file after the boundary is omitted, so the retained
		// set is a prefix of the walk and no later file is measured against the
		// budget again. That keeps a small file that follows a rejected large one
		// omitted too, rather than retained out of order. A file whose size
		// could not be measured is omitted without exhausting the budget, so it
		// can sit after retained files.
		retain := !exhausted && sizeKnown && totalContent+size <= budget
		if !retain && sizeKnown && !exhausted {
			exhausted = true
			s.warnings = append(s.warnings, fmt.Sprintf(
				"content budget exhausted: files past %d retained bytes are indexed without content, starting at %s",
				budget, rel))
		}
		fi, content, fnErr := fn(path, rel, retain)
		if fnErr != nil {
			s.warnings = append(s.warnings, fnErr.Error())
		}
		if retain && fnErr == nil {
			totalContent += int64(len(content))
		}
		results = append(results, ScannedFile{
			FileIndex:      fi,
			Content:        content,
			ReadErr:        fnErr,
			ContentOmitted: !retain,
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
// A file past the scan's total content budget is hashed by streaming and
// returned with a nil Content and ContentOmitted set: it stays in the file
// index, it just carries no bytes. Once the budget rejects a file, every later
// file is omitted the same way, so the retained files form a prefix of the walk.
// A file whose size could not be measured is omitted too, because it cannot be
// judged against the budget, so it can sit after retained files.
func (s *Scanner) ScanDetailed(ctx context.Context) ([]ScannedFile, error) {
	return s.walk(ctx, func(path, rel string, retain bool) (db.FileIndex, []byte, error) {
		if !retain {
			hash, size, err := hashFile(path)
			if err != nil {
				return db.FileIndex{
					Path:     rel,
					Language: DetectLanguage(rel),
				}, nil, fmt.Errorf("hash %s: %w", rel, err)
			}
			return db.FileIndex{
				Path:      rel,
				Language:  DetectLanguage(rel),
				Hash:      hash,
				SizeBytes: size,
			}, nil, nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return db.FileIndex{
				Path:     rel,
				Language: DetectLanguage(rel),
			}, nil, fmt.Errorf("read %s: %w", rel, err)
		}
		hash, size := hashBytes(content)
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
func hashBytes(content []byte) (string, int64) {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), int64(len(content))
}

// hashFile computes the SHA-256 hash of a file's bytes without holding them in
// memory, and returns the hash and the number of bytes read. It is the path
// taken for a file past the scan's content budget, so its digest must match
// hashBytes for the same bytes.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.CopyBuffer(h, f, make([]byte, 64<<10))
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
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
