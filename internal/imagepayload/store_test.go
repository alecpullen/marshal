package imagepayload

import (
	"context"
	"errors"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/llm/schema"
)

func TestStoreFilesArePrivate(t *testing.T) {
	st := newStore(t, Config{MaxEdge: 1568})
	ref := put(t, st, solidPNG(t, 10, 10, color.RGBA{A: 0xFF}), Options{})

	dir := filepath.Dir(st.path(ref.Hash))
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat shard: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("shard mode is %o, want 700", perm)
	}

	entryInfo, err := os.Stat(st.path(ref.Hash))
	if err != nil {
		t.Fatalf("stat entry: %v", err)
	}
	if perm := entryInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("entry mode is %o, want 600", perm)
	}
}

func TestImageMissingIsNotFound(t *testing.T) {
	st := newStore(t, Config{})
	_, _, err := st.Image(context.Background(), strings.Repeat("a", 64))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("image returned %v, want ErrNotFound", err)
	}
}

func TestSweepKeepsReferencedAndYoung(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, Config{MaxEdge: 1568, MaxStoreBytes: 1, RetainDays: 14})

	unreferencedOld := put(t, st, solidPNG(t, 10, 10, color.RGBA{R: 0xFF, A: 0xFF}), Options{})
	referencedOld := put(t, st, solidPNG(t, 10, 10, color.RGBA{G: 0xFF, A: 0xFF}), Options{})
	young := put(t, st, solidPNG(t, 10, 10, color.RGBA{B: 0xFF, A: 0xFF}), Options{})

	old := time.Now().UTC().AddDate(0, 0, -30)
	for _, ref := range []schema.ImageRef{unreferencedOld, referencedOld} {
		if err := st.writeSidecar(ref.Hash, sidecar{ImageRef: ref, LastUsed: old}); err != nil {
			t.Fatalf("write sidecar: %v", err)
		}
	}

	sweep, err := st.Sweep(ctx, func() (map[string]struct{}, error) {
		return map[string]struct{}{referencedOld.Hash: {}}, nil
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if sweep.Removed != 1 {
		t.Fatalf("sweep removed %d entries, want 1", sweep.Removed)
	}
	if sweep.FreedBytes != int64(unreferencedOld.Bytes) {
		t.Fatalf("sweep freed %d bytes, want %d", sweep.FreedBytes, unreferencedOld.Bytes)
	}
	if _, _, err := st.Image(ctx, unreferencedOld.Hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unreferenced old entry still present: %v", err)
	}
	if _, _, err := st.Image(ctx, referencedOld.Hash); err != nil {
		t.Fatalf("referenced old entry was removed: %v", err)
	}
	if _, _, err := st.Image(ctx, young.Hash); err != nil {
		t.Fatalf("young entry was removed: %v", err)
	}
}

func TestSweepNoOpUnderCap(t *testing.T) {
	st := newStore(t, Config{MaxEdge: 1568, MaxStoreBytes: 1 << 40, RetainDays: 14})
	put(t, st, solidPNG(t, 10, 10, color.RGBA{A: 0xFF}), Options{})

	sweep, err := st.Sweep(context.Background(), nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if sweep.Removed != 0 {
		t.Fatalf("sweep removed %d entries under the cap, want 0", sweep.Removed)
	}
}

func TestCrashedWriteLeavesNoEntry(t *testing.T) {
	st := newStore(t, Config{})
	if err := writeFileAtomic(filepath.Join(st.root, "no-such-shard", "entry.json"), []byte("{}")); err == nil {
		t.Fatal("writeFileAtomic into a missing directory succeeded, want an error")
	}

	var found []string
	err := filepath.WalkDir(st.root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(p) == ".json" {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk store: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("crashed write left %d entries: %v", len(found), found)
	}
}
