package sdd_test

import (
	"testing"

	"marshal/internal/sdd"
	"marshal/internal/tools/registry"
)

func TestRegisterToolsRegistersAllSDDTools(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	opts := sdd.SDDToolOpts{
		WS:       ws,
		RepoRoot: t.TempDir(),
		DAG:      &sdd.DAG{},
		RS:       &sdd.RepoState{},
		Progress: &sdd.Progress{},
		Git:      sdd.NewFakeGitOps(),
	}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	want := []string{
		"sdd.state_dump", "sdd.edit_guard", "sdd.health", "sdd.worktree",
		"sdd.contract", "sdd.validate_contract", "sdd.normalize_report",
		"sdd.validate_report", "sdd.verify", "sdd.audit_gate",
		"sdd.review_state", "sdd.review_guard", "sdd.merge", "sdd.commit",
		"sdd.prepare_retry", "sdd.branch_package", "sdd.rescue_bundle",
		"sdd.todo",
	}
	for _, name := range want {
		if _, ok := reg.Lookup(name); !ok {
			t.Errorf("missing tool %q", name)
		}
	}
}
