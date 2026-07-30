package skills

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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

	// Git URL install.
	if looksLikeGitURL(source) {
		return installGit(ctx, source, targetDir, name)
	}

	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("stat source: %w", err)
	}

	if info.IsDir() {
		bundle := filepath.Join(source, BundleFileName)
		if _, err := os.Stat(bundle); err == nil {
			return installBundleFile(bundle, targetDir, name)
		}
		return installBundleDir(source, targetDir, name)
	}

	if !strings.HasSuffix(source, ".md") {
		return "", fmt.Errorf("source must be a .md skill file or a SKILL.md bundle directory")
	}
	return installSingleFile(source, targetDir, name)
}

func looksLikeGitURL(source string) bool {
	if strings.HasPrefix(source, "github:") {
		return true
	}
	if strings.HasPrefix(source, "git@") {
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
	if name == "" {
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
	args := []string{"clone", "--depth", "1", url, cloneDir}
	if err := runGit(ctx, git, "", args...); err != nil {
		return "", fmt.Errorf("clone %s: %w", url, err)
	}

	// If the repo root contains a SKILL.md, treat it as a single bundle.
	rootBundle := filepath.Join(cloneDir, BundleFileName)
	if _, err := os.Stat(rootBundle); err == nil {
		return installBundleFile(rootBundle, targetDir, name)
	}

	// Otherwise copy every skills/<name>/SKILL.md bundle found.
	skillsDir := filepath.Join(cloneDir, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return "", fmt.Errorf("cloned repo has no %s at root or skills/ subdirectory", BundleFileName)
	}
	var installed string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		bundle := filepath.Join(skillsDir, e.Name(), BundleFileName)
		if _, err := os.Stat(bundle); err != nil {
			continue
		}
		bundleName := e.Name()
		if name != "" {
			// If user passed a specific name, install only the first matching
			// bundle and rename it.
			bundleName = name
		}
		path, err := installBundleFile(bundle, targetDir, bundleName)
		if err != nil {
			return "", err
		}
		installed = path
		if name != "" {
			break
		}
	}
	if installed == "" {
		return "", fmt.Errorf("cloned repo has no %s bundles", BundleFileName)
	}
	return installed, nil
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
	dest := filepath.Join(targetDir, name+".md")
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	return dest, os.WriteFile(dest, data, 0644)
}

func installBundleFile(bundlePath, targetDir, name string) (string, error) {
	if name == "" {
		name = filepath.Base(filepath.Dir(bundlePath))
	}
	dest := filepath.Join(targetDir, name)
	if err := os.MkdirAll(dest, 0755); err != nil {
		return "", err
	}
	destFile := filepath.Join(dest, BundleFileName)
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return "", err
	}
	return dest, os.WriteFile(destFile, data, 0644)
}

func installBundleDir(source, targetDir, name string) (string, error) {
	if name == "" {
		name = filepath.Base(source)
	}
	dest := filepath.Join(targetDir, name)
	if err := os.MkdirAll(dest, 0755); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(source, e.Name())
		dst := filepath.Join(dest, e.Name())
		data, err := os.ReadFile(src)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return "", err
		}
	}
	return dest, nil
}

func skillNameFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}