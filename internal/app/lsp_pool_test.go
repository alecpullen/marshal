package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/logging"
	"marshal/internal/lsp"
)

// absentServer is a server set whose command does not exist: the manager
// still exists and is pooled, its one language just never comes up.
func absentServer(t *testing.T, args ...string) map[string]lsp.ServerSpec {
	t.Helper()
	return map[string]lsp.ServerSpec{"go": {Command: filepath.Join(t.TempDir(), "no-such-server"), Args: args}}
}

func testLSPPool(t *testing.T, linger time.Duration) *lspPool {
	t.Helper()
	p := newLSPPool(context.Background(), logging.New(io.Discard, slog.LevelInfo, false), linger)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.close(ctx)
	})
	return p
}

func waitForStoppedManager(t *testing.T, p *lspPool, root string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for p.running(root) != nil {
		if time.Now().After(deadline) {
			t.Fatalf("manager for %s still pooled after the linger", root)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLSPPoolSharesAManagerPerRootAndServerSet(t *testing.T) {
	p := testLSPPool(t, time.Minute)
	servers := absentServer(t)

	a := p.acquire("/repo", servers)
	b := p.acquire("/repo", servers)
	if a.manager() == nil || a.manager() != b.manager() {
		t.Fatalf("two leases on one root and server set got managers %p and %p; want one shared manager", a.manager(), b.manager())
	}
	if other := p.acquire("/repo/.marshal/worktrees/x", servers); other.manager() == a.manager() {
		t.Fatal("a different root shared the root's manager")
	}
	changed := servers["go"]
	changed.Args = []string{"-remote=auto"}
	if diff := p.acquire("/repo", map[string]lsp.ServerSpec{"go": changed}); diff.manager() == a.manager() {
		t.Fatal("a different server set shared the manager")
	}
	if p.running("/repo") == nil {
		t.Fatal("running did not report the root's manager")
	}
}

func TestLSPPoolKeepsAReleasedManagerThroughTheLinger(t *testing.T) {
	p := testLSPPool(t, 100*time.Millisecond)
	servers := absentServer(t)

	first := p.acquire("/repo", servers)
	m := first.manager()
	first.release()
	first.release() // only the first release counts

	again := p.acquire("/repo", servers)
	if again.manager() != m {
		t.Fatal("an acquire inside the linger started a new manager")
	}
	again.release()
	waitForStoppedManager(t, p, "/repo")

	if fresh := p.acquire("/repo", servers); fresh.manager() == m {
		t.Fatal("an acquire after the linger reused the stopped manager")
	}
}

func TestLSPPoolWithoutLingerStopsOnLastRelease(t *testing.T) {
	p := testLSPPool(t, 0)
	servers := absentServer(t)
	a := p.acquire("/repo", servers)
	b := p.acquire("/repo", servers)
	a.release()
	if p.running("/repo") == nil {
		t.Fatal("the manager stopped while a lease was still held")
	}
	b.release()
	if p.running("/repo") != nil {
		t.Fatal("the last release did not stop the manager")
	}
}

func TestLSPPoolWithoutServersLeasesNothing(t *testing.T) {
	p := testLSPPool(t, time.Minute)
	lease := p.acquire("/repo", nil)
	if lease != nil || lease.manager() != nil {
		t.Fatal("an empty server set produced a lease")
	}
	lease.release() // a nil lease is safe to release
}

func TestLSPPoolCloseStopsEveryManager(t *testing.T) {
	p := newLSPPool(context.Background(), logging.New(io.Discard, slog.LevelInfo, false), time.Minute)
	servers := absentServer(t)
	p.acquire("/a", servers)
	released := p.acquire("/b", servers)
	released.release() // lingering, not yet stopped

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if p.running("/a") != nil || p.running("/b") != nil {
		t.Fatal("close left a manager pooled")
	}
	if p.acquire("/a", servers) != nil {
		t.Fatal("acquire after close returned a lease")
	}
}

func TestLSPServersForFollowsConfigAndRootLanguages(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.LSP.Servers = map[string]config.LSPServerConfig{"go": {Command: "/opt/gopls-test"}}

	got := lspServersFor(cfg, root)
	if got["go"].Command != "/opt/gopls-test" {
		t.Fatalf("lspServersFor = %v, want the configured go server", got)
	}
	if bare := lspServersFor(cfg, t.TempDir()); bare != nil {
		t.Fatalf("lspServersFor on a root with no Go files = %v, want nil", bare)
	}
	off := false
	cfg.LSP.Enabled = &off
	if disabled := lspServersFor(cfg, root); disabled != nil {
		t.Fatalf("lspServersFor with LSP disabled = %v, want nil", disabled)
	}
}
