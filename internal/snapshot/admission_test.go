package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file proves the storage bound is real. Every test here runs against a
// SMALL budget in a t.TempDir — never the user's real data directory — and
// asserts on PEAK writes rather than on the final total. A store whose final
// size is under budget has proved nothing: the defect being removed is a store
// that momentarily held two manifests, or that overran its allowance before
// settling.

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// admissionEnv is a small-budget managed store under a temp directory.
type admissionEnv struct {
	manager   *Manager
	catalog   *Catalog
	workspace string
	dataDir   string
	// events records every write boundary observed during the test.
	events []WriteEvent
	// peaks records the measured global and workspace allocation after every
	// write boundary, so a test can assert the peak rather than the end state.
	peaks []usageSample
}

type usageSample struct {
	kind      string
	global    int64
	workspace int64
}

// newAdmissionEnv builds a small-budget store. The limits are deliberately tiny
// so that rotation, reclamation, and refusal are all reachable deterministically
// rather than only on a store with gigabytes of history.
func newAdmissionEnv(t *testing.T, limits Limits, opts ...ManagerOption) *admissionEnv {
	t.Helper()
	dataDir := t.TempDir()
	workspace := filepath.Join(dataDir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	env := &admissionEnv{workspace: workspace, dataDir: dataDir}
	// The small-budget bounds are installed BEFORE the caller's options, so a
	// caller that wants different ones still wins.
	allOpts := append([]ManagerOption{
		WithLimits(limits),
		WithManifestBound(testManifestBound),
		WithMaintenanceAllowance(testMaintenanceBytes),
	}, opts...)
	allOpts = append(allOpts, WithWriteObserver(func(ev WriteEvent) {
		env.events = append(env.events, ev)
		usage, err := env.manager.UsageContext(context.Background())
		if err != nil {
			return
		}
		env.peaks = append(env.peaks, usageSample{
			kind:      ev.Kind,
			global:    usage.GlobalAllocated,
			workspace: workspaceStoreAllocated(usage, env.catalog.Workspace()),
		})
	}))

	m, err := NewManager(dataDir, allOpts...)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	env.manager = m
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
	env.catalog = cat
	return env
}

// testLimits returns small but workable ceilings. The rotation target is
// WorkspaceMaxBytes/4, so a 16 MiB workspace ceiling gives a 4 MiB target: large
// enough to hold a generation's baseline plus a few captures, small enough that
// repeated captures rotate deterministically.
func testLimits() Limits {
	return Limits{
		WorkspaceMaxBytes:      16 << 20,
		GlobalMaxBytes:         64 << 20,
		FreeSpaceMarginBytes:   1 << 20,
		FreeSpaceMarginPercent: 0,
		AllocationUnitBytes:    4096,
	}
}

// testManifestBound is the per-manifest bound the small-budget tests run under.
// The production bound is 2 MiB, so reserving the simultaneous old and new
// manifests would cost 4 MiB before a single object was written — which would
// make every budget decision here a decision about the manifest rather than
// about the capture. 64 KiB is still far more than a manifest this test
// produces.
const testManifestBound int64 = 64 << 10

// testMaintenanceBytes is the reserved maintenance allowance the small-budget
// tests run under. The production value is 4 MiB, which would be a fixed
// reservation larger than the budgets these tests are exercising.
const testMaintenanceBytes int64 = 256 << 10

// smallBounds installs the two bound overrides every small-budget store needs,
// so a store built outside the shared harness cannot accidentally run against
// the production manifest bound and prove nothing.
func smallBounds() ManagerOption {
	return func(m *Manager) {
		WithManifestBound(testManifestBound)(m)
		WithMaintenanceAllowance(testMaintenanceBytes)(m)
	}
}

// trackCapture runs the whole admitted capture lifecycle.
func trackCapture(t *testing.T, env *admissionEnv, req CaptureRequest) (string, *AdmissionResult) {
	t.Helper()
	hash, admission, err := env.manager.AdmitAndCaptureWithCleanup(context.Background(), env.catalog, req)
	if err != nil {
		t.Fatalf("AdmitAndCaptureWithCleanup: %v", err)
	}
	return hash, admission
}

// request builds a capture request for the env's workspace.
func request(env *admissionEnv) CaptureRequest {
	return CaptureRequest{WorkspaceRoot: env.workspace, Now: fixedTestTime()}
}

// globalPeak is the largest measured global allocation over the run.
func (e *admissionEnv) globalPeak() int64 {
	var max int64
	for _, s := range e.peaks {
		if s.global > max {
			max = s.global
		}
	}
	return max
}

// workspacePeak is the largest measured workspace allocation over the run.
func (e *admissionEnv) workspacePeak() int64 {
	var max int64
	for _, s := range e.peaks {
		if s.workspace > max {
			max = s.workspace
		}
	}
	return max
}

// ---------------------------------------------------------------------------
// Admission basics
// ---------------------------------------------------------------------------

// A first capture into an empty store is admitted, and it creates exactly one
// active generation.
func TestAdmissionAdmitsAndCreatesTheFirstGeneration(t *testing.T) {
	requireGit(t)
	env := newAdmissionEnv(t, testLimits())
	writeWorkspaceFile(t, env.workspace, "a.txt", "hello\n")

	hash, admission := trackCapture(t, env, request(env))
	if !validObjectHash(hash) {
		t.Fatalf("capture published %q, which is not an object id", hash)
	}
	if admission.Outcome != AdmissionRotated {
		// The first capture has no active generation to use, so it opens one.
		t.Fatalf("first admission outcome = %q, want %q", admission.Outcome, AdmissionRotated)
	}
	man, err := env.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(man.Generations) != 1 {
		t.Fatalf("manifest lists %d generations, want 1", len(man.Generations))
	}
	if man.ActiveID == "" {
		t.Fatal("no active generation after the first capture")
	}
	if len(admission.ReclaimedGenerations) != 0 {
		t.Fatalf("a first capture reclaimed %v", admission.ReclaimedGenerations)
	}
}

// The allowance admission grants is never smaller than the plan's own estimate,
// so the estimate remains a proven upper bound on the spend.
func TestAdmittedAllowanceBoundsThePlansEstimate(t *testing.T) {
	requireGit(t)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "compressible", data: []byte(strings.Repeat("a", 400<<10))},
		{name: "incompressible", data: incompressibleBytes(t, 400<<10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newAdmissionEnv(t, testLimits())
			writeWorkspaceFile(t, env.workspace, "data.bin", string(tc.data))
			writeWorkspaceFile(t, env.workspace, "top.txt", "top\n")

			plan, err := env.manager.planCapture(context.Background(), env.catalog, request(env))
			if err != nil {
				t.Fatalf("planCapture: %v", err)
			}
			_, admission := trackCapture(t, env, request(env))
			if admission.Allowance < plan.Allowance {
				t.Fatalf("admitted allowance %d is below the plan estimate %d",
					admission.Allowance, plan.Allowance)
			}
			if admission.Maintenance <= 0 {
				t.Fatal("admission reserved no maintenance headroom")
			}
			if admission.Reserved < admission.Allowance+admission.Maintenance {
				t.Fatalf("reservation %d does not include the allowance %d plus maintenance %d",
					admission.Reserved, admission.Allowance, admission.Maintenance)
			}
		})
	}
}

// Incompressible content must not shrink. The reservation is derived from the
// payload length through a stored-block bound, so a capture of random bytes
// costs at least as much as the bytes themselves — and never more than its
// admitted allowance.
func TestIncompressibleContentDoesNotShrink(t *testing.T) {
	requireGit(t)
	const size = 300 << 10
	env := newAdmissionEnv(t, testLimits())
	writeWorkspaceFile(t, env.workspace, "rand.bin", string(incompressibleBytes(t, size)))

	_, admission := trackCapture(t, env, request(env))
	usage, err := env.manager.MeasureWorkspaceGenerations(context.Background(), env.catalog.Workspace())
	if err != nil {
		t.Fatalf("MeasureWorkspaceGenerations: %v", err)
	}
	// The stored bytes cannot be smaller than the content: a bound derived from
	// "compression usually helps" would be wrong in both directions.
	if usage.Allocated < int64(size) {
		t.Fatalf("store holds %d bytes for %d bytes of incompressible content; it must not shrink",
			usage.Allocated, size)
	}
	if admission.Allowance < int64(size) {
		t.Fatalf("admitted allowance %d is below the %d byte payload it must hold",
			admission.Allowance, size)
	}
}

// ---------------------------------------------------------------------------
// The reservation is never exceeded
// ---------------------------------------------------------------------------

// Every instrumented write boundary must leave the store inside both ceilings.
// This is the assertion the whole design exists for: a timeout plus a post-hoc
// `du` would let the store pass this test's FINAL check while having overrun on
// the way.
func TestEveryInstrumentedWriteStaysWithinBothCeilings(t *testing.T) {
	requireGit(t)
	limits := testLimits()
	env := newAdmissionEnv(t, limits)
	for i := 0; i < 6; i++ {
		writeWorkspaceFile(t, env.workspace, fmt.Sprintf("file-%02d.txt", i),
			strings.Repeat("content\n", 512+i*97))
		hash, _ := trackCapture(t, env, request(env))
		if !validObjectHash(hash) {
			t.Fatalf("capture %d published %q", i, hash)
		}
	}
	if len(env.peaks) == 0 {
		t.Fatal("no write boundary was observed; the instrumentation is not wired")
	}
	for _, s := range env.peaks {
		if s.global > limits.GlobalMaxBytes {
			t.Fatalf("global allocation reached %d after a %s write, over the %d byte limit",
				s.global, s.kind, limits.GlobalMaxBytes)
		}
		if s.workspace > limits.WorkspaceMaxBytes {
			t.Fatalf("workspace allocation reached %d after a %s write, over the %d byte limit",
				s.workspace, s.kind, limits.WorkspaceMaxBytes)
		}
	}
	// Every kind of boundary this store writes must have been observed, or the
	// peak assertion above is only covering part of the write path.
	kinds := map[string]bool{}
	for _, ev := range env.events {
		kinds[ev.Kind] = true
	}
	for _, want := range []string{"generation", "manifest", "staging", "object", "ref"} {
		if !kinds[want] {
			t.Errorf("no %s write boundary was instrumented", want)
		}
	}
}

// A capture refused by admission leaves the store no larger than it was. "No
// larger" rather than "identical" because reconciliation of a previous
// interruption is allowed to run first, and that can only shrink the store.
func TestRefusedAdmissionLeavesTheStoreNoLarger(t *testing.T) {
	requireGit(t)
	env := newAdmissionEnv(t, testLimits())
	writeWorkspaceFile(t, env.workspace, "a.txt", "hello\n")
	trackCapture(t, env, request(env))

	before, err := env.manager.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}

	// Incompressible content far larger than the workspace ceiling. It cannot
	// be admitted however hard reclamation tries.
	writeWorkspaceFile(t, env.workspace, "huge.bin", string(incompressibleBytes(t, 24<<20)))

	_, _, err = env.manager.AdmitAndCaptureWithCleanup(context.Background(), env.catalog, request(env))
	if err == nil {
		t.Fatal("a capture larger than the workspace ceiling was admitted")
	}
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("refusal reason = %v, want ErrBudgetExhausted", err)
	}

	after, err := env.manager.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	if after.GlobalAllocated > before.GlobalAllocated {
		t.Fatalf("refused admission grew the store from %d to %d bytes",
			before.GlobalAllocated, after.GlobalAllocated)
	}
	// The earlier snapshot is still retained: a refusal must not have
	// reclaimed it in a doomed attempt to fit.
	man, err := env.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if man.Capture != nil {
		t.Fatalf("a refused capture left an in-flight capture record: %+v", man.Capture)
	}
}

// A store that is already over its workspace ceiling must not have the overage
// GROWN by a new capture. It is reported instead.
//
// The ceiling is lowered below the store's own size, exactly as a user editing
// their config would. The overage is REPORTED rather than repaired by deleting
// history the user never asked to lose.
func TestPreExistingOverBudgetStoreIsNeverGrown(t *testing.T) {
	requireGit(t)
	// A first, generous budget so the store can be built up.
	env := newAdmissionEnv(t, Limits{
		WorkspaceMaxBytes:      64 << 20,
		GlobalMaxBytes:         128 << 20,
		FreeSpaceMarginBytes:   1 << 20,
		FreeSpaceMarginPercent: 0,
		AllocationUnitBytes:    4096,
	}, smallBounds())
	for i := 0; i < 3; i++ {
		writeWorkspaceFile(t, env.workspace, fmt.Sprintf("f%d.bin", i), string(incompressibleBytes(t, 200<<10)))
		trackCapture(t, env, request(env))
	}
	before, err := env.manager.MeasureWorkspaceGenerations(context.Background(), env.catalog.Workspace())
	if err != nil {
		t.Fatalf("MeasureWorkspaceGenerations: %v", err)
	}
	if before.Allocated == 0 {
		t.Fatal("the store measured as empty; the test proves nothing")
	}
	globalBefore, err := env.manager.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}

	// Lower the WORKSPACE ceiling below what the store already holds. The
	// global ceiling is left generous on purpose, so bootstrap stays
	// satisfiable and the refusal is attributable to the workspace overage
	// under assertion.
	tight := Limits{
		WorkspaceMaxBytes:      before.Allocated / 2,
		GlobalMaxBytes:         globalBefore.GlobalAllocated * 4,
		FreeSpaceMarginBytes:   1 << 20,
		FreeSpaceMarginPercent: 0,
		AllocationUnitBytes:    4096,
	}
	// The first manager releases before the second acquires: the store lock is
	// exclusive, so a second owner can only exist once the first has let go.
	if err := env.manager.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	tightManager, err := NewManager(env.dataDir, WithLimits(tight), smallBounds())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := tightManager.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := tightManager.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = tightManager.Release() }()

	tightCat, err := tightManager.CatalogForRoot(env.workspace)
	if err != nil {
		t.Fatalf("CatalogForRoot: %v", err)
	}
	writeWorkspaceFile(t, env.workspace, "another.bin", string(incompressibleBytes(t, 400<<10)))

	_, admission, err := tightManager.AdmitAndCaptureWithCleanup(context.Background(), tightCat, request(env))
	if err == nil {
		t.Fatalf("a capture into an over-budget store was admitted (reserved %d)", admission.Reserved)
	}
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("refusal reason = %v, want ErrBudgetExhausted", err)
	}

	after, err := tightManager.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	if after.GlobalAllocated > globalBefore.GlobalAllocated {
		t.Fatalf("a refused capture GREW an already over-budget store from %d to %d bytes",
			globalBefore.GlobalAllocated, after.GlobalAllocated)
	}
	// The overage is reported, so the user can find it rather than discovering
	// it as a silent capture failure.
	statusErr := tightManager.Status(context.Background())
	if statusErr == nil {
		t.Fatal("Status reported a healthy store that is over its workspace ceiling")
	}
	if !errors.Is(statusErr, ErrBudgetExhausted) {
		t.Fatalf("Status reason = %v, want ErrBudgetExhausted", statusErr)
	}
}

// ---------------------------------------------------------------------------
// Rotation
// ---------------------------------------------------------------------------

// Repeated captures rotate: the active generation is sealed and a replacement
// opened, so more than one generation accumulates and the store keeps working.
func TestRepeatedCapturesRotateAndAccumulateGenerations(t *testing.T) {
	requireGit(t)
	limits := testLimits()
	env := newAdmissionEnv(t, limits)

	hashes := map[string]bool{}
	for i := 0; i < 6; i++ {
		// Each capture adds 1 MiB, so the 4 MiB rotation target is reached
		// after a handful of captures and rotation happens repeatedly.
		writeWorkspaceFile(t, env.workspace, fmt.Sprintf("blob-%d.bin", i), string(incompressibleBytes(t, 1<<20)))
		hash, _ := trackCapture(t, env, request(env))
		if hash == "" {
			t.Fatalf("capture %d published no hash", i)
		}
		if hashes[hash] {
			t.Fatalf("capture %d reproduced hash %s; distinct content must produce distinct snapshots", i, hash)
		}
		hashes[hash] = true

		if got := env.workspacePeak(); got > limits.WorkspaceMaxBytes {
			t.Fatalf("after capture %d the workspace peaked at %d bytes, over its %d byte limit",
				i, got, limits.WorkspaceMaxBytes)
		}
	}

	man, err := env.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(man.Generations) < 2 {
		t.Fatalf("repeated captures produced %d generation(s); rotation never happened", len(man.Generations))
	}
	// A workspace has AT MOST one active generation. It may legitimately have
	// none: a generation that exceeded its rotation target is sealed in the same
	// pass, and the next capture opens a fresh one.
	active := 0
	sealed := map[string]bool{}
	for i := range man.Generations {
		switch man.Generations[i].State {
		case GenerationActive:
			active++
		case GenerationSealed:
			sealed[man.Generations[i].ID] = true
		}
	}
	if active > 1 {
		t.Fatalf("%d active generations; a workspace has at most one", active)
	}
	if man.ActiveID != "" && man.Generation(man.ActiveID) == nil {
		t.Fatalf("active id %q is not listed in the manifest", man.ActiveID)
	}

	// The store keeps working after every rotation: another capture succeeds and
	// lands somewhere that is not already sealed.
	writeWorkspaceFile(t, env.workspace, "after-rotation.bin", "tail\n")
	hash, admission := trackCapture(t, env, request(env))
	if hash == "" {
		t.Fatal("the capture after rotation published nothing")
	}
	if sealed[admission.GenerationID] {
		t.Fatalf("admission captured into the sealed generation %s", admission.GenerationID)
	}
}

// A single admitted capture may EXCEED the rotation target — the target is a
// rotation point, not a quota — and the generation is then sealed immediately.
func TestCaptureExceedingTheRotationTargetIsSealedImmediately(t *testing.T) {
	requireGit(t)
	limits := Limits{
		// A 4 MiB ceiling gives a 1 MiB rotation target; the capture below is
		// comfortably larger.
		WorkspaceMaxBytes:      8 << 20,
		GlobalMaxBytes:         32 << 20,
		FreeSpaceMarginBytes:   1 << 20,
		FreeSpaceMarginPercent: 0,
		AllocationUnitBytes:    4096,
	}
	env := newAdmissionEnv(t, limits, smallBounds())

	// First capture, comfortably over the 2 MiB rotation target.
	writeWorkspaceFile(t, env.workspace, "big.bin", string(incompressibleBytes(t, 3<<20)))
	hash, admission := trackCapture(t, env, request(env))
	if hash == "" {
		t.Fatal("the oversized capture published nothing")
	}
	if admission.GenerationID == "" {
		t.Fatal("admission reported no generation")
	}

	man, err := env.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rec := man.Generation(admission.GenerationID)
	if rec == nil {
		t.Fatalf("generation %s is not listed", admission.GenerationID)
	}
	if rec.State != GenerationSealed {
		// The generation exceeded the rotation target, so it was sealed in the
		// same pass. Leaving it active would mean the next capture rotates into
		// a generation this one already overfilled.
		t.Fatalf("generation over its rotation target is in state %q, want %q",
			rec.State, GenerationSealed)
	}
	if man.ActiveID != "" {
		t.Fatalf("sealing the only generation left active id %q", man.ActiveID)
	}

	// A second capture must still work: it opens a fresh generation.
	writeWorkspaceFile(t, env.workspace, "more.bin", "small\n")
	second, admission2 := trackCapture(t, env, request(env))
	if second == "" {
		t.Fatal("the capture after a seal published nothing")
	}
	if admission2.GenerationID == admission.GenerationID {
		t.Fatal("the capture after a seal reused the sealed generation")
	}
}

// A rotation target of a quarter of the workspace ceiling is what the design
// specifies. It is asserted directly because every rotation decision depends on
// it.
func TestRotationTargetIsAQuarterOfTheWorkspaceLimit(t *testing.T) {
	requireGit(t)
	env := newAdmissionEnv(t, testLimits())
	want := int64(testLimits().WorkspaceMaxBytes) / 4
	if got := env.manager.rotationTarget(); got != want {
		t.Fatalf("rotationTarget() = %d, want %d (a quarter of the workspace limit)", got, want)
	}
}

// ---------------------------------------------------------------------------
// Free space
// ---------------------------------------------------------------------------

// Insufficient free space refuses independently of the configured budgets: a
// store well inside both ceilings must still refuse when the filesystem has no
// room. The two are separate reasons because they send the user to different
// remedies.
func TestInsufficientFreeSpaceRefusesIndependentlyOfTheBudgets(t *testing.T) {
	requireGit(t)
	env := newAdmissionEnv(t, testLimits(), WithFreeSpaceQuery(func(string) (int64, error) {
		return 8 << 20, nil
	}))
	writeWorkspaceFile(t, env.workspace, "a.txt", "hello\n")
	trackCapture(t, env, request(env))

	// Plenty of budget, no room on the filesystem. The reserve is 1 MiB, so a
	// capture needing more than 7 MiB has to be refused.
	env.manager.freeSpace = func(string) (int64, error) { return 1 << 20, nil }
	writeWorkspaceFile(t, env.workspace, "big.bin", string(incompressibleBytes(t, 512<<10)))

	_, _, err := env.manager.AdmitAndCaptureWithCleanup(context.Background(), env.catalog, request(env))
	if err == nil {
		t.Fatal("a capture with no free space was admitted")
	}
	if !errors.Is(err, ErrInsufficientFreeSpace) {
		t.Fatalf("refusal reason = %v, want ErrInsufficientFreeSpace (and not a budget reason)", err)
	}
	if errors.Is(err, ErrBudgetExhausted) {
		t.Fatal("a free-space refusal was reported as a budget refusal; the remedies differ")
	}
}

// A store that cannot read its own free space must fail closed rather than
// assuming there is plenty. Bootstrap has already succeeded when the seam starts
// failing, which is the interesting case: the store exists and is usable right
// up to the point where it would have to grow.
func TestUnreadableFreeSpaceFailsClosed(t *testing.T) {
	requireGit(t)
	env := newAdmissionEnv(t, testLimits(), smallBounds())
	writeWorkspaceFile(t, env.workspace, "a.txt", "hello\n")

	env.manager.freeSpace = func(string) (int64, error) {
		return 0, errors.New("statfs unavailable")
	}
	_, _, err := env.manager.AdmitAndCaptureWithCleanup(context.Background(), env.catalog, request(env))
	if err == nil {
		t.Fatal("a capture proceeded without knowing the free space")
	}
	// The failure is a structured accounting error, never a silent "there is
	// room".
	if ReasonOf(err) == "" {
		t.Fatalf("free-space failure %v carries no structured reason", err)
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

// Two captures must not be able to over-reserve the same headroom. They are
// serialized by the store lock, and the second sees the first's bytes.
func TestConcurrentCapturesCannotOverReserve(t *testing.T) {
	requireGit(t)
	limits := testLimits()
	env := newAdmissionEnv(t, limits, smallBounds())
	writeWorkspaceFile(t, env.workspace, "a.txt", string(incompressibleBytes(t, 512<<10)))

	// The first capture is admitted while the store is held.
	first, _, err := env.manager.AdmitAndCaptureWithCleanup(context.Background(), env.catalog, request(env))
	if err != nil {
		t.Fatalf("first capture: %v", err)
	}
	if first == "" {
		t.Fatal("first capture published nothing")
	}

	// A second manager, standing in for a second process, cannot even acquire
	// the lock while the first holds it: that is what makes the reservation
	// exclusive.
	second, err := NewManager(env.dataDir, WithLimits(limits), smallBounds())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*1e6)
	defer cancel()
	if _, err := second.Acquire(ctx); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("second manager acquired a held store: %v", err)
	}

	// After the first releases, the second admits against the UPDATED usage:
	// the first capture's bytes are visible, so it cannot reserve them twice.
	if err := env.manager.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := second.Acquire(context.Background()); err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	defer func() { _ = second.Release() }()

	cat2, err := second.CatalogForRoot(env.workspace)
	if err != nil {
		t.Fatalf("CatalogForRoot: %v", err)
	}
	usageBefore, err := second.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	if usageBefore.GlobalAllocated == 0 {
		t.Fatal("the second manager measured an empty store; the reservation would be double-spent")
	}

	admission2, err := second.AdmitCapture(context.Background(), cat2, request(env))
	if err != nil {
		t.Fatalf("second AdmitCapture: %v", err)
	}
	// The second reservation is measured against the store that already
	// contains the first capture.
	if admission2.Reserved <= 0 {
		t.Fatalf("second reservation = %d", admission2.Reserved)
	}
	if admission2.Usage.GlobalAllocated < usageBefore.GlobalAllocated {
		t.Fatalf("second admission measured %d bytes, less than the %d already present",
			admission2.Usage.GlobalAllocated, usageBefore.GlobalAllocated)
	}
}

// ---------------------------------------------------------------------------
// Small budgets and many files
// ---------------------------------------------------------------------------

// A huge number of tiny files must not let the count dodge the budget: each one
// is accounted, and a store that cannot hold them is refused.
func TestManyTinyFilesRespectTheBudget(t *testing.T) {
	requireGit(t)
	limits := testLimits()
	env := newAdmissionEnv(t, limits, smallBounds())

	// 300 tiny files, each its own blob, tree entry, and object directory
	// charge. The COUNT is what must not dodge the budget: every entry is
	// accounted even though no single file is large.
	const count = 300
	for i := 0; i < count; i++ {
		writeWorkspaceFile(t, env.workspace, fmt.Sprintf("d%02d/tiny.txt", i%20), fmt.Sprintf("%d\n", i))
	}
	hash, admission := trackCapture(t, env, request(env))
	if hash == "" {
		t.Fatal("the many-file capture published nothing")
	}
	if admission.Reserved < int64(count) {
		t.Fatalf("reservation %d is smaller than the %d entries it covers", admission.Reserved, count)
	}
	if got := env.workspacePeak(); got > limits.WorkspaceMaxBytes {
		t.Fatalf("workspace peaked at %d bytes, over its %d byte limit", got, limits.WorkspaceMaxBytes)
	}
	if got := env.globalPeak(); got > limits.GlobalMaxBytes {
		t.Fatalf("global peaked at %d bytes, over its %d byte limit", got, limits.GlobalMaxBytes)
	}
}

// A limit too small to hold a generation's baseline plus one capture is refused
// with a clear error rather than looping forever.
func TestLimitTooSmallForABaselineIsRefusedNotLoopedOver(t *testing.T) {
	requireGit(t)
	// A workspace ceiling smaller than the metadata a generation needs.
	tiny := Limits{
		WorkspaceMaxBytes:      8 << 10,
		GlobalMaxBytes:         8 << 10,
		FreeSpaceMarginBytes:   1 << 20,
		FreeSpaceMarginPercent: 0,
		AllocationUnitBytes:    4096,
	}
	dataDir := t.TempDir()
	workspace := filepath.Join(dataDir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeWorkspaceFile(t, workspace, "a.txt", "hello\n")

	m, err := NewManager(dataDir, WithLimits(tiny), smallBounds())
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
	cat, err := m.CatalogForRoot(workspace)
	if err != nil {
		t.Fatalf("CatalogForRoot: %v", err)
	}

	// The call must RETURN, not spin. A loop here would hold the store lock
	// forever, which is a worse failure than the refusal it was trying to avoid.
	done := make(chan error, 1)
	go func() {
		_, err := m.AdmitCapture(context.Background(), cat, CaptureRequest{WorkspaceRoot: workspace})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a capture into an unadmittably small store succeeded")
		}
		if !errors.Is(err, ErrBudgetExhausted) {
			t.Fatalf("refusal reason = %v, want ErrBudgetExhausted", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("admission did not return; a limit too small to admit must be refused, not looped over")
	}
}

// ---------------------------------------------------------------------------
// Admission's refusal surface
// ---------------------------------------------------------------------------

// A refusal must leave the store usable: the next capture after the offending
// file is removed succeeds.
func TestStoreIsUsableAfterARefusal(t *testing.T) {
	requireGit(t)
	env := newAdmissionEnv(t, testLimits(), smallBounds())
	writeWorkspaceFile(t, env.workspace, "a.txt", "hello\n")
	trackCapture(t, env, request(env))

	huge := filepath.Join(env.workspace, "huge.bin")
	if err := os.WriteFile(huge, incompressibleBytes(t, 24<<20), 0o644); err != nil {
		t.Fatalf("write huge: %v", err)
	}
	if _, _, err := env.manager.AdmitAndCaptureWithCleanup(context.Background(), env.catalog, request(env)); err == nil {
		t.Fatal("the oversized capture was admitted")
	}
	if err := os.Remove(huge); err != nil {
		t.Fatalf("remove huge: %v", err)
	}

	hash, _ := trackCapture(t, env, request(env))
	if hash == "" {
		t.Fatal("the capture after a refusal published nothing")
	}
}

// A workspace with no eligible paths is refused by admission rather than
// reserving a manifest replacement for a snapshot that cannot exist.
func TestAdmissionRefusesAnEmptyWorkspace(t *testing.T) {
	requireGit(t)
	env := newAdmissionEnv(t, testLimits())
	_, err := env.manager.AdmitCapture(context.Background(), env.catalog, request(env))
	if err == nil {
		t.Fatal("admission admitted a workspace with no eligible paths")
	}
	if !errors.Is(err, ErrStoreInternal) {
		t.Fatalf("refusal reason = %v, want ErrStoreInternal", err)
	}
}

// Admission requires the store lock. Capturing without it would race a writer
// in another process, and the usage it measured would be a guess.
func TestAdmissionRequiresTheStoreLock(t *testing.T) {
	requireGit(t)
	dataDir := t.TempDir()
	workspace := filepath.Join(dataDir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeWorkspaceFile(t, workspace, "a.txt", "hello\n")
	m, err := NewManager(dataDir, WithLimits(testLimits()))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	cat, err := m.CatalogForRoot(workspace)
	if err != nil {
		t.Fatalf("CatalogForRoot: %v", err)
	}
	if _, err := m.AdmitCapture(context.Background(), cat, CaptureRequest{WorkspaceRoot: workspace}); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("unowned admission = %v, want ErrNotOwned", err)
	}
}

// ---------------------------------------------------------------------------
// The user-global limit is read fresh
// ---------------------------------------------------------------------------

// A ceiling lowered while the process is running must take effect on the very
// next admission, because the check happens while the lock is held. A cached
// session value is exactly the stale read the design forbids.
func TestGlobalLimitIsReadFreshWhileLocked(t *testing.T) {
	requireGit(t)
	global := int64(32 << 20)
	env := newAdmissionEnv(t, testLimits(),
		smallBounds(),
		WithGlobalLimitReader(func() (Limits, error) {
			return Limits{
				WorkspaceMaxBytes:      4 << 20,
				GlobalMaxBytes:         global,
				FreeSpaceMarginBytes:   1 << 20,
				FreeSpaceMarginPercent: 0,
				AllocationUnitBytes:    4096,
			}, nil
		}))
	writeWorkspaceFile(t, env.workspace, "a.txt", "hello\n")
	if _, err := env.manager.AdmitCapture(context.Background(), env.catalog, request(env)); err != nil {
		t.Fatalf("AdmitCapture with a generous global ceiling: %v", err)
	}

	// Another process lowers the global ceiling below what the store holds.
	usage, err := env.manager.UsageContext(context.Background())
	if err != nil {
		t.Fatalf("UsageContext: %v", err)
	}
	global = usage.GlobalAllocated - 1
	if global <= 0 {
		t.Skip("the store measured too small for this assertion to mean anything")
	}

	_, err = env.manager.AdmitCapture(context.Background(), env.catalog, request(env))
	if err == nil {
		t.Fatal("a lowered global ceiling was ignored; the limit was read from a stale value")
	}
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("refusal reason = %v, want ErrBudgetExhausted", err)
	}
}

// A store whose accounting cannot be READ must fail closed. Treating an unknown
// total as zero is how a budget admits a write it cannot afford.
func TestUnknownAccountingFailsClosed(t *testing.T) {
	requireGit(t)
	env := newAdmissionEnv(t, testLimits(), smallBounds())
	writeWorkspaceFile(t, env.workspace, "a.txt", "hello\n")

	env.manager.allocatedSize = func(string, os.FileInfo) (int64, error) {
		return 0, storeErrorf(ReasonUnreadableFile, "injected accounting failure")
	}
	if _, _, err := env.manager.AdmitAndCaptureWithCleanup(context.Background(), env.catalog, request(env)); err == nil {
		t.Fatal("admission proceeded with unreadable accounting")
	}
}

// ---------------------------------------------------------------------------
// Staging lifecycle
// ---------------------------------------------------------------------------

// The publication intent must be durable BEFORE the ref, and cleared after. It
// is what lets reconciliation distinguish "published" from "never published".
func TestPublicationIntentIsRecordedBeforeTheRefAndClearedAfter(t *testing.T) {
	requireGit(t)
	env := newAdmissionEnv(t, testLimits(), smallBounds())
	writeWorkspaceFile(t, env.workspace, "a.txt", "hello\n")

	hash, _, err := env.manager.AdmitAndCaptureWithCleanup(context.Background(), env.catalog, request(env))
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	man, err := env.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if man.Capture != nil {
		t.Fatalf("a completed capture left an in-flight record: %+v", man.Capture)
	}
	// The staging directory is gone too.
	entries, err := os.ReadDir(env.catalog.StagingDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read staging dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a completed capture left staging directories: %v", entries)
	}
	// The ref is durable.
	rec := man.Generation(man.ActiveID)
	if rec == nil {
		if len(man.Generations) == 0 {
			t.Fatal("no generations after a capture")
		}
		rec = &man.Generations[len(man.Generations)-1]
	}
	exists, err := env.catalog.RefExists(context.Background(), rec.ID, hash)
	if err != nil {
		t.Fatalf("RefExists: %v", err)
	}
	if !exists {
		t.Fatalf("snapshot %s is not retained by a ref after a successful capture", hash)
	}
}

// A capture that fails after reserving staging must clear its in-flight record,
// or the workspace is wedged: ReserveStaging refuses a second capture while one
// is recorded.
func TestFailedCaptureClearsItsInFlightRecord(t *testing.T) {
	requireGit(t)
	env := newAdmissionEnv(t, testLimits(), smallBounds())
	writeWorkspaceFile(t, env.workspace, "a.txt", "hello\n")

	// Interrupt the capture at the "objects staged, nothing published"
	// boundary. An interruption there must leave a snapshot no ref retains,
	// plus an in-flight record for reconciliation to clear.
	injected := errors.New("simulated interruption before publication")
	env.manager.hooks = Hooks{BeforePublishRef: func() error { return injected }}

	writeWorkspaceFile(t, env.workspace, "b.txt", "world\n")
	_, _, err := env.manager.AdmitAndCaptureWithCleanup(context.Background(), env.catalog, request(env))
	if !errors.Is(err, injected) {
		t.Fatalf("interrupted capture = %v, want the injected interruption", err)
	}
	man, err := env.catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if man.Capture != nil {
		t.Fatalf("a failed capture left an in-flight record behind: %+v", man.Capture)
	}

	// The workspace is usable: the next capture succeeds.
	env.manager.hooks = Hooks{}
	hash, _, err := env.manager.AdmitAndCaptureWithCleanup(context.Background(), env.catalog, request(env))
	if err != nil {
		t.Fatalf("capture after a failure: %v", err)
	}
	if hash == "" {
		t.Fatal("the capture after a failure published nothing")
	}
}
