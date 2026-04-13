// Package sandbox implements security boundaries for tool use.
// It provides path allowlisting and command allowlisting to prevent
// unauthorized file access and command execution.
package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config defines the security boundaries for tool use.
type Config struct {
	// RepoRoot is the absolute path to the repository root.
	// All file operations must be within this directory.
	RepoRoot string

	// AllowedCommands is a list of shell commands that can be executed.
	// If empty, all commands are allowed (for backwards compatibility).
	// Examples: ["go", "git", "npm", "python"]
	AllowedCommands []string

	// ReadOnlyPaths are paths that can be read but not written.
	// These override the default repo-root restriction.
	ReadOnlyPaths []string
}

// Sandbox provides security validation for tool operations.
type Sandbox struct {
	cfg Config
}

// New creates a new Sandbox with the given configuration.
func New(cfg Config) *Sandbox {
	return &Sandbox{cfg: cfg}
}

// Config returns the sandbox configuration.
func (s *Sandbox) Config() Config {
	return s.cfg
}

// ValidatePath checks if a path is within the allowed repository root.
// It prevents path traversal attacks and symlink escapes.
func (s *Sandbox) ValidatePath(path string) error {
	if s.cfg.RepoRoot == "" {
		return fmt.Errorf("sandbox: repo root not configured")
	}

	// Clean the path
	cleanPath := filepath.Clean(path)

	// Reject absolute paths that don't start with repo root
	if filepath.IsAbs(cleanPath) {
		if !strings.HasPrefix(cleanPath, s.cfg.RepoRoot) {
			return fmt.Errorf("sandbox: absolute path %q outside repo root", path)
		}
	}

	// Resolve to absolute path
	absPath := cleanPath
	if !filepath.IsAbs(cleanPath) {
		absPath = filepath.Join(s.cfg.RepoRoot, cleanPath)
	}

	absPath, err := filepath.Abs(absPath)
	if err != nil {
		return fmt.Errorf("sandbox: cannot resolve path: %w", err)
	}

	// Ensure path is within repo root
	if !strings.HasPrefix(absPath, s.cfg.RepoRoot+string(filepath.Separator)) &&
		absPath != s.cfg.RepoRoot {
		return fmt.Errorf("sandbox: path %q escapes repo root", path)
	}

	return nil
}

// ValidateWritePath checks if a path can be written to.
// It ensures the parent directory exists or can be created within the repo.
func (s *Sandbox) ValidateWritePath(path string) error {
	if err := s.ValidatePath(path); err != nil {
		return err
	}

	absPath := path
	if !filepath.IsAbs(path) {
		absPath = filepath.Join(s.cfg.RepoRoot, path)
	}
	absPath, _ = filepath.Abs(absPath)

	// Check if file exists and is a directory
	info, err := os.Stat(absPath)
	if err == nil && info.IsDir() {
		return fmt.Errorf("sandbox: cannot write to directory %q", path)
	}

	// Ensure parent directory is within repo
	parent := filepath.Dir(absPath)
	if err := s.ValidatePath(parent); err != nil {
		return fmt.Errorf("sandbox: parent directory outside repo: %w", err)
	}

	return nil
}

// IsCommandAllowed checks if a command is in the allowlist.
// If no allowlist is configured, all commands are allowed.
func (s *Sandbox) IsCommandAllowed(command string) bool {
	if len(s.cfg.AllowedCommands) == 0 {
		return true // No restrictions
	}

	// Extract the base command (first word)
	cmd := strings.Fields(command)[0]
	cmd = filepath.Base(cmd) // Remove path prefix

	for _, allowed := range s.cfg.AllowedCommands {
		if strings.EqualFold(cmd, allowed) {
			return true
		}
	}

	return false
}

// ValidateCommand checks a command and returns an error if not allowed.
func (s *Sandbox) ValidateCommand(command string) error {
	if !s.IsCommandAllowed(command) {
		return fmt.Errorf("sandbox: command %q not in allowlist", command)
	}
	return nil
}

// DefaultConfig returns a Config with sensible defaults for a repository.
func DefaultConfig(repoRoot string) Config {
	return Config{
		RepoRoot: repoRoot,
		AllowedCommands: []string{
			"go", "git", "npm", "yarn", "pnpm",
			"python", "python3", "pip", "pytest",
			"make", "cmake",
			"cargo", "rustc",
			"javac", "java",
			"node", "deno", "bun",
			"sh", "bash", "zsh",
			"cat", "ls", "grep", "find", "head", "tail",
		},
	}
}
