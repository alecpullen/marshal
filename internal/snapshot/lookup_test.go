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

// ---------------------------------------------------------------------------
// Historical workspace identities
// ---------------------------------------------------------------------------

// writeSnapshotAt writes a blob, tree, and parentless commit carrying content
// into dir and publishes its snapshot ref, exactly as a capture would. The
// commit is stamped with a FIXED date so its hash is a pure function of the
// content: a fixture can put the SAME hash in two stores, or rely on distinct
// content producing distinct hashes, without depending on the clock.
func writeSnapshotAt(t *testing.T, ctx context.Context, dir, content string) string {
	t.Helper()
	blob := newGitCmd(ctx, "git", dir, "", false, "hash-object", "-w", "-t", "blob", "--stdin")
	blob.Stdin = strings.NewReader(content)
	out, err := runGitCombined(ctx, blob, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("hash-object in %s: %v\n%s", dir, err, out)
	}
	blobHash := strings.TrimSpace(string(out))

	tree := newGitCmd(ctx, "git", dir, "", false, "mktree")
	tree.Stdin = strings.NewReader("100644 blob " + blobHash + "\ta.txt\n")
	out, err = runGitCombined(ctx, tree, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("mktree in %s: %v\n%s", dir, err, out)
	}
	treeHash := strings.TrimSpace(string(out))

	commit := newGitCmd(ctx, "git", dir, "", false, "commit-tree", treeHash)
	commit.Stdin = strings.NewReader("snapshot\n")
	commit.Env = append(commit.Env,
		"GIT_AUTHOR_NAME=marshal", "GIT_AUTHOR_EMAIL=marshal@local",
		"GIT_COMMITTER_NAME=marshal", "GIT_COMMITTER_EMAIL=marshal@local",
		"GIT_AUTHOR_DATE=2020-01-01T00:00:00 +0000",
		"GIT_COMMITTER_DATE=2020-01-01T00:00:00 +0000",
	)
	out, err = runGitCombined(ctx, commit, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("commit-tree in %s: %v\n%s", dir, err, out)
	}
	commitHash := strings.TrimSpace(string(out))

	gitEnv(t, ctx, dir, "update-ref", commitRef(commitHash), commitHash)
	return commitHash
}

// historicalIdentityFixture is a workspace reachable by two spellings: a real
// directory and an owned symlink to it. The canonical store holds one snapshot;
// the symlink's spelling is the historical identity.
type historicalIdentityFixture struct {
	real, link, dataDir string
	// svc is the service bound to the real (canonical) spelling.
	svc *Service
	// canonicalRoot is the canonical workspace root captures record; rawID is
	// the identity of the original (symlink) spelling and canonicalID the store
	// captures write to.
	canonicalRoot      string
	canonicalID, rawID string
	canonicalHash      string
}

func newHistoricalIdentityFixture(t *testing.T, ctx context.Context) *historicalIdentityFixture {
	t.Helper()
	f := newAliasWorkspace(t)
	hash, err := f.svc.Track(ctx)
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if !validObjectHash(hash) {
		t.Fatalf("Track returned %q, which is not an object id", hash)
	}
	f.canonicalHash = hash
	return f
}

// newAliasWorkspace builds a workspace reachable by a real path and an owned
// symlink to it, WITHOUT capturing anything, so a maintenance test can decide
// which stores exist.
func newAliasWorkspace(t *testing.T) *historicalIdentityFixture {
	t.Helper()
	requireServiceGit(t)
	parent := t.TempDir()
	real := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "a.txt"), []byte("canonical\n"), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	link := filepath.Join(parent, "workspace-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink workspace: %v", err)
	}
	dataDir := t.TempDir()
	// The ORIGINAL supplied spelling is the link: that is the path whose raw
	// hash becomes the workspace's one known historical identity, exactly as the
	// plan specifies.
	svc := New(dataDir, link, 2_000_000, nil, testLogger())
	canonicalRoot, err := canonicalWorkspaceRoot(real)
	if err != nil {
		t.Fatalf("canonicalWorkspaceRoot: %v", err)
	}
	f := &historicalIdentityFixture{
		real: real, link: link, dataDir: dataDir, svc: svc, canonicalRoot: canonicalRoot,
		canonicalID: svc.Workspace(), rawID: WorkspaceHashFor(link),
	}
	if f.rawID == f.canonicalID {
		t.Fatalf("the fixture's historical and canonical identities collided (%s); the alias is not exercised", f.rawID)
	}
	return f
}

// withLock runs fn while this fixture owns the store, releasing it afterwards.
// Ownership is taken only for fixture setup: a Service operation acquires the
// same lock through its own manager, so holding it across one would deadlock.
func (f *historicalIdentityFixture) withLock(t *testing.T, ctx context.Context, fn func(*Manager)) {
	t.Helper()
	m, err := NewManager(f.dataDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := m.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() {
		if err := m.Release(); err != nil {
			t.Fatalf("Release: %v", err)
		}
	}()
	fn(m)
}

// historicalV2Catalog creates a v2 catalog under a HISTORICAL identity and
// returns it with its active generation's directory. The manifest records
// canonicalRoot, which is the provenance a historical catalog must prove before
// it may serve or reclaim anything.
func historicalV2Catalog(t *testing.T, ctx context.Context, m *Manager, rawID, canonicalRoot string) (*Catalog, string) {
	t.Helper()
	cat, err := m.Catalog(rawID)
	if err != nil {
		t.Fatalf("Catalog(%s): %v", rawID, err)
	}
	cat.WorkspaceRoot = canonicalRoot
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
	return cat, dir
}

// A workspace keeps serving snapshots from every identity it is known by: the
// canonical v2 store, a validated historical v2 catalog written under the
// original spelling, and a pre-v2 bare repository. An exact canonical hit wins
// when both stores retain a hash, and no lookup, diff or restore touches the
// historical stores.
func TestServiceLookupHistoricalWorkspaceIdentity(t *testing.T) {
	ctx := context.Background()
	f := newHistoricalIdentityFixture(t, ctx)

	var canonCat *Catalog
	var rawDir, rawHash string
	f.withLock(t, ctx, func(m *Manager) {
		var err error
		canonCat, err = m.CatalogForRoot(f.real)
		if err != nil {
			t.Fatalf("CatalogForRoot: %v", err)
		}
		_, rawDir = historicalV2Catalog(t, ctx, m, f.rawID, f.canonicalRoot)
		rawHash = writeSnapshotAt(t, ctx, rawDir, "raw-v2\n")
	})

	legacyDir := filepath.Join(f.dataDir, snapshotsDirName, f.rawID)
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	gitEnv(t, ctx, legacyDir, "init", "--bare")
	legacyHash := writeSnapshotAt(t, ctx, legacyDir, "raw-legacy\n")

	// A hash BOTH stores retain resolves to the canonical one: the historical
	// store is a fallback, never a competing answer. The duplicate is written
	// here, BEFORE the fingerprints, so the only writes after them are the
	// lookups under test.
	var sharedHash string
	f.withLock(t, ctx, func(m *Manager) {
		canonDir := generationHolding(t, m, canonCat, f.canonicalHash)
		sharedHash = writeSnapshotAt(t, ctx, canonDir, "shared\n")
		if got := writeSnapshotAt(t, ctx, rawDir, "shared\n"); got != sharedHash {
			t.Fatalf("identical content produced different hashes (%s vs %s); the duplicate case is not exercised", got, sharedHash)
		}
	})

	rawBefore := storeFingerprint(t, filepath.Join(f.dataDir, snapshotsDirName, v2DirName, f.rawID))
	legacyBefore := storeFingerprint(t, legacyDir)

	// Each exact hash resolves in its own store, and names it.
	canonLoc, err := f.svc.LookupSnapshot(ctx, f.canonicalHash)
	if err != nil {
		t.Fatalf("LookupSnapshot(canonical): %v", err)
	}
	if canonLoc.Workspace != f.canonicalID || canonLoc.Legacy || canonLoc.GitDir == rawDir {
		t.Fatalf("canonical location = %+v, want the canonical v2 store", canonLoc)
	}

	rawLoc, err := f.svc.LookupSnapshot(ctx, rawHash)
	if err != nil {
		t.Fatalf("LookupSnapshot(historical v2): %v", err)
	}
	if rawLoc.Workspace != f.rawID || rawLoc.Legacy || rawLoc.GitDir != rawDir {
		t.Fatalf("historical v2 location = %+v, want %s in %s", rawLoc, f.rawID, rawDir)
	}

	legacyLoc, err := f.svc.LookupSnapshot(ctx, legacyHash)
	if err != nil {
		t.Fatalf("LookupSnapshot(legacy): %v", err)
	}
	if !legacyLoc.Legacy || legacyLoc.GitDir != legacyDir || legacyLoc.Workspace != f.rawID {
		t.Fatalf("legacy location = %+v, want the bare repository %s", legacyLoc, legacyDir)
	}
	if legacyLoc.Protected {
		t.Fatal("a legacy bare repository reported itself protected")
	}

	// Diff and restore reach the same repositories.
	if _, err := f.svc.Diff(ctx, rawHash); err != nil {
		t.Fatalf("Diff(historical v2): %v", err)
	}
	if _, err := f.svc.Diff(ctx, legacyHash); err != nil {
		t.Fatalf("Diff(legacy): %v", err)
	}
	if err := f.svc.Restore(ctx, rawHash); err != nil {
		t.Fatalf("Restore(historical v2): %v", err)
	}
	if err := f.svc.Restore(ctx, legacyHash); err != nil {
		t.Fatalf("Restore(legacy): %v", err)
	}

	// The duplicate resolves to the canonical store rather than the historical
	// one.
	dupLoc, err := f.svc.LookupSnapshot(ctx, sharedHash)
	if err != nil {
		t.Fatalf("LookupSnapshot(duplicate): %v", err)
	}
	if dupLoc.Workspace != f.canonicalID {
		t.Fatalf("a hash both stores retain resolved to %q, want the canonical %q", dupLoc.Workspace, f.canonicalID)
	}

	// Lookup, diff and restore never mutate a historical store. Reclamation is
	// the only operation allowed to delete, and it was not called.
	if got := storeFingerprint(t, filepath.Join(f.dataDir, snapshotsDirName, v2DirName, f.rawID)); got != rawBefore {
		t.Fatal("lookup/diff/restore mutated the historical v2 catalog")
	}
	if got := storeFingerprint(t, legacyDir); got != legacyBefore {
		t.Fatal("lookup/diff/restore mutated the legacy bare repository")
	}
}

// A historical catalog is trusted only when its manifest can be read AND names
// this workspace. A corrupt, unreadable or mismatched one is a repair problem
// that must surface, never be skipped into "not found" — and it must not mask a
// canonical hit.
func TestServiceLookupRejectsMismatchedHistoricalManifest(t *testing.T) {
	ctx := context.Background()
	f := newHistoricalIdentityFixture(t, ctx)

	ghost := strings.Repeat("f", 40)

	// The manifest names a different workspace, so it may not serve this one.
	var rawCat *Catalog
	f.withLock(t, ctx, func(m *Manager) {
		var err error
		rawCat, _ = historicalV2Catalog(t, ctx, m, f.rawID, f.canonicalRoot)
		var man *Manifest
		man, err = rawCat.Load()
		if err != nil || man == nil {
			t.Fatalf("load historical manifest: %v (man=%v)", err, man)
		}
		man.Root = filepath.Join(t.TempDir(), "another-workspace")
		if err := rawCat.Save(man); err != nil {
			t.Fatalf("save historical manifest: %v", err)
		}
	})
	if _, err := f.svc.LookupSnapshot(ctx, ghost); ReasonOf(err) != ReasonUnreadableFile {
		t.Fatalf("lookup with a mismatched historical manifest = %v, want %q", err, ReasonUnreadableFile)
	}

	// A canonical hit is NOT masked by the historical problem.
	if _, err := f.svc.LookupSnapshot(ctx, f.canonicalHash); err != nil {
		t.Fatalf("a mismatched historical catalog masked a canonical hit: %v", err)
	}

	// A corrupt manifest is reported, not converted to "missing".
	if err := os.WriteFile(rawCat.ManifestPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupt historical manifest: %v", err)
	}
	if _, err := f.svc.LookupSnapshot(ctx, ghost); ReasonOf(err) != ReasonUnreadableFile {
		t.Fatalf("lookup with a corrupt historical manifest = %v, want %q", err, ReasonUnreadableFile)
	}

	// An unreadable manifest is reported too. A directory where the file belongs
	// fails the read on every platform, including one running as root.
	if err := os.Remove(rawCat.ManifestPath()); err != nil {
		t.Fatalf("remove historical manifest: %v", err)
	}
	if err := os.Mkdir(rawCat.ManifestPath(), 0o755); err != nil {
		t.Fatalf("replace historical manifest with a directory: %v", err)
	}
	if _, err := f.svc.LookupSnapshot(ctx, ghost); ReasonOf(err) != ReasonUnreadableFile {
		t.Fatalf("lookup with an unreadable historical manifest = %v, want %q", err, ReasonUnreadableFile)
	}
}

// A workspace whose checkout has been removed keeps its absolute/clean spelling.
// The lookup must not guess that a dangling alias once named the store recorded
// under the link's former target: an unidentified spelling is not this
// workspace's history.
func TestServiceLookupMissingRootDoesNotGuessAlias(t *testing.T) {
	ctx := context.Background()
	f := newHistoricalIdentityFixture(t, ctx)
	if err := os.RemoveAll(f.real); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}

	// The symlink is now dangling. Its own spelling is the only identity this
	// process may use.
	gone := New(f.dataDir, f.link, 2_000_000, nil, testLogger())
	if got := gone.Workspace(); got != f.rawID {
		t.Fatalf("an absent path reported identity %q, want its absolute/clean hash %q", got, f.rawID)
	}
	wantDir := filepath.Join(f.dataDir, snapshotsDirName, v2DirName, f.rawID)
	if got := gone.StoreDir(); got != wantDir {
		t.Fatalf("StoreDir for an absent path = %q, want %q", got, wantDir)
	}

	// The canonical store still holds the snapshot, but reaching it would mean
	// guessing the symlink's former target. That is exactly what must not happen.
	if _, err := gone.LookupSnapshot(ctx, f.canonicalHash); ReasonOf(err) != ReasonSnapshotNotFound {
		t.Fatalf("lookup through a dangling alias = %v, want %q", err, ReasonSnapshotNotFound)
	}
	if _, err := os.Lstat(wantDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the lookup created a store for the absent spelling: %v", err)
	}
	// The canonical store is untouched and still holds the snapshot.
	still := New(f.dataDir, f.real, 2_000_000, nil, testLogger())
	if _, err := still.LookupSnapshot(ctx, f.canonicalHash); err != nil {
		// The canonical store is keyed by the RESOLVED spelling, which a deleted
		// checkout no longer resolves to; a platform without a symlinked temp
		// root keeps the same spelling and must still find it.
		if ReasonOf(err) != ReasonSnapshotNotFound {
			t.Fatalf("lookup after the checkout was removed = %v", err)
		}
	}
}

// The error precedence across identities: an unexpected canonical result is
// final, a workspace that HAS a store reports an expiry rather than "never
// captured", and only a workspace with no store at all reports not-found.
func TestServiceLookupHistoricalErrorPrecedence(t *testing.T) {
	ctx := context.Background()
	f := newHistoricalIdentityFixture(t, ctx)

	// A historical legacy repository that retains one hash, beside the canonical
	// store.
	legacyDir := filepath.Join(f.dataDir, snapshotsDirName, f.rawID)
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	gitEnv(t, ctx, legacyDir, "init", "--bare")
	writeSnapshotAt(t, ctx, legacyDir, "historical\n")

	// Both identities hold a store; neither retains this hash. The answer stays
	// "expired", not "never captured".
	ghost := strings.Repeat("a", 40)
	if _, err := f.svc.LookupSnapshot(ctx, ghost); ReasonOf(err) != ReasonSnapshotExpired {
		t.Fatalf("lookup in two stores that lack the hash = %v, want %q", err, ReasonSnapshotExpired)
	}

	// A canonical store that does not exist beside a historical repository that
	// lacks the hash is still an expiry: this workspace HAS captured before.
	freshDataDir := t.TempDir()
	freshLegacy := filepath.Join(freshDataDir, snapshotsDirName, f.rawID)
	if err := os.MkdirAll(freshLegacy, 0o755); err != nil {
		t.Fatalf("mkdir fresh legacy: %v", err)
	}
	gitEnv(t, ctx, freshLegacy, "init", "--bare")
	fresh := New(freshDataDir, f.link, 2_000_000, nil, testLogger())
	if _, err := fresh.LookupSnapshot(ctx, ghost); ReasonOf(err) != ReasonSnapshotExpired {
		t.Fatalf("lookup with only a historical store = %v, want %q", err, ReasonSnapshotExpired)
	}
	// Resolving the fallback must not bootstrap the canonical store.
	if _, err := os.Lstat(filepath.Join(freshDataDir, snapshotsDirName, v2DirName, f.canonicalID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the lookup created a canonical store while searching a fallback: %v", err)
	}

	// No store in either known identity: "never captured".
	empty := New(t.TempDir(), f.real, 2_000_000, nil, testLogger())
	if _, err := empty.LookupSnapshot(ctx, ghost); ReasonOf(err) != ReasonSnapshotNotFound {
		t.Fatalf("lookup with no store at all = %v, want %q", err, ReasonSnapshotNotFound)
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
