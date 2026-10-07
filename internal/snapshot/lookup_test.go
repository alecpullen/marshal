package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file pins the lookup contract: which repository serves a snapshot hash,
// how the failures differ, and — most importantly — that hash text never
// becomes a Git revision expression.

// lookupService builds a Service against an isolated data dir and workspace.
func lookupService(t *testing.T, workspace string) (*Service, string) {
	t.Helper()
	requireServiceGit(t)
	dataDir := t.TempDir()
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	return New(dataDir, workspace, 2_000_000, nil, testLogger()), dataDir
}

// forceRotation seals the active generation and opens a replacement, which is
// exactly what admission does when a generation reaches its rotation target.
// It returns the sealed generation's id.
func forceRotation(t *testing.T, svc *Service) string {
	t.Helper()
	m, err := NewManager(svc.DataDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	cat, err := m.CatalogForRoot(svc.WorkTree())
	if err != nil {
		t.Fatalf("CatalogForRoot: %v", err)
	}
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = m.Release() }()

	man, err := cat.Load()
	if err != nil || man == nil {
		t.Fatalf("load manifest: %v (man=%v)", err, man)
	}
	active := man.ActiveID
	if active == "" {
		t.Fatal("workspace has no active generation to seal")
	}
	if err := cat.SealGeneration(active); err != nil {
		t.Fatalf("SealGeneration: %v", err)
	}
	rec, err := cat.CreateGeneration(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	if err := cat.ActivateGeneration(rec.ID); err != nil {
		t.Fatalf("ActivateGeneration: %v", err)
	}
	return active
}

// A hash retained in a SEALED generation (after rotation) is still found and
// restorable, and the location names the sealed generation rather than the
// active one.
func TestLookupFindsSealedGenerationAfterRotation(t *testing.T) {
	dir := t.TempDir()
	svc, _ := lookupService(t, dir)
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("before rotation"), 0o644); err != nil {
		t.Fatal(err)
	}
	sealedHash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	sealedGen := forceRotation(t, svc)

	// A capture into the new active generation.
	if err := os.WriteFile(path, []byte("after rotation"), 0o644); err != nil {
		t.Fatal(err)
	}
	activeHash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track after rotation: %v", err)
	}
	if activeHash == sealedHash {
		t.Fatal("rotation did not produce a distinct snapshot")
	}

	loc, err := svc.LookupSnapshot(context.Background(), sealedHash)
	if err != nil {
		t.Fatalf("LookupSnapshot(sealed): %v", err)
	}
	if loc.GenerationID != sealedGen {
		t.Fatalf("sealed hash resolved to generation %s, want %s", loc.GenerationID, sealedGen)
	}
	if loc.Legacy {
		t.Fatal("a v2 generation was reported as legacy")
	}

	activeLoc, err := svc.LookupSnapshot(context.Background(), activeHash)
	if err != nil {
		t.Fatalf("LookupSnapshot(active): %v", err)
	}
	if activeLoc.GenerationID == sealedGen {
		t.Fatalf("active hash resolved to the sealed generation %s", sealedGen)
	}

	// And the sealed generation's snapshot is still restorable.
	if err := os.WriteFile(path, []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.Restore(context.Background(), sealedHash); err != nil {
		t.Fatalf("Restore(sealed): %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before rotation" {
		t.Fatalf("restored %q, want the sealed generation's content", data)
	}
}

// The ACTIVE generation is checked FIRST, so the common case is one ref listing
// rather than one per generation. It is asserted structurally: with the active
// generation's directory renamed away, a lookup of a SEALED hash still succeeds
// (proving the loop continues), while a lookup that hit the active generation
// first never touches the sealed path.
func TestLookupChecksActiveGenerationFirst(t *testing.T) {
	dir := t.TempDir()
	svc, _ := lookupService(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	sealedHash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	forceRotation(t, svc)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	activeHash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}

	// The manifest's ordering is the assertion: the active generation must
	// come first, and every other generation after it.
	m, err := NewManager(svc.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	cat, err := m.CatalogForRoot(svc.WorkTree())
	if err != nil {
		t.Fatal(err)
	}
	man, err := cat.Load()
	if err != nil || man == nil {
		t.Fatalf("load manifest: %v", err)
	}
	order := orderedGenerations(man)
	if len(order) < 2 {
		t.Fatalf("orderedGenerations returned %v, want the active plus a sealed generation", order)
	}
	if order[0] != man.ActiveID {
		t.Fatalf("orderedGenerations = %v, want the active generation %s first", order, man.ActiveID)
	}
	if order[0] == "" {
		t.Fatal("the manifest has no active generation")
	}
	// Both hashes resolve, and each names a different generation.
	a, err := svc.LookupSnapshot(context.Background(), activeHash)
	if err != nil {
		t.Fatalf("lookup active: %v", err)
	}
	s, err := svc.LookupSnapshot(context.Background(), sealedHash)
	if err != nil {
		t.Fatalf("lookup sealed: %v", err)
	}
	if a.GenerationID != man.ActiveID {
		t.Fatalf("active hash resolved to %s, want the active generation %s", a.GenerationID, man.ActiveID)
	}
	if s.GenerationID == a.GenerationID {
		t.Fatal("the sealed and active hashes resolved to the same generation")
	}
}

// Expired, never-existed, corrupt, and invalid-identifier failures are
// distinguishable by reason.
func TestLookupDistinguishesExpiryNotFoundAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	svc, _ := lookupService(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}

	// Never existed: a syntactically valid hash nobody captured.
	ghost := strings.Repeat("0", 40)
	_, ghostErr := svc.LookupSnapshot(context.Background(), ghost)
	if got := ReasonOf(ghostErr); got != ReasonSnapshotExpired {
		t.Fatalf("lookup of an uncaptured hash = %q, want %q", got, ReasonSnapshotExpired)
	}
	if !errors.Is(ghostErr, ErrSnapshotExpired) {
		t.Fatalf("uncaptured hash error %v does not match ErrSnapshotExpired", ghostErr)
	}

	// Expired: the hash's generation was reclaimed whole. A pause is required
	// because retention compares the snapshot's committer time against the
	// cutoff, and a just-written ref is newer than "now".
	time.Sleep(1100 * time.Millisecond)
	if err := svc.Prune(context.Background(), 0); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// The reclamation must actually have happened, or this test would assert
	// nothing about expiry.
	if _, err := os.Stat(filepath.Join(svc.StoreDir(), generationsDirName)); err == nil {
		if entries, _ := os.ReadDir(filepath.Join(svc.StoreDir(), generationsDirName)); len(entries) != 0 {
			t.Fatalf("Prune left %d generation directories behind", len(entries))
		}
	}
	_, expiredErr := svc.LookupSnapshot(context.Background(), hash)
	if got := ReasonOf(expiredErr); got != ReasonSnapshotExpired {
		t.Fatalf("lookup of a reclaimed hash = %q, want %q", got, ReasonSnapshotExpired)
	}

	// Invalid identifier: refused BEFORE Git sees it.
	for _, bad := range []string{"", "HEAD", "main", "HEAD~1", "--upload-pack=/bin/echo", hash + "^{commit}", hash + ":a.txt"} {
		_, badErr := svc.LookupSnapshot(context.Background(), bad)
		if got := ReasonOf(badErr); got != ReasonInvalidObjectHash {
			t.Errorf("lookup(%q) reason = %q, want %q", bad, got, ReasonInvalidObjectHash)
		}
	}

	// Corrupt: a manifest that cannot be parsed is a repair problem, not an
	// expired snapshot.
	dataDir := t.TempDir()
	corruptDir := t.TempDir()
	corruptSvc := New(dataDir, corruptDir, 2_000_000, nil, testLogger())
	catRoot := filepath.Join(dataDir, snapshotsDirName, v2DirName, WorkspaceHashFor(corruptDir))
	if err := os.MkdirAll(catRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catRoot, manifestFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, corruptErr := corruptSvc.LookupSnapshot(context.Background(), hash)
	if got := ReasonOf(corruptErr); got != ReasonUnreadableFile {
		t.Fatalf("corrupt manifest lookup = %q, want %q", got, ReasonUnreadableFile)
	}
}

// A workspace with NO store at all is "never captured", not "expired": the
// remedy differs, and conflating them tells the user their snapshot was
// reclaimed when they never had one.
func TestLookupReportsNeverCapturedWhenNoStoreExists(t *testing.T) {
	requireServiceGit(t)
	dataDir := t.TempDir()
	dir := t.TempDir()
	svc := New(dataDir, dir, 2_000_000, nil, testLogger())

	_, err := svc.LookupSnapshot(context.Background(), strings.Repeat("a", 40))
	if got := ReasonOf(err); got != ReasonSnapshotNotFound {
		t.Fatalf("lookup with no store = %q, want %q", got, ReasonSnapshotNotFound)
	}
	if !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("error %v does not match ErrSnapshotNotFound", err)
	}
}

// A hash living in a LEGACY bare repository is still looked up and restorable.
// This is the migration-pending case: the v2 store may not exist yet (or may
// hold nothing), and the old history is still a valid rollback point.
func TestLookupFindsLegacySnapshotAndRestoresIt(t *testing.T) {
	requireServiceGit(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Build the legacy bare repo exactly as the pre-v2 layout did:
	// <snapshots>/<workspace-hash>/ with refs/snapshots/<hash>.
	legacyDir := filepath.Join(dataDir, snapshotsDirName, WorkspaceHashFor(dir))
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitEnv(t, ctx, legacyDir, "init", "--bare")
	legacyHash := writeSnapshotInto(t, ctx, legacyDir)

	svc := New(dataDir, dir, 2_000_000, nil, testLogger())
	loc, err := svc.LookupSnapshot(ctx, legacyHash)
	if err != nil {
		t.Fatalf("LookupSnapshot(legacy): %v", err)
	}
	if !loc.Legacy {
		t.Fatalf("location %+v is not marked legacy", loc)
	}
	if loc.GitDir != legacyDir {
		t.Fatalf("legacy location GitDir = %s, want %s", loc.GitDir, legacyDir)
	}

	// And it restores: the legacy store's own tree comes back.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.Restore(ctx, legacyHash); err != nil {
		t.Fatalf("Restore(legacy): %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "snapshot-content\n" {
		t.Fatalf("restored %q, want the legacy snapshot's content", data)
	}
}

// Revision-expression-shaped input is REJECTED before it reaches Git. The
// rejection is the command-injection boundary: a hash that Git would resolve as
// a name, an option, or a rev suffix must never be passed through.
func TestLookupRejectsRevisionExpressionsBeforeGit(t *testing.T) {
	requireServiceGit(t)
	dataDir := t.TempDir()
	dir := t.TempDir()
	svc := New(dataDir, dir, 2_000_000, nil, testLogger())

	good := strings.Repeat("a", 40)
	bad := []string{
		"",
		"HEAD",
		"main",
		"HEAD~1",
		"--upload-pack=/bin/echo",
		"--help",
		"refs/snapshots/" + good,
		good + "^{commit}",
		good + ":a.txt",
		good + "..HEAD",
		strings.ToUpper(good) + "z",
		strings.Repeat("g", 40),
	}
	for _, in := range bad {
		if _, err := svc.LookupSnapshot(context.Background(), in); !errors.Is(err, ErrInvalidObjectHash) {
			t.Errorf("LookupSnapshot(%q) = %v, want ErrInvalidObjectHash", in, err)
		}
		if _, err := svc.Diff(context.Background(), in); !errors.Is(err, ErrInvalidObjectHash) {
			t.Errorf("Diff(%q) = %v, want ErrInvalidObjectHash", in, err)
		}
		if err := svc.Restore(context.Background(), in); !errors.Is(err, ErrInvalidObjectHash) {
			t.Errorf("Restore(%q) = %v, want ErrInvalidObjectHash", in, err)
		}
	}
}

// A lookup, diff, or restore cannot run while another manager (standing in for
// a concurrent reclamation) owns the store. That mutual exclusion is what stops
// a reclamation deleting a generation out from under an in-flight operation:
// the operation either holds the lock already, or the reclamation does.
//
// The test is deterministic and uses NO sleeps. The seam is the lock itself: a
// bounded context turns "the lock is held elsewhere" into a definite answer,
// so the exclusion is asserted by its typed reason rather than by racing a
// goroutine and hoping.
func TestLookupDiffAndRestoreAreExcludedByTheStoreLock(t *testing.T) {
	dir := t.TempDir()
	svc, dataDir := lookupService(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}

	// A second manager takes the store lock, exactly as a concurrent
	// reclamation would, and holds it for the whole exclusion block.
	other, err := NewManager(dataDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := other.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := other.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Each operation must be REFUSED, not merely slow, and must say so with the
	// lock reason. A different reason would mean it got further than it should.
	// Each call gets its OWN bounded context, so one call's expiry can never be
	// mistaken for the next call's answer.
	heldLookup, cancelLookup := context.WithTimeout(context.Background(), lockPollInterval*4)
	_, lookupErr := svc.LookupSnapshot(heldLookup, hash)
	cancelLookup()
	if ReasonOf(lookupErr) != ReasonLockTimeout {
		t.Fatalf("LookupSnapshot while another owner held the store = %v, want a lock timeout", lookupErr)
	}
	heldDiff, cancelDiff := context.WithTimeout(context.Background(), lockPollInterval*4)
	_, diffErr := svc.Diff(heldDiff, hash)
	cancelDiff()
	if ReasonOf(diffErr) != ReasonLockTimeout {
		t.Fatalf("Diff while another owner held the store = %v, want a lock timeout", diffErr)
	}
	heldRestore, cancelRestore := context.WithTimeout(context.Background(), lockPollInterval*4)
	restoreErr := svc.Restore(heldRestore, hash)
	cancelRestore()
	if ReasonOf(restoreErr) != ReasonLockTimeout {
		t.Fatalf("Restore while another owner held the store = %v, want a lock timeout", restoreErr)
	}

	// The generation is exactly where it was, so the exclusion means the
	// concurrent reclamation never ran.
	if err := other.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := svc.LookupSnapshot(context.Background(), hash); err != nil {
		t.Fatalf("lookup after release: %v", err)
	}
	if _, err := svc.Diff(context.Background(), hash); err != nil {
		t.Fatalf("diff after release: %v", err)
	}
	if err := svc.Restore(context.Background(), hash); err != nil {
		t.Fatalf("restore after release: %v", err)
	}
}

// A hash that lives in the legacy repository is still found once the v2 store
// exists but does not retain it: the v2 path is checked first and the legacy
// repository is the fallback that preserves pre-migration rollback history.
func TestLookupFallsBackToLegacyWhenV2DoesNotRetain(t *testing.T) {
	requireServiceGit(t)
	ctx := context.Background()
	dir := t.TempDir()
	svc, dataDir := lookupService(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v2 store"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Track(ctx); err != nil {
		t.Fatalf("Track: %v", err)
	}

	// A legacy repository beside the v2 store, holding a different snapshot.
	legacyDir := filepath.Join(dataDir, snapshotsDirName, svc.Workspace())
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitEnv(t, ctx, legacyDir, "init", "--bare")
	legacyHash := writeSnapshotInto(t, ctx, legacyDir)

	loc, err := svc.LookupSnapshot(ctx, legacyHash)
	if err != nil {
		t.Fatalf("LookupSnapshot(legacy alongside v2): %v", err)
	}
	if !loc.Legacy || loc.GitDir != legacyDir {
		t.Fatalf("location = %+v, want the legacy repository", loc)
	}
}
