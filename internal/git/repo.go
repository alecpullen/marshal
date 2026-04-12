// Package git wraps os/exec git commands for marshal's branch-isolation model.
// All operations use the real git binary so that hooks, submodules, partial
// clones, and custom configs behave exactly as they would for the user.
package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrNothingToCommit is returned by CommitAll/CommitFiles when there are no
// staged changes to commit.
var ErrNothingToCommit = errors.New("nothing to commit")

// ErrAlreadyUpToDate is returned by MergeSquash when the branch being merged
// contains no changes relative to the current HEAD.
var ErrAlreadyUpToDate = errors.New("already up to date")

// GitError carries the full command output for debugging.
type GitError struct {
	Args   []string
	Output string
	Err    error
}

func (e *GitError) Error() string {
	return fmt.Sprintf("git %s: %v\n%s", strings.Join(e.Args, " "), e.Err, e.Output)
}

func (e *GitError) Unwrap() error { return e.Err }

// RepoConfig holds author identity and diff preferences.
type RepoConfig struct {
	// AuthorName / AuthorEmail are used for commits marshal makes directly.
	// Defaults to "marshal" / "marshal@local".
	AuthorName  string
	AuthorEmail string
	// CoAuthoredBy is the model name appended as a co-authored-by trailer.
	// Empty string disables the trailer.
	CoAuthoredBy string
	// DiffContext is the number of unified-diff context lines (default 1,
	// per the marshal spec §2).
	DiffContext int
}

func (c *RepoConfig) applyDefaults() {
	if c.AuthorName == "" {
		c.AuthorName = "marshal"
	}
	if c.AuthorEmail == "" {
		c.AuthorEmail = "marshal@local"
	}
	if c.DiffContext == 0 {
		c.DiffContext = 1
	}
}

// Repo is a handle to a git repository rooted at a specific directory.
type Repo struct {
	root string
	cfg  RepoConfig
}

// New validates that path is inside a git repo, resolves its root, and returns
// a Repo. Returns an error if git is not installed or path is not in a repo.
func New(path string, cfg RepoConfig) (*Repo, error) {
	cfg.applyDefaults()
	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("not a git repository: %s", path)
	}
	root := strings.TrimSpace(string(out))
	return &Repo{root: root, cfg: cfg}, nil
}

// Root returns the absolute path to the repository root.
func (r *Repo) Root() string { return r.root }

// --- Query methods -----------------------------------------------------------

// HeadSHA returns the full SHA of HEAD.
func (r *Repo) HeadSHA() (string, error) {
	return r.run("rev-parse", "HEAD")
}

// CurrentBranch returns the name of the currently checked-out branch.
func (r *Repo) CurrentBranch() (string, error) {
	return r.run("rev-parse", "--abbrev-ref", "HEAD")
}

// IsDirty returns true if there are any uncommitted changes (staged or unstaged).
func (r *Repo) IsDirty() (bool, error) {
	out, err := r.run("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// DirtyFiles returns a list of paths that have uncommitted changes.
func (r *Repo) DirtyFiles() ([]string, error) {
	out, err := r.run("status", "--porcelain")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// porcelain format: "XY path" or "XY orig -> path"
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			files = append(files, parts[len(parts)-1])
		}
	}
	return files, nil
}

// TrackedFiles returns all files tracked by git, respecting .gitignore.
// The paths are relative to the repo root.
func (r *Repo) TrackedFiles() ([]string, error) {
	out, err := r.run("ls-files")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// RefExists reports whether a branch or ref name exists in the repository.
func (r *Repo) RefExists(name string) (bool, error) {
	_, err := r.run("rev-parse", "--verify", name)
	if err != nil {
		var ge *GitError
		if errors.As(err, &ge) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// --- Branch operations -------------------------------------------------------

// CreateBranch creates a new branch at fromSHA without checking it out.
func (r *Repo) CreateBranch(name, fromSHA string) error {
	_, err := r.run("branch", name, fromSHA)
	return err
}

// Checkout switches to an existing branch or ref.
func (r *Repo) Checkout(name string) error {
	_, err := r.run("checkout", name)
	return err
}

// CheckoutNewBranch creates a new branch at fromSHA and checks it out.
func (r *Repo) CheckoutNewBranch(name, fromSHA string) error {
	_, err := r.run("checkout", "-b", name, fromSHA)
	return err
}

// DeleteBranch force-deletes a branch. It is safe to call even if the branch
// does not exist (returns nil in that case).
func (r *Repo) DeleteBranch(name string) error {
	_, err := r.run("branch", "-D", name)
	if err != nil {
		var ge *GitError
		if errors.As(err, &ge) && strings.Contains(ge.Output, "not found") {
			return nil
		}
	}
	return err
}

// MergeSquash squash-merges branch into the current branch and commits with
// message. Returns ErrAlreadyUpToDate if branch has no new commits.
// The co-authored-by trailer is appended automatically if configured.
func (r *Repo) MergeSquash(branch, message string) error {
	out, err := r.run("merge", "--squash", branch)
	if err != nil {
		return err
	}
	if strings.Contains(strings.ToLower(out), "already up to date") {
		return ErrAlreadyUpToDate
	}
	if err := r.commit(message); errors.Is(err, ErrNothingToCommit) {
		// The task branch had commits but their net diff against staging is
		// zero (e.g. a file was changed then reverted).  Treat as up-to-date
		// so the engine retries rather than returning a hard error.
		return ErrAlreadyUpToDate
	} else {
		return err
	}
}

// ResetHard resets HEAD and the working tree to sha.
func (r *Repo) ResetHard(sha string) error {
	_, err := r.run("reset", "--hard", sha)
	return err
}

// RevertNoCommit stages a revert of commit sha without creating a commit.
// The caller is responsible for committing afterwards.
// Returns an error (without committing) if there are conflicts.
func (r *Repo) RevertNoCommit(sha string) error {
	_, err := r.run("revert", "--no-commit", sha)
	return err
}

// --- Diff --------------------------------------------------------------------

// DiffOpts controls git diff behaviour.
type DiffOpts struct {
	// Base is the commit or ref to diff the working tree against.
	// Empty string diffs the working tree against HEAD.
	Base string
	// Context overrides the repo-level DiffContext setting.
	// Zero means use the repo default.
	Context int
	// Staged diffs the index against Base (git diff --cached).
	Staged bool
}

// Diff returns a unified diff string. With no Base set it diffs the working
// tree against HEAD; the default context is the repo-level DiffContext (1).
func (r *Repo) Diff(opts DiffOpts) (string, error) {
	ctx := opts.Context
	if ctx == 0 {
		ctx = r.cfg.DiffContext
	}
	args := []string{"diff", fmt.Sprintf("-U%d", ctx)}
	if opts.Staged {
		args = append(args, "--cached")
	}
	if opts.Base != "" {
		args = append(args, opts.Base)
	}
	return r.run(args...)
}

// --- Commit ------------------------------------------------------------------

// CommitAll stages all changes and commits with message.
// Returns ErrNothingToCommit if there are no changes.
func (r *Repo) CommitAll(message string) error {
	if _, err := r.run("add", "-A"); err != nil {
		return err
	}
	return r.commit(message)
}

// CommitFiles stages the given paths and commits with message.
// Returns ErrNothingToCommit if there are no staged changes after adding.
func (r *Repo) CommitFiles(paths []string, message string) error {
	args := append([]string{"add", "--"}, paths...)
	if _, err := r.run(args...); err != nil {
		return err
	}
	return r.commit(message)
}

// commit creates a commit with the given message, appending the co-authored-by
// trailer when configured. Returns ErrNothingToCommit on a clean index.
func (r *Repo) commit(message string) error {
	msg := r.buildCommitMessage(message)
	_, err := r.runWithEnv(
		[]string{
			"GIT_AUTHOR_NAME=" + r.cfg.AuthorName,
			"GIT_COMMITTER_NAME=" + r.cfg.AuthorName,
			"GIT_AUTHOR_EMAIL=" + r.cfg.AuthorEmail,
			"GIT_COMMITTER_EMAIL=" + r.cfg.AuthorEmail,
		},
		"commit", "-m", msg,
	)
	if err != nil {
		var ge *GitError
		if errors.As(err, &ge) && strings.Contains(ge.Output, "nothing to commit") {
			return ErrNothingToCommit
		}
		return err
	}
	return nil
}

func (r *Repo) buildCommitMessage(message string) string {
	if r.cfg.CoAuthoredBy == "" {
		return message
	}
	trailer := fmt.Sprintf(
		"Co-authored-by: marshal (%s) <marshal@local>",
		r.cfg.CoAuthoredBy,
	)
	return message + "\n\n" + trailer
}

// --- Internal helpers --------------------------------------------------------

// run executes a git command in the repo root and returns trimmed stdout.
func (r *Repo) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", &GitError{Args: args, Output: string(out), Err: err}
	}
	return strings.TrimSpace(string(out)), nil
}

// runWithEnv executes a git command with extra environment variables appended
// to the current process environment.
func (r *Repo) runWithEnv(extraEnv []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.root
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", &GitError{Args: args, Output: string(out), Err: err}
	}
	return strings.TrimSpace(string(out)), nil
}
