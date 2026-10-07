package snapshot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// requireGit skips only when Git is genuinely unavailable, which is the repo's
// existing convention. It never skips an assertion: every test below depends on
// real Git behaviour (unborn HEAD, ref reachability, no reflog), so a stand-in
// would prove nothing.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// testCatalog builds a manager rooted under a fresh temp directory and returns
// an owned catalog for a workspace root under that same temp directory.
//
// The workspace root lives inside the temp tree so nothing can reach the real
// ~/.local/share/marshal.
func testCatalog(t *testing.T, opts ...ManagerOption) (*Manager, *Catalog, context.Context) {
	t.Helper()
	dataDir := t.TempDir()
	workspace := filepath.Join(dataDir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	m, err := NewManager(dataDir, opts...)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = m.Release() })

	cat, err := m.CatalogForRoot(workspace)
	if err != nil {
		t.Fatalf("CatalogForRoot: %v", err)
	}
	return m, cat, context.Background()
}

// gitEnv runs a git command against dir using the package's own sanitized
// invocation, so tests exercise the same configuration pins production does.
func gitEnv(t *testing.T, ctx context.Context, dir string, args ...string) string {
	t.Helper()
	out, err := runGitCombined(ctx, newGitCmd(ctx, "git", dir, "", false, args...), defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitEnvErr(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := runGitCombined(ctx, newGitCmd(ctx, "git", dir, "", true, args...), defaultMaxCommandOutput)
	return strings.TrimSpace(string(out)), err
}

// writeSnapshotInto creates a blob, tree, and parentless commit in a generation
// and publishes refs/snapshots/<commit>, exactly as a capture would. It returns
// the commit hash.
func writeSnapshotInto(t *testing.T, ctx context.Context, dir string) string {
	t.Helper()
	// hash-object reads its content from stdin, so it is built by hand rather
	// than through gitEnv.
	cmd := newGitCmd(ctx, "git", dir, "", false, "hash-object", "-w", "-t", "blob", "--stdin")
	cmd.Stdin = strings.NewReader("snapshot-content\n")
	out, err := runGitCombined(ctx, cmd, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("hash-object: %v\n%s", err, out)
	}
	blobHash := strings.TrimSpace(string(out))

	treeCmd := newGitCmd(ctx, "git", dir, "", false, "mktree")
	treeCmd.Stdin = strings.NewReader("100644 blob " + blobHash + "\ta.txt\n")
	out, err = runGitCombined(ctx, treeCmd, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("mktree: %v\n%s", err, out)
	}
	treeHash := strings.TrimSpace(string(out))

	commitCmd := newGitCmd(ctx, "git", dir, "", false, "commit-tree", treeHash)
	commitCmd.Stdin = strings.NewReader("snapshot\n")
	commitCmd.Env = append(commitCmd.Env,
		"GIT_AUTHOR_NAME=marshal", "GIT_AUTHOR_EMAIL=marshal@local",
		"GIT_COMMITTER_NAME=marshal", "GIT_COMMITTER_EMAIL=marshal@local",
	)
	out, err = runGitCombined(ctx, commitCmd, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("commit-tree: %v\n%s", err, out)
	}
	commitHash := strings.TrimSpace(string(out))

	// A publication ref is the only thing that makes the objects durable.
	gitEnv(t, ctx, dir, "update-ref", commitRef(commitHash), commitHash)
	return commitHash
}

// commitRef is the publication ref for a snapshot commit hash.
func commitRef(hash string) string { return snapshotRefPrefix + hash }

// ---------------------------------------------------------------------------
// Generation identifiers and layout
// ---------------------------------------------------------------------------

// A generation identifier must be a sortable creation sequence plus a random
// suffix, and must NOT be derived from the workspace filesystem path. A
// path-derived identifier would make two workspaces' generations collide after
// a rename and would leak the user's directory names into the store.
func TestGenerationIDsAreSortableAndNotPathDerived(t *testing.T) {
	requireGit(t)
	_, cat, ctx := testCatalog(t)

	var seen []string
	for i := 0; i < 12; i++ {
		rec, err := cat.CreateGeneration(ctx, "")
		if err != nil {
			t.Fatalf("CreateGeneration: %v", err)
		}
		seen = append(seen, rec.ID)
		if !validGenerationID(rec.ID) {
			t.Fatalf("generation ID %q is not a valid safe identifier", rec.ID)
		}
		// The identifier must not contain any part of the workspace path: the
		// sequence and a random suffix only.
		if strings.Contains(rec.ID, filepath.Base(cat.WorkspaceRoot)) {
			t.Fatalf("generation ID %q leaks the workspace name", rec.ID)
		}
	}

	// Lexical order must equal creation order, which is what makes oldest-first
	// reclamation a plain sort.
	sorted := append([]string(nil), seen...)
	sortStrings(sorted)
	for i := range seen {
		if seen[i] != sorted[i] {
			t.Fatalf("creation order %v does not match lexical order %v", seen, sorted)
		}
	}
	// Identifiers must be distinct even when created in the same nanosecond.
	unique := make(map[string]bool)
	for _, id := range seen {
		if unique[id] {
			t.Fatalf("duplicate generation ID %q", id)
		}
		unique[id] = true
	}

	// The sequence must be parseable and monotonically non-decreasing.
	var last uint64
	for _, id := range seen {
		seq, _, ok := parseGenerationID(id)
		if !ok {
			t.Fatalf("generation ID %q does not parse", id)
		}
		if seq < last {
			t.Fatalf("sequence went backwards: %d after %d", seq, last)
		}
		last = seq
	}
}

func TestGenerationIDShape(t *testing.T) {
	// A fixed-width decimal sequence and a hex suffix.
	if !validGenerationID("00000000000000000007-0a1b2c3d") {
		t.Fatal("a canonical generation ID should validate")
	}
	for _, bad := range []string{
		"",
		"..",
		"../escape",
		"00000000000000000007-0a1b2c3d/../x",
		"0000000000000000007-0a1b2c3d",   // too short
		"00000000000000000007_0a1b2c3d",  // wrong separator
		"0000000000000000000x-0a1b2c3d",  // non-decimal sequence
		"00000000000000000007-0a1b2c3z",  // non-hex suffix
		"00000000000000000007-0a1b2c3d ", // trailing space
	} {
		if validGenerationID(bad) {
			t.Errorf("validGenerationID(%q) = true, want false", bad)
		}
	}
	if validStagingID("stage-short") {
		t.Error("a short staging identifier should not validate")
	}
	if !validStagingID("stage-0123456789abcdef") {
		t.Error("a canonical staging identifier should validate")
	}
	if validWorkspaceID("../escape") {
		t.Error("a workspace identifier must be a single safe path element")
	}
}

// ---------------------------------------------------------------------------
// Minimal bare repositories
// ---------------------------------------------------------------------------

// A generation repository must be a real bare repository whose HEAD is an
// UNBORN branch. If HEAD ever named a commit, that commit's whole parent chain
// would be reachable through the branch and would survive deleting every
// snapshot ref — the defect that kept 71 GiB of "deleted" history alive.
func TestGenerationRepoHeadIsUnbornAndUnreferenced(t *testing.T) {
	requireGit(t)
	_, cat, ctx := testCatalog(t)

	rec, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	dir, err := cat.GenerationDir(rec.ID)
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}

	if got := gitEnv(t, ctx, dir, "rev-parse", "--is-bare-repository"); got != "true" {
		t.Fatalf("rev-parse --is-bare-repository = %q, want true", got)
	}
	if got := gitEnv(t, ctx, dir, "symbolic-ref", "-q", "HEAD"); got != generationHeadTarget {
		t.Fatalf("HEAD = %q, want the unborn branch %q", got, generationHeadTarget)
	}
	// HEAD must not resolve to a commit.
	if _, err := gitEnvErr(ctx, dir, "rev-parse", "--verify", "-q", "HEAD"); err == nil {
		t.Fatal("HEAD resolved to a commit; it must be an unborn branch")
	}
	// No branch may exist, and no reflog may exist.
	if refs := gitEnv(t, ctx, dir, "for-each-ref", "refs/heads/"); refs != "" {
		t.Fatalf("refs/heads is not empty: %q", refs)
	}
	if _, err := os.Lstat(filepath.Join(dir, "logs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a reflog directory exists (%v); a reflog entry is a retention root", err)
	}
	// git itself must accept the repository.
	if out, err := gitEnvErr(ctx, dir, "fsck"); err != nil {
		t.Fatalf("git fsck rejected the generation repo: %v\n%s", err, out)
	}
}

// Auto-maintenance must be disabled in the repository itself, not merely pinned
// on command lines: a repack started by a process that forgot the pins is how
// abandoned tmp_pack_* files accumulated.
func TestGenerationRepoDisablesAutoMaintenance(t *testing.T) {
	requireGit(t)
	_, cat, ctx := testCatalog(t)
	rec, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	dir, _ := cat.GenerationDir(rec.ID)

	for key, want := range map[string]string{
		"gc.auto":               "0",
		"gc.autoDetach":         "false",
		"maintenance.auto":      "false",
		"core.bare":             "true",
		"core.logAllRefUpdates": "false",
	} {
		got := gitEnv(t, ctx, dir, "config", "--get", key)
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// The ONLY retention root may be refs/snapshots/<hash>. Deleting every snapshot
// ref must leave nothing reachable, which is the property the whole layout
// exists to provide.
func TestSnapshotObjectsAreRetainedOnlyBySnapshotRefs(t *testing.T) {
	requireGit(t)
	_, cat, ctx := testCatalog(t)
	rec, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	dir, _ := cat.GenerationDir(rec.ID)

	commit := writeSnapshotInto(t, ctx, dir)
	// The helper publishes refs/snapshots/<commit>; assert it is the only ref.
	ref := commitRef(commit)
	if refs := gitEnv(t, ctx, dir, "for-each-ref", "--format=%(refname)"); refs != ref {
		t.Fatalf("refs = %q, want only %q", refs, ref)
	}
	if n := gitEnv(t, ctx, dir, "rev-list", "--all", "--count"); n != "1" {
		t.Fatalf("reachable commits = %q, want 1", n)
	}

	// Deleting the snapshot ref must leave NOTHING reachable: no branch, no
	// reflog, no HEAD commit retaining it.
	gitEnv(t, ctx, dir, "update-ref", "-d", ref)
	if n := gitEnv(t, ctx, dir, "rev-list", "--all", "--count"); n != "0" {
		t.Fatalf("reachable commits after deleting the snapshot ref = %q, want 0: a branch or reflog is retaining them", n)
	}
	if refs := gitEnv(t, ctx, dir, "for-each-ref", "--format=%(refname)"); refs != "" {
		t.Fatalf("refs after deletion = %q, want none", refs)
	}
}

// ---------------------------------------------------------------------------
// Manifest and atomic replacement
// ---------------------------------------------------------------------------

func TestManifestRoundTripAndValidation(t *testing.T) {
	_, cat, ctx := testCatalog(t)

	rec, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	if err := cat.ActivateGeneration(rec.ID); err != nil {
		t.Fatalf("ActivateGeneration: %v", err)
	}
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if man.ActiveID != rec.ID {
		t.Fatalf("ActiveID = %q, want %q", man.ActiveID, rec.ID)
	}
	if got := man.Active(); got == nil || got.State != GenerationActive {
		t.Fatalf("Active() = %+v, want the active record", got)
	}
	if man.Root != cat.WorkspaceRoot {
		t.Fatalf("manifest Root = %q, want the canonical workspace root %q", man.Root, cat.WorkspaceRoot)
	}
	if man.Version != LayoutVersion {
		t.Fatalf("manifest Version = %d, want %d", man.Version, LayoutVersion)
	}
	// A manifest that is not in the store's own directory must not be written.
	if !strings.HasPrefix(cat.ManifestPath(), cat.Root()) {
		t.Fatalf("manifest path %q is outside the catalog %q", cat.ManifestPath(), cat.Root())
	}
}

// A manifest that is present but unreadable must be REFUSED, not replaced: the
// whole point of quarantining is to avoid guessing at state that still exists.
func TestCorruptManifestIsRefusedNotReplaced(t *testing.T) {
	_, cat, ctx := testCatalog(t)
	if _, err := cat.CreateGeneration(ctx, ""); err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	original, err := os.ReadFile(cat.ManifestPath())
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	for _, tt := range []struct {
		name string
		data string
	}{
		{"truncated JSON", `{"version":1,"workspace":`},
		{"not JSON", "this is not a manifest"},
		{"trailing garbage", string(original) + "\n{\"second\":true}"},
		{"unsupported version", `{"version":99,"workspace":"abcdefabcdef"}`},
		{"bad workspace id", `{"version":1,"workspace":"../escape"}`},
		{"two active generations", `{"version":1,"workspace":"abcdefabcdef","active_id":"00000000000000000001-aaaaaaaa","generations":[` +
			`{"id":"00000000000000000001-aaaaaaaa","state":"active","origin":"native"},` +
			`{"id":"00000000000000000002-bbbbbbbb","state":"active","origin":"native"}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(cat.ManifestPath(), []byte(tt.data), 0o644); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			if _, err := cat.Load(); err == nil {
				t.Fatal("Load accepted a corrupt manifest")
			}
			// The file must still be there, byte for byte: refusing must not
			// destroy the evidence a recovery workflow needs.
			got, err := os.ReadFile(cat.ManifestPath())
			if err != nil {
				t.Fatalf("corrupt manifest was removed: %v", err)
			}
			if string(got) != tt.data {
				t.Fatal("corrupt manifest was rewritten")
			}
		})
	}
}

// An absent manifest is not an error: a catalog that was never written must be
// distinguishable from one whose manifest is broken.
func TestAbsentManifestIsNotAnError(t *testing.T) {
	_, cat, _ := testCatalog(t)
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load on an absent manifest: %v", err)
	}
	if man != nil {
		t.Fatalf("Load = %+v, want nil for an absent manifest", man)
	}
}

// The atomic replace must leave either the old or the new manifest, never a
// partial one, and must clean up its scratch file.
func TestManifestReplaceIsAtomicAndCleansScratch(t *testing.T) {
	_, cat, ctx := testCatalog(t)

	first, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	if err := cat.ActivateGeneration(first.ID); err != nil {
		t.Fatalf("ActivateGeneration: %v", err)
	}
	afterFirst, err := os.ReadFile(cat.ManifestPath())
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	// A crashed replace leaves the scratch file behind and the OLD manifest
	// intact. That is exactly the state reconciliation must be able to clean.
	crashErr := errors.New("simulated crash before rename")
	_, cat2, ctx2 := testCatalog(t, WithHooks(Hooks{
		BeforeManifestRename: func(string) error { return crashErr },
	}))
	if _, err := cat2.CreateGeneration(ctx2, ""); !errors.Is(err, crashErr) {
		t.Fatalf("CreateGeneration error = %v, want the injected crash", err)
	}
	if _, err := cat2.Load(); err != nil {
		t.Fatalf("manifest was damaged by an interrupted replace: %v", err)
	}
	scratch := manifestScratchFiles(t, cat2.Root())
	if len(scratch) == 0 {
		t.Fatal("an interrupted manifest replace left no scratch file to reconcile")
	}

	// A completed replace must leave no scratch file at all.
	if got := manifestScratchFiles(t, cat.Root()); len(got) != 0 {
		t.Fatalf("a completed manifest replace left scratch files: %v", got)
	}
	if got, err := os.ReadFile(cat.ManifestPath()); err != nil || string(got) != string(afterFirst) {
		t.Fatalf("manifest changed without a write: %v", err)
	}
}

func manifestScratchFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), manifestTempPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// The manifest replacement estimate must include BOTH the old and the new file,
// because an atomic replace holds both at once.
func TestManifestReplacementBytesReservesBothFiles(t *testing.T) {
	_, cat, _ := testCatalog(t)
	got, err := cat.ManifestReplacementBytes()
	if err != nil {
		t.Fatalf("ManifestReplacementBytes: %v", err)
	}
	unit := int64(DefaultAllocationUnitBytes)
	want := int64(2) * roundUpForTest(t, ManifestMaxBytes, unit)
	if got != want {
		t.Fatalf("ManifestReplacementBytes = %d, want %d (two bounded files)", got, want)
	}
}

func roundUpForTest(t *testing.T, n, unit int64) int64 {
	t.Helper()
	got, err := roundUpAllocation(n, unit)
	if err != nil {
		t.Fatalf("roundUpAllocation: %v", err)
	}
	return got
}

// ---------------------------------------------------------------------------
// Lifecycle and protection
// ---------------------------------------------------------------------------

func TestGenerationLifecycleTransitions(t *testing.T) {
	_, cat, ctx := testCatalog(t)
	first, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	if first.State != GenerationCreating {
		t.Fatalf("new generation state = %q, want %q", first.State, GenerationCreating)
	}
	if err := cat.ActivateGeneration(first.ID); err != nil {
		t.Fatalf("ActivateGeneration: %v", err)
	}

	// Activating a second generation must seal the first in the SAME write: a
	// workspace never transiently has two active generations.
	second, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	if err := cat.ActivateGeneration(second.ID); err != nil {
		t.Fatalf("ActivateGeneration: %v", err)
	}
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if man.ActiveID != second.ID {
		t.Fatalf("ActiveID = %q, want %q", man.ActiveID, second.ID)
	}
	if rec := man.Generation(first.ID); rec == nil || rec.State != GenerationSealed {
		t.Fatalf("previous generation state = %+v, want sealed", rec)
	}
	if err := man.Validate(); err != nil {
		t.Fatalf("manifest with one active generation failed validation: %v", err)
	}

	// Sealing the active generation clears the pointer.
	if err := cat.SealGeneration(second.ID); err != nil {
		t.Fatalf("SealGeneration: %v", err)
	}
	man, _ = cat.Load()
	if man.ActiveID != "" || man.Active() != nil {
		t.Fatalf("ActiveID = %q after sealing the active generation, want empty", man.ActiveID)
	}
	if len(man.Sealed()) != 2 {
		t.Fatalf("Sealed() = %d records, want 2", len(man.Sealed()))
	}
}

// The protection flag must be persisted AND surfaced by a predicate, because
// migrated legacy history is the only surviving copy of rollback data. A
// generation marked deleting must also be unusable: reusing one would resurrect
// storage already scheduled to disappear.
func TestProtectionFlagAndUsability(t *testing.T) {
	_, cat, ctx := testCatalog(t)

	native, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	if err := cat.ActivateGeneration(native.ID); err != nil {
		t.Fatalf("ActivateGeneration: %v", err)
	}

	// Persist a legacy-origin protected generation directly, the way a
	// migration task will.
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	legacyID := formatGenerationID(999, "deadbeef")
	man.Generations = append(man.Generations, GenerationRecord{
		ID:        legacyID,
		State:     GenerationSealed,
		Origin:    OriginLegacy,
		Root:      cat.WorkspaceRoot,
		CreatedAt: time.Now().UTC(),
	})
	if err := cat.Save(man); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reloaded.IsProtected(legacyID) {
		t.Fatal("a legacy-origin generation is not reported as protected")
	}
	if reloaded.IsProtected(native.ID) {
		t.Fatal("a native generation was reported as protected")
	}
	// Even without the persisted flag, the origin makes it protected: a
	// manifest written before the flag existed must not become reclaimable.
	rec := reloaded.Generation(legacyID)
	rec.Protected = false
	if !rec.IsProtected() {
		t.Fatal("clearing the flag made a legacy-origin generation reclaimable")
	}

	// Reclaiming a protected generation must be refused outright.
	if _, err := cat.MarkDeleting(legacyID); !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("MarkDeleting(protected) error = %v, want ErrLegacyRecoveryRequired", err)
	}

	// A generation being deleted is never usable and never revivable.
	if _, err := cat.MarkDeleting(native.ID); err != nil {
		t.Fatalf("MarkDeleting: %v", err)
	}
	if GenerationDeleting.Usable() {
		t.Fatal("a deleting generation reports itself as usable")
	}
	if err := cat.ActivateGeneration(native.ID); err == nil {
		t.Fatal("a deleting generation was activated")
	}
	if err := cat.SealGeneration(native.ID); err == nil {
		t.Fatal("a deleting generation was sealed")
	}
	if _, err := cat.ReserveStaging(ctx, native.ID); err == nil {
		t.Fatal("a deleting generation accepted a capture")
	}
}

// ---------------------------------------------------------------------------
// Capture state
// ---------------------------------------------------------------------------

// Capture state must record the staging identifier and the intended publication
// ref and hash, so an interruption is recoverable in both directions.
func TestCaptureStateRecordsStagingAndPublication(t *testing.T) {
	_, cat, ctx := testCatalog(t)
	rec, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	if err := cat.ActivateGeneration(rec.ID); err != nil {
		t.Fatalf("ActivateGeneration: %v", err)
	}
	cs, err := cat.ReserveStaging(ctx, rec.ID)
	if err != nil {
		t.Fatalf("ReserveStaging: %v", err)
	}
	if cs.StagingID == "" || !validStagingID(cs.StagingID) {
		t.Fatalf("StagingID = %q, want a valid staging identifier", cs.StagingID)
	}
	if cs.GenerationID != rec.ID {
		t.Fatalf("GenerationID = %q, want %q", cs.GenerationID, rec.ID)
	}
	path, err := cat.StagingPath(cs.StagingID)
	if err != nil {
		t.Fatalf("StagingPath: %v", err)
	}
	if info, err := os.Lstat(path); err != nil || !info.IsDir() {
		t.Fatalf("staging directory %s missing: %v", path, err)
	}
	// The record must be durable, not merely in memory.
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if man.Capture == nil || man.Capture.StagingID != cs.StagingID {
		t.Fatalf("capture record was not persisted: %+v", man.Capture)
	}
	// A second capture must not start while one is in flight.
	if _, err := cat.ReserveStaging(ctx, rec.ID); !errors.Is(err, ErrInterruptedCapture) {
		t.Fatalf("second ReserveStaging error = %v, want ErrInterruptedCapture", err)
	}
	// Clearing removes both the record and the scratch directory.
	if err := cat.ClearCapture(); err != nil {
		t.Fatalf("ClearCapture: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging directory survived ClearCapture: %v", err)
	}
	man, _ = cat.Load()
	if man.Capture != nil {
		t.Fatalf("capture record survived ClearCapture: %+v", man.Capture)
	}
	// ClearCapture is idempotent.
	if err := cat.ClearCapture(); err != nil {
		t.Fatalf("second ClearCapture: %v", err)
	}
}

// A capture record can only name the publication ref of its own hash. A record
// whose ref and hash disagree would let reconciliation look up the wrong ref and
// conclude an unpublished capture was published.
func TestCaptureStateRefMustMatchHash(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef01234567"
	cs := &CaptureState{
		StagingID:    "stage-0123456789abcdef",
		GenerationID: "00000000000000000001-aaaaaaaa",
		Hash:         hash,
		Ref:          snapshotRefPrefix + strings.Repeat("f", 40),
	}
	if err := cs.Validate(nil); err == nil {
		t.Fatal("a capture record whose ref does not match its hash was accepted")
	}
	cs.Ref = snapshotRefPrefix + hash
	if err := cs.Validate(nil); err != nil {
		t.Fatalf("a consistent capture record was rejected: %v", err)
	}
	// Publication intent is recorded progressively, so both-empty is legal and
	// means "no intent yet".
	cs.Hash, cs.Ref = "", ""
	if err := cs.Validate(nil); err != nil {
		t.Fatalf("a capture with no publication intent yet was rejected: %v", err)
	}
	// Setting only one of the pair is not legal: it would describe a ref that
	// does not correspond to a hash.
	cs.Hash = hash
	if err := cs.Validate(nil); err == nil {
		t.Fatal("a capture with a hash but no ref was accepted")
	}
	cs.Hash, cs.Ref = "", snapshotRefPrefix+hash
	if err := cs.Validate(nil); err == nil {
		t.Fatal("a capture with a ref but no hash was accepted")
	}
	// An unvalidatable staging identifier must be refused before any filesystem
	// call could use it.
	cs.Hash, cs.Ref = "", ""
	cs.StagingID = "../../../etc"
	if err := cs.Validate(nil); err == nil {
		t.Fatal("a capture record with an escaping staging identifier was accepted")
	}
}

// Publication intent must be durable BEFORE the ref is written, and must name
// the ref belonging to the recorded hash. A crash between the two must leave a
// record that says which ref to look for.
func TestSetPublicationIntentIsDurableAndConsistent(t *testing.T) {
	_, cat, ctx := testCatalog(t)
	rec, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	if err := cat.ActivateGeneration(rec.ID); err != nil {
		t.Fatalf("ActivateGeneration: %v", err)
	}
	cs, err := cat.ReserveStaging(ctx, rec.ID)
	if err != nil {
		t.Fatalf("ReserveStaging: %v", err)
	}
	// A freshly reserved capture carries no publication intent yet, and that is
	// the only state in which the ref is not determined.
	if cs.Hash != "" || cs.Ref != "" {
		t.Fatalf("a freshly reserved capture already claims publication: %+v", cs)
	}

	const hash = "0123456789abcdef0123456789abcdef01234567"
	if _, err := cat.SetPublicationIntent("stage-0000000000000000", hash); err == nil {
		t.Fatal("SetPublicationIntent accepted a staging identifier that is not in flight")
	}
	intent, err := cat.SetPublicationIntent(cs.StagingID, hash)
	if err != nil {
		t.Fatalf("SetPublicationIntent: %v", err)
	}
	if intent.Ref != snapshotRefPrefix+hash {
		t.Fatalf("intent ref = %q, want %q", intent.Ref, snapshotRefPrefix+hash)
	}
	// It must be persisted, not just returned.
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if man.Capture == nil || man.Capture.Ref != intent.Ref || man.Capture.Hash != hash {
		t.Fatalf("publication intent was not persisted: %+v", man.Capture)
	}
	// An uppercase hash must be normalized, so it cannot name a second ref for
	// the same object.
	intent, err = cat.SetPublicationIntent(cs.StagingID, strings.ToUpper(hash))
	if err != nil {
		t.Fatalf("SetPublicationIntent with an uppercase hash: %v", err)
	}
	if intent.Hash != hash {
		t.Fatalf("hash = %q, want it lower-cased to %q", intent.Hash, hash)
	}
	// A non-hash must be refused before it reaches a ref name.
	if _, err := cat.SetPublicationIntent(cs.StagingID, "HEAD"); err == nil {
		t.Fatal("SetPublicationIntent accepted a Git revision expression as a hash")
	}
}

// ---------------------------------------------------------------------------
// Ownership and containment
// ---------------------------------------------------------------------------

// Every mutating catalog operation must refuse to run without the store lock.
// Without that, two processes could reconcile and capture concurrently.
func TestCatalogMutationsRequireOwnership(t *testing.T) {
	requireGit(t)
	dataDir := t.TempDir()
	workspace := filepath.Join(dataDir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	cat, err := m.CatalogForRoot(workspace)
	if err != nil {
		t.Fatalf("CatalogForRoot: %v", err)
	}
	ctx := context.Background()
	const id = "00000000000000000001-aaaaaaaa"

	for name, fn := range map[string]func() error{
		"CreateGeneration": func() error { _, err := cat.CreateGeneration(ctx, ""); return err },
		"InitGenerationRepo": func() error {
			return cat.InitGenerationRepo(ctx, id)
		},
		"Save":               func() error { return cat.Save(&Manifest{Version: LayoutVersion, Workspace: cat.Workspace()}) },
		"ActivateGeneration": func() error { return cat.ActivateGeneration(id) },
		"SealGeneration":     func() error { return cat.SealGeneration(id) },
		"MarkDeleting": func() error {
			_, err := cat.MarkDeleting(id)
			return err
		},
		"RemoveGenerationDirectory": func() error {
			return cat.RemoveGenerationDirectory(id)
		},
		"ForgetGeneration": func() error { return cat.ForgetGeneration(id) },
		"ReserveStaging": func() error {
			_, err := cat.ReserveStaging(ctx, id)
			return err
		},
		"ClearCapture": func() error { return cat.ClearCapture() },
	} {
		if err := fn(); !errors.Is(err, ErrNotOwned) {
			t.Errorf("%s without the store lock = %v, want ErrNotOwned", name, err)
		}
	}
	// Reconciliation must refuse too.
	if _, err := m.Reconcile(ctx); !errors.Is(err, ErrNotOwned) {
		t.Errorf("Reconcile without the store lock = %v, want ErrNotOwned", err)
	}
	if _, err := m.ReconcileWorkspace(ctx, cat.Workspace()); !errors.Is(err, ErrNotOwned) {
		t.Errorf("ReconcileWorkspace without the store lock = %v, want ErrNotOwned", err)
	}
}

// A generation or staging identifier must never escape the catalog directory,
// even if a corrupt manifest names one.
func TestCatalogPathsAreContained(t *testing.T) {
	_, cat, _ := testCatalog(t)
	for _, bad := range []string{"", ".", "..", "../../escape", "a/b", `a\b`, "00000000000000000001-aaaaaaaa/../x"} {
		if _, err := cat.GenerationDir(bad); err == nil {
			t.Errorf("GenerationDir(%q) succeeded, want a refusal", bad)
		}
		if _, err := cat.StagingPath(bad); err == nil {
			t.Errorf("StagingPath(%q) succeeded, want a refusal", bad)
		}
	}
	good, err := cat.GenerationDir("00000000000000000001-aaaaaaaa")
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	if filepath.Dir(good) != cat.GenerationsDir() {
		t.Fatalf("GenerationDir = %q, want a child of %q", good, cat.GenerationsDir())
	}
	if rel, err := filepath.Rel(cat.Root(), good); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("generation path %q is outside the catalog %q", good, cat.Root())
	}
}

// ---------------------------------------------------------------------------
// Fault injection: every generation-creation boundary
// ---------------------------------------------------------------------------

// Each hook sits on a named crash boundary. A restart at that boundary must
// leave the store in a state reconciliation can finish, and must never leave a
// generation that looks usable without a published snapshot behind it.
func TestGenerationCreationFaultBoundaries(t *testing.T) {
	requireGit(t)

	boundaries := []struct {
		name      string
		hook      func(marker error) Hooks
		wantDir   bool
		wantState GenerationState
	}{
		{
			name: "after the creating record, before the repository",
			hook: func(e error) Hooks {
				return Hooks{AfterGenerationRecord: func() error { return e }}
			},
			wantDir:   false,
			wantState: GenerationCreating,
		},
		{
			name: "after the repository metadata, still creating",
			hook: func(e error) Hooks {
				return Hooks{AfterInitGenerationRepo: func() error { return e }}
			},
			wantDir:   true,
			wantState: GenerationCreating,
		},
	}

	for _, b := range boundaries {
		t.Run(b.name, func(t *testing.T) {
			_, cat, ctx := testCatalog(t, WithHooks(b.hook(errInjected)))

			_, err := cat.CreateGeneration(ctx, "")
			if !errors.Is(err, errInjected) {
				t.Fatalf("CreateGeneration error = %v, want the injected interruption", err)
			}
			// The record must be durable, so reconciliation can see what was
			// being attempted.
			man, err := cat.Load()
			if err != nil {
				t.Fatalf("Load after an interruption: %v", err)
			}
			if man == nil || len(man.Generations) != 1 {
				t.Fatalf("generations after an interruption = %+v, want exactly one creating record", man)
			}
			rec := man.Generations[0]
			if rec.State != b.wantState {
				t.Fatalf("state = %q, want %q", rec.State, b.wantState)
			}
			dir, _ := cat.GenerationDir(rec.ID)
			_, statErr := os.Lstat(dir)
			if b.wantDir && statErr != nil {
				t.Fatalf("generation directory should exist at this boundary: %v", statErr)
			}
			if !b.wantDir && statErr == nil {
				t.Fatal("generation directory should not exist at this boundary")
			}
			// Nothing may be published at any of these boundaries.
			if b.wantDir {
				hasRefs, err := catHasSnapshotRefs(ctx, cat, rec.ID)
				if err != nil {
					t.Fatalf("catHasSnapshotRefs: %v", err)
				}
				if hasRefs {
					t.Fatal("an interrupted creation published a snapshot ref")
				}
			}
		})
	}
}

// A create interrupted at either boundary must be reconciled to nothing:
// removing a never-published generation loses no snapshot, and leaving a
// `creating` record behind would strand a generation nobody can use.
func TestReconcileRemovesInterruptedCreation(t *testing.T) {
	requireGit(t)

	for _, tc := range []struct {
		name  string
		hooks Hooks
	}{
		{name: "record only", hooks: Hooks{AfterGenerationRecord: func() error { return errInjected }}},
		{name: "record and repository", hooks: Hooks{AfterInitGenerationRepo: func() error { return errInjected }}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cat, ctx := testCatalog(t, WithHooks(tc.hooks))
			_, err := cat.CreateGeneration(ctx, "")
			if !errors.Is(err, errInjected) {
				t.Fatalf("CreateGeneration error = %v, want the injected interruption", err)
			}

			// Restart: reconciliation sees only durable state.
			report, err := cat.manager.Reconcile(ctx)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			out := report.Workspace(cat.Workspace())
			if out == nil {
				t.Fatal("reconciliation did not report the workspace")
			}
			if len(out.GenerationsForgotten) != 1 {
				t.Fatalf("GenerationsForgotten = %v, want the interrupted generation", out.GenerationsForgotten)
			}
			man, err := cat.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(man.Generations) != 0 {
				t.Fatalf("generations after reconciliation = %+v, want none", man.Generations)
			}
			if man.ActiveID != "" {
				t.Fatalf("ActiveID = %q after removing an interrupted creation, want empty", man.ActiveID)
			}
			// Reconciliation must be idempotent.
			if _, err := cat.manager.Reconcile(ctx); err != nil {
				t.Fatalf("second Reconcile: %v", err)
			}
		})
	}
}

var errInjected = errors.New("simulated interruption")

// ---------------------------------------------------------------------------
// Randomness seam
// ---------------------------------------------------------------------------

// A failure to generate an identifier must be reported, never papered over with
// a predictable or colliding one.
func TestGenerationIDGenerationFailsClosed(t *testing.T) {
	_, cat, ctx := testCatalog(t)
	original := randRead
	randRead = func([]byte) (int, error) { return 0, errors.New("simulated entropy failure") }
	t.Cleanup(func() { randRead = original })

	if _, err := cat.CreateGeneration(ctx, ""); err == nil {
		t.Fatal("CreateGeneration succeeded despite an entropy failure")
	}
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if man != nil && len(man.Generations) != 0 {
		t.Fatalf("a generation record was written after an entropy failure: %+v", man.Generations)
	}
}

// A generation identifier must never be produced by a predictable source: with
// two processes running concurrently a collision would make two generations
// share one directory.
func TestGenerationIDUniquenessUnderConcurrency(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 2000; i++ {
		suffix, err := randomSuffix(generationIDSuffixLen)
		if err != nil {
			t.Fatalf("randomSuffix: %v", err)
		}
		if !regexp.MustCompile(`^[0-9a-f]+$`).MatchString(suffix) {
			t.Fatalf("suffix %q is not lowercase hex", suffix)
		}
		id := formatGenerationID(uint64(i), suffix)
		if seen[id] {
			t.Fatalf("duplicate identifier %q", id)
		}
		seen[id] = true
	}
}
