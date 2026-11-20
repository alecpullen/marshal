package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"marshal/internal/agent/swarm"
	"marshal/internal/app/session"
	"marshal/internal/subagentwork"
	"marshal/internal/tools/native"
	"marshal/internal/worktree"
)

// subagentIdleGate is the app adapter of the workspace idle protocol. An
// owned-child lifecycle mutation (an integration or a discard) may only rewrite
// a target when nothing else is using it, and "nothing else" spans four
// independent scopes, each of which this gate closes in turn:
//
//  1. the runtime's stable write gate, so no local writer of this conversation
//     is mid-operation;
//  2. the conversation's live children at the target, so a child between two
//     tool calls still counts as a user;
//  3. the conversation's managed commands, through the command service's root
//     reservations, so a command admitted before the freeze cannot start after
//     it and a new one is refused;
//  4. every other Marshal process, through the sorted cross-process exclusive
//     target locks — which is also where a crashed owner's retained unknown use
//     refuses the mutation.
//
// It releases in reverse order, so a partially acquired gate never leaves a
// lock behind. An implementation of the README's IdleGate: P3 Task 2 supplies
// the MutationDeps consumer.
type subagentIdleGate struct {
	state *session.State
	lock  *swarm.WriteLock
	jobs  *native.JobManager
	git   worktree.GitOps

	mu           sync.Mutex
	repositories map[string]string
}

// newSubagentIdleGate builds the adapter for one conversation owner. Every
// dependency is instance-scoped: the write gate and command service belong to
// the conversation, and the git seam is how a root's canonical repository is
// resolved.
func newSubagentIdleGate(state *session.State, lock *swarm.WriteLock, jobs *native.JobManager, git worktree.GitOps) *subagentIdleGate {
	if git == nil {
		git = worktree.CLIGitOps{}
	}
	return &subagentIdleGate{
		state:        state,
		lock:         lock,
		jobs:         jobs,
		git:          git,
		repositories: make(map[string]string),
	}
}

// AcquireIdle takes the exclusive use of every root and returns its release, or
// an error naming the user that is still live. Roots are canonicalized, sorted
// and deduplicated before anything is acquired, so two callers naming the same
// roots in opposite orders cannot deadlock and an alias spelling of one root is
// the same root.
func (g *subagentIdleGate) AcquireIdle(ctx context.Context, owner string, roots []string) (func(), error) {
	ordered := canonicalRoots(roots)
	if len(ordered) == 0 {
		return func() {}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var releases []func()
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	fail := func(err error) (func(), error) {
		releaseAll()
		return nil, err
	}

	// 1. The runtime's stable write gate: a local writer of this conversation
	// must not be mid-operation at the target.
	if g.lock != nil {
		releaseWrite, err := g.lock.AcquireContext(ctx)
		if err != nil {
			return nil, err
		}
		releases = append(releases, releaseWrite)
	}

	// 2. Live children at the target, including a child that is between tool
	// calls and therefore holds no command of its own right now.
	if taskKey, busy := g.liveChildAt(ordered); busy {
		return fail(fmt.Errorf("%w: %s still has a live child (%s)", native.ErrWorkspaceBusy, strings.Join(ordered, ", "), taskKey))
	}

	// 3. Freeze the command roots, so no command is admitted into a workspace
	// this mutation now believes idle.
	if g.jobs != nil {
		releaseCommands, err := g.jobs.AcquireIdleWorkspaces(ctx, ordered)
		if err != nil {
			return fail(err)
		}
		releases = append(releases, releaseCommands)
	}

	// 4. Cross-process exclusivity, in canonical sorted order.
	for _, root := range ordered {
		repository, err := g.repository(root)
		if err != nil {
			return fail(err)
		}
		if repository == "" {
			// Not a git checkout: there is no cross-process participant to
			// coordinate with, and no target lock to take.
			continue
		}
		releaseTarget, err := worktree.AcquireTargetLock(ctx, repository, root)
		if err != nil {
			return fail(fmt.Errorf("%w: %w", native.ErrWorkspaceBusy, err))
		}
		releases = append(releases, releaseTarget)
	}

	var once sync.Once
	return func() {
		once.Do(releaseAll)
	}, nil
}

// workspaceUse is the shared root-use callback this adapter installs on a native
// registration, so every direct tool operation and managed command of that
// registration participates in the same protocol the lifecycle side takes
// exclusively. A root that is no git checkout acquires nothing.
func (g *subagentIdleGate) workspaceUse(ctx context.Context, root, kind string) (*worktree.UseLease, error) {
	repository, err := g.repository(root)
	if err != nil {
		return nil, err
	}
	if repository == "" {
		return nil, nil
	}
	owner := ""
	if g.state != nil {
		owner = g.state.SessionID()
	}
	return worktree.AcquireWorkspaceUse(ctx, repository, root, worktree.UseIdentity{
		OwnerSessionID: owner,
		Kind:           kind,
	})
}

// liveChildAt reports the task key of a live child bound to, or destined for,
// one of roots. A prepared or running record is a child whose model is alive:
// it holds no command between tool calls, so only its record can say so.
func (g *subagentIdleGate) liveChildAt(roots []string) (string, bool) {
	if g.state == nil {
		return "", false
	}
	wanted := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		wanted[root] = struct{}{}
	}
	for _, record := range g.state.SubagentWorkspaces() {
		switch record.Outcome {
		case subagentwork.OutcomePrepared, subagentwork.OutcomeRunning:
		default:
			continue
		}
		for _, root := range []string{record.Binding.Root, record.Target.Root} {
			root = strings.TrimSpace(root)
			if root == "" {
				continue
			}
			if _, ok := wanted[worktree.CanonicalPath(root)]; ok {
				return record.TaskKey, true
			}
		}
	}
	return "", false
}

// repository returns the canonical repository identity of root, or "" when root
// is not a git checkout. The answer is cached: it cannot change for the life of
// a checkout, and the lookup shells out to git.
func (g *subagentIdleGate) repository(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", nil
	}
	key := worktree.CanonicalPath(root)
	g.mu.Lock()
	if repository, ok := g.repositories[key]; ok {
		g.mu.Unlock()
		return repository, nil
	}
	g.mu.Unlock()

	repository := ""
	if identifier, ok := g.git.(interface {
		RepositoryID(dir string) (string, error)
	}); ok {
		if id, err := identifier.RepositoryID(root); err == nil {
			repository = id
		}
	}
	g.mu.Lock()
	g.repositories[key] = repository
	g.mu.Unlock()
	return repository, nil
}

// canonicalRoots canonicalizes, deduplicates and sorts a root list. The
// protocol always acquires multiple roots in this order.
func canonicalRoots(roots []string) []string {
	seen := make(map[string]struct{}, len(roots))
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		canonical := worktree.CanonicalPath(root)
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	sort.Strings(out)
	return out
}

// workspaceUseForRepository returns the shared root-use callback for one known
// repository — the form a child registration needs, because its dispatcher
// already recorded exactly which repository every one of its roots belongs to.
func workspaceUseForRepository(repository, ownerSessionID string) func(ctx context.Context, root, kind string) (*worktree.UseLease, error) {
	repository = strings.TrimSpace(repository)
	return func(ctx context.Context, root, kind string) (*worktree.UseLease, error) {
		root = strings.TrimSpace(root)
		if repository == "" || root == "" {
			return nil, nil
		}
		return worktree.AcquireWorkspaceUse(ctx, repository, root, worktree.UseIdentity{
			OwnerSessionID: ownerSessionID,
			Kind:           kind,
		})
	}
}

// lifecycleMutationDeps is the dependency set one parent-only lifecycle mutation
// runs with: this gate for exclusive use of the roots it touches, the exact-root
// capture of the checkout the record named, and the rescan announcement for the
// root an integration changed.
//
// Admit is deliberately absent. It is built per call by the tool's handler from
// that call's own opaque proof, so the decision is always the one the current
// invocation was evaluated under — never a decision borrowed from a previous
// call, and never one this adapter could have made without the live authority.
func (g *subagentIdleGate) lifecycleMutationDeps(commands *conversationCommandService) subagentwork.MutationDeps {
	return subagentwork.MutationDeps{
		Idle:     g,
		Snapshot: subagentSnapshotAt(g.state),
		Rescan: func(root string) {
			// Root-specific, like every other rescan: a live language server is
			// only re-synced for the workspace it actually serves, so an
			// integration into another checkout is not mistaken for a change
			// there.
			if commands != nil {
				commands.rescanLSP(root)
			}
		},
	}
}
