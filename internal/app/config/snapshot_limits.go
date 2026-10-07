package config

import "fmt"

// Snapshot storage budgets. Both are positive byte counts; zero does not
// mean "unlimited" — an invalid limit fails closed for storage (capture is
// refused rather than silently unbounded), and only [snapshots] enabled =
// false disables capture. The constants are exported so the write-time
// setters and later storage tasks share one source of truth.
const (
	// DefaultWorkspaceMaxBytes caps managed snapshot storage per workspace
	// (2 GiB).
	DefaultWorkspaceMaxBytes int64 = 2_147_483_648
	// DefaultGlobalMaxBytes caps managed snapshot storage across all
	// workspaces (10 GiB). It is user-global only.
	DefaultGlobalMaxBytes int64 = 10_737_418_240
)

// ValidateSnapshotLimit reports an error when v is not a positive byte
// count. The path is echoed so the caller's message names the offending
// setting. Reused by the config diagnostics and by the write-time setters
// (native tool and TUI) so an invalid limit is rejected before anything is
// persisted.
func ValidateSnapshotLimit(path string, v int64) error {
	if v <= 0 {
		return fmt.Errorf("%s must be a positive byte count (got %d); budgets are always on — set snapshots.enabled = false to disable capture instead", path, v)
	}
	return nil
}

// ValidateSnapshotLimits checks the merged snapshot storage budgets and
// returns one diagnostic per invalid value. It is the reusable limit
// validator for this feature: Diagnose reports its results, and the setters
// apply the identical rule so a rejected value can never be persisted.
//
// The returned diagnostics carry Path, Severity and Message; Diagnose fills
// in Source from layer provenance. Values are reported as written and are
// never normalized to an unlimited/no-limit value.
func ValidateSnapshotLimits(cfg Config) []Diagnostic {
	checks := []struct {
		path string
		v    int64
	}{
		{"snapshots.workspace_max_bytes", cfg.Snapshots.WorkspaceMaxBytes},
		{"snapshots.global_max_bytes", cfg.Snapshots.GlobalMaxBytes},
	}
	var ds []Diagnostic
	for _, c := range checks {
		if c.v > 0 {
			continue
		}
		ds = append(ds, Diagnostic{
			Severity: SeverityError,
			Path:     c.path,
			Message: fmt.Sprintf(
				"invalid storage budget %d; must be a positive byte count (budgets are always on — set snapshots.enabled = false to disable capture)",
				c.v),
		})
	}
	return ds
}

// UserGlobalSnapshotMaxBytes reads ONLY snapshots.global_max_bytes from the
// user config file at path.
//
// The cross-workspace ceiling is user-global only, so it must be re-read from
// the user file rather than from a merged config: a project file cannot carry
// the key, and the session's merged config is a snapshot taken at load time. A
// storage operation consults this while it holds the store lock, so a ceiling
// another process lowered takes effect immediately.
//
// An absent file or an absent [snapshots] section returns 0, which means
// "unset" and lets the caller keep the merged value. A file that cannot be
// PARSED is an error, never 0: an unreadable ceiling is unknown accounting, and
// treating it as unset would admit writes against a limit nobody can state.
func UserGlobalSnapshotMaxBytes(path string) (int64, error) {
	if path == "" {
		return 0, nil
	}
	file, err := loadFile(path)
	if err != nil {
		return 0, err
	}
	if file.Snapshots == nil || file.Snapshots.GlobalMaxBytes == nil {
		return 0, nil
	}
	return *file.Snapshots.GlobalMaxBytes, nil
}

// stripProjectSnapshotGlobal removes snapshots.global_max_bytes from a
// project-file mirror. The cross-workspace ceiling is user-global only: a
// project file must never carry it, so a project save can neither raise nor
// lower it. When the remaining project-relevant fields all equal their
// built-in defaults (the section differed only by the stripped global), the
// whole section is dropped rather than written with default values —
// matching writeSections' own default comparison.
func stripProjectSnapshotGlobal(file *configFile, cfg Config) {
	if file.Snapshots == nil {
		return
	}
	file.Snapshots.GlobalMaxBytes = nil
	def := Default().Snapshots
	if cfg.Snapshots.Enabled == def.Enabled &&
		cfg.Snapshots.RetentionDays == def.RetentionDays &&
		cfg.Snapshots.MaxFileBytes == def.MaxFileBytes &&
		cfg.Snapshots.WorkspaceMaxBytes == def.WorkspaceMaxBytes {
		file.Snapshots = nil
	}
}
