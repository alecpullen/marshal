package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func (s *Service) largeFileExcludes() ([]string, error) {
	if s.maxFile <= 0 {
		return nil, nil
	}

	var excludes []string
	err := filepath.Walk(s.workTree, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			rel, _ := filepath.Rel(s.workTree, path)
			if rel == "." {
				return nil
			}
			if rel == ".git" || isIgnored(rel, s.ignore) {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > s.maxFile {
			rel, _ := filepath.Rel(s.workTree, path)
			excludes = append(excludes, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return excludes, nil
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
	if strings.HasSuffix(pattern, "/") {
		pattern = strings.TrimSuffix(pattern, "/")
		prefix := pattern + string(filepath.Separator)
		return rel == pattern || strings.HasPrefix(rel, prefix)
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return rel == prefix || strings.HasPrefix(rel, prefix+string(filepath.Separator))
	}
	if strings.Contains(pattern, "*") {
		ok, _ := filepath.Match(pattern, rel)
		return ok
	}
	return rel == pattern || strings.HasPrefix(rel, pattern+string(filepath.Separator))
}
