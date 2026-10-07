package snapshot

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StoreKind classifies one entry discovered under the snapshots root.
type StoreKind string

const (
	// StoreKindLock is the permanent advisory lock file.
	StoreKindLock StoreKind = "store-lock"
	// StoreKindUnknown is any entry that is neither a recognized lock file nor
	// a directory. Unknown entries are counted toward global usage, but are
	// never automatically deleted by this task.
	StoreKindUnknown StoreKind = "unknown"
	// StoreKindWorkspace is a directory holding one workspace's store.
	StoreKindWorkspace StoreKind = "workspace"
)

// Usage is one entry's measured storage. All byte counts are conservative:
// allocated is the larger of the filesystem-allocated size and the rounded-up
// logical length, and directory metadata is counted too.
type Usage struct {
	// Path is the absolute path of the measured entry.
	Path string
	// Kind classifies the entry.
	Kind StoreKind
	// Workspace is the workspace hash for a workspace store, else "".
	Workspace string
	// Logical is the sum of logical file lengths (and directory entry sizes).
	Logical int64
	// Allocated is the conservative allocated total.
	Allocated int64
	// DirBytes is the part of Allocated attributed to directory metadata.
	// It is tracked separately because reclamation compares against file
	// bytes only: deleting generation contents does not reclaim directory
	// allocation.
	DirBytes int64
	// Files and Dirs count the entries that contributed.
	Files int
	Dirs  int
}

// StoreUsage is the measured state of the whole snapshots root.
type StoreUsage struct {
	// Root is the snapshots root.
	Root string
	// Workspaces maps a workspace hash to its measured usage.
	Workspaces map[string]*Usage
	// Unknown counts entries that are not recognized workspace stores. They
	// count toward Global but are not candidates for automatic deletion here.
	Unknown []*Usage
	// Lock is the measured cost of the lock file itself.
	Lock *Usage
	// RootMetadata is the measured cost of the root directory entry.
	RootMetadata int64
	// GlobalAllocated and GlobalLogical are the conservative totals for the
	// ENTIRE root: every workspace, unknown entry, the lock, and root metadata.
	GlobalAllocated int64
	GlobalLogical   int64
	// FilesAllocated and FilesLogical are the totals for regular files only,
	// excluding directory metadata. Directory metadata is not reclaimable by
	// deleting a generation, so reclamation compares against these.
	FilesAllocated int64
	FilesLogical   int64

	// V2Allocated is the part of GlobalAllocated that belongs to versioned
	// stores, and LegacyAllocated the part that belongs to pre-v2 stores. The
	// split matters because legacy storage is never automatically reclaimed, so
	// a budget that only counted the v2 side would believe there is room where
	// there is none.
	V2Allocated     int64
	LegacyAllocated int64
	// Generations maps a workspace hash to that workspace's per-generation
	// measurement. A workspace with no versioned store has no entry.
	Generations map[string]*WorkspaceGenerations
}

// WorkspaceGenerations is the per-generation measurement of one versioned
// workspace store.
//
// Allocated and its subtotals are conservative: each generation's directory is
// measured the same way the whole-root pass measures it, so the two agree on
// rounding and a per-generation figure can never under-report what the root
// total charges for the same bytes.
type WorkspaceGenerations struct {
	// Workspace is the workspace hash.
	Workspace string
	// Path is the workspace's versioned store directory.
	Path string
	// Allocated is the measured allocated size of the whole versioned store
	// directory, including the manifest, the generation directories, and
	// scratch.
	Allocated int64
	// ManifestAllocated is the measured cost of the manifest and any
	// interrupted manifest replacement beside it.
	ManifestAllocated int64
	// ByID maps a generation identifier to its measurement.
	ByID map[string]*GenerationUsage
	// Order lists the generation identifiers in creation order, which is the
	// order oldest-first reclamation consumes them.
	Order []string
	// Unlisted lists generation directories the manifest does not describe.
	// They are measured and reported, and are never candidates for deletion:
	// nothing durable says what they are.
	Unlisted []*GenerationUsage
	// StagingAllocated is the measured cost of capture scratch directories, and
	// StagingCount how many there are.
	StagingAllocated int64
	StagingCount     int
	// AbandonedAllocated is the measured cost of known abandoned artifacts:
	// capture scratch and interrupted manifest replacements. It is a measured
	// figure, not an estimate, and it is reported rather than reclaimed —
	// unreachable Git objects inside a live generation are deliberately not
	// counted here, because they may be shared with a snapshot that is still
	// needed.
	AbandonedAllocated int64
	// Corrupt is set when the manifest could not be read, in which case the
	// generation figures are incomplete and the workspace must not be reclaimed.
	Corrupt bool
}

// GenerationUsage is one generation's measured storage.
type GenerationUsage struct {
	// ID is the generation identifier, or the directory name when the manifest
	// does not list it.
	ID string
	// Path is the generation directory.
	Path string
	// Allocated is the measured allocated size of the directory.
	Allocated int64
	// Protected reports whether automatic reclamation must skip it. An unlisted
	// generation is protected: with no record, there is no basis for believing
	// its history is disposable.
	Protected bool
	// Listed reports whether the manifest describes this generation.
	Listed bool
	// Refs is the number of snapshot refs found, or -1 when the refs could not
	// be read at all. A generation whose refs are unknown must not be reclaimed.
	Refs int
}

// MeasureWorkspaceGenerations measures one versioned workspace store's
// generations, manifest, and scratch, using the same conservative accounting as
// the whole-root pass.
//
// It is additive to Usage: it reports, it never deletes. Unknown entries are
// counted and never removed.
//
// It takes the caller's context because listing a generation's refs spawns a
// read-only git process, and a cancelled turn must not be held open by
// accounting.
func (m *Manager) MeasureWorkspaceGenerations(ctx context.Context, workspace string) (*WorkspaceGenerations, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	cat, err := m.Catalog(workspace)
	if err != nil {
		return nil, err
	}
	unit := m.limits.Normalize().AllocationUnitBytes

	out := &WorkspaceGenerations{
		Workspace: workspace,
		Path:      cat.Root(),
		ByID:      make(map[string]*GenerationUsage),
	}

	if info, err := os.Lstat(cat.Root()); err == nil {
		allocated, err := m.measureTreeAllocated(cat.Root(), info)
		if err != nil {
			return nil, err
		}
		out.Allocated = allocated
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, m.storeError(ReasonUnreadableFile, cat.Root(), err)
	}

	man, err := cat.Load()
	if err != nil {
		out.Corrupt = true
	} else if man != nil {
		for i := range man.Generations {
			rec := &man.Generations[i]
			out.Order = append(out.Order, rec.ID)
		}
		sort.Strings(out.Order)
	}

	// The manifest and any interrupted replacement beside it.
	rootEntries, err := readDirNoFollow(cat.Root())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, m.storeError(ReasonUnreadableFile, cat.Root(), err)
	}
	for _, entry := range rootEntries {
		name := entry.Name()
		isManifest := name == manifestFileName
		isScratch := strings.HasPrefix(name, manifestTempPrefix)
		if !isManifest && !isScratch {
			continue
		}
		path := filepath.Join(cat.Root(), name)
		info, err := lsEntryInfo(cat.Root(), entry)
		if err != nil {
			return nil, m.storeError(ReasonUnreadableFile, path, err)
		}
		allocated, err := m.allocatedBytes(path, info, unit)
		if err != nil {
			return nil, err
		}
		if out.ManifestAllocated, err = addInt64(out.ManifestAllocated, allocated); err != nil {
			return nil, err
		}
		if isScratch {
			// An interrupted replacement is a known abandoned artifact.
			if out.AbandonedAllocated, err = addInt64(out.AbandonedAllocated, allocated); err != nil {
				return nil, err
			}
		}
	}

	// Generations.
	genDir := cat.GenerationsDir()
	genEntries, err := readDirNoFollow(genDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, m.storeError(ReasonUnreadableFile, genDir, err)
	}
	for _, entry := range genEntries {
		name := entry.Name()
		path := filepath.Join(genDir, name)
		info, err := lsEntryInfo(genDir, entry)
		if err != nil {
			return nil, m.storeError(ReasonUnreadableFile, path, err)
		}
		if !info.IsDir() {
			continue
		}
		allocated, err := m.measureTreeAllocated(path, info)
		if err != nil {
			return nil, err
		}
		gu := &GenerationUsage{ID: name, Path: path, Allocated: allocated, Protected: true, Refs: -1}
		if man != nil {
			if rec := man.Generation(name); rec != nil {
				gu.Listed = true
				gu.Protected = rec.IsProtected()
			}
		}
		if repositoryLooksInitialised(path) {
			// A generation whose refs cannot be listed keeps Refs = -1, which
			// marks it unreclaimable. Failing the whole measurement instead
			// would make one unreadable generation hide every other workspace's
			// accounting.
			if _, hashes, err := m.snapshotRefsInDir(ctx, path); err == nil {
				gu.Refs = len(hashes)
			}
		}
		if gu.Listed {
			out.ByID[name] = gu
		} else {
			out.Unlisted = append(out.Unlisted, gu)
		}
	}

	// Capture scratch.
	stagingDir := cat.StagingDir()
	stagingEntries, err := readDirNoFollow(stagingDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, m.storeError(ReasonUnreadableFile, stagingDir, err)
	}
	for _, entry := range stagingEntries {
		path := filepath.Join(stagingDir, entry.Name())
		info, err := lsEntryInfo(stagingDir, entry)
		if err != nil {
			return nil, m.storeError(ReasonUnreadableFile, path, err)
		}
		allocated, err := m.measureTreeAllocated(path, info)
		if err != nil {
			return nil, err
		}
		if out.StagingAllocated, err = addInt64(out.StagingAllocated, allocated); err != nil {
			return nil, err
		}
		if out.AbandonedAllocated, err = addInt64(out.AbandonedAllocated, allocated); err != nil {
			return nil, err
		}
		out.StagingCount++
	}
	return out, nil
}

// Reclaimable returns the measured allocated bytes of the generations that
// automatic reclamation may consider: sealed, unprotected, listed, and with
// readable refs. It is a measure of the ceiling on what reclamation could free,
// not an instruction to free it.
func (g *WorkspaceGenerations) Reclaimable() int64 {
	if g == nil {
		return 0
	}
	var total int64
	for _, id := range g.Order {
		gu := g.ByID[id]
		if gu == nil || gu.Protected || gu.Refs < 0 {
			continue
		}
		total += gu.Allocated
	}
	return total
}

// usageOptions controls a discovery pass.
type usageOptions struct {
	// allocationUnit is the rounding granularity applied to logical lengths.
	allocationUnit int64
}

// Usage walks root and measures it conservatively. It never follows symlinks,
// so a symlink inside the store cannot pull data outside it into the totals or
// lead to deleting something outside the store. It requires the root to exist.
func (m *Manager) Usage() (*StoreUsage, error) {
	return m.UsageContext(context.Background())
}

// UsageContext is Usage bounded by a caller's context, so a cancelled turn is
// not held open by a per-generation accounting pass.
func (m *Manager) UsageContext(ctx context.Context) (*StoreUsage, error) {
	return m.usageWith(ctx, usageOptions{allocationUnit: m.limits.Normalize().AllocationUnitBytes})
}

// measureV2Workspaces adds a per-generation breakdown for every versioned
// workspace under the versioned root.
//
// A workspace whose breakdown cannot be read is reported with Corrupt set
// rather than failing the whole pass: one broken workspace must not hide the
// accounting of every other one, and a caller can still see that it must not be
// reclaimed.
func (m *Manager) measureV2Workspaces(ctx context.Context, _ usageOptions, out *StoreUsage) error {
	entries, err := readDirNoFollow(filepath.Join(m.root, v2DirName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return m.storeError(ReasonUnreadableFile, filepath.Join(m.root, v2DirName), err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if !validWorkspaceID(name) {
			continue
		}
		gens, err := m.MeasureWorkspaceGenerations(ctx, name)
		if err != nil {
			return err
		}
		out.Generations[name] = gens
	}
	return nil
}

// usageWith is Usage with explicit rounding.
func (m *Manager) usageWith(ctx context.Context, o usageOptions) (*StoreUsage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	if o.allocationUnit <= 0 {
		o.allocationUnit = m.limits.Normalize().AllocationUnitBytes
	}
	info, err := os.Lstat(m.root)
	if err != nil {
		return nil, m.storeError(ReasonUnreadableFile, m.root, err)
	}
	if !info.IsDir() {
		return nil, m.storeErrorf(ReasonInternal, m.root, "snapshots root is not a directory")
	}

	out := &StoreUsage{
		Root:        m.root,
		Workspaces:  make(map[string]*Usage),
		Generations: make(map[string]*WorkspaceGenerations),
	}
	rootAllocated, err := m.allocatedBytes(m.root, info, o.allocationUnit)
	if err != nil {
		return nil, err
	}
	out.RootMetadata = rootAllocated

	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, m.storeError(ReasonUnreadableFile, m.root, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		p := filepath.Join(m.root, name)
		entryInfo, err := lsEntryInfo(m.root, entry)
		if err != nil {
			return nil, m.storeError(ReasonUnreadableFile, p, err)
		}
		switch {
		case name == LockFileName:
			lock, err := m.measure(p, entryInfo, StoreKindLock, "", o.allocationUnit)
			if err != nil {
				return nil, err
			}
			out.Lock = lock
		case name == v2DirName && entryInfo.IsDir():
			// The versioned store root: measured as one entry so its total is
			// visible, and classified separately from legacy storage because
			// only the v2 side is ever automatically reclaimed.
			u, err := m.measureTree(p, entryInfo, StoreKindWorkspace, name, o.allocationUnit)
			if err != nil {
				return nil, err
			}
			out.Workspaces[name] = u
			out.V2Allocated = u.Allocated
			if err := m.measureV2Workspaces(ctx, o, out); err != nil {
				return nil, err
			}
		case entryInfo.IsDir():
			u, err := m.measureTree(p, entryInfo, StoreKindWorkspace, name, o.allocationUnit)
			if err != nil {
				return nil, err
			}
			out.Workspaces[name] = u
			if validWorkspaceID(name) {
				// A pre-v2 bare repo. Its bytes count toward the global total
				// but are never automatically reclaimed, so they are tracked
				// separately: a budget that ignored them would believe there is
				// room where there is none.
				if out.LegacyAllocated, err = addInt64(out.LegacyAllocated, u.Allocated); err != nil {
					return nil, err
				}
			}
		default:
			// A stray file directly under the root: count it globally but do
			// not claim it is any workspace's. Never auto-delete here.
			u, err := m.measure(p, entryInfo, StoreKindUnknown, "", o.allocationUnit)
			if err != nil {
				return nil, err
			}
			out.Unknown = append(out.Unknown, u)
		}
	}

	totalAllocated := out.RootMetadata
	totalLogical := out.RootMetadata
	filesAllocated := int64(0)
	filesLogical := int64(0)
	add := func(u *Usage) error {
		if u == nil {
			return nil
		}
		var err error
		if totalAllocated, err = addInt64(totalAllocated, u.Allocated); err != nil {
			return err
		}
		if totalLogical, err = addInt64(totalLogical, u.Logical); err != nil {
			return err
		}
		if filesAllocated, err = addInt64(filesAllocated, u.Allocated-u.DirBytes); err != nil {
			return err
		}
		if filesLogical, err = addInt64(filesLogical, u.Logical-u.DirBytes); err != nil {
			return err
		}
		return nil
	}
	for _, u := range out.Workspaces {
		if err := add(u); err != nil {
			return nil, err
		}
	}
	for _, u := range out.Unknown {
		if err := add(u); err != nil {
			return nil, err
		}
	}
	if err := add(out.Lock); err != nil {
		return nil, err
	}
	out.GlobalAllocated = totalAllocated
	out.GlobalLogical = totalLogical
	out.FilesAllocated = filesAllocated
	out.FilesLogical = filesLogical
	return out, nil
}

// DirsAllocated is the allocated bytes attributed to directory entries, which
// are not reclaimable by deleting generation contents. It is the exact
// accumulated directory metadata, not an estimate.
func (u *Usage) DirsAllocated() int64 {
	if u == nil {
		return 0
	}
	return u.DirBytes
}

// measureTree measures a directory and everything below it without following
// symlinks. Unknown accounting errors are returned, never counted as zero.
func (m *Manager) measureTree(root string, info os.FileInfo, kind StoreKind, workspace string, unit int64) (*Usage, error) {
	u := &Usage{Path: root, Kind: kind, Workspace: workspace}
	dirAllocated, err := m.allocatedBytes(root, info, unit)
	if err != nil {
		return nil, err
	}
	u.Allocated = dirAllocated
	u.Logical = dirAllocated
	u.DirBytes = dirAllocated
	u.Dirs = 1

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, m.storeError(ReasonUnreadableFile, root, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		p := filepath.Join(root, name)
		entryInfo, err := lsEntryInfo(root, entry)
		if err != nil {
			return nil, m.storeError(ReasonUnreadableFile, p, err)
		}
		// entry.Type() comes from the directory read and does not follow a
		// symlink. os.ReadDir does not follow symlinks, so a symlink appears
		// as a symlink and is never traversed.
		if entryInfo.IsDir() {
			sub, err := m.measureTree(p, entryInfo, kind, workspace, unit)
			if err != nil {
				return nil, err
			}
			if u.Allocated, err = addInt64(u.Allocated, sub.Allocated); err != nil {
				return nil, err
			}
			if u.Logical, err = addInt64(u.Logical, sub.Logical); err != nil {
				return nil, err
			}
			if u.DirBytes, err = addInt64(u.DirBytes, sub.DirBytes); err != nil {
				return nil, err
			}
			u.Dirs += sub.Dirs
			u.Files += sub.Files
			continue
		}
		file, err := m.measure(p, entryInfo, kind, workspace, unit)
		if err != nil {
			return nil, err
		}
		if u.Allocated, err = addInt64(u.Allocated, file.Allocated); err != nil {
			return nil, err
		}
		if u.Logical, err = addInt64(u.Logical, file.Logical); err != nil {
			return nil, err
		}
		u.Files += file.Files
	}
	return u, nil
}

// measure measures one non-directory entry (or a directory's own metadata when
// called directly). Symlinks are measured by their link length only and never
// traversed, so they cannot escape the store's accounting.
func (m *Manager) measure(path string, info os.FileInfo, kind StoreKind, workspace string, unit int64) (*Usage, error) {
	u := &Usage{Path: path, Kind: kind, Workspace: workspace}
	allocated, err := m.allocatedBytes(path, info, unit)
	if err != nil {
		return nil, err
	}
	u.Allocated = allocated
	u.Logical = m.logicalBytes(info, unit)
	if info.IsDir() {
		u.Dirs = 1
		u.DirBytes = allocated
	} else {
		u.Files = 1
	}
	return u, nil
}

// logicalBytes is the conservative logical size of an entry: for a symlink the
// length of its target text (what the link itself occupies), never the size of
// whatever it points at.
func (m *Manager) logicalBytes(info os.FileInfo, unit int64) int64 {
	size := info.Size()
	if size < 0 {
		size = 0
	}
	rounded, err := roundUpAllocation(size, unit)
	if err != nil {
		// Round-up can only overflow near MaxInt64, where the file already
		// exceeds every possible budget. Saturate rather than wrap.
		return size
	}
	return rounded
}

// allocatedBytes returns the conservative allocated size of an entry. On Unix
// this is the larger of st_blocks*512 and the rounded-up logical length, so a
// sparse or compressed file is never under-counted. A platform that cannot
// report allocation falls back to the rounded logical length.
func (m *Manager) allocatedBytes(path string, info os.FileInfo, unit int64) (int64, error) {
	logical := m.logicalBytes(info, unit)
	// The seam, not the package-level function: tests inject an accounting
	// failure here, and a seam that is bypassed is not a seam.
	allocated, err := m.allocatedSize(path, info)
	if err != nil {
		// An unknown stat failure is an accounting error, not zero: counting
		// it as zero would admit writes the budget never allowed.
		if errors.Is(err, ErrUnsupportedPlatform) {
			return 0, m.storeError(ReasonUnsupportedPlatform, path, err)
		}
		return 0, m.storeError(ReasonUnreadableFile, path, err)
	}
	if allocated < 0 {
		return 0, m.storeErrorf(ReasonUnreadableFile, path, "negative allocated size %d", allocated)
	}
	if allocated < logical {
		return logical, nil
	}
	return allocated, nil
}

// mulBlockBytes multiplies a filesystem block size by a block count, reporting
// an overflow rather than wrapping. statfs-style APIs report both as unsigned
// 64-bit values, so a nonsense volume could otherwise produce a negative
// "available bytes" that would pass a naive check.
func mulBlockBytes(blockSize, blocks int64) (int64, error) {
	if blockSize <= 0 || blocks <= 0 {
		return 0, nil
	}
	const maxBlocks = math.MaxInt64
	if blocks > maxBlocks/blockSize {
		return 0, overflowError("block size", blockSize, blocks)
	}
	return blockSize * blocks, nil
}

// lsEntryInfo returns the DirEntry's own info, using Lstat semantics so a
// symlink is reported as a symlink and never followed. Falling back to
// os.Lstat keeps behaviour identical when the entry cannot describe itself.
func lsEntryInfo(dir string, entry fs.DirEntry) (os.FileInfo, error) {
	info, err := entry.Info()
	if err == nil {
		return info, nil
	}
	return os.Lstat(filepath.Join(dir, entry.Name()))
}

// WorkspaceHashFor returns the 12-hex workspace identifier for a workspace
// root. It is the same identifier the existing shadow-repo layout uses, so a
// manager and a Service agree on which directory belongs to which workspace.
func WorkspaceHashFor(root string) string {
	return projectHash(root)
}

// WorkspaceStorePath returns the store directory for one workspace below root.
// It is containment-checked: the result is always a direct child of root.
func WorkspaceStorePath(root, workspace string) (string, error) {
	if workspace == "" || workspace == "." || workspace == ".." ||
		strings.ContainsAny(workspace, `/\`) {
		return "", &StoreError{
			Reason: ReasonInternal,
			Err:    errors.New("workspace identifier must be a single path element"),
		}
	}
	return filepath.Join(root, workspace), nil
}

// ValidWorkspaceID reports whether s is a workspace hash as written by the
// legacy layout. It is exported so the recovery CLI can reject a filesystem
// path supplied as `--workspace` before it opens the store: the identifier
// check is the boundary that keeps a user-supplied path from ever being
// resolved for deletion.
func ValidWorkspaceID(s string) bool { return validWorkspaceID(s) }
