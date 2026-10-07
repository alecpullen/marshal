package config

import (
	"math"
	"strings"
	"testing"

	"marshal/internal/trust"
)

func TestSnapshotLimitsDefaults(t *testing.T) {
	s := Default().Snapshots
	if s.WorkspaceMaxBytes != DefaultWorkspaceMaxBytes {
		t.Errorf("workspace default = %d, want %d", s.WorkspaceMaxBytes, DefaultWorkspaceMaxBytes)
	}
	if s.GlobalMaxBytes != DefaultGlobalMaxBytes {
		t.Errorf("global default = %d, want %d", s.GlobalMaxBytes, DefaultGlobalMaxBytes)
	}
	if DefaultWorkspaceMaxBytes != 2_147_483_648 {
		t.Errorf("workspace default constant = %d, want 2 GiB", DefaultWorkspaceMaxBytes)
	}
	if DefaultGlobalMaxBytes != 10_737_418_240 {
		t.Errorf("global default constant = %d, want 10 GiB", DefaultGlobalMaxBytes)
	}
	if ds := ValidateSnapshotLimits(Default()); len(ds) != 0 {
		t.Errorf("default limits produced diagnostics: %+v", ds)
	}
}

func TestSnapshotLimitsOldConfigCompat(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	// A pre-existing file with no storage keys: the defaults must survive and
	// the existing per-file/retention semantics must be unchanged.
	writeFile(t, home+"/.config/marshal/config.toml",
		"[snapshots]\nenabled = true\nretention_days = 3\nmax_file_bytes = 1234\n")
	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatal(err)
	}
	if l.Merged.Snapshots.WorkspaceMaxBytes != DefaultWorkspaceMaxBytes ||
		l.Merged.Snapshots.GlobalMaxBytes != DefaultGlobalMaxBytes {
		t.Fatalf("old config lost new defaults: %+v", l.Merged.Snapshots)
	}
	if l.Merged.Snapshots.RetentionDays != 3 || l.Merged.Snapshots.MaxFileBytes != 1234 {
		t.Fatalf("old semantics changed: %+v", l.Merged.Snapshots)
	}
	if ds := ValidateSnapshotLimits(l.Merged); len(ds) != 0 {
		t.Errorf("old config produced diagnostics: %+v", ds)
	}
}

func TestSnapshotLimitsInt64RoundTrip(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	path := UserConfigPath(home)
	cfg := Default()
	cfg.Snapshots.WorkspaceMaxBytes = 5_368_709_120
	cfg.Snapshots.GlobalMaxBytes = math.MaxInt64 // accepted; must not overflow or panic
	if err := SaveUserConfigSection(path, cfg); err != nil {
		t.Fatal(err)
	}
	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatal(err)
	}
	if l.Merged.Snapshots.WorkspaceMaxBytes != 5_368_709_120 {
		t.Errorf("workspace = %d, want 5368709120", l.Merged.Snapshots.WorkspaceMaxBytes)
	}
	if l.Merged.Snapshots.GlobalMaxBytes != math.MaxInt64 {
		t.Errorf("global = %d, want MaxInt64", l.Merged.Snapshots.GlobalMaxBytes)
	}
	if ds := ValidateSnapshotLimits(l.Merged); len(ds) != 0 {
		t.Errorf("large int64 limits produced diagnostics: %+v", ds)
	}
}

func TestSnapshotLimitsRejectZeroAndNegative(t *testing.T) {
	for _, tc := range []struct {
		workspace, global int64
		wantPath          string
	}{
		{0, DefaultGlobalMaxBytes, "snapshots.workspace_max_bytes"},
		{-1, DefaultGlobalMaxBytes, "snapshots.workspace_max_bytes"},
		{DefaultWorkspaceMaxBytes, 0, "snapshots.global_max_bytes"},
		{DefaultWorkspaceMaxBytes, -4096, "snapshots.global_max_bytes"},
	} {
		cfg := Default()
		cfg.Snapshots.WorkspaceMaxBytes = tc.workspace
		cfg.Snapshots.GlobalMaxBytes = tc.global
		ds := ValidateSnapshotLimits(cfg)
		if len(ds) != 1 {
			t.Errorf("workspace=%d global=%d: %d diagnostics, want 1: %+v", tc.workspace, tc.global, len(ds), ds)
			continue
		}
		if ds[0].Path != tc.wantPath {
			t.Errorf("workspace=%d global=%d: path = %q, want %q", tc.workspace, tc.global, ds[0].Path, tc.wantPath)
		}
		if ds[0].Severity != SeverityError {
			t.Errorf("workspace=%d global=%d: severity = %v, want error", tc.workspace, tc.global, ds[0].Severity)
		}
	}
}

func TestSnapshotLimitsNotNormalizedAtLoad(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml",
		"[snapshots]\nworkspace_max_bytes = 0\nglobal_max_bytes = -5\n")
	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatal(err)
	}
	// Invalid limits are kept as written (fail closed later); they must never
	// be silently rewritten to an unlimited/no-limit value.
	if l.Merged.Snapshots.WorkspaceMaxBytes != 0 {
		t.Errorf("workspace normalized to %d, want 0 kept", l.Merged.Snapshots.WorkspaceMaxBytes)
	}
	if l.Merged.Snapshots.GlobalMaxBytes != -5 {
		t.Errorf("global normalized to %d, want -5 kept", l.Merged.Snapshots.GlobalMaxBytes)
	}
	found := map[string]bool{}
	for _, d := range Diagnose(l.Merged, l) {
		found[d.Path] = true
	}
	for _, p := range []string{"snapshots.workspace_max_bytes", "snapshots.global_max_bytes"} {
		if !found[p] {
			t.Errorf("no diagnostic for %s", p)
		}
	}
}

func TestSnapshotLimitsUserGlobalWinsOverProject(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml",
		"[snapshots]\nglobal_max_bytes = 10737418240\nworkspace_max_bytes = 2147483648\n")
	writeFile(t, work+"/.marshal/config.toml",
		"[snapshots]\nglobal_max_bytes = 9223372036854775807\nworkspace_max_bytes = 4294967296\n")
	l, err := LoadLayers(LoadOptions{
		HomeDir: home, WorkingDir: work,
		TrustResolver: staticTrustResolver{decision: trust.DecisionTrustPermanent},
	})
	if err != nil {
		t.Fatal(err)
	}
	if l.Merged.Snapshots.GlobalMaxBytes != 10_737_418_240 {
		t.Errorf("project raised the global ceiling: %d", l.Merged.Snapshots.GlobalMaxBytes)
	}
	if l.Merged.Snapshots.WorkspaceMaxBytes != 4_294_967_296 {
		t.Errorf("project workspace override dropped: %d", l.Merged.Snapshots.WorkspaceMaxBytes)
	}
	if !l.ProjectSnapshotGlobalIgnored {
		t.Error("ProjectSnapshotGlobalIgnored = false")
	}
	if !hasPath(Diagnose(l.Merged, l), "snapshots.global_max_bytes") {
		t.Error("no diagnostic for the ignored project global key")
	}
}

func TestSnapshotLimitsProjectCannotLowerGlobal(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", "[snapshots]\nglobal_max_bytes = 8589934592\n")
	writeFile(t, work+"/.marshal/config.toml", "[snapshots]\nglobal_max_bytes = 1024\n")
	l, err := LoadLayers(LoadOptions{
		HomeDir: home, WorkingDir: work,
		TrustResolver: staticTrustResolver{decision: trust.DecisionTrustPermanent},
	})
	if err != nil {
		t.Fatal(err)
	}
	if l.Merged.Snapshots.GlobalMaxBytes != 8_589_934_592 {
		t.Errorf("project lowered the global ceiling: %d", l.Merged.Snapshots.GlobalMaxBytes)
	}
}

func TestSnapshotLimitsProjectGlobalNotPersisted(t *testing.T) {
	_, work := t.TempDir(), t.TempDir()
	projectPath := ProjectConfigPath(work)

	user := Default()
	user.Snapshots.GlobalMaxBytes = 8_589_934_592
	merged := user
	merged.Snapshots.WorkspaceMaxBytes = 4_294_967_296
	layers := Layers{Default: Default(), User: user, Merged: merged}

	if err := SaveProjectConfig(projectPath, merged, layers); err != nil {
		t.Fatal(err)
	}
	data := readFile(t, projectPath)
	if strings.Contains(data, "global_max_bytes") {
		t.Errorf("global_max_bytes leaked into the project config:\n%s", data)
	}
	if !strings.Contains(data, "workspace_max_bytes") {
		t.Errorf("project workspace override was not persisted:\n%s", data)
	}
}

func TestValidateSnapshotLimitSingleValue(t *testing.T) {
	if err := ValidateSnapshotLimit("snapshots.workspace_max_bytes", 1); err != nil {
		t.Errorf("positive value rejected: %v", err)
	}
	if err := ValidateSnapshotLimit("snapshots.workspace_max_bytes", math.MaxInt64); err != nil {
		t.Errorf("MaxInt64 rejected: %v", err)
	}
	for _, v := range []int64{0, -1, math.MinInt64} {
		if err := ValidateSnapshotLimit("snapshots.workspace_max_bytes", v); err == nil {
			t.Errorf("value %d accepted, want rejection", v)
		}
	}
}
