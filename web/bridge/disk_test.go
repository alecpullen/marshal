package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSized(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testFleetWithState(t *testing.T) *Fleet {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	f := NewFleet(ws, "unused", nil, stateDir, Limits{}, "", nil, "marshal-state")
	bin, args, env := helperCommand("registry")
	f.newRuntime = func(a Agent) *Child { return &Child{MarshalBin: bin, Args: args, Env: env} }
	t.Cleanup(f.Close)
	return f
}

func TestMeasureDiskSumsBothTrees(t *testing.T) {
	state := t.TempDir()
	writeSized(t, filepath.Join(state, "repos", "aaa", "pack"), 4096)
	writeSized(t, filepath.Join(state, "work", "agent1", "file"), 2048)

	got, err := measureDisk(state)
	if err != nil {
		t.Fatalf("measureDisk: %v", err)
	}
	if got.Repos < 4096 || got.Work < 2048 {
		t.Fatalf("got %+v, want at least 4096 repos and 2048 work", got)
	}
	if got.Total != got.Repos+got.Work {
		t.Fatalf("Total %d != Repos %d + Work %d", got.Total, got.Repos, got.Work)
	}
}

func TestMeasureDiskToleratesAMissingStateDir(t *testing.T) {
	got, err := measureDisk(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatalf("a fresh install must not error: %v", err)
	}
	if got.Total != 0 {
		t.Fatalf("Total = %d, want 0", got.Total)
	}
}

func TestDiskUsageIsCachedUntilInvalidated(t *testing.T) {
	f := testFleetWithState(t)
	first := f.diskUsage()

	writeSized(t, filepath.Join(f.stateDir, "repos", "bbb", "pack"), 8192)

	// Without invalidation the cached figure stands: walking multi-
	// gigabyte mirrors on every spawn would be its own problem.
	if second := f.diskUsage(); second.Total != first.Total {
		t.Fatalf("usage changed without invalidation: %d then %d", first.Total, second.Total)
	}
	f.invalidateDisk()
	if third := f.diskUsage(); third.Total <= first.Total {
		t.Fatalf("usage did not refresh after invalidation: %d", third.Total)
	}
}

// testFleetWithStateAndGate builds a fleet with a real stateDir whose
// agent child scripts the given gate result, so git-sourced spawns
// (spawnGitAgent) can run without a real container.
func testFleetWithStateAndGate(t *testing.T, gate gateResult) *Fleet {
	t.Helper()
	f := testFleetWithState(t)
	if f.git == nil {
		t.Skip("git not installed")
	}
	tr := &scriptedTransport{gate: gate}
	f.newRuntime = func(a Agent) *Child { return &Child{Transport: tr} }
	return f
}

// testFleetWithAuditAndState builds a fleet with a real stateDir and an
// audit log, for prune tests that assert the audit record.
func testFleetWithAuditAndState(t *testing.T) *Fleet {
	t.Helper()
	f := testFleetWithState(t)
	f.audit = NewAuditLog(t.TempDir())
	return f
}

func TestPruneKeepsAMirrorALiveAgentNeeds(t *testing.T) {
	f := testFleetWithStateAndGate(t, gateResult{OK: true})
	registerRepo(t, f, "r1")
	id := spawnGitAgent(t, f) // resolves to r1's mirror

	if _, err := f.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	r, _ := f.ws.Repo("r1")
	if _, err := os.Stat(mirrorDir(f.stateDir, r.URL)); err != nil {
		t.Fatalf("pruned a mirror a live agent depends on: %v", err)
	}
	if _, err := f.runtimeForAgent(id); err != nil {
		t.Fatalf("the agent did not survive a prune: %v", err)
	}
}

func TestPruneKeepsAPausedAgentsMirror(t *testing.T) {
	f := testFleetWithStateAndGate(t, gateResult{OK: true})
	registerRepo(t, f, "r1")
	id := spawnGitAgent(t, f)
	if err := f.Pause(id); err != nil {
		t.Fatal(err)
	}

	if _, err := f.Prune(); err != nil {
		t.Fatal(err)
	}
	r, _ := f.ws.Repo("r1")
	if _, err := os.Stat(mirrorDir(f.stateDir, r.URL)); err != nil {
		t.Fatal("pruned a paused agent's mirror; its work is still there")
	}
}

func TestPruneRemovesAnUnreferencedMirror(t *testing.T) {
	f := testFleetWithState(t)
	orphan := filepath.Join(f.stateDir, "repos", "deadbeefdeadbeef")
	writeSized(t, filepath.Join(orphan, "pack"), 4096)

	reclaimed, err := f.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("an unreferenced mirror survived the prune")
	}
	if reclaimed < 4096 {
		t.Fatalf("reclaimed = %d, want at least 4096", reclaimed)
	}
}

func TestPruneRemovesAWorkDirWithNoAgent(t *testing.T) {
	f := testFleetWithState(t)
	orphan := workspaceDirFor(f.stateDir, "agent-that-no-longer-exists")
	writeSized(t, filepath.Join(orphan, "file"), 2048)

	if _, err := f.Prune(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("an orphaned work directory survived the prune")
	}
}

func TestPruneIsAudited(t *testing.T) {
	f := testFleetWithAuditAndState(t)
	writeSized(t, filepath.Join(f.stateDir, "repos", "deadbeefdeadbeef", "pack"), 4096)
	if _, err := f.Prune(); err != nil {
		t.Fatal(err)
	}
	rec := findEvent(auditTail(t, f), AuditPrune)
	if rec == nil || rec.Bytes == 0 {
		t.Fatalf("prune left no audit record with a byte count: %+v", rec)
	}
}
