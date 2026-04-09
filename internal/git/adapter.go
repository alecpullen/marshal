package git

import (
	"github.com/alecpullen/marshal/internal/loop"
)

// Adapter wraps Git to implement loop.GitLayer interface.
type Adapter struct {
	git *Git
}

// NewAdapter creates an adapter for the loop.
func NewAdapter(g *Git) loop.GitLayer {
	return &Adapter{git: g}
}

// CreateIsolationBranch creates and checks out a new branch from HEAD.
func (a *Adapter) CreateIsolationBranch(name string) error {
	return a.git.CreateIsolationBranch(name)
}

// GetDiff returns the diff between current state and HEAD.
func (a *Adapter) GetDiff() (string, error) {
	return a.git.GetDiff()
}

// StageAndCommit stages all changes and commits with message.
func (a *Adapter) StageAndCommit(message string) error {
	return a.git.StageAndCommit(message)
}

// HardResetToHead resets to HEAD, discarding changes.
func (a *Adapter) HardResetToHead() error {
	return a.git.HardResetToHead()
}

// DeleteBranch deletes a branch (force if needed).
func (a *Adapter) DeleteBranch(name string) error {
	return a.git.DeleteBranch(name)
}

// CheckoutBranch switches to an existing branch.
func (a *Adapter) CheckoutBranch(name string) error {
	return a.git.CheckoutBranch(name)
}

// MergeBranch merges branch into base branch with message.
func (a *Adapter) MergeBranch(name string, message string) error {
	return a.git.MergeBranch(name, message)
}
