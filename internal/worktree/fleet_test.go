package worktree

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestListFleetFiltersAndPopulates(t *testing.T) {
	g := NewFakeGitOps()
	agent := "/repo/.marshal/worktrees"
	wtPath := filepath.Join(agent, "feat-x")
	detached := filepath.Join(agent, "detached")
	pipeline := "/repo/.marshal/pipeline/p1/worktrees/run"
	g.ListWorktreesFunc = func(dir string) ([]WorktreeInfo, error) {
		return []WorktreeInfo{
			{Path: wtPath, Branch: "feat/x"},
			{Path: detached},                        // detached: no branch
			{Path: pipeline, Branch: "pipeline/p1"}, // outside the agent dir
			{Path: "/repo", Branch: "main"},         // the project checkout itself
		}, nil
	}
	g.AheadBehindFunc = func(dir, base, branch string) (int, int, error) {
		if dir == wtPath {
			return 2, 1, nil
		}
		return 0, 0, errors.New("unexpected dir")
	}
	g.DirtyDirs[wtPath] = true
	tip := time.Now().Add(-2 * time.Hour)
	g.BranchAgeFunc = func(dir, branch string) (time.Time, error) {
		if dir == wtPath {
			return tip, nil
		}
		return time.Time{}, errors.New("unexpected dir")
	}

	rows, err := ListFleet(g, "/repo", "main")
	if err != nil {
		t.Fatalf("ListFleet: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want only the agent worktree", rows)
	}
	row := rows[0]
	if row.Path != wtPath || row.Branch != "feat/x" {
		t.Fatalf("row = %+v", row)
	}
	if row.Ahead != 2 || row.Behind != 1 {
		t.Errorf("Ahead/Behind = %d/%d, want 2/1", row.Ahead, row.Behind)
	}
	if !row.Dirty {
		t.Errorf("Dirty = false, want true")
	}
	if row.Age < time.Hour+59*time.Minute || row.Age > 2*time.Hour+time.Minute {
		t.Errorf("Age = %v, want about 2h", row.Age)
	}
}

func TestListFleetDegradesPerRowOnGitErrors(t *testing.T) {
	g := NewFakeGitOps()
	wtPath := "/repo/.marshal/worktrees/feat-x"
	g.ListWorktreesFunc = func(dir string) ([]WorktreeInfo, error) {
		return []WorktreeInfo{{Path: wtPath, Branch: "feat/x"}}, nil
	}
	g.AheadBehindFunc = func(dir, base, branch string) (int, int, error) {
		return 0, 0, errors.New("boom")
	}
	g.BranchAgeFunc = func(dir, branch string) (time.Time, error) {
		return time.Time{}, errors.New("boom")
	}

	rows, err := ListFleet(g, "/repo", "main")
	if err != nil {
		t.Fatalf("ListFleet: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Ahead != 0 || rows[0].Behind != 0 || rows[0].Dirty || rows[0].Age != 0 {
		t.Fatalf("row = %+v, want zero-valued degraded row", rows[0])
	}
}

func TestListFleetPropagatesListError(t *testing.T) {
	g := NewFakeGitOps()
	g.ListWorktreesFunc = func(dir string) ([]WorktreeInfo, error) {
		return nil, errors.New("not a repo")
	}
	if _, err := ListFleet(g, "/repo", "main"); err == nil {
		t.Fatal("want an error when the worktree list fails")
	}
}
