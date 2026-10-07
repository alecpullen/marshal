package snapshot

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// randRead reads cryptographically strong random bytes. It is a package-level
// seam variable rather than a direct call so a test can make identifier
// generation fail deterministically.
var randRead = rand.Read

// This file creates and maintains one generation: a minimal bare Git
// repository whose only retention roots are explicit snapshot refs.
//
// Two properties make the layout bounded and are therefore load-bearing:
//
//  1. HEAD is a symbolic ref to an UNBORN branch. Never a commit. If HEAD
//     named a commit, that commit and its whole parent chain would be
//     reachable through the branch and would survive deleting every snapshot
//     ref — which is exactly the defect this work exists to fix (a pruned
//     store kept 71 GiB of "deleted" history reachable through a branch).
//  2. There is no reflog (core.logAllRefUpdates=false). A reflog entry is a
//     retention root too, and it is written where nothing looks for it.
//
// The repository metadata is written directly rather than by `git init`, so
// the created bytes are deterministic: no template, no hooks sample, no
// platform-dependent descriptions, and no chance of a local git configuration
// changing what appears on disk.
const (
	// generationHeadTarget is the unborn branch HEAD points at. The name is
	// namespaced under refs/heads/marshal/ so it can never collide with a
	// branch a user or a future subsystem creates.
	generationHeadTarget = "refs/heads/marshal/unborn"

	// generationConfig is the repository config. Every line is deliberate:
	//
	//   - core.bare: the repository has no work tree, and a work tree would let
	//     git interpret paths relative to something outside the store.
	//   - core.logallrefupdates=false: no reflog, so no hidden retention root.
	//   - gc.auto=0 / gc.autoDetach=false / maintenance.auto=false: no
	//     background repack may ever start. An interrupted repack leaving
	//     tmp_pack_* files is one of the three defects being fixed, and the
	//     design forbids repacks here outright.
	generationConfig = `[core]
	repositoryformatversion = 0
	filemode = true
	bare = true
	logallrefupdates = false
[gc]
	auto = 0
	autoDetach = false
[maintenance]
	auto = false
`
)

// generationDirs are the directories a minimal bare repository needs. They are
// created explicitly so the repository has the standard shape git expects
// (objects/pack is where git looks before falling back to loose objects) while
// staying empty and therefore negligible in size.
var generationDirs = []string{
	"objects",
	filepath.Join("objects", "info"),
	filepath.Join("objects", "pack"),
	"refs",
	filepath.Join("refs", "heads"),
	filepath.Join("refs", "tags"),
	filepath.Join("refs", "snapshots"),
}

// repositoryLooksInitialised reports whether a directory has the metadata a Git
// repository needs, without spawning a process.
//
// The entries are exactly what git itself requires to recognise a repository.
// Leaving any one out would treat a half-created directory as usable, and every
// caller here uses this to decide whether a git command may be run at all.
func repositoryLooksInitialised(dir string) bool {
	return looksLikeGitDir(dir)
}

// looksLikeGitDir is the shared repository-shape check. Legacy stores are
// recognised by the same test, so both layouts agree on what a repository is.
func looksLikeGitDir(dir string) bool {
	for _, name := range []string{"HEAD", "config", "objects", "refs"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

// CreateGeneration creates a new generation in state `creating` and returns its
// record. The record is committed to the manifest BEFORE the repository exists,
// so an interruption leaves a `creating` generation that reconciliation can
// identify as never-published and remove without guessing.
//
// root is the canonical workspace root the generation is for. An empty root
// reuses the catalog's recorded root.
func (c *Catalog) CreateGeneration(ctx context.Context, root string) (*GenerationRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.requireOwned(); err != nil {
		return nil, err
	}
	if root == "" {
		root = c.WorkspaceRoot
	}

	man, err := c.ensureManifest()
	if err != nil {
		return nil, err
	}
	if len(man.Generations) >= MaxGenerationsPerWorkspace {
		return nil, c.errf(ReasonBudgetExhausted,
			"manifest already lists %d generations, at the %d bound", len(man.Generations), MaxGenerationsPerWorkspace)
	}

	suffix, err := randomSuffix(generationIDSuffixLen)
	if err != nil {
		return nil, c.err(ReasonInternal, err)
	}
	rec := GenerationRecord{
		ID:        formatGenerationID(man.NextSeq, suffix),
		State:     GenerationCreating,
		Origin:    OriginNative,
		Root:      root,
		CreatedAt: c.manager.Now().UTC(),
	}
	man.NextSeq++
	man.Generations = append(man.Generations, rec)
	if err := c.Save(man); err != nil {
		return nil, err
	}

	// Crash boundary: a `creating` record exists with no repository.
	if err := c.manager.runHook(c.manager.hooks.AfterGenerationRecord); err != nil {
		return nil, err
	}

	if err := c.InitGenerationRepo(ctx, rec.ID); err != nil {
		return nil, err
	}
	return &rec, nil
}

// InitGenerationRepo writes the minimal bare repository for a generation. It is
// idempotent: the caller may re-run it on a generation left `creating` by an
// interruption, and a repository that already has HEAD and config is left as it
// is rather than re-created.
func (c *Catalog) InitGenerationRepo(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.requireOwned(); err != nil {
		return err
	}
	dir, err := c.GenerationDir(id)
	if err != nil {
		return err
	}

	if repositoryLooksInitialised(dir) {
		return nil
	}
	if _, err := os.Lstat(dir); err == nil {
		// The directory exists but is not a usable repository. Rebuilding it in
		// place would risk interpreting half-written state, so it is refused:
		// reconciliation removes a never-published generation whole instead.
		return c.errf(ReasonInterruptedCapture,
			"generation %s directory %s exists but is not an initialised bare repository", id, dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return c.err(ReasonUnreadableFile, err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return c.err(ReasonUnreadableFile, fmt.Errorf("create generation directory: %w", err))
	}
	for _, sub := range generationDirs {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return c.err(ReasonUnreadableFile, fmt.Errorf("create %s: %w", sub, err))
		}
	}
	if err := writeFileSynced(filepath.Join(dir, "config"), []byte(generationConfig), 0o644); err != nil {
		return c.err(ReasonUnreadableFile, err)
	}
	// HEAD names an unborn branch. If this ever wrote a commit hash, snapshot
	// objects would survive ref deletion through the branch — the exact bug
	// this layout prevents.
	if err := writeFileSynced(filepath.Join(dir, "HEAD"), []byte("ref: "+generationHeadTarget+"\n"), 0o644); err != nil {
		return c.err(ReasonUnreadableFile, err)
	}
	if err := syncDir(dir); err != nil {
		return c.err(ReasonUnreadableFile, err)
	}

	// Write boundary: a generation's baseline metadata is durable.
	c.manager.observeWrite(WriteEvent{Kind: "generation", Path: dir, Workspace: c.workspace})

	// Crash boundary: repository metadata written, generation still `creating`.
	if err := c.manager.runHook(c.manager.hooks.AfterInitGenerationRepo); err != nil {
		return err
	}

	// The result must be a repository git itself accepts, with an unborn HEAD.
	// Verifying here means a metadata typo fails a test rather than a capture.
	bare, err := c.manager.gitOutput(ctx, dir, "rev-parse", "--is-bare-repository")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(bare)) != "true" {
		return c.errf(ReasonInternal, "generation %s is not recognised as a bare repository", id)
	}
	head, err := c.manager.gitOutput(ctx, dir, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != generationHeadTarget {
		return c.errf(ReasonInternal, "generation %s HEAD is %q, want the unborn branch %q",
			id, strings.TrimSpace(string(head)), generationHeadTarget)
	}
	return nil
}

// ActivateGeneration marks a generation active. Any previously active
// generation is sealed in the same manifest write, so a workspace never
// transiently has two active generations.
func (c *Catalog) ActivateGeneration(id string) error {
	if err := c.requireOwned(); err != nil {
		return err
	}
	man, err := c.Load()
	if err != nil {
		return err
	}
	if man == nil {
		return c.errf(ReasonInternal, "no manifest for workspace %s", c.workspace)
	}
	rec := man.Generation(id)
	if rec == nil {
		return c.errf(ReasonInternal, "generation %s is not listed in the manifest", id)
	}
	// A generation being deleted is never revived: its directory may already be
	// partly removed, so making it active would capture into a hole.
	if rec.State == GenerationDeleting {
		return c.errf(ReasonInterruptedCapture, "generation %s is being deleted and cannot be activated", id)
	}
	if man.ActiveID == id && rec.State == GenerationActive {
		return nil
	}
	if prev := man.Active(); prev != nil {
		prev.State = GenerationSealed
	}
	rec.State = GenerationActive
	man.ActiveID = id
	return c.Save(man)
}

// SealGeneration marks a generation sealed: finished, retained for rollback,
// and no longer a capture target. The workspace's active pointer is cleared
// when it named this generation.
func (c *Catalog) SealGeneration(id string) error {
	if err := c.requireOwned(); err != nil {
		return err
	}
	man, err := c.Load()
	if err != nil {
		return err
	}
	if man == nil {
		return c.errf(ReasonInternal, "no manifest for workspace %s", c.workspace)
	}
	rec := man.Generation(id)
	if rec == nil {
		return c.errf(ReasonInternal, "generation %s is not listed in the manifest", id)
	}
	if rec.State == GenerationDeleting {
		return c.errf(ReasonInterruptedCapture, "generation %s is being deleted and cannot be sealed", id)
	}
	rec.State = GenerationSealed
	if man.ActiveID == id {
		man.ActiveID = ""
	}
	return c.Save(man)
}

// MarkDeleting persists the intent to delete a generation, and returns the
// record it marked. Only the persisted intent authorises removal: a crash after
// this call leaves a `deleting` generation that reconciliation finishes, and a
// crash before it leaves a generation reconciliation will not touch.
func (c *Catalog) MarkDeleting(id string) (*GenerationRecord, error) {
	if err := c.requireOwned(); err != nil {
		return nil, err
	}
	man, err := c.Load()
	if err != nil {
		return nil, err
	}
	if man == nil {
		return nil, c.errf(ReasonInternal, "no manifest for workspace %s", c.workspace)
	}
	rec := man.Generation(id)
	if rec == nil {
		return nil, c.errf(ReasonInternal, "generation %s is not listed in the manifest", id)
	}
	if rec.IsProtected() {
		// Defence in depth. Reclamation must already have skipped a protected
		// generation; this refuses the step that would discard the only copy of
		// migrated rollback history if it had not.
		return nil, c.errf(ReasonLegacyRecoveryRequired,
			"generation %s carries the legacy-origin protection flag and must not be reclaimed automatically", id)
	}
	rec.State = GenerationDeleting
	if man.ActiveID == id {
		man.ActiveID = ""
	}
	if err := c.Save(man); err != nil {
		return nil, err
	}
	// Crash boundary: the deletion intent is durable, the removal is not.
	if err := c.manager.runHook(c.manager.hooks.AfterMarkDeleting); err != nil {
		return nil, err
	}
	out := *rec
	return &out, nil
}

// RemoveGenerationDirectory removes a generation's repository directory. The
// caller must have persisted the `deleting` state first; see MarkDeleting.
func (c *Catalog) RemoveGenerationDirectory(id string) error {
	if err := c.requireOwned(); err != nil {
		return err
	}
	dir, err := c.GenerationDir(id)
	if err != nil {
		return err
	}
	// Symlink check with Lstat semantics: a symlinked generation directory must
	// never be followed, because removing through it would delete whatever it
	// points at.
	info, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return c.err(ReasonUnreadableFile, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return c.errf(ReasonSymlinkEscape, "generation %s directory %s is a symlink; refusing to remove it", id, dir)
	}
	// Crash boundary: the deletion intent is durable and the directory is
	// still whole. A restart here must simply retry the removal, not reconsider
	// whether the generation was really being deleted.
	if h := c.manager.hooks.BeforeRemoveGenerationDir; h != nil {
		if err := h(id); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return c.err(ReasonUnreadableFile, fmt.Errorf("remove generation directory: %w", err))
	}
	// Crash boundary: the directory is gone, the manifest record is not.
	return c.manager.runHook(c.manager.hooks.AfterRemoveGenerationDir)
}

// ForgetGeneration drops a generation's manifest record. A record whose
// directory is already gone would otherwise be re-reported as an abandoned
// generation forever.
func (c *Catalog) ForgetGeneration(id string) error {
	if err := c.requireOwned(); err != nil {
		return err
	}
	man, err := c.Load()
	if err != nil {
		return err
	}
	if man == nil {
		return nil
	}
	man.removeGeneration(id)
	return c.Save(man)
}

// ---------------------------------------------------------------------------
// Snapshot refs
// ---------------------------------------------------------------------------

// PublishedRefs lists the snapshot refs of a generation as hash → ref, plus the
// hashes they name. It is read-only and never mutates the repository, so it is
// safe to call on a legacy store as well as a generation.
//
// A repository that cannot be listed is an error, never "no refs": treating a
// failed listing as empty is how reconciliation would delete live objects.
func (c *Catalog) PublishedRefs(ctx context.Context, id string) (map[string]string, []string, error) {
	dir, err := c.GenerationDir(id)
	if err != nil {
		return nil, nil, err
	}
	return c.refsInDir(ctx, dir)
}

func (c *Catalog) refsInDir(ctx context.Context, dir string) (map[string]string, []string, error) {
	return c.manager.snapshotRefsInDir(ctx, dir)
}

// snapshotRefsInDir lists the snapshot refs of any Git directory. It lives on
// the Manager rather than the Catalog so the same read-only listing serves a v2
// generation and a legacy bare repo; a legacy store is only ever read.
//
// A listing that fails is an error, never an empty map: treating "could not
// list" as "nothing is published" is precisely how reconciliation would delete
// objects a live ref still needs.
func (m *Manager) snapshotRefsInDir(ctx context.Context, dir string) (map[string]string, []string, error) {
	// An explicit --format keeps the output to one line per ref, so the ref
	// name can never be confused with a peeled value or a decorative column.
	out, err := m.gitOutput(ctx, dir, "for-each-ref", "--format=%(objectname) %(refname)", snapshotRefPrefix)
	if err != nil {
		return nil, nil, err
	}
	refs := make(map[string]string)
	var hashes []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		hash, ref, err := parseRefLine(line)
		if err != nil {
			return nil, nil, m.storeError(ReasonUnreadableFile, dir, err)
		}
		if !validObjectHash(hash) {
			return nil, nil, m.storeErrorf(ReasonUnreadableFile, dir, "ref %s names non-hexadecimal object %q", ref, hash)
		}
		hash = strings.ToLower(hash)
		refs[hash] = ref
		hashes = append(hashes, hash)
	}
	sortStrings(hashes)
	return refs, hashes, nil
}

// PublishSnapshotRef writes the publication ref for a snapshot hash. It is the
// only step that makes captured objects durable against future collection, so
// it runs after the objects exist and never before.
//
// update-ref is used with an explicit ref name and hash; the hash is validated
// first and is never handed to git as a revision expression.
func (c *Catalog) PublishSnapshotRef(ctx context.Context, id, hash string) (string, error) {
	if err := c.requireOwned(); err != nil {
		return "", err
	}
	dir, err := c.GenerationDir(id)
	if err != nil {
		return "", err
	}
	ref, err := snapshotRefFor(hash)
	if err != nil {
		return "", c.err(ReasonInternal, err)
	}
	if _, err := c.manager.gitOutput(ctx, dir, "update-ref", ref, strings.ToLower(hash)); err != nil {
		return "", err
	}
	// Crash boundary: the ref is durable, the manifest's last-published
	// timestamp may not be. A durable ref always wins during reconciliation,
	// so a missed timestamp costs ordering accuracy, never a snapshot.
	if err := c.manager.runHook(c.manager.hooks.AfterPublishRef); err != nil {
		return "", err
	}
	if err := c.recordPublication(id); err != nil {
		return ref, err
	}
	return ref, nil
}

// recordPublication stamps the generation's last-published time. It is the
// ordering key for oldest-first reclamation, which is why it is written after
// the ref rather than before: a timestamp for a publication that never happened
// would make a generation look newer than it is.
func (c *Catalog) recordPublication(id string) error {
	man, err := c.Load()
	if err != nil {
		return err
	}
	if man == nil {
		return nil
	}
	rec := man.Generation(id)
	if rec == nil {
		// The generation is not listed: there is nowhere durable to record the
		// timestamp. The ref is already written and is the authoritative state,
		// so this is not worth failing the publication over.
		return nil
	}
	rec.LastPublishedAt = c.manager.Now().UTC()
	return c.Save(man)
}

// RefExists reports whether a generation already has a publication ref for the
// given hash. It is how reconciliation decides whether an interrupted
// publication actually landed.
func (c *Catalog) RefExists(ctx context.Context, id, hash string) (bool, error) {
	ref, err := snapshotRefFor(hash)
	if err != nil {
		return false, c.err(ReasonInternal, err)
	}
	dir, err := c.GenerationDir(id)
	if err != nil {
		return false, err
	}
	return c.refExistsInDir(ctx, dir, ref)
}

func (c *Catalog) refExistsInDir(ctx context.Context, dir, ref string) (bool, error) {
	if _, err := os.Lstat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, c.err(ReasonUnreadableFile, err)
	}
	out, err := c.manager.gitOutput(ctx, dir, "for-each-ref", "--format=%(refname)", ref)
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

// UnreachableBytes measures the on-disk allocation of the objects in a
// repository that no snapshot ref can reach: work that was staged but never
// published, or whose publishing ref was lost.
//
// It is a measurement, not an estimate, and it is deliberately NOT a licence to
// delete: Git objects are content-addressed, so an object that is unreachable
// today may be shared with, or identical to, one a published ref still needs.
// The number exists so abandoned storage is visible in accounting; removing it
// is a whole-generation decision, never a per-object sweep.
//
// Objects are counted as loose files because this store forbids repacks: a pack
// file is an unmanaged artifact, so a repository containing one is reported
// with a warning instead of a silently incomplete figure.
func (m *Manager) UnreachableBytes(ctx context.Context, gitDir string) (int64, error) {
	all, packPresent, err := looseObjects(gitDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No object store at all: there is nothing unreachable to measure.
			return 0, nil
		}
		return 0, err
	}
	if packPresent {
		return 0, m.storeErrorf(ReasonUnreadableFile, gitDir,
			"repository contains a pack file; object accounting is not available for packed storage")
	}
	if len(all) == 0 {
		return 0, nil
	}
	out, err := m.gitOutput(ctx, gitDir, "rev-list", "--objects", "--all", "--no-object-names")
	if err != nil {
		return 0, err
	}
	reachable := make(map[string]bool, len(all))
	for _, line := range strings.Split(string(out), "\n") {
		hash := strings.TrimSpace(line)
		if hash == "" {
			continue
		}
		reachable[strings.ToLower(hash)] = true
	}

	var total int64
	for hash, path := range all {
		if reachable[hash] {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return 0, m.storeError(ReasonUnreadableFile, path, err)
		}
		allocated, err := m.allocatedSize(path, info)
		if err != nil {
			return 0, m.storeError(ReasonUnreadableFile, path, err)
		}
		if total, err = addInt64(total, maxInt64(allocated, m.logicalBytes(info, m.limits.Normalize().AllocationUnitBytes))); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// looseObjects lists a repository's loose object files by hash. It never
// follows a symlink, so a link planted in an object directory cannot pull data
// outside the store into accounting.
//
// packPresent reports a pack or a temporary pack. Both are unmanaged here: no
// code in this package creates one, so seeing one means either a legacy store or
// an interrupted repack, and neither may be silently folded into a number that
// later authorises a deletion.
func looseObjects(gitDir string) (map[string]string, bool, error) {
	objectsDir := filepath.Join(gitDir, "objects")
	dirs, err := readDirNoFollow(objectsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	out := make(map[string]string)
	packPresent := false
	for _, d := range dirs {
		name := d.Name()
		if name == "pack" {
			packs, err := readDirNoFollow(filepath.Join(objectsDir, name))
			if err != nil {
				return nil, false, err
			}
			if len(packs) > 0 {
				packPresent = true
			}
			continue
		}
		if name == "info" {
			continue
		}
		if !isHex(name) || len(name) != 2 {
			continue
		}
		info, err := lsEntryInfo(objectsDir, d)
		if err != nil {
			return nil, false, err
		}
		if !info.IsDir() {
			continue
		}
		files, err := readDirNoFollow(filepath.Join(objectsDir, name))
		if err != nil {
			return nil, false, err
		}
		for _, f := range files {
			rest := f.Name()
			if !isHex(rest) || (len(rest) != 38 && len(rest) != 62) {
				continue
			}
			fileInfo, err := lsEntryInfo(filepath.Join(objectsDir, name), f)
			if err != nil {
				return nil, false, err
			}
			if !fileInfo.Mode().IsRegular() {
				continue
			}
			out[name+rest] = filepath.Join(objectsDir, name, rest)
		}
	}
	return out, packPresent, nil
}

// readDirNoFollow reads a directory, refusing to traverse a symlink. A link
// standing in for a directory inside the store must never be followed, because
// every caller of this either walks it into accounting or deletes below it.
func readDirNoFollow(dir string) ([]fs.DirEntry, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink", dir)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	return os.ReadDir(dir)
}

// ---------------------------------------------------------------------------
// Staging
// ---------------------------------------------------------------------------

// ReserveStaging creates the scratch directory for one capture and records it
// in the manifest as this workspace's in-flight capture. Recording it is what
// makes an interrupted capture distinguishable from a stray directory.
func (c *Catalog) ReserveStaging(ctx context.Context, generationID string) (*CaptureState, error) {
	if err := c.requireOwned(); err != nil {
		return nil, err
	}
	man, err := c.Load()
	if err != nil {
		return nil, err
	}
	if man == nil {
		return nil, c.errf(ReasonInternal, "no manifest for workspace %s", c.workspace)
	}
	if man.Capture != nil {
		return nil, c.errf(ReasonInterruptedCapture,
			"workspace %s already has an in-flight capture (%s); reconcile before starting another",
			c.workspace, man.Capture.StagingID)
	}
	rec := man.Generation(generationID)
	if rec == nil {
		return nil, c.errf(ReasonInternal, "generation %s is not listed in the manifest", generationID)
	}
	if !rec.State.Usable() {
		return nil, c.errf(ReasonInterruptedCapture,
			"generation %s is in state %q and cannot accept a capture", generationID, rec.State)
	}

	suffix, err := randomSuffix(stagingIDRandomLen)
	if err != nil {
		return nil, c.err(ReasonInternal, err)
	}
	cs := &CaptureState{
		StagingID:    formatStagingID(suffix),
		GenerationID: generationID,
		StartedAt:    c.manager.Now().UTC(),
	}
	path, err := c.StagingPath(cs.StagingID)
	if err != nil {
		return nil, err
	}

	man.Capture = cs
	if err := c.Save(man); err != nil {
		return nil, err
	}
	// Crash boundary: the capture is durable, the scratch directory is not.
	if err := c.manager.runHook(c.manager.hooks.AfterReserveStaging); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, c.err(ReasonUnreadableFile, fmt.Errorf("create staging directory: %w", err))
	}
	// Write boundary: the in-flight capture record and its scratch directory
	// are durable.
	c.manager.observeWrite(WriteEvent{Kind: "staging", Path: path, Workspace: c.workspace})
	return cs, nil
}

// ClearCapture removes the in-flight capture record and its scratch directory.
// It is idempotent so reconciliation can retry after an interruption.
func (c *Catalog) ClearCapture() error {
	if err := c.requireOwned(); err != nil {
		return err
	}
	man, err := c.Load()
	if err != nil {
		return err
	}
	if man == nil || man.Capture == nil {
		return nil
	}
	cs := man.Capture
	if path, err := c.StagingPath(cs.StagingID); err == nil {
		if err := removeDirectoryNoFollow(path); err != nil {
			return c.err(ReasonUnreadableFile, err)
		}
	} else {
		// A capture whose staging identifier is not a safe path element is a
		// corrupt record. Refuse to touch the filesystem on a guess; the
		// record itself is dropped so the workspace is not wedged forever.
		return err
	}
	man.Capture = nil
	return c.Save(man)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// writeFileSynced writes a file and flushes it before returning. Repository
// metadata written without a flush can be lost by a crash even though the
// directory entry survives, which would leave a generation that exists and is
// unusable.
func writeFileSynced(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// removeDirectoryNoFollow removes a directory without ever traversing a
// symlink. A symlink at the path is unlinked as a link; it is never followed,
// because following it would delete whatever it points at.
func removeDirectoryNoFollow(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to remove %s: it is a symlink", path)
	}
	if !info.IsDir() {
		return os.Remove(path)
	}
	return os.RemoveAll(path)
}

// sortStrings sorts in place. It exists so this file needs no import for one
// call site, and so the ordering is a named decision rather than an incidental
// one: sorted identifier lists are what make oldest-first reclamation
// deterministic.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// randomSuffix returns n hex characters of cryptographically strong randomness.
// The suffix is what keeps identifiers distinct when two generations are
// created in the same nanosecond or after a clock step backwards; a collision
// would make two generations share a directory.
//
// crypto/rand is read through the ReadRandom seam so a test can force the
// failure path; the production default is crypto/rand, never a seeded
// pseudo-random source, because a predictable identifier could collide across
// two processes running concurrently.
func randomSuffix(n int) (string, error) {
	buf := make([]byte, (n+1)/2)
	if _, err := randRead(buf); err != nil {
		return "", fmt.Errorf("generate random suffix: %w", err)
	}
	return hexEncode(buf)[:n], nil
}

func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// GenerationStats is a read-only measurement of one generation repository.
type GenerationStats struct {
	// ID is the generation identifier.
	ID string
	// Path is the repository directory.
	Path string
	// State and Origin are copied from the manifest when the generation is
	// listed there. An unlisted directory reports empty state.
	State  GenerationState
	Origin GenerationOrigin
	// Protected reports whether automatic reclamation must skip it.
	Protected bool
	// Refs is the number of snapshot refs, and Bytes the measured size.
	Refs  int
	Bytes int64
	// UnreachableBytes is the measured size of objects no snapshot ref can
	// reach. It is the abandoned-bytes figure reconciliation reports; it is
	// measured, not estimated, because Git objects are content-addressed and
	// shared objects must not be counted as freeable.
	UnreachableBytes int64
}

// GenerationStats measures one generation. bytes is the conservative allocated
// size of the repository directory, supplied by the caller's accounting pass so
// the two agree on rounding.
func (c *Catalog) GenerationStats(ctx context.Context, id string, bytes int64) (*GenerationStats, error) {
	dir, err := c.GenerationDir(id)
	if err != nil {
		return nil, err
	}
	stats := &GenerationStats{ID: id, Path: dir, Bytes: bytes}
	if man, err := c.Load(); err == nil && man != nil {
		if rec := man.Generation(id); rec != nil {
			stats.State = rec.State
			stats.Origin = rec.Origin
			stats.Protected = rec.IsProtected()
		}
	}
	if !repositoryLooksInitialised(dir) {
		return stats, nil
	}
	_, hashes, err := c.refsInDir(ctx, dir)
	if err != nil {
		return nil, err
	}
	stats.Refs = len(hashes)
	unreachable, err := c.manager.UnreachableBytes(ctx, dir)
	if err != nil {
		return nil, err
	}
	stats.UnreachableBytes = unreachable
	return stats, nil
}

// parseRefLine splits one "objectname refname" line from for-each-ref.
func parseRefLine(line string) (hash, ref string, err error) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) != 2 {
		return "", "", fmt.Errorf("unparseable ref line %q", line)
	}
	return fields[0], fields[1], nil
}
