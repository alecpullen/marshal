package bridge

import (
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
