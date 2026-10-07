package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file answers, for a pre-v2 store, three questions and no others:
//
//  1. WHAT IS HERE? A legacy store is a bare Git repository at
//     `<snapshots>/<workspace-hash>/` whose snapshot history may be reachable
//     through a branch and a parent chain rather than only through the
//     `refs/snapshots/*` refs the store publishes. Detection therefore has to
//     report the branch reachability and the retained refs, not merely count
//     the publication refs.
//  2. WHAT CAN BE DISCARDED? Exactly the artifacts whose names only Git's own
//     temporary machinery produces, inside the managed root: `tmp_pack_*` under
//     `objects/pack`. They are recognized by NAME, never by guessing from size
//     or age, and a recognized artifact is only ever removed under the offline
//     recovery workflow in recovery.go.
//  3. WHAT WOULD A MIGRATION COPY? The reachable object closure — every object a
//     retained ref needs, itself included — enumerated with a bounded stream so
//     a 71 GiB history never has to be held in memory.
//
// Everything here is READ-ONLY. Not one function in this file writes, removes,
// prunes, or repacks. That is the property the whole file exists to keep: old
// Marshal binaries do not honour the store lock, so a process that cannot prove
// no legacy writer is active must not mutate a legacy store at all.

// ---------------------------------------------------------------------------
// Recognized temporary artifacts
// ---------------------------------------------------------------------------

// gitTempPackPrefix is the name prefix Git gives a pack file it is still
// writing. An interrupted `git gc` or `git repack` leaves these behind, and on
// the store this work was written for they accounted for ~171 GiB.
const gitTempPackPrefix = "tmp_pack_"

// gitTempSuffixes are the remaining temporary names Git's pack machinery
// produces beside a pack. None of them is ever a completed pack: a finished
// pack is `pack-<hash>.pack` with a matching `.idx`, and only the `.pack` and
// `.idx` names are recognized as retained.
var gitTempSuffixes = []string{
	".keep",
	".rev",
	".bitmap",
	".promisor",
}

// gitLockSuffix is the suffix Git gives every lock file it holds while a writer
// is active. A lock file inside a legacy store IS evidence of a live writer:
// Git creates it for the duration of a write and removes it afterwards, so one
// that exists now is not an abandoned artifact.
const gitLockSuffix = ".lock"

// legacyDisposable describes one recognized disposable artifact.
type legacyDisposable struct {
	// Path is the artifact's path inside the managed root.
	Path string
	// Bytes is its measured allocated size.
	Bytes int64
	// Kind names the artifact class for reporting.
	Kind string
	// Name is the artifact's base name, so a caller can re-check that the
	// recognized name still holds immediately before removing it.
	Name string
}

// legacyPackArtifacts lists every entry under a repository's objects/pack that
// this code recognizes, classifying each as a completed pack (kept) or a
// temporary pack (disposable).
//
// Recognition is by NAME only. A file whose size or age merely looks abandoned
// is never reported as disposable: only Git's own temporary naming conventions
// are, and they are the names a crashed writer provably leaves behind because no
// completed pack ever carries them.
//
// The listing is bounded by readDirNoFollow, which refuses to follow a symlink
// out of the store.
func (m *Manager) legacyPackArtifacts(dir string) (disposables []legacyDisposable, packs []legacyDisposable, err error) {
	packDir := filepath.Join(dir, "objects", "pack")
	entries, err := readDirNoFollow(packDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, m.storeError(ReasonUnreadableFile, packDir, err)
	}
	unit := m.limits.Normalize().AllocationUnitBytes
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(packDir, name)
		info, err := lsEntryInfo(packDir, entry)
		if err != nil {
			return nil, nil, m.storeError(ReasonUnreadableFile, path, err)
		}
		allocated, err := m.allocatedBytes(path, info, unit)
		if err != nil {
			return nil, nil, err
		}
		switch {
		case strings.HasSuffix(name, gitLockSuffix):
			// Git's own lock file. It is NOT disposable: a lock exists only
			// while a writer holds it, so it is evidence of a live writer and is
			// reported as such by legacyLockFiles rather than removed here.
			continue
		case strings.HasPrefix(name, gitTempPackPrefix):
			// A temporary pack. Nothing but an interrupted writer creates one.
			disposables = append(disposables, legacyDisposable{Path: path, Bytes: allocated, Kind: "tmp_pack", Name: name})
		case strings.HasSuffix(name, ".pack"):
			// A completed pack: RETAINED. It is not a disposable artifact, and
			// removing it would discard every object it holds.
			packs = append(packs, legacyDisposable{Path: path, Bytes: allocated, Kind: "pack", Name: name})
		case strings.HasSuffix(name, ".idx"):
			// The index beside a pack. Retained with its pack.
			packs = append(packs, legacyDisposable{Path: path, Bytes: allocated, Kind: "pack_index", Name: name})
		default:
			for _, suffix := range gitTempSuffixes {
				if strings.HasSuffix(name, suffix) {
					disposables = append(disposables,
						legacyDisposable{Path: path, Bytes: allocated, Kind: "pack_scratch", Name: name})
					break
				}
			}
		}
	}
	sort.SliceStable(disposables, func(i, j int) bool { return disposables[i].Path < disposables[j].Path })
	sort.SliceStable(packs, func(i, j int) bool { return packs[i].Path < packs[j].Path })
	return disposables, packs, nil
}

// ---------------------------------------------------------------------------
// Detection
// ---------------------------------------------------------------------------

// LegacyInfo is the read-only description of one pre-v2 workspace store.
//
// It is the answer to "what is this, what does it hold, and what would it cost
// to keep it?" — and it is deliberately incomplete where the legacy layout is
// incomplete. A legacy store records only a workspace HASH, so Root stays empty
// and the store is identified by that hash. Inventing a path from the hash, or
// recovering one by guessing which directory on the machine hashes to it, would
// be fabricating provenance the store never carried.
type LegacyInfo struct {
	// Workspace is the 12-hex workspace identifier (the directory name).
	Workspace string
	// Path is the store directory, always a direct child of the snapshots root.
	Path string
	// Present reports whether the directory exists at all.
	Present bool
	// IsRepository reports whether it looks like a bare Git repository.
	IsRepository bool
	// Root is always "" for a legacy store. It is carried so a caller that
	// renders the v2 and legacy layouts through one shape does not have to
	// special-case the field, and so the emptiness is stated rather than
	// assumed.
	Root string
	// Bytes is the conservative allocated size of the whole store directory.
	Bytes int64

	// SnapshotRefs counts the publication refs (`refs/snapshots/*`) and
	// SnapshotHashes lists the hashes they name, sorted. These are the snapshots
	// the store can serve TODAY, and they are exactly the hashes a migration
	// must preserve.
	SnapshotRefs   int
	SnapshotHashes []string
	// RefsUnreadable is set when the publication refs could not be listed at
	// all. "Could not list" is never "nothing is published", and a store whose
	// refs are unknown must not be treated as empty.
	RefsUnreadable bool
	// RefListingError is the cause of an unreadable listing.
	RefListingError error

	// Branches lists the branch refs the repository publishes, with the commit
	// each names. A legacy store kept history reachable through a branch, which
	// is exactly why pruning the snapshot refs alone freed nothing: the branch
	// was a second retention root, and everything behind it survived.
	Branches []legacyBranchRef
	// ParentsNeeded is the conservative size of the reachable object closure:
	// every object a retained ref needs, including the parent chain behind it.
	// It is the figure a migration must copy and therefore the figure that
	// decides whether a migration can fit.
	ParentsNeeded int64
	// Objects is the number of objects in the reachable closure.
	Objects int64
	// ClosureError is set when the reachable closure could not be enumerated. A
	// failed enumeration is never reported as an empty closure.
	ClosureError error

	// Disposable lists the recognized temporary artifacts inside the store.
	Disposable []legacyDisposable
	// DisposableBytes is their total measured size. It is measured, never
	// estimated from a file's age or its name's shape.
	DisposableBytes int64
	// Packs lists the completed pack artifacts the store retains, for reporting
	// only. None of them is ever removed by this code.
	Packs []legacyDisposable

	// Warnings are the problems found while inspecting this store.
	Warnings []MaintenanceWarning
}

// RetainsHistory reports whether the store still holds any rollback history, in
// either ref namespace. A store that retains nothing is a candidate for reset
// without a migration; a store that retains something must be migrated first or
// explicitly reset.
func (l *LegacyInfo) RetainsHistory() bool {
	if l == nil {
		return false
	}
	return l.SnapshotRefs > 0 || len(l.Branches) > 0
}

// Heads lists the branch ref names, in the order they were listed.
func (l *LegacyInfo) Heads() []string {
	if l == nil {
		return nil
	}
	out := make([]string, 0, len(l.Branches))
	for _, b := range l.Branches {
		out = append(out, b.Ref)
	}
	return out
}

// legacyBranchRef is one branch ref and the commit it names. The commit is
// carried because a branch that named a real snapshot is itself a lookup-able
// rollback point, and a migration must preserve that hash too.
type legacyBranchRef struct {
	// Ref is the full ref name, e.g. refs/heads/master.
	Ref string
	// Hash is the object id the ref names.
	Hash string
}

// MigrationFeasible reports whether a migration of this store's closure could
// fit the supplied budgets. It is an ADVISORY evaluation against the budgets as
// they stand: the migration itself re-checks the closure against live
// accounting under the store lock, because only then is the source and
// destination measurement meaningful.
//
// A closure that could not be enumerated, and a store whose refs could not be
// read, are both reported as infeasible. "Unknown" must never read as "fits".
func (l *LegacyInfo) MigrationFeasible(limits Limits) bool {
	if l == nil || !l.Present || !l.IsRepository {
		return false
	}
	if l.RefsUnreadable || l.ClosureError != nil || !l.RetainsHistory() {
		return false
	}
	// The destination must be able to hold the closure, and the SOURCE is
	// charged against the budgets at the same time: the original duplicate still
	// exists until the duplicate-removal step completes, so both copies count.
	need, err := addInt64(l.ParentsNeeded, l.Bytes)
	if err != nil {
		return false
	}
	n := limits.Normalize()
	return need <= n.WorkspaceMaxBytes && need <= n.GlobalMaxBytes
}

// inspectLegacy performs the read-only detection pass for one legacy store.
//
// Every Git invocation is a read-only listing (`for-each-ref`, `rev-list`,
// `cat-file --batch-check`) with GIT_OPTIONAL_LOCKS=0 and the pinned
// configuration that disables auto-GC, maintenance, and hooks. Nothing here
// writes, prunes, or repacks, and no command holds the object list in memory:
// closure enumeration STREAMS the object list and consumes it in bounded
// batches.
func (m *Manager) inspectLegacy(ctx context.Context, workspace, path string, info os.FileInfo) (*LegacyInfo, error) {
	out := &LegacyInfo{Workspace: workspace, Path: path, Present: true}
	bytes, err := m.measureTreeAllocated(path, info)
	if err != nil {
		return nil, err
	}
	out.Bytes = bytes

	if !gitDirExists(path) {
		out.Warnings = append(out.Warnings, MaintenanceWarning{
			Reason:    ReasonLegacyRecoveryRequired,
			Workspace: workspace,
			Path:      path,
			Message:   "legacy store directory is not a Git repository, so no snapshot refs can be read from it",
		})
		return out, nil
	}
	out.IsRepository = true

	if _, hashes, err := m.snapshotRefsInDir(ctx, path); err != nil {
		out.RefsUnreadable = true
		out.RefListingError = err
		out.Warnings = append(out.Warnings, MaintenanceWarning{
			Reason:    ReasonUnreadableFile,
			Workspace: workspace,
			Path:      path,
			Message:   "legacy snapshot refs could not be read, so this store is not reported as empty: " + err.Error(),
		})
	} else {
		out.SnapshotRefs = len(hashes)
		out.SnapshotHashes = hashes
	}

	if branches, err := m.legacyBranches(ctx, path); err != nil {
		out.Warnings = append(out.Warnings, MaintenanceWarning{
			Reason:    ReasonUnreadableFile,
			Workspace: workspace,
			Path:      path,
			Message:   "legacy branch refs could not be read: " + err.Error(),
		})
	} else {
		out.Branches = branches
	}

	if err := m.walkLegacyClosure(ctx, path, func(_, _ string, size int64) error {
		out.Objects++
		// Charge the content plus a generous per-object overhead, rounded up to
		// an allocation unit, so the figure can only exceed the real cost.
		charge, err := addInt64(size, objectOverheadBytes)
		if err != nil {
			return err
		}
		if charge, err = m.limits.Normalize().RoundUp(charge); err != nil {
			return err
		}
		out.ParentsNeeded, err = addInt64(out.ParentsNeeded, charge)
		return err
	}); err != nil {
		out.ClosureError = err
		out.Warnings = append(out.Warnings, MaintenanceWarning{
			Reason:    ReasonOf(err),
			Workspace: workspace,
			Path:      path,
			Message:   "legacy object closure could not be enumerated: " + err.Error(),
		})
	}

	disposables, packs, err := m.legacyPackArtifacts(path)
	if err != nil {
		return nil, err
	}
	out.Disposable = disposables
	out.Packs = packs
	for _, d := range disposables {
		if out.DisposableBytes, err = addInt64(out.DisposableBytes, d.Bytes); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// legacyBranches lists a repository's branch refs with the commit each names.
// It is a read-only listing, and it reports what a "prune the snapshot refs"
// cleanup would have left behind: history reachable through a branch.
//
// A truncated listing is an error, never a short list: silently reporting fewer
// branches would understate what a migration must carry.
func (m *Manager) legacyBranches(ctx context.Context, dir string) ([]legacyBranchRef, error) {
	out, errOut, truncated, err := m.gitRun(ctx, dir, nil,
		"for-each-ref", "--format=%(objectname) %(refname)", "refs/heads/")
	if err != nil {
		return nil, m.storeError(ReasonUnreadableFile, dir,
			fmt.Errorf("list legacy branches: %w%s", err, gitOutputSuffix(errOut)))
	}
	if truncated {
		return nil, m.storeErrorf(ReasonUnreadableFile, dir,
			"legacy branch listing exceeded the %d byte command output bound", m.maxOutputBytes)
	}
	var refs []legacyBranchRef
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		hash, ref, err := parseRefLine(line)
		if err != nil {
			return nil, m.storeError(ReasonUnreadableFile, dir, err)
		}
		if !validObjectHash(hash) {
			return nil, m.storeErrorf(ReasonUnreadableFile, dir,
				"branch ref %s names non-hexadecimal object %q", ref, hash)
		}
		refs = append(refs, legacyBranchRef{Ref: ref, Hash: strings.ToLower(hash)})
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].Ref < refs[j].Ref })
	return refs, nil
}

// legacyLockFiles lists Git's own lock files anywhere in a repository. Their
// existence is unambiguous evidence of a writer that is active right now: Git
// holds the lock for the duration of a write and removes it afterwards, so a
// lock file is never an abandoned artifact.
func (m *Manager) legacyLockFiles(dir string) ([]string, error) {
	dirs := []string{
		dir,
		filepath.Join(dir, "objects"),
		filepath.Join(dir, "objects", "pack"),
		filepath.Join(dir, "refs"),
		filepath.Join(dir, "refs", "heads"),
		filepath.Join(dir, "refs", "snapshots"),
	}
	var out []string
	for _, d := range dirs {
		entries, err := readDirNoFollow(d)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, m.storeError(ReasonUnreadableFile, d, err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), gitLockSuffix) {
				out = append(out, filepath.Join(d, entry.Name()))
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// ---------------------------------------------------------------------------
// Reachable-closure enumeration
// ---------------------------------------------------------------------------

// objectOverheadBytes is the per-object overhead charged on top of a legacy
// object's content size when its closure is measured. It covers the loose
// object header, the zlib framing, and the object directory entry, and it is
// deliberately generous. It is a lower bound on nothing: the accounted figure
// can only exceed what the destination really needs.
const objectOverheadBytes = 64

// closureBatchSize bounds how many object names are fed to one `cat-file
// --batch-check` invocation. The batch's OUTPUT is what is buffered, so the
// bound is what keeps a large closure from ever being materialised.
const closureBatchSize = 256

// walkLegacyClosure enumerates the reachable object closure of a legacy
// repository, calling fn once per reachable object with its type and content
// size.
//
// The enumeration is streamed end to end, and that is load-bearing:
//
//   - `git rev-list --objects --all` writes its object list straight into this
//     function's line splitter through a process pipe, so a closure of millions
//     of objects is never buffered (the manager's ordinary buffered invocation
//     would have truncated the list at its output bound and silently
//     under-enumerated the closure);
//   - object names are fed to `git cat-file --batch-check` in bounded batches,
//     whose output IS checked for truncation and whose "missing" replies are
//     errors — a closure that cannot be fully described must never be reported
//     as a number a migration was admitted against.
func (m *Manager) walkLegacyClosure(ctx context.Context, dir string, fn func(hash, objType string, size int64) error) error {
	if fn == nil {
		return storeErrorf(ReasonInternal, "closure walk needs a callback")
	}
	w := &closureWalker{manager: m, ctx: ctx, dir: dir, fn: fn}
	stderr, truncated, err := m.gitRunEnvWriter(ctx, dir, w, "rev-list", "--objects", "--all")
	if err != nil {
		return m.storeError(ReasonUnreadableFile, dir,
			fmt.Errorf("rev-list --objects --all: %w%s", err, gitOutputSuffix(stderr)))
	}
	if truncated {
		// The stderr bound was hit, which is diagnostic noise rather than data
		// loss — but it means Git was complaining, so the listing is not trusted.
		return m.storeErrorf(ReasonUnreadableFile, dir, "rev-list stderr exceeded the command output bound")
	}
	if w.err != nil {
		return w.err
	}
	return w.flush()
}

// closureWalker is the streaming line splitter that turns rev-list's output
// into bounded cat-file batches.
type closureWalker struct {
	manager *Manager
	ctx     context.Context
	dir     string
	fn      func(hash, objType string, size int64) error

	// pending is the accumulated partial line, carried across Write calls
	// because a pipe hands over arbitrary chunk boundaries.
	pending []byte
	// batch holds the object names awaiting one cat-file invocation.
	batch []string
	// err latches the first failure; no further object is reported afterwards.
	err error
}

// Write consumes one chunk of rev-list output.
func (w *closureWalker) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.pending = append(w.pending, p...)
	for {
		i := indexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(w.pending[:i]))
		w.pending = w.pending[i+1:]
		if line == "" {
			continue
		}
		if err := w.add(line); err != nil {
			w.err = err
			return len(p), err
		}
	}
	return len(p), nil
}

// add records one rev-list line, flushing the batch when it is full.
func (w *closureWalker) add(line string) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	// A line is "<hash>" or "<hash> <path>". Only the name matters, and the path
	// can contain spaces, so the cut is at the FIRST space.
	hash, _, _ := strings.Cut(line, " ")
	if !validObjectHash(hash) {
		return w.manager.storeErrorf(ReasonUnreadableFile, w.dir,
			"rev-list reported an object name %q that is not a hexadecimal object id", hash)
	}
	w.batch = append(w.batch, hash)
	if len(w.batch) >= closureBatchSize {
		return w.flush()
	}
	return nil
}

// flush resolves the pending batch's type and size.
func (w *closureWalker) flush() error {
	if len(w.batch) == 0 {
		return nil
	}
	names := strings.Join(w.batch, "\n") + "\n"
	w.batch = w.batch[:0]
	out, stderr, truncated, err := w.manager.gitRunEnv(w.ctx, w.dir, "", strings.NewReader(names), nil,
		defaultMaxCommandOutput, "cat-file", "--batch-check")
	if err != nil {
		return w.manager.storeError(ReasonUnreadableFile, w.dir,
			fmt.Errorf("cat-file --batch-check: %w%s", err, gitOutputSuffix(stderr)))
	}
	if truncated {
		return w.manager.storeErrorf(ReasonUnreadableFile, w.dir,
			"cat-file --batch-check output exceeded the command output bound")
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return w.manager.storeErrorf(ReasonUnreadableFile, w.dir, "unexpected cat-file output %q", line)
		}
		if fields[1] == "missing" {
			return w.manager.storeErrorf(ReasonUnreadableFile, w.dir,
				"object %s is missing from the legacy store", fields[0])
		}
		if len(fields) < 3 {
			return w.manager.storeErrorf(ReasonUnreadableFile, w.dir, "unexpected cat-file output %q", line)
		}
		size, err := parseInt64(fields[2])
		if err != nil {
			return w.manager.storeErrorf(ReasonUnreadableFile, w.dir,
				"object %s has an unparseable size %q", fields[0], fields[2])
		}
		if err := w.fn(strings.ToLower(fields[0]), fields[1], size); err != nil {
			return err
		}
	}
	return nil
}

// indexByte returns the index of the first c in b, or -1. It exists so this
// file needs no extra import for one call site.
func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// legacyClosureBytes measures the reachable object closure of a legacy store:
// the conservative allocation of every object any ref can reach, and therefore
// the bytes a hash-preserving migration must copy.
//
// A failure is returned, never folded into a zero: an under-reported closure is
// how a migration would be admitted and then run out of room half way through.
func (m *Manager) legacyClosureBytes(ctx context.Context, dir string) (int64, error) {
	var total int64
	unit := m.limits.Normalize().AllocationUnitBytes
	err := m.walkLegacyClosure(ctx, dir, func(_, _ string, size int64) error {
		charge, err := addInt64(size, objectOverheadBytes)
		if err != nil {
			return err
		}
		if charge, err = roundUpAllocation(charge, unit); err != nil {
			return err
		}
		total, err = addInt64(total, charge)
		return err
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// parseInt64 parses a decimal integer, refusing anything else.
func parseInt64(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty number")
	}
	var n int64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("not a decimal integer: %q", s)
		}
		n = n*10 + int64(s[i]-'0')
		if n < 0 {
			return 0, fmt.Errorf("integer overflow: %q", s)
		}
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Store-wide survey
// ---------------------------------------------------------------------------

// LegacySurvey is the read-only state of every pre-v2 store under the snapshots
// root.
type LegacySurvey struct {
	// Root is the snapshots root.
	Root string
	// Stores lists every legacy store found, ordered by workspace hash.
	Stores []*LegacyInfo
	// Remnants lists the `<workspace>.deleting-<suffix>` directories an
	// interrupted recovery left behind. They are Marshal-owned accounted state:
	// a store renamed aside by a reset that did not finish removing it.
	Remnants []LegacyRemnant
	// TotalBytes is the measured size of every legacy store together.
	TotalBytes int64
	// DisposableBytes is the measured size of every recognized disposable
	// artifact across every legacy store.
	DisposableBytes int64
	// Warnings collects every problem found.
	Warnings []MaintenanceWarning
}

// LegacyRemnant is a store renamed into the accounted deleting state by a
// recovery operation that did not finish removing it.
type LegacyRemnant struct {
	// Workspace is the workspace hash the remnant belonged to.
	Workspace string
	// Path is the remnant directory, still inside the managed root.
	Path string
	// Bytes is its measured size.
	Bytes int64
}

// Store returns the surveyed store with the given workspace hash, or nil.
func (s *LegacySurvey) Store(workspace string) *LegacyInfo {
	if s == nil {
		return nil
	}
	for _, l := range s.Stores {
		if l.Workspace == workspace {
			return l
		}
	}
	return nil
}

// SurveyLegacy performs the read-only detection pass over every pre-v2 store.
//
// It requires the root to exist; an absent root reports an empty survey rather
// than an error, so the caller can run it unconditionally.
func (m *Manager) SurveyLegacy(ctx context.Context) (*LegacySurvey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	out := &LegacySurvey{Root: m.root}
	info, err := os.Lstat(m.root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return nil, m.storeError(ReasonUnreadableFile, m.root, err)
	}
	if !info.IsDir() {
		return nil, m.storeErrorf(ReasonInternal, m.root, "snapshots root is not a directory")
	}
	entries, err := readDirNoFollow(m.root)
	if err != nil {
		return nil, m.storeError(ReasonUnreadableFile, m.root, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if name == LockFileName || name == v2DirName {
			continue
		}
		entryInfo, err := lsEntryInfo(m.root, entry)
		if err != nil {
			return nil, m.storeError(ReasonUnreadableFile, filepath.Join(m.root, name), err)
		}
		path := filepath.Join(m.root, name)
		if !entryInfo.IsDir() {
			continue
		}
		if workspace, ok := parseLegacyRemnantName(name); ok {
			bytes, err := m.measureTreeAllocated(path, entryInfo)
			if err != nil {
				return nil, err
			}
			out.Remnants = append(out.Remnants, LegacyRemnant{Workspace: workspace, Path: path, Bytes: bytes})
			out.Warnings = append(out.Warnings, MaintenanceWarning{
				Reason:    ReasonLegacyRecoveryRequired,
				Workspace: workspace,
				Path:      path,
				Message: "an interrupted recovery renamed this store aside without removing it; " +
					"its history is still on disk until 'marshal snapshots cleanup' removes the remnant",
			})
			continue
		}
		if !validWorkspaceID(name) {
			continue
		}
		li, err := m.inspectLegacy(ctx, name, path, entryInfo)
		if err != nil {
			return nil, err
		}
		out.Stores = append(out.Stores, li)
		out.Warnings = append(out.Warnings, li.Warnings...)
		if out.TotalBytes, err = addInt64(out.TotalBytes, li.Bytes); err != nil {
			return nil, err
		}
		if out.DisposableBytes, err = addInt64(out.DisposableBytes, li.DisposableBytes); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out.Stores, func(i, j int) bool { return out.Stores[i].Workspace < out.Stores[j].Workspace })
	sort.SliceStable(out.Remnants, func(i, j int) bool { return out.Remnants[i].Path < out.Remnants[j].Path })
	return out, nil
}

// legacyRemnantFor returns the accounted-deleting remnant of one workspace, if
// an interrupted recovery left one.
func (m *Manager) legacyRemnantFor(workspace string) (string, bool) {
	if m == nil || !validWorkspaceID(workspace) {
		return "", false
	}
	entries, err := readDirNoFollow(m.root)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		ws, ok := parseLegacyRemnantName(entry.Name())
		if ok && ws == workspace {
			return filepath.Join(m.root, entry.Name()), true
		}
	}
	return "", false
}
