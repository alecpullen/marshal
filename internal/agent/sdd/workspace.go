package sdd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Workspace manages the .marshal/sdd/ scratch directory: task briefs,
// implementer reports, review packages, and the progress ledger.
type Workspace struct {
	root string
	dir  string
}

// NewWorkspace creates a Workspace rooted at the given working directory.
// The scratch directory lives at <root>/.marshal/sdd/.
func NewWorkspace(workingDir string) (*Workspace, error) {
	dir := filepath.Join(workingDir, ".marshal", "sdd")
	return &Workspace{root: workingDir, dir: dir}, nil
}

// Ensure creates the scratch directory and its self-ignoring .gitignore.
// Returns the directory path.
func (w *Workspace) Ensure() (string, error) {
	if err := os.MkdirAll(w.dir, 0755); err != nil {
		return "", fmt.Errorf("sdd workspace: mkdir: %w", err)
	}
	gitignore := filepath.Join(w.dir, ".gitignore")
	if _, err := os.Stat(gitignore); os.IsNotExist(err) {
		if err := os.WriteFile(gitignore, []byte("*\n"), 0644); err != nil {
			return "", fmt.Errorf("sdd workspace: write .gitignore: %w", err)
		}
	}
	return w.dir, nil
}

func (w *Workspace) BriefPath(n int) string {
	return filepath.Join(w.dir, fmt.Sprintf("task-%d-brief.md", n))
}

func (w *Workspace) ReportPath(n int) string {
	return filepath.Join(w.dir, fmt.Sprintf("task-%d-report.md", n))
}

func (w *Workspace) ReportsDir() string {
	return w.dir
}

func (w *Workspace) WriteTaskBrief(n int, body string) (string, error) {
	if _, err := w.Ensure(); err != nil {
		return "", err
	}
	path := w.BriefPath(n)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		return "", fmt.Errorf("sdd workspace: write brief: %w", err)
	}
	return path, nil
}

// WriteReviewPackage runs git log/diff for the base..head range and writes
// a review package file (commit list, stat summary, full diff with context).
// Returns the file path.
func (w *Workspace) WriteReviewPackage(base, head string) (string, error) {
	if _, err := w.Ensure(); err != nil {
		return "", err
	}
	logOut, err := exec.Command("git", "log", "--oneline", base+".."+head).Output()
	if err != nil {
		return "", fmt.Errorf("sdd workspace: git log: %w", err)
	}
	statOut, err := exec.Command("git", "diff", "--stat", base+".."+head).Output()
	if err != nil {
		return "", fmt.Errorf("sdd workspace: git diff --stat: %w", err)
	}
	diffOut, err := exec.Command("git", "diff", "-U10", base+".."+head).Output()
	if err != nil {
		return "", fmt.Errorf("sdd workspace: git diff: %w", err)
	}
	content := fmt.Sprintf("# Review package: %s..%s\n\n## Commits\n%s\n## Files changed\n%s\n## Diff\n%s",
		base, head, logOut, statOut, diffOut)
	shortBase := shortSHA(base)
	shortHead := shortSHA(head)
	path := filepath.Join(w.dir, fmt.Sprintf("review-%s..%s.diff", shortBase, shortHead))
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("sdd workspace: write review package: %w", err)
	}
	return path, nil
}

// WriteBranchReviewPackage is like WriteReviewPackage but for the whole
// branch (mergeBase..head).
func (w *Workspace) WriteBranchReviewPackage(mergeBase, head string) (string, error) {
	return w.WriteReviewPackage(mergeBase, head)
}

func shortSHA(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}
