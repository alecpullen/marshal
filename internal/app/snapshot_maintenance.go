package app

import (
	"errors"
	"fmt"
	"time"

	"marshal/internal/agent"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/snapshot"
)

// startupSnapshotMaintenanceTimeout bounds the whole startup sweep.
//
// It is a bound, not a budget to spend: the sweep reconciles interrupted
// bookkeeping and removes whole expired generations, and both are small. A
// store large enough to take longer than this is left to the next startup
// rather than holding the session open, and the timeout is reported as a
// cancellation (not as a storage fault the user must act on).
const startupSnapshotMaintenanceTimeout = 20 * time.Second

// snapshotLimits converts the effective configuration into the store's own
// ceilings. Only the two budgets are configured; the free-space reserve and the
// allocation unit keep their production defaults through Limits.Normalize.
func snapshotLimits(cfg config.Config) snapshot.Limits {
	return snapshot.Limits{
		WorkspaceMaxBytes: cfg.Snapshots.WorkspaceMaxBytes,
		GlobalMaxBytes:    cfg.Snapshots.GlobalMaxBytes,
	}
}

// snapshotGlobalLimitsReader returns the reader the store consults while it
// holds the store lock.
//
// The global ceiling is user-global only. The merged config already carries the
// user value (a project file's global key is stripped before merge), so the
// merged value IS the configured ceiling; what this reader adds is FRESHNESS.
// It re-reads the global key from the user config on every call, so a ceiling
// another process lowered takes effect without restarting this session — and
// without a config reload.
//
// The re-read is deliberately ONE file parse of ONE key, not a config reload:
// the session keeps its profile, routing, and overrides, and the store gets the
// current ceiling. A user config that cannot be read is an error, never a
// silent fall back to the stale value: an unreadable ceiling is unknown
// accounting, and admitting a write against an unknown ceiling is how a store
// grows past a limit its owner just lowered.
func snapshotGlobalLimitsReader(homeDir string, cfg config.Config) func() (snapshot.Limits, error) {
	path := config.UserConfigPath(homeDir)
	workspaceMax := cfg.Snapshots.WorkspaceMaxBytes
	globalMax := cfg.Snapshots.GlobalMaxBytes
	return func() (snapshot.Limits, error) {
		l := snapshot.Limits{WorkspaceMaxBytes: workspaceMax, GlobalMaxBytes: globalMax}
		global, err := config.UserGlobalSnapshotMaxBytes(path)
		if err != nil {
			return snapshot.Limits{}, fmt.Errorf("read user-global snapshot limits: %w", err)
		}
		if global > 0 {
			l.GlobalMaxBytes = global
		}
		return l, nil
	}
}

// reportStartupSnapshotMaintenance turns one startup sweep into log lines and,
// when the store needs the user's attention, one transcript warning through the
// SAME warning helper the in-turn capture hooks use.
//
// It never returns an error: a store that needs attention must not stop a
// session from starting, and the store's own admission still enforces the
// budgets either way. A sweep abandoned by the caller's cancellation is logged
// at Debug and never shown — an expected quit is not a recovery-required event.
func reportStartupSnapshotMaintenance(runner *agent.Runner, state *session.State, report *snapshot.MaintenanceReport, sweepErr error, cancelled bool) {
	if state == nil {
		return
	}
	logger := state.Logger()
	if report != nil {
		if len(report.Reclaimed) > 0 {
			logger.Info("snapshot startup maintenance reclaimed generations",
				"count", len(report.Reclaimed), "bytes", report.Bytes)
		}
		for _, w := range report.Warnings {
			logger.Warn("snapshot startup maintenance warning",
				"workspace", w.Workspace, "reason", string(w.Reason), "path", w.Path, "message", w.Message)
		}
	}
	if sweepErr != nil {
		logger.Warn("snapshot startup maintenance failed", "error", sweepErr)
	} else if report != nil && len(report.Warnings) > 0 {
		sweepErr = maintenanceWarningError(report.Warnings[0])
	}
	if sweepErr == nil || runner == nil {
		return
	}
	runner.ReportMaintenanceWarning(agent.SnapshotPhaseMaintenance, sweepErr, cancelled)
}

// maintenanceWarningError renders a structured maintenance warning as the error
// the warning helper classifies. The reason token is written verbatim, which is
// what lets the helper recover the structured reason without this package
// sharing a type with the store.
func maintenanceWarningError(w snapshot.MaintenanceWarning) error {
	if w.Message == "" {
		return errors.New(string(w.Reason))
	}
	return fmt.Errorf("%s: %s", w.Reason, w.Message)
}
