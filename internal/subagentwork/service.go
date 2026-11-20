package subagentwork

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"marshal/internal/worktree"
)

// subagentBranchPrefix is the branch namespace owned by this package. Every
// owned child branch is derived from it plus the stable task key, so a repeated
// admission derives the same branch and can never mint a second one.
const subagentBranchPrefix = "marshal/subagent/"

// Git is the git seam the service needs: the existing worktree seam plus the
// identity and history reads a durable record requires. Every method takes the
// working directory it runs in, and none of them runs shell-interpolated code.
type Git interface {
	worktree.GitOps
	// RepositoryID returns the canonical absolute common git directory of
	// dir's repository: the value a record binds its ownership to.
	RepositoryID(dir string) (string, error)
	// ResolveCommit resolves an exact existing commit object id.
	ResolveCommit(dir, oid string) (string, error)
	// CommitIDs lists the commits reachable from to but not from.
	CommitIDs(dir, from, to string) ([]string, error)
	// ChangedPaths lists the paths that differ between two commits.
	ChangedPaths(dir, from, to string) ([]string, error)
	// IsAncestor reports whether older is an ancestor of newer.
	IsAncestor(dir, older, newer string) (bool, error)
}

// Option configures a Service at construction. Options are instance-scoped:
// there is no package-global factory, so two services in one process never
// share an observer or a store.
type Option func(*Service)

// WithRecordObserver installs an observer. It is called with a deep copy of a
// record after each successful write or read reconciliation, outside any
// store or service lock, so it can publish state or append to a log without
// risking a deadlock or mutating what was stored.
func WithRecordObserver(observer func(Record)) Option {
	return func(s *Service) { s.observer = observer }
}

// Service allocates and inspects owned subagent workspaces. It holds a Store
// and a Git seam and nothing else: no command supervisor, no model client and
// no transport, so constructing it cannot start work.
type Service struct {
	store    Store
	git      Git
	observer func(Record)
}

// NewService builds a workspace service over store and git. Both are required:
// a store that cannot persist cannot settle a retained task, and a git seam
// that cannot name a repository cannot prove ownership.
func NewService(store Store, git Git, opts ...Option) *Service {
	s := &Service{store: store, git: git}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// taskKeyPattern is the shape of a task key this package accepts: generated,
// safe, opaque text. A key becomes a branch component and a directory name, so
// anything that needs quoting, escaping or filesystem interpretation ("..", a
// path separator, a git-ref metacharacter, a leading dash, a trailing ".lock")
// is refused rather than sanitised — two different keys must never collapse to
// one workspace.
var taskKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// validTaskKey reports whether key may name an owned workspace.
func validTaskKey(key string) bool {
	if !taskKeyPattern.MatchString(key) {
		return false
	}
	if strings.Contains(key, "..") || strings.HasSuffix(key, ".lock") {
		return false
	}
	return true
}

// Prepare allocates (or re-observes) the workspace one task owns. It is
// idempotent per (owner, task key, invocation): an existing record is returned
// with created == false semantics — the same record, never a second child.
//
// Nothing is allocated before the record's intent is durable, so a crash in the
// middle of creation leaves an identifiable record rather than an unowned
// worktree. A failure after the worktree directory is created records the
// failure and retains the artifacts; it never deletes or reallocates them.
func (s *Service) Prepare(ctx context.Context, req PrepareRequest) (Record, error) {
	mode, err := prepareMode(req)
	if err != nil {
		return Record{}, err
	}
	if err := validatePrepareRequest(req); err != nil {
		return Record{}, err
	}
	if mode == ModeShared {
		return s.prepareShared(ctx, req)
	}
	return s.prepareIsolated(ctx, req)
}

func prepareMode(req PrepareRequest) (Mode, error) {
	switch req.Mode {
	case "", ModeIsolated:
		return ModeIsolated, nil
	case ModeShared:
		return ModeShared, nil
	default:
		return "", fmt.Errorf("subagentwork: unknown workspace mode %q", string(req.Mode))
	}
}

func validatePrepareRequest(req PrepareRequest) error {
	if req.OwnerSessionID == "" {
		return fmt.Errorf("subagentwork: prepare needs an owner session")
	}
	if !validTaskKey(req.TaskKey) {
		return fmt.Errorf("subagentwork: task key %q is not generated safe opaque text", req.TaskKey)
	}
	if req.InvocationID == "" {
		return fmt.Errorf("subagentwork: prepare needs an invocation id")
	}
	if req.ProjectRoot == "" {
		return fmt.Errorf("subagentwork: prepare needs a project root")
	}
	if req.ParentRoot == "" {
		return fmt.Errorf("subagentwork: prepare needs the parent's active checkout")
	}
	return nil
}

// prepareShared binds a child to the captured parent checkout without touching
// git. A non-git directory is a valid shared workspace: shared mode makes no
// history promises, which is exactly why it is the supported mode there.
func (s *Service) prepareShared(ctx context.Context, req PrepareRequest) (Record, error) {
	if req.BaseCommit != "" {
		return Record{}, fmt.Errorf("subagentwork: shared workspace mode does not accept a base commit")
	}
	root, err := canonicalDir(req.ParentRoot)
	if err != nil {
		return Record{}, fmt.Errorf("subagentwork: shared workspace root: %w", err)
	}
	projectRoot, err := canonicalDir(req.ProjectRoot)
	if err != nil {
		return Record{}, fmt.Errorf("subagentwork: shared project root: %w", err)
	}
	identity, err := worktree.CaptureCheckoutIdentity(s.git, root)
	if err != nil {
		// Not a git repository, which shared mode supports: the record keeps
		// the canonical root and leaves the git fields empty rather than
		// guessing them.
		identity = worktree.CheckoutIdentity{Root: root}
	}
	target := Target{
		Root:       identity.Root,
		Repository: identity.Repository,
		Branch:     identity.Branch,
		BaseSHA:    identity.Head,
	}
	rec := Record{
		Version:        RecordVersion,
		OwnerSessionID: req.OwnerSessionID,
		TaskKey:        req.TaskKey,
		InvocationID:   req.InvocationID,
		Binding: Binding{
			Mode:        ModeShared,
			Root:        identity.Root,
			ProjectRoot: projectRoot,
			Repository:  identity.Repository,
			Branch:      identity.Branch,
			BaseSHA:     identity.Head,
		},
		Target: target,
		// A shared child owns no artifact of its own, so there is nothing to
		// retain or clean up later.
		Outcome:      OutcomePrepared,
		CleanupState: CleanupComplete,
	}
	stored, _, err := s.store.Create(ctx, rec)
	if err != nil {
		return Record{}, fmt.Errorf("subagentwork: record shared workspace: %w", err)
	}
	// A duplicate admission publishes the reconciliation it read, so an
	// observer sees the authoritative record either way.
	s.publish(stored)
	return stored, nil
}

// prepareIsolated allocates an owned worktree from an exact commit. The intent
// record is written before git runs, and the parent's active checkout — not the
// project root — is the git and seeding source, so a parent that is itself in a
// worktree hands its child that worktree's commit.
func (s *Service) prepareIsolated(ctx context.Context, req PrepareRequest) (Record, error) {
	if !req.IsolatedSupported {
		return Record{}, fmt.Errorf("subagentwork: this execution backend cannot host a linked git worktree, so isolated dispatch is unavailable")
	}
	parentRoot, err := canonicalDir(req.ParentRoot)
	if err != nil {
		return Record{}, fmt.Errorf("subagentwork: parent checkout: %w", err)
	}
	projectRoot, err := canonicalDir(req.ProjectRoot)
	if err != nil {
		return Record{}, fmt.Errorf("subagentwork: project root: %w", err)
	}
	parent, err := worktree.CaptureCheckoutIdentity(s.git, parentRoot)
	if err != nil {
		return Record{}, fmt.Errorf("subagentwork: isolated dispatch needs a git parent checkout: %w", err)
	}

	baseRef := parent.Head
	if req.BaseCommit != "" {
		baseRef = req.BaseCommit
	}
	base, err := s.git.ResolveCommit(parentRoot, baseRef)
	if err != nil {
		return Record{}, fmt.Errorf("subagentwork: base commit %q is not an existing commit in this repository: %w", baseRef, err)
	}

	worktreesDir, err := worktree.AgentDir(projectRoot)
	if err != nil {
		return Record{}, fmt.Errorf("subagentwork: %w", err)
	}
	branch := subagentBranchPrefix + req.TaskKey
	root := worktree.WorktreePath(worktreesDir, branch)

	plan := worktree.SetupPlan{Hooks: req.Setup.Config.SetupHooks}
	intent := Record{
		Version:        RecordVersion,
		OwnerSessionID: req.OwnerSessionID,
		TaskKey:        req.TaskKey,
		InvocationID:   req.InvocationID,
		Binding: Binding{
			Mode:        ModeIsolated,
			Root:        root,
			ProjectRoot: projectRoot,
			Repository:  parent.Repository,
			Branch:      branch,
			BaseSHA:     base,
		},
		// The target is the captured parent checkout. An explicit base commit
		// changes the child's starting point, never where its work lands.
		Target: Target{
			Root:       parent.Root,
			Repository: parent.Repository,
			Branch:     parent.Branch,
			BaseSHA:    parent.Head,
		},
		SetupPlan:    plan,
		Outcome:      OutcomePreparing,
		CleanupState: CleanupRetained,
	}

	stored, created, err := s.store.Create(ctx, intent)
	if err != nil {
		return Record{}, fmt.Errorf("subagentwork: record workspace intent: %w", err)
	}
	if !created {
		// A repeated admission observes the allocation it already has. It must
		// not create a second branch, and it must not repair a partial one
		// silently: the record is the evidence the caller resolves it from.
		s.publish(stored)
		return stored, nil
	}
	s.publish(stored)

	wt, err := worktree.EnsureWorktree(s.git, parentRoot, worktreesDir, branch, base, req.Setup)
	if err != nil {
		return s.failPrepared(ctx, stored, err)
	}
	owned := worktree.CheckoutIdentity{
		Root:       worktree.CanonicalPath(wt.Path),
		Repository: stored.Binding.Repository,
		Branch:     branch,
	}
	if err := worktree.CheckCheckoutIdentity(s.git, owned); err != nil {
		return s.failPrepared(ctx, stored, err)
	}
	if wt.Fresh && wt.Base != "" && wt.Base != base {
		return s.failPrepared(ctx, stored, fmt.Errorf("subagentwork: worktree %s was created at %s, not the recorded base %s", owned.Root, wt.Base, base))
	}
	// Recheck the parent identity once more: if the parent checkout was
	// replaced while the child was being created, the record's target would
	// describe a checkout that no longer exists.
	if id, err := worktree.CaptureCheckoutIdentity(s.git, parentRoot); err != nil || id.Repository != parent.Repository {
		return s.failPrepared(ctx, stored, fmt.Errorf("subagentwork: parent checkout %s changed while the child was created", parentRoot))
	}

	prepared := stored
	prepared.Binding.Root = owned.Root
	prepared.Outcome = OutcomePrepared
	if wt.Fresh {
		prepared.SetupPlan = wt.SetupPlan
		prepared.SetupWarnings = wt.SeedWarnings
	}
	written, err := s.store.CompareAndSwap(ctx, prepared, stored.Revision)
	if err != nil {
		// The workspace exists; report it with the write failure rather than
		// deleting what was just built.
		return prepared, fmt.Errorf("subagentwork: record prepared workspace: %w", err)
	}
	s.publish(written)
	return written, nil
}

// failPrepared records an allocation failure on the intent record, retaining
// the owned artifacts it names. It never removes the branch or worktree: the
// caller decides what to salvage.
func (s *Service) failPrepared(ctx context.Context, stored Record, cause error) (Record, error) {
	failed := stored
	failed.Outcome = OutcomeFailed
	failed.SourceError = cause.Error()
	written, err := s.store.CompareAndSwap(ctx, failed, stored.Revision)
	if err != nil {
		// The intent record is still the evidence; returning it with the
		// allocation error is what lets a caller report retained artifacts.
		cause = fmt.Errorf("%w (also failed to record the workspace failure: %v)", cause, err)
		s.publish(failed)
		return failed, cause
	}
	s.publish(written)
	return written, cause
}

// Complete records how a child's run ended and re-observes the workspace for
// the evidence a reviewer needs. The execution outcome is exactly what the
// caller reports: a failed, stopped or partial run is never promoted to
// success because its workspace happens to contain commits.
func (s *Service) Complete(ctx context.Context, ownerSessionID, taskKey string, completion Completion) (Record, error) {
	if ownerSessionID == "" || taskKey == "" {
		return Record{}, fmt.Errorf("subagentwork: complete needs an owner session and a task key")
	}
	outcome := completion.Outcome
	if outcome == "" {
		return Record{}, fmt.Errorf("subagentwork: complete needs an execution outcome")
	}
	if !validOutcome(outcome) {
		return Record{}, fmt.Errorf("subagentwork: unknown execution outcome %q", outcome)
	}
	rec, err := s.store.Get(ctx, ownerSessionID, taskKey)
	if err != nil {
		return Record{}, err
	}
	rec.Outcome = outcome
	if completion.ReportMessageID > 0 {
		rec.ReportMessageID = completion.ReportMessageID
	}
	if completion.Verification != nil {
		rec.Verification = cloneVerification(completion.Verification)
	}
	rec = s.observe(rec)

	written, err := s.store.CompareAndSwap(ctx, rec, rec.Revision)
	if err != nil {
		return Record{}, fmt.Errorf("subagentwork: record completion: %w", err)
	}
	s.publish(written)
	return written, nil
}

// observe fills the record's evidence from the workspace as it actually is now.
// Every value it cannot establish stays unknown: an unreachable or unowned
// workspace leaves the dirty flag nil and the source error set, never "clean".
func (s *Service) observe(rec Record) Record {
	if rec.Binding.Mode != ModeIsolated {
		// A shared child owns no branch and no commits: its edits are the
		// parent's checkout, so reporting them as its own evidence would
		// invite a reviewer to integrate work that is already there.
		rec.Dirty = nil
		return rec
	}
	root := rec.Binding.Root
	if err := worktree.CheckCheckoutIdentity(s.git, worktree.CheckoutIdentity{
		Root:       root,
		Repository: rec.Binding.Repository,
		Branch:     rec.Binding.Branch,
	}); err != nil {
		rec.SourceError = err.Error()
		rec.Dirty = nil
		return rec
	}
	head, err := s.git.RevParse(root, "HEAD")
	if err != nil {
		rec.SourceError = fmt.Sprintf("read workspace HEAD: %v", err)
		rec.Dirty = nil
		return rec
	}
	rec.Candidate = head
	rec.Commits = nil
	rec.ChangedPaths = nil
	if head != rec.Binding.BaseSHA {
		commits, err := s.git.CommitIDs(root, rec.Binding.BaseSHA, head)
		if err != nil {
			rec.SourceError = fmt.Sprintf("read workspace commits: %v", err)
			rec.Dirty = nil
			return rec
		}
		changed, err := s.git.ChangedPaths(root, rec.Binding.BaseSHA, head)
		if err != nil {
			rec.SourceError = fmt.Sprintf("read workspace changed paths: %v", err)
			rec.Dirty = nil
			return rec
		}
		rec.Commits = cloneStrings(commits)
		rec.ChangedPaths = cloneStrings(changed)
	}
	dirty, err := s.git.IsDirty(root)
	if err != nil {
		rec.SourceError = fmt.Sprintf("read workspace dirtiness: %v", err)
		rec.Dirty = nil
		return rec
	}
	rec.Dirty = &dirty
	rec.SourceError = ""
	return rec
}

// canonicalDir returns dir as a canonical absolute path, requiring that it
// exists and is a directory.
func canonicalDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("%s: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%s: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	return worktree.CanonicalPath(abs), nil
}

// publish hands the observer a deep copy of a record that was written or
// reconciled successfully. It is called with no lock held, so an observer that
// blocks or inspects the store cannot deadlock the service.
func (s *Service) publish(rec Record) {
	if s.observer == nil {
		return
	}
	s.observer(rec.clone())
}
