package snapshot

// storage_integration_test.go is Task 9's end-to-end proof.
//
// It exists because the three defects this feature removes were all *integration*
// failures, not unit failures: a store that retained 243 GiB of history behind a
// branch, 171 GiB of temporary packs nothing reclaimed, and multi-gigabyte files
// that entered history through an approximate scanner and a reused index. None
// of those is visible to a test that exercises one function.
//
// Four properties are proven here, and the second and third are the ones a
// self-reported number alone could never establish:
//
//  1. Each of the three original defects is REPRODUCED against a real
//     repository, and the fix is shown to remove it — the same physical
//     situation, measured before and after.
//  2. Every allocation and write boundary is instrumented (WithWriteObserver),
//     and the PEAK usage observed at those boundaries stays inside BOTH
//     ceilings. A store whose final size is under budget has proved nothing.
//  3. The manager's own accounting is cross-checked against an INDEPENDENTLY
//     sampled measurement of the bytes the filesystem actually holds, at the
//     same instant. The manager must not under-report, and must not wildly
//     over-report: the two are required to agree to within the conservative
//     per-entry rounding the manager applies deliberately.
//  4. Lifecycle safety holds across processes and crashes: helper processes
//     share one temporary root under deliberately tiny ceilings, and a real
//     process death (os.Exit) at each durable boundary is followed by recovery
//     that preserves every published hash.
//
// Every fixture lives in t.TempDir. Nothing in this file can reach the user's
// real data directory, and the helper processes it spawns are handed a
// sanitized environment (MARSHAL_*/XDG_* cleared) so they cannot resolve it
// either. Tests skip ONLY when the git binary is missing.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Budgets shared by every integration fixture
// ---------------------------------------------------------------------------

// The integration ceilings are deliberately tiny. They are the same numbers in
// the parent test and in every helper process it spawns, so a peak measured on
// one side is comparable with a ceiling asserted on the other.
const (
	integrationWorkspaceMax  int64 = 512 << 10
	integrationGlobalMax     int64 = 1536 << 10
	integrationManifestBound int64 = 16 << 10
	integrationMaintenance   int64 = 64 << 10
	integrationFreeMargin    int64 = 1 << 20
	integrationAllocUnit     int64 = 4096

	// The crash and multi-workspace fixtures use a second, slightly roomier
	// profile. It is still tiny next to the 2 GiB / 10 GiB production ceilings,
	// but it leaves enough headroom that a SECOND capture into a workspace fits
	// without budget reclamation — which is what makes "crash recovery preserves
	// every published hash" an assertion about recovery rather than an assertion
	// about reclamation. Under the tighter profile the same fixture would make
	// budget pressure reclaim the very generation the test means to preserve,
	// and the test would then be proving a different (also correct) behaviour.
	//
	// The TIGHT ceilings remain the ones the tiny-budget bound itself is proven
	// under, in the multi-process stress test and in the package's admission
	// tests.
	integrationCrashWorkspaceMax int64 = 8 << 20
	integrationCrashGlobalMax    int64 = 24 << 20

	// integrationCrashExit is the status a helper process dies with when it
	// simulates a crash at a write boundary. It is deliberately not 0 and not
	// the Go test failure status, so the parent can tell a crash from a test
	// failure.
	integrationCrashExit = 3
	// integrationHelperExit is the status a helper exits with when it cannot
	// even start, so a startup problem is never mistaken for a crash boundary.
	integrationHelperExit = 4
)

// integrationProfile is one set of ceilings, named so a helper process can be
// told which one to build its manager with and the parent can validate the
// helper's measurements against the same numbers.
type integrationProfile struct {
	Name         string
	WorkspaceMax int64
	GlobalMax    int64
}

// integrationProfiles maps a profile name to its ceilings.
var integrationProfiles = map[string]integrationProfile{
	"tiny":  {Name: "tiny", WorkspaceMax: integrationWorkspaceMax, GlobalMax: integrationGlobalMax},
	"crash": {Name: "crash", WorkspaceMax: integrationCrashWorkspaceMax, GlobalMax: integrationCrashGlobalMax},
}

func integrationProfileFor(name string) integrationProfile {
	if p, ok := integrationProfiles[name]; ok {
		return p
	}
	return integrationProfiles["tiny"]
}

func integrationLimits() Limits {
	return integrationLimitsFor(integrationProfiles["tiny"])
}

func integrationLimitsFor(p integrationProfile) Limits {
	return Limits{
		WorkspaceMaxBytes:      p.WorkspaceMax,
		GlobalMaxBytes:         p.GlobalMax,
		FreeSpaceMarginBytes:   integrationFreeMargin,
		FreeSpaceMarginPercent: 0,
		AllocationUnitBytes:    integrationAllocUnit,
	}
}

// integrationBounds shrinks the two production reserves that would otherwise
// dominate a tiny budget: the 2 MiB manifest bound (its replacement holds TWO
// manifests at once) and the 4 MiB maintenance allowance.
func integrationBounds() ManagerOption {
	return func(m *Manager) {
		WithManifestBound(integrationManifestBound)(m)
		WithMaintenanceAllowance(integrationMaintenance)(m)
	}
}

func integrationManagerOptions(extra ...ManagerOption) []ManagerOption {
	return integrationManagerOptionsFor(integrationProfiles["tiny"], extra...)
}

func integrationManagerOptionsFor(p integrationProfile, extra ...ManagerOption) []ManagerOption {
	opts := []ManagerOption{WithLimits(integrationLimitsFor(p)), integrationBounds()}
	return append(opts, extra...)
}

// ---------------------------------------------------------------------------
// Independent allocated-usage sampling
// ---------------------------------------------------------------------------

// sampleAllocation measures the bytes a directory tree actually holds, using a
// code path that shares nothing with the manager's accounting: it walks the tree
// itself and reads the allocated block count straight out of the platform's
// stat structure.
//
// Why a second implementation exists at all: the whole point of this task is
// that a bound proven only by the manager's self-report is not proven. The
// manager could under-count — a rounding mistake, a missed directory, an entry
// it treats as zero — and a test asserting only on the manager's number would
// pass while the filesystem held more. This sampler is the independent witness.
//
// It deliberately mirrors the manager's conservative rule (allocated bytes, or
// the logical length, whichever is larger; a directory always charged at least
// one allocation unit) because that is what makes the two comparable. It does
// NOT reuse the manager's code for it.
//
// entries is returned so a caller can bound the manager's deliberate per-entry
// rounding: rounding a logical length up to an allocation unit can add at most
// unit-1 bytes per entry, so the manager may exceed this sampler by at most that
// much per entry and by no more. That is the "the two must agree" direction that
// stops a wildly over-reporting manager from passing as conservative.
func sampleAllocation(root string) (total int64, entries int64, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// The store never contains symlinks, and this sampler must never
			// follow one out of the tree it is measuring.
			return nil
		}
		charge := blockedBytes(info)
		logical := info.Size()
		if logical < 0 {
			logical = 0
		}
		if info.IsDir() && charge < integrationAllocUnit {
			// A directory occupies at least one allocation unit even when the
			// platform reports no blocks for it.
			charge = integrationAllocUnit
		}
		if logical > charge {
			charge = logical
		}
		total += charge
		entries++
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return total, entries, nil
}

// blockedBytes reads an entry's allocated byte count from the platform's own
// stat structure (st_blocks, in 512-byte units on every supported Unix). A
// platform whose stat carries no block count — Windows' file attribute data, for
// one — reports 0, and the caller's logical-length floor then applies. That
// degradation is stated rather than hidden: on such a platform this sampler
// under-counts a sparse or compressed file, and only the manager's figure bounds
// it.
//
// The field is read reflectively so that one test file compiles on every
// platform the package supports, rather than needing a build-tagged twin.
func blockedBytes(info os.FileInfo) int64 {
	v := reflect.ValueOf(info.Sys())
	if !v.IsValid() {
		return 0
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return 0
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return 0
	}
	field := v.FieldByName("Blocks")
	switch field.Kind() {
	case reflect.Int, reflect.Int32, reflect.Int64:
		blocks := field.Int()
		if blocks < 0 {
			return 0
		}
		return blocks * 512
	}
	return 0
}

// sampledAllocatedBytes is sampleAllocation with the test's own failure
// handling.
func sampledAllocatedBytes(t *testing.T, root string) int64 {
	t.Helper()
	total, _, err := sampleAllocation(root)
	if err != nil {
		t.Fatalf("sample allocated usage of %s: %v", root, err)
	}
	return total
}

// ---------------------------------------------------------------------------
// Peak tracking: instrumentation plus the independent witness
// ---------------------------------------------------------------------------

// integrationSample is one instant's measurement of a store.
type integrationSample struct {
	// Kind names the write boundary the sample was taken at.
	Kind string
	// Physical is the independently sampled allocated usage of the store root.
	Physical int64
	// Entries counts the tree entries the physical sample covered.
	Entries int64
	// ManagerGlobal and ManagerWorkspace are the manager's own accounting at the
	// same instant, or 0 when this boundary is not one the manager is sampled
	// at.
	ManagerGlobal    int64
	ManagerWorkspace int64
	// Violations records any disagreement detected at this sample.
	Violations []string
}

// integrationPeakTracker records the PEAK of every usage measurement rather
// than the final total, and cross-checks the manager's figure against the
// independent one at the same instant.
//
// The physical sample is taken at EVERY boundary because it costs no subprocess.
// The manager's own accounting is taken only at the coarse boundaries
// (generation, ref, reclaim, staging, manifest) because it walks every workspace
// and runs a ref listing per generation, which is real work on a store holding
// many workspaces — and the fine-grained boundaries it would be repeated at are
// all inside one of those.
type integrationPeakTracker struct {
	mu        sync.Mutex
	root      string
	workspace string
	managerFn func() *Manager
	coarse    map[string]bool
	// profile supplies the ceilings this tracker validates against.
	profile integrationProfile

	globalPeak    int64
	workspacePeak int64
	physicalPeak  int64
	samples       []integrationSample
	violations    []string
}

// integrationCoarseKinds are the boundaries at which the manager's own
// accounting is sampled in-process.
var integrationCoarseKinds = map[string]bool{
	"generation": true,
	"ref":        true,
	"reclaim":    true,
	"staging":    true,
	"manifest":   true,
}

// integrationStressCoarseKinds are the boundaries sampled by the helper
// processes in the multi-workspace stress test, where a manager-wide accounting
// pass is far more expensive because it covers every workspace.
var integrationStressCoarseKinds = map[string]bool{
	"generation": true,
	"ref":        true,
	"reclaim":    true,
}

func newIntegrationPeakTracker(root, workspace string, managerFn func() *Manager, coarse map[string]bool, profile integrationProfile) *integrationPeakTracker {
	return &integrationPeakTracker{
		root: root, workspace: workspace, managerFn: managerFn, coarse: coarse, profile: profile,
	}
}

// observeEvent measures the store at one write boundary.
func (p *integrationPeakTracker) observeEvent(kind string) integrationSample {
	s := integrationSample{Kind: kind}
	physical, entries, err := sampleAllocation(p.root)
	if err != nil {
		s.Violations = append(s.Violations, fmt.Sprintf("physical sample at %s failed: %v", kind, err))
	} else {
		s.Physical = physical
		s.Entries = entries
	}
	if p.coarse[kind] && p.managerFn != nil {
		if m := p.managerFn(); m != nil {
			if usage, err := m.UsageContext(context.Background()); err != nil {
				s.Violations = append(s.Violations,
					fmt.Sprintf("manager accounting at %s failed: %v", kind, err))
			} else {
				s.ManagerGlobal = usage.GlobalAllocated
				s.ManagerWorkspace = workspaceStoreAllocated(usage, p.workspace)
			}
		}
	}

	// The two measurements of ONE tree must agree, in both directions.
	//
	// The manager must never be the SMALLER of the two: an under-reported total
	// is how a budget admits a write it cannot afford. And it may not exceed the
	// independent figure by more than its own deliberate per-entry rounding,
	// because a "conservative" number that is unbounded is not a measurement.
	if s.ManagerGlobal > 0 && s.Physical > 0 {
		if s.ManagerGlobal < s.Physical {
			s.Violations = append(s.Violations, fmt.Sprintf(
				"at %s the manager accounted %d bytes while the filesystem held %d: the manager under-reported",
				kind, s.ManagerGlobal, s.Physical))
		}
		if slack := s.Entries * integrationAllocUnit; s.ManagerGlobal > s.Physical+slack {
			s.Violations = append(s.Violations, fmt.Sprintf(
				"at %s the manager accounted %d bytes for a tree the filesystem holds in %d, over the "+
					"%d-byte per-entry rounding allowance for %d entries",
				kind, s.ManagerGlobal, s.Physical, slack, s.Entries))
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if s.Physical > p.physicalPeak {
		p.physicalPeak = s.Physical
	}
	if s.ManagerGlobal > p.globalPeak {
		p.globalPeak = s.ManagerGlobal
	}
	if s.ManagerWorkspace > p.workspacePeak {
		p.workspacePeak = s.ManagerWorkspace
	}
	p.samples = append(p.samples, s)
	p.violations = append(p.violations, s.Violations...)
	return s
}

// check fails the test unless every sampled peak stayed inside both ceilings and
// every cross-check agreed.
func (p *integrationPeakTracker) check(t *testing.T, label string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.samples) == 0 {
		t.Fatalf("%s: no write boundary was ever instrumented", label)
	}
	for _, v := range p.violations {
		t.Errorf("%s: %s", label, v)
	}
	if p.physicalPeak > p.profile.GlobalMax {
		t.Errorf("%s: the filesystem peak reached %d bytes, over the %d byte global ceiling",
			label, p.physicalPeak, p.profile.GlobalMax)
	}
	if p.globalPeak > p.profile.GlobalMax {
		t.Errorf("%s: the manager's global peak reached %d bytes, over the %d byte global ceiling",
			label, p.globalPeak, p.profile.GlobalMax)
	}
	if p.workspacePeak > p.profile.WorkspaceMax {
		t.Errorf("%s: the workspace peak reached %d bytes, over the %d byte workspace ceiling",
			label, p.workspacePeak, p.profile.WorkspaceMax)
	}
	t.Logf("%s: physical peak %d / global ceiling %d, manager global peak %d, workspace peak %d / ceiling %d",
		label, p.physicalPeak, p.profile.GlobalMax, p.globalPeak, p.workspacePeak, p.profile.WorkspaceMax)
}

// ---------------------------------------------------------------------------
// Deterministic incompressible input
// ---------------------------------------------------------------------------

// deterministicIncompressible returns n bytes of deterministic
// high-entropy content derived from seed.
//
// Both halves matter. Incompressible, so "compression will save us" cannot mask
// an overrun: the store's bound assumes every object occupies at least its
// payload, and content a compressor could shrink would let a broken bound look
// fine. Deterministic, so a repeated run measures the same thing — a random
// payload would make a byte-count assertion flaky for a reason that has nothing
// to do with the code under test.
func deterministicIncompressible(seed string, n int) []byte {
	if n <= 0 {
		return nil
	}
	out := make([]byte, 0, n+sha256.Size)
	for counter := 0; len(out) < n; counter++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", seed, counter)))
		out = append(out, sum[:]...)
	}
	return out[:n]
}

// ---------------------------------------------------------------------------
// A pre-v2 legacy store with a parent chain
// ---------------------------------------------------------------------------

// buildIntegrationLegacyChain creates a pre-v2 bare shadow repository holding n
// commits in a PARENT CHAIN, with refs/heads/master naming the tip and the tip
// also published as a snapshot ref.
//
// That shape is the original defect, reproduced faithfully: two retention roots,
// so deleting the snapshot refs alone leaves the whole chain reachable. Each
// commit carries fresh incompressible content, because identical content would
// deduplicate to one blob and the "the whole chain is still on disk" assertions
// would then pass for the wrong reason.
//
// It returns the store path, the commit hashes oldest-first, and the total
// payload byte count.
func buildIntegrationLegacyChain(t *testing.T, m *Manager, workspace string, commits, blobSize int) (string, []string, int64) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(m.Root(), workspace)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir legacy store: %v", err)
	}
	gitEnv(t, ctx, path, "init", "--bare")

	parent := ""
	var hashes []string
	var payload int64
	for i := 0; i < commits; i++ {
		content := deterministicIncompressible(fmt.Sprintf("legacy-%d", i), blobSize)
		blob := integrationGitStdin(t, ctx, path, content, "hash-object", "-w", "-t", "blob", "--stdin")
		tree := integrationGitStdin(t, ctx, path,
			[]byte("100644 blob "+blob+"\tweights.bin\n"), "mktree")

		args := []string{"commit-tree", tree}
		if parent != "" {
			args = append(args, "-p", parent)
		}
		commitCmd := newGitCmd(ctx, "git", path, "", false, args...)
		commitCmd.Stdin = strings.NewReader(fmt.Sprintf("legacy snapshot %d\n", i))
		commitCmd.Env = append(commitCmd.Env,
			"GIT_AUTHOR_NAME=marshal", "GIT_AUTHOR_EMAIL=marshal@local",
			"GIT_COMMITTER_NAME=marshal", "GIT_COMMITTER_EMAIL=marshal@local",
		)
		out, err := runGitCombined(ctx, commitCmd, defaultMaxCommandOutput)
		if err != nil {
			t.Fatalf("commit-tree: %v\n%s", err, out)
		}
		parent = strings.TrimSpace(string(out))
		hashes = append(hashes, parent)
		payload += int64(blobSize)
	}
	gitEnv(t, ctx, path, "update-ref", "refs/heads/master", parent)
	gitEnv(t, ctx, path, "update-ref", snapshotRefPrefix+parent, parent)
	return path, hashes, payload
}

// integrationGitStdin runs a git command with data on stdin through the
// package's sanitized invocation.
func integrationGitStdin(t *testing.T, ctx context.Context, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := newGitCmd(ctx, "git", dir, "", false, args...)
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := runGitCombined(ctx, cmd, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// integrationReachableObjects counts the objects every ref of a repository can
// reach. It is the concrete meaning of "the history is still retained", and it
// is deliberately read through git rather than through Marshal's own listing.
func integrationReachableObjects(t *testing.T, ctx context.Context, dir string) int {
	t.Helper()
	out := gitEnv(t, ctx, dir, "rev-list", "--objects", "--all")
	if strings.TrimSpace(out) == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(out), "\n"))
}

// integrationDeleteSnapshotRefs performs exactly the OLD cleanup's whole
// strategy: delete every publication ref and nothing else.
func integrationDeleteSnapshotRefs(t *testing.T, ctx context.Context, dir string) int {
	t.Helper()
	out := gitEnv(t, ctx, dir, "for-each-ref", "--format=%(refname)", snapshotRefPrefix)
	var deleted int
	for _, line := range strings.Split(out, "\n") {
		ref := strings.TrimSpace(line)
		if ref == "" {
			continue
		}
		gitEnv(t, ctx, dir, "update-ref", "-d", ref)
		deleted++
	}
	return deleted
}

// integrationSnapshotPaths lists the paths a published snapshot records, read
// through git against the generation that retains it.
func integrationSnapshotPaths(t *testing.T, m *Manager, cat *Catalog, hash string) []string {
	t.Helper()
	dir := generationHolding(t, m, cat, hash)
	out, err := m.gitOutput(context.Background(), dir, "ls-tree", "-r", "--name-only", "-z", hash)
	if err != nil {
		t.Fatalf("ls-tree for %s: %v", hash, err)
	}
	return splitNULPaths(string(out))
}

func hasIntegrationPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// logAdmissionPeaks records an admissionEnv's measured peaks and the ceilings
// they were asserted against, so a run's numbers are in the test output rather
// than only in the assertion that they were under the limit.
func logAdmissionPeaks(t *testing.T, label string, env *admissionEnv, limits Limits) {
	t.Helper()
	t.Logf("%s: PEAK workspace %d / ceiling %d; PEAK global %d / ceiling %d; %d write boundaries sampled",
		label, env.workspacePeak(), limits.WorkspaceMaxBytes, env.globalPeak(), limits.GlobalMaxBytes, len(env.events))
}

// ---------------------------------------------------------------------------
// Defect 1: unreachable-history retention
// ---------------------------------------------------------------------------

// The original defect, reproduced and then removed.
//
// Part A builds a legacy store with a parent chain reachable from a branch, and
// demonstrates the two halves of the incident: deleting every snapshot ref
// released NOT ONE BYTE of history (the branch kept the whole chain reachable,
// so `git gc` could not free it either), and only removing the branch itself
// released the chain. That is precisely why 4,980 commits and 243 GiB survived a
// refs-only prune.
//
// Part B performs the equivalent operation against the NEW layout: a whole
// generation is deleted, and the independently sampled allocated usage of the
// store DROPS by at least the payload it held. Same physical situation, opposite
// outcome — which is the fix.
func TestIntegrationDefect1RefsOnlyPruningReleasesNothingWhileGenerationDeletionReleasesIt(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	// --- Part A: the old scheme -------------------------------------------------
	t.Run("legacy refs-only pruning retains the chain", func(t *testing.T) {
		dataDir := t.TempDir()
		m, err := NewManager(dataDir)
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		if err := m.Bootstrap(ctx); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}

		const commits, blobSize = 6, 32 << 10
		store, hashes, payload := buildIntegrationLegacyChain(t, m, "0123456789ab", commits, blobSize)
		if got := len(hashes); got != commits {
			t.Fatalf("built %d commits, want %d", got, commits)
		}

		before := sampledAllocatedBytes(t, store)
		closureBefore, err := m.legacyClosureBytes(ctx, store)
		if err != nil {
			t.Fatalf("closure before: %v", err)
		}
		reachableBefore := integrationReachableObjects(t, ctx, store)
		if closureBefore <= 0 || reachableBefore == 0 {
			t.Fatal("the fixture retains nothing, so this test would prove nothing")
		}

		// The old cleanup: delete every snapshot ref.
		if deleted := integrationDeleteSnapshotRefs(t, ctx, store); deleted != 1 {
			t.Fatalf("deleted %d snapshot refs, want the one publication ref", deleted)
		}

		closureAfter, err := m.legacyClosureBytes(ctx, store)
		if err != nil {
			t.Fatalf("closure after ref pruning: %v", err)
		}
		if closureAfter != closureBefore {
			t.Fatalf("the reachable closure changed from %d to %d bytes after deleting every snapshot ref; "+
				"the branch was supposed to keep all of it reachable, which IS the defect",
				closureBefore, closureAfter)
		}
		afterPrune := sampledAllocatedBytes(t, store)
		// A ref file is a few bytes; deleting one can free at most one
		// allocation unit. Anything more than that would mean history was
		// released, which is exactly what did not happen.
		if released := before - afterPrune; released > 4*integrationAllocUnit {
			t.Fatalf("refs-only pruning released %d allocated bytes; it released nothing in the incident, "+
				"and the payload alone is %d bytes", released, payload)
		}
		if afterPrune < payload {
			t.Fatalf("the store holds only %d bytes after refs-only pruning but its payload is %d; "+
				"history was released, which contradicts the defect being reproduced", afterPrune, payload)
		}

		// And `git gc` cannot help: every object is still reachable, so a
		// repack has nothing to prune.
		gitEnv(t, ctx, store, "gc", "--prune=now")
		if reachableAfter := integrationReachableObjects(t, ctx, store); reachableAfter != reachableBefore {
			t.Fatalf("git gc changed the reachable object count from %d to %d", reachableBefore, reachableAfter)
		}
		for i, hash := range hashes {
			if _, err := m.gitOutput(ctx, store, "cat-file", "-e", hash+"^{commit}"); err != nil {
				t.Fatalf("commit %d (%s) was freed by gc even though a branch names it: %v", i, hash, err)
			}
		}
		if afterGC := sampledAllocatedBytes(t, store); afterGC < payload {
			t.Fatalf("after git gc the store holds %d bytes, less than its %d byte payload: gc freed "+
				"reachable objects, which it must not", afterGC, payload)
		}

		// Deleting the BRANCH is what finally releases it. That is why a
		// per-ref cleanup could never have freed this store.
		gitEnv(t, ctx, store, "update-ref", "-d", "refs/heads/master")
		empty, err := m.legacyClosureBytes(ctx, store)
		if err != nil {
			t.Fatalf("closure after branch deletion: %v", err)
		}
		if empty != 0 {
			t.Fatalf("closure = %d after deleting the branch, want 0: the branch was the last retention root", empty)
		}
	})

	// --- Part B: the new scheme -------------------------------------------------
	t.Run("generation deletion releases the allocated usage", func(t *testing.T) {
		env := newAdmissionEnv(t, testLimits())
		const payloadSize = 512 << 10
		if err := os.WriteFile(filepath.Join(env.workspace, "weights.bin"),
			deterministicIncompressible("captured-weights", payloadSize), 0o644); err != nil {
			t.Fatalf("write payload: %v", err)
		}

		hash, admission := trackCapture(t, env, request(env))
		if !validObjectHash(hash) {
			t.Fatalf("capture published %q", hash)
		}
		if admission.GenerationID == "" {
			t.Fatal("admission reported no generation")
		}

		before, err := env.manager.MeasureWorkspaceGenerations(ctx, env.catalog.Workspace())
		if err != nil {
			t.Fatalf("MeasureWorkspaceGenerations: %v", err)
		}
		sampledBefore := sampledAllocatedBytes(t, env.manager.Root())
		generationBytes := before.ByID[admission.GenerationID]
		if generationBytes == nil || generationBytes.Allocated <= 0 {
			t.Fatalf("generation %s was not measured before reclamation", admission.GenerationID)
		}
		if generationBytes.Refs != 1 {
			t.Fatalf("generation holds %d refs, want the one published snapshot", generationBytes.Refs)
		}

		// Expire and reclaim: the whole generation goes, refs and all. Sealing
		// happens first because only a sealed generation is eligible, exactly as
		// a rotation would leave it.
		report, err := env.manager.ReclaimExpired(ctx, 0)
		if err != nil {
			t.Fatalf("ReclaimExpired: %v", err)
		}
		if len(report.Reclaimed) != 1 {
			t.Fatalf("reclaimed %d generations, want the expired one (%s)", len(report.Reclaimed), report)
		}

		after, err := env.manager.MeasureWorkspaceGenerations(ctx, env.catalog.Workspace())
		if err != nil {
			t.Fatalf("MeasureWorkspaceGenerations after: %v", err)
		}
		if _, still := after.ByID[admission.GenerationID]; still {
			t.Fatalf("generation %s survived reclamation", admission.GenerationID)
		}
		sampledAfter := sampledAllocatedBytes(t, env.manager.Root())

		released := sampledBefore - sampledAfter
		if released < payloadSize {
			t.Fatalf("deleting the generation released only %d allocated bytes for a %d byte payload "+
				"(store %d -> %d bytes); the new scheme must release what the old one could not",
				released, payloadSize, sampledBefore, sampledAfter)
		}
		if after.Allocated >= before.Allocated {
			t.Fatalf("the workspace measured %d bytes after reclamation, not less than the %d before",
				after.Allocated, before.Allocated)
		}

		// The hash is now reported as EXPIRED — a distinct, actionable answer,
		// not a bare git failure and not "never captured".
		if _, err := env.manager.LookupSnapshotOn(ctx, env.catalog, hash); !errors.Is(err, ErrSnapshotExpired) {
			t.Fatalf("lookup of a reclaimed snapshot = %v, want ErrSnapshotExpired", err)
		}
		logAdmissionPeaks(t, "defect 1 part B", env, testLimits())
	})
}

// ---------------------------------------------------------------------------
// Defect 2: interrupted-pack accumulation
// ---------------------------------------------------------------------------

// The original defect, reproduced and then removed.
//
// An interrupted `git gc` left `tmp_pack_*` files that nothing ever reclaimed:
// the old shutdown ran a repack under a deadline and discarded its errors, so
// 171 GiB of temporary packs sat in the store forever. This test proves the three
// properties that together remove it:
//
//  1. The abandoned packs are NOT reclaimed silently: an unattended cleanup (no
//     controlling terminal) leaves every byte in place.
//  2. A pack that looks like a LIVE writer — a just-written temporary pack, or
//     Git's own lock file — is REFUSED even with the acknowledgement, because
//     deleting a running repack's work is the corruption the refusal exists to
//     prevent.
//  3. Once the debris is genuinely abandoned, the confirmed cleanup reclaims
//     exactly it — and nothing else: no repack runs, every object and ref is
//     untouched, and the independently sampled usage drops by the measured size
//     of the removed artifacts.
func TestIntegrationDefect2InterruptedPackAccumulationIsReclaimedOnlyWhenAbandoned(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	dataDir := t.TempDir()
	m, err := NewManager(dataDir, integrationManagerOptions()...)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := m.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = m.Release() }()

	store, _, _ := buildIntegrationLegacyChain(t, m, "fedcbafedcba", 2, 8<<10)
	packDir := filepath.Join(store, "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatalf("mkdir pack dir: %v", err)
	}

	// Three abandoned temporary packs, backdated past the live-writer window:
	// this is what a repack killed weeks ago leaves behind.
	var abandonedBytes int64
	const packSize = 48 << 10
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("tmp_pack_%06x", i)
		path := filepath.Join(packDir, name)
		if err := os.WriteFile(path, deterministicIncompressible(name, packSize), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if err := os.Chtimes(path, liveWriterPast, liveWriterPast); err != nil {
			t.Fatalf("backdate %s: %v", name, err)
		}
		abandonedBytes += int64(packSize)
	}
	// Plus one `.keep` scratch file, the other recognized temporary class.
	keepPath := filepath.Join(packDir, "tmp_pack_scratch.keep")
	if err := os.WriteFile(keepPath, deterministicIncompressible("keep", 4<<10), 0o644); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	if err := os.Chtimes(keepPath, liveWriterPast, liveWriterPast); err != nil {
		t.Fatalf("backdate keep: %v", err)
	}
	abandonedBytes += 4 << 10

	before := sampledAllocatedBytes(t, store)
	objectsBefore := integrationReachableObjects(t, ctx, store)
	looseBefore := looseObjectHashes(t, store)

	// (1) An unattended cleanup reclaims NOTHING from a legacy store. This is
	// the "nothing ever reclaimed them" half of the defect, asserted rather than
	// assumed: the confirmation, not the measurement, is what authorises removal.
	report, err := m.Cleanup(ctx, CleanupOptions{RetentionDays: -1})
	if err != nil {
		t.Fatalf("unattended Cleanup: %v", err)
	}
	if len(report.TempArtifactsRemoved) != 0 {
		t.Fatalf("an unattended cleanup removed %d legacy artifacts; it must remove none",
			len(report.TempArtifactsRemoved))
	}
	if report.Confirmed {
		t.Fatal("an unattended cleanup reported the offline acknowledgement as obtained")
	}
	if after := sampledAllocatedBytes(t, store); after < before {
		t.Fatalf("an unattended cleanup shrank the legacy store from %d to %d bytes", before, after)
	}
	assertIntegrationTempPacks(t, packDir, 4)

	// (2) A LIVE writer refuses, even with a terminal. A fresh temporary pack is
	// indistinguishable from an active repack's work in progress.
	livePath := filepath.Join(packDir, "tmp_pack_live")
	if err := os.WriteFile(livePath, deterministicIncompressible("live", 8<<10), 0o644); err != nil {
		t.Fatalf("write live pack: %v", err)
	}
	if _, err := m.Cleanup(ctx, CleanupOptions{Confirmer: acknowledged(), RetentionDays: -1}); !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("cleanup with a fresh temporary pack = %v, want a legacy-recovery refusal", err)
	}
	assertIntegrationTempPacks(t, packDir, 5)

	// Git's own lock file is unambiguous evidence of an active writer.
	lockPath := filepath.Join(packDir, "pack-marshal.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	if _, err := m.Cleanup(ctx, CleanupOptions{Confirmer: acknowledged(), RetentionDays: -1}); !errors.Is(err, ErrLegacyRecoveryRequired) {
		t.Fatalf("cleanup with a Git lock file = %v, want a legacy-recovery refusal", err)
	}
	assertIntegrationTempPacks(t, packDir, 5)
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove lock: %v", err)
	}

	// Now the live case is genuinely abandoned: wait out the freshness window by
	// backdating, which is what the refusal's own remedy describes.
	if err := os.Chtimes(livePath, liveWriterPast, liveWriterPast); err != nil {
		t.Fatalf("backdate live pack: %v", err)
	}
	abandonedBytes += 8 << 10

	// (3) Confirmed cleanup reclaims exactly the recognized artifacts.
	preCleanup := sampledAllocatedBytes(t, store)
	report, err = m.Cleanup(ctx, CleanupOptions{Confirmer: acknowledged(), RetentionDays: -1})
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if !report.Confirmed {
		t.Fatal("a confirmed cleanup did not record the offline acknowledgement")
	}
	if len(report.TempArtifactsRemoved) != 5 {
		t.Fatalf("removed %d artifacts, want the five recognized temporary ones: %+v",
			len(report.TempArtifactsRemoved), report.TempArtifactsRemoved)
	}
	if report.TempBytes < abandonedBytes-int64(integrationAllocUnit)*5 {
		t.Fatalf("removed %d bytes, want at least the %d bytes the artifacts held", report.TempBytes, abandonedBytes)
	}
	assertIntegrationTempPacks(t, packDir, 0)

	postCleanup := sampledAllocatedBytes(t, store)
	if postCleanup > preCleanup-abandonedBytes/2 {
		t.Fatalf("cleanup removed the artifacts but the store only went from %d to %d bytes; "+
			"the %d bytes they held were not released", preCleanup, postCleanup, abandonedBytes)
	}

	// Nothing else was touched, and no repack ran: the same objects are on disk,
	// the same refs are published, and a repack would have replaced loose objects
	// with pack files.
	if objectsAfter := integrationReachableObjects(t, ctx, store); objectsAfter != objectsBefore {
		t.Fatalf("cleanup changed the reachable object count from %d to %d", objectsBefore, objectsAfter)
	}
	if looseAfter := looseObjectHashes(t, store); !sameStringSet(looseBefore, looseAfter) {
		t.Fatalf("cleanup changed the loose object set (%d before, %d after); no repack may run",
			len(looseBefore), len(looseAfter))
	}
	if _, _, err := m.snapshotRefsInDir(ctx, store); err != nil {
		t.Fatalf("the legacy store's refs are no longer readable after cleanup: %v", err)
	}
}

// assertIntegrationTempPacks requires the pack directory to hold exactly want
// recognized temporary artifacts.
func assertIntegrationTempPacks(t *testing.T, packDir string, want int) {
	t.Helper()
	entries, err := os.ReadDir(packDir)
	if err != nil {
		t.Fatalf("read pack dir: %v", err)
	}
	var got []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), gitTempPackPrefix) || hasTempSuffix(e.Name()) {
			got = append(got, e.Name())
		}
	}
	sort.Strings(got)
	if len(got) != want {
		t.Fatalf("pack directory holds %d recognized temporary artifacts %v, want %d", len(got), got, want)
	}
}

// looseObjectHashes lists a repository's loose object hashes.
func looseObjectHashes(t *testing.T, gitDir string) []string {
	t.Helper()
	all, _, err := looseObjects(gitDir)
	if err != nil {
		t.Fatalf("looseObjects(%s): %v", gitDir, err)
	}
	out := make([]string, 0, len(all))
	for hash := range all {
		out = append(out, hash)
	}
	sort.Strings(out)
	return out
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Defect 3: size-limit bypass through a stale index
// ---------------------------------------------------------------------------

// The original defect, reproduced and then removed.
//
// A file captured once stayed captured: an approximate ignore scanner plus a
// persistent shadow index meant a path that had been recorded yesterday was not
// "untracked" today, so it never got re-checked against the ignore rules or the
// per-file cap. Multi-gigabyte model weights entered history that way despite a
// 2 MB `max_file_bytes`.
//
// The test drives that exact sequence: capture a file while it is small, then
// grow it far past the cap and make a second path ignored, then capture again
// with the same rules. Neither may survive, and the store must not have grown by
// anything like the oversized file — which is the limit actually being enforced
// rather than approximated. The earlier snapshot must still be intact, so the
// fix does not achieve its bound by discarding history.
func TestIntegrationDefect3OversizedOrIgnoredFileDoesNotSurviveALaterCapture(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	env := newAdmissionEnv(t, testLimits())
	const fileCap = 8 << 10
	const oversized = 8 << 20

	writeWorkspaceFile(t, env.workspace, "weights.bin", "small weights\n")
	writeWorkspaceFile(t, env.workspace, "keep.txt", "keep me\n")
	writeWorkspaceFile(t, env.workspace, "secret.log", "logged\n")

	req := request(env)
	req.MaxFileBytes = fileCap

	first, _ := trackCapture(t, env, req)
	firstPaths := integrationSnapshotPaths(t, env.manager, env.catalog, first)
	for _, want := range []string{"weights.bin", "keep.txt", "secret.log"} {
		if !hasIntegrationPath(firstPaths, want) {
			t.Fatalf("the first capture did not record %q; it recorded %v", want, firstPaths)
		}
	}

	// The two bypasses, together: one path becomes oversized (it must be
	// excluded by the cap), the other becomes ignored (it must be excluded by
	// git's own rules, not by a stale index that still thinks it is tracked).
	if err := os.WriteFile(filepath.Join(env.workspace, "weights.bin"),
		deterministicIncompressible("oversized-weights", oversized), 0o644); err != nil {
		t.Fatalf("grow weights.bin: %v", err)
	}
	writeWorkspaceFile(t, env.workspace, ".gitignore", "*.log\n")

	beforeSample := sampledAllocatedBytes(t, env.manager.Root())
	second, admission := trackCapture(t, env, req)
	afterSample := sampledAllocatedBytes(t, env.manager.Root())

	secondPaths := integrationSnapshotPaths(t, env.manager, env.catalog, second)
	if hasIntegrationPath(secondPaths, "weights.bin") {
		t.Fatalf("an oversized file survived a later capture: %v", secondPaths)
	}
	if hasIntegrationPath(secondPaths, "secret.log") {
		t.Fatalf("an ignored file survived a later capture through a stale index: %v", secondPaths)
	}
	if !hasIntegrationPath(secondPaths, "keep.txt") {
		t.Fatalf("the capture dropped an eligible file along with the excluded ones: %v", secondPaths)
	}

	// The limit was ENFORCED, not merely reported: the store cannot have grown
	// by anything approaching the oversized file's size. The second capture adds
	// a 12-byte .gitignore and some metadata, well under one MiB.
	if growth := afterSample - beforeSample; growth > 1<<20 {
		t.Fatalf("the store grew by %d bytes across a capture that had to exclude a %d byte file; "+
			"the size limit was bypassed", growth, oversized)
	}
	if admission.Allowance >= oversized {
		t.Fatalf("the excluded file was reserved for anyway: allowance %d for a %d byte file",
			admission.Allowance, oversized)
	}

	// History is preserved: the earlier snapshot still resolves and still holds
	// the file as it was.
	if _, err := env.manager.LookupSnapshotOn(ctx, env.catalog, first); err != nil {
		t.Fatalf("the earlier snapshot no longer resolves: %v", err)
	}
	if !hasIntegrationPath(integrationSnapshotPaths(t, env.manager, env.catalog, first), "weights.bin") {
		t.Fatal("the earlier snapshot lost the path it recorded")
	}
	// And no partially written object was left behind by the exclusion path.
	assertNoTempObjects(t, env.manager.Root())
	logAdmissionPeaks(t, "defect 3", env, testLimits())
}

// assertNoTempObjects fails when a partially written object file survived.
func assertNoTempObjects(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), objectTempPrefix) {
			t.Errorf("a partially written object survived at %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

// ---------------------------------------------------------------------------
// Pre-existing legacy overage
// ---------------------------------------------------------------------------

// A store that is ALREADY over its global ceiling must not be grown by a new
// capture, and the overage must be reported rather than repaired by discarding
// history nobody asked to lose.
//
// This is the 258 GiB case: the legacy store is bigger than the budget, so the
// only correct answers are "say so" and "refuse to add to it". The legacy bytes
// must also survive every automatic pass untouched, because no amount of budget
// pressure is consent to destroy pre-v2 rollback history.
func TestIntegrationPreExistingLegacyOverageIsNeverGrown(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	dataDir := t.TempDir()
	// Bootstrap first, with a generous ceiling, so the root and the permanent
	// lock file exist. A tight manager bootstrapping a fresh root over an
	// existing legacy store would refuse to create its own metadata, which is a
	// different (already-covered) behaviour.
	setup, err := NewManager(dataDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := setup.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	store, _, payload := buildIntegrationLegacyChain(t, setup, "001122334455", 8, 64<<10)
	legacyBytes := sampledAllocatedBytes(t, store)
	if legacyBytes <= payload {
		t.Fatalf("legacy store holds %d bytes for a %d byte payload; the fixture is wrong", legacyBytes, payload)
	}
	legacyFingerprint := dirFingerprint(t, store)

	// A ceiling BELOW what the legacy store already occupies. Its bytes are
	// counted: a budget that ignored them would believe there is room where there
	// is none.
	tight := integrationLimits()
	tight.GlobalMaxBytes = legacyBytes / 2
	if tight.GlobalMaxBytes < 2*integrationAllocUnit {
		t.Fatalf("computed a nonsense ceiling %d", tight.GlobalMaxBytes)
	}
	m, err := NewManager(dataDir, WithLimits(tight), integrationBounds())
	if err != nil {
		t.Fatalf("NewManager (tight): %v", err)
	}
	if err := m.Bootstrap(ctx); err != nil {
		// Bootstrap's fast path applies because the root and lock file exist.
		t.Fatalf("Bootstrap (tight): %v", err)
	}
	if _, err := m.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = m.Release() }()

	workspace := filepath.Join(dataDir, "fresh-workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	writeWorkspaceFile(t, workspace, "new.txt", "brand new\n")
	cat, err := m.CatalogForRoot(workspace)
	if err != nil {
		t.Fatalf("CatalogForRoot: %v", err)
	}

	beforeUsage, err := m.UsageContext(ctx)
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	if beforeUsage.LegacyAllocated < payload {
		t.Fatalf("the legacy store's %d byte payload is not counted in the global total (%d)",
			payload, beforeUsage.LegacyAllocated)
	}

	_, admission, err := m.AdmitAndCaptureWithCleanup(ctx, cat, CaptureRequest{
		WorkspaceRoot: workspace,
		Now:           fixedTestTime(),
	})
	if err == nil {
		t.Fatalf("a capture into an over-budget store was admitted (reserved %d)", admission.Reserved)
	}
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("refusal reason = %v, want ErrBudgetExhausted", err)
	}

	afterUsage, err := m.UsageContext(ctx)
	if err != nil {
		t.Fatalf("UsageContext after: %v", err)
	}
	if afterUsage.GlobalAllocated > beforeUsage.GlobalAllocated {
		t.Fatalf("a refused capture GREW an already over-budget store from %d to %d bytes",
			beforeUsage.GlobalAllocated, afterUsage.GlobalAllocated)
	}
	if sampled := sampledAllocatedBytes(t, m.Root()); sampled > afterUsage.GlobalAllocated {
		t.Fatalf("the manager accounted %d bytes while the filesystem holds %d: it under-reported",
			afterUsage.GlobalAllocated, sampled)
	}

	// The overage is REPORTED, so the user can act on it instead of discovering
	// it as a mysterious capture failure.
	if err := m.Status(ctx); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("Status on an over-budget store = %v, want ErrBudgetExhausted", err)
	}

	// No automatic pass may discard legacy history to relieve the pressure: not a
	// reconciliation, and not an expiry.
	if _, err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := m.ReclaimExpired(ctx, 0); err != nil {
		t.Fatalf("ReclaimExpired: %v", err)
	}
	if after := dirFingerprint(t, store); after != legacyFingerprint {
		t.Fatal("an automatic pass mutated the legacy store; only the confirmed offline workflow may")
	}
	if _, _, err := m.snapshotRefsInDir(ctx, store); err != nil {
		t.Fatalf("the legacy store's refs are unreadable: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Free space, independent of the configured budgets
// ---------------------------------------------------------------------------

// Free-space refusal is exercised through the injectable seam and asserted to be
// INDEPENDENT of the configured budgets: a store well inside both ceilings must
// still refuse when the filesystem has no room. The two failures send the user
// to different remedies, so conflating them is a real bug, and the store must
// not have grown by a single allocated byte either way.
func TestIntegrationFreeSpaceRefusalIsIndependentOfTheBudgets(t *testing.T) {
	requireGit(t)

	free := int64(16 << 20)
	env := newAdmissionEnv(t, testLimits(), WithFreeSpaceQuery(func(string) (int64, error) {
		return free, nil
	}))

	writeWorkspaceFile(t, env.workspace, "small.txt", "small\n")
	if _, _, err := env.manager.AdmitAndCaptureWithCleanup(context.Background(), env.catalog, request(env)); err != nil {
		t.Fatalf("first capture with 16 MiB free: %v", err)
	}

	// Plenty of budget on both axes; almost no room on the filesystem. The
	// reserve is 1 MiB, so a capture needing more than the remainder must be
	// refused for the free-space reason and NOT as a budget refusal.
	free = 512 << 10
	if err := os.WriteFile(filepath.Join(env.workspace, "big.bin"),
		deterministicIncompressible("big", 4<<20), 0o644); err != nil {
		t.Fatalf("write big.bin: %v", err)
	}

	before := sampledAllocatedBytes(t, env.manager.Root())
	_, _, err := env.manager.AdmitAndCaptureWithCleanup(context.Background(), env.catalog, request(env))
	if err == nil {
		t.Fatal("a capture with 512 KiB of free space was admitted")
	}
	if !errors.Is(err, ErrInsufficientFreeSpace) {
		t.Fatalf("refusal reason = %v, want ErrInsufficientFreeSpace", err)
	}
	if errors.Is(err, ErrBudgetExhausted) {
		t.Fatal("a free-space refusal was reported as a budget refusal; the remedies differ")
	}
	after := sampledAllocatedBytes(t, env.manager.Root())
	if after > before {
		t.Fatalf("a free-space refusal grew the store from %d to %d bytes", before, after)
	}
	if after > integrationGlobalMax {
		t.Fatalf("the store holds %d bytes, over the %d byte global ceiling", after, integrationGlobalMax)
	}
}

// ---------------------------------------------------------------------------
// Cancellation
// ---------------------------------------------------------------------------

// A cancelled capture must stop writing IMMEDIATELY: no in-flight record left
// behind, no partially written object file, no staging scratch, and — the part
// that matters for the bound — the store's allocated usage must not keep
// changing after the call returns.
//
// The context is cancelled from inside the capture at the first object boundary,
// which is deterministic rather than a race: the very next check in the writer
// loop observes it.
func TestIntegrationCancelledCaptureStopsWritingAndStaysBounded(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	env := newAdmissionEnv(t, integrationLimits(), integrationBounds())
	// Four small incompressible files: comfortably inside the tiny ceilings, so
	// the capture is ADMITTED and the cancellation — not a budget refusal — is
	// what ends it.
	for i := 0; i < 4; i++ {
		writeWorkspaceFile(t, env.workspace, fmt.Sprintf("chunk-%02d.bin", i),
			string(deterministicIncompressible(fmt.Sprintf("cancel-%d", i), 16<<10)))
	}

	var cancel context.CancelFunc
	runCtx, cancelFn := context.WithCancel(ctx)
	cancel = cancelFn
	defer cancel()

	// Cancel the moment the first object is durable. Everything after it must
	// unwind: the writer's remaining objects, the ref, and the manifest.
	env.manager.writeObserver = func(ev WriteEvent) {
		if ev.Kind == "object" {
			cancel()
		}
	}

	_, _, err := env.manager.AdmitAndCaptureWithCleanup(runCtx, env.catalog, request(env))
	if err == nil {
		t.Fatal("a cancelled capture reported success")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrInterruptedCapture) {
		t.Fatalf("cancelled capture = %v, want context.Canceled or ErrInterruptedCapture", err)
	}

	// Nothing is left mid-flight, and nothing is still being written.
	man, err := env.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if man != nil && man.Capture != nil {
		t.Fatalf("a cancelled capture left an in-flight record: %+v", man.Capture)
	}
	if entries, err := os.ReadDir(env.catalog.StagingDir()); err == nil && len(entries) != 0 {
		t.Fatalf("a cancelled capture left staging directories: %v", entries)
	}
	assertNoTempObjects(t, env.manager.Root())

	settled := sampledAllocatedBytes(t, env.manager.Root())
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(40 * time.Millisecond)
		if now := sampledAllocatedBytes(t, env.manager.Root()); now != settled {
			t.Fatalf("the store kept changing after cancellation: %d then %d bytes", settled, now)
		}
	}
	if settled > integrationGlobalMax {
		t.Fatalf("a cancelled capture left the store at %d bytes, over the %d byte global ceiling",
			settled, integrationGlobalMax)
	}

	// The store is still usable: a fresh context captures successfully.
	env.manager.writeObserver = nil
	hash, _, err := env.manager.AdmitAndCaptureWithCleanup(ctx, env.catalog, request(env))
	if err != nil {
		t.Fatalf("capture after a cancellation: %v", err)
	}
	if !validObjectHash(hash) {
		t.Fatalf("the capture after a cancellation published %q", hash)
	}
	t.Logf("cancelled capture: store settled at %d bytes, global ceiling %d", settled, integrationGlobalMax)
}

// A writer that is drained by releasing store ownership must be dead BEFORE the
// lock is handed on, and the store must therefore stop changing at that instant.
// The heartbeat file is the portable evidence: it stops growing only when every
// writer in the tree is dead.
func TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock(t *testing.T) {
	requireGit(t)

	env := newAdmissionEnv(t, integrationLimits(), integrationBounds())
	writeWorkspaceFile(t, env.workspace, "a.txt", "hello\n")

	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	cmd := exec.Command(os.Args[0], "-test.run=TestSnapshotHelperProcess")
	cmd.Env = append(os.Environ(),
		envHelperMode+"="+helperModeWriter,
		envHelperHeartbeat+"="+heartbeat,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := env.manager.RunManaged(ctx, cmd)
		done <- err
	}()
	waitForFileGrowth(t, heartbeat)

	if err := env.manager.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	settled := sampledAllocatedBytes(t, env.manager.Root())
	heartbeatAtRelease := fileSize(t, heartbeat)

	// Release returns only after the tree is confirmed gone, so both
	// measurements must be final.
	time.Sleep(400 * time.Millisecond)
	if grew := fileSize(t, heartbeat); grew != heartbeatAtRelease {
		t.Fatalf("a writer survived ownership release: the heartbeat grew from %d to %d bytes",
			heartbeatAtRelease, grew)
	}
	if now := sampledAllocatedBytes(t, env.manager.Root()); now != settled {
		t.Fatalf("the store changed after ownership was released: %d then %d bytes", settled, now)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunManaged did not return after its tree was drained")
	}
}

// ---------------------------------------------------------------------------
// Files that grow while they are being captured
// ---------------------------------------------------------------------------

// A file that grows during a capture must never produce a snapshot claiming
// bytes it does not hold, and the growth must never breach the store's bound.
//
// Two outcomes are legitimate and both are asserted: the capture refuses (the
// exact-read protocol saw the file change, or the grown payload no longer fits
// the admitted allowance), or it publishes a snapshot whose Git object header
// describes exactly the bytes stored. What must never happen is a silent
// mismatch, an overrun, or a leftover partially written object.
func TestIntegrationFilesGrowingDuringCaptureNeverBreachTheBound(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	env := newAdmissionEnv(t, integrationLimits(), integrationBounds())
	writeWorkspaceFile(t, env.workspace, "stable.txt", "stable\n")
	target := filepath.Join(env.workspace, "growing.bin")
	if err := os.WriteFile(target, deterministicIncompressible("growing", 16<<10), 0o644); err != nil {
		t.Fatalf("write growing.bin: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		chunk := deterministicIncompressible("appended", 8<<10)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := f.Write(chunk[:1<<10]); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
			if i > 400 {
				return
			}
		}
	}()

	peak := newIntegrationPeakTracker(env.manager.Root(), env.catalog.Workspace(),
		func() *Manager { return env.manager }, integrationCoarseKinds, integrationProfiles["tiny"])
	env.manager.writeObserver = func(ev WriteEvent) { peak.observeEvent(ev.Kind) }

	hash, _, err := env.manager.AdmitAndCaptureWithCleanup(ctx, env.catalog, request(env))
	close(stop)
	wg.Wait()
	env.manager.writeObserver = nil

	if err != nil {
		// A refusal is legitimate; it must be structured and must not have
		// breached anything.
		if ReasonOf(err) == "" {
			t.Fatalf("a capture that failed on a growing file carried no structured reason: %v", err)
		}
	} else {
		if !validObjectHash(hash) {
			t.Fatalf("capture published %q", hash)
		}
		// Whatever was stored must describe itself correctly: the object's
		// header declares a length, and the payload must be exactly that.
		assertPublishedObjectsAreSelfConsistent(t, env.manager, env.catalog, hash)
	}

	peak.check(t, "growing file")
	assertNoTempObjects(t, env.manager.Root())
	usage, err := env.manager.UsageContext(ctx)
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	if sampled := sampledAllocatedBytes(t, env.manager.Root()); sampled > usage.GlobalAllocated {
		t.Fatalf("the manager accounted %d bytes while the filesystem holds %d", usage.GlobalAllocated, sampled)
	}
}

// assertPublishedObjectsAreSelfConsistent proves, from the bytes on disk, that
// every loose object of a snapshot decompresses to exactly the content its
// header declares and hashes to its own name.
func assertPublishedObjectsAreSelfConsistent(t *testing.T, m *Manager, cat *Catalog, hash string) {
	t.Helper()
	dir := generationHolding(t, m, cat, hash)
	all, _, err := looseObjects(dir)
	if err != nil {
		t.Fatalf("looseObjects: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("the published snapshot has no loose objects")
	}
	for want, path := range all {
		raw, err := decompressLooseObject(path)
		if err != nil {
			t.Fatalf("decompress object %s: %v", want, err)
		}
		if got := objectHashOfRaw(t, raw); got != want {
			t.Fatalf("object stored as %s decompresses to bytes naming it %s", want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Helper processes: shared root, many workspaces, tiny ceilings
// ---------------------------------------------------------------------------

// Env sentinels for the integration helper. Like the ownership helper, it is
// inert unless one of these is set, so an ordinary `go test` run skips it.
const (
	envIntegrationMode   = "MARSHAL_SNAPSHOT_INTEGRATION_MODE"
	envIntegrationDir    = "MARSHAL_SNAPSHOT_INTEGRATION_DIR"
	envIntegrationWS     = "MARSHAL_SNAPSHOT_INTEGRATION_WORKSPACE"
	envIntegrationProg   = "MARSHAL_SNAPSHOT_INTEGRATION_PROGRESS"
	envIntegrationCrash  = "MARSHAL_SNAPSHOT_INTEGRATION_CRASH"
	envIntegrationFiles  = "MARSHAL_SNAPSHOT_INTEGRATION_FILES"
	envIntegrationTag    = "MARSHAL_SNAPSHOT_INTEGRATION_TAG"
	envIntegrationReplay = "MARSHAL_SNAPSHOT_INTEGRATION_REPLAY"
	envIntegrationLevel  = "MARSHAL_SNAPSHOT_INTEGRATION_LEVEL"

	integrationModeCapture = "capture"
	integrationModeCrash   = "crash"
	integrationModeReclaim = "reclaim"

	// integrationReplayOff is the default: a helper writes its files itself.
	// integrationReplayOn makes it capture whatever is already in the workspace,
	// which is how the "formerly captured file" sequence is driven across
	// processes.
	integrationReplayOff = "off"
	integrationReplayOn  = "on"
)

// integrationFile is one file a helper writes before capturing.
type integrationFile struct {
	Rel  string
	Size int
}

// integrationRecord is one line of the helper's append-only progress file. It is
// append-only and flushed per line on purpose: the interesting runs DIE in the
// middle, and their measurements have to survive them.
type integrationRecord struct {
	Event string `json:"event"` // write | published | done | error
	Kind  string `json:"kind,omitempty"`
	Path  string `json:"path,omitempty"`
	Hash  string `json:"hash,omitempty"`
	// Sampled is the independently measured allocated usage of the store root.
	Sampled int64 `json:"sampled,omitempty"`
	// Manager is the manager's own global accounting at the same instant.
	Manager int64 `json:"manager,omitempty"`
	// Workspace is the manager's accounting for the helper's workspace store.
	Workspace int64 `json:"workspace,omitempty"`
	// Entries is how many tree entries the physical sample covered.
	Entries int64 `json:"entries,omitempty"`
	// Violations are cross-check disagreements detected at this boundary.
	Violations []string `json:"violations,omitempty"`
	// Message carries an error's text.
	Message string `json:"message,omitempty"`
	// Reason carries an error's structured store reason.
	Reason string `json:"reason,omitempty"`
}

// integrationProgress appends records to a file, flushing each one.
type integrationProgress struct {
	mu sync.Mutex
	f  *os.File
}

func newIntegrationProgress(path string) *integrationProgress {
	if path == "" {
		return &integrationProgress{}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration helper: open progress file: %v\n", err)
		os.Exit(integrationHelperExit)
	}
	return &integrationProgress{f: f}
}

func (p *integrationProgress) record(rec integrationRecord) {
	if p == nil || p.f == nil {
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.f.Write(append(data, '\n')); err != nil {
		return
	}
	// Synced per line: a record written but not flushed would be lost by the
	// crash the record is about.
	_ = p.f.Sync()
}

func (p *integrationProgress) close() {
	if p == nil || p.f == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.f.Close()
}

// readIntegrationRecords parses a helper's progress file.
func readIntegrationRecords(t *testing.T, path string) []integrationRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		t.Fatalf("open progress file %s: %v", path, err)
	}
	defer f.Close()
	var out []integrationRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec integrationRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("progress file %s holds an unparseable line %q: %v", path, line, err)
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read progress file %s: %v", path, err)
	}
	return out
}

func formatIntegrationFiles(files []integrationFile) string {
	parts := make([]string, 0, len(files))
	for _, f := range files {
		parts = append(parts, fmt.Sprintf("%s:%d", f.Rel, f.Size))
	}
	return strings.Join(parts, ",")
}

func parseIntegrationFiles(spec string) []integrationFile {
	var out []integrationFile
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		rel, sizeText, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		size, err := strconv.Atoi(sizeText)
		if err != nil {
			continue
		}
		out = append(out, integrationFile{Rel: rel, Size: size})
	}
	return out
}

// writeIntegrationFiles writes a helper's fixture files, seeded by the file's
// own path so the content is deterministic across runs and distinct between
// files.
func writeIntegrationFiles(root string, files []integrationFile) error {
	for _, f := range files {
		full := filepath.Join(root, filepath.FromSlash(f.Rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, deterministicIncompressible("ws|"+f.Rel, f.Size), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// integrationChildEnv is the environment a helper process is given: the parent's
// environment with every MARSHAL_*/XDG_* path override stripped, so a helper
// cannot resolve the user's real data directory even if the parent's
// environment happens to point at it.
func integrationChildEnv() []string {
	out := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(name, "MARSHAL_") || strings.HasPrefix(name, "XDG_") {
			continue
		}
		out = append(out, kv)
	}
	// Explicitly blank, so a nested helper (or a stray default) cannot pick a
	// user path up either.
	out = append(out, "MARSHAL_CONFIG_DIR=", "MARSHAL_DATA_DIR=", "XDG_CONFIG_HOME=", "XDG_DATA_HOME=")
	return out
}

// crashHooksFor installs the fault-injection hook for a hook-named crash
// boundary. Each hook records the boundary it reached and then exits the process
// outright: no deferred work runs, which is what makes this a crash rather than
// an error return.
func crashHooksFor(crashAt string, progress *integrationProgress) Hooks {
	die := func(kind, path string) error {
		progress.record(integrationRecord{Event: "write", Kind: kind, Path: path})
		os.Exit(integrationCrashExit)
		return nil
	}
	var h Hooks
	switch crashAt {
	case "generation-record":
		h.AfterGenerationRecord = func() error { return die("generation-record", "") }
	case "staging":
		h.AfterReserveStaging = func() error { return die("staging", "") }
	case "before-publish":
		h.BeforePublishRef = func() error { return die("before-publish", "") }
	case "manifest":
		h.AfterManifestWrite = func(path string) error { return die("manifest", path) }
	case "mark-deleting":
		h.AfterMarkDeleting = func() error { return die("mark-deleting", "") }
	case "remove-generation-dir":
		h.BeforeRemoveGenerationDir = func(id string) error { return die("remove-generation-dir", id) }
	}
	return h
}

// TestSnapshotStorageIntegrationHelper is the re-executed helper process. It is
// inert unless a mode sentinel is set.
//
// It runs the REAL production code path — bootstrap, acquire, admit, capture,
// reclaim — in its own process, sharing one snapshots root with every other
// helper and with the parent. That is what makes its measurements evidence about
// cross-process behaviour rather than about a single in-process object graph.
func TestSnapshotStorageIntegrationHelper(t *testing.T) {
	mode := os.Getenv(envIntegrationMode)
	if mode == "" {
		t.Skip("helper process; only runs when re-executed with a mode sentinel")
	}
	dataDir := os.Getenv(envIntegrationDir)
	workspaceRoot := os.Getenv(envIntegrationWS)
	crashAt := os.Getenv(envIntegrationCrash)
	progress := newIntegrationProgress(os.Getenv(envIntegrationProg))
	defer progress.close()

	ctx := context.Background()
	profile := integrationProfileFor(os.Getenv(envIntegrationLevel))
	coarse := integrationStressCoarseKinds
	var tracker *integrationPeakTracker
	var mgr *Manager

	opts := integrationManagerOptionsFor(profile, WithWriteObserver(func(ev WriteEvent) {
		s := tracker.observeEvent(ev.Kind)
		progress.record(integrationRecord{
			Event:      "write",
			Kind:       ev.Kind,
			Path:       ev.Path,
			Sampled:    s.Physical,
			Manager:    s.ManagerGlobal,
			Workspace:  s.ManagerWorkspace,
			Entries:    s.Entries,
			Violations: s.Violations,
		})
		// Crash AFTER the record is durable, so the parent can prove the
		// boundary was reached rather than inferring it.
		if mode == integrationModeCrash && crashAt == ev.Kind {
			progress.close()
			os.Exit(integrationCrashExit)
		}
	}))
	// The crash hooks are installed for BOTH the crash mode and the reclaim
	// mode: the deletion boundaries (mark-deleting, remove-generation-dir) only
	// exist while reclamation is running, so a run that must die there has to
	// reclaim.
	if mode == integrationModeCrash || mode == integrationModeReclaim {
		opts = append(opts, WithHooks(crashHooksFor(crashAt, progress)))
	}

	m, err := NewManager(dataDir, opts...)
	if err != nil {
		progress.record(integrationRecord{Event: "error", Message: err.Error()})
		os.Exit(integrationHelperExit)
	}
	mgr = m
	cat, err := m.CatalogForRoot(workspaceRoot)
	if err != nil {
		progress.record(integrationRecord{Event: "error", Message: err.Error()})
		os.Exit(integrationHelperExit)
	}
	tracker = newIntegrationPeakTracker(m.Root(), cat.Workspace(), func() *Manager { return mgr }, coarse, profile)

	if err := m.Bootstrap(ctx); err != nil {
		progress.record(integrationRecord{Event: "error", Message: err.Error(), Reason: string(ReasonOf(err))})
		os.Exit(integrationHelperExit)
	}
	if _, err := m.Acquire(ctx); err != nil {
		progress.record(integrationRecord{Event: "error", Message: err.Error(), Reason: string(ReasonOf(err))})
		os.Exit(integrationHelperExit)
	}

	if os.Getenv(envIntegrationReplay) != integrationReplayOn {
		if err := writeIntegrationFiles(workspaceRoot, parseIntegrationFiles(os.Getenv(envIntegrationFiles))); err != nil {
			progress.record(integrationRecord{Event: "error", Message: err.Error()})
			os.Exit(integrationHelperExit)
		}
	}
	if tag := os.Getenv(envIntegrationTag); tag != "" {
		// A per-capture file, so successive captures of one workspace hold
		// DIFFERENT content. Identical content would deduplicate to the same
		// commit, and rotation and reclamation would never be exercised.
		if err := writeIntegrationFiles(workspaceRoot, []integrationFile{{Rel: "tag.txt", Size: len(tag)}}); err != nil {
			progress.record(integrationRecord{Event: "error", Message: err.Error()})
			os.Exit(integrationHelperExit)
		}
		if err := os.WriteFile(filepath.Join(workspaceRoot, "tag.txt"), []byte(tag), 0o644); err != nil {
			progress.record(integrationRecord{Event: "error", Message: err.Error()})
			os.Exit(integrationHelperExit)
		}
	}

	hash, _, err := m.AdmitAndCaptureWithCleanup(ctx, cat, CaptureRequest{
		WorkspaceRoot: workspaceRoot,
		Now:           fixedTestTime(),
	})
	if err != nil {
		progress.record(integrationRecord{
			Event: "error", Message: err.Error(), Reason: string(ReasonOf(err)),
		})
		_ = m.Release()
		os.Exit(0)
	}
	progress.record(integrationRecord{Event: "published", Hash: hash})

	if mode == integrationModeReclaim {
		// Expire and reclaim THIS workspace's generations: that is what makes the
		// deletion boundaries reachable, and it is the same work the runtime's
		// maintenance does. The scoped variant is used deliberately, so a crash
		// in this workspace's deletion cannot remove a neighbouring workspace's
		// generation — which would make "the anchor snapshot survived" true for
		// the wrong reason rather than because recovery preserved it.
		report, err := m.ReclaimExpiredWorkspace(ctx, cat, 0)
		if err != nil {
			progress.record(integrationRecord{Event: "error", Message: err.Error(), Reason: string(ReasonOf(err))})
			_ = m.Release()
			os.Exit(0)
		}
		progress.record(integrationRecord{Event: "done", Hash: hash, Message: report.String()})
	} else {
		progress.record(integrationRecord{Event: "done", Hash: hash})
	}
	if err := m.Release(); err != nil {
		progress.record(integrationRecord{Event: "error", Message: err.Error()})
		os.Exit(integrationHelperExit)
	}
	progress.close()
	os.Exit(0)
}

// spawnIntegrationHelper runs one helper process under the TIGHT profile.
func spawnIntegrationHelper(t *testing.T, dataDir, workspaceRoot string, files []integrationFile, mode, crashAt, tag string) (int, []integrationRecord) {
	t.Helper()
	return spawnIntegrationHelperWithProfile(t, integrationProfiles["tiny"], dataDir, workspaceRoot, files, mode, crashAt, tag)
}

// spawnIntegrationHelperWithProfile runs one helper process under a named
// ceiling profile and returns its exit code plus the records it wrote.
func spawnIntegrationHelperWithProfile(t *testing.T, profile integrationProfile, dataDir, workspaceRoot string, files []integrationFile, mode, crashAt, tag string) (int, []integrationRecord) {
	t.Helper()
	progressPath := filepath.Join(t.TempDir(), fmt.Sprintf("progress-%s-%s-%s.jsonl", mode, crashAt, tag))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSnapshotStorageIntegrationHelper")
	cmd.Env = append(integrationChildEnv(),
		envIntegrationMode+"="+mode,
		envIntegrationDir+"="+dataDir,
		envIntegrationWS+"="+workspaceRoot,
		envIntegrationProg+"="+progressPath,
		envIntegrationCrash+"="+crashAt,
		envIntegrationTag+"="+tag,
		envIntegrationLevel+"="+profile.Name,
		envIntegrationFiles+"="+formatIntegrationFiles(files),
	)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("helper (%s, crash at %q) could not run: %v\n%s", mode, crashAt, err, out)
		}
		code = exitErr.ExitCode()
	}
	if ctx.Err() != nil {
		t.Fatalf("helper (%s, crash at %q) exceeded its deadline\n%s", mode, crashAt, out)
	}
	records := readIntegrationRecords(t, progressPath)
	// Surface a helper's own failure text rather than a bare exit code.
	if code != 0 && code != integrationCrashExit {
		t.Fatalf("helper (%s, crash at %q) exited %d\nrecords: %+v\noutput:\n%s",
			mode, crashAt, code, records, out)
	}
	return code, records
}

// integrationRecordsPeak validates every measurement a helper reported against
// both ceilings and against the cross-check, and returns the physical peak.
func integrationRecordsPeak(t *testing.T, label string, recs []integrationRecord) int64 {
	t.Helper()
	var physicalPeak, managerPeak int64
	for _, rec := range recs {
		for _, v := range rec.Violations {
			t.Errorf("%s: %s", label, v)
		}
		if rec.Sampled > physicalPeak {
			physicalPeak = rec.Sampled
		}
		if rec.Manager > managerPeak {
			managerPeak = rec.Manager
		}
		if rec.Sampled > integrationGlobalMax {
			t.Errorf("%s: the filesystem held %d bytes at a %s boundary, over the %d byte global ceiling",
				label, rec.Sampled, rec.Kind, integrationGlobalMax)
		}
		if rec.Manager > integrationGlobalMax {
			t.Errorf("%s: the manager accounted %d bytes at a %s boundary, over the %d byte global ceiling",
				label, rec.Manager, rec.Kind, integrationGlobalMax)
		}
		if rec.Workspace > integrationWorkspaceMax {
			t.Errorf("%s: the workspace held %d bytes at a %s boundary, over the %d byte workspace ceiling",
				label, rec.Workspace, rec.Kind, integrationWorkspaceMax)
		}
		if rec.Manager > 0 && rec.Sampled > 0 {
			if rec.Manager < rec.Sampled {
				t.Errorf("%s: at %s the manager accounted %d bytes while the filesystem held %d",
					label, rec.Kind, rec.Manager, rec.Sampled)
			}
			if slack := rec.Entries * integrationAllocUnit; rec.Manager > rec.Sampled+slack {
				t.Errorf("%s: at %s the manager accounted %d bytes for a tree of %d bytes over %d entries",
					label, rec.Kind, rec.Manager, rec.Sampled, rec.Entries)
			}
		}
	}
	if physicalPeak == 0 && managerPeak == 0 {
		t.Errorf("%s: the helper reported no usage measurement at all", label)
	}
	return physicalPeak
}

func publishedHashes(recs []integrationRecord) []string {
	var out []string
	for _, rec := range recs {
		if rec.Event == "published" && rec.Hash != "" {
			out = append(out, rec.Hash)
		}
	}
	return out
}

func lastIntegrationRecord(recs []integrationRecord) (integrationRecord, bool) {
	if len(recs) == 0 {
		return integrationRecord{}, false
	}
	return recs[len(recs)-1], true
}

// TestIntegrationMultiProcessManyWorkspacesUnderTinyBudgets runs helper
// processes one after another against ONE temporary root, each capturing into
// its own workspace, under ceilings small enough that global pressure forces
// oldest-first reclamation across stores.
//
// Every workspace is then made INACTIVE — two of them were git worktrees whose
// directories are deleted afterwards — and the store must still be discovered,
// accounted, and compliant. That is the "inactive workspace and worktree stores"
// half of the design, proven across processes rather than in one object graph.
func TestIntegrationMultiProcessManyWorkspacesUnderTinyBudgets(t *testing.T) {
	requireGit(t)

	dataDir := t.TempDir()
	files := []integrationFile{
		{Rel: "weights.bin", Size: 48 << 10},
		{Rel: "empty.dat", Size: 0},
		{Rel: "tiny-1.txt", Size: 17},
		{Rel: "tiny-2.txt", Size: 4096},
		{Rel: "nested/deep/small.bin", Size: 300},
	}

	// A repository plus two linked worktrees: a worktree checkout is a
	// workspace of its own, and its store must be maintained even after the
	// worktree directory is gone.
	repo := filepath.Join(dataDir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runProjectGit(t, repo, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	runProjectGit(t, repo, "add", "seed.txt")
	runProjectGit(t, repo, "-c", "user.email=marshal@local", "-c", "user.name=marshal", "commit", "-q", "-m", "seed")

	worktrees := []string{
		filepath.Join(dataDir, "wt-alpha"),
		filepath.Join(dataDir, "wt-beta"),
	}
	for i, wt := range worktrees {
		runProjectGit(t, repo, "worktree", "add", "-q", "--detach", wt)
		if err := os.WriteFile(filepath.Join(wt, fmt.Sprintf("wt-%d.txt", i)), []byte("worktree\n"), 0o644); err != nil {
			t.Fatalf("write worktree file: %v", err)
		}
	}

	workspaces := []string{
		filepath.Join(dataDir, "ws-1"),
		filepath.Join(dataDir, "ws-2"),
		worktrees[0],
		worktrees[1],
		filepath.Join(dataDir, "ws-5"),
		filepath.Join(dataDir, "ws-6"),
	}
	for _, ws := range workspaces {
		if err := os.MkdirAll(ws, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", ws, err)
		}
	}

	const capturesPerWorkspace = 3
	var totalReclaims int
	var published int
	var refusals int
	var physicalPeak, managerPeak, workspacePeak int64
	for i, ws := range workspaces {
		for capture := 0; capture < capturesPerWorkspace; capture++ {
			tag := fmt.Sprintf("w%d-c%d", i, capture)
			code, recs := spawnIntegrationHelper(t, dataDir, ws, files, integrationModeCapture, "", tag)
			if code != 0 {
				t.Fatalf("capture helper for %s (%s) exited %d: %+v", ws, tag, code, recs)
			}
			label := fmt.Sprintf("workspace %d capture %d", i, capture)
			integrationRecordsPeak(t, label, recs)
			for _, rec := range recs {
				if rec.Sampled > physicalPeak {
					physicalPeak = rec.Sampled
				}
				if rec.Manager > managerPeak {
					managerPeak = rec.Manager
				}
				if rec.Workspace > workspacePeak {
					workspacePeak = rec.Workspace
				}
			}
			for _, rec := range recs {
				switch {
				case rec.Event == "published":
					published++
				case rec.Event == "error":
					// A refusal is acceptable — the design permits skipping a
					// capture rather than growing past a ceiling — but it must
					// be a budget refusal and the store must not have grown.
					if rec.Reason != string(ReasonBudgetExhausted) {
						t.Fatalf("%s: helper failed for an unexpected reason %q: %s", label, rec.Reason, rec.Message)
					}
					refusals++
				case rec.Event == "write" && rec.Kind == "reclaim":
					totalReclaims++
				}
			}
			last, ok := lastIntegrationRecord(recs)
			if !ok {
				t.Fatalf("%s: the helper reported nothing", label)
			}
			if last.Event != "done" && last.Event != "error" {
				t.Fatalf("%s: helper ended on %q: %+v", label, last.Event, last)
			}
			if last.Event == "done" && len(publishedHashes(recs)) == 0 {
				t.Fatalf("%s: helper completed without publishing a snapshot", label)
			}
		}
	}

	if published == 0 {
		t.Fatal("no capture ever published; the stress run proved nothing")
	}
	if published < len(workspaces) {
		t.Fatalf("only %d captures published across %d workspaces", published, len(workspaces))
	}
	if totalReclaims == 0 {
		t.Fatalf("%d captures under a %d byte global ceiling never reclaimed anything; the tiny-budget "+
			"stress did not exercise reclamation", published, integrationGlobalMax)
	}
	t.Logf("stress run (%d workspaces, %d captures each, all helpers under the TIGHT profile): "+
		"%d published, %d refused, %d generations reclaimed; PEAK physical %d vs global ceiling %d; "+
		"PEAK manager global %d; PEAK manager workspace %d vs workspace ceiling %d",
		len(workspaces), capturesPerWorkspace, published, refusals, totalReclaims,
		physicalPeak, integrationGlobalMax, managerPeak, workspacePeak, integrationWorkspaceMax)

	// Make every workspace INACTIVE, exactly as a deleted checkout or a removed
	// worktree does.
	for _, ws := range workspaces {
		if err := os.RemoveAll(ws); err != nil {
			t.Fatalf("remove workspace %s: %v", ws, err)
		}
	}

	// The store still knows every workspace, including the ones whose root no
	// longer exists, and it is compliant.
	m, err := NewManager(dataDir, integrationManagerOptions()...)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = m.Release() }()

	ctx := context.Background()
	discovery, err := m.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovery.Workspaces) != len(workspaces) {
		t.Fatalf("discovered %d workspaces, want %d: an inactive store was forgotten",
			len(discovery.Workspaces), len(workspaces))
	}
	for _, ws := range discovery.Workspaces {
		if ws.Layout != LayoutV2 {
			t.Errorf("workspace %s has layout %q, want v2", ws.Workspace, ws.Layout)
		}
		if ws.Corrupt {
			t.Errorf("workspace %s has an unreadable manifest", ws.Workspace)
		}
		if ws.Root == "" {
			t.Errorf("workspace %s forgot the root it was created for", ws.Workspace)
		}
		if !ws.AllowsCapture() {
			t.Errorf("workspace %s no longer permits capture after its checkout was deleted", ws.Workspace)
		}
		if ws.Bytes <= 0 {
			t.Errorf("workspace %s measured as empty", ws.Workspace)
		}
	}

	// A maintenance pass over every store leaves it compliant and reports
	// nothing that needs recovery.
	if _, err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := m.Status(ctx); err != nil {
		t.Fatalf("Status reports a non-compliant store after the stress run: %v", err)
	}
	usage, err := m.UsageContext(ctx)
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	if usage.GlobalAllocated > integrationGlobalMax {
		t.Fatalf("the store holds %d bytes, over the %d byte global ceiling",
			usage.GlobalAllocated, integrationGlobalMax)
	}
	if sampled := sampledAllocatedBytes(t, m.Root()); sampled > usage.GlobalAllocated {
		t.Fatalf("the manager accounted %d bytes while the filesystem holds %d", usage.GlobalAllocated, sampled)
	}
	if usage.LegacyAllocated != 0 || len(mustSurveyLegacy(t, m).Stores) != 0 {
		t.Fatal("the stress run produced a legacy store, which nothing in it should")
	}
}

// mustSurveyLegacy runs the read-only legacy survey.
func mustSurveyLegacy(t *testing.T, m *Manager) *LegacySurvey {
	t.Helper()
	survey, err := m.SurveyLegacy(context.Background())
	if err != nil {
		t.Fatalf("SurveyLegacy: %v", err)
	}
	return survey
}

// ---------------------------------------------------------------------------
// Process death at every durable boundary
// ---------------------------------------------------------------------------

// integrationCrashBoundaries names every boundary at which a process is killed
// outright, together with what recovery must do about it.
//
// The boundaries are the ones whose state is durable at that instant: a
// generation record without a repository, a generation repository still
// `creating`, an in-flight capture record without scratch, every object of a
// capture staged but unpublished, a publication ref written but not yet recorded
// in the manifest, a new manifest in place, a deletion decided but not performed,
// and a deletion intent with the directory still whole.
var integrationCrashBoundaries = []struct {
	name string
	// boundary is the crash point handed to the helper: either a write-event
	// kind observed by the manager, or a hook name.
	boundary string
	// reclaims reports whether the helper's run expires and reclaims a
	// generation in its own workspace, which is how the deletion boundaries are
	// reached.
	reclaims bool
	// expectAnchorsPreserved reports whether the hashes published before the
	// crash are required to still resolve afterwards.
	//
	// It is FALSE for the two deletion cases, where the crashed run itself asked
	// for the reclaim: finishing that already-authorised removal is the correct
	// recovery, and requiring the hash to survive would be requiring recovery to
	// undo an instruction. Those cases assert the typed expired answer instead.
	expectAnchorsPreserved bool
	// freshWorkspace makes the crash workspace start with NO generation at all,
	// so the crashing run is the one that creates its store.
	//
	// It is required for the two generation-creation boundaries: those hooks sit
	// inside CreateGeneration, so a pre-existing active generation — which is
	// exactly what a prior capture leaves — means the hook is never reached and
	// the crash cannot happen where the test claims. For those cases there is
	// also no hash published in the crash workspace to preserve; the anchor
	// workspace carries the preservation assertion.
	freshWorkspace bool
}{
	{name: "generation record written", boundary: "generation-record", freshWorkspace: true},
	{name: "generation repository created", boundary: "generation", freshWorkspace: true},
	{name: "capture staging reserved", boundary: "staging", expectAnchorsPreserved: true},
	{name: "objects staged, nothing published", boundary: "before-publish", expectAnchorsPreserved: true},
	{name: "publication ref written", boundary: "ref", expectAnchorsPreserved: true},
	{name: "manifest replaced", boundary: "manifest", expectAnchorsPreserved: true},
	{name: "deletion intent durable", boundary: "mark-deleting", reclaims: true},
	{name: "generation directory whole, deleting", boundary: "remove-generation-dir", reclaims: true},
}

// Process death at each durable boundary must be recoverable, and recovery must
// preserve every published hash.
//
// The test is deliberately a real crash: the helper calls os.Exit from inside a
// hook or a write observer, so no deferred cleanup runs and the store is left
// exactly as a power loss would leave it. The parent then reopens the store,
// reconciles, and asserts:
//
//   - the crash happened where the parent claims (the last record names the
//     boundary), so a boundary that was never reached cannot pass silently;
//   - every hash published before the crash still resolves — except in the
//     deletion cases, where the deletion was already authorised and finishing it
//     is the correct recovery;
//   - no in-flight capture record is left behind;
//   - the peak usage during both the crashed run and recovery stayed inside both
//     ceilings, measured independently at every boundary;
//   - the store is usable again: a fresh capture publishes.
func TestIntegrationCrashRecoveryPreservesEveryPublishedHash(t *testing.T) {
	requireGit(t)

	files := []integrationFile{
		{Rel: "weights.bin", Size: 64 << 10},
		{Rel: "empty.dat", Size: 0},
		{Rel: "tiny.txt", Size: 11},
	}

	for _, tc := range integrationCrashBoundaries {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			crashRoot := filepath.Join(dataDir, "crash-workspace")
			ctx := context.Background()

			// The profile decides what the assertion means.
			//
			// A "the published hash survived" claim is only about RECOVERY if the
			// budget left room for the capture that published it. Under a ceiling
			// tight enough to force reclamation, pressure would reclaim that very
			// generation and the test would silently be asserting the (also
			// correct) reclamation behaviour. So the preservation cases run with
			// headroom, and only the two deletion cases — whose whole purpose is
			// to reach `deleting` — run tight.
			profile := integrationProfiles["crash"]
			if tc.reclaims {
				profile = integrationProfiles["tiny"]
			}

			// ANCHOR WORKSPACE. Its snapshot is published before the crash and
			// must still resolve afterwards, whatever happens next door. It is a
			// separate workspace rather than the crashed one so a hash surviving
			// here is evidence of preservation rather than of the crashed
			// workspace's own generation having been reclaimed.
			anchorRoot := filepath.Join(dataDir, "anchor-workspace")
			if err := os.MkdirAll(anchorRoot, 0o755); err != nil {
				t.Fatalf("mkdir anchor workspace: %v", err)
			}
			code, anchorRecs := spawnIntegrationHelperWithProfile(t, integrationProfiles["tiny"], dataDir, anchorRoot,
				files, integrationModeCapture, "", "anchor")
			if code != 0 {
				t.Fatalf("anchor helper exited %d: %+v", code, anchorRecs)
			}
			integrationRecordsPeak(t, tc.name+" (anchor)", anchorRecs)
			anchorHashes := publishedHashes(anchorRecs)
			if len(anchorHashes) != 1 || !validObjectHash(anchorHashes[0]) {
				t.Fatalf("the anchor workspace published %v, want one valid snapshot", anchorHashes)
			}

			// The workspace whose process will die at the boundary under test.
			//
			// The generation-creation boundaries get a workspace with NO prior
			// capture, because those hooks only run while a generation is being
			// created; a workspace that already has an active generation would
			// never reach them.
			if err := os.MkdirAll(crashRoot, 0o755); err != nil {
				t.Fatalf("mkdir crash workspace: %v", err)
			}
			published := ""
			if !tc.freshWorkspace {
				code, cleanRecs := spawnIntegrationHelperWithProfile(t, profile, dataDir, crashRoot,
					files, integrationModeCapture, "", "clean")
				if code != 0 {
					t.Fatalf("clean helper exited %d: %+v", code, cleanRecs)
				}
				integrationRecordsPeak(t, tc.name+" (clean run)", cleanRecs)
				hashes := publishedHashes(cleanRecs)
				if len(hashes) != 1 || !validObjectHash(hashes[0]) {
					t.Fatalf("the clean run published %v, want one valid snapshot", hashes)
				}
				published = hashes[0]
			}

			// Now the process dies at the boundary under test.
			mode := integrationModeCrash
			if tc.reclaims {
				mode = integrationModeReclaim
			}
			code, crashRecs := spawnIntegrationHelperWithProfile(t, profile, dataDir, crashRoot, files, mode, tc.boundary, "crash")
			if code != integrationCrashExit {
				t.Fatalf("helper at %q exited %d, want the crash status %d: %+v",
					tc.boundary, code, integrationCrashExit, crashRecs)
			}
			integrationRecordsPeak(t, tc.name+" (crashed run)", crashRecs)
			last, ok := lastIntegrationRecord(crashRecs)
			if !ok {
				t.Fatalf("the crashed run reported nothing; the crash cannot be attributed to %q", tc.boundary)
			}
			if last.Kind != tc.boundary {
				t.Fatalf("the process died after boundary %q, not %q", last.Kind, tc.boundary)
			}
			crashPhysical := integrationRecordsPeak(t, tc.name+" (crashed run)", crashRecs)

			// Recover, instrumented at every boundary, so the RECOVERY pass is
			// bounded too — not only the crashed run.
			var mgr *Manager
			tracker := newIntegrationPeakTracker(filepath.Join(dataDir, snapshotsDirName), "", func() *Manager { return mgr },
				integrationCoarseKinds, profile)
			m, err := NewManager(dataDir, integrationManagerOptionsFor(profile, WithWriteObserver(func(ev WriteEvent) {
				tracker.observeEvent(ev.Kind)
			}))...)
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			mgr = m
			tracker.workspace = mustCatalog(t, m, WorkspaceHashFor(canonicalRootFor(t, crashRoot))).Workspace()
			if err := m.Bootstrap(ctx); err != nil {
				t.Fatalf("Bootstrap after the crash: %v", err)
			}
			if _, err := m.Acquire(ctx); err != nil {
				t.Fatalf("Acquire after the crash: %v", err)
			}
			defer func() { _ = m.Release() }()

			cat, err := m.CatalogForRoot(crashRoot)
			if err != nil {
				t.Fatalf("CatalogForRoot: %v", err)
			}
			anchorCat, err := m.CatalogForRoot(anchorRoot)
			if err != nil {
				t.Fatalf("CatalogForRoot(anchor): %v", err)
			}

			if _, err := m.Reconcile(ctx); err != nil {
				t.Fatalf("Reconcile after a crash at %q: %v", tc.boundary, err)
			}
			tracker.check(t, tc.name+" (recovery)")

			// THE ANCHOR IS UNCONDITIONAL: a crash in one workspace must never
			// cost another workspace its published snapshot.
			loc, err := m.LookupSnapshotOn(ctx, anchorCat, anchorHashes[0])
			if err != nil {
				t.Fatalf("the anchor workspace lost its published snapshot %s when a neighbouring "+
					"workspace crashed at %q: %v", anchorHashes[0], tc.boundary, err)
			}
			if loc.Legacy || loc.GenerationID == "" {
				t.Fatalf("the anchor snapshot resolves to %+v, want a v2 generation", loc)
			}

			// The crashed workspace: preserved, or — for the deletion cases — a
			// typed expiry, because finishing an already-authorised removal is
			// the correct recovery. A fresh workspace published nothing to
			// preserve; the anchor above is what carries that assertion.
			if published != "" {
				_, lookupErr := m.LookupSnapshotOn(ctx, cat, published)
				if tc.expectAnchorsPreserved {
					if lookupErr != nil {
						t.Fatalf("after a crash at %q the published snapshot %s no longer resolves: %v",
							tc.boundary, published, lookupErr)
					}
				} else if !errors.Is(lookupErr, ErrSnapshotExpired) {
					t.Fatalf("after a crash at %q the reclaimed snapshot = %v, want ErrSnapshotExpired",
						tc.boundary, lookupErr)
				}
			}

			// No in-flight record and no unresolved lifecycle state.
			man, err := cat.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if man != nil {
				if man.Capture != nil {
					t.Fatalf("recovery left an in-flight capture record: %+v", man.Capture)
				}
				for i := range man.Generations {
					if man.Generations[i].State == GenerationDeleting {
						t.Fatalf("recovery left generation %s in the deleting state", man.Generations[i].ID)
					}
				}
			}
			assertNoTempObjects(t, m.Root())

			// The store is usable again, and a capture after recovery still
			// publishes.
			writeWorkspaceFile(t, crashRoot, "after-recovery.txt", "recovered\n")
			fresh, ad, err := m.AdmitAndCaptureWithCleanup(ctx, cat, CaptureRequest{
				WorkspaceRoot: crashRoot,
				Now:           fixedTestTime(),
			})
			if err != nil {
				t.Fatalf("capture after recovery from %q: %v", tc.boundary, err)
			}
			if !validObjectHash(fresh) || ad.GenerationID == "" {
				t.Fatalf("the post-recovery capture published %q into generation %q", fresh, ad.GenerationID)
			}

			// The independent witness agrees with the manager after recovery, and
			// recovery did not push the store past a ceiling.
			usage, err := m.UsageContext(ctx)
			if err != nil {
				t.Fatalf("UsageContext: %v", err)
			}
			sampled := sampledAllocatedBytes(t, m.Root())
			if sampled > usage.GlobalAllocated {
				t.Fatalf("after recovery the manager accounted %d bytes while the filesystem holds %d",
					usage.GlobalAllocated, sampled)
			}
			if usage.GlobalAllocated > profile.GlobalMax {
				t.Fatalf("after recovery the store holds %d bytes, over the %d byte global ceiling",
					usage.GlobalAllocated, profile.GlobalMax)
			}
			if sampled > profile.GlobalMax {
				t.Fatalf("after recovery the filesystem holds %d bytes, over the %d byte global ceiling",
					sampled, profile.GlobalMax)
			}
			t.Logf("%s: after recovery manager %d bytes, filesystem %d bytes, ceiling %d",
				tc.name, usage.GlobalAllocated, sampled, profile.GlobalMax)
			if crashPhysical == 0 {
				t.Fatal("the crashed run reported no usage measurement")
			}
		})
	}
}

// canonicalRootFor resolves a workspace root the way the package does, so a
// test can compute the same workspace hash the manager does.
func canonicalRootFor(t *testing.T, root string) string {
	t.Helper()
	canonical, err := canonicalWorkspaceRoot(root)
	if err != nil {
		t.Fatalf("canonicalWorkspaceRoot(%s): %v", root, err)
	}
	return canonical
}

// ---------------------------------------------------------------------------
// A crash in the middle of one workspace must not disturb another
// ---------------------------------------------------------------------------

// Two independent workspaces share one root. A process that dies mid-capture in
// one of them must leave the other's published snapshot intact and the whole
// store under both ceilings — the accounting that makes a global bound real has
// to be correct across workspaces, not only within one.
func TestIntegrationCrashInOneWorkspaceLeavesAnotherIntact(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	dataDir := t.TempDir()
	files := []integrationFile{{Rel: "weights.bin", Size: 48 << 10}, {Rel: "tiny.txt", Size: 9}}

	survivor := filepath.Join(dataDir, "survivor")
	loser := filepath.Join(dataDir, "loser")
	for _, ws := range []string{survivor, loser} {
		if err := os.MkdirAll(ws, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", ws, err)
		}
	}

	code, recs := spawnIntegrationHelper(t, dataDir, survivor, files, integrationModeCapture, "", "survivor")
	if code != 0 {
		t.Fatalf("survivor helper exited %d: %+v", code, recs)
	}
	survivorHashes := publishedHashes(recs)
	if len(survivorHashes) != 1 {
		t.Fatalf("the survivor workspace published %v", survivorHashes)
	}

	// The other workspace dies with the publication ref already durable.
	code, crashRecs := spawnIntegrationHelperWithProfile(t, integrationProfiles["tiny"], dataDir, loser, files, integrationModeCrash, "ref", "loser")
	if code != integrationCrashExit {
		t.Fatalf("loser helper exited %d, want the crash status: %+v", code, crashRecs)
	}
	loserHashes := publishedHashes(crashRecs)
	integrationRecordsPeak(t, "two-workspace crash", crashRecs)

	m, err := NewManager(dataDir, integrationManagerOptions()...)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := m.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = m.Release() }()

	if _, err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	survivorCat, err := m.CatalogForRoot(survivor)
	if err != nil {
		t.Fatalf("CatalogForRoot(survivor): %v", err)
	}
	if _, err := m.LookupSnapshotOn(ctx, survivorCat, survivorHashes[0]); err != nil {
		t.Fatalf("the other workspace's snapshot was lost by a crash in a neighbouring store: %v", err)
	}

	// The crashed workspace either kept its ref (the ref was durable) or has been
	// reconciled to a clean state; what it must never be is a half-written store
	// that reports a usable snapshot it cannot serve.
	loserCat, err := m.CatalogForRoot(loser)
	if err != nil {
		t.Fatalf("CatalogForRoot(loser): %v", err)
	}
	for _, hash := range loserHashes {
		loc, err := m.LookupSnapshotOn(ctx, loserCat, hash)
		if err != nil {
			t.Fatalf("a published ref was not honoured by recovery: %v", err)
		}
		if loc.Hash != hash {
			t.Fatalf("lookup returned %s for %s", loc.Hash, hash)
		}
	}

	usage, err := m.UsageContext(ctx)
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	if usage.GlobalAllocated > integrationGlobalMax {
		t.Fatalf("the store holds %d bytes, over the %d byte global ceiling",
			usage.GlobalAllocated, integrationGlobalMax)
	}
	if sampled := sampledAllocatedBytes(t, m.Root()); sampled > usage.GlobalAllocated {
		t.Fatalf("the manager accounted %d bytes while the filesystem holds %d", usage.GlobalAllocated, sampled)
	}
	if usage.Generations[survivorCat.Workspace()] == nil {
		t.Fatal("the survivor workspace has no measured generations")
	}
	if usage.Generations[loserCat.Workspace()] == nil {
		t.Fatal("the crashed workspace has no measured generations")
	}
}
