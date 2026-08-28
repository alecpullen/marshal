package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// mirrorDir is the on-disk location of a repo's bare mirror.
//
// The URL is hashed rather than sanitised: a private repository's name
// should not be readable from a directory listing, and hashing sidesteps
// every path-traversal question a URL-derived name would raise.
func mirrorDir(stateDir, url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(stateDir, "repos", hex.EncodeToString(sum[:])[:16])
}

// EnsureMirror creates the repo's bare mirror if absent, or refreshes it
// if present, and returns its path. One mirror per URL is shared by every
// agent working on that repo, so only the first clone pays full cost.
func (g *gitRunner) EnsureMirror(stateDir, url string, cred Credential) (string, error) {
	dir := mirrorDir(stateDir, url)

	// Serialise concurrent calls for the same URL so two agents spawning
	// against one repo do not race on the shared bare mirror.
	mu := g.mirrorMutex(dir)
	mu.Lock()
	defer mu.Unlock()

	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		// --prune so deleted upstream branches do not linger forever.
		if _, err := g.run(dir, cred, "fetch", "--prune"); err != nil {
			return "", fmt.Errorf("refresh mirror: %w", err)
		}
		return dir, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat mirror: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", fmt.Errorf("create mirror parent: %w", err)
	}
	if _, err := g.run("", cred, "clone", "--mirror", url, dir); err != nil {
		// A half-written mirror would be treated as valid next time.
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("create mirror: %w", err)
	}
	return dir, nil
}

// EnsureMirrorCapped is EnsureMirror with a clone size cap. While the
// mirror is being cloned, the on-disk size is sampled on a ticker; if it
// exceeds maxBytes the clone is cancelled, the partial mirror removed,
// and an error returned. A maxBytes of 0 means unbounded (no watcher).
// An existing mirror is refreshed with a fetch and never size-checked.
func (g *gitRunner) EnsureMirrorCapped(ctx context.Context, stateDir, url string, cred Credential, maxBytes int64) (string, error) {
	dir := mirrorDir(stateDir, url)

	// Serialise concurrent calls for the same URL so two agents spawning
	// against one repo do not race on the shared bare mirror.
	mu := g.mirrorMutex(dir)
	mu.Lock()
	defer mu.Unlock()

	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		// --prune so deleted upstream branches do not linger forever.
		if _, err := g.runCtx(ctx, dir, cred, "fetch", "--prune"); err != nil {
			return "", fmt.Errorf("refresh mirror: %w", err)
		}
		return dir, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat mirror: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", fmt.Errorf("create mirror parent: %w", err)
	}

	cloneCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := g.runCtx(cloneCtx, "", cred, "clone", "--mirror", url, dir)
		done <- err
	}()

	if maxBytes <= 0 {
		// No cap — wait for the clone to finish.
		if err := <-done; err != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("create mirror: %w", err)
		}
		return dir, nil
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				_ = os.RemoveAll(dir)
				return "", fmt.Errorf("create mirror: %w", err)
			}
			return dir, nil
		case <-ticker.C:
			if size, serr := sumTree(dir); serr == nil && size > maxBytes {
				cancel()
				<-done // wait for the clone to actually stop
				_ = os.RemoveAll(dir)
				return "", fmt.Errorf("clone exceeds %d byte cap", maxBytes)
			}
		}
	}
}

// mirrorHead reports the branch a mirror's HEAD points at, used as the
// default ref when a spawn names no explicit one.
func (g *gitRunner) mirrorHead(mirror string) (string, error) {
	out, err := g.run(mirror, Credential{Kind: "none"},
		"symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve mirror HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
