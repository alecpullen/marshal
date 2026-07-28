package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func projectHash(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:12]
}

func (s *Service) ensureRepo(ctx context.Context) error {
	info, err := os.Stat(s.shadowDir)
	if err == nil && info.IsDir() {
		// Best-effort check that the dir is actually a git repo; if not,
		// re-initialize. init --bare on an existing bare repo is a no-op.
	}
	if err := os.MkdirAll(s.shadowDir, 0755); err != nil {
		return fmt.Errorf("create shadow dir: %w", err)
	}
	// init --bare must not include --work-tree.
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+s.shadowDir, "init", "--bare")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("init shadow repo: %w\n%s", err, string(out))
	}
	// Set user identity so commits work on systems without global git config.
	_ = s.runGit(ctx, "config", "user.name", "marshal")
	_ = s.runGit(ctx, "config", "user.email", "marshal@local")
	return nil
}

func (s *Service) runGit(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{
		"--git-dir=" + s.shadowDir,
		"--work-tree=" + s.workTree,
	}, args...)...)
	cmd.Dir = s.workTree
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.logger.Debug("git command failed", "args", args, "error", err, "output", string(out))
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func (s *Service) runGitOut(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{
		"--git-dir=" + s.shadowDir,
		"--work-tree=" + s.workTree,
	}, args...)...)
	cmd.Dir = s.workTree
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.logger.Debug("git command failed", "args", args, "error", err, "output", string(out))
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *Service) refreshExclude(ctx context.Context) error {
	var rules []string

	projectGitignore := filepath.Join(s.workTree, ".gitignore")
	if data, err := os.ReadFile(projectGitignore); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				rules = append(rules, line)
			}
		}
	}

	rules = append(rules, s.ignore...)

	infoDir := filepath.Join(s.shadowDir, "info")
	if err := os.MkdirAll(infoDir, 0755); err != nil {
		return fmt.Errorf("create info dir: %w", err)
	}
	data := strings.Join(rules, "\n")
	if data != "" {
		data += "\n"
	}
	if err := os.WriteFile(filepath.Join(infoDir, "exclude"), []byte(data), 0644); err != nil {
		return fmt.Errorf("write exclude: %w", err)
	}
	return nil
}

// walkContextErr reports the context error underlying err, or nil when err is
// not a cancellation or deadline.
func walkContextErr(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// largeFileExcludes walks the work tree for files over the size cap. The walk
// is bounded twice: by ctx, so a cancelled turn abandons it, and by
// s.walkTimeout, so a tree that is merely enormous still fails loudly instead
// of holding the caller open forever.
func (s *Service) largeFileExcludes(ctx context.Context) ([]string, error) {
	if s.maxFile <= 0 {
		return nil, nil
	}

	timeout := s.walkTimeout
	if timeout <= 0 {
		timeout = defaultWalkTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ignoreRules := s.allIgnoreRules()

	var excludes []string
	err := filepath.Walk(s.workTree, func(path string, info os.FileInfo, err error) error {
		// Checked per entry so cancellation takes effect part-way through a
		// large tree rather than only at the end.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return nil
		}
		if info.IsDir() {
			rel, _ := filepath.Rel(s.workTree, path)
			if rel == "." {
				return nil
			}
			if rel == ".git" || isIgnored(rel, ignoreRules) {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > s.maxFile {
			rel, _ := filepath.Rel(s.workTree, path)
			if !isIgnored(rel, ignoreRules) {
				excludes = append(excludes, rel)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return excludes, nil
}

func (s *Service) allIgnoreRules() []string {
	projectGitignore := filepath.Join(s.workTree, ".gitignore")
	if data, err := os.ReadFile(projectGitignore); err == nil {
		var rules []string
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				rules = append(rules, line)
			}
		}
		return append(rules, s.ignore...)
	}
	return s.ignore
}

func isIgnored(rel string, ignore []string) bool {
	for _, pattern := range ignore {
		if matchIgnorePattern(pattern, rel) {
			return true
		}
	}
	return false
}

func matchIgnorePattern(pattern, rel string) bool {
	// Gitignore anchoring: a leading slash matches only paths relative to the
	// directory containing the ignore file (the project root here).
	anchored := false
	if strings.HasPrefix(pattern, "/") {
		anchored = true
		pattern = strings.TrimPrefix(pattern, "/")
	}

	if strings.HasSuffix(pattern, "/") {
		pattern = strings.TrimSuffix(pattern, "/")
		prefix := pattern + string(filepath.Separator)
		if anchored {
			return rel == pattern || strings.HasPrefix(rel, prefix)
		}
		return rel == pattern || strings.HasPrefix(rel, prefix)
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		if anchored {
			return rel == prefix || strings.HasPrefix(rel, prefix+string(filepath.Separator))
		}
		return rel == prefix || strings.HasPrefix(rel, prefix+string(filepath.Separator))
	}
	if strings.Contains(pattern, "*") {
		if anchored {
			ok, _ := filepath.Match(pattern, rel)
			return ok
		}
		// Unanchored glob: try matching the basename and any path suffix.
		ok, _ := filepath.Match(pattern, rel)
		if ok {
			return true
		}
		// Also match against each path component suffix (e.g. "*.log").
		for rest := rel; ; {
			i := strings.Index(rest, string(filepath.Separator))
			if i == -1 {
				return false
			}
			rest = rest[i+1:]
			if ok, _ := filepath.Match(pattern, rest); ok {
				return true
			}
		}
	}
	if anchored {
		return rel == pattern || strings.HasPrefix(rel, pattern+string(filepath.Separator))
	}
	return rel == pattern || strings.HasPrefix(rel, pattern+string(filepath.Separator))
}
