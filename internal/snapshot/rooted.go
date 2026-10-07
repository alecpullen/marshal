package snapshot

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Rooted is a snapshotter whose target root is resolved on every call, so
// it follows a session's worktree rebinds without any subscription or
// swap. Each root gets an independent store (the store path derives from the
// absolute root hash), so project-root and worktree histories never mix.
// (Pipeline run worktrees live under <projectRoot>/.marshal/pipeline/<slug>/worktrees;
// this package neither knows nor cares.)
//
// Maintenance is deliberately NOT per-root. A sweep must cover every store
// under the snapshots root — the main checkout, each worktree a session has
// bound to, and stores whose checkout no longer exists at all — because
// storage is bounded globally and an abandoned store is exactly the kind that
// grows without anyone noticing. The per-operation methods (Track, Diff,
// Restore) stay bound to the ACTIVE root; only Prune/Maintain sweep.
type Rooted struct {
	dataDir     string
	projectRoot string
	activeRoot  func() string
	maxFile     int64
	ignore      []string
	logger      *slog.Logger

	// limits and globalLimits are the storage ceilings a maintenance sweep
	// enforces. They are options rather than constructor arguments so every
	// existing NewRooted call keeps working; an unset limits value falls back
	// to the production defaults through the manager's own normalization.
	limits       Limits
	limitsSet    bool
	globalLimits func() (Limits, error)
	// gitBinary overrides the Git executable this service's maintenance and
	// status invocations use. Production leaves it empty (the manager default
	// is "git"); a test installs a recording wrapper here to prove what does
	// and does not run.
	gitBinary string

	mu       sync.Mutex
	services map[string]*Service
}

// RootedOption configures a Rooted. Every option is optional; the defaults are
// production-safe, which is what lets the original six-argument constructor
// keep working unchanged.
type RootedOption func(*Rooted)

// WithRootedLimits sets the ceilings maintenance enforces. A zero value means
// "use the production defaults", exactly as Manager treats it.
func WithRootedLimits(l Limits) RootedOption {
	return func(r *Rooted) {
		r.limits = l
		r.limitsSet = true
	}
}

// WithRootedGlobalLimits installs the user-global ceiling reader. It is
// consulted FRESH on every sweep (the manager calls it under the store lock on
// every admission decision), so a limit the user lowered in another process
// takes effect without restarting this one.
func WithRootedGlobalLimits(fn func() (Limits, error)) RootedOption {
	return func(r *Rooted) {
		if fn != nil {
			r.globalLimits = fn
		}
	}
}

// WithRootedGitBinary overrides the Git executable for this service's
// maintenance and status invocations. It is a fault-injection seam: a test
// installs a recording wrapper so it can assert exactly which Git commands a
// lifecycle path runs. Production never calls it.
func WithRootedGitBinary(path string) RootedOption {
	return func(r *Rooted) {
		if path != "" {
			r.gitBinary = path
		}
	}
}

// NewRooted builds a Rooted. activeRoot must return the session's current
// active root (or "" to mean the project root).
//
// The options are additive: the six-argument form is the production default and
// remains valid.
func NewRooted(dataDir, projectRoot string, activeRoot func() string, maxFile int64, ignore []string, logger *slog.Logger, opts ...RootedOption) *Rooted {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Rooted{dataDir: dataDir, projectRoot: projectRoot, activeRoot: activeRoot, maxFile: maxFile, ignore: ignore, logger: logger}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

func (r *Rooted) svc() *Service {
	root := r.activeRoot()
	if root == "" {
		root = r.projectRoot
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.services[root]; ok {
		return s
	}
	s := New(r.dataDir, root, r.maxFile, r.ignore, r.logger)
	if r.services == nil {
		r.services = make(map[string]*Service)
	}
	r.services[root] = s
	return s
}

func (r *Rooted) Track(ctx context.Context) (string, error) { return r.svc().Track(ctx) }
func (r *Rooted) Diff(ctx context.Context, hash string) (string, error) {
	return r.svc().Diff(ctx, hash)
}
func (r *Rooted) Restore(ctx context.Context, hash string) error { return r.svc().Restore(ctx, hash) }
func (r *Rooted) Enabled() bool                                  { return r.svc().Enabled() }

// LookupSnapshot resolves a hash against the active root's workspace, across
// every v2 generation and any pending legacy repository.
//
// It is the Rooted-shaped form of Service.LookupSnapshot, so a caller holding
// the Rooted the runtime already wires up can obtain the typed
// expiry/not-found/invalid-identifier distinction without constructing a
// Service of its own.
func (r *Rooted) LookupSnapshot(ctx context.Context, hash string) (*SnapshotLocation, error) {
	return r.svc().LookupSnapshot(ctx, hash)
}

// MaintenanceReport summarises one bounded maintenance sweep.
type MaintenanceReport struct {
	// Reconciled lists the workspace hashes reconciliation finished work in.
	Reconciled []string
	// Reclaimed lists the generations removed, oldest first.
	Reclaimed []ReclaimedGeneration
	// Bytes is the measured size of the reclaimed generations.
	Bytes int64
	// Warnings are the structured problems the sweep found.
	Warnings []MaintenanceWarning
}

// Maintain runs a bounded reconciliation and retention sweep over EVERY store
// under the snapshots root.
//
// It is the whole-root replacement for the old main-checkout-only Prune:
//
//   - reconciliation finishes interrupted work in every versioned workspace,
//     including a workspace whose checkout no longer exists (the manifest is
//     the only record it ever belonged to a path);
//   - retention reclaims whole expired generations in every versioned
//     workspace, so a worktree store that is not this session's active root is
//     still maintained;
//   - legacy and protected storage is never mutated: it is inspected and left
//     byte-for-byte alone.
//
// It is BOUNDED by construction: it removes whole generations only (there is no
// per-object sweep and no repack anywhere in this package), it honours ctx
// cancellation between every step, and each spawned Git command carries the
// manager's own deadline. Nothing here creates a pack, a reflog, or an index.
//
// A workspace with no store at all is not created just to be maintained: the
// sweep is a no-op when the snapshots root does not exist, so a session that
// never captured leaves no empty store behind.
func (r *Rooted) Maintain(ctx context.Context, retentionDays int) (*MaintenanceReport, error) {
	if r == nil || r.dataDir == "" || !gitAvailable() {
		return &MaintenanceReport{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m, err := NewManager(r.dataDir, r.managerOptions()...)
	if err != nil {
		return nil, err
	}
	// Nothing to maintain: an absent root means no workspace has ever captured.
	// Bootstrapping here would create the store as a side effect of a no-op.
	info, err := os.Lstat(m.Root())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &MaintenanceReport{}, nil
		}
		return nil, m.storeError(ReasonUnreadableFile, m.Root(), err)
	}
	if !info.IsDir() {
		return nil, m.storeErrorf(ReasonInternal, m.Root(), "snapshots root is not a directory")
	}

	if err := m.Bootstrap(ctx); err != nil {
		return nil, err
	}
	if _, err := m.Acquire(ctx); err != nil {
		return nil, err
	}
	defer func() {
		if releaseErr := m.Release(); releaseErr != nil {
			r.logger.Warn("snapshot store ownership was not released after maintenance", "error", releaseErr)
		}
	}()

	report := &MaintenanceReport{}

	// 1. Reconcile every versioned workspace: finish interrupted deletions,
	//    resolve in-flight captures, remove known scratch.
	if err := ctx.Err(); err != nil {
		return report, err
	}
	reconciled, err := m.Reconcile(ctx)
	if err != nil {
		return report, err
	}
	for _, ws := range reconciled.Workspaces {
		report.Reconciled = append(report.Reconciled, ws.Workspace)
	}
	report.Warnings = append(report.Warnings, reconciled.Warnings...)

	// 2. Reclaim whole expired generations, oldest first, in every workspace.
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if retentionDays >= 0 {
		expired, err := m.ReclaimExpired(ctx, retentionDays)
		if err != nil {
			return report, err
		}
		if expired != nil {
			report.Reclaimed = append(report.Reclaimed, expired.Reclaimed...)
			report.Bytes = expired.Bytes
			report.Warnings = append(report.Warnings, expired.Warnings...)
		}
	}

	// 3. Report a store that still needs attention. This is the signal the
	//    runtime turns into a user-visible warning; it is not an error the
	//    sweep failed with, because the sweep did everything it could.
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if statusErr := m.Status(ctx); statusErr != nil {
		return report, statusErr
	}
	return report, nil
}

// Prune runs the shared whole-root maintenance sweep, discarding the report.
//
// RetentionDays keeps the meaning the legacy prune had: a negative value
// disables expiry, and zero expires everything older than now. Unlike the old
// implementation this covers worktree and inactive stores too — a store whose
// checkout was deleted is exactly the one nobody would otherwise notice.
func (r *Rooted) Prune(ctx context.Context, retentionDays int) error {
	_, err := r.Maintain(ctx, retentionDays)
	return err
}

// managerOptions is the shared manager configuration every sweep and every
// per-workspace operation uses, so a sweep and a capture can never disagree
// about the ceilings in force.
func (r *Rooted) managerOptions() []ManagerOption {
	opts := []ManagerOption{WithLogger(r.logger)}
	if r.limitsSet {
		opts = append(opts, WithLimits(r.limits))
	}
	if r.globalLimits != nil {
		opts = append(opts, WithGlobalLimitReader(r.globalLimits))
	}
	if r.gitBinary != "" {
		opts = append(opts, WithGitBinary(r.gitBinary))
	}
	return opts
}

// HasActiveStore reports whether the ACTIVE root has ANY snapshot store: a
// versioned one, or a pre-v2 legacy repository that still holds rollback
// history.
//
// It exists so the runtime's startup sweep can be skipped entirely for a
// workspace that has never captured and never will — the same fast-path
// philosophy as Service.hasStore, extended to the legacy layout. Without it, a
// session in an unrelated directory would pay for a whole-root scan of every
// other store on the machine (which on a store holding hundreds of gigabytes of
// legacy history is real, repeated, startup latency), and it would create store
// metadata for a workspace that asked for none.
//
// The check is two Lstats, never a scan.
func (r *Rooted) HasActiveStore() bool {
	if r == nil || r.dataDir == "" {
		return false
	}
	root := r.activeRoot()
	if root == "" {
		root = r.projectRoot
	}
	svc := New(r.dataDir, root, r.maxFile, r.ignore, r.logger)
	if svc.hasStore() {
		return true
	}
	legacy, err := WorkspaceStorePath(filepath.Join(r.dataDir, snapshotsDirName), svc.Workspace())
	if err != nil {
		return false
	}
	info, err := os.Lstat(legacy)
	return err == nil && info.IsDir()
}

// Status reports a structured problem when the ACTIVE root's store needs
// attention, or nil when it does not.
//
// It is deliberately scoped and read-only: it measures one workspace, lists
// that workspace's snapshot refs, and writes nothing. It never reconciles,
// never reclaims, and never repacks — which is what makes it safe on the
// shutdown path, where heavy storage work is forbidden.
//
// A workspace with no versioned store is a no-op. The runtime calls this once
// per session close, and a session that never captured must not pay for a
// whole-root scan (nor leave an empty store behind by bootstrapping one).
func (r *Rooted) Status(ctx context.Context) error {
	if r == nil || r.dataDir == "" || !gitAvailable() {
		return nil
	}
	root := r.activeRoot()
	if root == "" {
		root = r.projectRoot
	}
	svc := New(r.dataDir, root, r.maxFile, r.ignore, r.logger)
	if !svc.hasStore() {
		return nil
	}
	m, err := NewManager(r.dataDir, r.managerOptions()...)
	if err != nil {
		return err
	}
	if err := m.Bootstrap(ctx); err != nil {
		return err
	}
	if _, err := m.Acquire(ctx); err != nil {
		return err
	}
	defer func() {
		if releaseErr := m.Release(); releaseErr != nil {
			r.logger.Warn("snapshot store ownership was not released after status", "error", releaseErr)
		}
	}()
	return m.StatusWorkspace(ctx, svc.Workspace())
}

// CloseSnapshots cancels and joins this service's snapshot work. It performs
// NO storage maintenance.
//
// That absence is the whole point of the method. The lifecycle it replaces ran
// a filesystem prune and then `git gc --prune=now` on every shutdown; the gc
// could not free anything (snapshots were parent-chained and reachable from
// HEAD), and an interrupted repack left 171 GiB of tmp_pack_* files behind. A
// shutdown must now do no heavy storage work at all: maintenance runs at
// startup and before admission, where it is bounded and observable.
//
// Joining means waiting for any in-flight per-root operation (each Service
// serialises its own work on a semaphore) and returning as soon as they have
// finished or ctx ends.
func (r *Rooted) CloseSnapshots(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	services := make([]*Service, 0, len(r.services))
	for _, s := range r.services {
		services = append(services, s)
	}
	r.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	for _, s := range services {
		if err := s.lock(ctx); err != nil {
			return err
		}
		s.unlock()
	}
	return nil
}
