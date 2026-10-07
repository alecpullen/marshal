package snapshot

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newTestManager builds a manager rooted under a fresh temporary directory.
// Every accounting and ownership test uses one of these: no test may touch the
// real ~/.local/share/marshal.
func newTestManager(t *testing.T, opts ...ManagerOption) *Manager {
	t.Helper()
	dataDir := t.TempDir()
	m, err := NewManager(dataDir, opts...)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestNewManagerRootsAtSnapshotsDir(t *testing.T) {
	dataDir := t.TempDir()
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	want := filepath.Join(dataDir, "snapshots")
	if m.Root() != want {
		t.Fatalf("Root() = %q, want %q", m.Root(), want)
	}
	if m.DataDir() != dataDir {
		t.Fatalf("DataDir() = %q, want %q", m.DataDir(), dataDir)
	}
}

func TestNewManagerRejectsEmptyDataDir(t *testing.T) {
	if _, err := NewManager(""); err == nil {
		t.Fatal("NewManager(\"\") should fail")
	}
}

func TestNewManagerRejectsInvalidLimits(t *testing.T) {
	_, err := NewManager(t.TempDir(), WithLimits(Limits{WorkspaceMaxBytes: -1}))
	if !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("NewManager error = %v, want ErrInvalidLimits", err)
	}
}

// Defaults must be the design's 2 GiB / 10 GiB, and a zero field must mean
// "use the default" rather than "no budget".
func TestDefaultLimitsMatchDesign(t *testing.T) {
	l := DefaultLimits()
	if l.WorkspaceMaxBytes != 2*1024*1024*1024 {
		t.Errorf("WorkspaceMaxBytes = %d, want 2 GiB", l.WorkspaceMaxBytes)
	}
	if l.GlobalMaxBytes != 10*1024*1024*1024 {
		t.Errorf("GlobalMaxBytes = %d, want 10 GiB", l.GlobalMaxBytes)
	}
	zero := Limits{}.Normalize()
	if zero.WorkspaceMaxBytes != 2*1024*1024*1024 || zero.GlobalMaxBytes != 10*1024*1024*1024 {
		t.Errorf("zero Limits normalized to %+v, want production defaults", zero)
	}
}

func TestLimitsValidate(t *testing.T) {
	tests := []struct {
		name string
		l    Limits
		ok   bool
	}{
		{"zero is allowed and means default", Limits{}, true},
		{"defaults", DefaultLimits(), true},
		{"negative workspace", Limits{WorkspaceMaxBytes: -5}, false},
		{"negative global", Limits{GlobalMaxBytes: -5}, false},
		{"negative margin", Limits{FreeSpaceMarginBytes: -5}, false},
		{"percent over 100", Limits{FreeSpaceMarginPercent: 101}, false},
		{"negative percent", Limits{FreeSpaceMarginPercent: -1}, false},
		{"negative allocation unit", Limits{AllocationUnitBytes: -1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.l.Validate()
			if tt.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("Validate() = nil, want error")
			}
		})
	}
}

// The free-space reserve is max(floor, percent of allowance). The margin must
// be injectable with small numbers so tests need not create gigabytes.
func TestFreeSpaceReserveIsLargerOfFloorAndPercent(t *testing.T) {
	small := Limits{FreeSpaceMarginBytes: 1000, FreeSpaceMarginPercent: 2}
	got, err := small.FreeSpaceReserve(1000)
	if err != nil {
		t.Fatalf("FreeSpaceReserve: %v", err)
	}
	if got != 1000 {
		t.Fatalf("reserve for allowance 1000 = %d, want the 1000-byte floor", got)
	}
	got, err = small.FreeSpaceReserve(100_000)
	if err != nil {
		t.Fatalf("FreeSpaceReserve: %v", err)
	}
	if got != 2000 {
		t.Fatalf("reserve for allowance 100000 = %d, want 2%% = 2000", got)
	}
	if _, err := small.FreeSpaceReserve(-1); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("negative allowance error = %v, want ErrInvalidLimits", err)
	}
}

func TestLimitsRoundUpAllocationUnits(t *testing.T) {
	l := Limits{AllocationUnitBytes: 4096}
	got, err := l.RoundUp(1)
	if err != nil {
		t.Fatalf("RoundUp: %v", err)
	}
	if got != 4096 {
		t.Fatalf("RoundUp(1) = %d, want one whole allocation unit", got)
	}
	got, err = l.RoundUp(4096)
	if err != nil {
		t.Fatalf("RoundUp: %v", err)
	}
	if got != 4096 {
		t.Fatalf("RoundUp(4096) = %d, want 4096", got)
	}
	got, err = l.RoundUp(4097)
	if err != nil {
		t.Fatalf("RoundUp: %v", err)
	}
	if got != 8192 {
		t.Fatalf("RoundUp(4097) = %d, want 8192", got)
	}
	// RoundUp(0) is one unit, not zero: an empty file still occupies storage.
	got, err = l.RoundUp(0)
	if err != nil {
		t.Fatalf("RoundUp: %v", err)
	}
	if got != 4096 {
		t.Fatalf("RoundUp(0) = %d, want one allocation unit", got)
	}
}

// Overflow must be an error, never a silent wrap: a wrapped total would
// under-report usage and admit writes the budget never allowed.
func TestLimitsRoundUpOverflowIsError(t *testing.T) {
	l := Limits{AllocationUnitBytes: 4096}
	_, err := l.RoundUp(math.MaxInt64)
	if !errors.Is(err, ErrAccountingOverflow) {
		t.Fatalf("RoundUp(MaxInt64) error = %v, want ErrAccountingOverflow", err)
	}
}

func TestAddInt64OverflowIsError(t *testing.T) {
	if _, err := addInt64(math.MaxInt64, 1); !errors.Is(err, ErrAccountingOverflow) {
		t.Fatalf("addInt64 overflow error = %v, want ErrAccountingOverflow", err)
	}
	if _, err := addInt64(math.MinInt64, -1); !errors.Is(err, ErrAccountingOverflow) {
		t.Fatalf("addInt64 underflow error = %v, want ErrAccountingOverflow", err)
	}
	got, err := addInt64(2, 3)
	if err != nil || got != 5 {
		t.Fatalf("addInt64(2,3) = %d, %v; want 5, nil", got, err)
	}
}

func TestMulDivOverflowIsError(t *testing.T) {
	if _, err := mulDiv(math.MaxInt64, 2, 100); !errors.Is(err, ErrAccountingOverflow) {
		t.Fatalf("mulDiv overflow error = %v, want ErrAccountingOverflow", err)
	}
}

// Structured reasons must be reachable both structurally and with errors.Is, so
// later tasks and the UI can distinguish failures without matching strings.
func TestStoreErrorReasonsAreStructured(t *testing.T) {
	err := storeErrorf(ReasonLockTimeout, "waited too long")
	if got := ReasonOf(err); got != ReasonLockTimeout {
		t.Fatalf("ReasonOf = %q, want %q", got, ReasonLockTimeout)
	}
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("errors.Is(err, ErrLockTimeout) = false; err = %v", err)
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As(*StoreError) = false; err = %v", err)
	}
	if !strings.Contains(err.Error(), "waited too long") {
		t.Fatalf("Error() = %q, want the underlying message", err.Error())
	}
	if ReasonOf(errors.New("plain")) != "" {
		t.Fatal("ReasonOf(plain error) should be empty")
	}
}

func newTestLock(t *testing.T) *StoreLock {
	t.Helper()
	m := newTestManager(t)
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	lock, err := m.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	return lock
}

func TestBootstrapCreatesRootAndPermanentLockFile(t *testing.T) {
	m := newTestManager(t)
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	info, err := os.Lstat(filepath.Join(m.Root(), LockFileName))
	if err != nil {
		t.Fatalf("lock file missing after Bootstrap: %v", err)
	}
	if info.IsDir() {
		t.Fatal("lock file is a directory")
	}
	// A second Bootstrap is idempotent.
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}
}

// Acquiring ownership must create the lock file, and that file must never be
// removed by release: generation cleanup can never take the lock with it.
func TestAcquireThenReleaseKeepsLockFile(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !m.Owned() {
		t.Fatal("Owned() = false while holding the lock")
	}
	lockPath := filepath.Join(m.Root(), LockFileName)
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file missing while held: %v", err)
	}
	if err := m.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if m.Owned() {
		t.Fatal("Owned() = true after Release")
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file was removed by Release: %v", err)
	}
	// Release is idempotent.
	if err := m.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

// A cancelled wait must return ReasonLockTimeout instead of blocking, and the
// failure must be distinguishable without string matching.
func TestAcquireRespectsCancellation(t *testing.T) {
	lock := newTestLock(t)
	// Hold the first lock for the whole test. lock.Path() is the lock file
	// inside the root, so its parent is the root the second manager must share.
	m2 := newTestManagerAt(t, filepath.Dir(lock.Path()))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := m2.Acquire(ctx)
	if err == nil {
		t.Fatal("second Acquire succeeded while the store was owned")
	}
	if got := ReasonOf(err); got != ReasonLockTimeout {
		t.Fatalf("ReasonOf = %q, want %q (err = %v)", got, ReasonLockTimeout, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Acquire took %s; it should give up at the deadline", elapsed)
	}
}

// newTestManagerAt builds a manager rooted at an existing snapshots directory,
// which is how two managers are made to contend for the same store.
func newTestManagerAt(t *testing.T, snapshotsRoot string) *Manager {
	t.Helper()
	m, err := NewManager(filepath.Dir(snapshotsRoot))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if m.Root() != snapshotsRoot {
		t.Fatalf("manager root = %q, want %q", m.Root(), snapshotsRoot)
	}
	return m
}

// Usage must measure the ENTIRE root, not one workspace: every workspace,
// unknown entries, the permanent lock file, and the root's own metadata.
func TestUsageAccountsWholeRoot(t *testing.T) {
	m := newTestManager(t)
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	root := m.Root()

	writeFile(t, filepath.Join(root, "aaaaaaaaaaaa", "objects", "pack", "blob"), 8192)
	writeFile(t, filepath.Join(root, "bbbbbbbbbbbb", "info", "exclude"), 10)
	// A stray file directly under the root is an "unknown" entry: counted
	// globally, never attributed to a workspace, never auto-deleted here.
	writeFile(t, filepath.Join(root, "stray.tmp"), 4096)

	usage, err := m.Usage()
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(usage.Workspaces) != 2 {
		t.Fatalf("Workspaces = %d, want 2", len(usage.Workspaces))
	}
	if _, ok := usage.Workspaces["aaaaaaaaaaaa"]; !ok {
		t.Fatal("workspace aaaaaaaaaaaa missing from accounting")
	}
	if len(usage.Unknown) != 1 {
		t.Fatalf("Unknown entries = %d, want 1", len(usage.Unknown))
	}
	if usage.Lock == nil {
		t.Fatal("the permanent lock file must be accounted")
	}
	if usage.RootMetadata <= 0 {
		t.Fatalf("RootMetadata = %d, want the root directory's own metadata", usage.RootMetadata)
	}

	// The global total must cover everything measured.
	var sum int64 = usage.RootMetadata
	for _, u := range usage.Workspaces {
		sum += u.Allocated
	}
	for _, u := range usage.Unknown {
		sum += u.Allocated
	}
	sum += usage.Lock.Allocated
	if usage.GlobalAllocated != sum {
		t.Fatalf("GlobalAllocated = %d, want %d (sum of every measured entry)", usage.GlobalAllocated, sum)
	}
	if usage.GlobalAllocated <= 8192+4096 {
		t.Fatalf("GlobalAllocated = %d, want it to include the written files", usage.GlobalAllocated)
	}
	if usage.FilesAllocated >= usage.GlobalAllocated {
		t.Fatalf("FilesAllocated (%d) must exclude directory metadata (%d total)",
			usage.FilesAllocated, usage.GlobalAllocated)
	}
}

// Every measured file must count at least its rounded allocation, and
// directories must be counted too, so metadata cannot evade a tiny limit.
func TestUsageCountsAllocationAndDirectories(t *testing.T) {
	m := newTestManager(t)
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	// One nested directory so both the workspace directory and a subdirectory
	// inside it must be counted.
	writeFile(t, filepath.Join(m.Root(), "cccccccccccc", "objects", "one-byte"), 1)

	usage, err := m.Usage()
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	ws := usage.Workspaces["cccccccccccc"]
	if ws == nil {
		t.Fatal("workspace cccccccccccc missing")
	}
	if ws.Dirs != 2 {
		t.Fatalf("Dirs = %d, want the workspace directory and its nested objects directory", ws.Dirs)
	}
	if ws.Files != 1 {
		t.Fatalf("Files = %d, want 1", ws.Files)
	}
	unit := m.Limits().AllocationUnitBytes
	if ws.Allocated < unit {
		t.Fatalf("Allocated = %d, want at least one allocation unit (%d) for a 1-byte file", ws.Allocated, unit)
	}
	if ws.DirBytes <= 0 {
		t.Fatal("DirBytes = 0, want directory metadata counted separately")
	}
	if got := ws.DirsAllocated(); got != ws.DirBytes {
		t.Fatalf("DirsAllocated() = %d, want %d (not reclaimable by deleting generation contents)", got, ws.DirBytes)
	}
	if ws.Logical < unit {
		t.Fatalf("Logical = %d, want it rounded up to at least one allocation unit", ws.Logical)
	}
}

// A symlink inside the store must be measured as a link and never followed, so
// accounting cannot be inflated by, or traverse to, data outside the root.
func TestUsageDoesNotFollowSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	outside := t.TempDir()
	big := filepath.Join(outside, "big.bin")
	writeFile(t, big, 1<<20)

	m := newTestManager(t)
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	wsDir := filepath.Join(m.Root(), "dddddddddddd")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(wsDir, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	usage, err := m.Usage()
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	ws := usage.Workspaces["dddddddddddd"]
	if ws == nil {
		t.Fatal("workspace dddddddddddd missing")
	}
	if ws.Allocated >= 1<<20 {
		t.Fatalf("Allocated = %d, want only the link's own size, not the %d-byte target", ws.Allocated, 1<<20)
	}
	if ws.Files != 1 {
		t.Fatalf("Files = %d, want the symlink counted as one entry", ws.Files)
	}
}

// An unknown accounting failure must be an error, never a zero contribution.
// Counting it as zero would under-report usage and admit writes the budget
// never allowed.
func TestUsageFailsClosedOnAccountingError(t *testing.T) {
	m := newTestManager(t, WithAllocatedSizeQuery(func(string, os.FileInfo) (int64, error) {
		return 0, errors.New("simulated stat failure")
	}))
	if err := os.MkdirAll(m.Root(), 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	_, err := m.Usage()
	if err == nil {
		t.Fatal("Usage succeeded despite an accounting failure")
	}
	if got := ReasonOf(err); got != ReasonUnreadableFile {
		t.Fatalf("ReasonOf = %q, want %q (err = %v)", got, ReasonUnreadableFile, err)
	}
	if !errors.Is(err, ErrUnreadableFile) {
		t.Fatalf("errors.Is(err, ErrUnreadableFile) = false; err = %v", err)
	}
}

// A total that would overflow int64 is an error, never a silent wrap.
func TestUsageOverflowIsAnError(t *testing.T) {
	// A stat seam that reports an impossible size for every entry.
	m := newTestManager(t, WithAllocatedSizeQuery(func(string, os.FileInfo) (int64, error) {
		return math.MaxInt64, nil
	}))
	writeFile(t, filepath.Join(m.Root(), "eeeeeeeeeeee", "a"), 8)
	writeFile(t, filepath.Join(m.Root(), "eeeeeeeeeeee", "b"), 8)
	_, err := m.Usage()
	if !errors.Is(err, ErrAccountingOverflow) {
		t.Fatalf("Usage error = %v, want ErrAccountingOverflow", err)
	}
}

// The free-space query must fail closed: an unavailable value is an error, not
// "plenty of space".
func TestFreeSpaceFailsClosedOnError(t *testing.T) {
	m := newTestManager(t, WithFreeSpaceQuery(func(string) (int64, error) {
		return 0, errors.New("simulated statfs failure")
	}))
	if _, err := m.FreeSpace(); err == nil {
		t.Fatal("FreeSpace succeeded despite a query failure")
	}
	if err := m.CheckFreeSpace(1024); !errors.Is(err, ErrUnreadableFile) {
		t.Fatalf("CheckFreeSpace error = %v, want ErrUnreadableFile", err)
	}
}

// The free-space margin must be injectable with small numbers and must reject a
// write that does not fit allowance + reserve.
func TestCheckFreeSpaceUsesInjectableMargin(t *testing.T) {
	limits := Limits{
		WorkspaceMaxBytes:      1 << 20,
		GlobalMaxBytes:         1 << 20,
		FreeSpaceMarginBytes:   1000,
		FreeSpaceMarginPercent: 2,
		AllocationUnitBytes:    4096,
	}
	free := int64(0)
	m := newTestManager(t,
		WithLimits(limits),
		WithFreeSpaceQuery(func(string) (int64, error) { return free, nil }),
	)
	if err := os.MkdirAll(m.Root(), 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	// allowance 1000 + reserve max(1000, 20) = 2000.
	free = 1999
	if err := m.CheckFreeSpace(1000); !errors.Is(err, ErrInsufficientFreeSpace) {
		t.Fatalf("CheckFreeSpace error = %v, want ErrInsufficientFreeSpace", err)
	}
	free = 2000
	if err := m.CheckFreeSpace(1000); err != nil {
		t.Fatalf("CheckFreeSpace with exactly enough free space: %v", err)
	}
	// The proportional part wins for a large allowance: 100000 + 2% = 102000.
	free = 101999
	if err := m.CheckFreeSpace(100_000); !errors.Is(err, ErrInsufficientFreeSpace) {
		t.Fatalf("error = %v, want the 2%% reserve to apply", err)
	}
	free = 102000
	if err := m.CheckFreeSpace(100_000); err != nil {
		t.Fatalf("CheckFreeSpace at exactly 2%% margin: %v", err)
	}
}

// Bootstrapping metadata must itself be admitted: a budget too small to hold
// the root directory and the lock file must fail rather than create them.
func TestBootstrapRefusesWhenMetadataBaselineExceedsBudget(t *testing.T) {
	m := newTestManager(t, WithLimits(Limits{
		WorkspaceMaxBytes:      1,
		GlobalMaxBytes:         1,
		FreeSpaceMarginBytes:   1,
		FreeSpaceMarginPercent: 0,
		AllocationUnitBytes:    4096,
	}))
	err := m.Bootstrap(context.Background())
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("Bootstrap error = %v, want ErrBudgetExhausted", err)
	}
	if _, statErr := os.Lstat(m.Root()); statErr == nil {
		t.Fatal("Bootstrap created the root despite an unadmittable metadata baseline")
	}
}

// A workspace identifier must never escape the store root.
func TestWorkspaceStorePathRejectsEscape(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`} {
		if _, err := WorkspaceStorePath("/root", bad); err == nil {
			t.Errorf("WorkspaceStorePath(%q) succeeded, want an error", bad)
		}
	}
	got, err := WorkspaceStorePath("/root", "abc123")
	if err != nil {
		t.Fatalf("WorkspaceStorePath: %v", err)
	}
	if got != filepath.Join("/root", "abc123") {
		t.Fatalf("WorkspaceStorePath = %q", got)
	}
}

// The manager must agree with the existing shadow-repo layout on which
// directory belongs to which workspace.
func TestWorkspaceHashForMatchesProjectHash(t *testing.T) {
	root := t.TempDir()
	if got, want := WorkspaceHashFor(root), projectHash(root); got != want {
		t.Fatalf("WorkspaceHashFor = %q, want %q", got, want)
	}
}

// A Service and a Manager built from the same data dir and workspace root must
// agree on which directory belongs to which workspace, so reclamation can never
// target the wrong store.
func TestServiceStoreDirMatchesManagerCatalog(t *testing.T) {
	dataDir := t.TempDir()
	project := t.TempDir()
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	cat, err := m.CatalogForRoot(project)
	if err != nil {
		t.Fatalf("CatalogForRoot: %v", err)
	}
	svc := New(dataDir, project, 1<<20, nil, testLogger())
	if got := svc.StoreDir(); got != cat.Root() {
		t.Fatalf("Service.StoreDir() = %q, want the manager's catalog root %q", got, cat.Root())
	}
	if got := svc.Workspace(); got != WorkspaceHashFor(project) {
		t.Fatalf("Service.Workspace() = %q, want %q", got, WorkspaceHashFor(project))
	}
}

// Managed git invocations must be sanitized: a project must not be able to run
// arbitrary code through a shadow-repo hook, auto-GC must be pinned off, and a
// hostile inherited Git environment must not redirect the command.
//
// Each assertion here is a real regression guard. Verified by hand before the
// test was written: WITHOUT the core.hooksPath pin the planted hook runs, and
// with it the hook does not.
func TestManagedGitInvocationIsSanitized(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	shadow := filepath.Join(dir, "shadow.git")
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	// Text content, not NUL bytes: git renders an all-zero file as binary and
	// the diff assertion below needs real diff text.
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("initial-content\n"), 0o644); err != nil {
		t.Fatalf("write work file: %v", err)
	}

	// A hostile inherited environment. None of these may reach the child:
	// each one, if honored, would point git at the wrong repository, index, or
	// config file.
	t.Setenv("GIT_DIR", filepath.Join(dir, "bogus-git-dir"))
	t.Setenv("GIT_WORK_TREE", filepath.Join(dir, "bogus-work-tree"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(dir, "bogus-index"))
	t.Setenv("GIT_EXTERNAL_DIFF", "false")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "bogus-global"))

	ctx := context.Background()
	init := newGitCmd(ctx, "git", shadow, "", false, "init", "--bare")
	if _, err := runGitCombined(ctx, init, defaultMaxCommandOutput); err != nil {
		t.Fatalf("init shadow repo: %v", err)
	}

	// Plant a hook that would run on commit. It must never execute.
	hookRan := filepath.Join(dir, "HOOK-RAN")
	hook := "#!/bin/sh\ntouch " + hookRan + "\n"
	if err := os.WriteFile(filepath.Join(shadow, "hooks", "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatalf("plant hook: %v", err)
	}

	run := func(args ...string) (string, error) {
		cmd := newGitCmd(ctx, "git", shadow, work, false, args...)
		out, err := runGitCombined(ctx, cmd, defaultMaxCommandOutput)
		return strings.TrimSpace(string(out)), err
	}

	// A commit needs an identity. The sanitized environment deliberately points
	// git away from any user config, so a repository used for a commit must
	// carry its own.
	if _, err := run("config", "user.name", "marshal"); err != nil {
		t.Fatalf("config user.name: %v", err)
	}
	if _, err := run("config", "user.email", "marshal@local"); err != nil {
		t.Fatalf("config user.email: %v", err)
	}

	if _, err := run("add", "-A", "--", "."); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := run("commit", "-m", "snapshot", "--allow-empty"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := os.Stat(hookRan); err == nil {
		t.Fatal("a shadow-repo hook executed; a project must not run arbitrary code through a snapshot")
	}

	// gc.auto must be pinned to 0, which is what stops background repacks from
	// leaving abandoned tmp_pack_* files behind.
	if got, err := run("config", "--get", "gc.auto"); err != nil || got != "0" {
		t.Fatalf("gc.auto = %q, %v; want it pinned to \"0\"", got, err)
	}
	// The commit landed in the intended shadow repo despite GIT_DIR pointing
	// at a bogus path.
	head, err := run("rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if len(head) != 40 {
		t.Fatalf("HEAD = %q, want a full object hash from the intended shadow repo", head)
	}

	// GIT_EXTERNAL_DIFF=false must not be honored: a diff must still produce
	// real text rather than being handed to the named program.
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("changed-content\n"), 0o644); err != nil {
		t.Fatalf("rewrite file: %v", err)
	}
	cmd := newGitCmd(ctx, "git", shadow, work, false, "diff", "--no-ext-diff", head)
	diffOut, err := runGitCombined(ctx, cmd, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !strings.Contains(string(diffOut), "+changed-content") {
		t.Fatalf("diff output = %q, want git's own diff text; an external diff program may have run", diffOut)
	}
}

// A command that outlives its deadline must be killed and reported, not left
// running while the caller moves on.
func TestRunManagedHonoursCommandDeadline(t *testing.T) {
	m := newTestManager(t, WithCommandDeadline(200*time.Millisecond))
	cmd := exec.Command(os.Args[0], "-test.run=TestSnapshotHelperProcess")
	cmd.Env = append(os.Environ(),
		envHelperMode+"="+helperModeHeartbeat,
		envHelperHeartbeat+"="+filepath.Join(t.TempDir(), "hb"),
	)
	start := time.Now()
	_, err := m.RunManaged(context.Background(), cmd)
	if err == nil {
		t.Fatal("RunManaged returned success for a command that outlived its deadline")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("RunManaged took %s; the command deadline should have ended it", elapsed)
	}
}
