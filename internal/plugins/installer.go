package plugins

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Installer performs git operations for plugin install/update. The load
// path never touches git — an Installer is only needed by the CLI.
type Installer struct {
	git string
}

// NewInstaller locates the git binary, erroring clearly when it is absent.
func NewInstaller() (*Installer, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git is required to install plugins but was not found in PATH: %w", err)
	}
	return &Installer{git: git}, nil
}

// Clone clones source into dest (which must not exist yet) at ref (empty
// means the remote default branch HEAD) and returns the checked-out commit.
func (i *Installer) Clone(ctx context.Context, source, ref, dest string) (string, error) {
	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, source, dest)
	if err := i.run(ctx, "", args...); err != nil {
		return "", fmt.Errorf("clone %s: %w", source, err)
	}
	return i.HeadCommit(ctx, dest)
}

// HeadCommit returns the current HEAD commit hash of the repo at dir.
func (i *Installer) HeadCommit(ctx context.Context, dir string) (string, error) {
	out, err := i.runOutput(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD in %s: %w", dir, err)
	}
	return strings.TrimSpace(out), nil
}

func (i *Installer) run(ctx context.Context, dir string, args ...string) error {
	_, err := i.runOutput(ctx, dir, args...)
	return err
}

func (i *Installer) runOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, i.git, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// CopyDir recursively copies regular files from src to dst, preserving
// permission bits. Non-regular files are skipped.
func CopyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
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
}
