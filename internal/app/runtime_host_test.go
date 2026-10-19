package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/lsp"
	"marshal/internal/trust"
)

// hostedProjectConfig is a minimal trusted project config with a mock
// provider and a language server for shell scripts. The server is a stub
// that exits at once: its manager is still created and pooled, which is all
// these tests compare.
const hostedProjectConfig = `[project]
name = "hosted"

[profile]
default = "mock_profile"

[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"

[models.presets."mock/mock-model"]
local_only = true

[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"

[lsp]
enabled = true

[lsp.servers.shell]
command = "STUB"
`

// hostedProject writes a git repository whose root holds the project config
// and an LSP stub script (which also gives the root its shell language).
func hostedProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(root, "lsp-stub.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := []byte(strings.ReplaceAll(hostedProjectConfig, "STUB", stub))
	if err := os.WriteFile(filepath.Join(root, ".marshal", "config.toml"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// startOn starts a runtime in dir on hs (nil for a private registry) and
// closes it when the test ends.
func startOn(t *testing.T, hs *Hosts, home, dir string) *Runtime {
	t.Helper()
	opts := []Option{
		WithWorkingDir(dir),
		WithHomeDir(home),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
	}
	if hs != nil {
		opts = append(opts, WithHosts(hs))
	}
	rt, err := StartRuntime(context.Background(), opts...)
	if err != nil {
		t.Fatalf("StartRuntime(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	return rt
}

func waitForManager(t *testing.T, rt *Runtime, want func(*lsp.Manager) bool, what string) *lsp.Manager {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if m := rt.LSPManager.Get(); want(m) {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRuntimesOnOneRootShareTheProjectHost(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	home := t.TempDir()
	root, other := hostedProject(t), hostedProject(t)
	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	a := startOn(t, hs, home, root)
	b := startOn(t, hs, home, sub)
	if a.Host == nil || a.Host != b.Host {
		t.Fatal("runtimes on one root (one from a subdirectory) got different hosts")
	}
	if c := startOn(t, hs, home, other); c.Host == a.Host {
		t.Fatal("a runtime on another root shared the host")
	}
	if p, q := startOn(t, nil, home, root), startOn(t, nil, home, root); p.Host == q.Host || p.Host == a.Host {
		t.Fatal("runtimes without WithHosts shared a host")
	}
}

func TestSecondRuntimeOnAHostKeepsTheFirstSessionsGenerationOpen(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	home := t.TempDir()
	root := hostedProject(t)

	first := startOn(t, hs, home, root)
	database := must[*db.DB](first.DB)
	gen := db.Generation{ID: first.SessionID + "-live", SessionID: first.SessionID, Seq: 99, StartedAt: time.Now()}
	if err := database.BeginGeneration(gen); err != nil {
		t.Fatal(err)
	}
	isOpen := func() bool {
		gens, err := database.GenerationsForSession(first.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range gens {
			if g.ID == gen.ID {
				return g.EndedAt == nil
			}
		}
		t.Fatal("live generation missing")
		return false
	}

	startOn(t, hs, home, root)
	if !isOpen() {
		t.Fatal("starting a second session on the host ended the first session's live generation")
	}
	// A runtime with a private registry still reconciles on start, as every
	// runtime did before project hosts.
	startOn(t, nil, home, root)
	if isOpen() {
		t.Fatal("a private-registry start did not reconcile open generations")
	}
}

func TestRuntimesShareLanguageServersByActiveRoot(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	home := t.TempDir()
	root := hostedProject(t)
	a := startOn(t, hs, home, root)
	b := startOn(t, hs, home, root)

	shared := a.LSPManager.Get()
	if shared == nil || b.LSPManager.Get() != shared {
		t.Fatalf("runtimes on one root got managers %p and %p; want one shared manager", shared, b.LSPManager.Get())
	}

	worktree := filepath.Join(root, ".marshal", "worktrees", "feature")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "run.sh"), []byte("echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.State.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: worktree, Branch: "feature"})
	inWorktree := waitForManager(t, a, func(m *lsp.Manager) bool { return m != nil && m != shared }, "the worktree's manager")
	if inWorktree.Root() != worktree {
		t.Fatalf("worktree manager root = %q, want %q", inWorktree.Root(), worktree)
	}
	if b.LSPManager.Get() != shared {
		t.Fatal("one session entering a worktree moved the other session's servers")
	}

	a.State.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: root})
	waitForManager(t, a, func(m *lsp.Manager) bool { return m == shared }, "the shared root manager again")
}

func TestReloadKeepsLanguageServersUnlessTheServerSetChanges(t *testing.T) {
	// A private registry has no linger, so it also checks that a rebind takes
	// the new manager before it releases the old one.
	for _, shared := range []bool{true, false} {
		t.Run(fmt.Sprintf("shared=%t", shared), func(t *testing.T) {
			var hs *Hosts
			if shared {
				hs = testHosts(t, false, time.Minute)
			}
			rt := startOn(t, hs, t.TempDir(), hostedProject(t))
			handle := rt.LSPManager
			before := handle.Get()
			if before == nil {
				t.Fatal("no manager bound at start")
			}

			if err := reloadAgentRuntime(context.Background(), rt.Config, rt); err != nil {
				t.Fatalf("reload: %v", err)
			}
			if rt.LSPManager != handle {
				t.Fatal("a reload replaced the runtime's LSP handle")
			}
			if rt.LSPManager.Get() != before {
				t.Fatal("a reload with an unchanged server set restarted the language servers")
			}

			changed := rt.Config
			spec := changed.LSP.Servers["shell"]
			spec.Args = []string{"--stdio"}
			changed.LSP.Servers = map[string]config.LSPServerConfig{"shell": spec}
			if err := reloadAgentRuntime(context.Background(), changed, rt); err != nil {
				t.Fatalf("reload with a changed server: %v", err)
			}
			if after := rt.LSPManager.Get(); after == nil || after == before {
				t.Fatal("a reload that changed the server set kept the old manager")
			}
		})
	}
}

func TestNewSessionKeepsTheLSPHandleAndHost(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	rt := startOn(t, hs, t.TempDir(), hostedProject(t))
	handle, host, before := rt.LSPManager, rt.Host, rt.LSPManager.Get()

	if _, _, _, _, _, _, _, err := rt.NewSession("next"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if rt.LSPManager != handle || rt.Host != host {
		t.Fatal("NewSession replaced the runtime's LSP handle or host")
	}
	if rt.LSPManager.Get() != before {
		t.Fatal("NewSession restarted the root's language servers")
	}
}

func TestCloseReleasesThePrivateHost(t *testing.T) {
	root := hostedProject(t)
	rt, err := StartRuntime(context.Background(), WithWorkingDir(root), WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatal(err)
	}
	host := rt.Host
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !hostClosed(host) {
		t.Fatal("closing a runtime left its private host open")
	}
	if rt.LSPManager.Get() != nil {
		t.Fatal("Close left the LSP handle bound")
	}
}

// A WorkspaceEvent bind still in flight when Close runs must not rebind: on a
// lingering host it would hold a pooled manager nobody releases.
func TestBindAfterCloseTakesNoLease(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	root := hostedProject(t)
	rt := startOn(t, hs, t.TempDir(), root)
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rt.bindLSP(rt.Config, root)
	if rt.LSPManager.Get() != nil || rt.lspLease != nil {
		t.Fatal("a bind after Close took a language-server lease")
	}
}
