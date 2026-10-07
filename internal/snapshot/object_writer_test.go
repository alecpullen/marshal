package snapshot

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// testObjectWriterEnv builds an owned manager, a catalog, an active generation,
// and an object writer bounded by allowance. It returns the generation's
// repository directory so a test can measure what was written.
func testObjectWriterEnv(t *testing.T, allowance int64) (*Manager, *Catalog, string, *objectWriter, context.Context) {
	t.Helper()
	m, cat, ctx := testCatalog(t)
	_, dir := newTestGeneration(t, ctx, cat)
	limits, err := m.EffectiveLimits()
	if err != nil {
		t.Fatalf("EffectiveLimits: %v", err)
	}
	budget, err := newObjectBudget(limits, allowance)
	if err != nil {
		t.Fatalf("newObjectBudget: %v", err)
	}
	w, err := newObjectWriter(ctx, m, dir, budget)
	if err != nil {
		t.Fatalf("newObjectWriter: %v", err)
	}
	t.Cleanup(w.cleanup)
	return m, cat, dir, w, ctx
}

// newTestGeneration creates, initialises, and activates one generation.
func newTestGeneration(t *testing.T, ctx context.Context, cat *Catalog) (string, string) {
	t.Helper()
	rec, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	if err := cat.ActivateGeneration(rec.ID); err != nil {
		t.Fatalf("ActivateGeneration: %v", err)
	}
	dir, err := cat.GenerationDir(rec.ID)
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	return rec.ID, dir
}

// testBlobSource is a controlled blob source: it declares one size and yields
// whatever the test tells it to yield. It is the deterministic seam the
// exact-read protocol is tested with, instead of a sleep-based race.
type testBlobSource struct {
	desc     string
	declared int64
	data     []byte
	pos      int
	// errorAt injects a read failure at a byte offset.
	errorAt int64
	err     error
}

func (s *testBlobSource) Size() int64         { return s.declared }
func (s *testBlobSource) Description() string { return s.desc }

func (s *testBlobSource) ReadChunk(p []byte) (int, error) {
	if s.err != nil && int64(s.pos) >= s.errorAt {
		return 0, s.err
	}
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += n
	return n, nil
}

// testBudgetWriter is a minimal destination for budgeted-writer tests.
type testBudgetWriter struct {
	buf bytes.Buffer
}

func (w *testBudgetWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

// ---------------------------------------------------------------------------
// zlib bound
// ---------------------------------------------------------------------------

// The reservation bound must hold for content that does not compress. This is
// the whole reason compression is disabled: a bound derived from the input
// length cannot be beaten by a stored-block encoding, whereas a compressing
// encoder could store incompressible content LONGER than its input in the worst
// case and shorter in the common case, which would make the bound wrong in both
// directions.
func TestZlibBoundCoversCompressibleAndIncompressibleContent(t *testing.T) {
	lengths := []int64{0, 1, 2, 100, 65534, 65535, 65536, 131070, 131071, 131072, 1 << 20, 3 << 20}
	for _, n := range lengths {
		for _, incompressible := range []bool{false, true} {
			payload := make([]byte, n)
			if incompressible {
				if _, err := rand.Read(payload); err != nil {
					t.Fatalf("rand.Read: %v", err)
				}
			}
			var buf bytes.Buffer
			zw, err := zlib.NewWriterLevel(&buf, flate.NoCompression)
			if err != nil {
				t.Fatalf("NewWriterLevel: %v", err)
			}
			if _, err := zw.Write(payload); err != nil {
				t.Fatalf("write payload: %v", err)
			}
			if err := zw.Close(); err != nil {
				t.Fatalf("close zlib: %v", err)
			}

			bound, err := zlibBound(n)
			if err != nil {
				t.Fatalf("zlibBound(%d): %v", n, err)
			}
			if int64(buf.Len()) > bound {
				t.Fatalf("n=%d incompressible=%v: encoded %d bytes exceeds bound %d",
					n, incompressible, buf.Len(), bound)
			}
			// Stored blocks must never store incompressible content ANY
			// smaller than its input, or a reservation derived from the input
			// length would be optimistic rather than conservative.
			if incompressible && int64(buf.Len()) < n {
				t.Fatalf("n=%d incompressible: encoded %d bytes is below the input length %d",
					n, buf.Len(), n)
			}
		}
	}
}

func TestZlibBoundRejectsNegativeLength(t *testing.T) {
	if _, err := zlibBound(-1); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("zlibBound(-1) error = %v, want ErrInvalidLimits", err)
	}
}

// ---------------------------------------------------------------------------
// Budgeted writer
// ---------------------------------------------------------------------------

// The budgeted writer refuses BEFORE the write that would exceed the allowance,
// so the bytes it refuses are never written. A bound checked after the write
// has already filled the disk.
func TestBudgetedWriterRefusesBeforeExceedingAllowance(t *testing.T) {
	limits := DefaultLimits()
	budget, err := newObjectBudget(limits, 10)
	if err != nil {
		t.Fatalf("newObjectBudget: %v", err)
	}
	dst := &testBudgetWriter{}
	w, err := NewBudgetedWriter(budget, "test")
	if err != nil {
		t.Fatalf("NewBudgetedWriter: %v", err)
	}
	if _, err := w.Write(make([]byte, 10)); err != nil {
		t.Fatalf("write within allowance: %v", err)
	}
	if budget.Spent() != 10 {
		t.Fatalf("spent = %d, want 10", budget.Spent())
	}
	if err := budget.Charge("one more byte", 1, "test"); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("charge past the allowance = %v, want ErrBudgetExhausted", err)
	}
	if budget.Spent() != 10 {
		t.Fatalf("a refused charge must not spend: spent = %d", budget.Spent())
	}
	// The refused byte is charged to a wrapper writer, which must also refuse
	// rather than write and then report.
	dst.buf.Reset()
	w2, err := NewBudgetedWriter(budget, "test2")
	if err != nil {
		t.Fatalf("NewBudgetedWriter: %v", err)
	}
	if _, err := w2.Write([]byte("x")); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("write past the allowance = %v, want ErrBudgetExhausted", err)
	}
	if w2.Written() != 0 {
		t.Fatalf("refused writer wrote %d bytes", w2.Written())
	}
}

func TestBudgetChargeRejectsNegativeAmount(t *testing.T) {
	budget, err := newObjectBudget(DefaultLimits(), 4096)
	if err != nil {
		t.Fatalf("newObjectBudget: %v", err)
	}
	if err := budget.Charge("negative", -1, "test"); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("Charge(-1) = %v, want ErrInvalidLimits", err)
	}
}

func TestNewObjectBudgetRejectsNegativeAllowance(t *testing.T) {
	if _, err := newObjectBudget(DefaultLimits(), -1); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("newObjectBudget(-1) = %v, want ErrInvalidLimits", err)
	}
}

// The per-object cap refuses a write that would exceed the reservation that
// object charged, so a mistake in one object's reservation cannot be paid for
// out of another object's allowance.
func TestCappedWriterRefusesBeyondItsReservation(t *testing.T) {
	dst := &testBudgetWriter{}
	c := &cappedWriter{dst: dst, limit: 4}
	if _, err := c.Write(make([]byte, 4)); err != nil {
		t.Fatalf("write within cap: %v", err)
	}
	if _, err := c.Write([]byte("x")); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("write past the cap = %v, want ErrBudgetExhausted", err)
	}
	if dst.buf.Len() != 4 {
		t.Fatalf("cap let %d bytes through, want 4", dst.buf.Len())
	}
}

// ---------------------------------------------------------------------------
// Canonical hashing
// ---------------------------------------------------------------------------

// Git SHA-1 addressing: the in-process hash and `git hash-object` must agree for
// blobs, trees, and commits. A disagreement means the object written is not the
// object named, which is corruption rather than a tolerable difference.
func TestInProcessHashAgreesWithGitForEveryObjectType(t *testing.T) {
	requireGit(t)
	_, _, _, w, ctx := testObjectWriterEnv(t, 1<<20)

	content := []byte("hello\n")
	blobHash, err := w.writeBlob(&bytesBlobSource{desc: "blob", data: content})
	if err != nil {
		t.Fatalf("writeBlob: %v", err)
	}
	want, err := objectID(objTypeBlob, content)
	if err != nil {
		t.Fatalf("objectID: %v", err)
	}
	if blobHash != want {
		t.Fatalf("blob hash = %s, want %s", blobHash, want)
	}
	// Git's independent hash of the same content, computed from a real file so
	// no filter can be involved.
	blobFile := filepath.Join(t.TempDir(), "blob.bin")
	if err := os.WriteFile(blobFile, content, 0o644); err != nil {
		t.Fatalf("write blob file: %v", err)
	}
	gitBlob := gitEnv(t, ctx, w.gitDir, "hash-object", "-t", "blob", "--no-filters", "--", blobFile)
	if gitBlob != blobHash {
		t.Fatalf("git hash-object = %s but this process hashed the blob as %s", gitBlob, blobHash)
	}

	treePayload := treePayloadFor(t, blobHash, "100644", "a.txt")
	tree, err := newGitObject(objTypeTree, treePayload)
	if err != nil {
		t.Fatalf("newGitObject(tree): %v", err)
	}
	if _, err := w.installObject(tree); err != nil {
		t.Fatalf("installObject(tree): %v", err)
	}
	gitTree := gitEnvStdin(t, ctx, w.gitDir, treePayload, "hash-object", "-t", "tree", "--stdin", "--no-filters")
	if gitTree != tree.Hash {
		t.Fatalf("git hashed the tree as %s but this process hashed it as %s", gitTree, tree.Hash)
	}

	commitHash, err := w.writeCommit(tree.Hash, CaptureRequest{Now: fixedTestTime()}, fixedTestTime())
	if err != nil {
		t.Fatalf("writeCommit: %v", err)
	}

	// Every object is readable by git and its type is what was written.
	for _, tc := range []struct{ hash, wantType string }{
		{blobHash, "blob"},
		{tree.Hash, "tree"},
		{commitHash, "commit"},
	} {
		got := gitEnv(t, ctx, w.gitDir, "cat-file", "-t", tc.hash)
		if got != tc.wantType {
			t.Fatalf("cat-file -t %s = %q, want %q", tc.hash, got, tc.wantType)
		}
	}
	// git fsck accepts the whole store.
	gitEnv(t, ctx, w.gitDir, "fsck", "--strict")
}

// gitEnvStdin runs a git command against dir with data on stdin, using the
// package's own sanitized invocation.
func gitEnvStdin(t *testing.T, ctx context.Context, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := newGitCmd(ctx, "git", dir, "", false, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := runGitCombined(ctx, cmd, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// treePayloadFor builds a one-entry tree payload for tests.
func treePayloadFor(t *testing.T, blobHash, mode, name string) []byte {
	t.Helper()
	raw, err := decodeObjectHash(blobHash)
	if err != nil {
		t.Fatalf("decodeObjectHash: %v", err)
	}
	out := []byte(mode + " " + name)
	out = append(out, 0)
	return append(out, raw...)
}

// A deliberate disagreement between the in-process hash and Git's hash aborts,
// and the object is not installed under its name.
func TestObjectWriterAbortsOnHashDisagreement(t *testing.T) {
	requireGit(t)
	_, _, _, w, _ := testObjectWriterEnv(t, 1<<20)
	w.gitHashObject = func(context.Context, string, io.Reader) (string, error) {
		return strings.Repeat("0", 40), nil
	}
	_, err := w.writeBlob(&bytesBlobSource{desc: "blob", data: []byte("hello\n")})
	if !errors.Is(err, ErrUnverifiedObject) {
		t.Fatalf("writeBlob error = %v, want ErrUnverifiedObject", err)
	}
	hash, idErr := objectID(objTypeBlob, []byte("hello\n"))
	if idErr != nil {
		t.Fatalf("objectID: %v", idErr)
	}
	if _, statErr := os.Lstat(filepath.Join(w.objectsDir, hash[:2], hash[2:])); statErr == nil {
		t.Fatal("a mismatched object was installed under its content-addressed name")
	}
}

// ---------------------------------------------------------------------------
// Reservation is an upper bound on what is written
// ---------------------------------------------------------------------------

// The reservation estimate must be an upper bound on the bytes actually written
// for both compressible and incompressible content. Measuring the object store
// after the fact is the only way to prove the bound is real rather than
// arithmetic that happens to agree with itself.
func TestReservationBoundsActualObjectBytes(t *testing.T) {
	requireGit(t)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "compressible", data: bytes.Repeat([]byte("a"), 300<<10)},
		{name: "incompressible", data: incompressibleBytes(t, 300<<10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, genDir, w, _ := testObjectWriterEnv(t, 4<<20)
			hash, err := w.writeBlob(&bytesBlobSource{desc: tc.name, data: tc.data})
			if err != nil {
				t.Fatalf("writeBlob: %v", err)
			}
			w.cleanup()

			rawLen, err := addInt64(int64(len(objectHeader(objTypeBlob, int64(len(tc.data))))), int64(len(tc.data)))
			if err != nil {
				t.Fatalf("addInt64: %v", err)
			}
			bound, err := zlibBound(rawLen)
			if err != nil {
				t.Fatalf("zlibBound: %v", err)
			}
			reservation, err := m.Limits().RoundUp(bound)
			if err != nil {
				t.Fatalf("RoundUp: %v", err)
			}
			spent, err := measureObjectBytes(m, genDir, hash)
			if err != nil {
				t.Fatalf("measureObjectBytes: %v", err)
			}
			// The reservation covers both the encoded bytes and the allocation
			// the filesystem gives them, so an object can never occupy more
			// space than was reserved for it.
			if spent.Logical > reservation {
				t.Fatalf("object %s is %d bytes, over its %d byte reservation",
					hash, spent.Logical, reservation)
			}
			allocatedBound, err := roundUpAllocation(spent.Logical, m.Limits().AllocationUnitBytes)
			if err != nil {
				t.Fatalf("roundUpAllocation: %v", err)
			}
			if spent.Allocated > allocatedBound {
				t.Fatalf("object %s allocates %d bytes, coarser than the %d byte allocation unit",
					hash, spent.Allocated, m.Limits().AllocationUnitBytes)
			}
			if spent.Logical < int64(len(tc.data)) {
				t.Fatalf("object %s is %d bytes on disk, smaller than its %d byte content",
					hash, spent.Logical, len(tc.data))
			}
		})
	}
}

// incompressibleBytes returns random bytes.
func incompressibleBytes(t *testing.T, n int) []byte {
	t.Helper()
	out := make([]byte, n)
	if _, err := rand.Read(out); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return out
}

// objectBytesOnDisk is the measured size of one loose object.
type objectBytesOnDisk struct {
	Logical   int64
	Allocated int64
}

// measureObjectBytes measures one loose object using the store's own accounting
// rules, so the figure is comparable with what the budget charges.
func measureObjectBytes(m *Manager, genDir, hash string) (objectBytesOnDisk, error) {
	if !validObjectHash(hash) {
		return objectBytesOnDisk{}, storeErrorf(ReasonInternal, "invalid object hash %q", hash)
	}
	hash = strings.ToLower(hash)
	path := filepath.Join(genDir, "objects", hash[:2], hash[2:])
	info, err := os.Lstat(path)
	if err != nil {
		return objectBytesOnDisk{}, storeError(ReasonUnreadableFile, err)
	}
	unit := m.Limits().AllocationUnitBytes
	allocated, err := m.allocatedBytes(path, info, unit)
	if err != nil {
		return objectBytesOnDisk{}, err
	}
	return objectBytesOnDisk{Logical: info.Size(), Allocated: allocated}, nil
}

// ---------------------------------------------------------------------------
// Deduplication is only counted after verification
// ---------------------------------------------------------------------------

// Writing the same content twice must spend once. It is not "the file exists":
// the existing object is decompressed and compared, so a truncated or foreign
// object at that path is not treated as a hit.
func TestDeduplicatedObjectIsVerifiedBeforeItIsCounted(t *testing.T) {
	requireGit(t)
	_, _, _, w, _ := testObjectWriterEnv(t, 1<<20)
	src := func() blobSource { return &bytesBlobSource{desc: "dup", data: []byte("the same bytes\n")} }

	first, err := w.writeBlob(src())
	if err != nil {
		t.Fatalf("first writeBlob: %v", err)
	}
	afterFirst := w.budget.Spent()
	second, err := w.writeBlob(src())
	if err != nil {
		t.Fatalf("second writeBlob: %v", err)
	}
	if first != second {
		t.Fatalf("identical content hashed differently: %s vs %s", first, second)
	}
	verified, deduped := w.Stats()
	if verified < 2 {
		t.Fatalf("verified = %d, want both writes verified against git", verified)
	}
	if deduped != 1 {
		t.Fatalf("deduped = %d, want exactly the second write", deduped)
	}
	// A deduplicated write charges no object bytes. The only charge it may add
	// is a single object directory, and only when the two contents landed in
	// different two-hex prefix directories.
	if delta := w.budget.Spent() - afterFirst; delta > w.budget.limits.AllocationUnitBytes {
		t.Fatalf("a deduplicated write spent %d extra bytes, more than one object directory", delta)
	}

	// Corrupt the existing object. It must not be accepted as a deduplication
	// hit, and it must not be silently overwritten either: an object at a
	// content-addressed path is what every snapshot naming that hash restores
	// from.
	dst, err := w.objectPath(first)
	if err != nil {
		t.Fatalf("objectPath: %v", err)
	}
	corrupt := []byte("not a zlib stream")
	if err := os.WriteFile(dst, corrupt, 0o644); err != nil {
		t.Fatalf("corrupt object: %v", err)
	}
	w.deduped = 0
	if _, err := w.writeBlob(src()); !errors.Is(err, ErrUnverifiedObject) {
		t.Fatalf("writeBlob over a corrupt object = %v, want ErrUnverifiedObject", err)
	}
	if _, deduped := w.Stats(); deduped != 0 {
		t.Fatalf("a corrupt existing object was counted as a deduplication hit (%d)", deduped)
	}
	if got, readErr := os.ReadFile(dst); readErr != nil || !bytes.Equal(got, corrupt) {
		t.Fatalf("the corrupt object was overwritten: %q (err %v)", got, readErr)
	}
}

// An existing object holding different bytes at the same content-addressed path
// is a hash collision. It is reported rather than silently overwritten, because
// overwriting it would rewrite history for every snapshot that already
// referenced it.
func TestExistingObjectWithDifferentBytesIsRefused(t *testing.T) {
	requireGit(t)
	_, _, _, w, _ := testObjectWriterEnv(t, 1<<20)
	// A real, well-formed tree object, so the only reason the write can fail is
	// the foreign content already at its path.
	blobHash, err := w.writeBlob(&bytesBlobSource{desc: "blob", data: []byte("hello\n")})
	if err != nil {
		t.Fatalf("writeBlob: %v", err)
	}
	obj, err := newGitObject(objTypeTree, treePayloadFor(t, blobHash, "100644", "a.txt"))
	if err != nil {
		t.Fatalf("newGitObject: %v", err)
	}
	dst, err := w.objectPath(obj.Hash)
	if err != nil {
		t.Fatalf("objectPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	foreign := []byte("foreign content, not a zlib stream")
	if err := os.WriteFile(dst, foreign, 0o644); err != nil {
		t.Fatalf("plant foreign object: %v", err)
	}
	if _, err := w.installObject(obj); !errors.Is(err, ErrUnverifiedObject) {
		t.Fatalf("installObject over a foreign object = %v, want ErrUnverifiedObject", err)
	}
	if got, readErr := os.ReadFile(dst); readErr != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("the foreign object was overwritten: %q (err %v)", got, readErr)
	}
}

// ---------------------------------------------------------------------------
// Exact-read protocol
// ---------------------------------------------------------------------------

// A source that declares one length and yields fewer bytes is truncation. The
// Git header would describe content that is not there, so it aborts.
func TestWriteBlobAbortsWhenSourceIsTruncated(t *testing.T) {
	requireGit(t)
	_, _, _, w, _ := testObjectWriterEnv(t, 1<<20)
	src := &testBlobSource{desc: "truncated", declared: 10, data: []byte("short")}
	if _, err := w.writeBlob(src); !errors.Is(err, ErrUnreadableFile) {
		t.Fatalf("writeBlob(truncated) = %v, want ErrUnreadableFile", err)
	}
}

// A source that yields MORE than it declared would make the header lie about
// the content, so it aborts even when the extra bytes are small.
func TestWriteBlobAbortsWhenSourceGrows(t *testing.T) {
	requireGit(t)
	_, _, _, w, _ := testObjectWriterEnv(t, 1<<20)
	src := &testBlobSource{desc: "growing", declared: 5, data: []byte("more than five bytes")}
	if _, err := w.writeBlob(src); !errors.Is(err, ErrUnreadableFile) {
		t.Fatalf("writeBlob(growing) = %v, want ErrUnreadableFile", err)
	}
}

// A real file that grows after it is opened is detected by the EOF probe, and a
// real file that shrinks is detected by the exact-length read. Both are
// deterministic: the mutation happens before the read.
func TestWriteBlobAbortsOnLiveFileGrowthAndTruncation(t *testing.T) {
	requireGit(t)
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, path string)
	}{
		{
			name: "grew within the size cap",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
					t.Fatalf("grow file: %v", err)
				}
			},
		},
		{
			// Growth PAST the per-file cap aborts too. A reader that stopped at
			// the declared length would otherwise record content from a file the
			// cap was supposed to exclude, and the snapshot would claim a
			// rollback point for something it never fully read.
			name: "grew past the per-file cap",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(strings.Repeat("z", 512)), 0o644); err != nil {
					t.Fatalf("grow file past the cap: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cat, _, w, _ := testObjectWriterEnv(t, 1<<20)
			path := filepath.Join(cat.WorkspaceRoot, "live.txt")
			if err := os.WriteFile(path, []byte("01234"), 0o644); err != nil {
				t.Fatalf("write file: %v", err)
			}
			src, err := newFileBlobSource(path)
			if err != nil {
				t.Fatalf("newFileBlobSource: %v", err)
			}
			defer src.Close()

			tc.mutate(t, path)

			if _, err := w.writeBlob(src); !errors.Is(err, ErrUnreadableFile) {
				t.Fatalf("writeBlob after growth = %v, want ErrUnreadableFile", err)
			}
		})
	}

	// The contrast that makes the previous cases meaningful: a file that was
	// ALREADY over the cap when it was classified is deliberately left out of
	// the snapshot rather than aborting it. "Excluded because too big" and
	// "capture failed" must stay distinguishable.
	t.Run("already oversized is excluded, not an abort", func(t *testing.T) {
		m, cat, _, _, _ := testObjectWriterEnv(t, 1<<20)
		writeWorkspaceFile(t, cat.WorkspaceRoot, "big.bin", strings.Repeat("z", 512))
		req := captureRequest(cat.WorkspaceRoot)
		req.MaxFileBytes = 64
		plan, err := m.planCapture(context.Background(), cat, req)
		if err != nil {
			t.Fatalf("planCapture: %v", err)
		}
		if len(plan.Entries) != 0 {
			t.Fatalf("an oversized file was selected: %+v", plan.Entries)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		_, cat, _, w, _ := testObjectWriterEnv(t, 1<<20)
		path := filepath.Join(cat.WorkspaceRoot, "live.txt")
		if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		src, err := newFileBlobSource(path)
		if err != nil {
			t.Fatalf("newFileBlobSource: %v", err)
		}
		defer src.Close()

		if err := os.Truncate(path, 3); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		if _, err := w.writeBlob(src); !errors.Is(err, ErrUnreadableFile) {
			t.Fatalf("writeBlob after truncation = %v, want ErrUnreadableFile", err)
		}
	})
}

// A named pipe must be refused rather than opened: opening one read-only blocks
// until a writer appears, so a capture that opened it would hang forever.
func TestOpenWorkspaceFileRefusesUnsupportedType(t *testing.T) {
	requireUnixFifo(t)
	_, cat, _, _, _ := testObjectWriterEnv(t, 1<<20)
	path := filepath.Join(cat.WorkspaceRoot, "a.fifo")
	if err := mkFifo(path); err != nil {
		t.Skipf("cannot create a fifo: %v", err)
	}
	if _, _, err := openWorkspaceFile(path); err == nil {
		t.Fatal("openWorkspaceFile accepted a named pipe")
	}
}

// A path that became a symlink after it was classified must not be read through
// the link: the bytes would come from outside the workspace.
func TestOpenWorkspaceFileRefusesAFinalSymlink(t *testing.T) {
	requireGit(t)
	_, cat, _, _, _ := testObjectWriterEnv(t, 1<<20)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	link := filepath.Join(cat.WorkspaceRoot, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create a symlink: %v", err)
	}
	if _, _, err := openWorkspaceFile(link); !errors.Is(err, errFinalSymlink) {
		t.Fatalf("openWorkspaceFile(symlink) = %v, want errFinalSymlink", err)
	}
	// The target text is still readable without following the link.
	target, err := openWorkspaceSymlink(link)
	if err != nil {
		t.Fatalf("openWorkspaceSymlink: %v", err)
	}
	if target != outside {
		t.Fatalf("symlink target = %q, want %q", target, outside)
	}
}
