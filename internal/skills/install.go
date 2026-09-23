package skills

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ScopeDir returns the target skills directory for the requested scope.
func ScopeDir(home, workingDir string, project bool) string {
	if project {
		return filepath.Join(workingDir, ".marshal", "skills")
	}
	return filepath.Join(home, ".config", "marshal", "skills")
}

// Install copies a local skill file or bundle, or clones a git URL, into
// the target skills directory. name may be empty, in which case it is
// derived from the source path or URL.
func Install(ctx context.Context, source, targetDir, name string) (string, error) {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return "", fmt.Errorf("create skills directory: %w", err)
	}
	if name != "" && !ValidName(name) {
		return "", fmt.Errorf("invalid skill name %q", name)
	}

	// Raw single-file skill URL: fetch it to a temp .md file first, then
	// install that local file. This must run before looksLikeGitURL, which
	// treats every http(s) string as a git URL.
	if isRawSkillURL(source) {
		localPath, cleanup, err := fetchRawSkillURL(ctx, source)
		if err != nil {
			return "", fmt.Errorf("fetch skill from %s: %w", source, err)
		}
		defer cleanup()
		source = localPath
	}

	// Git URL install.
	if looksLikeGitURL(source) {
		return installGit(ctx, source, targetDir, name)
	}

	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("stat source: %w", err)
	}

	if info.IsDir() {
		return installBundleDir(source, targetDir, name)
	}

	if !strings.HasSuffix(source, ".md") {
		return "", fmt.Errorf("source must be a .md skill file or a SKILL.md bundle directory")
	}
	return installSingleFile(source, targetDir, name)
}

// ValidName reports whether name is safe to use as a single directory
// name in the skills store. Mirrors plugins.ValidName.
func ValidName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

func looksLikeGitURL(source string) bool {
	if strings.HasPrefix(source, "github:") {
		return true
	}
	if strings.HasPrefix(source, "git@") {
		return true
	}
	if strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://") {
		return true
	}
	if strings.HasSuffix(source, ".git") {
		return true
	}
	return false
}

func installGit(ctx context.Context, source, targetDir, name string) (string, error) {
	url, defaultName, err := normalizeSkillSource(source)
	if err != nil {
		return "", err
	}
	// An explicitly-provided name means "install only the first matching
	// bundle and rename it". An auto-derived name (from the URL) must not
	// trigger that behaviour — it would install only one skill from a
	// multi-skill suite and rename it to the repo name.
	explicitName := name != ""
	if !explicitName {
		name = defaultName
	}

	git, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git is required to install skills from a URL but was not found in PATH")
	}

	tmpDir, err := os.MkdirTemp("", "marshal-skill-install-*")
	if err != nil {
		return "", fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cloneDir := filepath.Join(tmpDir, "src")
	// "--" prevents URLs beginning with "-" from being parsed as git flags.
	args := []string{"clone", "--depth", "1", "--", url, cloneDir}
	if err := runGit(ctx, git, "", args...); err != nil {
		return "", fmt.Errorf("clone %s: %w", url, err)
	}

	// If the repo root contains a SKILL.md, treat it as a single bundle.
	rootBundle := filepath.Join(cloneDir, BundleFileName)
	if _, err := os.Stat(rootBundle); err == nil {
		return installBundleDir(filepath.Dir(rootBundle), targetDir, name)
	}

	// Otherwise discover every SKILL.md bundle anywhere in the clone. A repo
	// may lay its bundles out for several harnesses at once (the Runpod
	// plugin repo uses plugins/<owner>/skills/<name>/SKILL.md), so the clone
	// root is the only thing that must be predictable.
	bundles, err := discoverBundles(cloneDir)
	if err != nil {
		return "", err
	}
	if len(bundles) == 0 {
		return "", fmt.Errorf("cloned repo has no %s bundles", BundleFileName)
	}
	var installed string
	for _, bundle := range bundles {
		bundleName := filepath.Base(filepath.Dir(bundle))
		if !ValidName(bundleName) {
			continue
		}
		if explicitName {
			// If user passed a specific name, install only the first matching
			// bundle and rename it.
			bundleName = name
		}
		path, err := installBundleDir(filepath.Dir(bundle), targetDir, bundleName)
		if err != nil {
			return "", err
		}
		installed = path
		if explicitName {
			break
		}
	}
	if installed == "" {
		return "", fmt.Errorf("cloned repo has no %s bundles", BundleFileName)
	}
	return installed, nil
}

// discoverBundles walks a cloned repo for SKILL.md bundle files and returns
// their paths sorted for a deterministic install order. The walk is bounded:
// a bundle nested deeper than maxBundleDepth is not treated as a bundle, and
// the two directories that can hold thousands of irrelevant files are
// skipped outright.
func discoverBundles(root string) ([]string, error) {
	const maxBundleDepth = 6
	var found []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == root {
				return nil
			}
			switch d.Name() {
			case ".git", "node_modules":
				return fs.SkipDir
			}
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return relErr
			}
			if strings.Count(rel, string(filepath.Separator)) >= maxBundleDepth {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == BundleFileName {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan cloned repo for %s: %w", BundleFileName, err)
	}
	sort.Strings(found)
	return found, nil
}

func normalizeSkillSource(source string) (cloneURL, name string, err error) {
	if strings.HasPrefix(source, "github:") {
		repo := strings.TrimPrefix(source, "github:")
		parts := strings.Split(repo, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", fmt.Errorf("invalid source %q: want github:owner/repo", source)
		}
		return "https://github.com/" + repo + ".git", parts[1], nil
	}
	name = path.Base(filepath.ToSlash(strings.TrimSuffix(source, ".git")))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", "", fmt.Errorf("cannot derive a skill name from %q; pass --name", source)
	}
	return source, name, nil
}

func runGit(ctx context.Context, git, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, git, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func installSingleFile(source, targetDir, name string) (string, error) {
	if name == "" {
		name = skillNameFromPath(source)
	}
	if !ValidName(name) {
		return "", fmt.Errorf("invalid skill name %q", name)
	}
	dest := filepath.Join(targetDir, name+".md")
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	return dest, os.WriteFile(dest, data, 0644)
}

func installBundleDir(source, targetDir, name string) (string, error) {
	if name == "" {
		name = filepath.Base(source)
	}
	dest := filepath.Join(targetDir, name)
	if err := os.MkdirAll(dest, 0755); err != nil {
		return "", err
	}
	err := filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		return "", err
	}
	return dest, nil
}

func skillNameFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
