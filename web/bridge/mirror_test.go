package bridge

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureMirrorClonesThenFetches(t *testing.T) {
	origin := newBareRepoFixture(t)
	state := t.TempDir()
	g := testGitRunner(t)

	first, err := g.EnsureMirror(state, origin, Credential{Kind: "none"})
	if err != nil {
		t.Fatalf("EnsureMirror (clone): %v", err)
	}
	if _, err := os.Stat(filepath.Join(first, "HEAD")); err != nil {
		t.Fatalf("mirror is not a bare repo: %v", err)
	}

	// A second call must reuse the same mirror, not re-clone.
	second, err := g.EnsureMirror(state, origin, Credential{Kind: "none"})
	if err != nil {
		t.Fatalf("EnsureMirror (fetch): %v", err)
	}
	if first != second {
		t.Fatalf("mirror path changed between calls: %q then %q", first, second)
	}
}

func TestMirrorDirIsStableAndDoesNotLeakTheURL(t *testing.T) {
	a := mirrorDir("/state", "https://github.com/you/private-repo.git")
	b := mirrorDir("/state", "https://github.com/you/private-repo.git")
	if a != b {
		t.Fatal("mirrorDir is not deterministic")
	}
	if strings.Contains(a, "private-repo") {
		t.Errorf("repo name leaked into the on-disk path: %s", a)
	}
}

func TestTwoAgentsShareOneMirror(t *testing.T) {
	origin := newBareRepoFixture(t)
	state := t.TempDir()
	g := testGitRunner(t)

	for i := 0; i < 2; i++ {
		if _, err := g.EnsureMirror(state, origin, Credential{Kind: "none"}); err != nil {
			t.Fatalf("EnsureMirror %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(state, "repos"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d mirrors, want 1 shared", len(entries))
	}
}

func TestCloneAbortsPastTheSizeCap(t *testing.T) {
	origin := newBareRepoFixture(t)
	seedLargeBlob(t, origin, 3<<20) // comfortably over the cap below
	state := t.TempDir()
	g := testGitRunner(t)

	// Use the file:// protocol so git uses the smart transfer protocol
	// instead of hardlinking objects, which completes instantly and
	// defeats the size watcher.
	fileURL := "file://" + origin
	_, err := g.EnsureMirrorCapped(context.Background(), state, fileURL, Credential{Kind: "none"}, 256<<10)
	if err == nil {
		t.Fatal("a clone past the cap was allowed to finish")
	}
	if _, serr := os.Stat(mirrorDir(state, fileURL)); !os.IsNotExist(serr) {
		t.Fatal("an aborted clone left a partial mirror in place")
	}
}

func TestCloneUnderTheCapSucceeds(t *testing.T) {
	origin := newBareRepoFixture(t)
	state := t.TempDir()
	g := testGitRunner(t)

	dir, err := g.EnsureMirrorCapped(context.Background(), state, origin, Credential{Kind: "none"}, 64<<20)
	if err != nil {
		t.Fatalf("EnsureMirrorCapped: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		t.Fatalf("mirror not created: %v", err)
	}
}

func TestRunCtxCancels(t *testing.T) {
	g := testGitRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.runCtx(ctx, "", Credential{Kind: "none"}, "version"); err == nil {
		t.Fatal("runCtx ignored a cancelled context")
	}
}

// seedLargeBlob clones the bare repo, adds a file of the given size,
// commits it, and pushes it back so the origin carries a large object.
// The blob is filled with random bytes: all-zeros would compress to
// almost nothing in git's zlib and defeat a size cap.
func seedLargeBlob(t *testing.T, bareRepo string, size int64) {
	t.Helper()
	root := t.TempDir()
	mustGit(t, "", "clone", bareRepo, root)
	mustGit(t, root, "config", "user.email", "test@example.com")
	mustGit(t, root, "config", "user.name", "Test")
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.bin"), buf, 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "add", "large.bin")
	mustGit(t, root, "commit", "-m", "large blob")
	mustGit(t, root, "push", "origin", "main")
}
