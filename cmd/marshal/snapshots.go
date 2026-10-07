package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"marshal/internal/app/config"
	"marshal/internal/snapshot"
)

// runSnapshots implements `marshal snapshots <status|cleanup|migrate|reset|discard>`:
// the conservative recovery CLI for Marshal's snapshot storage.
//
// It is deliberately standalone. It opens the store directly through
// internal/snapshot rather than starting a session, so it needs NO agent, NO
// TUI, and NO provider: a user recovering a 243 GiB legacy store has to be able
// to do it on a machine where nothing else works.
//
// The I/O shape follows runPlugin exactly (ctx, args, stdin, stdout, stderr) so
// main.go's dispatch is uniform and so tests can substitute the streams.
//
// The safety split is the design's:
//
//   - `status` is READ-ONLY, needs no confirmation, and works with no terminal.
//   - `cleanup` reconciles the versioned store automatically, but mutates a
//     LEGACY store only under the offline acknowledgement collected from the
//     CONTROLLING TERMINAL.
//   - `migrate`, `reset`, and `discard` require the controlling terminal for
//     every confirmation. Nothing can be authorised through stdin.
func runSnapshots(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("marshal snapshots: a subcommand is required (status|cleanup|migrate|reset|discard)")
	}
	switch args[0] {
	case "status":
		return runSnapshotsStatus(ctx, args[1:], stdout, stderr)
	case "cleanup":
		return runSnapshotsCleanup(ctx, args[1:], stdout, stderr)
	case "migrate":
		return runSnapshotsMigrate(ctx, args[1:], stdout, stderr)
	case "reset":
		return runSnapshotsReset(ctx, args[1:], stdout, stderr)
	case "discard":
		return runSnapshotsDiscard(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("marshal snapshots: unknown subcommand %q (status|cleanup|migrate|reset|discard)", args[0])
	}
}

// snapshotsDeps bundles the environment one subcommand needs, so every
// subcommand shares one construction path and a test can substitute the whole
// thing.
type snapshotsDeps struct {
	// Manager is the opened store.
	Manager *snapshot.Manager
	// DataDir is the data directory the store lives under, for the report.
	DataDir string
	// Limits are the effective budgets.
	Limits snapshot.Limits
	// Confirmer opens the CONTROLLING TERMINAL. It is a field rather than a
	// direct call so a test injects a scripted terminal and never needs a tty.
	Confirmer func() snapshot.Confirmer
	// RetentionDays is the retention to apply during cleanup.
	RetentionDays int
}

// snapshotsDepsFn is the package-level seam. Production builds the real
// dependencies; a test replaces it entirely.
var snapshotsDepsFn = openSnapshotsDeps

// openSnapshotsDeps resolves the data directory, reads the USER-GLOBAL config
// (never a project's untrusted settings), and opens the store.
//
// The config is read with SkipProjectConfig set, so an untrusted project file
// can never influence a storage-ceiling decision: a recovery command run inside
// a repository must apply the same limits as one run anywhere else. The
// cross-workspace ceiling is user-global only for exactly this reason, and the
// retention window comes from the same user-global read.
func openSnapshotsDeps(ctx context.Context, retentionDays int) (*snapshotsDeps, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find home directory: %w", err)
	}
	dataDir := config.DataDir(home)

	// User-global config only. A project file is never merged here.
	cfg, err := config.Load(config.LoadOptions{
		HomeDir:           home,
		SkipProjectConfig: true,
	})
	if err != nil {
		return nil, fmt.Errorf("load user-global config: %w", err)
	}
	limits := snapshot.Limits{
		WorkspaceMaxBytes: cfg.Snapshots.WorkspaceMaxBytes,
		GlobalMaxBytes:    cfg.Snapshots.GlobalMaxBytes,
	}
	// An unset value falls back to the production default through Normalize; a
	// NEGATIVE value is rejected rather than repaired, because a nonsense
	// ceiling must fail closed.
	if err := limits.Validate(); err != nil {
		return nil, fmt.Errorf("snapshot budgets in the user-global config are invalid: %w", err)
	}
	if retentionDays == 0 && cfg.Snapshots.RetentionDays != 0 {
		retentionDays = cfg.Snapshots.RetentionDays
	}

	m, err := snapshot.NewManager(dataDir, snapshot.WithLimits(limits))
	if err != nil {
		return nil, err
	}
	return &snapshotsDeps{
		Manager:       m,
		DataDir:       dataDir,
		Limits:        limits.Normalize(),
		Confirmer:     snapshot.OpenConfirmer,
		RetentionDays: retentionDays,
	}, nil
}

// withOwnedStore runs fn with exclusive ownership of the store.
//
// Ownership is taken for every subcommand, read-only ones included, so two
// concurrent recovery commands cannot interleave: `status` measures a store that
// no reclamation is mid-way through changing. A read-only command that cannot
// take the lock is reported as a timeout rather than retried forever.
func withOwnedStore(ctx context.Context, deps *snapshotsDeps, fn func() error) error {
	if err := deps.Manager.Bootstrap(ctx); err != nil {
		return err
	}
	if _, err := deps.Manager.Acquire(ctx); err != nil {
		return err
	}
	defer func() {
		// The mutations release ownership themselves while awaiting input, so
		// this drain must tolerate already-released state (Release is idempotent).
		_ = deps.Manager.Release()
	}()
	return fn()
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

// runSnapshotsStatus renders the READ-ONLY store report.
//
// Usage: marshal snapshots status
func runSnapshotsStatus(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("snapshots status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return fmt.Errorf("marshal snapshots status: unexpected argument %q (usage: marshal snapshots status)", rest[0])
	}

	deps, err := snapshotsDepsFn(ctx, -1)
	if err != nil {
		return err
	}
	var report string
	err = withOwnedStore(ctx, deps, func() error {
		st, err := deps.Manager.BuildStoreStatus(ctx)
		if err != nil {
			return err
		}
		report = snapshot.RenderStoreStatus(st)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, report)
	if !strings.HasSuffix(report, "\n") {
		fmt.Fprintln(stdout)
	}
	return nil
}

// ---------------------------------------------------------------------------
// cleanup
// ---------------------------------------------------------------------------

// runSnapshotsCleanup reconciles the versioned store and, under the offline
// acknowledgement, removes recognized abandoned temporary files from old-format
// stores.
//
// Usage: marshal snapshots cleanup [--retention-days N]
//
// There is no flag that can supply the acknowledgement. The ONLY way to
// authorise legacy mutation is to type it at the controlling terminal, which a
// process without a tty (an agent's tool call, a CI job, a pipeline) does not
// have.
func runSnapshotsCleanup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("snapshots cleanup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	retention := fs.Int("retention-days", -1, "expire versioned snapshots older than this many days (0 expires everything older than now; negative uses the configured retention)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return fmt.Errorf("marshal snapshots cleanup: unexpected argument %q", rest[0])
	}

	deps, err := snapshotsDepsFn(ctx, *retention)
	if err != nil {
		return err
	}
	var report string
	err = withOwnedStore(ctx, deps, func() error {
		res, err := deps.Manager.Cleanup(ctx, snapshot.CleanupOptions{
			Confirmer:     deps.Confirmer(),
			RetentionDays: deps.RetentionDays,
		})
		if err != nil {
			return err
		}
		report = snapshot.RenderCleanupReport(res, deps.Manager.Root())
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, report)
	if !strings.HasSuffix(report, "\n") {
		fmt.Fprintln(stdout)
	}
	return nil
}

// ---------------------------------------------------------------------------
// migrate
// ---------------------------------------------------------------------------

// runSnapshotsMigrate copies one old-format store into a protected, sealed,
// legacy-origin generation, preserving every lookup-able hash.
//
// Usage: marshal snapshots migrate --workspace <12-hex-id>
func runSnapshotsMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	workspace, err := parseWorkspaceFlag("snapshots migrate", args, stderr)
	if err != nil {
		return err
	}
	deps, err := snapshotsDepsFn(ctx, -1)
	if err != nil {
		return err
	}
	var report string
	err = withOwnedStore(ctx, deps, func() error {
		res, err := deps.Manager.MigrateWorkspace(ctx, workspace, snapshot.RecoveryOptions{
			Confirmer: deps.Confirmer(),
		})
		if err != nil {
			return err
		}
		report = snapshot.RenderMigrationReport(res, deps.Manager.Root())
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, report)
	if !strings.HasSuffix(report, "\n") {
		fmt.Fprintln(stdout)
	}
	return nil
}

// ---------------------------------------------------------------------------
// reset
// ---------------------------------------------------------------------------

// runSnapshotsReset deletes one old-format shadow store, and nothing else.
//
// Usage: marshal snapshots reset --workspace <12-hex-id>
//
// There is deliberately NO --yes and no other bypass. The full ID must be typed
// at the controlling terminal, because "delete this workspace's entire snapshot
// history" is not a decision a flag or a pipe is allowed to make.
func runSnapshotsReset(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	workspace, err := parseWorkspaceFlag("snapshots reset", args, stderr)
	if err != nil {
		return err
	}
	deps, err := snapshotsDepsFn(ctx, -1)
	if err != nil {
		return err
	}
	var report string
	err = withOwnedStore(ctx, deps, func() error {
		res, err := deps.Manager.ResetWorkspace(ctx, workspace, snapshot.RecoveryOptions{
			Confirmer: deps.Confirmer(),
		})
		if err != nil {
			return err
		}
		report = snapshot.RenderResetReport(res)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, report)
	if !strings.HasSuffix(report, "\n") {
		fmt.Fprintln(stdout)
	}
	return nil
}

// ---------------------------------------------------------------------------
// discard
// ---------------------------------------------------------------------------

// runSnapshotsDiscard removes a PROTECTED generation — normally a migrated
// legacy-origin archive — under the same confirmation rules a reset uses.
//
// Usage: marshal snapshots discard --workspace <id> --generation <id>
//
// It exists because a migration deliberately makes its protection persist:
// discarding the only surviving copy of pre-v2 rollback history has to be an
// explicit act of its own, not a side effect of ordinary retention. This is the
// ONLY path that lifts that protection.
func runSnapshotsDiscard(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("snapshots discard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	workspaceFlag := fs.String("workspace", "", "the 12-character workspace hash whose protected generation to discard")
	generationFlag := fs.String("generation", "", "the generation identifier to discard")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return fmt.Errorf("marshal snapshots discard: unexpected argument %q", rest[0])
	}
	if *workspaceFlag == "" {
		return errors.New("marshal snapshots discard: --workspace is required (usage: marshal snapshots discard --workspace <id> --generation <id>)")
	}
	if *generationFlag == "" {
		return errors.New("marshal snapshots discard: --generation is required; find it with 'marshal snapshots status'")
	}
	workspace, err := parseWorkspaceID(*workspaceFlag, "snapshots discard")
	if err != nil {
		return err
	}
	deps, err := snapshotsDepsFn(ctx, -1)
	if err != nil {
		return err
	}
	var report string
	err = withOwnedStore(ctx, deps, func() error {
		res, err := deps.Manager.DiscardProtectedGeneration(ctx, workspace, *generationFlag, snapshot.RecoveryOptions{
			Confirmer: deps.Confirmer(),
		})
		if err != nil {
			return err
		}
		report = snapshot.RenderDiscardReport(res)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, report)
	if !strings.HasSuffix(report, "\n") {
		fmt.Fprintln(stdout)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Shared argument handling
// ---------------------------------------------------------------------------

// parseWorkspaceFlag parses a `--workspace <id>` argument and validates that the
// value is a plain workspace hash.
//
// Validation happens BEFORE anything opens the store, so a user-supplied
// FILESYSTEM PATH is refused as `--workspace` by construction. The store path is
// always derived from the validated identifier inside the manager's own root, so
// there is no code path in which an arbitrary path could be resolved for
// deletion.
func parseWorkspaceFlag(name string, args []string, stderr io.Writer) (string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	workspaceFlag := fs.String("workspace", "", "the 12-character workspace hash to recover (see 'marshal snapshots status')")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return "", fmt.Errorf("marshal %s: unexpected argument %q", name, rest[0])
	}
	if *workspaceFlag == "" {
		return "", fmt.Errorf("marshal %s: --workspace is required (usage: marshal %s --workspace <id>)", name, name)
	}
	return parseWorkspaceID(*workspaceFlag, name)
}

// parseWorkspaceID validates a workspace identifier or explains why it is not
// one. The message names the expected shape, because the most common mistake is
// passing a project path where an identifier is required.
func parseWorkspaceID(value, name string) (string, error) {
	if !snapshot.ValidWorkspaceID(value) {
		return "", fmt.Errorf(
			"marshal %s: %q is not a workspace identifier; it must be the 12-character hexadecimal "+
				"workspace hash shown by 'marshal snapshots status' (a filesystem path is never accepted)",
			name, value)
	}
	return value, nil
}
