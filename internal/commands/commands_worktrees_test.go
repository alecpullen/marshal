package commands

import (
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

func TestWorktreesCommandListsFleet(t *testing.T) {
	state := newTestState()
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)
	cmd, _ := cmdReg.Lookup("worktrees")

	orig, origBase := listFleet, listFleetBase
	listFleet = func(git worktree.GitOps, repoRoot, base string) ([]worktree.FleetRow, error) {
		if repoRoot != "/repo" {
			t.Errorf("repoRoot = %q, want /repo", repoRoot)
		}
		if base != "abc123" {
			t.Errorf("base = %q, want the resolved SHA abc123", base)
		}
		return []worktree.FleetRow{
			{Path: "/repo/.marshal/worktrees/feat-x", Branch: "feat/x", Ahead: 2, Behind: 1, Dirty: true, Age: 2 * time.Hour},
			{Path: "/repo/.marshal/worktrees/feat-y", Branch: "feat/y", Age: 30 * time.Minute},
		}, nil
	}
	listFleetBase = func(root string) (string, error) { return "abc123", nil }
	t.Cleanup(func() { listFleet, listFleetBase = orig, origBase })

	res := cmd.Handler(state, nil)
	if res.Doc == nil {
		t.Fatalf("/worktrees should return a Doc, got %+v", res)
	}
	if res.Doc.Title != "Worktrees" {
		t.Errorf("Title = %q, want Worktrees", res.Doc.Title)
	}
	if len(res.Doc.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(res.Doc.Rows))
	}
	r := res.Doc.Rows[0]
	if r.Text != "feat/x" {
		t.Errorf("Text = %q, want the branch", r.Text)
	}
	if !strings.Contains(r.Detail, "↑2") || !strings.Contains(r.Detail, "↓1") {
		t.Errorf("Detail = %q, want ahead/behind counts", r.Detail)
	}
	if !strings.Contains(r.Detail, "dirty") {
		t.Errorf("Detail = %q, want the dirty marker", r.Detail)
	}
	if !strings.Contains(r.Detail, "2h") {
		t.Errorf("Detail = %q, want the human age", r.Detail)
	}
	if r.Desc != "/repo/.marshal/worktrees/feat-x" {
		t.Errorf("Desc = %q, want the path", r.Desc)
	}
	if r.Action != nil {
		t.Error("rows must be read-only: Action set")
	}
}

func TestWorktreesCommandEmpty(t *testing.T) {
	state := newTestState()
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)
	cmd, _ := cmdReg.Lookup("worktrees")

	orig, origBase := listFleet, listFleetBase
	listFleet = func(git worktree.GitOps, repoRoot, base string) ([]worktree.FleetRow, error) {
		return nil, nil
	}
	listFleetBase = func(root string) (string, error) { return "abc123", nil }
	t.Cleanup(func() { listFleet, listFleetBase = orig, origBase })

	res := cmd.Handler(state, nil)
	if res.Text != "No agent worktrees." {
		t.Errorf("Text = %q, want the empty message", res.Text)
	}
}

func TestWorktreesCommandListError(t *testing.T) {
	state := newTestState()
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)
	cmd, _ := cmdReg.Lookup("worktrees")

	orig, origBase := listFleet, listFleetBase
	listFleet = func(git worktree.GitOps, repoRoot, base string) ([]worktree.FleetRow, error) {
		return nil, errors.New("boom")
	}
	listFleetBase = func(root string) (string, error) { return "abc123", nil }
	t.Cleanup(func() { listFleet, listFleetBase = orig, origBase })

	res := cmd.Handler(state, nil)
	if res.Text == "" {
		t.Error("want an error message on ListFleet failure")
	}
}
