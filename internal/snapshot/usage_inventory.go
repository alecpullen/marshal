package snapshot

import (
	"context"
	"os"
	"path/filepath"
)

// usageInventory belongs to one read phase. Only directory aggregates are
// retained: keeping every loose object's stat would turn saved work into a
// large retained heap. Metadata files are cheap to measure individually.
type usageInventory struct {
	unit int64
	dirs map[string]usageDirectory
}

type usageDirectory struct {
	usage *Usage
	info  os.FileInfo
}

func (m *Manager) inventoryTree(ctx context.Context, path string, info os.FileInfo, kind StoreKind, workspace string, unit int64) (*usageInventory, *Usage, error) {
	inv := &usageInventory{unit: unit, dirs: make(map[string]usageDirectory)}
	u, err := m.measureTreeWith(ctx, path, info, kind, workspace, unit, inv)
	return inv, u, err
}

// allocatedFromInventory refuses a directory that appeared or was replaced
// after the walk. Inventories never survive mutations or move between phases.
func (m *Manager) allocatedFromInventory(ctx context.Context, path string, info os.FileInfo, inv *usageInventory) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	unit := m.limits.Normalize().AllocationUnitBytes
	if inv != nil {
		unit = inv.unit
	}
	if !info.IsDir() {
		return m.allocatedBytes(path, info, unit)
	}
	if inv != nil {
		d, ok := inv.dirs[path]
		if !ok || !os.SameFile(d.info, info) || !d.info.ModTime().Equal(info.ModTime()) {
			return 0, m.storeErrorf(ReasonUnreadableFile, path, "directory changed during usage inventory")
		}
		return d.usage.Allocated, nil
	}
	u, err := m.measureTreeWith(ctx, path, info, StoreKindWorkspace, "", unit, nil)
	if err != nil {
		return 0, err
	}
	return u.Allocated, nil
}

func (m *Manager) measureTreeWith(ctx context.Context, root string, info os.FileInfo, kind StoreKind, workspace string, unit int64, inv *usageInventory) (*Usage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u := &Usage{Path: root, Kind: kind, Workspace: workspace}
	dirAllocated, err := m.allocatedBytes(root, info, unit)
	if err != nil {
		return nil, err
	}
	u.Allocated, u.Logical, u.DirBytes, u.Dirs = dirAllocated, dirAllocated, dirAllocated, 1
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, m.storeError(ReasonUnreadableFile, root, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(root, entry.Name())
		entryInfo, err := lsEntryInfo(root, entry)
		if err != nil {
			return nil, m.storeError(ReasonUnreadableFile, path, err)
		}
		var sub *Usage
		if entryInfo.IsDir() {
			sub, err = m.measureTreeWith(ctx, path, entryInfo, kind, workspace, unit, inv)
		} else {
			// Entry info never follows symlinks; a link contributes its own
			// allocation/target-text length and cannot escape the store.
			sub, err = m.measure(path, entryInfo, kind, workspace, unit)
		}
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
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if inv != nil {
		inv.dirs[root] = usageDirectory{usage: u, info: info}
	}
	return u, nil
}
