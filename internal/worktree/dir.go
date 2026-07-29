package worktree

import (
	"fmt"
	"os"
	"path/filepath"
)

// AgentDir is where interactive-session (agent) worktrees live:
// <projectRoot>/.marshal/worktrees. It is created on first call with a
// self-ignoring .gitignore so worktree checkouts never appear in the main
// checkout's git status. Pipeline run worktrees live elsewhere
// (.marshal/pipeline/<slug>/worktrees) and are unaffected.
func AgentDir(projectRoot string) (string, error) {
	dir := filepath.Join(projectRoot, ".marshal", "worktrees")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("agent worktrees: mkdir %s: %w", dir, err)
	}
	gitignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gitignore); os.IsNotExist(err) {
		if err := os.WriteFile(gitignore, []byte("*\n"), 0o644); err != nil {
			return "", fmt.Errorf("agent worktrees: write .gitignore: %w", err)
		}
	}
	return dir, nil
}
