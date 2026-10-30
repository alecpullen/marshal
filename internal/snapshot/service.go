package snapshot

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"log/slog"
)

// Service captures one workspace into the bounded, manager-owned snapshot
// store.
//
// It is a thin, per-workspace façade over Manager and Catalog. It exists
// because the agent's Snapshotter surface is per-workspace and synchronous
// (`Track`, `Diff`, `Restore`), while the store's ownership, accounting, and
// reclamation are global. Everything the façade does is therefore:
//
//  1. resolve (once) the Manager for the data directory and the Catalog for
//     this workspace, both derived from the SAME data directory and workspace
//     root, so a service and a manager can never disagree about which directory
//     belongs to which workspace;
//  2. take the store lock for the duration of the operation, because every
//     managed capture allocates and every reclamation deletes;
//  3. delegate.
//
// The lock is taken per operation rather than held for the Service's lifetime:
// a Service lives as long as a session and is idle most of that time, whereas
// the store lock excludes other PROCESSES. Holding it across an idle session
// would block the user's own `marshal snapshots` commands for no reason.
type Service struct {
	dataDir string
	// workTree is the workspace root this service captures.
	workTree string
	logger   *slog.Logger
	enabled  bool
	maxFile  int64
	ignore   []string
	// gitBinary overrides the Git executable this service's manager runs. It is
	// a fault-injection seam: a test installs a recording wrapper so it can
	// assert exactly which Git commands a lifecycle path runs. Production leaves
	// it empty (the manager default is "git").
	gitBinary string

	// sem serialises this service's operations. It is a channel rather than a
	// sync.Mutex so a caller can give up when its context ends instead of
	// queueing behind an in-flight snapshot indefinitely.
	sem chan struct{}

	// mu guards the lazily resolved manager and catalog. It is a leaf lock and
	// is never held while the store lock is taken.
	mu      sync.Mutex
	manager *Manager
	catalog *Catalog
}

// ServiceOption configures a Service. Every option is optional; the
// five-argument New form keeps working unchanged.
type ServiceOption func(*Service)

// WithServiceGitBinary installs the Git executable this service's manager runs
// for capture, lookup and maintenance alike, so a recording wrapper observes
// every invocation a service makes. It is a fault-injection seam: production
// never calls it.
func WithServiceGitBinary(path string) ServiceOption {
	return func(s *Service) {
		if path != "" {
			s.gitBinary = path
		}
	}
}

// New builds a Service for one workspace rooted at projectRoot, storing
// snapshots below dataDir.
//
// Nothing is created here: the store root, its lock file, and the workspace
// catalog are all created lazily on the first capture that actually needs
// them. That is what keeps `snapshots.enabled = false` from leaving an empty
// store behind, and it is why a disabled service never touches the filesystem.
func New(dataDir, projectRoot string, maxFileBytes int64, ignore []string, logger *slog.Logger, opts ...ServiceOption) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		dataDir:  dataDir,
		workTree: projectRoot,
		logger:   logger,
		enabled:  true,
		maxFile:  maxFileBytes,
		ignore:   ignore,
		sem:      make(chan struct{}, 1),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if !gitAvailable() {
		s.enabled = false
		logger.Warn("git not found; snapshots disabled")
	}
	return s
}

// managerOptions is the manager configuration every per-workspace operation
// uses, so a capture and a maintenance sweep can never disagree about the Git
// executable in force.
func (s *Service) managerOptions() []ManagerOption {
	if s.gitBinary == "" {
		return nil
	}
	return []ManagerOption{WithGitBinary(s.gitBinary)}
}

// Enabled reports whether capture is possible at all.
func (s *Service) Enabled() bool { return s.enabled }

// WorkTree is the workspace root this service captures.
func (s *Service) WorkTree() string { return s.workTree }

// DataDir is the Marshal user data directory this service stores snapshots
// below.
func (s *Service) DataDir() string { return s.dataDir }

// StoreDir is the versioned store directory this service captures into,
// <dataDir>/snapshots/v2/<canonical-workspace-hash>. It is reported rather than
// derived by callers so a Service and a Manager can never disagree about the
// layout.
//
// The reported identifier is the canonical one captures use. The raw-path hash
// is only a diagnostic fallback for a root that cannot be canonicalized.
// Nothing is created here.
func (s *Service) StoreDir() string {
	return filepath.Join(s.dataDir, snapshotsDirName, v2DirName, s.Workspace())
}

// Workspace is this service's workspace hash.
//
// It is the canonical identifier the manager uses for captures, so a service
// and a manager built from the same workspace root always agree on the store
// even when one of them was handed an alias of that root. The raw-path hash is
// only a diagnostic fallback for a root that cannot be canonicalized. Nothing
// is created here.
func (s *Service) Workspace() string {
	_, ids, err := s.serviceWorkspaceIDs()
	if err != nil || len(ids) == 0 {
		return WorkspaceHashFor(s.workTree)
	}
	return ids[0]
}

// serviceWorkspaceIDs returns canonical and original-path hashes in that
// order, deduplicated. canonicalRoot is the validated canonical workspace.
//
// The first identifier is the one captures use; the second is the raw-path
// hash, which is kept because a store written by an older layout may still be
// named by it. Nothing is stat'd or created beyond the symlink resolution
// canonicalWorkspaceRoot performs.
func (s *Service) serviceWorkspaceIDs() (canonicalRoot string, ids []string, err error) {
	canonicalRoot, err = canonicalWorkspaceRoot(s.workTree)
	if err != nil {
		return "", nil, err
	}
	canonical := WorkspaceHashFor(canonicalRoot)
	ids = []string{canonical}
	if raw := WorkspaceHashFor(s.workTree); raw != canonical {
		ids = append(ids, raw)
	}
	return canonicalRoot, ids, nil
}

// serviceIdentity is one identity of this service's workspace that a lookup or
// a maintenance pass may consult.
type serviceIdentity struct {
	// cat is the catalog handle for the identity. For a legacyOnly identity it
	// is a bare handle whose only job is to name that identity's pre-v2
	// repository.
	cat *Catalog
	// legacyOnly is set when the identity has no validated v2 catalog, so only
	// its pre-v2 bare repository may be consulted.
	legacyOnly bool
}

// serviceIdentities resolves every known identity of this service's workspace,
// canonical first: the catalog captures use, then one entry per historical
// identity.
//
// A historical identity is included only when it holds a v2 catalog whose
// manifest provenance validates, or a pre-v2 bare repository. Nothing is
// created, moved, merged or deleted here: an identity that holds neither is left
// alone, and an unreadable or mismatched catalog is reported rather than
// silently skipped.
func (s *Service) serviceIdentities(m *Manager, canonical *Catalog) ([]serviceIdentity, error) {
	if m == nil || canonical == nil {
		return nil, storeErrorf(ReasonInternal, "snapshot identities need a manager and a canonical catalog")
	}
	canonicalRoot, ids, err := s.serviceWorkspaceIDs()
	if err != nil {
		return nil, err
	}
	out := []serviceIdentity{{cat: canonical}}
	for _, id := range ids[1:] {
		cat, present, err := s.historicalCatalog(m, canonicalRoot, id)
		if err != nil {
			return nil, err
		}
		if present {
			out = append(out, serviceIdentity{cat: cat})
			continue
		}
		// No validated v2 catalog. A pre-v2 bare repository has no root
		// provenance at all, so it is consulted at its own exact hash — never a
		// guessed one, and never a store discovered elsewhere.
		raw, err := m.Catalog(id)
		if err != nil {
			return nil, err
		}
		if !legacyRepoPresent(m, raw) {
			continue
		}
		out = append(out, serviceIdentity{cat: raw, legacyOnly: true})
	}
	return out, nil
}

// serviceCatalogs returns the canonical catalog and any existing, validated
// historical v2 catalog, in that order.
//
// It must be called with manager store ownership held. It performs no
// migration or deletion: an identity whose store is absent is simply not in the
// result, and one whose manifest cannot be read or does not describe this
// workspace is an error.
func (s *Service) serviceCatalogs(m *Manager, canonical *Catalog) ([]*Catalog, error) {
	identities, err := s.serviceIdentities(m, canonical)
	if err != nil {
		return nil, err
	}
	cats := make([]*Catalog, 0, len(identities))
	for _, identity := range identities {
		if identity.legacyOnly {
			continue
		}
		cats = append(cats, identity.cat)
	}
	return cats, nil
}

// historicalCatalog resolves one KNOWN historical identity to a v2 catalog that
// may be trusted, or reports that it holds none.
//
// Validation is provenance: the catalog is trusted only when its readable
// manifest names, canonicalized now, the same canonical workspace root. A store
// that cannot be read, or that describes a different workspace, is reported —
// never used to serve or reclaim another workspace's snapshots. A historical
// catalog is not trusted as a symbolic link either, because following one would
// make "this workspace's store" a path this process never validated.
func (s *Service) historicalCatalog(m *Manager, canonicalRoot, id string) (*Catalog, bool, error) {
	cat, err := m.Catalog(id)
	if err != nil {
		return nil, false, err
	}
	info, err := os.Lstat(cat.Root())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, m.storeError(ReasonUnreadableFile, cat.Root(), err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, storeErrorf(ReasonSymlinkEscape,
			"historical snapshot catalog %s is a symbolic link, so it is not trusted as this workspace's store", cat.Root())
	}
	if !info.IsDir() {
		return nil, false, m.storeErrorf(ReasonInternal, cat.Root(), "historical snapshot catalog is not a directory")
	}
	man, err := cat.Load()
	if err != nil {
		return nil, false, err
	}
	if man == nil {
		// A directory with no manifest is not a v2 catalog: it has no
		// provenance to validate, so it is left untouched.
		return nil, false, nil
	}
	if man.Root == "" {
		return nil, false, cat.errf(ReasonUnreadableFile,
			"historical catalog %s records no workspace root, so it cannot be attributed to this workspace", id)
	}
	root, err := canonicalWorkspaceRoot(man.Root)
	if err != nil {
		return nil, false, err
	}
	if root != canonicalRoot {
		return nil, false, cat.errf(ReasonUnreadableFile,
			"historical catalog %s records workspace root %q, which is not %q", id, man.Root, canonicalRoot)
	}
	cat.WorkspaceRoot = canonicalRoot
	return cat, true, nil
}

// lookupServiceSnapshotOwned tries the known identities under the store lock,
// canonical first.
//
// It falls back to a second identity only when the canonical one reports the
// snapshot as not found or expired: an unreadable, corrupt, cancelled or
// otherwise unexpected canonical result is returned immediately, because a
// repair problem must not be masked by a hit elsewhere. A corrupt historical
// candidate is surfaced for the same reason.
func (s *Service) lookupServiceSnapshotOwned(ctx context.Context, m *Manager, canonical *Catalog, hash string) (*SnapshotLocation, error) {
	// The canonical identity is answered FIRST, and an unexpected result is
	// final: resolving the historical identities eagerly would let a broken
	// historical catalog mask a perfectly good canonical rollback point.
	loc, canonicalErr := lookupSnapshotOwned(ctx, m, canonical, hash)
	if canonicalErr == nil {
		return loc, nil
	}
	if !lookupMayHaveAnotherIdentity(canonicalErr) {
		return nil, canonicalErr
	}
	identities, err := s.serviceIdentities(m, canonical)
	if err != nil {
		return nil, err
	}
	// A canonical "expired" means the workspace HAS a store that does not retain
	// this hash. That distinction survives the fallback: if every identity is
	// absent or lacks the hash, the answer is still an expiry, not "never
	// captured".
	sawStore := ReasonOf(canonicalErr) == ReasonSnapshotExpired

	for _, identity := range identities[1:] {
		var found *SnapshotLocation
		var err error
		if identity.legacyOnly {
			found, err = legacyLookup(ctx, m, identity.cat, hash)
		} else {
			found, err = lookupSnapshotOwned(ctx, m, identity.cat, hash)
		}
		if err == nil {
			return found, nil
		}
		switch ReasonOf(err) {
		case ReasonSnapshotNotFound:
			// This identity holds no store at all; keep looking.
		case ReasonSnapshotExpired:
			sawStore = true
		default:
			return nil, err
		}
	}
	if sawStore {
		return nil, expiredSnapshotError(canonical, hash)
	}
	return nil, canonicalErr
}

// lookupMayHaveAnotherIdentity reports whether a failed canonical lookup is one
// a second known identity could still answer.
func lookupMayHaveAnotherIdentity(err error) bool {
	switch ReasonOf(err) {
	case ReasonSnapshotNotFound, ReasonSnapshotExpired:
		return true
	}
	return false
}

// lock acquires the operation semaphore, returning the context error if ctx
// ends first. On a non-nil error the semaphore was NOT acquired and unlock must
// not be called.
func (s *Service) lock(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) unlock() { <-s.sem }

// store resolves the manager and catalog for this workspace, creating them on
// first use. It performs no I/O beyond constructing handles.
func (s *Service) store() (*Manager, *Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.manager != nil {
		return s.manager, s.catalog, nil
	}
	m, err := NewManager(s.dataDir, s.managerOptions()...)
	if err != nil {
		return nil, nil, err
	}
	cat, err := m.CatalogForRoot(s.workTree)
	if err != nil {
		return nil, nil, err
	}
	s.manager, s.catalog = m, cat
	return m, cat, nil
}

// withStore runs fn with the store lock held.
//
// Bootstrap runs before Acquire because Bootstrap is what accounts for the root
// directory and the permanent lock file; creating them without accounting for
// them is exactly how metadata creation evades a tiny configured limit.
//
// A release failure is joined with fn's error rather than only logged. An owner
// that could not prove its writers are gone still holds the store, so reporting
// the operation as successful would let a caller believe the snapshot is safely
// published while this process keeps the lock the next owner needs. Both causes
// stay visible: errors.Is/As reach fn's typed error through the join.
func (s *Service) withStore(ctx context.Context, fn func(*Manager, *Catalog) error) (err error) {
	m, cat, err := s.store()
	if err != nil {
		return err
	}
	if err := m.Bootstrap(ctx); err != nil {
		return err
	}
	if _, err := m.Acquire(ctx); err != nil {
		return err
	}
	defer func() {
		releaseErr := m.Release()
		if releaseErr == nil {
			return
		}
		// Ownership was retained because a writer could not be confirmed dead.
		// Log it: the alternative is silence about a store this process still
		// owns.
		s.logger.Warn("snapshot store ownership was not released", "error", releaseErr)
		err = errors.Join(err, releaseErr)
	}()
	return fn(m, cat)
}

// Track captures this workspace and returns the published snapshot hash.
//
// The empty hash with a nil error means "no snapshot was taken" — a disabled
// service, or a workspace with nothing eligible to capture. The empty hash with
// a non-nil error means the capture failed, and a caller must record a snapshot
// row only when the hash is non-empty and the error is nil.
func (s *Service) Track(ctx context.Context) (string, error) {
	if !s.enabled {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.lock(ctx); err != nil {
		return "", err
	}
	defer s.unlock()

	var hash string
	err := s.withStore(ctx, func(m *Manager, cat *Catalog) error {
		var captureErr error
		hash, _, captureErr = m.AdmitAndCaptureWithCleanup(ctx, cat, CaptureRequest{
			WorkspaceRoot: s.workTree,
			Ignore:        s.ignore,
			MaxFileBytes:  s.maxFile,
			Bounds:        defaultCaptureBounds(),
		})
		return captureErr
	})
	return hash, err
}

// Diff returns a unified diff between the snapshot named by hash and the
// current contents of the work tree.
//
// The diff runs READ-ONLY. Its comparison index is a version-2 index the
// MANAGER encodes from the snapshot's OWN tree entries into a temporary file
// OUTSIDE the managed store; Git is invoked with GIT_OPTIONAL_LOCKS=0 so it may
// not rewrite it. That is what keeps the repository a pure read: the previous
// approach used `git read-tree`, which WRITES an index, and a native Git writer
// anywhere in the managed root is precisely what the design forbids.
//
// The index must still be populated, and it must be populated with the
// snapshot's entries. An EMPTY index (what a missing GIT_INDEX_FILE effectively
// is) makes Git report every snapshot path as deleted, which is not a diff of
// the change — it is a diff against nothing.
//
// `diff-index` is used rather than `diff` so the comparison is explicitly
// INDEX-vs-WORK-TREE: `git diff <tree>` builds a temporary index of its own and
// can refresh the one it is given, whereas `diff-index --patch <tree>` compares
// the index exactly as the manager wrote it to the work tree and leaves it in
// place. That is what keeps the manager-encoded index a manager-written file.
//
// --no-ext-diff and --no-textconv are required, not merely defensive: the
// sanitized environment clears GIT_EXTERNAL_DIFF and GIT_DIFF_OPTS, but a
// repository-level diff.external or a `.gitattributes` textconv driver would
// otherwise still execute an arbitrary program. The pinned configuration also
// clears hooks and auto-maintenance, so no external program runs here.
//
// The caller holds the in-process semaphore and, through withStore, the
// cross-process store lock. That is the lock that stops a concurrent
// reclamation from deleting the generation out from under this diff.
func (s *Service) Diff(ctx context.Context, hash string) (string, error) {
	if !s.enabled {
		return "", nil
	}
	if !validObjectHash(hash) {
		return "", storeErrorf(ReasonInvalidObjectHash,
			"snapshot %q is not a hexadecimal object id, so it is not diffed as a revision", hash)
	}
	if err := s.lock(ctx); err != nil {
		return "", err
	}
	defer s.unlock()

	var out string
	err := s.withStore(ctx, func(m *Manager, cat *Catalog) error {
		loc, err := s.lookupServiceSnapshotOwned(ctx, m, cat, hash)
		if err != nil {
			return err
		}
		scratch, err := m.encodeReadOnlyIndex(ctx, loc)
		if err != nil {
			return err
		}
		defer scratch.remove()
		diff, diffErr, _, err := m.gitRunEnv(ctx, loc.GitDir, s.workTree, nil, scratch.env(), defaultMaxCommandOutput,
			"diff-index", "--patch", "--no-ext-diff", "--no-textconv", loc.Hash)
		if err != nil {
			return fmt.Errorf("diff snapshot %s: %w%s", hash, err, gitOutputSuffix(diffErr))
		}
		out = strings.TrimRight(string(diff), "\n")
		return nil
	})
	if err != nil {
		return "", err
	}
	return out, nil
}

// Restore writes the snapshot named by hash back into the work tree.
//
// It is deliberately NOT a `git checkout`. A checkout writes an index, and the
// design forbids any native Git writer inside the managed root; it would also
// make the restore's scope a Git decision rather than ours. Instead the
// snapshot's trees are ENUMERATED read-only (`ls-tree -r -z`) and each blob is
// streamed straight into its workspace file with `cat-file blob`, so:
//
//   - the repository is only ever read;
//   - the scope is exactly the snapshot's own paths, which preserves the
//     established behaviour of leaving extra workspace files alone;
//   - mode bits and symlinks are applied explicitly rather than left to Git;
//   - no destination symlink is ever followed out of the workspace, because
//     each destination is opened with O_NOFOLLOW through an openat() walk.
func (s *Service) Restore(ctx context.Context, hash string) error {
	if !s.enabled {
		return nil
	}
	if !validObjectHash(hash) {
		return storeErrorf(ReasonInvalidObjectHash,
			"snapshot %q is not a hexadecimal object id, so it is not restored as a revision", hash)
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()

	return s.withStore(ctx, func(m *Manager, cat *Catalog) error {
		loc, err := s.lookupServiceSnapshotOwned(ctx, m, cat, hash)
		if err != nil {
			return err
		}
		return m.restoreSnapshotToWorkTree(ctx, loc, s.workTree)
	})
}

// hasStore reports whether this workspace already has a versioned store under
// EITHER known identity, the canonical one or the original supplied spelling.
//
// It exists because a MAINTENANCE pass over a workspace that has never captured
// must be a no-op rather than a reason to bootstrap the store. Bootstrapping
// means measuring the whole snapshots root and comparing that figure against
// the ceilings — an expensive scan on a machine holding another workspace's
// legacy history, and a scan whose answer can legitimately be "this store is
// already over budget". Neither belongs on the shutdown path of a session that
// never captured anything.
//
// The check is one Lstat per known identity, and it is deliberately
// per-workspace: a versioned store that DOES exist is maintained even when a
// different workspace is over budget.
func (s *Service) hasStore() bool {
	_, ids, err := s.serviceWorkspaceIDs()
	if err != nil || len(ids) == 0 {
		// A root that cannot be canonicalized still gets the diagnostic spelling
		// checked; store() propagates the canonicalization error itself.
		ids = []string{WorkspaceHashFor(s.workTree)}
	}
	for _, id := range ids {
		if catalogDirExists(filepath.Join(s.dataDir, snapshotsDirName, v2DirName, id)) {
			return true
		}
	}
	return false
}

// Prune reclaims every generation whose snapshots have ALL expired under
// retentionDays.
//
// RetentionDays keeps the meaning the legacy prune had: a negative value
// disables expiry, and zero expires everything older than now.
//
// A workspace with no versioned store is a no-op: there is nothing to expire,
// and the old path's "not a git repository" diagnostic was logged on every
// single shutdown because of it.
func (s *Service) Prune(ctx context.Context, retentionDays int) error {
	if !s.enabled || retentionDays < 0 {
		return nil
	}
	if !s.hasStore() {
		return nil
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()

	return s.withStore(ctx, func(m *Manager, cat *Catalog) error {
		cats, err := s.serviceCatalogs(m, cat)
		if err != nil {
			return err
		}
		// Canonical first, then any validated historical catalog. Each existing
		// reclaim commits its own progress, so this is NOT an atomic multi-store
		// transaction: on the first error the sweep stops and returns it, and a
		// later call resumes from whatever the stores actually contain.
		for _, c := range cats {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := m.ReclaimExpiredWorkspace(ctx, c, retentionDays); err != nil {
				return err
			}
		}
		return nil
	})
}

// Status reports a structured problem when this workspace's store needs
// attention, or nil when it does not. It runs no heavy storage work.
//
// It is scoped to THIS workspace and, like Prune, is a no-op when there is no
// versioned store: a whole-root status scan costs real time on a store holding
// other workspaces' history, and the runtime calls this on the shutdown path
// for every session. Every validated catalog of this workspace is reported on,
// so a problem in a historical store is not hidden by a healthy canonical one.
func (s *Service) Status(ctx context.Context) error {
	if !s.enabled || !s.hasStore() {
		return nil
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	return s.withStore(ctx, func(m *Manager, cat *Catalog) error {
		cats, err := s.serviceCatalogs(m, cat)
		if err != nil {
			return err
		}
		for _, c := range cats {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := m.StatusWorkspace(ctx, c.Workspace()); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Read-only index for a snapshot diff
// ---------------------------------------------------------------------------

// Git's index format, version 2. Only the constants this encoder emits are
// named; the layout is fixed and stable (it is a documented on-disk format Git
// still reads and writes itself), and nothing here ever asks Git to write one.
const (
	gitIndexSignature    = "DIRC"
	gitIndexVersion      = 2
	gitIndexHeaderBytes  = 12 // signature(4) + version(4) + entry count(4)
	gitIndexEntryPadding = 8  // entries are padded with NULs to a multiple of 8
	gitIndexChecksumLen  = 20 // SHA-1 over everything before it
	gitIndexNameMaxBytes = 0xFFF
	// The stat fields that precede the object id: ctime(8) mtime(8) dev(4)
	// ino(4) mode(4) uid(4) gid(4) size(4). The mode sits at offset 24.
	gitIndexStatFields = 40
	// An entry's path-independent part: the 40 stat bytes, a 20-byte object id,
	// and a 2-byte flags word whose low 12 bits are the path length.
	gitIndexEntryFixed = gitIndexStatFields + 20 + 2
	// gitIndexObjectIDBytes is the SHA-1 object id width Git writes here.
	gitIndexObjectIDBytes = 20
)

// readOnlyIndexScratch is the temporary index Diff compares against.
//
// It lives in the operating system's temporary directory, NOT under the managed
// root, and that is the point: the design forbids any native Git writer (and any
// unaccounted allocation) inside the managed root, and the index this operation
// needs is written by the MANAGER as part of a bounded, pre-sized buffer rather
// than by `git read-tree`.
type readOnlyIndexScratch struct {
	dir       string
	indexPath string
	// bytes is the exact encoded index length, reserved before the file is
	// created so the allocation is a deliberate one rather than whatever the
	// write happened to need.
	bytes int64
}

// remove discards the scratch directory. It is best-effort: the operation it
// supported has already finished, and a leftover temporary directory outside
// the managed root is not a store artifact.
func (w *readOnlyIndexScratch) remove() {
	if w == nil || w.dir == "" {
		return
	}
	_ = os.RemoveAll(w.dir)
}

// env pins the index file for a managed Git invocation.
func (w *readOnlyIndexScratch) env() []string {
	return []string{"GIT_INDEX_FILE=" + w.indexPath}
}

// indexEntry is one tree entry to encode, read straight out of `ls-tree`.
type indexEntry struct {
	// mode is the numeric Git mode (100644, 100755, 120000, …).
	mode uint32
	// hash is the object id the path names.
	hash string
	// path is the entry's repository-relative path.
	path string
}

// encodedIndexBytes is the EXACT size of the version-2 index for entries.
//
// It accounts for every component the format charges for: the 12-byte header,
// each entry's 62-byte fixed part, the object id, the path, and the NUL padding
// that rounds each entry up to a multiple of eight — plus the trailing 20-byte
// SHA-1 checksum. It is exact rather than approximate so the caller can compare
// the encoder's output against it (a drift between the two would be a silent
// under-reservation) and then round it to allocation units for the reservation.
func encodedIndexBytes(entries []indexEntry) (int64, error) {
	total, err := addInt64(gitIndexHeaderBytes, gitIndexChecksumLen)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if len(e.path) > gitIndexNameMaxBytes {
			return 0, storeErrorf(ReasonInternal,
				"index entry path is %d bytes, over the %d byte index limit", len(e.path), gitIndexNameMaxBytes)
		}
		per := gitIndexEntryFixed + len(e.path)
		if rem := per % gitIndexEntryPadding; rem != 0 {
			per += gitIndexEntryPadding - rem
		}
		if total, err = addInt64(total, int64(per)); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// reservedIndexBytes is the store-facing reservation for an encoded index: the
// exact byte count rounded up to the allocation unit every other managed
// allocation is expressed in.
func reservedIndexBytes(entries []indexEntry) (int64, error) {
	exact, err := encodedIndexBytes(entries)
	if err != nil {
		return 0, err
	}
	return roundUpAllocation(exact, DefaultAllocationUnitBytes)
}

// encodeReadOnlyIndex builds a version-2 Git index from a snapshot's own tree
// entries and writes it to a temporary file outside the managed store.
//
// The index is what makes a read-only diff correct: `git diff <commit>` with no
// populated index reports every path in the snapshot as deleted. Git is asked
// for the entries with `ls-tree -r -z` (read-only, NUL-delimited so a path with
// spaces or newlines survives intact), the encoder writes the index itself, and
// the following diff runs with GIT_OPTIONAL_LOCKS=0 so Git may not rewrite it.
func (m *Manager) encodeReadOnlyIndex(ctx context.Context, loc *SnapshotLocation) (*readOnlyIndexScratch, error) {
	if loc == nil {
		return nil, storeErrorf(ReasonInternal, "index encoding needs a snapshot location")
	}
	entries, err := m.snapshotTreeEntries(ctx, loc.GitDir, loc.Hash)
	if err != nil {
		return nil, err
	}
	exact, err := encodedIndexBytes(entries)
	if err != nil {
		return nil, err
	}
	reserved, err := reservedIndexBytes(entries)
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "marshal-snapshot-index-")
	if err != nil {
		return nil, storeError(ReasonUnreadableFile, fmt.Errorf("create index scratch: %w", err))
	}
	scratch := &readOnlyIndexScratch{dir: dir, indexPath: filepath.Join(dir, "index"), bytes: reserved}
	buf, err := encodeGitIndex(entries)
	if err != nil {
		scratch.remove()
		return nil, err
	}
	if int64(len(buf)) != exact {
		// The reservation and the encoder must never drift: a reservation that
		// under-counts is how an unaccounted allocation ships.
		scratch.remove()
		return nil, storeErrorf(ReasonInternal,
			"encoded index is %d bytes but the entry count accounted for %d", len(buf), exact)
	}
	if err := os.WriteFile(scratch.indexPath, buf, 0o600); err != nil {
		scratch.remove()
		return nil, storeError(ReasonUnreadableFile, fmt.Errorf("write index scratch: %w", err))
	}
	return scratch, nil
}

// snapshotTreeEntries lists a snapshot's recursive tree entries.
//
// `-r` recurses, `-l` is deliberately NOT used (the size column is display
// padding, not part of the entry we encode), and `-z` keeps unusual path names
// intact. Every field is validated before it is encoded: a malformed line is an
// error rather than a silently skipped path, because a missing entry would make
// the diff claim a file was deleted.
func (m *Manager) snapshotTreeEntries(ctx context.Context, gitDir, hash string) ([]indexEntry, error) {
	if !validObjectHash(hash) {
		return nil, storeErrorf(ReasonInvalidObjectHash, "snapshot %q is not a hexadecimal object id", hash)
	}
	if _, err := m.gitOutput(ctx, gitDir, "cat-file", "-e", hash+"^{commit}"); err != nil {
		return nil, fmt.Errorf("snapshot %s is not readable in %s: %w", hash, gitDir, err)
	}
	out, err := m.gitOutput(ctx, gitDir, "ls-tree", "-r", "-z", hash)
	if err != nil {
		return nil, fmt.Errorf("list snapshot %s: %w", hash, err)
	}
	var entries []indexEntry
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		entry, err := parseLsTreeEntry(rec)
		if err != nil {
			return nil, m.storeError(ReasonUnreadableFile, gitDir, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// parseLsTreeEntry parses one `ls-tree` record: "<mode> SP <type> SP <oid> TAB <path>".
func parseLsTreeEntry(rec string) (indexEntry, error) {
	meta, path, ok := strings.Cut(rec, "\t")
	if !ok {
		return indexEntry{}, fmt.Errorf("unparseable tree entry %q", rec)
	}
	fields := strings.Fields(meta)
	if len(fields) != 3 {
		return indexEntry{}, fmt.Errorf("unparseable tree entry metadata %q", meta)
	}
	mode, err := strconv.ParseUint(fields[0], 8, 32)
	if err != nil {
		return indexEntry{}, fmt.Errorf("tree entry mode %q is not octal: %w", fields[0], err)
	}
	if !validObjectHash(fields[2]) {
		return indexEntry{}, fmt.Errorf("tree entry %q names non-hexadecimal object %q", path, fields[2])
	}
	return indexEntry{mode: uint32(mode), hash: strings.ToLower(fields[2]), path: path}, nil
}

// encodeGitIndex serializes entries as a version-2 index.
//
// The index is sorted by path, which is what the format requires and what Git
// relies on for its binary search. The entries `ls-tree` returns are already in
// tree order, which for Git's tree encoding is the same ordering, but sorting
// explicitly here means the encoder is correct even if a caller passes entries
// in a different order.
func encodeGitIndex(entries []indexEntry) ([]byte, error) {
	sorted := make([]indexEntry, len(entries))
	copy(sorted, entries)
	sortIndexEntries(sorted)

	total, err := encodedIndexBytes(sorted)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 0, total)
	header := make([]byte, gitIndexHeaderBytes)
	copy(header, gitIndexSignature)
	binary.BigEndian.PutUint32(header[4:], gitIndexVersion)
	binary.BigEndian.PutUint32(header[8:], uint32(len(sorted)))
	buf = append(buf, header...)

	for _, e := range sorted {
		// ctime, mtime, dev, ino, uid, gid, and size are all deliberately zero:
		// this index exists to answer "what did the snapshot contain?", so the
		// stat cache would be noise — and it is never written back, because the
		// diff runs with GIT_OPTIONAL_LOCKS=0.
		statFields := make([]byte, gitIndexStatFields)
		binary.BigEndian.PutUint32(statFields[24:], e.mode)
		buf = append(buf, statFields...)

		oid, err := hexDecode(e.hash)
		if err != nil {
			return nil, storeErrorf(ReasonInternal, "index object id %q is not hexadecimal: %v", e.hash, err)
		}
		if len(oid) != gitIndexObjectIDBytes {
			return nil, storeErrorf(ReasonInternal,
				"index object id %q is %d bytes, want %d", e.hash, len(oid), gitIndexObjectIDBytes)
		}
		buf = append(buf, oid...)

		// The low 12 bits of the flags word are the path length, saturating at
		// 0xFFF.
		nameLen := len(e.path)
		if nameLen > gitIndexNameMaxBytes {
			nameLen = gitIndexNameMaxBytes
		}
		var flags [2]byte
		binary.BigEndian.PutUint16(flags[:], uint16(nameLen))
		buf = append(buf, flags[:]...)

		buf = append(buf, e.path...)
		// Each entry is NUL-padded to a multiple of eight, with at least one
		// NUL terminator.
		pad := gitIndexEntryPadding - ((gitIndexEntryFixed + len(e.path)) % gitIndexEntryPadding)
		buf = append(buf, make([]byte, pad)...)
	}
	sum := sha1.Sum(buf)
	buf = append(buf, sum[:]...)
	if int64(len(buf)) != total {
		return nil, storeErrorf(ReasonInternal, "encoded index is %d bytes, expected %d", len(buf), total)
	}
	return buf, nil
}

// sortIndexEntries sorts by path bytes, then by stage. The encoder only ever
// emits stage-0 entries, so the path comparison is the whole ordering.
func sortIndexEntries(entries []indexEntry) {
	// Insertion sort: an index is built for a diff, and the entry counts here
	// are small enough that the simplicity is worth more than the asymptotics.
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].path < entries[j-1].path; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

// restoreSnapshotToWorkTree writes a snapshot's own paths back into workTree.
//
// The scope is exactly the snapshot's paths: a file the snapshot never
// contained is left alone, which is the behaviour restore has always had. Each
// blob is streamed from `cat-file blob` straight into its destination, so the
// repository is only ever read and no index is written anywhere.
func (m *Manager) restoreSnapshotToWorkTree(ctx context.Context, loc *SnapshotLocation, workTree string) error {
	if loc == nil {
		return storeErrorf(ReasonInternal, "restore needs a snapshot location")
	}
	if workTree == "" {
		return storeErrorf(ReasonInternal, "restore needs a workspace root")
	}
	root, err := os.OpenRoot(workTree)
	if err != nil {
		return storeError(ReasonUnreadableFile, fmt.Errorf("open workspace %s: %w", workTree, err))
	}
	defer root.Close()

	entries, err := m.snapshotTreeEntries(ctx, loc.GitDir, loc.Hash)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !restorableGitMode(e.mode) {
			// A gitlink (a submodule) cannot be restored into the workspace as
			// a file, and writing something else in its place would not be the
			// snapshot. Refuse rather than guess at what the user wanted.
			return storeErrorf(ReasonUnsupportedFileType,
				"snapshot %s contains %s with mode %o, which cannot be restored as a workspace file",
				loc.Hash, e.path, e.mode)
		}
		if err := restoreTreeEntry(ctx, m, loc, root, e); err != nil {
			return err
		}
	}
	return nil
}

// restorableGitMode reports whether a tree entry's mode names something restore
// can write into a workspace: a regular file (with or without the executable
// bit) or a symlink.
func restorableGitMode(mode uint32) bool {
	switch mode {
	case 0o100644, 0o100755, gitModeSymlink:
		return true
	}
	return false
}

// restoreTreeEntry recreates one snapshot entry inside root.
//
// Every filesystem call goes through os.Root, which resolves the path inside
// the workspace and refuses a symbolic link that would leave it. The explicit
// Lstat is belt-and-braces on top of that: a symlink sitting at the destination
// (or at any directory component) is reported rather than written through, so a
// link planted in the workspace cannot redirect a restore elsewhere.
func restoreTreeEntry(ctx context.Context, m *Manager, loc *SnapshotLocation, root *os.Root, e indexEntry) error {
	name, err := containedWorkspacePath(e.path)
	if err != nil {
		return err
	}
	if existing, err := root.Lstat(name); err == nil && existing.Mode()&os.ModeSymlink != 0 {
		return storeError(ReasonSymlinkEscape,
			fmt.Errorf("refusing to restore %s: the workspace path is a symlink, so writing it could escape the workspace", e.path))
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		// A directory component that is a symlink surfaces here too.
		return storeError(ReasonSymlinkEscape, fmt.Errorf("inspect workspace path %s: %w", e.path, err))
	}

	if e.mode == gitModeSymlink {
		// A symlink's blob content IS its target text. Any existing entry is
		// removed first (a file cannot be replaced by Symlink), and the link
		// being created is never followed.
		out, err := m.gitOutput(ctx, loc.GitDir, "cat-file", "blob", e.hash)
		if err != nil {
			return fmt.Errorf("read symlink %s from snapshot %s: %w", e.path, loc.Hash, err)
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return storeError(ReasonUnreadableFile, fmt.Errorf("clear workspace path %s: %w", e.path, err))
		}
		if err := root.Symlink(string(out), name); err != nil {
			return storeError(ReasonUnreadableFile, fmt.Errorf("restore symlink %s: %w", e.path, err))
		}
		return nil
	}

	mode := os.FileMode(0o644)
	if e.mode == gitModeExecutable {
		mode = 0o755
	}
	if err := root.MkdirAll(parentDir(name), 0o755); err != nil {
		return storeError(ReasonUnreadableFile, fmt.Errorf("create workspace directory for %s: %w", e.path, err))
	}
	// The no-follow flag is the final guard: even if the containment walk above
	// were bypassed, opening a symlink at the destination fails instead of
	// writing through it. It is zero on platforms that have no such flag, where
	// os.Root and the Lstat above carry the guarantee on their own.
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|openNoFollowFlag, mode)
	if err != nil {
		return storeError(ReasonSymlinkEscape, fmt.Errorf("open workspace file %s: %w", e.path, err))
	}
	errOut, _, runErr := m.gitRunEnvWriter(ctx, loc.GitDir, f, "cat-file", "blob", e.hash)
	closeErr := f.Close()
	if runErr != nil {
		return fmt.Errorf("read %s from snapshot %s: %w%s", e.path, loc.Hash, runErr, gitOutputSuffix(errOut))
	}
	if closeErr != nil {
		return storeError(ReasonUnreadableFile, fmt.Errorf("write workspace file %s: %w", e.path, closeErr))
	}
	// The mode is re-applied explicitly: the process umask may have masked it
	// at creation, and a restored executable that lost its bit is not the
	// snapshot.
	if err := root.Chmod(name, mode); err != nil {
		return storeError(ReasonUnreadableFile, fmt.Errorf("set mode on %s: %w", e.path, err))
	}
	return nil
}

// containedWorkspacePath validates that a snapshot path names something inside
// the workspace, and returns it in the slash form os.Root expects.
//
// A Git tree path is always relative and never contains "." or ".." components,
// so anything that does is a malformed entry or a crafted one; either way it is
// refused rather than normalized into something that resolves outside the
// workspace.
func containedWorkspacePath(path string) (string, error) {
	clean := filepath.ToSlash(path)
	if clean == "" || strings.HasPrefix(clean, "/") {
		return "", storeErrorf(ReasonSymlinkEscape, "snapshot path %q is not workspace-relative", path)
	}
	if strings.HasSuffix(clean, "/") {
		return "", storeErrorf(ReasonSymlinkEscape, "snapshot path %q is a directory, not a file", path)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return "", storeErrorf(ReasonSymlinkEscape, "snapshot path %q is not a contained path", path)
		}
	}
	return clean, nil
}

// parentDir returns name's directory, or "." when it has none.
func parentDir(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return "."
}

// gitModeSymlink and gitModeExecutable are the two non-regular modes a snapshot
// can carry. A regular file's default mode is 100644.
const (
	gitModeSymlink    = 0o120000
	gitModeExecutable = 0o100755
)
