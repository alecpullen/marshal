package bridge

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareTreeChecksOutTheRef(t *testing.T) {
	origin := newBareRepoFixture(t)
	state := t.TempDir()
	g := testGitRunner(t)

	mirror, err := g.EnsureMirror(state, origin, Credential{Kind: "none"})
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	dir, err := g.PrepareTree(state, "agent1", mirror, origin, "main")
	if err != nil {
		t.Fatalf("PrepareTree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("working tree was not checked out: %v", err)
	}
}

// TestPrepareTreeLeaksNoCredential is the regression test for the whole
// bridge-mediated model: if a secret reaches .git/config, the agent can
// read it and the isolation is worthless.
func TestPrepareTreeLeaksNoCredential(t *testing.T) {
	origin := newBareRepoFixture(t)
	state := t.TempDir()
	g := testGitRunner(t)

	mirror, err := g.EnsureMirror(state, origin, Credential{Kind: "none"})
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	dir, err := g.PrepareTree(state, "agent1", mirror, origin, "main")
	if err != nil {
		t.Fatalf("PrepareTree: %v", err)
	}

	err = filepath.WalkDir(filepath.Join(dir, ".git"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil // unreadable objects are not a leak vector
		}
		if bytes.Contains(body, []byte("sk-super-secret")) {
			return fmt.Errorf("credential leaked into %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk .git: %v", err)
	}
}

func TestPrepareTreePointsOriginAtTheRealURL(t *testing.T) {
	origin := newBareRepoFixture(t)
	state := t.TempDir()
	g := testGitRunner(t)

	mirror, _ := g.EnsureMirror(state, origin, Credential{Kind: "none"})
	dir, err := g.PrepareTree(state, "agent1", mirror, "https://example.test/r.git", "main")
	if err != nil {
		t.Fatalf("PrepareTree: %v", err)
	}

	cfg, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "https://example.test/r.git") {
		t.Errorf("origin was not repointed at the real URL:\n%s", cfg)
	}
	if strings.Contains(string(cfg), mirror) {
		t.Errorf("the mirror path is still the remote; the agent tree depends on server internals:\n%s", cfg)
	}
}

// TestBridgeGitDoesNotRunAgentHooks proves the container-escape mitigation.
func TestBridgeGitDoesNotRunAgentHooks(t *testing.T) {
	origin := newBareRepoFixture(t)
	state := t.TempDir()
	g := testGitRunner(t)

	mirror, _ := g.EnsureMirror(state, origin, Credential{Kind: "none"})
	dir, err := g.PrepareTree(state, "agent1", mirror, origin, "main")
	if err != nil {
		t.Fatalf("PrepareTree: %v", err)
	}

	// The agent controls .git/hooks. Plant one and prove it never runs.
	sentinel := filepath.Join(t.TempDir(), "pwned")
	hook := filepath.Join(dir, ".git", "hooks", "post-checkout")
	script := "#!/bin/sh\ntouch " + sentinel + "\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := g.run(dir, Credential{Kind: "none"}, "checkout", "main"); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("an agent-authored git hook executed on the host")
	}
}
