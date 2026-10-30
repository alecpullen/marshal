package snapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// snapshotsDirName is the directory, directly under the user data dir, that
// holds every workspace's snapshot store. The manager is rooted there rather
// than at one workspace, because accounting and reclamation are global.
const snapshotsDirName = "snapshots"

// Manager owns the snapshot storage root. It provides exclusive cross-process
// ownership, conservative whole-root accounting, free-space queries, and
// centralized sanitized subprocess execution.
//
// Nothing in this task wires the manager into Service.Track: it is built and
// tested independently so existing capture behaviour is unchanged. Later tasks
// (admission, generations, retention, recovery) build on it.
type Manager struct {
	dataDir string
	root    string
	limits  Limits
	logger  *slog.Logger

	// Dependency seams. Production defaults are set by NewManager; tests
	// replace individual seams to inject failures and small numbers.
	now             func() time.Time
	lstat           func(string) (os.FileInfo, error)
	allocatedSize   func(string, os.FileInfo) (int64, error)
	freeSpace       func(string) (int64, error)
	globalLimits    func() (Limits, error)
	hooks           Hooks
	gitBinary       string
	maxOutputBytes  int
	commandDeadline time.Duration
	// maintenanceBytes is the per-workspace maintenance allowance reserved on
	// top of a capture's own write allowance. It is a field rather than the
	// constant so a test can shrink it and run under a small budget without
	// having to make the budget large enough to hold the production value.
	maintenanceBytes int64
	// manifestMaxBytes bounds one serialized manifest. It is a field for the
	// same reason as maintenanceBytes: the production bound is 2 MiB, so
	// reserving the simultaneous old and new manifests costs 4 MiB before a
	// single object is written, and a test that wants to exercise a small
	// budget must be able to shrink it. Production always uses
	// ManifestMaxBytes.
	manifestMaxBytes int64
	// liveWriterWindow is how recently a recognized disposable artifact must
	// have been modified for its modification time to count as evidence that a
	// legacy writer is active. Legacy recovery refuses on that evidence.
	//
	// It is a field rather than a constant so a test can exercise the
	// heuristic with a real window and can turn it off (a non-positive value)
	// where it is irrelevant, instead of every fixture having to backdate its
	// files. Production uses DefaultLiveWriterWindow.
	liveWriterWindow time.Duration

	// legacyUsage caches the measured size of each legacy (pre-v2) store for
	// the usage pass admission runs before every capture. This binary never
	// writes a legacy store, and measuring them meant walking every file of
	// every one on each tool call. See measureLegacyStore for the bounds on
	// how stale an entry may be.
	legacyMu    sync.Mutex
	legacyUsage map[string]legacyUsageEntry

	// writeObserver, when installed, is called after every managed write
	// boundary (an object installed, a ref published, a manifest replaced, a
	// staging directory created) with the store's measured usage and the
	// reservation the write was admitted under.
	//
	// It exists so a test can assert the PEAK, not the final total. A store
	// whose final size is under budget has proved nothing: the defect this work
	// removes is a store that momentarily doubled while an atomic replace held
	// two manifests, or that overran its allowance before settling.
	//
	// Production leaves it nil, and a nil observer costs one comparison.
	writeObserver func(WriteEvent)

	// newTree is the process-tree constructor. Production leaves it nil and uses
	// newProcessTree. A test sets it on its own manager instance to model a tree
	// whose termination cannot be confirmed, or to hold a start in flight,
	// without signalling an unrelated process and without a package-global hook.
	newTree func(*exec.Cmd) (*processTree, error)

	// releaseMu serialises Release so a second caller cannot observe the
	// temporary nil lock while the first is still draining and report success
	// for a store that is in fact still owned.
	releaseMu sync.Mutex

	mu    sync.Mutex
	lock  *StoreLock
	trees map[*ownedTree]struct{}
}

// Hooks are fault-injection points used by the ownership, generation, and
// reconciliation tests. Production code leaves them nil, so they cost nothing.
//
// Each generation and reconciliation hook name states the crash boundary it
// sits on: what is durable when the hook runs, and therefore what a restart
// must find. A hook returns an error to simulate the interruption reaching the
// caller; a test that wants a hard crash calls os.Exit instead.
type Hooks struct {
	// AfterAcquire runs while the store lock is held, before Acquire returns.
	// A hook that exits the process simulates a crash while owning the store.
	AfterAcquire func() error
	// AfterRelease runs after the store lock has been released.
	AfterRelease func() error

	// AfterGenerationRecord runs once a `creating` generation record is
	// durable but before its repository exists.
	AfterGenerationRecord func() error
	// AfterInitGenerationRepo runs once the bare repository metadata is
	// written, while the generation is still `creating`.
	AfterInitGenerationRepo func() error
	// AfterReserveStaging runs once the in-flight capture record is durable
	// but before the staging directory exists.
	AfterReserveStaging func() error
	// AfterPublishRef runs once a snapshot ref is durable but before any
	// manifest update records it. A published ref must always win over the
	// manifest during reconciliation.
	AfterPublishRef func() error
	// BeforePublishRef runs once every object of a capture is durable but
	// before the publication intent is recorded and the ref is written. It is
	// the "objects staged, nothing published" boundary: an interruption here
	// must leave a snapshot no ref retains, plus an in-flight capture record
	// for reconciliation to clear.
	BeforePublishRef func() error
	// AfterMarkDeleting runs once the `deleting` state is durable but before
	// the generation directory is removed.
	AfterMarkDeleting func() error
	// BeforeRemoveGenerationDir runs immediately before a generation
	// directory is removed, with the `deleting` state already durable.
	BeforeRemoveGenerationDir func(id string) error
	// AfterRemoveGenerationDir runs once a generation directory is gone but
	// before its manifest record is dropped.
	AfterRemoveGenerationDir func() error

	// BeforeManifestRename runs with the new manifest bytes durable in a
	// scratch file and the previous manifest still in place.
	BeforeManifestRename func(path string) error
	// AfterManifestWrite runs once the new manifest is durable at its final
	// path.
	AfterManifestWrite func(path string) error

	// Recovery are the legacy-recovery crash boundaries. They are grouped in
	// their own struct so a destructive-recovery test reads as a sequence of
	// named steps rather than as eleven loose fields, and so a production
	// manager that installs none of them pays nothing.
	Recovery RecoveryHooks
}

// RecoveryHooks are the fault-injection points on the legacy recovery
// operations. Each name states the boundary it sits on, and every one of them is
// placed so that an interruption there must remain RECOVERABLE.
//
// The property each boundary is chosen to prove is the same one: nothing is
// deleted, and no published ref is lost, whatever happens at this point.
type RecoveryHooks struct {
	// AfterLegacyGenerationRecord runs once the destination generation's
	// protected, legacy-origin record is durable but before its repository
	// exists.
	AfterLegacyGenerationRecord func() error
	// AfterLegacyObjectsCopied runs once the whole closure is copied but before
	// any snapshot ref is published into the destination.
	AfterLegacyObjectsCopied func() error
	// AfterLegacyRefsPublished runs once every preserved hash has a ref but
	// before the destination generation is sealed into a usable state.
	AfterLegacyRefsPublished func() error
	// AfterLegacyGenerationSealed runs once the generation is sealed — the
	// lookup swap — but before the original duplicate is renamed aside.
	AfterLegacyGenerationSealed func() error
	// BeforeLegacyOriginalRenamed runs immediately before the original store is
	// renamed into the accounted deleting state.
	BeforeLegacyOriginalRenamed func() error
	// AfterLegacyOriginalRenamed runs once the rename is durable but before the
	// remnant is removed.
	AfterLegacyOriginalRenamed func() error
	// BeforeLegacyResetRename runs immediately before a reset renames the
	// selected store into the accounted deleting state.
	BeforeLegacyResetRename func() error
	// BeforeLegacyResetRemove runs once the reset's rename is durable but before
	// the remnant is removed.
	BeforeLegacyResetRemove func() error
	// AfterProtectionReleased runs once a protected generation's protection is
	// durably lifted but before it is marked for deletion.
	AfterProtectionReleased func() error
}

// runHook invokes a hook when it is installed. A nil hook is the production
// case and is not an error.
func (m *Manager) runHook(fn func() error) error {
	if fn == nil {
		return nil
	}
	return fn()
}

// runHookPath invokes a path-carrying hook when it is installed.
func (m *Manager) runHookPath(fn func(string) error, path string) error {
	if fn == nil {
		return nil
	}
	return fn(path)
}

// ManagerOption configures a Manager. Every option is optional; the defaults are
// production-safe.
type ManagerOption func(*Manager)

// WithLimits sets the effective storage ceilings. Unset (zero) fields fall back
// to the production defaults when they are used.
func WithLimits(l Limits) ManagerOption { return func(m *Manager) { m.limits = l } }

// WithLogger sets the logger used for maintenance diagnostics.
func WithLogger(l *slog.Logger) ManagerOption {
	return func(m *Manager) {
		if l != nil {
			m.logger = l
		}
	}
}

// WithClock injects the clock. It exists so generation timestamps (later tasks)
// and margin tests are deterministic.
func WithClock(now func() time.Time) ManagerOption {
	return func(m *Manager) {
		if now != nil {
			m.now = now
		}
	}
}

// WithStatQuery injects the per-entry stat call.
func WithStatQuery(lstat func(string) (os.FileInfo, error)) ManagerOption {
	return func(m *Manager) {
		if lstat != nil {
			m.lstat = lstat
		}
	}
}

// WithAllocatedSizeQuery injects the allocated-size query, which is how the
// accounting tests force an unknown accounting error.
func WithAllocatedSizeQuery(fn func(string, os.FileInfo) (int64, error)) ManagerOption {
	return func(m *Manager) {
		if fn != nil {
			m.allocatedSize = fn
		}
	}
}

// WithFreeSpaceQuery injects the available-space query, which is how the
// free-space tests use small numbers.
func WithFreeSpaceQuery(fn func(string) (int64, error)) ManagerOption {
	return func(m *Manager) {
		if fn != nil {
			m.freeSpace = fn
		}
	}
}

// WithGlobalLimitReader injects the user-global limit reader. All processes read
// the current user-global ceiling while holding the lock rather than trusting a
// stale session value, so this is consulted on every use, not cached.
func WithGlobalLimitReader(fn func() (Limits, error)) ManagerOption {
	return func(m *Manager) {
		if fn != nil {
			m.globalLimits = fn
		}
	}
}

// WithHooks installs fault-injection hooks.
func WithHooks(h Hooks) ManagerOption { return func(m *Manager) { m.hooks = h } }

// WithGitBinary overrides the git executable used by managed invocations.
func WithGitBinary(path string) ManagerOption {
	return func(m *Manager) {
		if path != "" {
			m.gitBinary = path
		}
	}
}

// WithMaxCommandOutput bounds how much managed subprocess output is retained.
func WithMaxCommandOutput(n int) ManagerOption {
	return func(m *Manager) {
		if n > 0 {
			m.maxOutputBytes = n
		}
	}
}

// WithCommandDeadline bounds how long a managed git invocation may run when the
// caller's context has no earlier deadline.
func WithCommandDeadline(d time.Duration) ManagerOption {
	return func(m *Manager) {
		if d > 0 {
			m.commandDeadline = d
		}
	}
}

// WithWriteObserver installs a write-boundary observer. Tests use it to assert
// peak usage rather than final usage.
func WithWriteObserver(fn func(WriteEvent)) ManagerOption {
	return func(m *Manager) {
		if fn != nil {
			m.writeObserver = fn
		}
	}
}

// WithMaintenanceAllowance overrides the per-workspace maintenance allowance
// that is reserved on top of every capture's own write allowance.
func WithMaintenanceAllowance(n int64) ManagerOption {
	return func(m *Manager) {
		if n > 0 {
			m.maintenanceBytes = n
		}
	}
}

// WithManifestBound overrides the bound on one serialized manifest. It exists
// so a test can run under a budget small enough to exercise rotation and
// refusal without a multi-megabyte manifest reservation dominating every
// decision. Production leaves it at ManifestMaxBytes.
func WithManifestBound(n int64) ManagerOption {
	return func(m *Manager) {
		if n > 0 {
			m.manifestMaxBytes = n
		}
	}
}

// manifestBound is the effective bound on one serialized manifest.
func (m *Manager) manifestBound() int64 {
	if m.manifestMaxBytes <= 0 {
		return ManifestMaxBytes
	}
	return m.manifestMaxBytes
}

// WithLiveWriterWindow overrides how recently a recognized disposable artifact
// must have been modified for that to count as live-writer evidence. A
// non-positive value disables the heuristic; the lock-file check is unaffected,
// because a Git lock file is unambiguous evidence in its own right.
func WithLiveWriterWindow(d time.Duration) ManagerOption {
	return func(m *Manager) { m.liveWriterWindow = d }
}

// liveWriterWindowEffective is the window in force for this manager.
func (m *Manager) liveWriterWindowEffective() time.Duration {
	if m == nil {
		return 0
	}
	return m.liveWriterWindow
}

// WriteEvent describes one managed write boundary.
type WriteEvent struct {
	// Kind names the boundary: object, tree, commit, ref, manifest, staging,
	// generation, or reclaim.
	Kind string
	// Path is the store path the write concerns.
	Path string
	// Allowance is the reservation the write was admitted under, or 0 when the
	// write is a maintenance one.
	Allowance int64
	// Reserved is the full amount the store committed for the operation the
	// write belongs to, or 0 for a maintenance write.
	Reserved int64
	// Workspace is the workspace the write belongs to, when known.
	Workspace string
}

// observeWrite reports one write boundary to the installed observer.
func (m *Manager) observeWrite(ev WriteEvent) {
	if m == nil || m.writeObserver == nil {
		return
	}
	m.writeObserver(ev)
}

// NewManager builds a Manager rooted at <dataDir>/snapshots.
func NewManager(dataDir string, opts ...ManagerOption) (*Manager, error) {
	if dataDir == "" {
		return nil, storeErrorf(ReasonInternal, "data directory must not be empty")
	}
	m := &Manager{
		dataDir:          dataDir,
		root:             filepath.Join(dataDir, snapshotsDirName),
		limits:           DefaultLimits(),
		logger:           slog.Default(),
		now:              time.Now,
		lstat:            os.Lstat,
		allocatedSize:    allocatedSize,
		freeSpace:        freeSpaceBytes,
		gitBinary:        "git",
		maxOutputBytes:   defaultMaxCommandOutput,
		commandDeadline:  30 * time.Second,
		maintenanceBytes: DefaultMaintenanceAllowanceBytes,
		manifestMaxBytes: ManifestMaxBytes,
		liveWriterWindow: DefaultLiveWriterWindow,
		trees:            make(map[*ownedTree]struct{}),
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.allocatedSize == nil {
		// On a platform with no allocation query the manager must not pretend
		// to account conservatively: fail closed on first use.
		m.allocatedSize = func(string, os.FileInfo) (int64, error) {
			return 0, storeErrorf(ReasonUnsupportedPlatform, "filesystem allocation accounting")
		}
	}
	if err := m.limits.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Root is the snapshots storage root, <dataDir>/snapshots.
func (m *Manager) Root() string { return m.root }

// DataDir is the Marshal user data directory the manager was built for.
func (m *Manager) DataDir() string { return m.dataDir }

// Limits returns the configured limits after applying production defaults.
func (m *Manager) Limits() Limits { return m.limits.Normalize() }

// Now returns the injected clock's current time.
func (m *Manager) Now() time.Time { return m.now() }

// EffectiveLimits returns the limits that must actually be enforced right now.
// When a user-global limit reader is installed it is consulted on every call,
// so a limit lowered by another process takes effect without restarting this
// one, and only the user-global ceiling can tighten or loosen it.
func (m *Manager) EffectiveLimits() (Limits, error) {
	if err := m.checkReady(); err != nil {
		return Limits{}, err
	}
	if m.globalLimits == nil {
		return m.limits.Normalize(), nil
	}
	l, err := m.globalLimits()
	if err != nil {
		return Limits{}, storeError(ReasonInvalidLimits, fmt.Errorf("read user-global snapshot limits: %w", err))
	}
	if err := l.Validate(); err != nil {
		return Limits{}, err
	}
	return l.Normalize(), nil
}

// Owned reports whether this manager currently holds store ownership.
func (m *Manager) Owned() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lock != nil
}

// Acquire takes exclusive ownership of the snapshots root and returns the
// handle. The wait respects ctx cancellation and yields a ReasonLockTimeout
// error rather than blocking.
//
// The root and the permanent lock file are created if absent; creating them is
// itself accounted for by Bootstrap, which callers should run first.
func (m *Manager) Acquire(ctx context.Context) (*StoreLock, error) {
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	alreadyHeld := m.lock != nil
	m.mu.Unlock()
	if alreadyHeld {
		return nil, storeErrorf(ReasonInternal, "manager already owns %s", m.root)
	}

	lock, err := AcquireStoreLock(ctx, m.root)
	if err != nil {
		return nil, err
	}
	// Any subprocess tree this manager started must be gone before ownership
	// is released, so a cancelled child cannot keep writing into the store
	// after the lock is handed on.
	lock.OnRelease(m.killLiveTrees)

	m.mu.Lock()
	m.lock = lock
	m.mu.Unlock()

	if m.hooks.AfterAcquire != nil {
		if err := m.hooks.AfterAcquire(); err != nil {
			_ = m.Release()
			return nil, err
		}
	}
	return lock, nil
}

// Release drops ownership. It runs the registered drains (killing and reaping
// any live subprocess tree) before the OS lock is released, and never deletes
// the permanent lock file.
//
// Release attempts serialise on releaseMu. Without that, a second caller could
// observe the temporary nil lock left by the first — which is nil for exactly as
// long as the first is draining — and report success for a store that is still
// owned.
func (m *Manager) Release() error {
	m.releaseMu.Lock()
	defer m.releaseMu.Unlock()

	m.mu.Lock()
	lock := m.lock
	m.lock = nil
	m.mu.Unlock()
	if lock == nil {
		return nil
	}
	if err := lock.Release(); err != nil {
		// Ownership was retained because a writer could not be confirmed
		// dead. Put the handle back so a later Release can retry rather than
		// silently forgetting that the store is still owned.
		m.mu.Lock()
		m.lock = lock
		m.mu.Unlock()
		return err
	}
	if m.hooks.AfterRelease != nil {
		return m.hooks.AfterRelease()
	}
	return nil
}

// killLiveTrees is the release drain: it kills and then waits for every
// subprocess tree this manager started. waitGone is what makes the guarantee,
// since kill alone only sends a signal.
//
// A tree whose disappearance cannot be proved stays registered with its platform
// handle open and the joined error is returned, which keeps ownership (and this
// drain, for retry) in place. Only trees confirmed gone are forgotten, so a retry
// re-examines the uncertain ones instead of losing the condition that blocked
// the release.
func (m *Manager) killLiveTrees() error {
	m.mu.Lock()
	trees := make([]*ownedTree, 0, len(m.trees))
	for t := range m.trees {
		trees = append(trees, t)
	}
	m.mu.Unlock()

	var errs []error
	var settled []*ownedTree
	for _, t := range trees {
		// terminate waits for an in-flight start before deciding anything: a
		// tree that has not been assigned a group id or job handle would
		// otherwise read as gone.
		if err := t.terminate(); err != nil {
			errs = append(errs, err)
			continue
		}
		settled = append(settled, t)
	}
	if len(settled) > 0 {
		m.mu.Lock()
		for _, t := range settled {
			delete(m.trees, t)
		}
		m.mu.Unlock()
	}
	return errors.Join(errs...)
}

// newManagedTree builds the process tree for cmd and registers it before the
// child is started, so a release drain that runs in between waits for this start
// to resolve instead of concluding that a tree without a group id is already
// gone.
func (m *Manager) newManagedTree(cmd *exec.Cmd) (*ownedTree, error) {
	life := newStartingTree()
	// The ownership check and the registration happen together under m.mu, so a
	// tree can never be started just as Release is sweeping.
	m.mu.Lock()
	if m.lock != nil {
		m.trees[life] = struct{}{}
		life.markOwned()
	}
	m.mu.Unlock()

	tree, err := m.processTreeFor(cmd)
	if err != nil {
		// Nothing was started, so there is no process to keep ownership for.
		life.failStart()
		m.mu.Lock()
		delete(m.trees, life)
		m.mu.Unlock()
		return nil, err
	}
	life.attach(tree)
	return life, nil
}

// processTreeFor builds the platform tree for cmd, honouring the instance-scoped
// test seam when one is installed.
func (m *Manager) processTreeFor(cmd *exec.Cmd) (*processTree, error) {
	if m.newTree != nil {
		return m.newTree(cmd)
	}
	return newProcessTree(cmd)
}

// releaseTree forgets a tree that is confirmed gone and keeps one whose fate is
// unknown, so the next release drains it again rather than racing a writer that
// may still be alive.
func (m *Manager) releaseTree(life *ownedTree) {
	if !life.finished() {
		return
	}
	m.mu.Lock()
	delete(m.trees, life)
	m.mu.Unlock()
}

// trackTreeIfOwned registers a tree whose child is already running so that
// releasing ownership drains it.
//
// It is the legacy migration reader's entry point: that reader owns its own
// start and wait sequence, so there is no start boundary left to synchronise
// with. Every other managed launch goes through newManagedTree.
func (m *Manager) trackTreeIfOwned(t *processTree) bool {
	life := newLiveTree(t)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil {
		return false
	}
	m.trees[life] = struct{}{}
	return true
}

// untrackTree forgets the lifecycle registered for t. It is the legacy reader's
// counterpart to trackTreeIfOwned; the managed launch paths call releaseTree,
// which keeps a tree whose termination is unconfirmed.
func (m *Manager) untrackTree(t *processTree) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for life := range m.trees {
		if life.platformTree() == t {
			delete(m.trees, life)
			return
		}
	}
}

// Bootstrap creates the snapshots root and the permanent lock file if they do
// not exist, and refuses to create them when the metadata they consume cannot
// be admitted under the global ceiling or the free-space reserve.
//
// This is what stops metadata creation from evading a tiny configured limit: a
// budget of zero bytes cannot be satisfied by "an empty directory costs
// nothing", because a directory and the lock file are each charged at least one
// allocation unit.
func (m *Manager) Bootstrap(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.checkReady(); err != nil {
		return err
	}
	limits, err := m.EffectiveLimits()
	if err != nil {
		return err
	}

	// Fast path: when the root AND the permanent lock file already exist there
	// is no metadata to create, so there is nothing to admit and nothing to
	// measure. Measuring here instead would make every per-session bootstrap
	// pay for a whole-root accounting scan, which is real time on a store
	// holding hundreds of gigabytes of another workspace's history.
	//
	// The accounting below exists for the case where Bootstrap would CREATE
	// metadata: that is the case a tiny configured limit must be able to
	// refuse, and it is the only case where there is a proposed write to
	// compare against the ceiling.
	if rootInfo, rootErr := m.lstat(m.root); rootErr == nil && rootInfo.IsDir() {
		lockPath := filepath.Join(m.root, LockFileName)
		if _, lockErr := m.lstat(lockPath); lockErr == nil {
			return nil
		} else if !errors.Is(lockErr, fs.ErrNotExist) {
			return m.storeError(ReasonUnreadableFile, lockPath, lockErr)
		}
	} else if rootErr != nil && !errors.Is(rootErr, fs.ErrNotExist) {
		return m.storeError(ReasonUnreadableFile, m.root, rootErr)
	}

	// Measure what already exists so a bootstrap that is about to create the
	// missing metadata is idempotent and does not double-count.
	var existing int64
	if _, statErr := m.lstat(m.root); statErr == nil {
		usage, err := m.Usage()
		if err != nil {
			return err
		}
		existing = usage.GlobalAllocated
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return m.storeError(ReasonUnreadableFile, m.root, statErr)
	}

	// The root directory entry and the lock file each consume at least one
	// allocation unit.
	unit, err := limits.RoundUp(0)
	if err != nil {
		return err
	}
	proposed, err := addInt64(unit, unit)
	if err != nil {
		return err
	}
	projected, err := addInt64(existing, proposed)
	if err != nil {
		return err
	}
	if projected > limits.GlobalMaxBytes {
		return m.storeErrorf(ReasonBudgetExhausted, m.root,
			"metadata baseline %d plus %d bytes exceeds the global budget %d",
			existing, proposed, limits.GlobalMaxBytes)
	}
	if err := m.CheckFreeSpace(proposed); err != nil {
		return err
	}

	if err := os.MkdirAll(m.root, 0o755); err != nil {
		return m.storeErrorf(ReasonUnreadableFile, m.root, "create snapshots root: %v", err)
	}
	lockPath := filepath.Join(m.root, LockFileName)
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return m.storeErrorf(ReasonUnreadableFile, lockPath, "create lock file: %v", err)
	}
	return f.Close()
}

// CheckFreeSpace verifies that the filesystem holding the store has room for a
// proposed write allowance plus the free-space reserve. The reserve is
// max(configured floor, configured percentage of the allowance), so a large
// proposed write leaves proportionally more headroom.
//
// An unreadable free-space value is an error, never treated as "plenty".
func (m *Manager) CheckFreeSpace(allowance int64) error {
	if err := m.checkReady(); err != nil {
		return err
	}
	limits, err := m.EffectiveLimits()
	if err != nil {
		return err
	}
	reserve, err := limits.FreeSpaceReserve(allowance)
	if err != nil {
		return err
	}
	need, err := addInt64(allowance, reserve)
	if err != nil {
		return err
	}
	free, err := m.FreeSpace()
	if err != nil {
		return err
	}
	if free < need {
		return m.storeErrorf(ReasonInsufficientFreeSpace, m.root,
			"need %d bytes (allowance %d + reserve %d) but only %d are available",
			need, allowance, reserve, free)
	}
	return nil
}

// FreeSpace returns the bytes available to an unprivileged writer on the
// filesystem holding the store. When the root does not exist yet the query runs
// against the nearest existing ancestor, so admission can be evaluated before
// the first write.
func (m *Manager) FreeSpace() (int64, error) {
	if err := m.checkReady(); err != nil {
		return 0, err
	}
	target := m.root
	if _, err := m.lstat(target); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return 0, m.storeError(ReasonUnreadableFile, target, err)
		}
		target = m.dataDir
		if _, err := m.lstat(target); err != nil {
			return 0, m.storeError(ReasonUnreadableFile, target, err)
		}
	}
	free, err := m.freeSpace(target)
	if err != nil {
		return 0, m.storeError(ReasonUnreadableFile, target, err)
	}
	if free < 0 {
		return 0, m.storeErrorf(ReasonUnreadableFile, target, "negative free space %d", free)
	}
	return free, nil
}

// RunManaged runs cmd in its own process tree while bounding its retained
// output, and guarantees that no process in that tree is alive when it
// returns. Under ownership the tree is also registered so Release drains it.
func (m *Manager) RunManaged(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	if deadline := m.deadlineFor(ctx); deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, deadline)
		defer cancel()
	}
	life, err := m.newManagedTree(cmd)
	if err != nil {
		return nil, err
	}
	out, bytesFn := combinedOutput(normalizeMaxOutput(m.maxOutputBytes))
	err = runProcessTreeWith(ctx, cmd, life, out)
	// A tree that is confirmed gone, or that was never started, can be forgotten.
	// One whose termination is unknown stays registered for the next drain.
	m.releaseTree(life)
	return bytesFn(), err
}

// gitRun runs a sanitized, read-only git command against gitDir and returns its
// stdout, stderr, and whether either stream was truncated at the output bound.
//
// Read-only is not a comment: GIT_OPTIONAL_LOCKS=0 also stops git from taking
// an index lock, and the pinned configuration disables auto-GC, maintenance,
// and hooks. Discovery and reconciliation must never write into a store they
// are only inspecting, including a legacy store this process does not own.
//
// The truncation flag exists because a truncated object listing would silently
// under-report abandoned bytes, and an under-reported total is how a budget
// admits a write it cannot afford.
func (m *Manager) gitRun(ctx context.Context, gitDir string, stdin []byte, args ...string) (stdout, stderr []byte, truncated bool, err error) {
	if err := m.checkReady(); err != nil {
		return nil, nil, false, err
	}
	if gitDir == "" {
		return nil, nil, false, storeErrorf(ReasonInternal, "git directory must not be empty")
	}
	if deadline := m.deadlineFor(ctx); deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, deadline)
		defer cancel()
	}

	cmd := newGitCmd(ctx, m.gitBinary, gitDir, "", true, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	life, err := m.newManagedTree(cmd)
	if err != nil {
		return nil, nil, false, err
	}
	max := normalizeMaxOutput(m.maxOutputBytes)
	outBuf := &limitedBuffer{max: max}
	errBuf := &limitedBuffer{max: max}
	err = runProcessTreeWith(ctx, cmd, life, treeOutput{stdout: outBuf, stderr: errBuf})
	m.releaseTree(life)
	return outBuf.Bytes(), errBuf.Bytes(), outBuf.Truncated() || errBuf.Truncated(), err
}

// gitRunEnv is gitRun with an io.Reader on stdin, an explicit work tree, and
// additional environment entries.
//
// The work tree is explicit rather than implied because the store's own
// repository has none: a listing that needs to enumerate workspace paths must
// name the workspace, and leaving it to be discovered would make eligibility
// depend on the process's current directory.
//
// The extra environment is how a caller pins a variable the sanitized
// environment deliberately clears — GIT_INDEX_FILE is cleared by default, and
// the only legitimate use of it here is to point a read-only listing at a
// scratch index that lives OUTSIDE the store. A caller that passes anything
// else is responsible for its own read-only promise: nothing in this method
// makes a writing command safe.
func (m *Manager) gitRunEnv(ctx context.Context, gitDir, workTree string, stdin io.Reader, extraEnv []string, maxOutput int, args ...string) (stdout, stderr []byte, truncated bool, err error) {
	if err := m.checkReady(); err != nil {
		return nil, nil, false, err
	}
	if gitDir == "" {
		return nil, nil, false, storeErrorf(ReasonInternal, "git directory must not be empty")
	}
	if deadline := m.deadlineFor(ctx); deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, deadline)
		defer cancel()
	}

	cmd := newGitCmd(ctx, m.gitBinary, gitDir, workTree, true, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	if len(extraEnv) > 0 {
		// Appended AFTER the sanitized environment so an explicit pin wins over
		// the cleared default.
		cmd.Env = append(cmd.Env, extraEnv...)
	}
	life, err := m.newManagedTree(cmd)
	if err != nil {
		return nil, nil, false, err
	}
	max := normalizeMaxOutput(maxOutput)
	outBuf := &limitedBuffer{max: max}
	errBuf := &limitedBuffer{max: max}
	err = runProcessTreeWith(ctx, cmd, life, treeOutput{stdout: outBuf, stderr: errBuf})
	m.releaseTree(life)
	return outBuf.Bytes(), errBuf.Bytes(), outBuf.Truncated() || errBuf.Truncated(), err
}

// gitRunEnvWriter is gitRunEnv for a command whose stdout is STREAMED into a
// caller-provided writer instead of being buffered.
//
// Restore uses it to pipe one blob straight into its destination file, so a
// large snapshot file is never held in memory. The command is built exactly like
// every other managed invocation — sanitized environment, pinned configuration,
// bounded stderr, process-tree lifetime — so streaming changes only where the
// bytes go, not what is allowed to run.
func (m *Manager) gitRunEnvWriter(ctx context.Context, gitDir string, stdout io.Writer, args ...string) (stderr []byte, truncated bool, err error) {
	if err := m.checkReady(); err != nil {
		return nil, false, err
	}
	if gitDir == "" {
		return nil, false, storeErrorf(ReasonInternal, "git directory must not be empty")
	}
	if stdout == nil {
		return nil, false, storeErrorf(ReasonInternal, "git output writer must not be nil")
	}
	if deadline := m.deadlineFor(ctx); deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, deadline)
		defer cancel()
	}
	cmd := newGitCmd(ctx, m.gitBinary, gitDir, "", true, args...)
	life, err := m.newManagedTree(cmd)
	if err != nil {
		return nil, false, err
	}
	errBuf := &limitedBuffer{max: normalizeMaxOutput(m.maxOutputBytes)}
	err = runProcessTreeWith(ctx, cmd, life, treeOutput{stdout: stdout, stderr: errBuf})
	m.releaseTree(life)
	return errBuf.Bytes(), errBuf.Truncated(), err
}

// gitOutput is gitRun requiring success, with stderr folded into the error so a
// failure reads as git's own diagnostic rather than a bare "exit status 1".
func (m *Manager) gitOutput(ctx context.Context, gitDir string, args ...string) ([]byte, error) {
	out, errOut, _, err := m.gitRun(ctx, gitDir, nil, args...)
	if err != nil {
		return out, m.storeError(ReasonUnreadableFile, gitDir,
			fmt.Errorf("git %s: %w%s", strings.Join(args, " "), err, gitOutputSuffix(errOut)))
	}
	return out, nil
}

// gitOutputStdin is gitOutput with data on stdin.
func (m *Manager) gitOutputStdin(ctx context.Context, gitDir string, stdin []byte, args ...string) ([]byte, bool, error) {
	out, errOut, truncated, err := m.gitRun(ctx, gitDir, stdin, args...)
	if err != nil {
		return out, truncated, m.storeError(ReasonUnreadableFile, gitDir,
			fmt.Errorf("git %s: %w%s", strings.Join(args, " "), err, gitOutputSuffix(errOut)))
	}
	return out, truncated, nil
}

// gitDirExists reports whether a directory looks like a Git repository, without
// running git. It is used where spawning a process on an unrecognised directory
// would be the only thing standing between a corrupt store and a guess.
func gitDirExists(dir string) bool { return looksLikeGitDir(dir) }

// deadlineFor returns how long a managed command may run. The caller's own
// deadline always wins when it is sooner.
func (m *Manager) deadlineFor(ctx context.Context) time.Duration {
	if m.commandDeadline <= 0 {
		return 0
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < m.commandDeadline {
			return remaining
		}
	}
	return m.commandDeadline
}

// checkReady validates the manager and its seams. A nil seam is an internal
// error rather than a panic or a silently skipped check.
func (m *Manager) checkReady() error {
	if m == nil || m.root == "" {
		return storeErrorf(ReasonInternal, "manager is not initialised")
	}
	switch {
	case m.now == nil:
		return storeErrorf(ReasonInternal, "manager clock seam is nil")
	case m.lstat == nil:
		return storeErrorf(ReasonInternal, "manager stat seam is nil")
	case m.allocatedSize == nil:
		return storeErrorf(ReasonInternal, "manager allocated-size seam is nil")
	case m.freeSpace == nil:
		return storeErrorf(ReasonInternal, "manager free-space seam is nil")
	case m.logger == nil:
		return storeErrorf(ReasonInternal, "manager logger is nil")
	}
	return m.limits.Validate()
}

// storeError decorates an error with the store path.
func (m *Manager) storeError(reason StoreReason, path string, err error) error {
	se := storeError(reason, err)
	se.Path = path
	return se
}

// storeErrorf decorates a formatted error with the store path.
func (m *Manager) storeErrorf(reason StoreReason, path, format string, args ...any) error {
	se := storeError(reason, fmt.Errorf(format, args...))
	se.Path = path
	return se
}
