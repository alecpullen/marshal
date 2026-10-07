package snapshot

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
)

// This file answers one question: which repository can serve the snapshot named
// by a hash, right now?
//
// The answer has to span every place a workspace's snapshots can live:
//
//   - the ACTIVE v2 generation, which is what a fresh capture's hash names;
//   - every SEALED v2 generation, because rotation moves history out of the
//     active generation without moving the hashes the user may still roll back
//     to;
//   - a pending LEGACY bare repository, `<snapshots>/<workspace-hash>/`, because
//     a pre-v2 capture is still a valid rollback point until the migration task
//     (Task 8) exists. This is deliberately read-only: the old layout only ever
//     gets ref listings here.
//
// Two rules are load-bearing and are stated once, here, because every caller
// depends on them:
//
//  1. HASH TEXT IS NEVER A GIT REVISION EXPRESSION. A hash that does not pass
//     validObjectHash (a plain 40- or 64-character hexadecimal object id) is
//     refused before the process spawns. That rejection is an argument- and
//     command-injection boundary, not a nicety: "HEAD", "main", "HEAD~1", a
//     "--upload-pack=..." option, and a 40-hex object id with a trailing "^{...}"
//     or ":path" suffix are all strings that Git would happily interpret as
//     something other than "this exact object". The ref name a lookup finally
//     uses is likewise derived from the hash by snapshotRefFor, never accepted
//     as free text.
//  2. THE LOOKUP IS EXACT. Membership is decided by asking each repository
//     which refs it publishes, not by resolving a name through Git. An
//     exact object whose bytes were already collected cannot be revived by
//     resolution, and a lookup that reported a diff for one would be claiming a
//     rollback point the store does not have.

// SnapshotLocation is where a snapshot hash is currently retained.
type SnapshotLocation struct {
	// Workspace is the workspace hash the snapshot belongs to.
	Workspace string
	// Hash is the canonical (lower-cased) object id.
	Hash string
	// GitDir is the repository that can serve it: a v2 generation directory or
	// a legacy bare repository.
	GitDir string
	// GenerationID names the v2 generation that retains the hash, or is empty
	// for a legacy repository.
	GenerationID string
	// Legacy is set when the hash lives in a pre-v2 bare repository.
	Legacy bool
	// Ref is the publication ref inside GitDir that names the hash.
	Ref string
	// Protected reports whether the location is a generation that no budget
	// pressure may reclaim: a migrated legacy-origin generation.
	//
	// It is deliberately FALSE for a legacy bare repository. That store is not
	// reclaimed automatically either, but for a different reason — this process
	// cannot prove no legacy writer is active — and conflating the two would
	// misstate which confirmation is needed to discard it.
	Protected bool
}

// LookupSnapshot finds the repository that retains hash for the ACTIVE
// workspace, or returns a typed error.
//
// It requires exclusive store ownership, which the caller normally already
// holds through withStore. Requiring it here is what makes the result safe to
// act on: a concurrent reclamation runs under the same lock, so a location this
// call returns cannot be deleted before the caller finishes with it.
//
// The failures are deliberately distinguished, because the user's remedy
// differs for each:
//
//   - a non-hexadecimal identifier         → ReasonInvalidObjectHash
//   - no store at all for the workspace    → ReasonSnapshotNotFound
//   - a store exists but no usable
//     repository retains the hash          → ReasonSnapshotExpired
//   - the manifest or a repository cannot
//     be read                              → ReasonUnreadableFile
func (s *Service) LookupSnapshot(ctx context.Context, hash string) (*SnapshotLocation, error) {
	if !validObjectHash(hash) {
		// Refuse before anything else. Git must never see this string.
		return nil, storeErrorf(ReasonInvalidObjectHash,
			"snapshot %q is not a hexadecimal object id, so it is not looked up as a revision", hash)
	}
	canonical := strings.ToLower(hash)
	if !s.enabled {
		return nil, storeErrorf(ReasonSnapshotNotFound,
			"snapshots are disabled, so workspace %s has no store to look %s up in", s.Workspace(), canonical)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.lock(ctx); err != nil {
		return nil, err
	}
	defer s.unlock()

	var loc *SnapshotLocation
	err := s.withStore(ctx, func(m *Manager, cat *Catalog) error {
		found, err := lookupSnapshotOwned(ctx, m, cat, canonical)
		if err != nil {
			return err
		}
		loc = found
		return nil
	})
	if err != nil {
		return nil, err
	}
	return loc, nil
}

// lookupSnapshotOwned is the lookup proper. Store ownership must already be
// held; see LookupSnapshot.
func lookupSnapshotOwned(ctx context.Context, m *Manager, cat *Catalog, hash string) (*SnapshotLocation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validObjectHash(hash) {
		return nil, storeErrorf(ReasonInvalidObjectHash, "snapshot %q is not a hexadecimal object id", hash)
	}
	canonical := strings.ToLower(hash)

	// A store root that does not exist has never captured anything, in either
	// layout. That is a different answer from "your rollback point expired".
	rootInfo, err := os.Lstat(m.root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, storeErrorf(ReasonSnapshotNotFound,
				"workspace %s has no snapshot store, so snapshot %s was never captured here", cat.Workspace(), canonical)
		}
		return nil, m.storeError(ReasonUnreadableFile, m.root, err)
	}
	if !rootInfo.IsDir() {
		return nil, m.storeErrorf(ReasonInternal, m.root, "snapshots root is not a directory")
	}

	man, err := cat.Load()
	if err != nil {
		// A manifest that cannot be read means the workspace cannot be
		// reasoned about, which is a repair problem, not a missing snapshot.
		return nil, err
	}
	if man == nil {
		// No v2 manifest. A legacy repository may still retain the hash, which
		// is exactly the migration-pending case. A workspace with NEITHER
		// layout has never captured anything, which legacyLookup reports as
		// "never captured" rather than as an expiry.
		return legacyLookup(ctx, m, cat, canonical)
	}

	// A workspace with no repository in EITHER layout has never captured
	// anything. That is deliberately a different answer from "your rollback
	// point expired": the remedy is "there is nothing to roll back to", not
	// "your snapshot was reclaimed".
	if !catalogDirExists(cat.Root()) && !legacyRepoPresent(m, cat) {
		return nil, storeErrorf(ReasonSnapshotNotFound,
			"workspace %s has no snapshot store, so snapshot %s was never captured here", cat.Workspace(), canonical)
	}

	// The active generation is checked first. A fresh capture's hash always
	// lives there, so the common case must never enumerate every generation.
	order := orderedGenerations(man)

	sawUsable := false
	for _, id := range order {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rec := man.Generation(id)
		if rec == nil || !rec.State.Usable() {
			continue
		}
		sawUsable = true
		dir, err := cat.GenerationDir(id)
		if err != nil {
			return nil, err
		}
		exists, err := lookupRefExists(ctx, m, dir, canonical)
		if err != nil {
			// An unreadable generation is skipped rather than failing the
			// whole lookup: another generation may still retain the snapshot,
			// and a corrupt directory must not make a healthy rollback point
			// unreachable.
			continue
		}
		if !exists {
			continue
		}
		ref, err := snapshotRefFor(canonical)
		if err != nil {
			return nil, err
		}
		return &SnapshotLocation{
			Workspace:    cat.Workspace(),
			Hash:         canonical,
			GitDir:       dir,
			GenerationID: id,
			Ref:          ref,
			Protected:    man.IsProtected(id),
		}, nil
	}
	// No usable generation retains the hash. The legacy repository is consulted
	// BEFORE the "no usable generation" verdict, because a legacy store may still
	// be the thing that serves this rollback point — an interrupted migration
	// leaves exactly that state: a `creating`, protected generation that holds no
	// refs yet, beside the original legacy store that holds all of them.
	legacy, legacyErr := legacyLookup(ctx, m, cat, canonical)
	if legacyErr == nil {
		return legacy, nil
	}
	if ReasonOf(legacyErr) == ReasonUnreadableFile {
		// The legacy repository could not be READ (rather than simply not
		// retaining the hash). That is a repair problem, and reporting an
		// expiry would claim a rollback point is gone when the truth is that
		// it could not be checked.
		return nil, legacyErr
	}
	if !sawUsable && len(man.Generations) > 0 {
		// The manifest lists generations but not one of them is usable
		// (`creating` or `deleting`), and the legacy repository does not retain
		// the hash either. So nothing durable describes what this store holds:
		// that is a repair problem, not an expiry.
		return nil, storeErrorf(ReasonUnreadableFile,
			"workspace %s has no usable snapshot generation, so %s cannot be looked up", cat.Workspace(), canonical)
	}
	// The manifest retains nothing (every generation was reclaimed whole) and the
	// legacy repository does not have it either, so the rollback point is gone.
	return nil, legacyErr
}

// legacyLookup checks the pre-v2 bare repository for one workspace.
//
// The repository is only ever READ: a ref listing, nothing more. Old Marshal
// binaries do not honour the v2 store lock, so this process cannot prove no
// legacy writer is active and must not mutate one.
//
// A workspace with NO store in either layout has never captured anything, and
// that is reported as "never captured" rather than as an expiry. A workspace
// whose store exists but does not retain the hash has had its rollback point
// reclaimed (or never had that hash at all), and that is the expiry.
func legacyLookup(ctx context.Context, m *Manager, cat *Catalog, hash string) (*SnapshotLocation, error) {
	path, err := WorkspaceStorePath(m.root, cat.Workspace())
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, m.storeError(ReasonUnreadableFile, path, err)
		}
		if !catalogDirExists(cat.Root()) {
			return nil, neverCapturedSnapshotError(cat, hash)
		}
		return nil, expiredSnapshotError(cat, hash)
	}
	if !info.IsDir() || !gitDirExists(path) {
		if !catalogDirExists(cat.Root()) {
			return nil, neverCapturedSnapshotError(cat, hash)
		}
		return nil, expiredSnapshotError(cat, hash)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	exists, err := lookupRefExists(ctx, m, path, hash)
	if err != nil || !exists {
		return nil, expiredSnapshotError(cat, hash)
	}
	ref, err := snapshotRefFor(hash)
	if err != nil {
		return nil, err
	}
	return &SnapshotLocation{
		Workspace: cat.Workspace(),
		Hash:      hash,
		GitDir:    path,
		Legacy:    true,
		Ref:       ref,
	}, nil
}

// catalogDirExists reports whether a v2 workspace catalog directory is present.
func catalogDirExists(root string) bool {
	info, err := os.Lstat(root)
	return err == nil && info.IsDir()
}

// legacyRepoPresent reports whether the workspace has a pre-v2 bare repository.
func legacyRepoPresent(m *Manager, cat *Catalog) bool {
	path, err := WorkspaceStorePath(m.root, cat.Workspace())
	if err != nil {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	return gitDirExists(path)
}

// neverCapturedSnapshotError is the single construction of the "this workspace
// has no store at all" error.
func neverCapturedSnapshotError(cat *Catalog, hash string) *StoreError {
	se := storeErrorf(ReasonSnapshotNotFound,
		"workspace %s has no snapshot store, so snapshot %s was never captured here", cat.Workspace(), hash)
	se.Path = cat.Root()
	se.Workspace = cat.Workspace()
	return se
}

// expiredSnapshotError is the single construction of the "this rollback point
// is gone" error.
func expiredSnapshotError(cat *Catalog, hash string) *StoreError {
	se := storeErrorf(ReasonSnapshotExpired,
		"snapshot %s is not retained in workspace %s's store; it was reclaimed by retention or budget pressure, "+
			"so it can no longer be diffed or restored", hash, cat.Workspace())
	se.Path = cat.Root()
	se.Workspace = cat.Workspace()
	return se
}

// lookupRefExists reports whether dir publishes the snapshot ref for hash.
//
// It is the same exact question RefExists asks, asked against a repository that
// may be a legacy bare repo rather than a v2 generation — which is why the
// repository is enumerated directly instead of going through the catalog's
// generation-dir resolution.
func lookupRefExists(ctx context.Context, m *Manager, dir, hash string) (bool, error) {
	ref, err := snapshotRefFor(hash)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, m.storeError(ReasonUnreadableFile, dir, err)
	}
	out, err := m.gitOutput(ctx, dir, "for-each-ref", "--format=%(refname)", ref)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == ref {
			return true, nil
		}
	}
	return false, nil
}

// orderedGenerations lists a manifest's generations with the usable active one
// first, so the common lookup costs one ref listing rather than one per
// generation.
func orderedGenerations(man *Manifest) []string {
	ids := make([]string, 0, len(man.Generations))
	if man.ActiveID != "" {
		if active := man.Active(); active != nil && active.State.Usable() {
			ids = append(ids, man.ActiveID)
		}
	}
	for i := range man.Generations {
		if man.Generations[i].ID == man.ActiveID {
			continue
		}
		ids = append(ids, man.Generations[i].ID)
	}
	return ids
}

// LookupSnapshotOn is lookupSnapshotOwned for a manager and catalog the caller
// already owns, without a per-workspace Service. It exists so maintenance
// sweeps that already hold the lock (Rooted, the runtime's startup pass) can
// reuse one lookup implementation instead of a second one.
func (m *Manager) LookupSnapshotOn(ctx context.Context, cat *Catalog, hash string) (*SnapshotLocation, error) {
	if !validObjectHash(hash) {
		return nil, storeErrorf(ReasonInvalidObjectHash,
			"snapshot %q is not a hexadecimal object id, so it is not looked up as a revision", hash)
	}
	if cat == nil {
		return nil, storeErrorf(ReasonInternal, "snapshot lookup needs a catalog")
	}
	return lookupSnapshotOwned(ctx, m, cat, strings.ToLower(hash))
}
