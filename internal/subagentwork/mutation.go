package subagentwork

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"marshal/internal/worktree"
)

// operationTrailer is the merge-message trailer that binds a merge commit in the
// target's history to the durable journal entry that asked for it, so a later
// process can tell which merge belonged to which operation.
const operationTrailer = "Marshal-Subagent-Operation"

// Mutation names exactly what a parent asks to integrate or discard: the owner's
// task key and the exact candidate commit it reviewed. It carries no path,
// branch or repository, so a caller cannot steer the mutation at a checkout the
// record does not own.
type Mutation struct {
	OwnerSessionID string
	TaskKey        string
	ExpectedHead   string
}

// IdleGate acquires exclusive use of every root a mutation touches. A nil gate is
// never permission: the service refuses to mutate without one.
type IdleGate interface {
	// AcquireIdle returns the release function once the owner holds every root,
	// or an error when any of them is busy.
	AcquireIdle(ctx context.Context, owner string, roots []string) (func(), error)
}

// Admission is the opaque proof that a specific effect — the one the effect key
// names — has been authorized. Commit spends it at the accepted mutation
// boundary; Release hands it back unused.
type Admission interface {
	Commit() error
	Release()
}

// MutationDeps is everything a workspace mutation needs from the caller's
// runtime. Idle and Admit are required; Snapshot and Rescan are optional.
type MutationDeps struct {
	Idle IdleGate
	// Snapshot captures the exact root it is given so the mutation is
	// recoverable. It is called with the target root before an integration and
	// with the source root before a discard.
	Snapshot func(ctx context.Context, root string) (string, error)
	// Rescan is told about a target root whose checkout changed.
	Rescan func(root string)
	// Admit authorizes one effect key. It must not be nil for a mutation.
	Admit func(ctx context.Context, effectKey string) (Admission, error)
}

// MutationGit is the git seam a mutation needs: the P2 reads plus the exact merge
// primitives. A backend without them is refused with an explicit error instead
// of a partial mutation.
type MutationGit interface {
	Git
	// MergeExact merges one exact commit with a real merge commit and returns
	// the resulting commit.
	MergeExact(ctx context.Context, dir, candidate, message string) (string, error)
	// MergeState reports a merge in progress (empty means idle).
	MergeState(dir string) (string, error)
	// Parents returns a commit's parents.
	Parents(dir, commit string) ([]string, error)
}

// PreparedMutation is the exact state one mutation would act on, captured under
// the caller's lease. Its effect key binds every field, so a changed record,
// source or target needs a fresh admission.
type PreparedMutation struct {
	RecordRevision uint64
	Repository     string
	SourceRoot     string
	SourceHead     string
	TargetRoot     string
	TargetBranch   string
	TargetHead     string
}

// EffectKey is a deterministic digest of every prepared fact. Two evaluations
// that see the same record, source and target produce the same key; any change
// produces another one.
func (p PreparedMutation) EffectKey() string {
	digest := sha256.New()
	for _, field := range []string{
		strconv.FormatUint(p.RecordRevision, 10),
		p.Repository,
		p.SourceRoot,
		p.SourceHead,
		p.TargetRoot,
		p.TargetBranch,
		p.TargetHead,
	} {
		digest.Write([]byte(field))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// errMutationUnsupported reports a git seam that cannot perform exact merges.
var errMutationUnsupported = errors.New("subagentwork: this git backend cannot perform exact merges, so workspace mutation is unavailable")

// PrepareMutation captures the exact facts a mutation would act on. It reads and
// never writes: an unchanged inspection does not advance the record revision, so
// a caller can compute the effect key before evaluating the request.
func (s *Service) PrepareMutation(ctx context.Context, m Mutation) (PreparedMutation, error) {
	if err := validateMutation(m); err != nil {
		return PreparedMutation{}, err
	}
	rec, err := s.store.Get(ctx, m.OwnerSessionID, m.TaskKey)
	if err != nil {
		return PreparedMutation{}, err
	}
	return s.prepareMutationRecord(rec)
}

// prepareMutationRecord rechecks the record's ownership against the checkouts as
// they are now. Every check is a refusal: a checkout that no longer matches the
// record is never followed.
func (s *Service) prepareMutationRecord(rec Record) (PreparedMutation, error) {
	if rec.Binding.Mode != ModeIsolated {
		return PreparedMutation{}, fmt.Errorf("subagentwork: a %q child works in the parent's checkout and owns no candidate to mutate", string(rec.Binding.Mode))
	}
	if rec.Binding.Root == "" || rec.Binding.Branch == "" {
		return PreparedMutation{}, fmt.Errorf("subagentwork: the record names no owned workspace checkout")
	}
	if rec.Target.Root == "" || rec.Target.Repository == "" {
		return PreparedMutation{}, fmt.Errorf("subagentwork: the record captured no target checkout")
	}
	if err := worktree.CheckCheckoutIdentity(s.git, worktree.CheckoutIdentity{
		Root:       rec.Binding.Root,
		Repository: rec.Binding.Repository,
		Branch:     rec.Binding.Branch,
	}); err != nil {
		return PreparedMutation{}, fmt.Errorf("subagentwork: the child workspace is not the one this record owns: %w", err)
	}
	source, err := worktree.CaptureCheckoutIdentity(s.git, rec.Binding.Root)
	if err != nil {
		return PreparedMutation{}, fmt.Errorf("subagentwork: read the child workspace identity: %w", err)
	}
	target, err := worktree.CaptureCheckoutIdentity(s.git, rec.Target.Root)
	if err != nil {
		return PreparedMutation{}, fmt.Errorf("subagentwork: read the target checkout identity: %w", err)
	}
	if target.Repository != rec.Target.Repository {
		return PreparedMutation{}, fmt.Errorf("subagentwork: the target checkout %s now belongs to repository %s, recorded %s", target.Root, target.Repository, rec.Target.Repository)
	}
	if target.Branch != rec.Target.Branch {
		return PreparedMutation{}, fmt.Errorf("subagentwork: the target checkout %s is on branch %q, recorded %q", target.Root, target.Branch, rec.Target.Branch)
	}
	return PreparedMutation{
		RecordRevision: rec.Revision,
		Repository:     rec.Binding.Repository,
		SourceRoot:     source.Root,
		SourceHead:     source.Head,
		TargetRoot:     target.Root,
		TargetBranch:   target.Branch,
		TargetHead:     target.Head,
	}, nil
}

// Integrate merges the record's exact candidate into the captured target checkout
// with a real merge commit, journalling every step so an interruption recovers one
// truthful result instead of a second merge.
//
// The work is admitted — not assumed: the caller must supply an idle gate and an
// admission function, the effect key is recomputed under the lease, and the
// admission is spent only once the mutation is accepted.
func (s *Service) Integrate(ctx context.Context, m Mutation, deps MutationDeps) (Record, error) {
	if err := validateMutation(m); err != nil {
		return Record{}, err
	}
	if err := requireMutationDeps(deps); err != nil {
		return Record{}, err
	}
	git, ok := s.git.(MutationGit)
	if !ok {
		return Record{}, errMutationUnsupported
	}
	rec, err := s.store.Get(ctx, m.OwnerSessionID, m.TaskKey)
	if err != nil {
		return Record{}, err
	}
	if err := admissibleForMutation(rec, m); err != nil {
		return s.refuse(ctx, rec, KindIntegrate, m, err, "")
	}
	if rec.Integration != nil {
		switch rec.Integration.State {
		case IntegrationIntegrated, IntegrationNoChanges:
			if rec.Integration.ExpectedSource != m.ExpectedHead {
				return s.refuse(ctx, rec, KindIntegrate, m, fmt.Errorf(
					"subagentwork: task %q already recorded candidate %s; %s is a changed task candidate, so dispatch follow-up work under a new task key",
					m.TaskKey, rec.Integration.ExpectedSource, m.ExpectedHead), "")
			}
			// The settled receipt is the answer; only pending cleanup remains.
			return s.resumeCleanup(ctx, git, deps, rec, m)
		case IntegrationDiscarded:
			return s.refuse(ctx, rec, KindIntegrate, m, fmt.Errorf(
				"subagentwork: the candidate for task %q was discarded; dispatch follow-up work under a new task key", m.TaskKey), "")
		}
	}
	return s.attemptIntegration(ctx, git, deps, rec, m)
}

// attemptIntegration performs one guarded integration attempt under the lease.
func (s *Service) attemptIntegration(ctx context.Context, git MutationGit, deps MutationDeps, rec Record, m Mutation) (Record, error) {
	release, err := acquireMutationIdle(ctx, deps.Idle, m.OwnerSessionID, rec)
	if err != nil {
		return rec, err
	}
	defer release()

	prepared, err := s.prepareMutationRecord(rec)
	if err != nil {
		return s.refuse(ctx, rec, KindIntegrate, m, err, "")
	}
	if prepared.SourceHead != m.ExpectedHead {
		return s.refuse(ctx, rec, KindIntegrate, m, fmt.Errorf(
			"subagentwork: the child workspace moved from the reviewed candidate %s to %s", m.ExpectedHead, prepared.SourceHead), "")
	}
	if err := s.requireCleanSource(git, prepared.SourceRoot); err != nil {
		return s.refuse(ctx, rec, KindIntegrate, m, err, "")
	}
	if err := s.requireCleanTarget(git, prepared.TargetRoot); err != nil {
		return s.refuse(ctx, rec, KindIntegrate, m, err, "")
	}

	var snapshot string
	if deps.Snapshot != nil {
		snapshot, err = deps.Snapshot(ctx, prepared.TargetRoot)
		if err != nil {
			return rec, fmt.Errorf("subagentwork: snapshot the target checkout %s: %w", prepared.TargetRoot, err)
		}
	}

	proof, err := s.admitMutation(ctx, deps, prepared.EffectKey())
	if err != nil {
		return rec, err
	}
	defer proof.release()

	// The facts must still be the admitted ones: anything new needs a new proof.
	recheck, err := s.prepareMutationRecord(rec)
	if err != nil {
		return s.refuse(ctx, rec, KindIntegrate, m, err, snapshot)
	}
	if recheck.EffectKey() != prepared.EffectKey() {
		return s.refuse(ctx, rec, KindIntegrate, m, fmt.Errorf(
			"subagentwork: the recorded source or target changed while the mutation was admitted"), snapshot)
	}

	// A candidate equal to its own base carries no history: marking it
	// no_changes never brings the base into the parent implicitly.
	if prepared.SourceHead == rec.Binding.BaseSHA {
		integ := Integration{
			OperationID:    newOperationID(),
			Kind:           KindIntegrate,
			State:          IntegrationNoChanges,
			ExpectedSource: prepared.SourceHead,
			BeforeTarget:   prepared.TargetHead,
			SnapshotHash:   snapshot,
			Reason:         "the candidate is the workspace base, so there is no work to integrate",
		}
		return s.settleIntegrated(ctx, git, deps, rec, integ, prepared.TargetRoot, proof)
	}

	// Already-present ancestry is a distinct idempotent success: the work is in
	// the target, and no new merge is needed or made.
	present, err := s.git.IsAncestor(prepared.TargetRoot, prepared.SourceHead, prepared.TargetHead)
	if err != nil {
		return s.refuse(ctx, rec, KindIntegrate, m, fmt.Errorf("subagentwork: check whether the candidate is already present: %w", err), snapshot)
	}
	if present {
		integ := Integration{
			OperationID:    newOperationID(),
			Kind:           KindIntegrate,
			State:          IntegrationIntegrated,
			ExpectedSource: prepared.SourceHead,
			BeforeTarget:   prepared.TargetHead,
			ResultCommit:   prepared.TargetHead,
			SnapshotHash:   snapshot,
			Reason:         "the candidate is already reachable from the target, so no merge was needed",
		}
		return s.settleIntegrated(ctx, git, deps, rec, integ, prepared.TargetRoot, proof)
	}

	// Journal the intent before any git mutation, so an interruption leaves an
	// identifiable operation rather than an unexplained commit.
	intent := Integration{
		OperationID:    newOperationID(),
		Kind:           KindIntegrate,
		State:          IntegrationIntent,
		ExpectedSource: prepared.SourceHead,
		BeforeTarget:   prepared.TargetHead,
		SnapshotHash:   snapshot,
	}
	journalled, err := s.writeMutation(ctx, rec, intent, CleanupRetained)
	if err != nil {
		return rec, fmt.Errorf("subagentwork: journal the integration intent: %w", err)
	}

	// The accepted mutation boundary: the proof is spent here, immediately
	// before git runs, and never held across the subprocess.
	if err := proof.spend(); err != nil {
		return journalled, fmt.Errorf("subagentwork: commit the mutation admission: %w", err)
	}

	result, mergeErr := git.MergeExact(ctx, prepared.TargetRoot, prepared.SourceHead,
		integrationMessage(intent.OperationID, prepared.SourceHead))
	if mergeErr != nil {
		return s.settleFailedMerge(ctx, git, journalled, intent, mergeErr)
	}
	if deps.Rescan != nil {
		deps.Rescan(prepared.TargetRoot)
	}

	parents, err := git.Parents(prepared.TargetRoot, result)
	if err != nil || !recordsMerge(parents, prepared.TargetHead, prepared.SourceHead) {
		unknown := intent
		unknown.State = IntegrationUnknown
		unknown.ResultCommit = result
		unknown.Reason = fmt.Sprintf("the target advanced to %s but its parents %v do not record this operation", result, parents)
		written, werr := s.writeMutation(ctx, journalled, unknown, CleanupRetained)
		if werr != nil {
			return journalled, fmt.Errorf("subagentwork: journal the unproven merge %s: %w", result, werr)
		}
		return written, fmt.Errorf("subagentwork: the merge result %s does not record the operation's parents", result)
	}

	integrated := intent
	integrated.State = IntegrationIntegrated
	integrated.ResultCommit = result
	return s.settleIntegrated(ctx, git, deps, journalled, integrated, prepared.TargetRoot, proof)
}

// settleFailedMerge classifies a failed merge. Only a merge this operation can
// prove it owns is aborted, and never anyone else's.
func (s *Service) settleFailedMerge(ctx context.Context, git MutationGit, rec Record, intent Integration, cause error) (Record, error) {
	state, stateErr := git.MergeState(rec.Target.Root)
	head, headErr := s.git.RevParse(rec.Target.Root, "HEAD")
	if stateErr != nil {
		unknown := intent
		unknown.State = IntegrationUnknown
		unknown.Reason = fmt.Sprintf("the merge failed (%v) and the target's merge state could not be read: %v", cause, stateErr)
		written, werr := s.writeMutation(ctx, rec, unknown, rec.CleanupState)
		if werr != nil {
			return rec, cause
		}
		return written, cause
	}

	info := worktree.ParseMergeState(state)
	owns := info.HasHead(intent.ExpectedSource) && headErr == nil && head == intent.BeforeTarget
	if !owns {
		failed := intent
		if state == "" {
			// git refused before writing any merge state (an untracked file it
			// would overwrite, for instance): nothing is in progress to undo.
			failed.State = IntegrationRefused
			failed.Reason = cause.Error()
		} else {
			failed.State = IntegrationUnknown
			failed.Reason = fmt.Sprintf("the merge failed (%v) and the target holds a merge this operation cannot prove it owns", cause)
		}
		written, werr := s.writeMutation(ctx, rec, failed, rec.CleanupState)
		if werr != nil {
			return rec, cause
		}
		return written, cause
	}

	// Our own merge: abort it and prove the target returned to exactly where the
	// operation found it.
	if err := git.MergeAbort(rec.Target.Root); err != nil {
		unknown := intent
		unknown.State = IntegrationUnknown
		unknown.Reason = fmt.Sprintf("the conflicting merge could not be aborted: %v", err)
		written, werr := s.writeMutation(ctx, rec, unknown, rec.CleanupState)
		if werr != nil {
			return rec, cause
		}
		return written, cause
	}

	restored, herr := s.git.RevParse(rec.Target.Root, "HEAD")
	after, serr := git.MergeState(rec.Target.Root)
	tracked, terr := s.git.TrackedChanges(rec.Target.Root)
	if herr != nil || serr != nil || terr != nil || restored != intent.BeforeTarget || after != "" || len(tracked) > 0 {
		unknown := intent
		unknown.State = IntegrationUnknown
		unknown.Reason = fmt.Sprintf("the target could not be restored to %s after the conflicting merge (head %q, merge state %q, %d tracked paths)",
			intent.BeforeTarget, restored, after, len(tracked))
		written, werr := s.writeMutation(ctx, rec, unknown, rec.CleanupState)
		if werr != nil {
			return rec, cause
		}
		return written, cause
	}

	conflict := intent
	conflict.State = IntegrationConflict
	conflict.Reason = "conflicting paths: " + strings.Join(info.Conflicts, ", ")
	written, werr := s.writeMutation(ctx, rec, conflict, rec.CleanupState)
	if werr != nil {
		return rec, fmt.Errorf("%w (also failed to journal the conflict: %v)", cause, werr)
	}
	return written, fmt.Errorf("subagentwork: integrating %s into %s conflicts on %s: %w",
		intent.ExpectedSource, intent.BeforeTarget, strings.Join(info.Conflicts, ", "), cause)
}

// settleIntegrated persists the integration result before touching any artifact,
// then removes the owned source.
func (s *Service) settleIntegrated(ctx context.Context, git MutationGit, deps MutationDeps, rec Record, integ Integration, targetRoot string, proof *mutationProof) (Record, error) {
	pending, err := s.writeMutation(ctx, rec, integ, CleanupIntegratedPending)
	if err != nil {
		return rec, fmt.Errorf("subagentwork: journal the integrated result: %w", err)
	}
	// Removing the owned artifacts is itself a mutation: it spends the proof.
	if err := proof.spend(); err != nil {
		return pending, fmt.Errorf("subagentwork: commit the mutation admission: %w", err)
	}
	return s.applyCleanup(ctx, git, pending, integ, targetRoot, false, CleanupIntegratedPending)
}

// Discard removes the owned artifacts of a task whose work is deliberately not
// integrated. Only a verified clean, owned branch at the reviewed candidate is
// deleted — with the force an unmerged branch needs — and never a worktree or
// dirty data.
func (s *Service) Discard(ctx context.Context, m Mutation, deps MutationDeps) (Record, error) {
	if err := validateMutation(m); err != nil {
		return Record{}, err
	}
	if err := requireMutationDeps(deps); err != nil {
		return Record{}, err
	}
	git, ok := s.git.(MutationGit)
	if !ok {
		return Record{}, errMutationUnsupported
	}
	rec, err := s.store.Get(ctx, m.OwnerSessionID, m.TaskKey)
	if err != nil {
		return Record{}, err
	}
	if err := admissibleForMutation(rec, m); err != nil {
		return s.refuse(ctx, rec, KindDiscard, m, err, "")
	}
	if rec.Integration != nil {
		switch rec.Integration.State {
		case IntegrationDiscarded:
			return s.resumeCleanup(ctx, git, deps, rec, m)
		case IntegrationIntegrated, IntegrationNoChanges:
			return s.refuse(ctx, rec, KindDiscard, m, fmt.Errorf(
				"subagentwork: the candidate for task %q is already integrated, so there is nothing to discard", m.TaskKey), "")
		}
	}

	release, err := acquireMutationIdle(ctx, deps.Idle, m.OwnerSessionID, rec)
	if err != nil {
		return rec, err
	}
	defer release()

	prepared, err := s.prepareMutationRecord(rec)
	if err != nil {
		return s.refuse(ctx, rec, KindDiscard, m, err, "")
	}
	if prepared.SourceHead != m.ExpectedHead {
		return s.refuse(ctx, rec, KindDiscard, m, fmt.Errorf(
			"subagentwork: the child workspace moved from the reviewed candidate %s to %s", m.ExpectedHead, prepared.SourceHead), "")
	}
	if err := s.requireCleanSource(git, prepared.SourceRoot); err != nil {
		return s.refuse(ctx, rec, KindDiscard, m, err, "")
	}

	var snapshot string
	if deps.Snapshot != nil {
		// A discard destroys the only copy of the candidate, so the snapshot is
		// of the source root, not of the target that never moves.
		snapshot, err = deps.Snapshot(ctx, prepared.SourceRoot)
		if err != nil {
			return rec, fmt.Errorf("subagentwork: snapshot the discarded workspace %s: %w", prepared.SourceRoot, err)
		}
	}

	proof, err := s.admitMutation(ctx, deps, prepared.EffectKey())
	if err != nil {
		return rec, err
	}
	defer proof.release()

	recheck, err := s.prepareMutationRecord(rec)
	if err != nil {
		return s.refuse(ctx, rec, KindDiscard, m, err, snapshot)
	}
	if recheck.EffectKey() != prepared.EffectKey() {
		return s.refuse(ctx, rec, KindDiscard, m, fmt.Errorf(
			"subagentwork: the recorded source or target changed while the discard was admitted"), snapshot)
	}

	intent := Integration{
		OperationID:    newOperationID(),
		Kind:           KindDiscard,
		State:          IntegrationIntent,
		ExpectedSource: prepared.SourceHead,
		BeforeTarget:   prepared.TargetHead,
		SnapshotHash:   snapshot,
	}
	journalled, err := s.writeMutation(ctx, rec, intent, CleanupRetained)
	if err != nil {
		return rec, fmt.Errorf("subagentwork: journal the discard intent: %w", err)
	}
	if err := proof.spend(); err != nil {
		return journalled, fmt.Errorf("subagentwork: commit the mutation admission: %w", err)
	}
	if err := s.cleanupSource(ctx, git, prepared.TargetRoot, journalled, true); err != nil {
		pending := intent
		pending.Reason = "cleanup pending: " + err.Error()
		written, werr := s.writeMutation(ctx, journalled, pending, CleanupRetained)
		if werr != nil {
			return journalled, fmt.Errorf("subagentwork: the candidate is discarded but its artifacts could not be removed: %w", err)
		}
		return written, fmt.Errorf("subagentwork: the candidate is discarded but its artifacts were retained: %w", err)
	}
	discarded := intent
	discarded.State = IntegrationDiscarded
	return s.finishCleanup(ctx, journalled, discarded)
}

// resumeCleanup finishes the cleanup a settled operation still owes. It never
// repeats a merge: the receipt is already proof that the merge happened.
func (s *Service) resumeCleanup(ctx context.Context, git MutationGit, deps MutationDeps, rec Record, m Mutation) (Record, error) {
	if rec.CleanupState == CleanupComplete {
		return rec, nil
	}
	if rec.Integration == nil {
		return rec, nil
	}
	release, err := acquireMutationIdle(ctx, deps.Idle, m.OwnerSessionID, rec)
	if err != nil {
		return rec, err
	}
	defer release()

	target, err := s.cleanupCheckout(rec)
	if err != nil {
		return rec, err
	}
	integ := *rec.Integration
	prepared := PreparedMutation{
		RecordRevision: rec.Revision,
		Repository:     rec.Binding.Repository,
		SourceRoot:     rec.Binding.Root,
		SourceHead:     rec.Candidate,
		TargetRoot:     target.Root,
		TargetBranch:   rec.Target.Branch,
		TargetHead:     target.Head,
	}
	proof, err := s.admitMutation(ctx, deps, prepared.EffectKey())
	if err != nil {
		return rec, err
	}
	defer proof.release()
	if err := proof.spend(); err != nil {
		return rec, fmt.Errorf("subagentwork: commit the mutation admission: %w", err)
	}
	pendingState := CleanupIntegratedPending
	if integ.Kind == KindDiscard {
		pendingState = CleanupRetained
	}
	return s.applyCleanup(ctx, git, rec, integ, target.Root, integ.Kind == KindDiscard, pendingState)
}

// applyCleanup removes the owned artifacts and records the outcome. A cleanup
// failure never fails the mutation: the integration or discard stands, and the
// state says the artifacts are still there.
func (s *Service) applyCleanup(ctx context.Context, git MutationGit, rec Record, integ Integration, targetRoot string, force bool, pendingState string) (Record, error) {
	if err := s.cleanupSource(ctx, git, targetRoot, rec, force); err != nil {
		pending := integ
		note := strings.TrimSpace(integ.Reason)
		if note != "" {
			note += "; "
		}
		pending.Reason = note + "cleanup pending: " + err.Error()
		written, werr := s.writeMutation(ctx, rec, pending, pendingState)
		if werr != nil {
			return rec, nil
		}
		return written, nil
	}
	return s.finishCleanup(ctx, rec, integ)
}

func (s *Service) finishCleanup(ctx context.Context, rec Record, integ Integration) (Record, error) {
	if rec.Integration != nil && *rec.Integration == integ && rec.CleanupState == CleanupComplete {
		return rec, nil
	}
	out := rec
	copied := integ
	out.Integration = &copied
	out.CleanupState = CleanupComplete
	written, err := s.store.CompareAndSwap(ctx, out, rec.Revision)
	if err != nil {
		return rec, fmt.Errorf("subagentwork: record the completed cleanup: %w", err)
	}
	s.publish(written)
	return written, nil
}

// cleanupSource removes the record's owned worktree and then its branch. Each
// step proves ownership again and leaves anything it cannot prove alone.
func (s *Service) cleanupSource(ctx context.Context, git MutationGit, targetRoot string, rec Record, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.removeOwnedWorktree(git, targetRoot, rec); err != nil {
		return err
	}
	return s.deleteOwnedBranch(git, targetRoot, rec, force)
}

func (s *Service) removeOwnedWorktree(git MutationGit, targetRoot string, rec Record) error {
	root := rec.Binding.Root
	if root == "" || rec.Binding.Mode != ModeIsolated {
		return nil
	}
	if _, err := os.Stat(root); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("subagentwork: inspect the owned workspace %s: %w", root, err)
		}
		// The directory is gone; a stale registration is pruned, never guessed
		// at, and a worktree that was already removed needs no second removal.
		if err := git.WorktreePrune(targetRoot); err != nil {
			return fmt.Errorf("subagentwork: prune the removed workspace %s: %w", root, err)
		}
		return nil
	}
	branch, err := git.WorktreeBranch(targetRoot, root)
	if err != nil {
		// Not registered in this repository: nothing of ours to remove.
		return nil
	}
	if branch != rec.Binding.Branch {
		return fmt.Errorf("subagentwork: %s is checked out on %q, not the recorded %q", root, branch, rec.Binding.Branch)
	}
	state, err := git.MergeState(root)
	if err != nil {
		return fmt.Errorf("subagentwork: read the merge state of %s: %w", root, err)
	}
	if state != "" {
		return fmt.Errorf("subagentwork: %s has a merge in progress", root)
	}
	dirty, err := s.git.IsDirty(root)
	if err != nil {
		return fmt.Errorf("subagentwork: read the state of %s: %w", root, err)
	}
	if dirty {
		return fmt.Errorf("subagentwork: %s has uncommitted or untracked changes", root)
	}
	if err := git.WorktreeRemove(targetRoot, root); err != nil {
		return fmt.Errorf("subagentwork: remove the owned workspace %s: %w", root, err)
	}
	return nil
}

func (s *Service) deleteOwnedBranch(git MutationGit, targetRoot string, rec Record, force bool) error {
	branch := rec.Binding.Branch
	if branch == "" || rec.Binding.Mode != ModeIsolated {
		return nil
	}
	if !git.BranchExists(targetRoot, branch) {
		return nil
	}
	tip, err := s.git.RevParse(targetRoot, "refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("subagentwork: resolve %s: %w", branch, err)
	}
	if rec.Candidate == "" || tip != rec.Candidate {
		return fmt.Errorf("subagentwork: %s now points at %s, not the reviewed candidate %s", branch, tip, rec.Candidate)
	}
	if err := git.BranchDelete(targetRoot, branch, force); err != nil {
		return fmt.Errorf("subagentwork: delete %s: %w", branch, err)
	}
	return nil
}

// cleanupCheckout returns the target checkout the owned artifacts live in,
// re-proving it still belongs to the recorded repository. A cleanup follows the
// repository, never a branch: the operator may have switched branches since.
func (s *Service) cleanupCheckout(rec Record) (worktree.CheckoutIdentity, error) {
	if rec.Target.Root == "" || rec.Target.Repository == "" {
		return worktree.CheckoutIdentity{}, fmt.Errorf("subagentwork: the record names no target checkout to clean up against")
	}
	target, err := worktree.CaptureCheckoutIdentity(s.git, rec.Target.Root)
	if err != nil {
		return worktree.CheckoutIdentity{}, fmt.Errorf("subagentwork: read the target checkout identity: %w", err)
	}
	if target.Repository != rec.Target.Repository {
		return worktree.CheckoutIdentity{}, fmt.Errorf("subagentwork: the target checkout %s now belongs to repository %s, recorded %s", target.Root, target.Repository, rec.Target.Repository)
	}
	return target, nil
}

// writeMutation stores a journal entry together with the cleanup state it
// belongs to. A state this build cannot interpret is refused rather than stored.
func (s *Service) writeMutation(ctx context.Context, rec Record, integ Integration, cleanup string) (Record, error) {
	if !validIntegrationState(integ.State) {
		return rec, fmt.Errorf("subagentwork: refusing to journal unknown integration state %q", integ.State)
	}
	if !validCleanupState(cleanup) {
		return rec, fmt.Errorf("subagentwork: refusing to journal unknown cleanup state %q", cleanup)
	}
	out := rec
	copied := integ
	out.Integration = &copied
	out.CleanupState = cleanup
	written, err := s.store.CompareAndSwap(ctx, out, rec.Revision)
	if err != nil {
		return rec, err
	}
	s.publish(written)
	return written, nil
}

// refuse journals a refusal — no mutation happened — and reports the cause.
func (s *Service) refuse(ctx context.Context, rec Record, kind string, m Mutation, cause error, snapshot string) (Record, error) {
	cleanup := rec.CleanupState
	if cleanup == "" {
		cleanup = CleanupRetained
	}
	// A settled receipt is not overwritten by a later refusal.
	if rec.Integration != nil && rec.Integration.State != IntegrationIntent {
		return rec, cause
	}
	integ := Integration{
		OperationID:    newOperationID(),
		Kind:           kind,
		State:          IntegrationRefused,
		ExpectedSource: m.ExpectedHead,
		SnapshotHash:   snapshot,
		Reason:         cause.Error(),
	}
	written, err := s.writeMutation(ctx, rec, integ, cleanup)
	if err != nil {
		return rec, cause
	}
	return written, cause
}

// requireCleanSource refuses a source workspace with work that is not committed:
// the artifacts are going to be removed, so nothing may be left only there.
func (s *Service) requireCleanSource(git MutationGit, root string) error {
	state, err := git.MergeState(root)
	if err != nil {
		return fmt.Errorf("subagentwork: read the merge state of %s: %w", root, err)
	}
	if state != "" {
		return fmt.Errorf("subagentwork: %s has a merge in progress", root)
	}
	dirty, err := s.git.IsDirty(root)
	if err != nil {
		return fmt.Errorf("subagentwork: read the state of %s: %w", root, err)
	}
	if dirty {
		return fmt.Errorf("subagentwork: %s has uncommitted or untracked changes", root)
	}
	return nil
}

// requireCleanTarget refuses a target checkout with changes to tracked files or a
// merge already in progress. Untracked files do not block: no step here stages
// them, and git itself refuses a merge that would overwrite one.
func (s *Service) requireCleanTarget(git MutationGit, root string) error {
	state, err := git.MergeState(root)
	if err != nil {
		return fmt.Errorf("subagentwork: read the merge state of %s: %w", root, err)
	}
	if state != "" {
		return fmt.Errorf("subagentwork: a merge is already in progress in %s", root)
	}
	tracked, err := s.git.TrackedChanges(root)
	if err != nil {
		return fmt.Errorf("subagentwork: read the state of %s: %w", root, err)
	}
	if len(tracked) > 0 {
		return fmt.Errorf("subagentwork: the target checkout %s has uncommitted changes to tracked files: %s", root, strings.Join(tracked, ", "))
	}
	return nil
}

// mutationProof spends an admission exactly once, at the first accepted mutation
// boundary, and releases it unused otherwise.
type mutationProof struct {
	admission Admission
	spent     bool
}

func (p *mutationProof) spend() error {
	if p.spent {
		return nil
	}
	if err := p.admission.Commit(); err != nil {
		return err
	}
	p.spent = true
	return nil
}

func (p *mutationProof) release() {
	// Release unlocks the admission lease even after Commit: Commit only marks
	// the proof consumed. policy.Admission.Release is idempotent, so an unused
	// proof and a spent one take this path, and a session that integrated one
	// child can be admitted again for the next.
	p.admission.Release()
}

func (s *Service) admitMutation(ctx context.Context, deps MutationDeps, effectKey string) (*mutationProof, error) {
	admission, err := deps.Admit(ctx, effectKey)
	if err != nil {
		return nil, fmt.Errorf("subagentwork: the mutation was not admitted: %w", err)
	}
	if admission == nil {
		return nil, fmt.Errorf("subagentwork: the mutation admission returned no proof")
	}
	return &mutationProof{admission: admission}, nil
}

func acquireMutationIdle(ctx context.Context, gate IdleGate, owner string, rec Record) (func(), error) {
	var roots []string
	if rec.Binding.Root != "" {
		roots = append(roots, rec.Binding.Root)
	}
	if rec.Target.Root != "" {
		roots = append(roots, rec.Target.Root)
	}
	release, err := gate.AcquireIdle(ctx, owner, roots)
	if err != nil {
		return nil, fmt.Errorf("subagentwork: the workspace is not idle: %w", err)
	}
	if release == nil {
		return func() {}, nil
	}
	return release, nil
}

func validateMutation(m Mutation) error {
	if m.OwnerSessionID == "" {
		return fmt.Errorf("subagentwork: a workspace mutation needs an owner session")
	}
	if !validTaskKey(m.TaskKey) {
		return fmt.Errorf("subagentwork: task key %q is not generated safe opaque text", m.TaskKey)
	}
	if m.ExpectedHead == "" {
		return fmt.Errorf("subagentwork: a workspace mutation needs the exact reviewed candidate")
	}
	return nil
}

func requireMutationDeps(deps MutationDeps) error {
	if deps.Idle == nil {
		return fmt.Errorf("subagentwork: a workspace mutation needs an idle gate; a nil gate is not permission to mutate")
	}
	if deps.Admit == nil {
		return fmt.Errorf("subagentwork: a workspace mutation needs an admission; a nil admitter is not approval")
	}
	return nil
}

// admissibleForMutation refuses records whose exact candidate cannot be mutated:
// a shared child owns no candidate, an unobserved one is not exact, and an
// unknown execution is never admitted.
func admissibleForMutation(rec Record, m Mutation) error {
	if rec.Binding.Mode != ModeIsolated {
		return fmt.Errorf("subagentwork: a %q child works in the parent's checkout and owns no candidate to mutate", string(rec.Binding.Mode))
	}
	if rec.Binding.Root == "" || rec.Binding.Branch == "" {
		return fmt.Errorf("subagentwork: the record names no owned workspace checkout")
	}
	if rec.Candidate == "" {
		return fmt.Errorf("subagentwork: task %q has no observed candidate, so there is nothing exact to mutate", m.TaskKey)
	}
	if rec.Candidate != m.ExpectedHead {
		return fmt.Errorf("subagentwork: the reviewed candidate for task %q is %s, not %s; dispatch follow-up work under a new task key", m.TaskKey, rec.Candidate, m.ExpectedHead)
	}
	switch rec.Outcome {
	case OutcomePrepared, OutcomeDone, OutcomeFailed, OutcomeStopped, OutcomePartial, OutcomeInterrupted:
		return nil
	case OutcomeUnknown:
		return fmt.Errorf("subagentwork: task %q has an unknown execution, so its source is never admitted for mutation", m.TaskKey)
	default:
		return fmt.Errorf("subagentwork: task %q is %q, so its source is not admitted for mutation", m.TaskKey, rec.Outcome)
	}
}

// recordsMerge reports whether parents names exactly a merge of before and
// candidate: the shape a --no-ff merge of the exact candidate produces.
func recordsMerge(parents []string, before, candidate string) bool {
	return len(parents) == 2 && parents[0] == before && parents[1] == candidate
}

func integrationMessage(operationID, candidate string) string {
	return "Integrate subagent candidate " + candidate + "\n\n" +
		operationTrailer + ": " + operationID + "\n" +
		"Marshal-Subagent-Candidate: " + candidate + "\n"
}

var operationSequence atomic.Uint64

// newOperationID mints one operation identity, which the journal and the merge
// trailer share.
func newOperationID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), operationSequence.Add(1))
}
