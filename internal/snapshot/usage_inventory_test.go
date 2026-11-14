package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each consumer needs byte totals for the same objects at several hierarchy
// levels. A second accounting visit does filesystem work without adding facts.
func TestAccountingVisitsObjectsOncePerPhase(t *testing.T) {
	for _, phase := range []string{"usage", "workspace", "discovery"} {
		t.Run(phase, func(t *testing.T) {
			requireGit(t)
			m, cat, root, gen := testCaptureEnv(t)
			writeWorkspaceFile(t, root, "source.go", "package sample\n")
			runCapture(t, m, cat, root, captureRequest(root))
			objects, _, err := looseObjects(gen)
			if err != nil || len(objects) == 0 {
				t.Fatalf("objects: %v", err)
			}
			counts := map[string]int{}
			original := m.allocatedSize
			m.allocatedSize = func(path string, info os.FileInfo) (int64, error) {
				for _, object := range objects {
					if path == object {
						counts[path]++
					}
				}
				return original(path, info)
			}
			switch phase {
			case "usage":
				_, err = m.UsageContext(context.Background())
			case "workspace":
				_, err = m.MeasureWorkspaceGenerations(context.Background(), cat.Workspace())
			case "discovery":
				_, err = m.discoverWorkspace(context.Background(), cat.Workspace())
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, object := range objects {
				if counts[object] != 1 {
					t.Errorf("%s accounted %s %d times, want once", phase, filepath.Base(object), counts[object])
				}
			}
		})
	}
}

func TestRotationMeasurementDoesNotWalkOtherGenerations(t *testing.T) {
	requireGit(t)
	m, cat, root, target := testCaptureEnv(t)
	writeWorkspaceFile(t, root, "source.go", "package sample\n")
	runCapture(t, m, cat, root, captureRequest(root))
	man, err := cat.Load()
	if err != nil {
		t.Fatal(err)
	}
	targetID := man.ActiveID
	_, other := newTestGeneration(t, context.Background(), cat)
	original := m.allocatedSize
	otherVisits := 0
	m.allocatedSize = func(path string, info os.FileInfo) (int64, error) {
		if path == other || strings.HasPrefix(path, other+string(filepath.Separator)) {
			otherVisits++
			return 0, errors.New("unrelated generation cannot be measured")
		}
		if path == target {
			return m.rotationTarget() + 4096, nil
		}
		return original(path, info)
	}
	if !m.overRotationTarget(context.Background(), cat, targetID) {
		t.Fatal("readable oversized target was not assessed")
	}
	if otherVisits != 0 {
		t.Fatalf("rotation visited unrelated generation %d times", otherVisits)
	}
}

func TestInventoryConservativeTotalsWithoutRetainingFiles(t *testing.T) {
	for _, tc := range []struct{ unit, bytes int64 }{{4096, 28672}, {8192, 49152}} {
		m, _, _ := testCatalog(t, WithAllocatedSizeQuery(func(string, os.FileInfo) (int64, error) { return 4096, nil }))
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "sub", "payload"), 8193)
		writeFile(t, filepath.Join(root, "empty"), 0)
		outside := filepath.Join(t.TempDir(), "outside")
		writeFile(t, outside, 1<<20)
		if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		info, err := os.Lstat(root)
		if err != nil {
			t.Fatal(err)
		}
		inv, u, err := m.inventoryTree(context.Background(), root, info, StoreKindWorkspace, "", tc.unit)
		if err != nil {
			t.Fatal(err)
		}
		if u.Allocated != tc.bytes || u.Logical != tc.bytes || u.DirBytes != 2*tc.unit || u.Dirs != 2 || u.Files != 3 {
			t.Fatalf("unit %d: unexpected conservative usage %+v", tc.unit, u)
		}
		if len(inv.dirs) != 2 {
			t.Fatalf("inventory retained more than directory totals: %d", len(inv.dirs))
		}
	}
}

func TestInventoryRefusesMissingAndReplacedDirectories(t *testing.T) {
	m, _, _ := testCatalog(t)
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	inv, _, err := m.inventoryTree(context.Background(), root, info, StoreKindWorkspace, "", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(child, filepath.Join(t.TempDir(), "held-inode")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{child, filepath.Join(root, "new-directory")} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.allocatedFromInventory(context.Background(), path, info, inv); err == nil {
			t.Fatal("changed directory was accepted", path)
		}
	}
}

func TestUsageInventoryIsFreshAcrossCalls(t *testing.T) {
	m, _, _, gen := testCaptureEnv(t)
	path := filepath.Join(gen, "growing")
	writeFile(t, path, 1)
	before, err := m.UsageContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, 128<<10)
	after, err := m.UsageContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.GlobalAllocated <= before.GlobalAllocated {
		t.Fatal("v2 inventory reused across calls")
	}
}

func TestInventoryStopsAccountingAfterCancellation(t *testing.T) {
	m, _, _ := testCatalog(t)
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		writeFile(t, filepath.Join(root, name), 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := m.allocatedSize
	afterCancel := 0
	m.allocatedSize = func(path string, info os.FileInfo) (int64, error) {
		if ctx.Err() != nil {
			afterCancel++
		}
		if path == filepath.Join(root, "a") {
			cancel()
		}
		return original(path, info)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.inventoryTree(ctx, root, info, StoreKindWorkspace, "", 4096); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if afterCancel != 0 {
		t.Fatalf("accounting continued after cancellation: %d calls", afterCancel)
	}
}

func TestRotationMeasurementPreservesSafetyGuards(t *testing.T) {
	for _, guard := range []string{"protected", "legacy", "corrupt-manifest", "unreadable-refs", "unlisted", "symlink", "symlink-workspace", "symlink-generations"} {
		t.Run(guard, func(t *testing.T) {
			requireGit(t)
			m, cat, root, gen := testCaptureEnv(t)
			writeWorkspaceFile(t, root, "source.go", "package sample\n")
			runCapture(t, m, cat, root, captureRequest(root))
			man, err := cat.Load()
			if err != nil {
				t.Fatal(err)
			}
			id := man.ActiveID
			original := m.allocatedSize
			m.allocatedSize = func(path string, info os.FileInfo) (int64, error) {
				if path == gen {
					return m.rotationTarget() + 4096, nil
				}
				return original(path, info)
			}
			if !m.overRotationTarget(context.Background(), cat, id) {
				t.Fatal("fixture target is not oversized and readable")
			}
			switch guard {
			case "protected":
				man.Generation(id).Protected = true
				err = cat.Save(man)
			case "legacy":
				man.Generation(id).Origin = OriginLegacy
				err = cat.Save(man)
			case "corrupt-manifest":
				err = os.WriteFile(cat.ManifestPath(), []byte("not a manifest"), 0o644)
			case "unreadable-refs":
				m.gitBinary = filepath.Join(t.TempDir(), "missing-git")
			case "unlisted":
				man.Generations = nil
				man.ActiveID = ""
				err = cat.Save(man)
			case "symlink", "symlink-workspace", "symlink-generations":
				linked := gen
				if guard == "symlink-workspace" {
					linked = cat.Root()
				}
				if guard == "symlink-generations" {
					linked = cat.GenerationsDir()
				}
				outside := filepath.Join(t.TempDir(), "held")
				if err := os.Rename(linked, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, linked); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if m.overRotationTarget(context.Background(), cat, id) {
				t.Fatalf("rotation ignored %s guard", guard)
			}
		})
	}
}

func TestRotationMeasurementSeesFreshGrowth(t *testing.T) {
	requireGit(t)
	m, cat, root, gen := testCaptureEnv(t)
	writeWorkspaceFile(t, root, "source.go", "package sample\n")
	runCapture(t, m, cat, root, captureRequest(root))
	man, err := cat.Load()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(gen, "padding")
	writeFile(t, path, 1)
	if m.overRotationTarget(context.Background(), cat, man.ActiveID) {
		t.Fatal("small generation exceeds target")
	}
	// Sparse growth changes the file, not its parent's mtime. A directory-
	// metadata cache would miss it; the rotation check must measure afresh.
	if err := os.Truncate(path, m.rotationTarget()); err != nil {
		t.Fatal(err)
	}
	if !m.overRotationTarget(context.Background(), cat, man.ActiveID) {
		t.Fatal("rotation missed fresh generation growth")
	}
}
