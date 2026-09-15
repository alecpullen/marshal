package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// provisionFleet builds a fleet with a real state directory, a real git
// runner, and a scripted agent child, for tests that interleave spawns
// with prunes. The limits are the caller's: the self-prune test needs a
// disk budget, the rest take none.
func provisionFleet(t *testing.T, limits Limits) *Fleet {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "unused", nil, t.TempDir(), limits, "", nil, "marshal-state")
	f.newRuntime = func(a Agent) (*Child, error) {
		return &Child{Transport: &scriptedTransport{gate: gateResult{OK: true}}}, nil
	}
	t.Cleanup(f.Close)
	if f.git == nil {
		t.Skip("git not installed")
	}
	return f
}

// TestSpawnSurvivesAPruneDuringTreePreparation pins the first half of
// the provisioning window: between EnsureMirrorCapped creating the
// mirror and PutAgent recording the agent, a prune fired from the
// outside — the HTTP endpoint, or another spawn's enforceDisk — must
// not delete the mirror this spawn is cloning from.
//
// The mirror-clone moment is pinned too: when the mirror itself is
// being written, the in-flight marker must already be registered —
// that ordering is what keeps a prune fired at the mirror from
// deleting it — and no work dir may exist yet to orphan. The prune
// proper is fired from the git seam at the exact moment PrepareTree
// starts its clone, and the clone then runs for real, so a prune that
// took the mirror fails the clone outright rather than leaving a
// broken tree.
func TestSpawnSurvivesAPruneDuringTreePreparation(t *testing.T) {
	f := provisionFleet(t, Limits{})
	registerGitRepo(t, f, "r1")
	r, _ := f.ws.Repo("r1")
	mirror := mirrorDir(f.stateDir, r.URL)

	bin := f.git.bin
	fired := false
	mirrorCloned := false
	f.git.exec = func(dir string, env []string, args ...string) ([]byte, error) {
		// args arrives pre-prefixed with hardenedGitArgs; the caller's
		// own arguments start after the two -c pairs.
		real := args[4:]
		if len(real) == 4 && real[0] == "clone" && real[1] == "--mirror" {
			// The mirror is being written right now: the in-flight
			// marker must already be registered, or a prune fired at
			// this exact moment deletes it.
			mirrorCloned = true
			snap := f.provisioningSnapshot()
			if len(snap) != 1 {
				t.Errorf("mirror clone without exactly one in-flight marker: %d registered", len(snap))
			}
			for id, m := range snap {
				if m != mirror {
					t.Errorf("in-flight marker registered the wrong mirror: got %q, want %q", m, mirror)
				}
				if _, err := os.Stat(workspaceDirFor(f.stateDir, id)); err == nil {
					t.Errorf("work dir exists before PrepareTree for id %s", id)
				}
			}
		}
		if len(real) == 3 && real[0] == "clone" {
			fired = true
			if _, err := f.Prune(); err != nil {
				return nil, fmt.Errorf("prune fired mid-preparation: %w", err)
			}
		}
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			return out, fmt.Errorf("git %s: %w", strings.Join(real, " "), err)
		}
		return out, nil
	}

	id, err := f.Spawn(context.Background(), "", SpawnOptions{RepoID: "r1", Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !fired {
		t.Fatal("the prune never fired; the test exercised nothing")
	}
	if !mirrorCloned {
		t.Fatal("the mirror clone never ran; the marker ordering went unpinned")
	}
	if _, err := os.Stat(mirror); err != nil {
		t.Fatalf("a prune deleted the mirror mid-preparation: %v", err)
	}
	if _, err := os.Stat(workspaceDirFor(f.stateDir, id)); err != nil {
		t.Fatalf("the working tree did not survive the prune: %v", err)
	}
}

// TestSpawnSurvivesAPruneAfterTreePreparation pins the second half of
// the window: the tree exists, the runtime is about to start, and the
// record still has not landed. A prune here deletes the tree of a spawn
// that is about to run in it — and the mirror with it. The prune is
// fired from the newRuntime seam, which is exactly where an outside
// caller would see the state: on disk, explained by no record.
func TestSpawnSurvivesAPruneAfterTreePreparation(t *testing.T) {
	f := provisionFleet(t, Limits{})
	registerGitRepo(t, f, "r1")
	r, _ := f.ws.Repo("r1")
	mirror := mirrorDir(f.stateDir, r.URL)

	f.newRuntime = func(a Agent) (*Child, error) {
		if _, err := f.Prune(); err != nil {
			return nil, fmt.Errorf("prune fired before the runtime: %w", err)
		}
		return &Child{Transport: &scriptedTransport{gate: gateResult{OK: true}}}, nil
	}

	id, err := f.Spawn(context.Background(), "", SpawnOptions{RepoID: "r1", Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, err := os.Stat(mirror); err != nil {
		t.Fatalf("a prune deleted the mirror of an in-flight spawn: %v", err)
	}
	if _, err := os.Stat(workspaceDirFor(f.stateDir, id)); err != nil {
		t.Fatalf("a prune deleted the working tree of an in-flight spawn: %v", err)
	}
}

// TestSpawnsOwnEnforceDiskPruneKeepsItsFreshState reproduces the
// single-threaded variant of the bug: a spawn over budget prunes via
// enforceDisk after preparing its own tree, and that prune deletes the
// very state the spawn just created. The scripted child never touches
// the filesystem, which is why the existing over-budget test passed
// through the deletion unnoticed.
func TestSpawnsOwnEnforceDiskPruneKeepsItsFreshState(t *testing.T) {
	f := provisionFleet(t, Limits{MaxDiskMB: 1})
	registerGitRepo(t, f, "r1")
	r, _ := f.ws.Repo("r1")
	mirror := mirrorDir(f.stateDir, r.URL)

	// Over budget, with the excess entirely reclaimable: the orphan is
	// what the prune should take, never the fresh state.
	writeSized(t, filepath.Join(f.stateDir, "repos", "orphan-garbage", "pack"), 4<<20)
	f.invalidateDisk()

	id, err := f.Spawn(context.Background(), "", SpawnOptions{RepoID: "r1", Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, err := os.Stat(mirror); err != nil {
		t.Fatalf("the spawn's own prune deleted its mirror: %v", err)
	}
	if _, err := os.Stat(workspaceDirFor(f.stateDir, id)); err != nil {
		t.Fatalf("the spawn's own prune deleted its fresh working tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.stateDir, "repos", "orphan-garbage")); !os.IsNotExist(err) {
		t.Fatalf("the prune kept the orphan garbage it exists to reclaim: %v", err)
	}
}

// TestConcurrentSpawnsSurviveConcurrentPrunes exercises the whole
// design under contention: several spawns provision while prunes
// hammer the same trees. Everything is guarded by leaf locks —
// provMu for the in-flight set, pruneMu for prune callers — so the
// only acceptable outcome is every spawn succeeding with every tree
// and mirror intact. The counts are small; each spawn clones a real
// repo and the suite must stay fast.
func TestConcurrentSpawnsSurviveConcurrentPrunes(t *testing.T) {
	const spawns, prunes = 4, 8
	f := provisionFleet(t, Limits{})
	for i := 0; i < spawns; i++ {
		// One repo per spawn: each spawn provisions its own mirror and
		// tree, so a prune sweep sees the maximum number of
		// unreferenced-looking paths, while the shared repos/ and work/
		// parents stay under real contention.
		registerGitRepo(t, f, fmt.Sprintf("r%d", i))
	}

	ids := make([]string, spawns)
	var wg sync.WaitGroup
	for i := 0; i < spawns; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := f.Spawn(context.Background(), "", SpawnOptions{
				RepoID: fmt.Sprintf("r%d", i), Prompt: "x",
			})
			if err != nil {
				t.Errorf("spawn %d: %v", i, err)
				return
			}
			ids[i] = id
		}(i)
	}
	for i := 0; i < prunes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.Prune(); err != nil {
				t.Errorf("concurrent prune: %v", err)
			}
		}()
	}
	wg.Wait()

	// Nothing running was harmed to make room.
	for i, id := range ids {
		if id == "" {
			continue // a failed spawn reported its own error above
		}
		if _, err := os.Stat(workspaceDirFor(f.stateDir, id)); err != nil {
			t.Errorf("spawn %d's tree did not survive the hammering: %v", i, err)
		}
		repo, _ := f.ws.Repo(fmt.Sprintf("r%d", i))
		if _, err := os.Stat(mirrorDir(f.stateDir, repo.URL)); err != nil {
			t.Errorf("spawn %d's mirror did not survive the hammering: %v", i, err)
		}
	}
	// A leaked marker would protect a dead spawn's tree forever.
	if got := f.provisioningSnapshot(); len(got) != 0 {
		t.Fatalf("provisioning map leaked %d entries after every spawn returned", len(got))
	}
}

// TestFailedSpawnLeavesNoProvisioningResidue pins the failure-path
// hygiene of the marker: a spawn that dies mid-window — here when
// session/new fails inside the runtime — must clear its marker, leaving
// no ghost protection over state nothing references. The spawn's own
// error path removes the tree (stopAgent), so the residue to check is
// the mirror: created, never recorded, and reclaimable.
func TestFailedSpawnLeavesNoProvisioningResidue(t *testing.T) {
	f := provisionFleet(t, Limits{})
	registerGitRepo(t, f, "r1")
	r, _ := f.ws.Repo("r1")

	// A transport that answers everything but session/new, which it
	// fails: the spawn reaches the runtime, then gives up mid-window.
	f.newRuntime = func(a Agent) (*Child, error) {
		return &Child{Transport: &refusingTransport{}}, nil
	}
	if _, err := f.Spawn(context.Background(), "", SpawnOptions{RepoID: "r1", Prompt: "x"}); err == nil {
		t.Fatal("a spawn with a failing session/new succeeded")
	}
	if got := f.provisioningSnapshot(); len(got) != 0 {
		t.Fatalf("a failed spawn leaked %d in-flight markers; every early return must clear its own", len(got))
	}
	// The mirror is now state nothing references — the failed spawn's
	// record never landed — so a prune must be able to reclaim it.
	if _, err := f.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Stat(mirrorDir(f.stateDir, r.URL)); !os.IsNotExist(err) {
		t.Fatalf("a failed spawn's mirror became unprunable: %v", err)
	}
}

// TestPanickedSpawnClearsItsProvisioningMarker pins the defer property
// directly: a panic anywhere in Spawn — the runtime factory is the
// convenient seam — must not leave a marker protecting state nothing
// references. A panic also skips the explicit RemoveTree calls, so the
// half-created tree genuinely survives the spawn — and must be
// reclaimable by the next prune, exactly as it would be after a bridge
// crash, where the map dies with the process.
func TestPanickedSpawnClearsItsProvisioningMarker(t *testing.T) {
	f := provisionFleet(t, Limits{})
	registerGitRepo(t, f, "r1")

	var tree string
	f.newRuntime = func(a Agent) (*Child, error) {
		// Capture the id here rather than from Spawn's return: a panic
		// unwinds Spawn, so its named results never materialise.
		tree = workspaceDirFor(f.stateDir, a.ID)
		panic("boom: runtime factory exploded")
	}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("the runtime factory did not panic; the test exercised nothing")
			}
		}()
		_, _ = f.Spawn(context.Background(), "", SpawnOptions{RepoID: "r1", Prompt: "x"})
	}()
	if tree == "" {
		t.Fatal("the panicked spawn minted no agent id; the test exercised nothing")
	}
	if got := f.provisioningSnapshot(); len(got) != 0 {
		t.Fatalf("a panicked spawn leaked %d in-flight markers; the defer must cover panics too", len(got))
	}
	// The half-created tree survived the panic, orphaned.
	if _, err := os.Stat(tree); err != nil {
		t.Fatalf("the panicked spawn left no tree to reclaim: %v", err)
	}
	if _, err := f.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatalf("the half-created tree of a panicked spawn is not reclaimable: %v", err)
	}
}

// refusingTransport is a scriptedTransport that fails session/new so a
// test can drive a spawn to a specific mid-window failure. Everything
// else answers as usual: the spawn must get far enough to hold state.
type refusingTransport struct{}

func (t *refusingTransport) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	go t.serve(stdinR, stdoutW)
	return stdinW, stdoutR, io.NopCloser(strings.NewReader("")), nil
}

func (t *refusingTransport) serve(r io.Reader, w io.WriteCloser) {
	defer w.Close()
	sc := bufio.NewScanner(r)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		var result any
		if req.Method == "session/new" {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32000, "message": "refused for the test"}})
			continue
		}
		result = map[string]any{}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}

func (t *refusingTransport) Wait() error                { return nil }
func (t *refusingTransport) Signal(sig os.Signal) error { return nil }
func (t *refusingTransport) Kill() error                { return nil }
func (t *refusingTransport) Detach() error              { return nil }
