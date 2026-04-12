package git

import (
	"fmt"
	"time"
)

// stagingBranchName formats the session staging branch name.
func stagingBranchName() string {
	return "marshal/session-" + time.Now().UTC().Format("20060102-1504")
}

// taskBranchName formats the per-task isolation branch name.
func taskBranchName(id string) string {
	return "marshal/task-" + id
}

// Session represents one marshal working session: a target branch the user
// started on, a staging branch where task work accumulates, and the SHA of
// the target branch at session start.
//
// The target branch is never touched except by Ship. Every task runs on a
// short-lived marshal/task-<id> branch off the staging branch.
type Session struct {
	repo           *Repo
	TargetBranch   string
	StagingBranch  string
	TargetStartSHA string
	keepBranch     bool
}

// SessionOptions controls Session behaviour.
type SessionOptions struct {
	// KeepBranch suppresses deletion of the staging branch on Teardown.
	KeepBranch bool
}

// NewSession creates a Session for the given repo. Call Start to initialise
// the branch hierarchy.
func NewSession(repo *Repo, opts SessionOptions) *Session {
	return &Session{repo: repo, keepBranch: opts.KeepBranch}
}

// Start records the current branch as the target, creates the session staging
// branch off its HEAD, and checks it out. It is idempotent: calling Start
// twice is an error.
func (s *Session) Start() error {
	if s.TargetBranch != "" {
		return fmt.Errorf("session already started on branch %q", s.TargetBranch)
	}

	branch, err := s.repo.CurrentBranch()
	if err != nil {
		return fmt.Errorf("reading current branch: %w", err)
	}
	sha, err := s.repo.HeadSHA()
	if err != nil {
		return fmt.Errorf("reading HEAD SHA: %w", err)
	}

	staging := stagingBranchName()
	if err := s.repo.CheckoutNewBranch(staging, sha); err != nil {
		return fmt.Errorf("creating staging branch %q: %w", staging, err)
	}

	s.TargetBranch = branch
	s.StagingBranch = staging
	s.TargetStartSHA = sha
	return nil
}

// StagingHEAD returns the current HEAD SHA of the session staging branch.
func (s *Session) StagingHEAD() (string, error) {
	return s.repo.HeadSHA()
}

// BeginTask creates a new task isolation branch off the current staging HEAD
// and checks it out. The returned TaskTx is used to commit, merge, or abandon
// the task.
func (s *Session) BeginTask(id string) (*TaskTx, error) {
	stagingSHA, err := s.repo.HeadSHA()
	if err != nil {
		return nil, fmt.Errorf("reading staging HEAD: %w", err)
	}
	branch := taskBranchName(id)
	if err := s.repo.CheckoutNewBranch(branch, stagingSHA); err != nil {
		return nil, fmt.Errorf("creating task branch %q: %w", branch, err)
	}
	return &TaskTx{
		ID:               id,
		Branch:           branch,
		ParentStagingSHA: stagingSHA,
		repo:             s.repo,
		session:          s,
	}, nil
}

// Ship squash-merges the staging branch into the target branch with message,
// returns the new target SHA, then starts a fresh staging branch from the new
// target HEAD so chatting can continue.
//
// If message is empty, a default message is used.
func (s *Session) Ship(message string) (string, error) {
	if message == "" {
		message = fmt.Sprintf("marshal: ship session %s", s.StagingBranch)
	}

	staging := s.StagingBranch

	// Switch to target branch.
	if err := s.repo.Checkout(s.TargetBranch); err != nil {
		return "", fmt.Errorf("checkout target %q: %w", s.TargetBranch, err)
	}

	// Squash-merge the staging branch.
	if err := s.repo.MergeSquash(staging, message); err != nil {
		// Roll back to staging on failure.
		_ = s.repo.Checkout(staging)
		return "", fmt.Errorf("squash-merging %q into %q: %w", staging, s.TargetBranch, err)
	}

	newTargetSHA, err := s.repo.HeadSHA()
	if err != nil {
		return "", fmt.Errorf("reading new target SHA: %w", err)
	}

	// Start a fresh staging branch from the new target HEAD.
	newStaging := stagingBranchName()
	if err := s.repo.CheckoutNewBranch(newStaging, newTargetSHA); err != nil {
		return newTargetSHA, fmt.Errorf("creating fresh staging branch: %w", err)
	}

	// Clean up the old staging branch.
	_ = s.repo.DeleteBranch(staging)

	s.StagingBranch = newStaging
	return newTargetSHA, nil
}

// Teardown cleans up the session. It checks out the target branch and, unless
// KeepBranch was set, deletes the staging branch. Safe to call on a
// partially-started session.
func (s *Session) Teardown() error {
	if s.TargetBranch == "" {
		return nil // never started
	}

	if err := s.repo.Checkout(s.TargetBranch); err != nil {
		return fmt.Errorf("checkout target %q on teardown: %w", s.TargetBranch, err)
	}

	if !s.keepBranch && s.StagingBranch != "" {
		if err := s.repo.DeleteBranch(s.StagingBranch); err != nil {
			return fmt.Errorf("deleting staging branch %q: %w", s.StagingBranch, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// TaskTx
// ---------------------------------------------------------------------------

// TaskTx is a short-lived handle for a single task's isolation branch.
// It is created by Session.BeginTask and used by the loop engine.
type TaskTx struct {
	// ID is the task identifier (used in branch names and ledger entries).
	ID string
	// Branch is the full git branch name (marshal/task-<id>).
	Branch string
	// ParentStagingSHA is the staging HEAD SHA at the moment this task
	// branched off. Used as the diff base for critic review.
	ParentStagingSHA string

	repo    *Repo
	session *Session
}

// Commit stages all working-tree changes and commits them on the task branch.
// The co-authored-by trailer is appended automatically.
func (t *TaskTx) Commit(message string) error {
	return t.repo.CommitAll(message)
}

// Diff returns a -U1 unified diff of all changes on this task branch relative
// to the parent staging SHA.
func (t *TaskTx) Diff() (string, error) {
	return t.repo.Diff(DiffOpts{Base: t.ParentStagingSHA})
}

// Merge squash-merges this task branch into the session staging branch and
// deletes the task branch. message is the squash commit message.
// On error the staging branch is left in whatever state git left it.
func (t *TaskTx) Merge(message string) error {
	// Check out staging.
	if err := t.repo.Checkout(t.session.StagingBranch); err != nil {
		return fmt.Errorf("checkout staging for merge: %w", err)
	}

	// Squash-merge the task branch.
	if err := t.repo.MergeSquash(t.Branch, message); err != nil {
		return fmt.Errorf("squash-merging task %q: %w", t.ID, err)
	}

	// Delete the task branch.
	return t.repo.DeleteBranch(t.Branch)
}

// Abandon checks out the staging branch and deletes the task branch without
// merging any of its changes.
func (t *TaskTx) Abandon() error {
	if err := t.repo.Checkout(t.session.StagingBranch); err != nil {
		return fmt.Errorf("checkout staging for abandon: %w", err)
	}
	return t.repo.DeleteBranch(t.Branch)
}
