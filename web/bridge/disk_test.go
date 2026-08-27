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
	f := NewFleet(ws, "unused", nil, stateDir)
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
