package acp

import (
	"context"
	"encoding/json"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/worktree"
)

// ExitRuntime is what an exit-path handler needs: the session and the
// directory its work lives in.
//
// This deliberately does not reuse WorktreeRuntime. Worktree handlers
// route through isolated(), which requires git-worktree isolation — a
// git-sourced agent has a plain clone and no worktree, so those handlers
// would refuse it.
type ExitRuntime struct {
	State *session.State
	Dir   string
}

// exitGitOps is the subset of worktree.GitOps the exit path needs.
// Narrowing it keeps the test fake small and honest.
type exitGitOps interface {
	IsDirty(dir string) (bool, error)
	CommitAll(dir, message string) (string, error)
}

type ExitManagerConfig struct {
	Lookup func(sessionID string) (*ExitRuntime, bool)
	Git    exitGitOps
}

// ExitManager serves the exit-path methods that apply to any session,
// isolated or not.
type ExitManager struct {
	lookup func(string) (*ExitRuntime, bool)
	git    exitGitOps
}

func NewExitManager(cfg ExitManagerConfig) *ExitManager {
	git := cfg.Git
	if git == nil {
		git = worktree.CLIGitOps{}
	}
	return &ExitManager{lookup: cfg.Lookup, git: git}
}

type commitParams struct {
	SessionID string `json:"sessionId"`
	Message   string `json:"message"`
}

// CommitResult reports what session/commit did. Clean means the tree had
// nothing to commit — the agent had already committed its own work,
// which is the normal case rather than an error.
type CommitResult struct {
	Commit string `json:"commit,omitempty"`
	Clean  bool   `json:"clean"`
}

// Commit handles session/commit: it stages and commits everything in the
// session's working tree.
//
// session/merge cannot serve this purpose — it commits AND merges into a
// base checkout, and a git-remote agent has nothing to merge into.
func (m *ExitManager) Commit(_ context.Context, params json.RawMessage) (any, error) {
	var p commitParams
	if err := decodeParams(params, &p, "session/commit"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Message) == "" {
		return nil, invalidParamsError("message is required")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok || rt == nil {
		return nil, serverErrorf("unknown session %s", p.SessionID)
	}

	dirty, err := m.git.IsDirty(rt.Dir)
	if err != nil {
		return nil, serverErrorf("check working tree: %v", err)
	}
	if !dirty {
		return CommitResult{Clean: true}, nil
	}
	sha, err := m.git.CommitAll(rt.Dir, p.Message)
	if err != nil {
		return nil, serverErrorf("commit: %v", err)
	}
	return CommitResult{Commit: sha}, nil
}
