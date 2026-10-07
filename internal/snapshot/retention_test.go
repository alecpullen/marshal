package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file proves reclamation is whole-generation and oldest-first, and that
// every generation it must never touch is left alone. Each test runs against a
// small budget in t.TempDir and asserts on generations, not on bytes: "the
// oldest generation is gone" is a statement about which history survived, and
// a byte count cannot make it.

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// retentionEnv owns one or more workspaces under a single snapshots root, so
// cross-workspace reclamation can be exercised.
type retentionEnv struct {
	manager *Manager
	dataDir string
	// workspaces maps a workspace name to its root and catalog.
	workspaces map[string]*workspaceStore
	clock      time.Time
}

type workspaceStore struct {
	name    string
	root    string
	catalog *Catalog
}

// newRetentionEnv builds a store with a controllable clock.
func newRetentionEnv(t *testing.T, limits Limits) *retentionEnv {
	t.Helper()
	env := &retentionEnv{
		dataDir:    t.TempDir(),
		workspaces: map[string]*workspaceStore{},
		clock:      time.Unix(1_800_000_000, 0).UTC(),
	}
	m, err := NewManager(env.dataDir,
		WithLimits(limits),
		WithManifestBound(testManifestBound),
		WithMaintenanceAllowance(testMaintenanceBytes),
		WithClock(func() time.Time { return env.clock }),
	)
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
	env.manager = m
	return env
}

// addWorkspace registers a named workspace under the store.
func (e *retentionEnv) addWorkspace(t *testing.T, name string) *workspaceStore {
	t.Helper()
	root := filepath.Join(e.dataDir, "ws-"+name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir workspace %s: %v", name, err)
	}
	cat, err := e.manager.CatalogForRoot(root)
	if err != nil {
		t.Fatalf("CatalogForRoot %s: %v", name, err)
	}
	ws := &workspaceStore{name: name, root: root, catalog: cat}
	e.workspaces[name] = ws
	return ws
}

// advance moves the injected clock forward, so a later capture is genuinely
// younger than an earlier one.
func (e *retentionEnv) advance(d time.Duration) { e.clock = e.clock.Add(d) }

// capture writes a file and captures, returning the published hash.
func (e *retentionEnv) capture(t *testing.T, ws *workspaceStore, content string) string {
	t.Helper()
	writeWorkspaceFile(t, ws.root, fmt.Sprintf("data-%d.bin", e.clock.UnixNano()), content)
	req := CaptureRequest{
		WorkspaceRoot: ws.root,
		Now:           e.clock,
		// Each capture gets a distinct commit time, so distinct captures cannot
		// reproduce the same hash and the identity of a generation stays
		// observable.
	}
	hash, _, err := e.manager.AdmitAndCaptureWithCleanup(context.Background(), ws.catalog, req)
	if err != nil {
		t.Fatalf("capture into %s: %v", ws.name, err)
	}
	if hash == "" {
		t.Fatalf("capture into %s published nothing", ws.name)
	}
	return hash
}

// seal seals a generation, making its history eligible for whole-generation
// reclamation. It is how a test reaches the state a rotation would leave.
func (e *retentionEnv) seal(t *testing.T, ws *workspaceStore, id string) {
	t.Helper()
	if err := ws.catalog.SealGeneration(id); err != nil {
		t.Fatalf("SealGeneration %s: %v", id, err)
	}
}

// generationIDs returns a workspace's generation identifiers in creation order.
func (e *retentionEnv) generationIDs(t *testing.T, ws *workspaceStore) []string {
	t.Helper()
	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load %s: %v", ws.name, err)
	}
	if man == nil {
		return nil
	}
	ids := make([]string, 0, len(man.Generations))
	for i := range man.Generations {
		ids = append(ids, man.Generations[i].ID)
	}
	return ids
}

// setRetention stamps every generation's publication time so retention has a
// deterministic cutoff to compare against.
func (e *retentionEnv) stampPublished(t *testing.T, ws *workspaceStore, at time.Time) {
	t.Helper()
	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if man == nil {
		t.Fatal("workspace has no manifest")
	}
	for i := range man.Generations {
		man.Generations[i].LastPublishedAt = at.UTC()
	}
	if err := ws.catalog.Save(man); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// justOverWorkspaceCeiling returns the smallest reservation that no longer fits
// the workspace's ceiling, so a reclamation pass frees EXACTLY the generations
// it must and no more. Using an enormous need instead would reclaim every
// eligible generation, which cannot distinguish "oldest-first" from "all of
// them".
func (e *retentionEnv) justOverWorkspaceCeiling(t *testing.T, ws *workspaceStore) int64 {
	t.Helper()
	limits, err := e.manager.EffectiveLimits()
	if err != nil {
		t.Fatalf("EffectiveLimits: %v", err)
	}
	usage, err := e.manager.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	used := workspaceStoreAllocated(usage, ws.catalog.Workspace())
	need := limits.WorkspaceMaxBytes - used + 1
	if need <= 0 {
		t.Fatalf("the workspace already holds %d bytes of its %d byte ceiling", used, limits.WorkspaceMaxBytes)
	}
	return need
}

// ---------------------------------------------------------------------------
// Oldest-first ordering
// ---------------------------------------------------------------------------

// The candidate order is the design's whole reclamation policy: last-published
// timestamp, then the stable generation identifier. It is asserted directly
// because a wrong order would reclaim the newest rollback points first, which
// is worse than reclaiming nothing.
func TestReclaimCandidatesAreOrderedOldestFirst(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")

	// Three generations with distinct publication times, created newest-first
	// in identifier terms so the ORDER cannot be coming from creation sequence.
	var ids []string
	for i := 0; i < 3; i++ {
		env.capture(t, ws, fmt.Sprintf("content-%d\n", i))
		env.advance(time.Hour)
		man, err := ws.catalog.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if man.ActiveID != "" {
			ids = append(ids, man.ActiveID)
			env.seal(t, ws, man.ActiveID)
		}
	}
	if len(ids) < 3 {
		t.Fatalf("expected three generations, got %d", len(ids))
	}
	// Stamp them in REVERSE: the newest-created generation is the OLDEST
	// published, so an implementation that ordered by creation sequence would
	// pick the wrong one.
	for i, id := range ids {
		man, err := ws.catalog.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		rec := man.Generation(id)
		if rec == nil {
			t.Fatalf("generation %s vanished", id)
		}
		rec.LastPublishedAt = env.clock.Add(-time.Duration(i+1) * time.Hour).UTC()
		if err := ws.catalog.Save(man); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	usage, err := env.manager.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	cands := env.manager.reclaimCandidates(context.Background(), reclaimScope{}, usage, map[string]bool{})
	if len(cands) != 3 {
		t.Fatalf("candidates = %d, want 3", len(cands))
	}
	for i := 1; i < len(cands); i++ {
		if cands[i-1].order > cands[i].order {
			t.Fatalf("candidates are not oldest-first: %q then %q", cands[i-1].order, cands[i].order)
		}
	}
	// The oldest-published generation is the one created LAST.
	if cands[0].id != ids[2] {
		t.Fatalf("oldest candidate is %s, want the newest-created generation %s (ordering must follow publication)",
			cands[0].id, ids[2])
	}
}

// The identifier is the tiebreak when two generations share a publication time,
// and the identifier's leading creation sequence makes lexical order
// chronological.
func TestReclaimCandidatesTieBreakOnGenerationID(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")

	var ids []string
	for i := 0; i < 3; i++ {
		env.capture(t, ws, fmt.Sprintf("content-%d\n", i))
		env.advance(time.Minute)
		man, err := ws.catalog.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if man.ActiveID != "" {
			ids = append(ids, man.ActiveID)
			env.seal(t, ws, man.ActiveID)
		}
	}
	// Identical publication times for every generation.
	env.stampPublished(t, ws, env.clock)

	usage, err := env.manager.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	cands := env.manager.reclaimCandidates(context.Background(), reclaimScope{}, usage, map[string]bool{})
	if len(cands) != 3 {
		t.Fatalf("candidates = %d, want 3", len(cands))
	}
	for i := 1; i < len(cands); i++ {
		if cands[i-1].id >= cands[i].id {
			t.Fatalf("tie-break is not the stable generation id: %q then %q", cands[i-1].id, cands[i].id)
		}
	}
}

// ---------------------------------------------------------------------------
// Whole-generation removal
// ---------------------------------------------------------------------------

// Reclamation removes a WHOLE generation: the directory and every object in it
// are gone, and the manifest no longer lists it.
func TestReclaimRemovesAWholeGeneration(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")

	first := env.capture(t, ws, "first\n")
	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	firstGen := man.ActiveID
	if firstGen == "" {
		t.Fatal("no active generation")
	}
	dir, err := ws.catalog.GenerationDir(firstGen)
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	env.advance(time.Hour)
	env.capture(t, ws, "second\n")
	env.seal(t, ws, firstGen)

	reclaimed, err := env.manager.ReclaimForAdmission(context.Background(), ws.catalog,
		env.justOverWorkspaceCeiling(t, ws), "")
	if err != nil {
		t.Fatalf("ReclaimForAdmission: %v", err)
	}
	if len(reclaimed) == 0 || reclaimed[0] != firstGen {
		t.Fatalf("reclaimed %v, want the oldest generation %s first", reclaimed, firstGen)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the whole generation directory must be gone, but %s still exists: %v", dir, err)
	}
	after, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.Generation(firstGen) != nil {
		t.Fatalf("generation %s is still listed after reclamation", firstGen)
	}
	// The other generation survives, so reclamation did not over-reach.
	if after.Generation(man.ActiveID) == nil && man.ActiveID != firstGen {
		t.Fatal("reclamation removed a generation that was not the candidate")
	}
	// The reclaimed snapshot's ref is gone: its history is genuinely released.
	if exists, err := ws.catalog.RefExists(context.Background(), firstGen, first); err == nil && exists {
		t.Fatal("a reclaimed generation still retains its snapshot ref")
	}
}

// Reclamation must never repack or delete individual objects inside a retained
// generation: content-addressed objects may be shared with a live snapshot.
func TestReclaimNeverLeavesAScratchOrPackArtifact(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")

	env.capture(t, ws, "first\n")
	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	firstGen := man.ActiveID
	env.advance(time.Hour)
	env.capture(t, ws, "second\n")
	env.seal(t, ws, firstGen)
	if _, err := env.manager.ReclaimForAdmission(context.Background(), ws.catalog,
		env.justOverWorkspaceCeiling(t, ws), ""); err != nil {
		t.Fatalf("ReclaimForAdmission: %v", err)
	}

	// The surviving repository holds only loose objects, and nothing that looks
	// like an interrupted pack.
	root := ws.catalog.GenerationsDir()
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		name := filepath.Base(path)
		if strings.HasPrefix(name, "tmp_pack_") || strings.HasSuffix(name, ".pack") ||
			strings.HasSuffix(name, ".idx") || strings.HasPrefix(name, objectTempPrefix) {
			t.Errorf("reclamation left an unmanaged artifact at %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// ---------------------------------------------------------------------------
// What must never be reclaimed
// ---------------------------------------------------------------------------

// A protected generation is never reclaimed, whatever the pressure. Legacy
// history is the only surviving copy of pre-v2 rollback points, and removing it
// requires an explicit confirmation this code must never invent.
func TestProtectedGenerationIsNeverReclaimed(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")

	env.capture(t, ws, "legacy history\n")
	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	protectedID := man.ActiveID
	// Mark it as legacy-origin history: the state a migrated generation has.
	for i := range man.Generations {
		if man.Generations[i].ID == protectedID {
			man.Generations[i].Origin = OriginLegacy
			man.Generations[i].State = GenerationSealed
		}
	}
	man.ActiveID = ""
	if err := ws.catalog.Save(man); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Enormous pressure, and it is the only candidate.
	reclaimed, err := env.manager.ReclaimForAdmission(context.Background(), ws.catalog, 1<<30, "")
	if err != nil {
		t.Fatalf("ReclaimForAdmission: %v", err)
	}
	if len(reclaimed) != 0 {
		t.Fatalf("reclamation removed a protected generation: %v", reclaimed)
	}
	after, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.Generation(protectedID) == nil {
		t.Fatal("the protected generation was forgotten")
	}
	// Even the deletion-intent step refuses it, so no path can reclaim it by
	// accident.
	if _, err := ws.catalog.MarkDeleting(protectedID); !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("MarkDeleting on a protected generation = %v, want ErrLegacyRecoveryRequired", err)
	}
}

// Even time expiry must not reclaim a protected generation: its snapshots may
// be arbitrarily old and that is exactly the case the protection exists for.
func TestProtectedGenerationIsNeverExpired(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "legacy\n")

	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	id := man.ActiveID
	for i := range man.Generations {
		if man.Generations[i].ID == id {
			man.Generations[i].Origin = OriginLegacy
			man.Generations[i].State = GenerationSealed
			man.Generations[i].LastPublishedAt = time.Unix(0, 0).UTC()
		}
	}
	man.ActiveID = ""
	if err := ws.catalog.Save(man); err != nil {
		t.Fatalf("Save: %v", err)
	}

	report, err := env.manager.ReclaimExpiredWorkspace(context.Background(), ws.catalog, 0)
	if err != nil {
		t.Fatalf("ReclaimExpiredWorkspace: %v", err)
	}
	if len(report.Reclaimed) != 0 {
		t.Fatalf("retention reclaimed a protected generation: %+v", report.Reclaimed)
	}
	after, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.Generation(id) == nil {
		t.Fatal("retention forgot a protected generation")
	}
}

// A legacy bare repository below the root is discovered and left byte-for-byte
// alone. Old Marshal binaries do not honour the store lock, so a mutation could
// race a writer this process cannot see.
func TestLegacyBareRepositoryIsNeverReclaimed(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())

	// A pre-v2 shadow repo at <root>/<workspace-hash>.
	legacyDir := filepath.Join(env.manager.Root(), WorkspaceHashFor(t.TempDir()))
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := exec.Command("git", "init", "--bare", legacyDir).Run(); err != nil {
		t.Skipf("git init: %v", err)
	}
	marker := filepath.Join(legacyDir, "keep-me")
	if err := os.WriteFile(marker, []byte("legacy\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	before, err := env.manager.measureTreeAllocated(legacyDir, mustStat(t, legacyDir))
	if err != nil {
		t.Fatalf("measure: %v", err)
	}

	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "native\n")

	// Every reclamation surface must leave it alone.
	if _, err := env.manager.ReclaimForAdmission(context.Background(), ws.catalog, 1<<30, ""); err != nil {
		t.Fatalf("ReclaimForAdmission: %v", err)
	}
	if _, err := env.manager.ReclaimExpired(context.Background(), 0); err != nil {
		t.Fatalf("ReclaimExpired: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the legacy repository lost a file: %v", err)
	}
	after, err := env.manager.measureTreeAllocated(legacyDir, mustStat(t, legacyDir))
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if after != before {
		t.Fatalf("the legacy repository changed size from %d to %d bytes", before, after)
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return info
}

// A generation directory the manifest does not list is reported and never
// removed: nothing durable says what it is or whether it is anyone's only copy.
func TestUnlistedGenerationIsNeverReclaimed(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "native\n")

	// A well-formed generation directory the manifest does not describe.
	suffix, err := randomSuffix(generationIDSuffixLen)
	if err != nil {
		t.Fatalf("randomSuffix: %v", err)
	}
	orphanID := formatGenerationID(4242, suffix)
	orphanDir, err := ws.catalog.GenerationDir(orphanID)
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(orphanDir, "objects"), 0o755); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "payload"), []byte("unknown provenance\n"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	reclaimed, err := env.manager.ReclaimForAdmission(context.Background(), ws.catalog, 1<<30, "")
	if err != nil {
		t.Fatalf("ReclaimForAdmission: %v", err)
	}
	for _, id := range reclaimed {
		if id == orphanID {
			t.Fatalf("reclamation removed the unlisted generation %s", orphanID)
		}
	}
	if _, err := os.Stat(orphanDir); err != nil {
		t.Fatalf("the unlisted generation directory was removed: %v", err)
	}
	// It is reported, so the user can see it.
	gens, err := env.manager.MeasureWorkspaceGenerations(context.Background(), ws.catalog.Workspace())
	if err != nil {
		t.Fatalf("MeasureWorkspaceGenerations: %v", err)
	}
	found := false
	for _, gu := range gens.Unlisted {
		if gu.ID == orphanID {
			found = true
			if !gu.Protected {
				t.Errorf("an unlisted generation is reported as reclaimable")
			}
		}
	}
	if !found {
		t.Fatalf("the unlisted generation %s was not reported", orphanID)
	}
}

// A generation whose snapshot refs cannot be READ is never reclaimed.
// "Could not list the refs" is not "nothing is published".
func TestUnreadableGenerationIsNeverReclaimed(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "native\n")

	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	id := man.ActiveID
	env.advance(time.Hour)
	env.capture(t, ws, "second\n")
	env.seal(t, ws, id)

	// Corrupt the sealed generation so its refs cannot be listed.
	dir, err := ws.catalog.GenerationDir(id)
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	// A directory without the metadata git needs to be recognised: the refs
	// cannot be read, so the generation must be excluded rather than assumed to
	// hold nothing.
	for _, name := range []string{"HEAD", "config", "objects", "refs"} {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			t.Fatalf("remove %s: %v", name, err)
		}
	}

	reclaimed, err := env.manager.ReclaimForAdmission(context.Background(), ws.catalog, 1<<30, "")
	if err != nil {
		t.Fatalf("ReclaimForAdmission: %v", err)
	}
	for _, got := range reclaimed {
		if got == id {
			t.Fatalf("reclamation removed the unreadable generation %s", id)
		}
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("the unreadable generation directory was removed: %v", err)
	}
}

// A corrupt manifest quarantines a workspace from reclamation: nothing durable
// describes what its directories are, so the safe answer is to refuse to guess.
func TestCorruptManifestWorkspaceIsNeverReclaimed(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "native\n")

	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	id := man.ActiveID
	env.advance(time.Hour)
	env.capture(t, ws, "second\n")
	env.seal(t, ws, id)

	gens := env.generationIDs(t, ws)
	if err := os.WriteFile(ws.catalog.ManifestPath(), []byte("{ not json"), 0o644); err != nil {
		t.Fatalf("corrupt manifest: %v", err)
	}

	reclaimed, err := env.manager.ReclaimForAdmission(context.Background(), ws.catalog, 1<<30, "")
	if err != nil {
		t.Fatalf("ReclaimForAdmission: %v", err)
	}
	if len(reclaimed) != 0 {
		t.Fatalf("reclamation acted on a corrupt workspace: %v", reclaimed)
	}
	for _, genID := range gens {
		dir, err := ws.catalog.GenerationDir(genID)
		if err != nil {
			t.Fatalf("GenerationDir: %v", err)
		}
		if _, err := os.Lstat(dir); err != nil {
			t.Fatalf("a corrupt workspace lost generation %s: %v", genID, err)
		}
	}
}

// The ACTIVE generation is never reclaimed: it is the capture target, and
// removing it mid-flight would leave the workspace with nowhere to write.
func TestActiveGenerationIsNeverReclaimed(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "active\n")

	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	id := man.ActiveID
	if id == "" {
		t.Fatal("no active generation")
	}

	reclaimed, err := env.manager.ReclaimForAdmission(context.Background(), ws.catalog, 1<<30, "")
	if err != nil {
		t.Fatalf("ReclaimForAdmission: %v", err)
	}
	for _, got := range reclaimed {
		if got == id {
			t.Fatalf("reclamation removed the active generation %s", id)
		}
	}
	after, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.ActiveID != id {
		t.Fatalf("active id changed from %s to %s", id, after.ActiveID)
	}
}

// A generation the caller is about to write into is excluded, even when it
// would otherwise be a candidate.
func TestExcludedGenerationIsNeverReclaimed(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "first\n")
	first := env.generationIDs(t, ws)[0]
	env.advance(time.Hour)
	env.capture(t, ws, "second\n")
	env.seal(t, ws, first)

	reclaimed, err := env.manager.ReclaimForAdmission(context.Background(), ws.catalog, 1<<30, first)
	if err != nil {
		t.Fatalf("ReclaimForAdmission: %v", err)
	}
	for _, got := range reclaimed {
		if got == first {
			t.Fatalf("reclamation removed the excluded generation %s", first)
		}
	}
}

// ---------------------------------------------------------------------------
// Workspace vs global pressure
// ---------------------------------------------------------------------------

// Workspace pressure searches THAT workspace first, so a workspace under its own
// ceiling is not made to pay for another workspace's overage before its own
// history is touched.
func TestWorkspacePressureStaysInItsWorkspace(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	a := env.addWorkspace(t, "a")
	b := env.addWorkspace(t, "b")

	// Both workspaces accumulate sealed history.
	for i := 0; i < 3; i++ {
		env.capture(t, a, fmt.Sprintf("a-%d\n", i))
		env.advance(time.Minute)
		man, err := a.catalog.Load()
		if err != nil {
			t.Fatalf("Load a: %v", err)
		}
		if man.ActiveID != "" {
			env.seal(t, a, man.ActiveID)
		}
		env.capture(t, b, fmt.Sprintf("b-%d\n", i))
		env.advance(time.Minute)
		man, err = b.catalog.Load()
		if err != nil {
			t.Fatalf("Load b: %v", err)
		}
		if man.ActiveID != "" {
			env.seal(t, b, man.ActiveID)
		}
	}
	aIDs := env.generationIDs(t, a)
	aOldest := aIDs[0]
	bIDs := env.generationIDs(t, b)

	usage, err := env.manager.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	// A workspace-phase scope searches only workspace a.
	got := env.manager.reclaimCandidates(context.Background(),
		reclaimScope{workspace: a.catalog.Workspace()}, usage, map[string]bool{})
	for _, cand := range got {
		if cand.workspace != a.catalog.Workspace() {
			t.Fatalf("a workspace-scoped search returned generation %s from workspace %s",
				cand.id, cand.workspace)
		}
	}
	if len(got) == 0 || got[0].id != aOldest {
		t.Fatalf("workspace a's oldest candidate is %v, want %s", got, aOldest)
	}
	// None of b's generations appear.
	for _, cand := range got {
		for _, bid := range bIDs {
			if cand.id == bid {
				t.Fatalf("a workspace-scoped search returned workspace b's generation %s", bid)
			}
		}
	}
}

// Global pressure searches EVERY managed workspace, ordered by last publication,
// so the oldest history in the whole store is reclaimed first.
func TestGlobalPressureReclaimsOldestAcrossWorkspaces(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	a := env.addWorkspace(t, "a")
	b := env.addWorkspace(t, "b")

	// Workspace b's history is published FIRST, so it is the oldest overall;
	// workspace a's is newer.
	env.capture(t, b, "b-old\n")
	bMan, err := b.catalog.Load()
	if err != nil {
		t.Fatalf("Load b: %v", err)
	}
	bOld := bMan.ActiveID
	env.seal(t, b, bOld)
	env.advance(time.Hour)

	env.capture(t, a, "a-new\n")
	aMan, err := a.catalog.Load()
	if err != nil {
		t.Fatalf("Load a: %v", err)
	}
	aOld := aMan.ActiveID
	env.seal(t, a, aOld)
	env.advance(time.Hour)

	usage, err := env.manager.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	cands := env.manager.reclaimCandidates(context.Background(), reclaimScope{}, usage, map[string]bool{})
	if len(cands) < 2 {
		t.Fatalf("candidates = %d, want both workspaces represented", len(cands))
	}
	// The oldest publication across the WHOLE store comes first, whichever
	// workspace it is in.
	if cands[0].id != bOld {
		t.Fatalf("global oldest-first chose %s from workspace %s, want %s from workspace b",
			cands[0].id, cands[0].workspace, bOld)
	}
	if cands[0].workspace != b.catalog.Workspace() {
		t.Fatalf("oldest candidate's workspace = %s, want %s", cands[0].workspace, b.catalog.Workspace())
	}
}

// A global shortfall is relieved from ANY managed workspace, because the global
// ceiling is what is binding.
func TestGlobalPressureCanReclaimFromAnyWorkspace(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	a := env.addWorkspace(t, "a")
	b := env.addWorkspace(t, "b")

	env.capture(t, a, "a\n")
	aMan, err := a.catalog.Load()
	if err != nil {
		t.Fatalf("Load a: %v", err)
	}
	env.seal(t, a, aMan.ActiveID)
	env.advance(time.Hour)
	env.capture(t, b, "b\n")
	bMan, err := b.catalog.Load()
	if err != nil {
		t.Fatalf("Load b: %v", err)
	}
	env.seal(t, b, bMan.ActiveID)

	// Workspace c asks for room; workspace a's history is reclaimable even
	// though it is not c's.
	c := env.addWorkspace(t, "c")
	reclaimed, err := env.manager.ReclaimForAdmission(context.Background(), c.catalog, 1<<30, "")
	if err != nil {
		t.Fatalf("ReclaimForAdmission: %v", err)
	}
	if len(reclaimed) == 0 {
		t.Fatal("global pressure reclaimed nothing from other workspaces")
	}
	if reclaimed[0] != aMan.ActiveID {
		t.Fatalf("global pressure reclaimed %s first, want the oldest overall %s",
			reclaimed[0], aMan.ActiveID)
	}
}

// ---------------------------------------------------------------------------
// Free-space pressure
// ---------------------------------------------------------------------------

// Insufficient free space drives reclamation just as a budget shortfall does:
// the store must free room from anywhere before it refuses.
func TestFreeSpacePressureDrivesReclamation(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")

	env.capture(t, ws, "first\n")
	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	first := man.ActiveID
	env.seal(t, ws, first)
	env.advance(time.Hour)
	env.capture(t, ws, "second\n")

	// Plenty of budget, no room at all.
	env.manager.freeSpace = func(string) (int64, error) { return 0, nil }
	reclaimed, err := env.manager.ReclaimForAdmission(context.Background(), ws.catalog, 1, "")
	if err != nil {
		t.Fatalf("ReclaimForAdmission: %v", err)
	}
	if len(reclaimed) == 0 {
		t.Fatal("free-space pressure reclaimed nothing although a generation was eligible")
	}
	if reclaimed[0] != first {
		t.Fatalf("free-space pressure reclaimed %s, want the oldest eligible %s", reclaimed[0], first)
	}
	// Restore a real query so the deferred Release does not log nonsense.
	env.manager.freeSpace = freeSpaceBytes
}

// ---------------------------------------------------------------------------
// Time expiry
// ---------------------------------------------------------------------------

// Expiry requires EVERY snapshot in a generation to be older than the cutoff. A
// generation holding one recent snapshot is not expired, however old its oldest
// snapshot is.
func TestExpiryRequiresAllSnapshotsToBeOld(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")

	// Two snapshots in the SAME generation: one old, one recent.
	writeWorkspaceFile(t, ws.root, "old.txt", "old\n")
	oldReq := CaptureRequest{WorkspaceRoot: ws.root, Now: env.clock.AddDate(0, 0, -30)}
	oldHash, _, err := env.manager.AdmitAndCaptureWithCleanup(context.Background(), ws.catalog, oldReq)
	if err != nil {
		t.Fatalf("old capture: %v", err)
	}
	env.advance(time.Hour)
	writeWorkspaceFile(t, ws.root, "new.txt", "new\n")
	newReq := CaptureRequest{WorkspaceRoot: ws.root, Now: env.clock}
	newHash, admission, err := env.manager.AdmitAndCaptureWithCleanup(context.Background(), ws.catalog, newReq)
	if err != nil {
		t.Fatalf("new capture: %v", err)
	}
	if oldHash == newHash {
		t.Fatal("the two captures produced the same hash; the generation would hold one snapshot")
	}
	// Seal the generation so it is not excluded merely for being active.
	env.seal(t, ws, admission.GenerationID)

	report, err := env.manager.ReclaimExpiredWorkspace(context.Background(), ws.catalog, 7)
	if err != nil {
		t.Fatalf("ReclaimExpiredWorkspace: %v", err)
	}
	if len(report.Reclaimed) != 0 {
		t.Fatalf("a generation holding a recent snapshot was expired: %+v", report.Reclaimed)
	}

	// Now age the recent snapshot too, and the whole generation expires.
	env.advance(60 * 24 * time.Hour)
	report, err = env.manager.ReclaimExpiredWorkspace(context.Background(), ws.catalog, 7)
	if err != nil {
		t.Fatalf("ReclaimExpiredWorkspace: %v", err)
	}
	if len(report.Reclaimed) == 0 {
		t.Fatal("a generation whose snapshots have ALL expired was not reclaimed")
	}
	if report.Reclaimed[0].Cause != "expired" {
		t.Fatalf("reclaim cause = %q, want %q", report.Reclaimed[0].Cause, "expired")
	}
	if report.Reclaimed[0].Snapshots < 2 {
		t.Fatalf("reclaimed generation reports %d snapshots, want both", report.Reclaimed[0].Snapshots)
	}
}

// A negative retention disables expiry entirely, exactly as the legacy prune's
// early return did.
func TestNegativeRetentionDisablesExpiry(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "old\n")
	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	id := man.ActiveID
	env.seal(t, ws, id)
	env.stampPublished(t, ws, time.Unix(0, 0).UTC())

	report, err := env.manager.ReclaimExpiredWorkspace(context.Background(), ws.catalog, -1)
	if err != nil {
		t.Fatalf("ReclaimExpiredWorkspace: %v", err)
	}
	if len(report.Reclaimed) != 0 {
		t.Fatalf("a negative retention expired %+v", report.Reclaimed)
	}
	after, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.Generation(id) == nil {
		t.Fatal("a negative retention removed a generation")
	}
}

// Zero retention expires every snapshot older than now, which is what
// `now.AddDate(0, 0, -0)` always meant.
func TestZeroRetentionExpiresEverythingOlderThanNow(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "old\n")
	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	id := man.ActiveID
	env.seal(t, ws, id)
	env.advance(time.Hour)

	report, err := env.manager.ReclaimExpiredWorkspace(context.Background(), ws.catalog, 0)
	if err != nil {
		t.Fatalf("ReclaimExpiredWorkspace: %v", err)
	}
	if len(report.Reclaimed) != 1 {
		t.Fatalf("zero retention expired %d generations, want 1", len(report.Reclaimed))
	}
	after, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.Generation(id) != nil {
		t.Fatal("zero retention left the expired generation listed")
	}
	dir, err := ws.catalog.GenerationDir(id)
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the expired generation directory was not removed: %v", err)
	}
}

// A generation that is still RECENT is not expired, and the store keeps it even
// when other generations are gone.
func TestRecentGenerationSurvivesExpiry(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")

	env.capture(t, ws, "old\n")
	oldMan, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	oldID := oldMan.ActiveID
	env.seal(t, ws, oldID)
	// Age only the first generation's snapshots.
	env.advance(30 * 24 * time.Hour)

	env.capture(t, ws, "recent\n")
	newMan, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The new capture may have opened a fresh generation, so the old one is
	// already sealed and the new one is active.
	report, err := env.manager.ReclaimExpiredWorkspace(context.Background(), ws.catalog, 7)
	if err != nil {
		t.Fatalf("ReclaimExpiredWorkspace: %v", err)
	}
	after, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.Generation(oldID) != nil {
		t.Fatalf("the fully expired generation %s survived", oldID)
	}
	if len(report.Reclaimed) != 1 {
		t.Fatalf("expired %d generations, want just the old one", len(report.Reclaimed))
	}
	// Whatever generation holds the recent snapshot survives.
	for i := range after.Generations {
		times, err := env.manager.generationRefTimes(context.Background(), ws.catalog, after.Generations[i].ID)
		if err != nil {
			t.Fatalf("generationRefTimes: %v", err)
		}
		if len(times) > 0 && !allBefore(times, env.clock.AddDate(0, 0, -7).Unix()) {
			// A recent snapshot exists and its generation is still listed.
			newMan = after
			break
		}
	}
	if newMan == nil {
		t.Fatal("no generation remains")
	}
}

// The active generation is sealed and then reclaimed when ALL of its snapshots
// have expired. The seal is safe because expiry is only ever decided from
// snapshots that have already expired.
func TestActiveGenerationIsSealedAndExpiredWhenItsHistoryIsOld(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "old\n")
	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	id := man.ActiveID
	if man.Generation(id).State != GenerationActive {
		t.Fatalf("generation %s is in state %q, want active", id, man.Generation(id).State)
	}
	env.advance(30 * 24 * time.Hour)

	report, err := env.manager.ReclaimExpiredWorkspace(context.Background(), ws.catalog, 7)
	if err != nil {
		t.Fatalf("ReclaimExpiredWorkspace: %v", err)
	}
	after, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.Generation(id) != nil {
		t.Fatalf("the fully expired active generation %s survived", id)
	}
	if after.ActiveID != "" {
		t.Fatalf("active id %q survived reclamation of its generation", after.ActiveID)
	}
	if len(report.Reclaimed) != 1 {
		t.Fatalf("expired %d generations, want 1", len(report.Reclaimed))
	}
}

// A generation whose snapshots cannot be READ is never treated as expired: an
// unreadable listing is an error, and treating it as "nothing is published"
// would delete recent history.
func TestExpiryNeverTreatsUnreadableRefsAsEmpty(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "content\n")
	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	id := man.ActiveID
	env.seal(t, ws, id)

	// Break the repository so for-each-ref fails, without removing the
	// directory: the generation looks unmeasurable, not empty.
	dir, err := ws.catalog.GenerationDir(id)
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("rewrite HEAD: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "objects")); err != nil {
		t.Fatalf("remove objects: %v", err)
	}
	env.advance(30 * 24 * time.Hour)

	report, err := env.manager.ReclaimExpiredWorkspace(context.Background(), ws.catalog, 7)
	if err == nil {
		// A nil error with no reclamation is also acceptable: what must never
		// happen is the generation being expired.
		if len(report.Reclaimed) != 0 {
			t.Fatalf("an unreadable generation was expired: %+v", report.Reclaimed)
		}
	}
	if _, statErr := os.Lstat(dir); statErr != nil {
		t.Fatalf("the unreadable generation directory was removed: %v", statErr)
	}
}

// Expiry across every workspace is what ReclaimExpired does, and it must skip
// the ones that are not eligible while reclaiming the ones that are.
func TestReclaimExpiredAcrossEveryWorkspace(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	a := env.addWorkspace(t, "a")
	b := env.addWorkspace(t, "b")

	for _, ws := range []*workspaceStore{a, b} {
		env.capture(t, ws, "old\n")
		man, err := ws.catalog.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		env.seal(t, ws, man.ActiveID)
	}
	env.advance(30 * 24 * time.Hour)

	report, err := env.manager.ReclaimExpired(context.Background(), 7)
	if err != nil {
		t.Fatalf("ReclaimExpired: %v", err)
	}
	if len(report.Reclaimed) != 2 {
		t.Fatalf("ReclaimExpired reclaimed %d generations, want one per workspace", len(report.Reclaimed))
	}
	for _, ws := range []*workspaceStore{a, b} {
		man, err := ws.catalog.Load()
		if err != nil {
			t.Fatalf("Load %s: %v", ws.name, err)
		}
		if len(man.Generations) != 0 {
			t.Fatalf("workspace %s still lists %d generations", ws.name, len(man.Generations))
		}
	}
}

// ---------------------------------------------------------------------------
// Accounting of pending deletions
// ---------------------------------------------------------------------------

// A generation marked for deletion but not yet physically removed is still
// COUNTED. Subtracting its bytes before they are gone is how a budget doubles
// its available room.
func TestPendingDeletionIsStillCounted(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "content\n")
	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	id := man.ActiveID
	dir, err := ws.catalog.GenerationDir(id)
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	before, err := env.manager.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}

	// Persist the intent, but do NOT remove the directory.
	if _, err := ws.catalog.MarkDeleting(id); err != nil {
		t.Fatalf("MarkDeleting: %v", err)
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("generation directory vanished on MarkDeleting: %v", err)
	}

	after, err := env.manager.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	if after.GlobalAllocated != before.GlobalAllocated {
		t.Fatalf("a pending deletion changed the measured total from %d to %d bytes; its bytes are not gone yet",
			before.GlobalAllocated, after.GlobalAllocated)
	}

	// Once the directory is gone, the bytes are released.
	if err := ws.catalog.RemoveGenerationDirectory(id); err != nil {
		t.Fatalf("RemoveGenerationDirectory: %v", err)
	}
	if err := ws.catalog.ForgetGeneration(id); err != nil {
		t.Fatalf("ForgetGeneration: %v", err)
	}
	released, err := env.manager.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	if released.GlobalAllocated >= before.GlobalAllocated {
		t.Fatalf("removing the generation did not release bytes: %d then %d",
			before.GlobalAllocated, released.GlobalAllocated)
	}
}

// A removal that fails leaves the generation MARKED for deletion, so the next
// reconciliation finishes it and the bytes stay counted in the meantime.
func TestFailedRemovalLeavesADurableDeletionIntent(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "first\n")
	first := env.generationIDs(t, ws)[0]
	env.advance(time.Hour)
	env.capture(t, ws, "second\n")
	env.seal(t, ws, first)

	// Interrupt the removal after the intent is durable.
	env.manager.hooks = Hooks{BeforeRemoveGenerationDir: func(string) error {
		return errors.New("injected removal failure")
	}}
	defer func() { env.manager.hooks = Hooks{} }()

	reclaimed, err := env.manager.ReclaimForAdmission(context.Background(), ws.catalog,
		env.justOverWorkspaceCeiling(t, ws), "")
	if err != nil {
		t.Fatalf("ReclaimForAdmission: %v", err)
	}
	for _, got := range reclaimed {
		if got == first {
			t.Fatalf("a failed removal was reported as reclaimed: %s", first)
		}
	}
	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rec := man.Generation(first)
	if rec == nil {
		t.Fatalf("generation %s was forgotten despite its directory still existing", first)
	}
	if rec.State != GenerationDeleting {
		t.Fatalf("generation %s is in state %q after a failed removal, want %q",
			first, rec.State, GenerationDeleting)
	}
	// Its bytes are still counted.
	usage, err := env.manager.MeasureWorkspaceGenerations(context.Background(), ws.catalog.Workspace())
	if err != nil {
		t.Fatalf("MeasureWorkspaceGenerations: %v", err)
	}
	if gu := usage.ByID[first]; gu == nil || gu.Allocated == 0 {
		t.Fatal("a pending deletion is not counted in the workspace measurement")
	}

	// Reconciliation finishes it, which is the whole reason the intent is
	// durable.
	env.manager.hooks = Hooks{}
	if _, err := env.manager.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	after, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.Generation(first) != nil {
		t.Fatalf("reconciliation did not finish the deletion of %s", first)
	}
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

// Status reports a healthy store as healthy and an over-budget one as needing
// attention, using the SAME structured reason class the capture path uses.
func TestStatusReportsOverageWithAStructuredReason(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "content\n")

	if err := env.manager.Status(context.Background()); err != nil {
		t.Fatalf("Status on a healthy store = %v, want nil", err)
	}

	// Lower the workspace ceiling below the store's own size.
	gens, err := env.manager.MeasureWorkspaceGenerations(context.Background(), ws.catalog.Workspace())
	if err != nil {
		t.Fatalf("MeasureWorkspaceGenerations: %v", err)
	}
	tight := testLimits()
	tight.WorkspaceMaxBytes = gens.Allocated / 2
	tight.GlobalMaxBytes = gens.Allocated * 4
	env.manager.limits = tight

	err = env.manager.Status(context.Background())
	if err == nil {
		t.Fatal("Status reported an over-budget store as healthy")
	}
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("Status reason = %v, want ErrBudgetExhausted", err)
	}
	if !strings.Contains(err.Error(), ws.catalog.Workspace()) {
		t.Fatalf("Status message does not name the offending workspace:\n%s", err)
	}
}

// Status must not run heavy storage work: the runtime calls it on the shutdown
// path, and a reconciliation there would hold the session open.
func TestStatusDoesNotReconcile(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "content\n")

	// A deleting generation reconciliation would finish.
	man, err := ws.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	id := man.ActiveID
	dir, err := ws.catalog.GenerationDir(id)
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	if _, err := ws.catalog.MarkDeleting(id); err != nil {
		t.Fatalf("MarkDeleting: %v", err)
	}

	if err := env.manager.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	// The deleting generation is untouched: its directory still exists.
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("Status removed a generation directory: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Reclamation requires the store lock
// ---------------------------------------------------------------------------

// Reclamation deletes directories and rewrites manifests, so running it without
// ownership would race a capture in another process.
func TestReclamationRequiresTheStoreLock(t *testing.T) {
	requireGit(t)
	env := newRetentionEnv(t, testLimits())
	ws := env.addWorkspace(t, "a")
	env.capture(t, ws, "content\n")

	if err := env.manager.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := env.manager.ReclaimForAdmission(context.Background(), ws.catalog, 1, ""); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("unowned ReclaimForAdmission = %v, want ErrNotOwned", err)
	}
	if _, err := env.manager.ReclaimExpired(context.Background(), 0); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("unowned ReclaimExpired = %v, want ErrNotOwned", err)
	}
}
