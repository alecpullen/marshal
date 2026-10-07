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

// This file answers two questions about the store: what is in it, and what
// interrupted work needs finishing.
//
// Discovery is strictly read-only, including for legacy stores. It never runs a
// command that could write, never prunes a ref, and never repacks — an old
// Marshal binary may still be writing a legacy store, so a "repair" here could
// race a writer this process cannot see.
//
// Reconciliation runs under the store lock and only ever finishes work whose
// intent is already durable in the manifest. The governing rule is asymmetric
// on purpose:
//
//	A durable published ref WINS over an interrupted manifest update.
//
// If the two disagree, the ref is the truth: manifest writes are cheap to
// repeat, but deleting objects a live ref needs destroys rollback history that
// nothing can reconstruct.

// StoreLayout classifies one workspace store under the snapshots root.
type StoreLayout string

const (
	// LayoutV2 is a versioned store, <root>/v2/<workspace-hash>.
	LayoutV2 StoreLayout = "v2"
	// LayoutLegacy is a pre-v2 bare repository, <root>/<workspace-hash>. It is
	// discovered and reported, and never mutated.
	LayoutLegacy StoreLayout = "legacy"
	// LayoutUnknown is a directory that is neither. It counts toward usage and
	// is never a candidate for deletion.
	LayoutUnknown StoreLayout = "unknown"
)

// MaintenanceWarning is a structured, reportable problem found during discovery
// or reconciliation. It is deliberately structured rather than a formatted
// string so the UI can deduplicate by workspace and reason, and so a caller can
// branch on the cause without matching text.
type MaintenanceWarning struct {
	// Reason is the failure class, for callers that switch on it.
	Reason StoreReason
	// Workspace is the workspace hash the warning concerns, when known.
	Workspace string
	// Path is the store path the warning concerns, when there is one.
	Path string
	// Message is the human-readable explanation.
	Message string
}

func (w MaintenanceWarning) String() string {
	var b strings.Builder
	if w.Workspace != "" {
		b.WriteString("workspace ")
		b.WriteString(w.Workspace)
		b.WriteString(": ")
	}
	b.WriteString(w.Message)
	return b.String()
}

// DiscoveredGeneration is one generation directory found on disk together with
// what the manifest says about it.
type DiscoveredGeneration struct {
	// ID is the generation identifier (the directory name).
	ID string
	// Path is the repository directory.
	Path string
	// Record is the manifest record, or nil for a directory the manifest does
	// not list. An unlisted directory is never deleted: nothing durable says
	// what it is, so removing it would be a guess.
	Record *GenerationRecord
	// Bytes is the conservative allocated size of the whole directory.
	Bytes int64
	// Refs is the number of snapshot refs, and Hashes the hashes they name.
	Refs   int
	Hashes []string
	// UnreachableBytes is the measured size of objects no snapshot ref can
	// reach. It is reported as abandoned storage; it is not a deletion
	// authorisation, because those objects may be shared with live snapshots.
	UnreachableBytes int64
	// ListingError is set when the refs could not be read at all, in which case
	// Refs and UnreachableBytes are meaningless and the generation must not be
	// acted on.
	ListingError error
}

// Usable reports whether this generation may serve a capture or a rollback.
func (g *DiscoveredGeneration) Usable() bool {
	if g == nil || g.ListingError != nil || g.Record == nil {
		return false
	}
	return g.Record.State.Usable()
}

// Protected reports whether automatic reclamation must skip this generation.
// An unlisted generation is treated as protected: it has no recorded origin, so
// there is no basis for believing its history is disposable.
func (g *DiscoveredGeneration) Protected() bool {
	if g == nil {
		return true
	}
	if g.Record == nil {
		return true
	}
	return g.Record.IsProtected()
}

// DiscoveredWorkspace is one workspace's store as found on disk.
type DiscoveredWorkspace struct {
	// Workspace is the workspace hash (the directory name), which is the only
	// identifier a legacy store has.
	Workspace string
	// Path is the store directory.
	Path string
	// Layout is v2, legacy, or unknown.
	Layout StoreLayout
	// Root is the canonical workspace root. For a v2 store it comes from the
	// manifest; for a legacy store it is unknown, because the legacy layout
	// records only the hash. It is "" when the checkout was deleted before the
	// root was ever recorded.
	Root string
	// Manifest is the parsed manifest for a v2 store, nil when absent or
	// unreadable.
	Manifest *Manifest
	// Corrupt is set when a manifest exists but could not be read or validated.
	Corrupt bool
	// Bytes is the conservative allocated size of the whole store directory.
	Bytes int64
	// Generations lists the generation directories found on disk, ordered by
	// identifier. Legacy stores have none.
	Generations []*DiscoveredGeneration
	// LegacyRefs counts the snapshot refs of a legacy store, and LegacyHashes
	// the hashes they name. They are read for reporting only.
	LegacyRefs   int
	LegacyHashes []string
	// StagingBytes is the allocated size of capture scratch directories, and
	// UnpublishedBytes the allocated size of every known abandoned artifact:
	// capture scratch and interrupted manifest scratch files.
	StagingBytes     int64
	UnpublishedBytes int64
	// Warnings are the problems found while inspecting this workspace.
	Warnings []MaintenanceWarning
}

// Quarantined reports whether this workspace's storage must be excluded from
// deletion and from admission.
//
// A corrupt manifest quarantines the store because nothing durable describes
// what its directories are: admitting a capture could overwrite state that a
// working installation still needs, and reclaiming could delete a generation
// that is the only copy of someone's history. The safe answer to "what is
// this?" is to refuse to guess.
func (w *DiscoveredWorkspace) Quarantined() bool {
	if w == nil {
		return true
	}
	if w.Corrupt {
		return true
	}
	// A legacy store is always quarantined from *automatic* deletion: old
	// binaries do not honour the store lock, so this process cannot prove no
	// writer is active. It remains available for rollback.
	return w.Layout != LayoutV2
}

// AllowsCapture reports whether new snapshots may be captured into this store.
func (w *DiscoveredWorkspace) AllowsCapture() bool {
	if w == nil {
		return false
	}
	return w.Layout == LayoutV2 && !w.Quarantined()
}

// ActiveGeneration returns the workspace's active generation, or nil.
func (w *DiscoveredWorkspace) ActiveGeneration() *DiscoveredGeneration {
	if w == nil || w.Manifest == nil {
		return nil
	}
	for _, g := range w.Generations {
		if g.ID == w.Manifest.ActiveID {
			return g
		}
	}
	return nil
}

// Generation returns the discovered generation with the given identifier.
func (w *DiscoveredWorkspace) Generation(id string) *DiscoveredGeneration {
	if w == nil {
		return nil
	}
	for _, g := range w.Generations {
		if g.ID == id {
			return g
		}
	}
	return nil
}

// StoreDiscovery is the measured, read-only state of the whole snapshots root.
type StoreDiscovery struct {
	// Root is the snapshots root.
	Root string
	// Workspaces lists every workspace store found, ordered by workspace hash.
	// It includes stores whose workspace path no longer exists on disk.
	Workspaces []*DiscoveredWorkspace
	// Unknown lists entries that are neither a workspace store nor the lock
	// file. They are reported and counted, never deleted.
	Unknown []string
	// Warnings collects every problem found, in discovery order.
	Warnings []MaintenanceWarning
	// UnpublishedBytes is the total measured size of known abandoned artifacts
	// across every workspace.
	UnpublishedBytes int64
}

// Workspace returns the discovered workspace with the given hash, or nil.
func (d *StoreDiscovery) Workspace(hash string) *DiscoveredWorkspace {
	if d == nil {
		return nil
	}
	for _, w := range d.Workspaces {
		if w.Workspace == hash {
			return w
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

// Discover performs a read-only scan of the snapshots root.
//
// It requires the root to exist; an absent root means there is nothing to
// manage yet, and is reported as an empty discovery rather than an error so a
// caller can run it unconditionally at startup.
func (m *Manager) Discover(ctx context.Context) (*StoreDiscovery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	out := &StoreDiscovery{Root: m.root}
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

	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, m.storeError(ReasonUnreadableFile, m.root, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		path := filepath.Join(m.root, name)
		entryInfo, err := lsEntryInfo(m.root, entry)
		if err != nil {
			return nil, m.storeError(ReasonUnreadableFile, path, err)
		}
		switch {
		case name == LockFileName:
			// The permanent lock file is not a workspace and is never removed.
			continue
		case name == v2DirName && entryInfo.IsDir():
			if err := m.discoverV2Root(ctx, path, out); err != nil {
				return nil, err
			}
		case entryInfo.IsDir() && validWorkspaceID(name):
			ws, err := m.discoverLegacyWorkspace(ctx, name, path, entryInfo)
			if err != nil {
				return nil, err
			}
			out.Workspaces = append(out.Workspaces, ws)
		case entryInfo.IsDir():
			// A directory that is neither the versioned root nor a workspace
			// hash. It is not ours; report it and leave it alone.
			out.Unknown = append(out.Unknown, path)
			out.Warnings = append(out.Warnings, MaintenanceWarning{
				Reason:  ReasonUnreadableFile,
				Path:    path,
				Message: "unrecognised directory in the snapshots root; it is counted but never automatically removed",
			})
		default:
			// A stray file. Counted toward usage, never attributed to a
			// workspace, never automatically removed.
			out.Unknown = append(out.Unknown, path)
		}
	}

	sort.SliceStable(out.Workspaces, func(i, j int) bool {
		return out.Workspaces[i].Workspace < out.Workspaces[j].Workspace
	})
	for _, ws := range out.Workspaces {
		var err error
		if out.UnpublishedBytes, err = addInt64(out.UnpublishedBytes, ws.UnpublishedBytes); err != nil {
			return nil, err
		}
		// Workspace warnings are collected into the store-level list as well as
		// kept on the workspace. A caller that only wants "what is wrong with my
		// store?" must not have to walk every workspace to find out.
		out.Warnings = append(out.Warnings, ws.Warnings...)
	}
	return out, nil
}

// discoverV2Root enumerates the versioned store root's workspaces.
func (m *Manager) discoverV2Root(ctx context.Context, v2Root string, out *StoreDiscovery) error {
	entries, err := readDirNoFollow(v2Root)
	if err != nil {
		return m.storeError(ReasonUnreadableFile, v2Root, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		path := filepath.Join(v2Root, name)
		info, err := lsEntryInfo(v2Root, entry)
		if err != nil {
			return m.storeError(ReasonUnreadableFile, path, err)
		}
		if !info.IsDir() || !validWorkspaceID(name) {
			out.Unknown = append(out.Unknown, path)
			out.Warnings = append(out.Warnings, MaintenanceWarning{
				Reason:  ReasonUnreadableFile,
				Path:    path,
				Message: "unrecognised entry in the versioned snapshots root; it is counted but never automatically removed",
			})
			continue
		}
		ws, err := m.discoverV2Workspace(ctx, name, path, info)
		if err != nil {
			return err
		}
		out.Workspaces = append(out.Workspaces, ws)
	}
	return nil
}

// discoverV2Workspace inspects one versioned workspace store.
//
// A workspace whose canonical root no longer exists is still reported: the
// manifest is the only record that the store belonged to that path, and
// forgetting it would leave the user with orphaned storage and no way to know
// where it came from.
func (m *Manager) discoverV2Workspace(ctx context.Context, workspace, path string, info os.FileInfo) (*DiscoveredWorkspace, error) {
	ws := &DiscoveredWorkspace{Workspace: workspace, Path: path, Layout: LayoutV2}
	measure, err := m.measureTreeAllocated(path, info)
	if err != nil {
		return nil, err
	}
	ws.Bytes = measure

	cat, err := m.Catalog(workspace)
	if err != nil {
		return nil, err
	}
	man, loadErr := cat.Load()
	switch {
	case loadErr != nil:
		// Quarantine, and say so in a structured warning. Discovery continues
		// for the rest of the store: one broken workspace must not hide every
		// other one.
		ws.Corrupt = true
		reason := ReasonOf(loadErr)
		if reason == "" {
			reason = ReasonUnreadableFile
		}
		ws.Warnings = append(ws.Warnings, MaintenanceWarning{
			Reason:    reason,
			Workspace: workspace,
			Path:      cat.ManifestPath(),
			Message: "snapshot manifest is unreadable or invalid; this workspace is quarantined from " +
				"capture and from deletion until the manifest is repaired: " + loadErr.Error(),
		})
	case man != nil:
		ws.Manifest = man
		ws.Root = man.Root
	}

	if err := m.discoverGenerations(ctx, ws, cat); err != nil {
		return nil, err
	}
	if err := m.discoverCatalogScratch(ws, cat); err != nil {
		return nil, err
	}
	return ws, nil
}

// discoverGenerations enumerates the generation directories of a versioned
// workspace and reconciles each against the manifest.
func (m *Manager) discoverGenerations(ctx context.Context, ws *DiscoveredWorkspace, cat *Catalog) error {
	dir := cat.GenerationsDir()
	entries, err := readDirNoFollow(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return m.storeError(ReasonUnreadableFile, dir, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		path := filepath.Join(dir, name)
		info, err := lsEntryInfo(dir, entry)
		if err != nil {
			return m.storeError(ReasonUnreadableFile, path, err)
		}
		if !info.IsDir() || !validGenerationID(name) {
			ws.Warnings = append(ws.Warnings, MaintenanceWarning{
				Reason:    ReasonUnreadableFile,
				Workspace: ws.Workspace,
				Path:      path,
				Message:   "unrecognised entry in the generations directory; it is reported but never automatically removed",
			})
			continue
		}
		g := &DiscoveredGeneration{ID: name, Path: path}
		if ws.Manifest != nil {
			g.Record = ws.Manifest.Generation(name)
		}
		if g.Bytes, err = m.measureTreeAllocated(path, info); err != nil {
			return err
		}
		if !repositoryLooksInitialised(path) {
			// A directory with no usable repository metadata: either a creation
			// that was interrupted before the metadata was written, or a
			// repository that lost its object store. Its refs cannot be listed,
			// so it is excluded rather than assumed to publish nothing.
			g.ListingError = fmt.Errorf("%s is not an initialised bare repository", path)
			ws.Warnings = append(ws.Warnings, MaintenanceWarning{
				Reason:    ReasonUnreadableFile,
				Workspace: ws.Workspace,
				Path:      path,
				Message:   "generation directory is not a usable bare repository; it is excluded from reclamation",
			})
			ws.Generations = append(ws.Generations, g)
			continue
		}
		_, hashes, err := cat.refsInDir(ctx, path)
		if err != nil {
			// A generation whose refs cannot be listed must not be acted on:
			// "could not list" is not "nothing is published".
			g.ListingError = err
			ws.Warnings = append(ws.Warnings, MaintenanceWarning{
				Reason:    ReasonUnreadableFile,
				Workspace: ws.Workspace,
				Path:      path,
				Message:   "generation snapshot refs could not be read; it is excluded from reclamation: " + err.Error(),
			})
		} else {
			g.Refs = len(hashes)
			g.Hashes = hashes
			if g.UnreachableBytes, err = m.UnreachableBytes(ctx, path); err != nil {
				// Packed or unreadable objects are a reportable condition, not
				// a reason to guess at a smaller number.
				ws.Warnings = append(ws.Warnings, MaintenanceWarning{
					Reason:    ReasonOf(err),
					Workspace: ws.Workspace,
					Path:      path,
					Message:   "generation object accounting is unavailable: " + err.Error(),
				})
			}
		}
		ws.Generations = append(ws.Generations, g)
	}
	sort.SliceStable(ws.Generations, func(i, j int) bool { return ws.Generations[i].ID < ws.Generations[j].ID })
	return nil
}

// discoverCatalogScratch measures the known abandoned artifacts of a versioned
// workspace: capture scratch directories and interrupted manifest scratch
// files.
//
// Both live in namespaces only this package writes, which is what makes them
// "known" — unlike a stray directory anywhere else in the store, their
// provenance needs no guesswork.
func (m *Manager) discoverCatalogScratch(ws *DiscoveredWorkspace, cat *Catalog) error {
	unit := m.limits.Normalize().AllocationUnitBytes

	stagingDir := cat.StagingDir()
	entries, err := readDirNoFollow(stagingDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return m.storeError(ReasonUnreadableFile, stagingDir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(stagingDir, name)
		info, err := lsEntryInfo(stagingDir, entry)
		if err != nil {
			return m.storeError(ReasonUnreadableFile, path, err)
		}
		if !validStagingID(name) {
			ws.Warnings = append(ws.Warnings, MaintenanceWarning{
				Reason:    ReasonUnreadableFile,
				Workspace: ws.Workspace,
				Path:      path,
				Message:   "unrecognised entry in the capture staging directory; it is reported but never automatically removed",
			})
			continue
		}
		bytes, err := m.measureTreeAllocated(path, info)
		if err != nil {
			return err
		}
		if ws.StagingBytes, err = addInt64(ws.StagingBytes, bytes); err != nil {
			return err
		}
	}

	// Interrupted manifest replacements: the scratch file is created before the
	// rename, so one that outlived its writer is a replace that never finished.
	rootEntries, err := readDirNoFollow(cat.Root())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return m.storeError(ReasonUnreadableFile, cat.Root(), err)
	}
	var scratch int64
	for _, entry := range rootEntries {
		name := entry.Name()
		if !strings.HasPrefix(name, manifestTempPrefix) {
			continue
		}
		path := filepath.Join(cat.Root(), name)
		info, err := lsEntryInfo(cat.Root(), entry)
		if err != nil {
			return m.storeError(ReasonUnreadableFile, path, err)
		}
		allocated, err := m.allocatedBytes(path, info, unit)
		if err != nil {
			return err
		}
		if scratch, err = addInt64(scratch, allocated); err != nil {
			return err
		}
	}

	unpublished, err := addInt64(ws.StagingBytes, scratch)
	if err != nil {
		return err
	}
	ws.UnpublishedBytes = unpublished
	return nil
}

// discoverLegacyWorkspace inspects a pre-v2 bare repository.
//
// It is read-only in the strict sense: the only command run against it is a ref
// listing. No ref is pruned, nothing is removed, and no repack is ever started.
// Its snapshot refs are counted so the user can see what is still recoverable
// from it, and so a later migration task can verify nothing was lost.
func (m *Manager) discoverLegacyWorkspace(ctx context.Context, workspace, path string, info os.FileInfo) (*DiscoveredWorkspace, error) {
	ws := &DiscoveredWorkspace{Workspace: workspace, Path: path, Layout: LayoutLegacy}
	bytes, err := m.measureTreeAllocated(path, info)
	if err != nil {
		return nil, err
	}
	ws.Bytes = bytes

	if !gitDirExists(path) {
		ws.Warnings = append(ws.Warnings, MaintenanceWarning{
			Reason:    ReasonLegacyRecoveryRequired,
			Workspace: workspace,
			Path:      path,
			Message:   "legacy store directory is not a Git repository; no snapshot refs can be read from it",
		})
		return ws, nil
	}
	_, hashes, err := m.snapshotRefsInDir(ctx, path)
	if err != nil {
		ws.Warnings = append(ws.Warnings, MaintenanceWarning{
			Reason:    ReasonLegacyRecoveryRequired,
			Workspace: workspace,
			Path:      path,
			Message:   "legacy store snapshot refs could not be read; it is left untouched: " + err.Error(),
		})
		return ws, nil
	}
	ws.LegacyRefs = len(hashes)
	ws.LegacyHashes = hashes
	return ws, nil
}

// measureTreeAllocated measures the conservative allocated size of one path.
func (m *Manager) measureTreeAllocated(path string, info os.FileInfo) (int64, error) {
	unit := m.limits.Normalize().AllocationUnitBytes
	if !info.IsDir() {
		return m.allocatedBytes(path, info, unit)
	}
	usage, err := m.measureTree(path, info, StoreKindWorkspace, "", unit)
	if err != nil {
		return 0, err
	}
	return usage.Allocated, nil
}

// ---------------------------------------------------------------------------
// Reconciliation
// ---------------------------------------------------------------------------

// WorkspaceReconcile is what reconciliation did to one workspace.
type WorkspaceReconcile struct {
	// Workspace is the workspace hash.
	Workspace string
	// Quarantined is set when the workspace was skipped because its storage
	// could not be interpreted.
	Quarantined bool
	// StagingRemoved lists the capture scratch directories that were removed.
	StagingRemoved []string
	// ManifestScratchRemoved lists the interrupted manifest replacements that
	// were removed.
	ManifestScratchRemoved []string
	// GenerationsRemoved lists the generations whose directories were removed,
	// and GenerationsForgotten the records dropped for them.
	GenerationsRemoved   []string
	GenerationsForgotten []string
	// GenerationsSealed lists the generations sealed because they hold
	// published snapshots but were interrupted before activation completed.
	GenerationsSealed []string
	// CapturesCleared counts in-flight capture records resolved.
	CapturesCleared int
	// AbandonedBytes is the measured size of everything this workspace held
	// that no published snapshot needs. It is reported, not necessarily freed:
	// unreachable objects inside a retained generation stay until that whole
	// generation is reclaimed.
	AbandonedBytes int64
	// Warnings are the problems found while reconciling this workspace.
	Warnings []MaintenanceWarning
}

// ReconcileReport is the outcome of one reconciliation pass.
type ReconcileReport struct {
	// Workspaces lists the per-workspace outcomes, ordered by hash.
	Workspaces []*WorkspaceReconcile
	// Warnings collects every warning from the pass.
	Warnings []MaintenanceWarning
	// AbandonedBytes is the total measured abandoned storage.
	AbandonedBytes int64
	// LegacyUntouched lists the legacy stores that were inspected and left
	// exactly as they were.
	LegacyUntouched []string
}

// Workspace returns the outcome for one workspace, or nil.
func (r *ReconcileReport) Workspace(hash string) *WorkspaceReconcile {
	if r == nil {
		return nil
	}
	for _, w := range r.Workspaces {
		if w.Workspace == hash {
			return w
		}
	}
	return nil
}

// Reconcile finishes every interrupted storage operation visible in the store.
//
// It requires the store lock: it deletes directories and rewrites manifests,
// so running it without ownership would race a capture in another process. It
// is safe to run repeatedly — each step is idempotent and re-derives what to do
// from durable state rather than from what it did last time.
//
// Legacy stores and quarantined workspaces are never touched.
func (m *Manager) Reconcile(ctx context.Context) (*ReconcileReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !m.Owned() {
		return nil, storeErrorf(ReasonNotOwned, "reconciliation requires exclusive ownership of the snapshot store")
	}
	discovery, err := m.Discover(ctx)
	if err != nil {
		return nil, err
	}

	report := &ReconcileReport{Warnings: append([]MaintenanceWarning(nil), discovery.Warnings...)}
	for _, ws := range discovery.Workspaces {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ws.Layout != LayoutV2 {
			// Legacy stores are inspected and left byte-for-byte alone. Old
			// Marshal binaries do not honour the store lock, so this process
			// cannot prove no writer is active.
			report.LegacyUntouched = append(report.LegacyUntouched, ws.Path)
			report.Warnings = append(report.Warnings, ws.Warnings...)
			continue
		}
		out, err := m.reconcileWorkspace(ctx, ws)
		if err != nil {
			return nil, err
		}
		if report.AbandonedBytes, err = addInt64(report.AbandonedBytes, out.AbandonedBytes); err != nil {
			return nil, err
		}
		report.Workspaces = append(report.Workspaces, out)
		report.Warnings = append(report.Warnings, out.Warnings...)
	}
	return report, nil
}

// reconcileWorkspace reconciles one versioned workspace store.
func (m *Manager) reconcileWorkspace(ctx context.Context, ws *DiscoveredWorkspace) (*WorkspaceReconcile, error) {
	out := &WorkspaceReconcile{Workspace: ws.Workspace}
	if ws.Corrupt {
		// Nothing durable describes this store, so there is no safe action
		// beyond reporting it. Every step below would need a state this
		// workspace does not have.
		out.Quarantined = true
		out.Warnings = append(out.Warnings, ws.Warnings...)
		if ws.UnpublishedBytes > 0 {
			out.AbandonedBytes = ws.UnpublishedBytes
		}
		return out, nil
	}

	cat, err := m.Catalog(ws.Workspace)
	if err != nil {
		return nil, err
	}

	man, err := cat.Load()
	if err != nil {
		// Readable at discovery, unreadable now: treat it exactly as a
		// quarantine rather than proceeding on a half-read state.
		out.Quarantined = true
		out.Warnings = append(out.Warnings, MaintenanceWarning{
			Reason:    ReasonOf(err),
			Workspace: ws.Workspace,
			Path:      cat.ManifestPath(),
			Message:   "manifest became unreadable during reconciliation; the workspace is quarantined: " + err.Error(),
		})
		return out, nil
	}
	if man == nil {
		// No manifest but a store directory exists. Records cannot be trusted
		// on a guess, so only known scratch is cleaned and everything else is
		// reported.
		out.Warnings = append(out.Warnings, MaintenanceWarning{
			Reason:    ReasonUnreadableFile,
			Workspace: ws.Workspace,
			Path:      cat.Root(),
			Message:   "versioned store has no manifest; its directories are reported but not removed",
		})
		if err := m.cleanKnownScratch(ctx, cat, man, ws, out); err != nil {
			return nil, err
		}
		return out, nil
	}

	// 1. Resolve an in-flight capture. This must happen before generation
	//    cleanup, because the capture may be exactly what makes a generation's
	//    objects live.
	if err := m.reconcileCapture(ctx, cat, man, ws, out); err != nil {
		return nil, err
	}

	// 2. Resolve each generation's lifecycle state.
	if err := m.reconcileGenerations(ctx, cat, ws, out); err != nil {
		return nil, err
	}

	// 3. Remove known scratch that survived its writer. The manifest is
	//    re-read because steps 1 and 2 both rewrite it, and step 3 must not act
	//    on a capture record that is already resolved.
	man, err = cat.Load()
	if err != nil {
		return nil, err
	}
	if err := m.cleanKnownScratch(ctx, cat, man, ws, out); err != nil {
		return nil, err
	}
	return out, nil
}

// reconcileCapture resolves the workspace's in-flight capture record, if any.
//
// The two possible interruptions have opposite correct answers, and the
// publication ref is what decides between them:
//
//   - The ref EXISTS. The capture published and then the manifest update was
//     interrupted. The ref wins: the snapshots stay, the staging scratch goes,
//     and the generation is made usable so they remain rollback targets.
//   - The ref does NOT exist. The capture never published. The staging scratch
//     goes and the record is cleared. Objects that were staged into the
//     generation are counted as abandoned but NOT removed here: Git objects are
//     content-addressed, so an unpublished object may be byte-identical to one
//     a published ref still needs, and removing it would break that snapshot.
//     The generation is sealed, so the whole thing (objects included) can be
//     reclaimed later as one unit.
func (m *Manager) reconcileCapture(ctx context.Context, cat *Catalog, man *Manifest, ws *DiscoveredWorkspace, out *WorkspaceReconcile) error {
	cs := man.Capture
	if cs == nil {
		return nil
	}
	if err := cs.Validate(man); err != nil {
		// A capture record that cannot be validated names paths or states that
		// must not be acted on. Keep it so the problem stays visible, and
		// quarantine the workspace: capturing now could publish into a
		// generation this record was about to change.
		out.Quarantined = true
		out.Warnings = append(out.Warnings, MaintenanceWarning{
			Reason:    ReasonInterruptedCapture,
			Workspace: ws.Workspace,
			Path:      cat.ManifestPath(),
			Message:   "in-flight capture record is invalid; the workspace is quarantined: " + err.Error(),
		})
		return nil
	}

	if cs.Hash == "" {
		// No publication intent was ever recorded, so this capture never
		// reached the point of writing a ref. It may still have written
		// objects, so those are measured before the record is cleared, but
		// there is no ref to preserve.
		staged, err := m.generationUnreachableBytes(ctx, cat, cs.GenerationID, ws)
		if err != nil {
			return err
		}
		if out.AbandonedBytes, err = addInt64(out.AbandonedBytes, staged); err != nil {
			return err
		}
		if err := cat.ClearCapture(); err != nil {
			return err
		}
		out.CapturesCleared++
		m.noteDirtyGeneration(ctx, cat, cs.GenerationID, ws, out, staged)
		return nil
	}

	published, err := cat.RefExists(ctx, cs.GenerationID, cs.Hash)
	if err != nil {
		return err
	}
	if published {
		// A durable published ref WINS over the interrupted manifest update.
		if err := cat.ClearCapture(); err != nil {
			return err
		}
		out.CapturesCleared++
		if rec := man.Generation(cs.GenerationID); rec != nil && rec.State == GenerationCreating && !rec.IsProtected() {
			// The generation holds a published snapshot but was interrupted
			// before activation was recorded. Sealing keeps the snapshot
			// reachable, keeps the generation out of the capture path (we
			// cannot know whether activation was intended), and makes it a
			// normal whole-generation reclamation candidate later.
			if err := cat.SealGeneration(cs.GenerationID); err != nil {
				return err
			}
			out.GenerationsSealed = append(out.GenerationsSealed, cs.GenerationID)
			out.Warnings = append(out.Warnings, MaintenanceWarning{
				Reason:    ReasonInterruptedCapture,
				Workspace: ws.Workspace,
				Path:      cs.Ref,
				Message: "capture published a snapshot into a generation whose creation was interrupted; " +
					"the generation was sealed and its snapshot preserved",
			})
		}
		return nil
	}

	// Not published: count what is staged or stranded, then clear the record.
	staged, err := m.generationUnreachableBytes(ctx, cat, cs.GenerationID, ws)
	if err != nil {
		return err
	}
	if out.AbandonedBytes, err = addInt64(out.AbandonedBytes, staged); err != nil {
		return err
	}
	if err := cat.ClearCapture(); err != nil {
		return err
	}
	out.CapturesCleared++
	out.Warnings = append(out.Warnings, MaintenanceWarning{
		Reason:    ReasonInterruptedCapture,
		Workspace: ws.Workspace,
		Path:      cs.StagingID,
		Message: "capture was interrupted before publication; its staging scratch was removed and any " +
			"unpublished objects were counted as abandoned rather than deleted",
	})

	m.noteDirtyGeneration(ctx, cat, cs.GenerationID, ws, out, staged)
	return nil
}

// noteDirtyGeneration seals a generation left holding objects that no snapshot
// ref needs.
//
// Sealing is the safe response to a dirty generation: the objects stay where
// they are (Git objects are content-addressed and an "unreachable" one may be
// shared with a published snapshot, so deleting it individually can break that
// snapshot), the next capture gets a fresh generation, and the dirty one becomes
// reclaimable as a whole unit.
//
// A generation with no unpublished bytes is left exactly as it is: nothing about
// it is dirty, and sealing a healthy active generation would rotate for no
// reason.
func (m *Manager) noteDirtyGeneration(ctx context.Context, cat *Catalog, id string, ws *DiscoveredWorkspace, out *WorkspaceReconcile, abandoned int64) {
	if abandoned <= 0 {
		return
	}
	man, err := cat.Load()
	if err != nil || man == nil {
		return
	}
	rec := man.Generation(id)
	if rec == nil || rec.IsProtected() {
		return
	}
	if rec.State != GenerationActive && rec.State != GenerationCreating {
		return
	}
	if err := cat.SealGeneration(id); err != nil {
		return
	}
	out.GenerationsSealed = append(out.GenerationsSealed, id)
	out.Warnings = append(out.Warnings, MaintenanceWarning{
		Reason:    ReasonInterruptedCapture,
		Workspace: ws.Workspace,
		Path:      cat.Root(),
		Message:   "generation holding an interrupted capture was sealed so its storage is reclaimable as a whole",
	})
}

// generationUnreachableBytes measures the unpublished objects of one
// generation, reporting 0 when the generation cannot be measured.
func (m *Manager) generationUnreachableBytes(ctx context.Context, cat *Catalog, id string, ws *DiscoveredWorkspace) (int64, error) {
	dir, err := cat.GenerationDir(id)
	if err != nil {
		return 0, err
	}
	if !repositoryLooksInitialised(dir) {
		return 0, nil
	}
	bytes, err := m.UnreachableBytes(ctx, dir)
	if err != nil {
		// Unmeasurable is reported, never counted as zero silently: an
		// under-reported abandoned total is how a budget later admits a write
		// it cannot afford.
		out := MaintenanceWarning{
			Reason:    ReasonOf(err),
			Workspace: ws.Workspace,
			Path:      dir,
			Message:   "unpublished object accounting is unavailable for this generation: " + err.Error(),
		}
		ws.Warnings = append(ws.Warnings, out)
		return 0, nil
	}
	return bytes, nil
}

// reconcileGenerations resolves every generation's lifecycle state and the
// disk/manifest discrepancies around it.
//
// Each case is decided from durable state only, so a restart at any point
// recomputes the same answer:
//
//   - `deleting`            → finish the removal. Never usable again.
//   - `creating`, no dir    → never had a repository; drop the record.
//   - `creating`, no refs   → interrupted creation; remove it whole.
//   - `creating`, has refs  → interrupted activation; seal and keep.
//   - any state, dir missing→ report; the manifest is left describing what is
//     lost rather than being quietly edited to hide it.
//   - directory not listed  → report; never removed, because nothing durable
//     says what it is.
func (m *Manager) reconcileGenerations(ctx context.Context, cat *Catalog, ws *DiscoveredWorkspace, out *WorkspaceReconcile) error {
	man, err := cat.Load()
	if err != nil {
		return err
	}
	if man == nil {
		return nil
	}

	// A pass over the manifest's own records first: a generation that is
	// listed but absent from disk must not stay as the active capture target.
	for i := range man.Generations {
		rec := &man.Generations[i]
		dir, err := cat.GenerationDir(rec.ID)
		if err != nil {
			return err
		}
		if _, statErr := os.Lstat(dir); statErr == nil {
			continue
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return cat.err(ReasonUnreadableFile, statErr)
		}
		switch rec.State {
		case GenerationCreating:
			// The record was written and the repository never appeared. There
			// is nothing on disk and nothing published, so the record is the
			// only artifact and dropping it is exactly undoing the interrupted
			// creation.
			if rec.IsProtected() {
				out.Warnings = append(out.Warnings, MaintenanceWarning{
					Reason:    ReasonLegacyRecoveryRequired,
					Workspace: ws.Workspace,
					Path:      dir,
					Message:   "protected generation has no directory; refusing to remove its record automatically",
				})
				continue
			}
			if err := cat.ForgetGeneration(rec.ID); err != nil {
				return err
			}
			out.GenerationsForgotten = append(out.GenerationsForgotten, rec.ID)
		case GenerationDeleting:
			// The removal finished before the record could be dropped.
			if err := cat.ForgetGeneration(rec.ID); err != nil {
				return err
			}
			out.GenerationsForgotten = append(out.GenerationsForgotten, rec.ID)
		default:
			out.Warnings = append(out.Warnings, MaintenanceWarning{
				Reason:    ReasonUnreadableFile,
				Workspace: ws.Workspace,
				Path:      dir,
				Message: "generation directory is missing but its manifest record is not; snapshots in it " +
					"are lost and the record is left in place so the loss stays visible",
			})
		}
	}

	// Re-read: the pass above may have rewritten the manifest.
	man, err = cat.Load()
	if err != nil {
		return err
	}
	if man == nil {
		return nil
	}
	for i := range man.Generations {
		rec := &man.Generations[i]
		if err := ctx.Err(); err != nil {
			return err
		}
		switch rec.State {
		case GenerationDeleting:
			// Interrupted deletion. The intent is durable, so the only correct
			// action is to finish it. A generation in this state is never made
			// usable, and is never counted as a capture target.
			if err := m.finishDeletion(ctx, cat, rec, out); err != nil {
				return err
			}
		case GenerationCreating:
			hasRefs, err := catHasSnapshotRefs(ctx, cat, rec.ID)
			if err != nil {
				return err
			}
			if hasRefs {
				// Objects are published, so they win. Seal rather than delete.
				if err := cat.SealGeneration(rec.ID); err != nil {
					return err
				}
				out.GenerationsSealed = append(out.GenerationsSealed, rec.ID)
				bytes, err := m.generationUnreachableBytes(ctx, cat, rec.ID, ws)
				if err != nil {
					return err
				}
				if out.AbandonedBytes, err = addInt64(out.AbandonedBytes, bytes); err != nil {
					return err
				}
				continue
			}
			// Interrupted creation: a record and possibly a repository, but no
			// published snapshot ever. Removing it whole loses nothing.
			if rec.IsProtected() {
				out.Warnings = append(out.Warnings, MaintenanceWarning{
					Reason:    ReasonLegacyRecoveryRequired,
					Workspace: ws.Workspace,
					Path:      cat.Root(),
					Message:   "protected generation has no published snapshots; refusing to remove it automatically",
				})
				continue
			}
			if err := m.finishDeletion(ctx, cat, rec, out); err != nil {
				return err
			}
		}
	}

	// Finally, directories the manifest does not list. These are reported and
	// left alone: with no record there is no durable statement about what they
	// are or whether their contents are anyone's only copy.
	for _, g := range ws.Generations {
		if g.Record != nil || g.ListingError != nil {
			continue
		}
		out.Warnings = append(out.Warnings, MaintenanceWarning{
			Reason:    ReasonUnreadableFile,
			Workspace: ws.Workspace,
			Path:      g.Path,
			Message:   "generation directory is not listed in the manifest; it is reported but never automatically removed",
		})
	}
	return nil
}

// catHasSnapshotRefs reports whether a generation has any snapshot ref at all.
func catHasSnapshotRefs(ctx context.Context, cat *Catalog, id string) (bool, error) {
	dir, err := cat.GenerationDir(id)
	if err != nil {
		return false, err
	}
	if !repositoryLooksInitialised(dir) {
		return false, nil
	}
	_, hashes, err := cat.refsInDir(ctx, dir)
	if err != nil {
		// Refuse to proceed on an unreadable listing: assuming "no refs" here
		// would delete objects a live ref might need.
		return false, err
	}
	return len(hashes) > 0, nil
}

// finishDeletion marks, removes, and forgets one generation. It is the single
// path that removes a generation directory, so every removal goes through the
// same durable intent-first sequence.
func (m *Manager) finishDeletion(ctx context.Context, cat *Catalog, rec *GenerationRecord, out *WorkspaceReconcile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if rec.IsProtected() {
		return cat.errf(ReasonLegacyRecoveryRequired,
			"generation %s is protected and must not be removed", rec.ID)
	}
	if rec.State != GenerationDeleting {
		// Persist the intent before the removal. A crash between these two
		// steps must leave a `deleting` record that the next pass finishes,
		// rather than an unexplained missing directory.
		if _, err := cat.MarkDeleting(rec.ID); err != nil {
			return err
		}
	}
	if err := cat.RemoveGenerationDirectory(rec.ID); err != nil {
		return err
	}
	out.GenerationsRemoved = append(out.GenerationsRemoved, rec.ID)
	if err := cat.ForgetGeneration(rec.ID); err != nil {
		return err
	}
	out.GenerationsForgotten = append(out.GenerationsForgotten, rec.ID)
	return nil
}

// cleanKnownScratch removes the scratch this package is certain it created:
// staging directories no in-flight capture owns, and manifest replacements that
// never completed.
//
// Both live in namespaces only this package writes, so unlike an arbitrary
// stray directory their provenance needs no guesswork. They are also never
// published by definition, so removing them cannot discard a snapshot.
func (m *Manager) cleanKnownScratch(ctx context.Context, cat *Catalog, man *Manifest, ws *DiscoveredWorkspace, out *WorkspaceReconcile) error {
	if man != nil && man.Capture != nil {
		if err := man.Capture.Validate(man); err != nil {
			// A capture whose staging identifier cannot be validated is not
			// acted on at all; reconcileCapture already quarantined it.
			return nil
		}
	}

	stagingDir := cat.StagingDir()
	entries, err := readDirNoFollow(stagingDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return m.storeError(ReasonUnreadableFile, stagingDir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !validStagingID(name) {
			continue
		}
		if man != nil && man.Capture != nil && man.Capture.StagingID == name {
			// The capture record still owns this directory: an interruption is
			// still in flight for it and reconcileCapture decides its fate.
			continue
		}
		path := filepath.Join(stagingDir, name)
		info, err := lsEntryInfo(stagingDir, entry)
		if err != nil {
			return m.storeError(ReasonUnreadableFile, path, err)
		}
		bytes, err := m.measureTreeAllocated(path, info)
		if err != nil {
			return err
		}
		if out.AbandonedBytes, err = addInt64(out.AbandonedBytes, bytes); err != nil {
			return err
		}
		if err := removeDirectoryNoFollow(path); err != nil {
			return cat.err(ReasonUnreadableFile, err)
		}
		out.StagingRemoved = append(out.StagingRemoved, name)
	}

	rootDir := cat.Root()
	rootEntries, err := readDirNoFollow(rootDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return m.storeError(ReasonUnreadableFile, rootDir, err)
	}
	unit := m.limits.Normalize().AllocationUnitBytes
	for _, entry := range rootEntries {
		name := entry.Name()
		if !strings.HasPrefix(name, manifestTempPrefix) {
			continue
		}
		path := filepath.Join(rootDir, name)
		info, err := lsEntryInfo(rootDir, entry)
		if err != nil {
			return m.storeError(ReasonUnreadableFile, path, err)
		}
		if info.IsDir() {
			// os.CreateTemp never makes a directory, so this is not a scratch
			// file of ours. Report it instead of removing it.
			out.Warnings = append(out.Warnings, MaintenanceWarning{
				Reason:    ReasonUnreadableFile,
				Workspace: ws.Workspace,
				Path:      path,
				Message:   "path uses the manifest scratch prefix but is a directory; it is reported and not removed",
			})
			continue
		}
		allocated, err := m.allocatedBytes(path, info, unit)
		if err != nil {
			return err
		}
		if out.AbandonedBytes, err = addInt64(out.AbandonedBytes, allocated); err != nil {
			return err
		}
		if err := removeDirectoryNoFollow(path); err != nil {
			return cat.err(ReasonUnreadableFile, err)
		}
		out.ManifestScratchRemoved = append(out.ManifestScratchRemoved, name)
	}
	return nil
}

// ReconcileWorkspace reconciles a single workspace store under the store lock.
// It is the narrow variant of Reconcile for callers that already know which
// workspace they care about.
func (m *Manager) ReconcileWorkspace(ctx context.Context, workspace string) (*WorkspaceReconcile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !m.Owned() {
		return nil, storeErrorf(ReasonNotOwned, "reconciliation requires exclusive ownership of the snapshot store")
	}
	discovery, err := m.Discover(ctx)
	if err != nil {
		return nil, err
	}
	ws := discovery.Workspace(workspace)
	if ws == nil {
		return &WorkspaceReconcile{Workspace: workspace}, nil
	}
	if ws.Layout != LayoutV2 {
		return &WorkspaceReconcile{
			Workspace:   workspace,
			Quarantined: true,
			Warnings: append(ws.Warnings, MaintenanceWarning{
				Reason:    ReasonLegacyRecoveryRequired,
				Workspace: workspace,
				Path:      ws.Path,
				Message:   "legacy stores are never reconciled automatically; an offline recovery workflow is required",
			}),
		}, nil
	}
	return m.reconcileWorkspace(ctx, ws)
}
