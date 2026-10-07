package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// publishSnapshot writes a real snapshot into a generation and publishes its
// ref, returning the commit hash. It is the "capture succeeded" state
// reconciliation must always preserve.
func publishSnapshot(t *testing.T, ctx context.Context, cat *Catalog, id string) string {
	t.Helper()
	dir, err := cat.GenerationDir(id)
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	commit := writeSnapshotInto(t, ctx, dir)
	ref, err := cat.PublishSnapshotRef(ctx, id, commit)
	if err != nil {
		t.Fatalf("PublishSnapshotRef: %v", err)
	}
	if want := snapshotRefPrefix + commit; ref != want {
		t.Fatalf("published ref = %q, want %q", ref, want)
	}
	return commit
}

// activeGeneration creates and activates a generation, returning its record.
func activeGeneration(t *testing.T, ctx context.Context, cat *Catalog) *GenerationRecord {
	t.Helper()
	rec, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	if err := cat.ActivateGeneration(rec.ID); err != nil {
		t.Fatalf("ActivateGeneration: %v", err)
	}
	return rec
}

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

// Discovery must find every workspace and the legacy stores, even when the
// corresponding workspace path no longer exists on disk. A store whose checkout
// was deleted is exactly when the user needs it reported, not forgotten.
func TestDiscoverFindsV2AndLegacyStoresWhoseCheckoutIsGone(t *testing.T) {
	requireGit(t)
	m, cat, ctx := testCatalog(t)

	// A live v2 workspace with a generation.
	rec := activeGeneration(t, ctx, cat)
	hash := publishSnapshot(t, ctx, cat, rec.ID)

	// A legacy bare repo whose workspace path does not exist.
	legacyHash := WorkspaceHashFor(filepath.Join(m.DataDir(), "gone-workspace"))
	legacyDir := filepath.Join(m.Root(), legacyHash)
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	init := newGitCmd(ctx, "git", legacyDir, "", false, "init", "--bare")
	if _, err := runGitCombined(ctx, init, defaultMaxCommandOutput); err != nil {
		t.Fatalf("init legacy repo: %v", err)
	}
	legacyCommit := writeSnapshotInto(t, ctx, legacyDir)
	gitEnv(t, ctx, legacyDir, "update-ref", commitRef(legacyCommit), legacyCommit)

	// A v2 workspace whose recorded root no longer exists.
	orphanRoot := filepath.Join(m.DataDir(), "deleted-checkout")
	if err := os.MkdirAll(orphanRoot, 0o755); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}
	orphan, err := m.CatalogForRoot(orphanRoot)
	if err != nil {
		t.Fatalf("CatalogForRoot: %v", err)
	}
	if _, err := orphan.CreateGeneration(ctx, ""); err != nil {
		t.Fatalf("CreateGeneration for orphan: %v", err)
	}
	if err := os.RemoveAll(orphanRoot); err != nil {
		t.Fatalf("remove orphan checkout: %v", err)
	}

	discovery, err := m.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	v2 := discovery.Workspace(cat.Workspace())
	if v2 == nil {
		t.Fatal("the versioned workspace was not discovered")
	}
	if v2.Layout != LayoutV2 || !v2.AllowsCapture() {
		t.Fatalf("v2 workspace = %+v, want a capturing v2 store", v2)
	}
	if v2.Root != cat.WorkspaceRoot {
		t.Fatalf("discovered root = %q, want %q", v2.Root, cat.WorkspaceRoot)
	}
	if got := v2.LegacyRefs; got != 0 {
		t.Fatalf("v2 workspace reported %d legacy refs", got)
	}
	gen := v2.Generation(rec.ID)
	if gen == nil || gen.Refs != 1 {
		t.Fatalf("generation %s = %+v, want one snapshot ref", rec.ID, gen)
	}
	if len(gen.Hashes) != 1 || gen.Hashes[0] != hash {
		t.Fatalf("generation hashes = %v, want [%s]", gen.Hashes, hash)
	}
	if !gen.Usable() || gen.Protected() {
		t.Fatalf("an active native generation reports usable=%v protected=%v", gen.Usable(), gen.Protected())
	}

	legacy := discovery.Workspace(legacyHash)
	if legacy == nil {
		t.Fatal("the legacy store whose checkout is gone was not discovered")
	}
	if legacy.Layout != LayoutLegacy || legacy.AllowsCapture() || !legacy.Quarantined() {
		t.Fatalf("legacy workspace = %+v, want a quarantined, non-capturing legacy store", legacy)
	}
	if legacy.LegacyRefs != 1 || legacy.LegacyHashes[0] != legacyCommit {
		t.Fatalf("legacy refs = %v, want [%s]", legacy.LegacyHashes, legacyCommit)
	}

	orphaned := discovery.Workspace(orphan.Workspace())
	if orphaned == nil {
		t.Fatal("the workspace whose checkout was deleted was not discovered")
	}
	if orphaned.Root != orphanRoot {
		t.Fatalf("orphaned workspace root = %q, want the recorded %q", orphaned.Root, orphanRoot)
	}
	if _, err := os.Lstat(orphaned.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the orphaned checkout unexpectedly exists: %v", err)
	}
}

// A corrupt manifest must quarantine the storage and yield a structured
// warning. It must never cause a delete, and must never admit a capture: both
// would act on a state nobody can read.
func TestCorruptManifestQuarantinesWithoutDeleting(t *testing.T) {
	requireGit(t)
	m, cat, ctx := testCatalog(t)
	rec := activeGeneration(t, ctx, cat)
	publishSnapshot(t, ctx, cat, rec.ID)
	dir, _ := cat.GenerationDir(rec.ID)

	const corrupt = `{"version":1,"workspace":`
	if err := os.WriteFile(cat.ManifestPath(), []byte(corrupt), 0o644); err != nil {
		t.Fatalf("write corrupt manifest: %v", err)
	}

	discovery, err := m.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	ws := discovery.Workspace(cat.Workspace())
	if ws == nil {
		t.Fatal("the quarantined workspace was not discovered")
	}
	if !ws.Corrupt || !ws.Quarantined() {
		t.Fatalf("workspace = %+v, want it quarantined", ws)
	}
	if ws.AllowsCapture() {
		t.Fatal("a corrupt store was admitted for capture")
	}
	if len(ws.Warnings) == 0 {
		t.Fatal("a corrupt manifest produced no warning")
	}
	found := false
	for _, w := range discovery.Warnings {
		if w.Workspace == cat.Workspace() && w.Reason == ReasonUnreadableFile {
			found = true
		}
	}
	if !found {
		t.Fatalf("no structured warning for the corrupt manifest: %+v", discovery.Warnings)
	}

	// Reconciliation must report it and change nothing at all.
	report, err := m.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	out := report.Workspace(cat.Workspace())
	if out == nil || !out.Quarantined {
		t.Fatalf("reconcile outcome = %+v, want it quarantined", out)
	}
	if len(out.GenerationsRemoved) != 0 || len(out.StagingRemoved) != 0 {
		t.Fatalf("reconciliation acted on a corrupt store: %+v", out)
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("reconciliation removed a generation in a corrupt store: %v", err)
	}
	if got, err := os.ReadFile(cat.ManifestPath()); err != nil || string(got) != corrupt {
		t.Fatalf("reconciliation rewrote a corrupt manifest: %v", err)
	}
}

// A generation whose refs cannot be listed must be excluded rather than assumed
// empty: "could not list" is not "nothing is published".
func TestUnreadableRefsAreReportedNotAssumedEmpty(t *testing.T) {
	requireGit(t)
	m, cat, ctx := testCatalog(t)
	rec := activeGeneration(t, ctx, cat)
	publishSnapshot(t, ctx, cat, rec.ID)
	dir, _ := cat.GenerationDir(rec.ID)

	// Make the repository unusable for ref listing without removing it: the
	// object store disappears, so git cannot resolve anything.
	if err := os.RemoveAll(filepath.Join(dir, "objects")); err != nil {
		t.Fatalf("remove objects: %v", err)
	}

	discovery, err := m.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	ws := discovery.Workspace(cat.Workspace())
	gen := ws.Generation(rec.ID)
	if gen == nil {
		t.Fatal("the generation was not discovered")
	}
	if gen.ListingError == nil {
		t.Fatal("an unreadable ref listing was not reported")
	}
	if gen.Usable() {
		t.Fatal("a generation whose refs are unknown reported itself usable")
	}
	// A generation whose refs cannot be read must never be removed: assuming
	// "no refs" is exactly how reconciliation would delete live objects.
	report, err := m.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	out := report.Workspace(cat.Workspace())
	for _, removed := range out.GenerationsRemoved {
		if removed == rec.ID {
			t.Fatal("reconciliation removed a generation whose refs could not be read")
		}
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("reconciliation removed the directory: %v", err)
	}
}

// Reconciliation must require the store lock: it deletes directories and
// rewrites manifests.
func TestReconcileRequiresOwnership(t *testing.T) {
	dataDir := t.TempDir()
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.Reconcile(context.Background()); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("Reconcile error = %v, want ErrNotOwned", err)
	}
	if _, err := m.ReconcileWorkspace(context.Background(), "0123456789ab"); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("ReconcileWorkspace error = %v, want ErrNotOwned", err)
	}
}

// ---------------------------------------------------------------------------
// Crash boundaries: generation creation
// ---------------------------------------------------------------------------

// Restarting at every generation-creation boundary must preserve published refs
// and converge on the same state, whatever the interruption.
func TestReconcileAtGenerationCreationBoundaries(t *testing.T) {
	requireGit(t)

	boundaries := []struct {
		name     string
		hook     func(error) Hooks
		wantGone bool
	}{
		{
			name:     "after the creating record",
			hook:     func(e error) Hooks { return Hooks{AfterGenerationRecord: func() error { return e }} },
			wantGone: true,
		},
		{
			name:     "after the repository metadata",
			hook:     func(e error) Hooks { return Hooks{AfterInitGenerationRepo: func() error { return e }} },
			wantGone: true,
		},
	}

	for _, b := range boundaries {
		t.Run(b.name, func(t *testing.T) {
			first, cat, ctx := testCatalog(t, WithHooks(b.hook(errInjected)))
			if _, err := cat.CreateGeneration(ctx, ""); !errors.Is(err, errInjected) {
				t.Fatalf("CreateGeneration error = %v, want the injected interruption", err)
			}

			// Restart: drop ownership so a second manager can take it. This is
			// the crash semantics the boundaries model — the interrupted
			// process is gone and leaves nothing but durable state.
			if err := first.Release(); err != nil {
				t.Fatalf("Release: %v", err)
			}

			restarted, err := NewManager(cat.manager.DataDir())
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			if _, err := restarted.Acquire(ctx); err != nil {
				t.Fatalf("Acquire: %v", err)
			}
			defer restarted.Release()

			report, err := restarted.Reconcile(ctx)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			out := report.Workspace(cat.Workspace())
			if out == nil {
				t.Fatal("reconciliation did not report the workspace")
			}
			if want := 1; len(out.GenerationsForgotten) != want {
				t.Fatalf("GenerationsForgotten = %v, want the interrupted generation", out.GenerationsForgotten)
			}

			cat2, err := restarted.Catalog(cat.Workspace())
			if err != nil {
				t.Fatalf("Catalog: %v", err)
			}
			man, err := cat2.Load()
			if err != nil {
				t.Fatalf("Load after reconciliation: %v", err)
			}
			if len(man.Generations) != 0 || man.ActiveID != "" {
				t.Fatalf("manifest after reconciliation = %+v, want an empty catalog", man)
			}

			// A second restart must be a no-op: reconciliation is idempotent.
			second, err := restarted.Reconcile(ctx)
			if err != nil {
				t.Fatalf("second Reconcile: %v", err)
			}
			secondOut := second.Workspace(cat.Workspace())
			if len(secondOut.GenerationsForgotten) != 0 || len(secondOut.GenerationsRemoved) != 0 {
				t.Fatalf("second reconciliation acted again: %+v", secondOut)
			}
		})
	}
}

// An interrupted ACTIVE generation holding published snapshots must be sealed,
// never deleted: the published ref wins over the interrupted manifest update.
func TestPublishedRefWinsOverInterruptedManifest(t *testing.T) {
	requireGit(t)
	_, cat, ctx := testCatalog(t)

	// Publish into a generation whose state is still `creating`: exactly what a
	// crash between the ref write and the activation write leaves behind.
	rec, err := cat.CreateGeneration(ctx, "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	hash := publishSnapshot(t, ctx, cat, rec.ID)

	report, err := cat.manager.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	out := report.Workspace(cat.Workspace())
	for _, removed := range out.GenerationsRemoved {
		if removed == rec.ID {
			t.Fatal("reconciliation deleted a generation holding a published ref")
		}
	}
	if len(out.GenerationsSealed) != 1 || out.GenerationsSealed[0] != rec.ID {
		t.Fatalf("GenerationsSealed = %v, want [%s]", out.GenerationsSealed, rec.ID)
	}

	// The snapshot must still be there and still be resolvable.
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gone := man.Generation(rec.ID)
	if gone == nil || gone.State != GenerationSealed {
		t.Fatalf("generation after reconciliation = %+v, want sealed", gone)
	}
	refs, hashes, err := cat.PublishedRefs(ctx, rec.ID)
	if err != nil {
		t.Fatalf("PublishedRefs: %v", err)
	}
	if len(hashes) != 1 || hashes[0] != hash || refs[hash] == "" {
		t.Fatalf("published refs = %v, want the snapshot %s preserved", hashes, hash)
	}
	dir, _ := cat.GenerationDir(rec.ID)
	if _, err := gitEnvErr(ctx, dir, "cat-file", "-e", hash); err != nil {
		t.Fatalf("published object is no longer readable: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Crash boundaries: staging and publication
// ---------------------------------------------------------------------------

// An interrupted STAGING (scratch reserved, never published) must lose only the
// scratch. The objects an interrupted capture may have written must NOT be
// deleted indiscriminately: Git objects are content-addressed and may be shared
// with a snapshot that is still needed.
func TestReconcileCleansInterruptedStagingAndCountsAbandoned(t *testing.T) {
	requireGit(t)
	_, cat, ctx := testCatalog(t)
	rec := activeGeneration(t, ctx, cat)

	// A published snapshot that shares its object namespace with the interrupted
	// capture's leftovers.
	published := publishSnapshot(t, ctx, cat, rec.ID)
	dir, _ := cat.GenerationDir(rec.ID)

	// Reserve staging, then write objects into the generation without
	// publishing a ref for them: abandoned work.
	cs, err := cat.ReserveStaging(ctx, rec.ID)
	if err != nil {
		t.Fatalf("ReserveStaging: %v", err)
	}
	stagingPath, err := cat.StagingPath(cs.StagingID)
	if err != nil {
		t.Fatalf("StagingPath: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagingPath, "partial"), make([]byte, 40), 0o644); err != nil {
		t.Fatalf("write staging file: %v", err)
	}
	// An object no ref names, plus a re-write of the published blob so a shared
	// object demonstrably exists.
	orphanCmd := newGitCmd(ctx, "git", dir, "", false, "hash-object", "-w", "-t", "blob", "--stdin")
	orphanCmd.Stdin = strings.NewReader("abandoned-and-unpublished\n")
	if _, err := runGitCombined(ctx, orphanCmd, defaultMaxCommandOutput); err != nil {
		t.Fatalf("write orphan object: %v", err)
	}
	before, err := cat.manager.UnreachableBytes(ctx, dir)
	if err != nil {
		t.Fatalf("UnreachableBytes: %v", err)
	}
	if before <= 0 {
		t.Fatalf("UnreachableBytes = %d, want the abandoned object measured", before)
	}

	report, err := cat.manager.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	out := report.Workspace(cat.Workspace())
	if out.CapturesCleared != 1 {
		t.Fatalf("CapturesCleared = %d, want 1", out.CapturesCleared)
	}
	if _, err := os.Lstat(stagingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging scratch survived reconciliation: %v", err)
	}
	if out.AbandonedBytes < before {
		t.Fatalf("AbandonedBytes = %d, want at least the %d measured unreachable bytes", out.AbandonedBytes, before)
	}
	// The published snapshot must be untouched.
	_, hashes, err := cat.PublishedRefs(ctx, rec.ID)
	if err != nil {
		t.Fatalf("PublishedRefs: %v", err)
	}
	if len(hashes) != 1 || hashes[0] != published {
		t.Fatalf("published refs = %v, want the snapshot %s preserved", hashes, published)
	}
	if _, err := gitEnvErr(ctx, dir, "cat-file", "-e", published); err != nil {
		t.Fatalf("the published object was removed: %v", err)
	}
	// The interrupted capture's leftovers must NOT have been deleted
	// object-by-object: shared objects make that unsafe.
	after, err := cat.manager.UnreachableBytes(ctx, dir)
	if err != nil {
		t.Fatalf("UnreachableBytes: %v", err)
	}
	if after != before {
		t.Fatalf("unreachable bytes changed from %d to %d; an interrupted capture's objects were removed individually", before, after)
	}
	if out.GenerationsSealed == nil || len(out.GenerationsSealed) != 1 || out.GenerationsSealed[0] != rec.ID {
		t.Fatalf("GenerationsSealed = %v, want the dirty generation sealed for whole reclamation", out.GenerationsSealed)
	}
}

// An interrupted PUBLICATION (objects durable, ref not yet written, manifest not
// yet updated) must be resolved by looking at the REF, which is the only durable
// statement about whether the snapshot exists.
func TestReconcileInterruptedPublication(t *testing.T) {
	requireGit(t)

	t.Run("ref written, manifest not updated", func(t *testing.T) {
		marker := errors.New("interrupted after the ref landed")
		_, cat, ctx := testCatalog(t, WithHooks(Hooks{AfterPublishRef: func() error { return marker }}))
		rec := activeGeneration(t, ctx, cat)

		dir, _ := cat.GenerationDir(rec.ID)
		hash := writeSnapshotInto(t, ctx, dir)
		// The publication itself is interrupted: the ref may or may not have
		// been written, so the test drives it explicitly.
		intent, err := cat.ReserveStaging(ctx, rec.ID)
		if err != nil {
			t.Fatalf("ReserveStaging: %v", err)
		}
		if _, err := cat.SetPublicationIntent(intent.StagingID, hash); err != nil {
			t.Fatalf("SetPublicationIntent: %v", err)
		}
		if _, err := cat.PublishSnapshotRef(ctx, rec.ID, hash); !errors.Is(err, marker) {
			t.Fatalf("PublishSnapshotRef error = %v, want the injected interruption", err)
		}

		report, err := cat.manager.Reconcile(ctx)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		out := report.Workspace(cat.Workspace())
		if out.CapturesCleared != 1 {
			t.Fatalf("CapturesCleared = %d, want 1", out.CapturesCleared)
		}
		man, err := cat.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if man.Capture != nil {
			t.Fatalf("capture record survived reconciliation: %+v", man.Capture)
		}
		// The ref landed, so the snapshot must be preserved.
		_, hashes, err := cat.PublishedRefs(ctx, rec.ID)
		if err != nil {
			t.Fatalf("PublishedRefs: %v", err)
		}
		if len(hashes) != 1 || hashes[0] != hash {
			t.Fatalf("published refs = %v, want the snapshot %s preserved", hashes, hash)
		}
		if _, err := gitEnvErr(ctx, dir, "cat-file", "-e", hash); err != nil {
			t.Fatalf("the published object was removed: %v", err)
		}
		// Staging scratch goes: the snapshot is already durable.
		stagingPath, _ := cat.StagingPath(intent.StagingID)
		if _, err := os.Lstat(stagingPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("staging scratch survived a completed publication: %v", err)
		}
	})

	t.Run("objects written, ref never written", func(t *testing.T) {
		_, cat, ctx := testCatalog(t)
		rec := activeGeneration(t, ctx, cat)
		dir, _ := cat.GenerationDir(rec.ID)

		intent, err := cat.ReserveStaging(ctx, rec.ID)
		if err != nil {
			t.Fatalf("ReserveStaging: %v", err)
		}
		// Objects exist, but no ref was ever published for them.
		orphanCmd := newGitCmd(ctx, "git", dir, "", false, "hash-object", "-w", "-t", "blob", "--stdin")
		orphanCmd.Stdin = strings.NewReader("never-published\n")
		if _, err := runGitCombined(ctx, orphanCmd, defaultMaxCommandOutput); err != nil {
			t.Fatalf("write orphan object: %v", err)
		}

		report, err := cat.manager.Reconcile(ctx)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		out := report.Workspace(cat.Workspace())
		if out.CapturesCleared != 1 {
			t.Fatalf("CapturesCleared = %d, want 1", out.CapturesCleared)
		}
		if out.AbandonedBytes <= 0 {
			t.Fatalf("AbandonedBytes = %d, want the unpublished objects counted", out.AbandonedBytes)
		}
		// Nothing may be published, and nothing may be deleted object-by-object.
		_, hashes, err := cat.PublishedRefs(ctx, rec.ID)
		if err != nil {
			t.Fatalf("PublishedRefs: %v", err)
		}
		if len(hashes) != 0 {
			t.Fatalf("published refs = %v, want none", hashes)
		}
		stagingPath, _ := cat.StagingPath(intent.StagingID)
		if _, err := os.Lstat(stagingPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("staging scratch survived reconciliation: %v", err)
		}
		if len(out.GenerationsSealed) != 1 || out.GenerationsSealed[0] != rec.ID {
			t.Fatalf("GenerationsSealed = %v, want the dirty active generation sealed", out.GenerationsSealed)
		}
	})
}

// A capture interrupted before it recorded any publication intent must still be
// resolvable: there is no ref to find, so only the staging scratch is removed.
func TestReconcileInterruptedStagingWithNoIntent(t *testing.T) {
	requireGit(t)
	marker := errors.New("interrupted right after reserving staging")
	_, cat, ctx := testCatalog(t, WithHooks(Hooks{AfterReserveStaging: func() error { return marker }}))
	rec := activeGeneration(t, ctx, cat)

	if _, err := cat.ReserveStaging(ctx, rec.ID); !errors.Is(err, marker) {
		t.Fatalf("ReserveStaging error = %v, want the injected interruption", err)
	}
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if man.Capture == nil {
		t.Fatal("the capture intent was not durable at this boundary")
	}
	stagingPath, err := cat.StagingPath(man.Capture.StagingID)
	if err != nil {
		t.Fatalf("StagingPath: %v", err)
	}
	// At this boundary the record is durable and the scratch directory is not.
	if _, err := os.Lstat(stagingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging scratch should not exist at this boundary: %v", err)
	}

	report, err := cat.manager.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	out := report.Workspace(cat.Workspace())
	if out.CapturesCleared != 1 {
		t.Fatalf("CapturesCleared = %d, want 1", out.CapturesCleared)
	}
	man, _ = cat.Load()
	if man.Capture != nil {
		t.Fatalf("capture record survived reconciliation: %+v", man.Capture)
	}
	// The active generation must stay active: nothing was ever written into it.
	if man.ActiveID != rec.ID {
		t.Fatalf("ActiveID = %q, want the generation left active (%q)", man.ActiveID, rec.ID)
	}
	if len(out.GenerationsSealed) != 0 {
		t.Fatalf("GenerationsSealed = %v, want none: no objects were written", out.GenerationsSealed)
	}
}

// ---------------------------------------------------------------------------
// Crash boundaries: deletion
// ---------------------------------------------------------------------------

// An interrupted DELETION must be resumed, and the generation must never become
// usable again. A half-removed Git directory cannot be trusted to hold what its
// refs claim.
func TestReconcileResumesInterruptedDeletion(t *testing.T) {
	requireGit(t)

	for _, tc := range []struct {
		name       string
		hooks      Hooks
		dirRemoved bool
	}{
		{
			name:       "interrupted before the directory is removed",
			hooks:      Hooks{AfterMarkDeleting: func() error { return errInjected }},
			dirRemoved: false,
		},
		{
			name:       "interrupted after the directory is removed",
			hooks:      Hooks{AfterRemoveGenerationDir: func() error { return errInjected }},
			dirRemoved: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cat, ctx := testCatalog(t, WithHooks(tc.hooks))
			rec := activeGeneration(t, ctx, cat)

			// Seal it, mark it deleting, and let the hook interrupt the removal.
			if err := cat.SealGeneration(rec.ID); err != nil {
				t.Fatalf("SealGeneration: %v", err)
			}
			_, err := cat.MarkDeleting(rec.ID)
			if tc.hooks.AfterMarkDeleting != nil {
				if !errors.Is(err, errInjected) {
					t.Fatalf("MarkDeleting error = %v, want the injected interruption", err)
				}
			} else if err != nil {
				t.Fatalf("MarkDeleting: %v", err)
			}

			// Drive removal explicitly so both boundaries are reached.
			if tc.hooks.AfterMarkDeleting == nil {
				if err := cat.RemoveGenerationDirectory(rec.ID); !errors.Is(err, errInjected) {
					t.Fatalf("RemoveGenerationDirectory error = %v, want the injected interruption", err)
				}
			}

			dir, _ := cat.GenerationDir(rec.ID)
			_, statErr := os.Lstat(dir)
			if tc.dirRemoved && statErr == nil {
				t.Fatal("the directory should have been removed at this boundary")
			}
			if !tc.dirRemoved && statErr != nil {
				t.Fatalf("the directory should still exist at this boundary: %v", statErr)
			}
			// Whatever the boundary, the state must be durable as `deleting`.
			man, err := cat.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			rec2 := man.Generation(rec.ID)
			if rec2 == nil || rec2.State != GenerationDeleting {
				t.Fatalf("generation state = %+v, want %q", rec2, GenerationDeleting)
			}
			if rec2.State.Usable() {
				t.Fatal("a generation being deleted reports itself usable")
			}

			// A restart must finish the deletion, never revive it.
			report, err := cat.manager.Reconcile(ctx)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			out := report.Workspace(cat.Workspace())
			if len(out.GenerationsForgotten) != 1 || out.GenerationsForgotten[0] != rec.ID {
				t.Fatalf("GenerationsForgotten = %v, want [%s]", out.GenerationsForgotten, rec.ID)
			}
			if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the generation directory survived reconciliation: %v", err)
			}
			man, _ = cat.Load()
			if man.Generation(rec.ID) != nil {
				t.Fatal("the manifest record survived reconciliation")
			}
			// And again, idempotently.
			if _, err := cat.manager.Reconcile(ctx); err != nil {
				t.Fatalf("second Reconcile: %v", err)
			}
		})
	}
}

// finishDeletion must persist the intent before touching the directory, so a
// crash between the two leaves a resumable state rather than an unexplained
// missing directory.
func TestDeletionPersistsIntentBeforeRemoval(t *testing.T) {
	requireGit(t)
	var (
		sawDeleting bool
		// The hook is installed before the manager exists, so the catalog it
		// needs is captured indirectly.
		loaded *Catalog
	)
	_, cat, ctx := testCatalog(t, WithHooks(Hooks{
		BeforeRemoveGenerationDir: func(id string) error {
			// At this instant the state must ALREADY be `deleting`.
			man, err := loaded.Load()
			if err != nil {
				t.Errorf("Load during removal: %v", err)
				return err
			}
			rec := man.Generation(id)
			sawDeleting = rec != nil && rec.State == GenerationDeleting
			return errInjected
		},
	}))
	loaded = cat
	rec := activeGeneration(t, ctx, cat)
	if err := cat.SealGeneration(rec.ID); err != nil {
		t.Fatalf("SealGeneration: %v", err)
	}
	// Reconciliation only finishes a deletion whose intent is already durable.
	// That is the point of the ordering being tested here: nothing removes a
	// generation until something has decided to.
	if _, err := cat.MarkDeleting(rec.ID); err != nil {
		t.Fatalf("MarkDeleting: %v", err)
	}

	// The hook's error is the interruption reaching the caller, which is what a
	// crash at this boundary looks like to the process that was interrupted.
	if _, err := cat.manager.Reconcile(ctx); !errors.Is(err, errInjected) {
		t.Fatalf("Reconcile error = %v, want the injected interruption", err)
	}
	if !sawDeleting {
		t.Fatal("the directory was being removed before the deleting state was durable")
	}
	// The refusal must have left a resumable `deleting` record, not a hole.
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rec2 := man.Generation(rec.ID); rec2 == nil || rec2.State != GenerationDeleting {
		t.Fatalf("generation after an interrupted removal = %+v, want %q", rec2, GenerationDeleting)
	}
	dir, _ := cat.GenerationDir(rec.ID)
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("the interrupted removal left no directory to retry: %v", err)
	}
}

// A protected generation must never be reclaimed automatically, because its
// objects are the only surviving copy of migrated rollback history.
func TestReconcileNeverReclaimsProtectedGeneration(t *testing.T) {
	requireGit(t)
	_, cat, ctx := testCatalog(t)
	rec := activeGeneration(t, ctx, cat)

	// Mark it legacy-origin and protected, the way a migration would.
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	target := man.Generation(rec.ID)
	target.Origin = OriginLegacy
	target.Protected = true
	if err := cat.Save(man); err != nil {
		t.Fatalf("Save: %v", err)
	}
	dir, _ := cat.GenerationDir(rec.ID)

	report, err := cat.manager.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	out := report.Workspace(cat.Workspace())
	for _, removed := range out.GenerationsRemoved {
		if removed == rec.ID {
			t.Fatal("reconciliation reclaimed a protected generation")
		}
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("a protected generation's directory was removed: %v", err)
	}
	man, _ = cat.Load()
	if man.Generation(rec.ID) == nil {
		t.Fatal("a protected generation's manifest record was dropped")
	}
}

// A generation directory the manifest does not describe must be reported and
// NEVER removed: with no record there is no durable statement about what it is.
func TestUnlistedGenerationIsReportedNotRemoved(t *testing.T) {
	requireGit(t)
	m, cat, ctx := testCatalog(t)
	activeGeneration(t, ctx, cat)

	// A generation directory with no manifest record.
	unlistedID := formatGenerationID(5000, "feedface")
	unlistedDir, err := cat.GenerationDir(unlistedID)
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	// The directory has to be a real bare repository, otherwise it is reported
	// as unusable rather than as merely unlisted — a different condition.
	for _, sub := range generationDirs {
		if err := os.MkdirAll(filepath.Join(unlistedDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	if err := os.WriteFile(filepath.Join(unlistedDir, "HEAD"), []byte("ref: "+generationHeadTarget+"\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unlistedDir, "config"), []byte(generationConfig), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	discovery, err := m.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	ws := discovery.Workspace(cat.Workspace())
	gen := ws.Generation(unlistedID)
	if gen == nil {
		t.Fatal("the unlisted generation was not discovered")
	}
	if gen.Record != nil {
		t.Fatal("an unlisted generation reported a manifest record")
	}
	if !gen.Protected() {
		t.Fatal("an unlisted generation must be treated as protected")
	}
	if gen.Usable() {
		t.Fatal("an unlisted generation reported itself usable")
	}

	report, err := m.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	out := report.Workspace(cat.Workspace())
	for _, removed := range out.GenerationsRemoved {
		if removed == unlistedID {
			t.Fatal("reconciliation removed an unlisted generation directory")
		}
	}
	if _, err := os.Lstat(unlistedDir); err != nil {
		t.Fatalf("an unlisted generation directory was removed: %v", err)
	}
	warned := false
	for _, w := range out.Warnings {
		if strings.Contains(w.Message, "not listed in the manifest") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("no warning was reported for an unlisted generation: %+v", out.Warnings)
	}
}

// ---------------------------------------------------------------------------
// Known scratch
// ---------------------------------------------------------------------------

// Interrupted manifest replacements and orphaned staging directories are the
// only artifacts whose provenance needs no guesswork, so they are the only ones
// removed automatically. Both are measured into abandoned bytes first.
func TestReconcileRemovesKnownScratchAndCountsIt(t *testing.T) {
	requireGit(t)
	_, cat, ctx := testCatalog(t)
	activeGeneration(t, ctx, cat)

	// An orphaned staging directory: valid shape, but no capture record owns it.
	stagingDir := cat.StagingDir()
	orphan := filepath.Join(stagingDir, "stage-aaaaaaaaaaaaaaaa")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatalf("mkdir orphan staging: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "blob"), make([]byte, 8192), 0o644); err != nil {
		t.Fatalf("write orphan staging file: %v", err)
	}
	// An unrecognised entry in the same namespace must be left alone.
	unknown := filepath.Join(stagingDir, "not-a-staging-id")
	if err := os.MkdirAll(unknown, 0o755); err != nil {
		t.Fatalf("mkdir unknown staging entry: %v", err)
	}
	// An interrupted manifest replacement.
	scratch := filepath.Join(cat.Root(), manifestTempPrefix+"leftover")
	if err := os.WriteFile(scratch, make([]byte, 4096), 0o644); err != nil {
		t.Fatalf("write manifest scratch: %v", err)
	}

	// Discovery must already account for them.
	discovery, err := cat.manager.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	ws := discovery.Workspace(cat.Workspace())
	if ws.StagingBytes <= 0 {
		t.Fatalf("StagingBytes = %d, want the orphaned staging directory measured", ws.StagingBytes)
	}
	if ws.UnpublishedBytes < ws.StagingBytes {
		t.Fatalf("UnpublishedBytes = %d, want it to include StagingBytes = %d", ws.UnpublishedBytes, ws.StagingBytes)
	}
	if discovery.UnpublishedBytes <= 0 {
		t.Fatalf("store UnpublishedBytes = %d, want the abandoned artifacts counted", discovery.UnpublishedBytes)
	}

	before := discovery.UnpublishedBytes
	report, err := cat.manager.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	out := report.Workspace(cat.Workspace())
	if len(out.StagingRemoved) != 1 || out.StagingRemoved[0] != "stage-aaaaaaaaaaaaaaaa" {
		t.Fatalf("StagingRemoved = %v, want the orphaned staging directory", out.StagingRemoved)
	}
	if len(out.ManifestScratchRemoved) != 1 {
		t.Fatalf("ManifestScratchRemoved = %v, want the interrupted replacement", out.ManifestScratchRemoved)
	}
	if out.AbandonedBytes < before {
		t.Fatalf("AbandonedBytes = %d, want at least the %d measured before removal", out.AbandonedBytes, before)
	}
	if _, err := os.Lstat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the orphaned staging directory survived: %v", err)
	}
	if _, err := os.Lstat(scratch); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the manifest scratch file survived: %v", err)
	}
	// An entry that is not a valid staging identifier must be left alone.
	if _, err := os.Lstat(unknown); err != nil {
		t.Fatalf("an unrecognised staging entry was removed: %v", err)
	}
}

// Scratch owned by an in-flight capture must be resolved with its record, not
// swept by the generic scratch path: only the capture record says whether the
// publication landed.
func TestScratchOwnedByACaptureIsResolvedWithItsRecord(t *testing.T) {
	requireGit(t)
	_, cat, ctx := testCatalog(t)
	rec := activeGeneration(t, ctx, cat)
	cs, err := cat.ReserveStaging(ctx, rec.ID)
	if err != nil {
		t.Fatalf("ReserveStaging: %v", err)
	}
	path, err := cat.StagingPath(cs.StagingID)
	if err != nil {
		t.Fatalf("StagingPath: %v", err)
	}
	report, err := cat.manager.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	out := report.Workspace(cat.Workspace())
	// The record was resolved (never published), so the scratch is gone.
	if out.CapturesCleared != 1 {
		t.Fatalf("CapturesCleared = %d, want 1", out.CapturesCleared)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the resolved capture's scratch survived: %v", err)
	}
	for _, removed := range out.StagingRemoved {
		if removed == cs.StagingID {
			t.Fatal("the capture's scratch was swept by the generic path instead of being resolved with its record")
		}
	}
}

// ---------------------------------------------------------------------------
// Legacy stores
// ---------------------------------------------------------------------------

// Reconciliation must never mutate a legacy store: an old Marshal binary does
// not honour the store lock, so this process cannot prove no writer is active.
func TestReconcileLeavesLegacyStoresByteIdentical(t *testing.T) {
	requireGit(t)
	m, cat, ctx := testCatalog(t)
	activeGeneration(t, ctx, cat)

	legacyHash := WorkspaceHashFor(filepath.Join(m.DataDir(), "legacy-workspace"))
	legacyDir := filepath.Join(m.Root(), legacyHash)
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	init := newGitCmd(ctx, "git", legacyDir, "", false, "init", "--bare")
	if _, err := runGitCombined(ctx, init, defaultMaxCommandOutput); err != nil {
		t.Fatalf("init legacy repo: %v", err)
	}
	legacyCommit := writeSnapshotInto(t, ctx, legacyDir)
	gitEnv(t, ctx, legacyDir, "update-ref", commitRef(legacyCommit), legacyCommit)

	before := dirFingerprint(t, legacyDir)
	report, err := m.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	after := dirFingerprint(t, legacyDir)
	if before != after {
		t.Fatal("reconciliation mutated a legacy store")
	}
	// The legacy store must be reported as untouched, not as a workspace that
	// was reconciled.
	found := false
	for _, p := range report.LegacyUntouched {
		if p == legacyDir {
			found = true
		}
	}
	if !found {
		t.Fatalf("LegacyUntouched = %v, want it to include %q", report.LegacyUntouched, legacyDir)
	}
	// Its refs must survive: no automatic legacy ref pruning, ever.
	_, hashes, err := m.snapshotRefsInDir(ctx, legacyDir)
	if err != nil {
		t.Fatalf("snapshotRefsInDir: %v", err)
	}
	if len(hashes) != 1 || hashes[0] != legacyCommit {
		t.Fatalf("legacy refs = %v, want [%s] untouched", hashes, legacyCommit)
	}
	// A narrow reconciliation of a legacy workspace must refuse rather than
	// proceed.
	narrow, err := m.ReconcileWorkspace(ctx, legacyHash)
	if err != nil {
		t.Fatalf("ReconcileWorkspace: %v", err)
	}
	if !narrow.Quarantined {
		t.Fatalf("ReconcileWorkspace on a legacy store = %+v, want it quarantined", narrow)
	}
	if dirFingerprint(t, legacyDir) != before {
		t.Fatal("ReconcileWorkspace mutated a legacy store")
	}
}

// A legacy store must never be confused with a v2 workspace: its layout decides
// whether capture and deletion are permitted at all.
func TestLegacyIsNeverAdmittedForCapture(t *testing.T) {
	requireGit(t)
	m, ctx := newTestManager(t), context.Background()
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	legacyHash := WorkspaceHashFor(filepath.Join(m.DataDir(), "some-workspace"))
	legacyDir := filepath.Join(m.Root(), legacyHash)
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	init := newGitCmd(ctx, "git", legacyDir, "", false, "init", "--bare")
	if _, err := runGitCombined(ctx, init, defaultMaxCommandOutput); err != nil {
		t.Fatalf("init legacy repo: %v", err)
	}

	discovery, err := m.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	ws := discovery.Workspace(legacyHash)
	if ws == nil {
		t.Fatal("the legacy store was not discovered")
	}
	if ws.AllowsCapture() {
		t.Fatal("a legacy store was admitted for capture")
	}
	if !ws.Quarantined() {
		t.Fatal("a legacy store was not quarantined from automatic deletion")
	}
	// A handle for the legacy hash must build a V2 catalog path, never the
	// legacy directory: reclamation must never target the wrong directory.
	cat, err := m.Catalog(legacyHash)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if cat.Root() == legacyDir {
		t.Fatalf("the v2 catalog path %q is the legacy store directory", cat.Root())
	}
	if !strings.HasPrefix(cat.Root(), m.V2Root()) {
		t.Fatalf("catalog root %q is not under the versioned root %q", cat.Root(), m.V2Root())
	}
}

// ---------------------------------------------------------------------------
// Accounting
// ---------------------------------------------------------------------------

// The whole-root accounting must classify v2 and legacy storage separately, and
// expose per-generation figures. A budget that ignored legacy bytes would
// believe there is room where there is none.
func TestUsageSeparatesV2AndLegacyAndMeasuresGenerations(t *testing.T) {
	requireGit(t)
	m, cat, ctx := testCatalog(t)
	rec := activeGeneration(t, ctx, cat)
	hash := publishSnapshot(t, ctx, cat, rec.ID)

	legacyHash := WorkspaceHashFor(filepath.Join(m.DataDir(), "legacy"))
	legacyDir := filepath.Join(m.Root(), legacyHash)
	if err := os.MkdirAll(filepath.Join(legacyDir, "objects"), 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	writeFile(t, filepath.Join(legacyDir, "objects", "pack", "big"), 1<<16)

	usage, err := m.Usage()
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.V2Allocated <= 0 {
		t.Fatalf("V2Allocated = %d, want the versioned store measured", usage.V2Allocated)
	}
	if usage.LegacyAllocated <= 0 {
		t.Fatalf("LegacyAllocated = %d, want the legacy store measured", usage.LegacyAllocated)
	}
	// The split must be a partition, not a double count: both parts are already
	// inside the global total.
	var sum int64 = usage.RootMetadata
	for _, u := range usage.Workspaces {
		sum += u.Allocated
	}
	for _, u := range usage.Unknown {
		sum += u.Allocated
	}
	if usage.Lock != nil {
		sum += usage.Lock.Allocated
	}
	if usage.GlobalAllocated != sum {
		t.Fatalf("GlobalAllocated = %d, want %d", usage.GlobalAllocated, sum)
	}
	if usage.V2Allocated+usage.LegacyAllocated > usage.GlobalAllocated {
		t.Fatalf("the v2 (%d) plus legacy (%d) split exceeds the global total %d",
			usage.V2Allocated, usage.LegacyAllocated, usage.GlobalAllocated)
	}

	gens := usage.Generations[cat.Workspace()]
	if gens == nil {
		t.Fatal("no per-generation measurement for the versioned workspace")
	}
	if gens.Corrupt {
		t.Fatal("a healthy workspace was reported as corrupt")
	}
	gu := gens.ByID[rec.ID]
	if gu == nil {
		t.Fatalf("generation %s missing from the measurement", rec.ID)
	}
	if gu.Allocated <= 0 {
		t.Fatalf("generation Allocated = %d, want the directory measured", gu.Allocated)
	}
	if !gu.Listed || gu.Protected {
		t.Fatalf("generation usage = %+v, want it listed and unprotected", gu)
	}
	if gu.Refs != 1 {
		t.Fatalf("generation Refs = %d, want 1", gu.Refs)
	}
	if len(gens.Order) != 1 || gens.Order[0] != rec.ID {
		t.Fatalf("generation order = %v, want [%s]", gens.Order, rec.ID)
	}
	if gens.ManifestAllocated <= 0 {
		t.Fatalf("ManifestAllocated = %d, want the manifest measured", gens.ManifestAllocated)
	}
	_ = hash
}

// An unlisted generation directory must be measured but marked protected and
// never counted as reclaimable: nothing durable says what it is.
func TestUsageMarksUnlistedGenerationsUnreclaimable(t *testing.T) {
	requireGit(t)
	m, cat, ctx := testCatalog(t)
	rec := activeGeneration(t, ctx, cat)

	unlistedID := formatGenerationID(4242, "cafebabe")
	unlistedDir, err := cat.GenerationDir(unlistedID)
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	writeFile(t, filepath.Join(unlistedDir, "objects", "loose"), 4096)

	gens, err := m.MeasureWorkspaceGenerations(ctx, cat.Workspace())
	if err != nil {
		t.Fatalf("MeasureWorkspaceGenerations: %v", err)
	}
	if len(gens.Unlisted) != 1 || gens.Unlisted[0].ID != unlistedID {
		t.Fatalf("Unlisted = %+v, want the unlisted generation", gens.Unlisted)
	}
	if !gens.Unlisted[0].Protected {
		t.Fatal("an unlisted generation was not marked protected")
	}
	if _, ok := gens.ByID[unlistedID]; ok {
		t.Fatal("an unlisted generation was reported as listed")
	}
	// The reclaimable total must not include an unlisted generation's bytes.
	if got, want := gens.Reclaimable(), int64(0); got > want+gens.ByID[rec.ID].Allocated {
		t.Fatalf("Reclaimable = %d, want it bounded by the listed generations", got)
	}

	// A protected generation must not be reclaimable either.
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	target := man.Generation(rec.ID)
	target.Origin = OriginLegacy
	target.Protected = true
	if err := cat.Save(man); err != nil {
		t.Fatalf("Save: %v", err)
	}
	gens, err = m.MeasureWorkspaceGenerations(ctx, cat.Workspace())
	if err != nil {
		t.Fatalf("MeasureWorkspaceGenerations: %v", err)
	}
	if got := gens.Reclaimable(); got != 0 {
		t.Fatalf("Reclaimable = %d, want 0 for a protected generation", got)
	}
	if !gens.ByID[rec.ID].Protected {
		t.Fatal("a protected generation was not marked protected in accounting")
	}
}

// ---------------------------------------------------------------------------
// Ordering
// ---------------------------------------------------------------------------

// Oldest-first ordering must be by creation sequence, so reclamation consumes
// generations in the order they were created regardless of identifier text.
func TestGenerationRecordsAreOrderedByCreation(t *testing.T) {
	requireGit(t)
	_, cat, ctx := testCatalog(t)

	var created []string
	for i := 0; i < 5; i++ {
		rec, err := cat.CreateGeneration(ctx, "")
		if err != nil {
			t.Fatalf("CreateGeneration: %v", err)
		}
		if err := cat.SealGeneration(rec.ID); err != nil {
			t.Fatalf("SealGeneration: %v", err)
		}
		created = append(created, rec.ID)
	}
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sealed := man.Sealed()
	if len(sealed) != len(created) {
		t.Fatalf("Sealed() = %d records, want %d", len(sealed), len(created))
	}
	for i, rec := range sealed {
		if rec.ID != created[i] {
			t.Fatalf("sealed[%d] = %s, want %s (creation order)", i, rec.ID, created[i])
		}
		seq, ok := rec.Sequence()
		if !ok {
			t.Fatalf("generation %s has no parseable sequence", rec.ID)
		}
		if i > 0 {
			prev, _ := sealed[i-1].Sequence()
			if seq <= prev {
				t.Fatalf("sequence %d is not greater than %d", seq, prev)
			}
		}
	}
	ids := man.IDs()
	for i, id := range ids {
		if id != created[i] {
			t.Fatalf("IDs()[%d] = %s, want %s", i, id, created[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Manifest growth bounds
// ---------------------------------------------------------------------------

// The manifest must be bounded: an unbounded one would consume the budget it is
// meant to protect and would have to be fully read into memory.
func TestManifestBoundsAreEnforced(t *testing.T) {
	_, cat, _ := testCatalog(t)

	// A manifest with more generation records than the bound must be refused on
	// read and on write.
	man := &Manifest{Version: LayoutVersion, Workspace: cat.Workspace()}
	for i := 0; i <= MaxGenerationsPerWorkspace; i++ {
		man.Generations = append(man.Generations, GenerationRecord{
			ID:     formatGenerationID(uint64(i), "aaaaaaaa"),
			State:  GenerationSealed,
			Origin: OriginNative,
		})
	}
	if err := man.Validate(); err == nil {
		t.Fatal("a manifest over the generation bound was accepted")
	}
	if err := cat.Save(man); err == nil {
		t.Fatal("a manifest over the generation bound was written")
	}

	// Creating beyond the bound must be refused rather than growing forever.
	_, cat2, ctx2 := testCatalog(t)
	if _, err := cat2.CreateGeneration(ctx2, ""); err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	man2, err := cat2.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for i := len(man2.Generations); i <= MaxGenerationsPerWorkspace; i++ {
		man2.Generations = append(man2.Generations, GenerationRecord{
			ID:     formatGenerationID(uint64(1000+i), "bbbbbbbb"),
			State:  GenerationSealed,
			Origin: OriginNative,
		})
	}
	// Write it directly so the bound is reached without hundreds of creations.
	data := marshalForTest(t, man2)
	if err := os.WriteFile(cat2.ManifestPath(), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if _, err := cat2.CreateGeneration(ctx2, ""); err == nil {
		t.Fatal("CreateGeneration succeeded past the generation bound")
	}

	// An oversized manifest file must be refused on read rather than parsed.
	_, cat3, ctx3 := testCatalog(t)
	if _, err := cat3.CreateGeneration(ctx3, ""); err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	big := make([]byte, ManifestMaxBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(cat3.ManifestPath(), big, 0o644); err != nil {
		t.Fatalf("write oversized manifest: %v", err)
	}
	if _, err := cat3.Load(); err == nil {
		t.Fatal("an oversized manifest was accepted")
	}
}

// ---------------------------------------------------------------------------
// Context and cancellation
// ---------------------------------------------------------------------------

// Discovery and reconciliation must honour cancellation: a cancelled turn must
// not be held open by a store walk.
func TestDiscoverAndReconcileHonourCancellation(t *testing.T) {
	requireGit(t)
	m, cat, ctx := testCatalog(t)
	activeGeneration(t, ctx, cat)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.Discover(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Discover error = %v, want context.Canceled", err)
	}
	if _, err := m.Reconcile(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconcile error = %v, want context.Canceled", err)
	}
	if _, err := cat.CreateGeneration(cancelled, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateGeneration error = %v, want context.Canceled", err)
	}
	if _, err := m.MeasureWorkspaceGenerations(cancelled, cat.Workspace()); !errors.Is(err, context.Canceled) {
		t.Fatalf("MeasureWorkspaceGenerations error = %v, want context.Canceled", err)
	}
	if _, err := m.UsageContext(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("UsageContext error = %v, want context.Canceled", err)
	}
}

// ---------------------------------------------------------------------------
// Symlink refusal
// ---------------------------------------------------------------------------

// A symlink standing in for a generation directory must never be followed: a
// removal through it would delete whatever it points at.
func TestSymlinkedGenerationIsRefusedForRemoval(t *testing.T) {
	requireGit(t)
	_, cat, ctx := testCatalog(t)
	rec := activeGeneration(t, ctx, cat)
	if err := cat.SealGeneration(rec.ID); err != nil {
		t.Fatalf("SealGeneration: %v", err)
	}

	outside := t.TempDir()
	victim := filepath.Join(outside, "precious")
	if err := os.WriteFile(victim, []byte("do not delete"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	dir, _ := cat.GenerationDir(rec.ID)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove generation dir: %v", err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	if _, err := cat.MarkDeleting(rec.ID); err != nil {
		t.Fatalf("MarkDeleting: %v", err)
	}
	if err := cat.RemoveGenerationDirectory(rec.ID); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("RemoveGenerationDirectory error = %v, want ErrSymlinkEscape", err)
	}
	if _, err := os.Lstat(victim); err != nil {
		t.Fatalf("a symlinked generation directory was followed and its target removed: %v", err)
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("the symlink itself should be untouched: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// dirFingerprint is a content-and-shape fingerprint of a directory tree, used to
// prove a legacy store was not touched at all.
func dirFingerprint(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// A ref's mtime is irrelevant; its content and existence are not.
		kind := "f"
		if info.IsDir() {
			kind = "d"
		}
		b.WriteString(kind + " " + rel)
		if !info.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			b.WriteString(" " + string(data))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return b.String()
}

// marshalForTest encodes a manifest with the same encoder Save uses, so a test
// can write state that is valid JSON but over a bound.
func marshalForTest(t *testing.T, man *Manifest) []byte {
	t.Helper()
	data, err := marshalManifest(man)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	return data
}

// ---------------------------------------------------------------------------
// Timing
// ---------------------------------------------------------------------------

// Last-published timestamps must be durable, because oldest-first reclamation
// orders by them across workspaces.
func TestLastPublishedTimestampIsPersistedAndOrdered(t *testing.T) {
	requireGit(t)
	_, cat, ctx := testCatalog(t)

	old := activeGeneration(t, ctx, cat)
	hash := publishSnapshot(t, ctx, cat, old.ID)

	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	target := man.Generation(old.ID)
	if target.LastPublishedAt.IsZero() {
		t.Fatal("LastPublishedAt was not recorded")
	}
	// It must survive a round trip.
	if err := cat.Save(man); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := reloaded.Generation(old.ID); !got.LastPublishedAt.Equal(target.LastPublishedAt) {
		t.Fatalf("LastPublishedAt = %v, want %v", got.LastPublishedAt, target.LastPublishedAt)
	}
	_ = hash
}
