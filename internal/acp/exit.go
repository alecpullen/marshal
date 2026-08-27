package acp

import (
	"context"
	"encoding/json"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/pipeline"
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
	Verify func(ctx context.Context, dir string) (pipeline.VerifyResult, error)
}

// ExitManager serves the exit-path methods that apply to any session,
// isolated or not.
type ExitManager struct {
	lookup func(string) (*ExitRuntime, bool)
	git    exitGitOps
	verify func(context.Context, string) (pipeline.VerifyResult, error)
}

func NewExitManager(cfg ExitManagerConfig) *ExitManager {
	git := cfg.Git
	if git == nil {
		git = worktree.CLIGitOps{}
	}
	verify := cfg.Verify
	if verify == nil {
		verify = func(ctx context.Context, dir string) (pipeline.VerifyResult, error) {
			r := pipeline.ResolveVerifyCommands(dir, "", "")
			if !r.Known {
				// Nothing confidently resolvable: report skipped rather
				// than inventing commands that might do anything.
				return pipeline.VerifyResult{Skipped: true}, nil
			}
			v := pipeline.Verifier{Build: r.Build, Test: r.Test, Runner: pipeline.CLICommandRunner{}}
			return v.Run(ctx, dir)
		}
	}
	return &ExitManager{lookup: cfg.Lookup, git: git, verify: verify}
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

// maxVerifyOutput bounds the verify output returned over the wire. A
// failing suite can emit megabytes, and the operator needs the tail to
// act on, not the whole thing.
const maxVerifyOutput = 64 << 10

// VerifyReply is the wire shape of a gate result.
//
// OK and Skipped are distinct: a skipped gate has proved nothing, so it
// blocks and requires an override. Collapsing it into OK would make the
// guarantee conditional in a way invisible to whoever reads the PR.
type VerifyReply struct {
	OK            bool   `json:"ok"`
	Skipped       bool   `json:"skipped"`
	FailedCommand string `json:"failedCommand,omitempty"`
	Output        string `json:"output,omitempty"`
}

// Verify handles session/verify: it runs the repository's build and test
// commands in the session's working tree.
//
// This must run agent-side. The code and the toolchain are inside the
// container; the bridge has neither.
func (m *ExitManager) Verify(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := decodeParams(params, &p, "session/verify"); err != nil {
		return nil, err
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok || rt == nil {
		return nil, serverErrorf("unknown session %s", p.SessionID)
	}

	res, err := m.verify(ctx, rt.Dir)
	if err != nil {
		return nil, serverErrorf("verify: %v", err)
	}
	return VerifyReply{
		OK:            res.OK,
		Skipped:       res.Skipped,
		FailedCommand: res.FailedCommand,
		Output:        tailBytes(res.Output, maxVerifyOutput),
	}, nil
}

// tailBytes returns the last n bytes of s, prefixed with a truncation
// marker. The tail matters more than the head: a build failure's cause
// is at the end.
func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…(truncated)…\n" + s[len(s)-n:]
}
