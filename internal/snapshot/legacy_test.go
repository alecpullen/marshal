package snapshot

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file pins legacy DETECTION: what is found, what is reported as unknown,
// and — most importantly — that nothing in the detection path writes, prunes, or
// repacks. Every fixture is built with the real Git binary under t.TempDir, so
// no test can reach the user's real ~/.local/share/marshal.

// legacyFixture builds a pre-v2 bare repository under a temp store root and
// returns the manager, the context, the workspace hash, and the store path.
//
// The repository is created exactly as the old shadow-repo code did:
// `<snapshots>/<workspace-hash>/`, with snapshot refs under refs/snapshots/ and
// — crucially — a BRANCH, because a branch is the second retention root that
// made the original defect possible.
type legacyFixture struct {
	m         *Manager
	ctx       context.Context
	workspace string
	path      string
}

func newLegacyFixture(t *testing.T, opts ...ManagerOption) *legacyFixture {
	t.Helper()
	requireGit(t)
	dataDir := t.TempDir()
	m, err := NewManager(dataDir, opts...)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	// An explicit workspace hash keeps the fixture deterministic and makes the
	// "the old layout records only a hash" property directly assertable.
	workspace := "0123456789ab"
	path := filepath.Join(m.Root(), workspace)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir legacy store: %v", err)
	}
	ctx := context.Background()
	gitEnv(t, ctx, path, "init", "--bare")
	return &legacyFixture{m: m, ctx: ctx, workspace: workspace, path: path}
}

// commitChain writes n commits in a PARENT CHAIN and points refs/heads/master at
// the tip, reproducing the old layout's shape: history reachable through a
// branch, with every parent behind it also reachable.
//
// The chain is what the original defect turned on. Pruning the snapshot refs
// freed nothing while a branch named the tip, because every commit in the chain
// stayed reachable through the parent link.
func (f *legacyFixture) commitChain(t *testing.T, n int, blobSize int) []string {
	t.Helper()
	parent := ""
	var commits []string
	for i := 0; i < n; i++ {
		// The content is UNIQUE per commit. Identical content would deduplicate
		// to one blob and one tree, and the object counts below (which pin that
		// the whole chain is enumerated) would then pass for the wrong reason.
		content := strings.Repeat("x", blobSize) + "\ncommit " + strconv.Itoa(i) + "\n"
		cmd := newGitCmd(f.ctx, "git", f.path, "", false, "hash-object", "-w", "-t", "blob", "--stdin")
		cmd.Stdin = strings.NewReader(content)
		out, err := runGitCombined(f.ctx, cmd, defaultMaxCommandOutput)
		if err != nil {
			t.Fatalf("hash-object: %v\n%s", err, out)
		}
		blobHash := strings.TrimSpace(string(out))

		treeCmd := newGitCmd(f.ctx, "git", f.path, "", false, "mktree")
		treeCmd.Stdin = strings.NewReader("100644 blob " + blobHash + "\tlarge.bin\n")
		out, err = runGitCombined(f.ctx, treeCmd, defaultMaxCommandOutput)
		if err != nil {
			t.Fatalf("mktree: %v\n%s", err, out)
		}
		treeHash := strings.TrimSpace(string(out))

		args := []string{"commit-tree", treeHash}
		if parent != "" {
			args = append(args, "-p", parent)
		}
		commitCmd := newGitCmd(f.ctx, "git", f.path, "", false, args...)
		commitCmd.Stdin = strings.NewReader("snapshot " + strconv.Itoa(i) + "\n")
		commitCmd.Env = append(commitCmd.Env,
			"GIT_AUTHOR_NAME=marshal", "GIT_AUTHOR_EMAIL=marshal@local",
			"GIT_COMMITTER_NAME=marshal", "GIT_COMMITTER_EMAIL=marshal@local",
		)
		out, err = runGitCombined(f.ctx, commitCmd, defaultMaxCommandOutput)
		if err != nil {
			t.Fatalf("commit-tree: %v\n%s", err, out)
		}
		parent = strings.TrimSpace(string(out))
		commits = append(commits, parent)
	}
	// The branch is the retention root the original defect hinged on.
	gitEnv(t, f.ctx, f.path, "update-ref", "refs/heads/master", parent)
	// And the tip is ALSO published as a snapshot ref, exactly as the old
	// capture code did when a turn ended.
	gitEnv(t, f.ctx, f.path, "update-ref", snapshotRefPrefix+parent, parent)
	return commits
}

// abandonTempPack plants a recognized `tmp_pack_*` artifact, which is what an
// interrupted `git gc` leaves behind.
//
// The file is BACKDATED past the live-writer freshness window, because that is
// what genuinely abandoned debris is: the interruption happened before the
// operator sat down to recover, not in the seconds since. A test that wants the
// fresh (possibly-active-repack) case uses plantFreshTempPack instead.
func (f *legacyFixture) abandonTempPack(t *testing.T, name string, size int) string {
	t.Helper()
	path := f.plantTempPack(t, name, size)
	if err := os.Chtimes(path, liveWriterPast, liveWriterPast); err != nil {
		t.Fatalf("backdate temp pack: %v", err)
	}
	return path
}

// plantFreshTempPack plants a temporary artifact whose modification time is NOW,
// which is indistinguishable from an active repack's work in progress.
func (f *legacyFixture) plantFreshTempPack(t *testing.T, name string, size int) string {
	t.Helper()
	return f.plantTempPack(t, name, size)
}

// plantTempPack writes a `tmp_pack_*` file with its default (current) times.
func (f *legacyFixture) plantTempPack(t *testing.T, name string, size int) string {
	t.Helper()
	packDir := filepath.Join(f.path, "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatalf("mkdir pack dir: %v", err)
	}
	path := filepath.Join(packDir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write temp pack: %v", err)
	}
	return path
}

// liveWriterPast is comfortably outside DefaultLiveWriterWindow, so a fixture
// dated with it is unambiguously abandoned debris rather than an active repack.
var liveWriterPast = time.Now().Add(-2 * DefaultLiveWriterWindow)

// ---------------------------------------------------------------------------
// Detection
// ---------------------------------------------------------------------------

// A legacy store with a parent CHAIN and a formerly captured large file is
// detected as legacy, with branch reachability, retained refs, and the reachable
// closure measured — and abandoned tmp_pack_* artifacts are recognized.
func TestLegacyDetectsChainBranchesAndTempPacks(t *testing.T) {
	f := newLegacyFixture(t)
	commits := f.commitChain(t, 3, 4096)
	f.abandonTempPack(t, "tmp_pack_AbCdEf", 8192)

	survey, err := f.m.SurveyLegacy(f.ctx)
	if err != nil {
		t.Fatalf("SurveyLegacy: %v", err)
	}
	li := survey.Store(f.workspace)
	if li == nil {
		t.Fatalf("the legacy store was not detected; survey has %d stores", len(survey.Stores))
	}
	if !li.Present || !li.IsRepository {
		t.Fatalf("detected store = present %v, repository %v, want both true", li.Present, li.IsRepository)
	}
	if li.Root != "" {
		t.Fatalf("legacy root = %q, want empty: the old layout records only a workspace hash", li.Root)
	}

	// Retained refs: the tip is published, and the branch is a second root.
	if li.SnapshotRefs != 1 {
		t.Fatalf("snapshot refs = %d, want 1", li.SnapshotRefs)
	}
	if li.SnapshotHashes[0] != commits[len(commits)-1] {
		t.Fatalf("snapshot hash = %s, want the chain tip %s", li.SnapshotHashes[0], commits[len(commits)-1])
	}
	if len(li.Branches) != 1 || li.Branches[0].Ref != "refs/heads/master" {
		t.Fatalf("branches = %+v, want refs/heads/master", li.Branches)
	}
	if li.Branches[0].Hash != commits[len(commits)-1] {
		t.Fatalf("branch hash = %s, want %s", li.Branches[0].Hash, commits[len(commits)-1])
	}

	// The closure covers the WHOLE chain, not just the tip: every parent is
	// reachable through the branch, which is the defect.
	if li.ClosureError != nil {
		t.Fatalf("closure error: %v", li.ClosureError)
	}
	// 3 blobs + 3 trees + 3 commits.
	if li.Objects != 9 {
		t.Fatalf("closure enumerated %d objects, want 9 (the whole parent chain)", li.Objects)
	}
	if li.ParentsNeeded <= 0 {
		t.Fatalf("closure size = %d, want the measured reachable bytes", li.ParentsNeeded)
	}
	if !li.RetainsHistory() {
		t.Fatal("store with refs and a branch reported as retaining no history")
	}

	// Abandoned artifacts.
	if len(li.Disposable) != 1 {
		t.Fatalf("disposable artifacts = %+v, want exactly the tmp_pack_ file", li.Disposable)
	}
	if li.Disposable[0].Kind != "tmp_pack" {
		t.Fatalf("disposable kind = %q, want tmp_pack", li.Disposable[0].Kind)
	}
	if li.DisposableBytes != 8192 {
		t.Fatalf("disposable bytes = %d, want 8192", li.DisposableBytes)
	}
	if survey.DisposableBytes != 8192 {
		t.Fatalf("survey disposable bytes = %d, want 8192", survey.DisposableBytes)
	}
}

// A completed pack is RETAINED, never reported as disposable. Removing one would
// discard every object it holds, which is the opposite of a recovery.
func TestLegacyNeverTreatsACompletedPackAsDisposable(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	packDir := filepath.Join(f.path, "objects", "pack")
	if err := os.WriteFile(filepath.Join(packDir, "pack-deadbeef.pack"), []byte("real pack"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packDir, "pack-deadbeef.idx"), []byte("real index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packDir, "tmp_pack_0f0f"), []byte("temp"), 0o644); err != nil {
		t.Fatal(err)
	}

	li, err := f.m.inspectLegacy(f.ctx, f.workspace, f.path, mustLstat(t, f.path))
	if err != nil {
		t.Fatalf("inspectLegacy: %v", err)
	}
	if len(li.Disposable) != 1 || li.Disposable[0].Name != "tmp_pack_0f0f" {
		t.Fatalf("disposable = %+v, want only the tmp_pack_ file", li.Disposable)
	}
	if len(li.Packs) != 2 {
		t.Fatalf("retained packs = %+v, want the .pack and its .idx", li.Packs)
	}
}

// Detection is READ-ONLY. The store's bytes, refs, and object files must be
// identical before and after a survey, or this code could corrupt a store it was
// only asked to describe.
func TestLegacyDetectionIsReadOnly(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 2, 512)
	f.abandonTempPack(t, "tmp_pack_112233", 1024)

	before := dirFingerprint(t, f.path)
	survey, err := f.m.SurveyLegacy(f.ctx)
	if err != nil {
		t.Fatalf("SurveyLegacy: %v", err)
	}
	if survey.Store(f.workspace) == nil {
		t.Fatal("survey did not find the store")
	}
	if after := dirFingerprint(t, f.path); after != before {
		t.Fatal("legacy detection mutated the store")
	}
	// And the refs still list, which proves no pruning happened.
	_, hashes, err := f.m.snapshotRefsInDir(f.ctx, f.path)
	if err != nil {
		t.Fatalf("snapshotRefsInDir: %v", err)
	}
	if len(hashes) != 1 {
		t.Fatalf("snapshot refs = %v, want the one published ref untouched", hashes)
	}
	if heads, err := f.m.legacyBranches(f.ctx, f.path); err != nil || len(heads) != 1 {
		t.Fatalf("branches = %v (err %v), want refs/heads/master untouched", heads, err)
	}
}

// The ORIGINAL DEFECT, reproduced. Under the old scheme, deleting every snapshot
// ref left the whole chain reachable through the branch — so retention freed
// nothing at all. The test proves both halves: the reachable closure survives
// ref deletion, and it is the BRANCH that keeps it alive.
func TestOriginalDefectRefPruningFreesNothingWhileABranchRemains(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 4, 2048)

	tip := strings.TrimSpace(gitEnv(t, f.ctx, f.path, "rev-parse", "refs/heads/master"))
	ref := snapshotRefPrefix + tip

	// Measure the reachable closure with both roots present.
	before, err := f.m.legacyClosureBytes(f.ctx, f.path)
	if err != nil {
		t.Fatalf("closure before: %v", err)
	}
	if before <= 0 {
		t.Fatal("closure before was empty, so this test would prove nothing")
	}

	// "Prune the snapshot refs" — the old cleanup's whole strategy.
	gitEnv(t, f.ctx, f.path, "update-ref", "-d", ref)

	// The chain is STILL reachable: the branch keeps it alive.
	after, err := f.m.legacyClosureBytes(f.ctx, f.path)
	if err != nil {
		t.Fatalf("closure after: %v", err)
	}
	if after != before {
		t.Fatalf("closure changed from %d to %d after deleting every snapshot ref; the branch should "+
			"have kept the whole chain reachable, which is the defect", before, after)
	}
	// And every commit in the chain is still resolvable, which is what
	// "reachable" means concretely.
	for i, hash := range f.commitChainHashes(t) {
		if _, err := f.m.gitOutput(f.ctx, f.path, "cat-file", "-e", hash+"^{commit}"); err != nil {
			t.Fatalf("commit %d (%s) became unreachable after ref pruning: %v", i, hash, err)
		}
	}

	// Deleting the BRANCH is what finally releases the chain, which is why a
	// per-ref cleanup could never have freed the store.
	gitEnv(t, f.ctx, f.path, "update-ref", "-d", "refs/heads/master")
	empty, err := f.m.legacyClosureBytes(f.ctx, f.path)
	if err != nil {
		// rev-list --all with no refs lists nothing and exits 0 on a modern
		// git; a failure here is a genuine problem.
		t.Fatalf("closure after branch deletion: %v", err)
	}
	if empty != 0 {
		t.Fatalf("closure = %d after deleting the branch, want 0; the branch was the last retention root", empty)
	}
}

// commitChainExtraSnapshot publishes a snapshot ref for a NEW commit, so the
// store's ref namespace differs from the one a fingerprint described.
func (f *legacyFixture) commitChainExtraSnapshot(t *testing.T) {
	t.Helper()
	cmd := newGitCmd(f.ctx, "git", f.path, "", false, "hash-object", "-w", "-t", "blob", "--stdin")
	cmd.Stdin = strings.NewReader("extra snapshot\n")
	out, err := runGitCombined(f.ctx, cmd, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("hash-object: %v\n%s", err, out)
	}
	blobHash := strings.TrimSpace(string(out))

	treeCmd := newGitCmd(f.ctx, "git", f.path, "", false, "mktree")
	treeCmd.Stdin = strings.NewReader("100644 blob " + blobHash + "\textra.bin\n")
	out, err = runGitCombined(f.ctx, treeCmd, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("mktree: %v\n%s", err, out)
	}
	treeHash := strings.TrimSpace(string(out))

	commitCmd := newGitCmd(f.ctx, "git", f.path, "", false, "commit-tree", treeHash)
	commitCmd.Stdin = strings.NewReader("extra\n")
	commitCmd.Env = append(commitCmd.Env,
		"GIT_AUTHOR_NAME=marshal", "GIT_AUTHOR_EMAIL=marshal@local",
		"GIT_COMMITTER_NAME=marshal", "GIT_COMMITTER_EMAIL=marshal@local",
	)
	out, err = runGitCombined(f.ctx, commitCmd, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("commit-tree: %v\n%s", err, out)
	}
	commitHash := strings.TrimSpace(string(out))
	gitEnv(t, f.ctx, f.path, "update-ref", snapshotRefPrefix+commitHash, commitHash)
}

// commitChainHashes re-reads the chain's commit hashes from the branch history.
func (f *legacyFixture) commitChainHashes(t *testing.T) []string {
	t.Helper()
	out := gitEnv(t, f.ctx, f.path, "rev-list", "refs/heads/master")
	var hashes []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			hashes = append(hashes, line)
		}
	}
	return hashes
}

// A store whose closure cannot be enumerated is reported as UNKNOWN, never as
// empty: "could not describe it" must not read as "there is nothing here".
func TestLegacyReportsUnreadableClosureAsUnknown(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	// A named ref whose object is missing makes rev-list fail, which is the
	// "the closure cannot be described" case. (Emptying objects/ entirely would
	// instead make the directory stop looking like a repository at all, which is
	// a different, already-covered answer.)
	removeOldestLooseObject(t, f.path)
	li, err := f.m.inspectLegacy(f.ctx, f.workspace, f.path, mustLstat(t, f.path))
	if err != nil {
		t.Fatalf("inspectLegacy: %v", err)
	}
	if li.ClosureError == nil {
		t.Fatal("a store with no object database reported a successful closure enumeration")
	}
	if li.MigrationFeasible(DefaultLimits()) {
		t.Fatal("a store with an unknown closure was reported as migratable")
	}
}

// A directory that is not a Git repository at all is reported as legacy and not
// as a repository, rather than being skipped or treated as empty.
func TestLegacyReportsNonRepositoryDirectory(t *testing.T) {
	dataDir := t.TempDir()
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	workspace := "abcdefabcdef"
	path := filepath.Join(m.Root(), workspace)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	survey, err := m.SurveyLegacy(context.Background())
	if err != nil {
		t.Fatalf("SurveyLegacy: %v", err)
	}
	li := survey.Store(workspace)
	if li == nil {
		t.Fatal("the directory was not reported as a legacy store")
	}
	if li.IsRepository {
		t.Fatal("a non-repository directory was reported as a Git repository")
	}
	if li.RetainsHistory() {
		t.Fatal("a non-repository directory was reported as retaining history")
	}
}

// A live Git lock file is evidence of an ACTIVE writer, not an abandoned
// artifact, and it must refuse a mutation.
func TestLegacyLockFileIsRefusedAsLiveWriterEvidence(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	// A lock file is created only while a writer holds it.
	if err := os.WriteFile(filepath.Join(f.path, "refs", "heads", "master.lock"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	ev, err := f.m.liveWriterEvidenceIn(f.path)
	if err != nil {
		t.Fatalf("liveWriterEvidenceIn: %v", err)
	}
	if len(ev.LockFiles) == 0 {
		t.Fatal("a Git lock file was not recognized as live-writer evidence")
	}
	if err := f.m.refuseIfLiveWriterIn(f.path); !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("refuseIfLiveWriterIn = %v, want a legacy-recovery refusal", err)
	}
}

// A just-written tmp_pack_ file is evidence of an active repack; an old one is
// not. The freshness window is what separates them, and the test exercises it
// with the real window rather than by disabling the check.
func TestLegacyFreshTempPackIsLiveWriterEvidence(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	path := f.plantFreshTempPack(t, "tmp_pack_fresh", 4096)

	if err := f.m.refuseIfLiveWriterIn(f.path); !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("a fresh tmp_pack_ was not refused as live-writer evidence: %v", err)
	}

	// Backdate it past the window: it is now abandoned debris, not an active
	// repack, and the refusal lifts.
	if err := os.Chtimes(path, liveWriterPast, liveWriterPast); err != nil {
		t.Fatal(err)
	}
	if err := f.m.refuseIfLiveWriterIn(f.path); err != nil {
		t.Fatalf("an old tmp_pack_ was refused as live-writer evidence: %v", err)
	}
}

// The status report is READ-ONLY and states every unknown as unknown.
func TestBuildStoreStatusIsReadOnlyAndStatesUnknowns(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 2, 1024)
	f.abandonTempPack(t, "tmp_pack_status", 2048)

	before := dirFingerprint(t, f.path)
	st, err := f.m.BuildStoreStatus(f.ctx)
	if err != nil {
		t.Fatalf("BuildStoreStatus: %v", err)
	}
	if after := dirFingerprint(t, f.path); after != before {
		t.Fatal("building the status report mutated the store")
	}
	text := RenderStoreStatus(st)
	for _, want := range []string{
		f.workspace,
		"unknown (the old layout records only the workspace hash)",
		"abandoned temps:",
		"tmp_pack_status",
		"Ownership note:",
		"marshal snapshots cleanup",
		"marshal snapshots migrate --workspace <id>",
		"marshal snapshots reset --workspace <id>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status output missing %q:\n%s", want, text)
		}
	}
}

// A workspace whose closure exceeds the budgets is reported as NOT migratable,
// because a hash-preserving migration would not fit and this code refuses to
// copy only part of the history.
func TestLegacyMigrationFeasibilityAccountsSourceAndDestination(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 3, 8192)
	li, err := f.m.inspectLegacy(f.ctx, f.workspace, f.path, mustLstat(t, f.path))
	if err != nil {
		t.Fatalf("inspectLegacy: %v", err)
	}
	// A generous budget admits it.
	if !li.MigrationFeasible(Limits{WorkspaceMaxBytes: 1 << 30, GlobalMaxBytes: 1 << 30}) {
		t.Fatal("a migration that fits an ample budget was reported as infeasible")
	}
	// A budget smaller than the closure plus the still-present source refuses.
	tight := Limits{WorkspaceMaxBytes: 1024, GlobalMaxBytes: 1 << 30}
	if li.MigrationFeasible(tight) {
		t.Fatal("a migration larger than the workspace budget was reported as feasible")
	}
	// And "unknown" never reads as "fits".
	unknown := &LegacyInfo{Present: true, IsRepository: true, SnapshotRefs: 1, ClosureError: errors.New("nope")}
	if unknown.MigrationFeasible(DefaultLimits()) {
		t.Fatal("a store with an unknown closure was reported as migratable")
	}
}

// A store whose refs could not be listed is never reported as empty.
func TestLegacyNeverReportsUnreadableRefsAsEmpty(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	li, err := f.m.inspectLegacy(f.ctx, f.workspace, f.path, mustLstat(t, f.path))
	if err != nil {
		t.Fatal(err)
	}
	li.RefsUnreadable = true
	li.RefListingError = errors.New("unreadable")
	li.SnapshotRefs = 0
	li.SnapshotHashes = nil
	// Even with zero refs counted, an unreadable listing is not "empty".
	if li.MigrationFeasible(DefaultLimits()) {
		t.Fatal("a store with an unreadable ref listing was reported as migratable")
	}
}

// parseLegacyRemnantName must accept only a validated workspace hash plus a safe
// suffix, so a crafted directory name can never steer a removal.
func TestParseLegacyRemnantNameRejectsUnsafeNames(t *testing.T) {
	good, ok := parseLegacyRemnantName(formatLegacyDeletingName("0123456789ab", "deadbeef"))
	if !ok || good != "0123456789ab" {
		t.Fatalf("parse of a valid remnant name = (%q, %v), want the workspace hash", good, ok)
	}
	for _, bad := range []string{
		"",
		"0123456789ab",
		"notahash.deleting-abc",
		"../etc.deleting-abc",
		"/abs/path.deleting-abc",
		"0123456789ab.deleting-",
		"0123456789ab.deleting-a/b",
	} {
		if _, ok := parseLegacyRemnantName(bad); ok {
			t.Errorf("parseLegacyRemnantName(%q) accepted an unsafe name", bad)
		}
	}
}

// containedLegacyStorePath refuses anything that is not a plain workspace hash,
// and refuses a symlink at the store path.
func TestContainedLegacyStorePathRefusesEscapes(t *testing.T) {
	f := newLegacyFixture(t)
	// A path, a traversal, and an absolute path are all refused as identifiers.
	for _, bad := range []string{
		"",
		"..",
		"../etc",
		"/etc/passwd",
		"0123456789ab/../..",
		"0123456789abcd",
		"not-hex-here",
	} {
		if _, err := f.m.containedLegacyStorePath(bad); err == nil {
			t.Errorf("containedLegacyStorePath(%q) was accepted", bad)
		}
	}
	// The validated one resolves inside the root.
	got, err := f.m.containedLegacyStorePath(f.workspace)
	if err != nil {
		t.Fatalf("containedLegacyStorePath(%q): %v", f.workspace, err)
	}
	if got != f.path {
		t.Fatalf("path = %q, want %q", got, f.path)
	}

	// A symlink planted at the store path is refused rather than followed.
	other := newLegacyFixture(t)
	elsewhere := t.TempDir()
	if err := os.RemoveAll(other.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, other.path); err != nil {
		t.Fatal(err)
	}
	if _, err := other.m.containedLegacyStorePath(other.workspace); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("a symlinked store path = %v, want ErrSymlinkEscape", err)
	}
}

// A store whose branch refs cannot be read must not silently report fewer
// branches: under-reporting what a migration must carry is a correctness bug.
func TestLegacyBranchesDetectsAllBranches(t *testing.T) {
	f := newLegacyFixture(t)
	commits := f.commitChain(t, 2, 128)
	gitEnv(t, f.ctx, f.path, "update-ref", "refs/heads/second", commits[0])

	branches, err := f.m.legacyBranches(f.ctx, f.path)
	if err != nil {
		t.Fatalf("legacyBranches: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("branches = %+v, want both refs/heads/master and refs/heads/second", branches)
	}
	if branches[0].Ref != "refs/heads/master" || branches[1].Ref != "refs/heads/second" {
		t.Fatalf("branches = %+v, want them sorted by ref name", branches)
	}
}

// mustLstat is a test helper for the os.FileInfo inspectLegacy requires.
func mustLstat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return info
}

// removeOldestLooseObject deletes one loose object file, leaving the repository
// structurally intact but unable to enumerate its full closure.
func removeOldestLooseObject(t *testing.T, gitDir string) {
	t.Helper()
	objectsDir := filepath.Join(gitDir, "objects")
	removed := false
	err := filepath.WalkDir(objectsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || removed {
			return err
		}
		if len(d.Name()) == 38 {
			removed = true
			return os.Remove(path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk objects: %v", err)
	}
	if !removed {
		t.Skip("no loose object was found to remove")
	}
}

// The closure walk must survive an object database containing more objects than
// one batch, which is what proves the batching is real rather than incidental.
func TestLegacyClosureBatchesAcrossManyObjects(t *testing.T) {
	f := newLegacyFixture(t)
	// More than closureBatchSize objects: 300 blobs, trees, and commits.
	n := closureBatchSize + 64
	commits := f.commitChain(t, n, 32)
	if len(commits) != n {
		t.Fatalf("built %d commits, want %d", len(commits), n)
	}
	li, err := f.m.inspectLegacy(f.ctx, f.workspace, f.path, mustLstat(t, f.path))
	if err != nil {
		t.Fatalf("inspectLegacy: %v", err)
	}
	if li.ClosureError != nil {
		t.Fatalf("closure error across %d objects: %v", n, li.ClosureError)
	}
	// n blobs + n trees + n commits.
	if want := int64(3 * n); li.Objects != want {
		t.Fatalf("closure enumerated %d objects, want %d", li.Objects, want)
	}
}

// A store that retains nothing is not migratable, and says so plainly.
func TestLegacyWithNoHistoryIsNotMigratable(t *testing.T) {
	f := newLegacyFixture(t)
	li, err := f.m.inspectLegacy(f.ctx, f.workspace, f.path, mustLstat(t, f.path))
	if err != nil {
		t.Fatal(err)
	}
	if li.RetainsHistory() {
		t.Fatal("an empty repository was reported as retaining history")
	}
	if li.MigrationFeasible(DefaultLimits()) {
		t.Fatal("an empty repository was reported as migratable")
	}
}

// A store root that does not exist reports an empty survey rather than an error,
// so a fresh machine gets a useful answer.
func TestSurveyLegacyOnAbsentRootIsEmpty(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	survey, err := m.SurveyLegacy(context.Background())
	if err != nil {
		t.Fatalf("SurveyLegacy on an absent root: %v", err)
	}
	if len(survey.Stores) != 0 || survey.TotalBytes != 0 {
		t.Fatalf("survey = %+v, want an empty result", survey)
	}
}

// The status report must not require a versioned store to exist.
func TestBuildStoreStatusWithoutVersionedStore(t *testing.T) {
	dataDir := t.TempDir()
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := m.BuildStoreStatus(context.Background())
	if err != nil {
		t.Fatalf("BuildStoreStatus: %v", err)
	}
	text := RenderStoreStatus(st)
	if !strings.Contains(text, "Versioned workspaces (0):") {
		t.Errorf("status did not report zero versioned workspaces:\n%s", text)
	}
	if !strings.Contains(text, "Old-format workspaces (0):") {
		t.Errorf("status did not report zero old-format workspaces:\n%s", text)
	}
}

// A leftover remnant of an interrupted reset is reported by the survey.
func TestSurveyLegacyReportsRemnants(t *testing.T) {
	dataDir := t.TempDir()
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	remnant := filepath.Join(m.Root(), formatLegacyDeletingName("0123456789ab", "deadbeef"))
	if err := os.MkdirAll(remnant, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remnant, "leftover"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	survey, err := m.SurveyLegacy(context.Background())
	if err != nil {
		t.Fatalf("SurveyLegacy: %v", err)
	}
	if len(survey.Remnants) != 1 {
		t.Fatalf("remnants = %+v, want the interrupted-reset remnant", survey.Remnants)
	}
	if survey.Remnants[0].Workspace != "0123456789ab" {
		t.Fatalf("remnant workspace = %q, want 0123456789ab", survey.Remnants[0].Workspace)
	}
	if survey.Remnants[0].Bytes <= 0 {
		t.Fatal("remnant size was not measured")
	}
	// It is reported as a warning, because the bytes are still on disk.
	found := false
	for _, w := range survey.Warnings {
		if w.Reason == ReasonLegacyRecoveryRequired && strings.Contains(w.Message, "remnant") {
			found = true
		}
	}
	if !found {
		t.Errorf("remnant was not reported as a warning: %+v", survey.Warnings)
	}
}

// A directory whose name is not a workspace hash must be ignored by the survey,
// not guessed at.
func TestSurveyLegacyIgnoresUnrecognizedDirectories(t *testing.T) {
	dataDir := t.TempDir()
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(m.Root(), "not-a-workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(m.Root(), "v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	survey, err := m.SurveyLegacy(context.Background())
	if err != nil {
		t.Fatalf("SurveyLegacy: %v", err)
	}
	if len(survey.Stores) != 0 || len(survey.Remnants) != 0 {
		t.Fatalf("survey = %+v, want nothing recognized", survey)
	}
}

// An unrecognized directory entry under the snapshots root must never be treated
// as a store: the store root may contain entries this code does not own.
func TestSurveyLegacyIgnoresStrayFiles(t *testing.T) {
	dataDir := t.TempDir()
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Root(), "stray-file"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	survey, err := m.SurveyLegacy(context.Background())
	if err != nil {
		t.Fatalf("SurveyLegacy: %v", err)
	}
	if len(survey.Stores) != 0 {
		t.Fatalf("survey = %+v, want no stores", survey.Stores)
	}
}

// A store path that is a plain file is refused rather than treated as a store.
func TestLegacyStorePathThatIsAFileIsRefused(t *testing.T) {
	dataDir := t.TempDir()
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	workspace := "fedcbafedcba"
	if err := os.WriteFile(filepath.Join(m.Root(), workspace), []byte("not a store"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.containedLegacyStorePath(workspace); !errors.Is(err, ErrUnreadableFile) {
		t.Fatalf("file store path = %v, want ErrUnreadableFile", err)
	}
}

// An artifact directory is left alone: only files are disposable artifacts.
func TestRemoveRecognizedArtifactRefusesADirectory(t *testing.T) {
	f := newLegacyFixture(t)
	path := f.abandonTempPack(t, "tmp_pack_dir", 0)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.m.removeRecognizedArtifact(path, "tmp_pack_dir"); err == nil {
		t.Fatal("a directory wearing a disposable artifact name was removed")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the directory was removed anyway: %v", err)
	}
}

// An unrecognized name is refused even when the path is inside the root.
func TestRemoveRecognizedArtifactRefusesUnrecognizedNames(t *testing.T) {
	f := newLegacyFixture(t)
	packDir := filepath.Join(f.path, "objects", "pack")
	path := filepath.Join(packDir, "pack-real.pack")
	if err := os.WriteFile(path, []byte("a real pack"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.m.removeRecognizedArtifact(path, "pack-real.pack"); err == nil {
		t.Fatal("a completed pack was removed as if it were disposable")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the completed pack was removed: %v", err)
	}
}

// proveInsideRoot refuses a path outside the managed root.
func TestProveInsideRootRefusesOutside(t *testing.T) {
	f := newLegacyFixture(t)
	if err := f.m.proveInsideRoot(f.path); err != nil {
		t.Fatalf("proveInsideRoot(store path): %v", err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := f.m.proveInsideRoot(outside); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("proveInsideRoot(outside) = %v, want ErrSymlinkEscape", err)
	}
	if err := f.m.proveInsideRoot(f.m.Root()); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("proveInsideRoot(root itself) = %v, want ErrSymlinkEscape", err)
	}
}

// The closure walk must report a store whose refs list no objects as an EMPTY
// closure, not as an error, so an empty store is distinguishable from a broken
// one.
func TestLegacyClosureOfEmptyRepositoryIsZero(t *testing.T) {
	f := newLegacyFixture(t)
	size, err := f.m.legacyClosureBytes(f.ctx, f.path)
	if err != nil {
		t.Fatalf("legacyClosureBytes on an empty repository: %v", err)
	}
	if size != 0 {
		t.Fatalf("empty repository closure = %d, want 0", size)
	}
}

// An object that is missing from the object database is an ERROR, never a
// smaller closure: an under-reported closure is how a migration would be
// admitted and then run out of room.
func TestLegacyClosureFailsOnAMissingObject(t *testing.T) {
	f := newLegacyFixture(t)
	f.commitChain(t, 1, 64)
	// Delete one loose object out from under the repository.
	removeOldestLooseObject(t, f.path)
	if _, err := f.m.legacyClosureBytes(f.ctx, f.path); err == nil {
		t.Fatal("a closure containing a missing object was measured successfully")
	}
}
