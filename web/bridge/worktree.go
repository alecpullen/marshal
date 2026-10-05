package bridge

import (
	"fmt"
	"os"
	"path/filepath"
)

// workspaceDirFor is the host directory bind-mounted at /work for one agent.
func workspaceDirFor(stateDir, agentID string) string {
	return filepath.Join(stateDir, "work", agentID)
}

// PrepareTree creates one agent's working tree from the shared mirror
// and returns its path.
//
// The clone source is the LOCAL MIRROR, never the remote URL. Two
// reasons: on the same filesystem git hardlinks the object store, so the
// second agent on a repo is nearly free; and a credential-bearing URL is
// never written into .git/config, which lives inside the agent's
// workspace where the agent can read it.
//
// The remote is repointed at the real URL afterwards so that S2b can push
// without the agent tree depending on a server-internal path.
//
// PrepareTree takes no credential: its only remote is the local mirror,
// which needs none. Authentication happens in EnsureMirror.
func (g *gitRunner) PrepareTree(stateDir, agentID, mirror, url, ref string) (string, error) {
	dir := workspaceDirFor(stateDir, agentID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", fmt.Errorf("create workspace parent: %w", err)
	}
	// A leftover tree from a previous generation would be checked out at
	// the wrong ref and silently reused.
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("clear stale workspace: %w", err)
	}
	return g.cloneTree(dir, mirror, url, ref, false)
}

// PrepareTreeIn is PrepareTree into a directory a running container has
// already mounted, as a warm-pool container has. The directory itself is
// kept (removing it would orphan the mount) and only its contents are
// replaced.
func (g *gitRunner) PrepareTreeIn(dir, mirror, url, ref string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read workspace: %w", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return "", fmt.Errorf("clear stale workspace: %w", err)
		}
	}
	return g.cloneTree(dir, mirror, url, ref, true)
}

// cloneTree clones the mirror into an empty dir and checks out ref. It
// removes dir's contents on failure, and dir itself unless keepDir.
func (g *gitRunner) cloneTree(dir, mirror, url, ref string, keepDir bool) (string, error) {
	clean := func() {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				_ = os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
		if !keepDir {
			_ = os.Remove(dir)
		}
	}
	// No credential needed: the source is a local path.
	if _, err := g.run("", Credential{Kind: "none"}, "clone", mirror, dir); err != nil {
		clean()
		return "", fmt.Errorf("create workspace: %w", err)
	}
	if _, err := g.run(dir, Credential{Kind: "none"}, "remote", "set-url", "origin", url); err != nil {
		clean()
		return "", fmt.Errorf("repoint origin: %w", err)
	}
	if ref != "" {
		if _, err := g.run(dir, Credential{Kind: "none"}, "checkout", ref); err != nil {
			clean()
			return "", fmt.Errorf("checkout %s: %w", ref, err)
		}
	}
	return dir, nil
}

// RemoveTree deletes one agent's working tree. The shared mirror is left
// alone — other agents depend on it.
func (g *gitRunner) RemoveTree(stateDir, agentID string) error {
	return os.RemoveAll(workspaceDirFor(stateDir, agentID))
}
