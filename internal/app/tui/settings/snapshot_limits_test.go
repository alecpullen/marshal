package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func snapshotsRow(t *testing.T, s *state, id string) *field {
	t.Helper()
	for _, row := range snapshotsFrame(s).List.Rows() {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("no row %q in snapshotsFrame", id)
	return nil
}

func TestSnapshotsFrameHasStorageBudgetFields(t *testing.T) {
	s := newState(config.Default())
	for _, id := range []string{"snapshots.workspace_max_bytes", "snapshots.global_max_bytes"} {
		f := snapshotsRow(t, s, id)
		if f.TomlPath != id {
			t.Errorf("%s: tomlPath = %q, want %q", id, f.TomlPath, id)
		}
		if f.Desc == "" {
			t.Errorf("%s: empty description", id)
		}
	}
}

func TestSnapshotsWorkspaceBudgetApplies(t *testing.T) {
	s := newState(config.Default())
	f := snapshotsRow(t, s, "snapshots.workspace_max_bytes")
	if err := f.SetStr("4 GB"); err != nil {
		t.Fatalf("SetStr: %v", err)
	}
	if s.cfg.Snapshots.WorkspaceMaxBytes != 4*1024*1024*1024 {
		t.Fatalf("workspace = %d, want 4 GiB", s.cfg.Snapshots.WorkspaceMaxBytes)
	}
}

func TestSnapshotsBudgetsRejectInvalid(t *testing.T) {
	s := newState(config.Default())
	before := s.cfg.Snapshots
	for _, id := range []string{"snapshots.workspace_max_bytes", "snapshots.global_max_bytes"} {
		f := snapshotsRow(t, s, id)
		for _, v := range []string{"0", "-1", "-2 GB"} {
			if err := f.SetStr(v); err == nil {
				t.Errorf("%s accepted invalid %q", id, v)
			}
		}
	}
	// An invalid value must not have been applied by a UI-only path.
	if s.cfg.Snapshots != before {
		t.Fatalf("invalid input mutated config: %+v", s.cfg.Snapshots)
	}
}

func TestSnapshotsGlobalBudgetWritesUserScope(t *testing.T) {
	s := newState(config.Default())
	global := snapshotsRow(t, s, "snapshots.global_max_bytes")
	if !FieldWriteGlobal(global) {
		t.Fatal("global budget field must be a user-global target")
	}
	if err := global.SetStr("20 GB"); err != nil {
		t.Fatalf("SetStr: %v", err)
	}
	if s.cfg.Snapshots.GlobalMaxBytes != 20*1024*1024*1024 {
		t.Fatalf("global = %d, want 20 GiB", s.cfg.Snapshots.GlobalMaxBytes)
	}
	// The per-workspace ceiling stays project-scoped.
	if FieldWriteGlobal(snapshotsRow(t, s, "snapshots.workspace_max_bytes")) {
		t.Error("workspace budget must not be a user-global target")
	}
}
