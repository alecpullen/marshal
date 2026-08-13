package bridge

import (
	"context"
	"path/filepath"
	"testing"
)

func testFleet(t *testing.T) *Fleet {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "unused")
	bin, args, env := helperCommand("registry")
	f.newChild = func(root string) *Child { return &Child{MarshalBin: bin, Args: args, Env: env} }
	t.Cleanup(f.Close)
	return f
}

func TestFleetSpawnsChildLazilyPerProject(t *testing.T) {
	f := testFleet(t)
	ctx := context.Background()
	if _, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "one"}); err != nil {
		t.Fatal(err)
	}
	if len(f.runtimes) != 1 {
		t.Fatalf("runtimes = %d", len(f.runtimes))
	}
	if _, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "two"}); err != nil {
		t.Fatal(err)
	}
	if len(f.runtimes) != 1 {
		t.Fatalf("same project spawned another runtime")
	}
	if _, err := f.Spawn(ctx, "/home/u/b", SpawnOptions{Name: "three"}); err != nil {
		t.Fatal(err)
	}
	if len(f.runtimes) != 2 {
		t.Fatalf("runtimes = %d, want 2", len(f.runtimes))
	}
}

func TestFleetResolvesSessionToProject(t *testing.T) {
	f := testFleet(t)
	id, err := f.Spawn(context.Background(), "/home/u/a", SpawnOptions{Name: "one"})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if rt.root != "/home/u/a" {
		t.Fatalf("root = %q", rt.root)
	}
	if _, err := f.RuntimeForSession("unknown"); err != ErrUnknownSession {
		t.Fatalf("err = %v", err)
	}
}

func TestSpawnIsolatedRecordsBranchAndTarget(t *testing.T) {
	f := testFleet(t)
	ctx, cancel := testContext(t)
	defer cancel()

	id, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "fix", Mode: "edit", Isolated: true, BaseRef: "HEAD"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a, ok := f.ws.Agent(id)
	if !ok {
		t.Fatal("agent not recorded")
	}
	if !a.Isolated {
		t.Error("Isolated must be recorded")
	}
	// TargetBranch cannot be derived later — it must be persisted at spawn.
	if a.TargetBranch == "" {
		t.Error("TargetBranch must be recorded at spawn")
	}
	if a.Branch == "" {
		t.Error("Branch must be recorded")
	}
}

func TestSpawnNonIsolatedRecordsNoBranch(t *testing.T) {
	f := testFleet(t)
	ctx, cancel := testContext(t)
	defer cancel()

	id, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "quick", Mode: "edit"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a, _ := f.ws.Agent(id)
	if a.Isolated || a.Branch != "" {
		t.Errorf("expected a non-isolated agent, got %+v", a)
	}
}

func TestFleetSpawnFailureIsReported(t *testing.T) {
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "not-a-real-marshal-binary")
	defer f.Close()
	if _, err := f.Spawn(context.Background(), "/home/u/a", SpawnOptions{Name: "one"}); err == nil {
		t.Fatal("expected spawn failure")
	}
	statuses := f.ProjectStatus()
	if len(statuses) != 1 || statuses[0].Available || statuses[0].Error == "" {
		t.Fatalf("statuses = %+v", statuses)
	}
}
