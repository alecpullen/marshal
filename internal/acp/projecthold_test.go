package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpclient/rpc"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/db"
	"marshal/internal/trust"
)

// fakeProjectHold records the roots it is asked to hold and counts its
// releases.
type fakeProjectHold struct {
	mu       sync.Mutex
	roots    []string
	releases int
	err      error
}

func (f *fakeProjectHold) hold(ctx context.Context, root string) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roots = append(f.roots, root)
	if f.err != nil {
		return nil, f.err
	}
	return func() {
		f.mu.Lock()
		f.releases++
		f.mu.Unlock()
	}, nil
}

// held returns the roots the fake was asked to hold.
func (f *fakeProjectHold) held() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.roots...)
}

// releaseCount returns how many of the fake's releases ran.
func (f *fakeProjectHold) releaseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.releases
}

// holdParams encodes a project/hold or project/release body for root.
func holdParams(t *testing.T, root string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(ProjectHoldParams{Root: root})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return encoded
}

// holdResult asserts res is a ProjectHoldResult and returns it.
func holdResult(t *testing.T, res any) ProjectHoldResult {
	t.Helper()
	r, ok := res.(ProjectHoldResult)
	if !ok {
		t.Fatalf("result = %T, want ProjectHoldResult", res)
	}
	return r
}

// TestAHoldIsKeptPerRoot pins decision 5: a hold is per root and idempotent.
func TestAHoldIsKeptPerRoot(t *testing.T) {
	fake := &fakeProjectHold{}
	holds := &projectHolds{hold: fake.hold, held: map[string]func(){}}
	root := t.TempDir()

	for i := 0; i < 2; i++ {
		res, err := holds.Hold(context.Background(), holdParams(t, root))
		if err != nil {
			t.Fatalf("hold %d: %v", i, err)
		}
		if got := holdResult(t, res); !got.Held || got.Root != root {
			t.Fatalf("hold %d = %+v, want held at %q", i, got, root)
		}
	}
	if roots := fake.held(); len(roots) != 1 || roots[0] != root {
		t.Fatalf("the fake held %v, want one hold on %q", roots, root)
	}

	res, err := holds.Release(context.Background(), holdParams(t, root))
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := holdResult(t, res); got.Held {
		t.Fatalf("release = %+v, want not held", got)
	}
	if n := fake.releaseCount(); n != 1 {
		t.Fatalf("the release ran %d times, want 1", n)
	}

	if _, err := holds.Release(context.Background(), holdParams(t, root)); err != nil {
		t.Fatalf("second release: %v", err)
	}
	if n := fake.releaseCount(); n != 1 {
		t.Fatalf("the second release ran the release %d times, want 1", n)
	}
}

// TestAHoldResolvesTheRepositoryRoot pins the root a hold keeps: the
// repository's, not the directory the caller named.
func TestAHoldResolvesTheRepositoryRoot(t *testing.T) {
	fake := &fakeProjectHold{}
	holds := &projectHolds{hold: fake.hold, held: map[string]func(){}}
	root := t.TempDir()
	runGit(t, root, "init")
	sub := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := holds.Hold(context.Background(), holdParams(t, sub))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if got := holdResult(t, res); !got.Held || got.Root != root {
		t.Fatalf("hold = %+v, want held at the repository root %q", got, root)
	}
	if roots := fake.held(); len(roots) != 1 || roots[0] != root {
		t.Fatalf("the fake held %v, want the repository root %q", roots, root)
	}
}

// TestAHoldRefusesARelativeOrMissingRoot pins the root checks: a relative or
// missing root is invalid params, and nothing is held.
func TestAHoldRefusesARelativeOrMissingRoot(t *testing.T) {
	fake := &fakeProjectHold{}
	holds := &projectHolds{hold: fake.hold, held: map[string]func(){}}

	for _, root := range []string{"", "relative/dir", filepath.Join(t.TempDir(), "missing")} {
		_, err := holds.Hold(context.Background(), holdParams(t, root))
		if err == nil {
			t.Fatalf("hold %q = nil error, want invalid params", root)
		}
		var rpcErr *jsonRPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != invalidParams {
			t.Fatalf("hold %q = %v, want invalid params", root, err)
		}
	}
	if roots := fake.held(); len(roots) != 0 {
		t.Fatalf("the fake held %v, want no hold", roots)
	}
}

// TestAFailedHoldKeepsNothing pins the failure path: the hold's error is the
// call's, and nothing is kept to release.
func TestAFailedHoldKeepsNothing(t *testing.T) {
	boom := errors.New("hold failed")
	fake := &fakeProjectHold{err: boom}
	holds := &projectHolds{hold: fake.hold, held: map[string]func(){}}
	root := t.TempDir()

	if _, err := holds.Hold(context.Background(), holdParams(t, root)); !errors.Is(err, boom) {
		t.Fatalf("hold = %v, want the fake's error", err)
	}
	if _, err := holds.Release(context.Background(), holdParams(t, root)); err != nil {
		t.Fatalf("release: %v", err)
	}
	if n := fake.releaseCount(); n != 0 {
		t.Fatalf("the release ran %d times, want 0", n)
	}
}

// TestClosingTheHostReleasesItsHolds pins decision 5's other half: closing the
// host drops every hold it kept.
func TestClosingTheHostReleasesItsHolds(t *testing.T) {
	fake := &fakeProjectHold{}
	cfg := testRunConfig(t)
	cfg.hold = fake.hold
	host, err := newAgentHost(cfg)
	if err != nil {
		t.Fatalf("newAgentHost: %v", err)
	}
	roots := []string{t.TempDir(), t.TempDir()}
	for _, root := range roots {
		if _, err := host.holds.Hold(context.Background(), holdParams(t, root)); err != nil {
			t.Fatalf("hold %q: %v", root, err)
		}
	}

	if err := host.close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if n := fake.releaseCount(); n != len(roots) {
		t.Fatalf("close ran %d releases, want %d", n, len(roots))
	}
}

// TestAViewerCannotHold pins decision 8's refusal: a viewer connection cannot
// hold or release a project, as it cannot start or load a session.
func TestAViewerCannotHold(t *testing.T) {
	h := newViewerHarness(t)
	v := h.openRawViewer(t)
	v.initialize(t, true)

	for _, method := range []string{"project/hold", "project/release"} {
		var res json.RawMessage
		err := v.conn.Call(context.Background(), method, map[string]any{"root": t.TempDir()}, &res)
		if err == nil || !strings.Contains(err.Error(), "a viewer connection cannot start or load a session") {
			t.Fatalf("%s = %v, want the viewer's lifecycle refusal", method, err)
		}
	}
}

// TestOnlyAViewersChildServesTheHold pins decision 8: a host built without a
// hold answers project/hold as a method that does not exist.
func TestOnlyAViewersChildServesTheHold(t *testing.T) {
	client, _ := startAgentHost(t)
	conn := rpc.NewConn(client, rpc.Handlers{})

	var res json.RawMessage
	err := conn.Call(context.Background(), "project/hold", map[string]any{"root": t.TempDir()}, &res)
	if err == nil || !strings.Contains(err.Error(), "method not found") {
		t.Fatalf("project/hold on a host without a hold = %v, want method not found", err)
	}
}

// TestTheChildsHoldIndexesItsProject pins the child's hold end to end: the
// config loads by the headless rule with no project config, the project host
// opens, and its index fills the project's file index.
func TestTheChildsHoldIndexesItsProject(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	runGit(t, root, "init")
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hosts := app.NewHosts()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hosts.Close(ctx)
	}()

	store := trust.NewStore(config.DataDir(home))
	hold := hostsHold(hosts, home, trust.NewHeadlessResolver(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	release, err := hold(context.Background(), root)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	defer release()

	database, err := db.Open(db.Path(root))
	if err != nil {
		t.Fatalf("open the project database: %v", err)
	}
	defer database.Close()

	deadline := time.Now().Add(30 * time.Second)
	for {
		project, err := database.GetProjectByRoot(root)
		if err == nil {
			if files, ferr := database.GetFileIndex(project.ID, 0); ferr == nil && len(files) > 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the project's file index stayed empty")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
