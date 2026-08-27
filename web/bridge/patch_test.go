package bridge

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFormatPatchAppliesOntoTheBase(t *testing.T) {
	origin := newBareRepoFixture(t)
	state := t.TempDir()
	g := testGitRunner(t)

	mirror, _ := g.EnsureMirror(state, origin, Credential{Kind: "none"})
	dir, err := g.PrepareTree(state, "a1", mirror, origin, "main")
	if err != nil {
		t.Fatal(err)
	}
	// The agent works on a branch off the base, so the patch is the
	// commits since the base rather than the whole history.
	mustGit(t, dir, "checkout", "-b", "agent-work")
	if err := os.WriteFile(filepath.Join(dir, "added.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", "added.txt")
	mustGit(t, dir, "-c", "user.email=t@e.com", "-c", "user.name=T", "commit", "-m", "add a file")

	patch, err := g.FormatPatch(dir, "main")
	if err != nil {
		t.Fatalf("FormatPatch: %v", err)
	}
	if len(patch) == 0 {
		t.Fatal("empty patch")
	}

	// The patch must apply cleanly onto a fresh checkout of the base.
	fresh, err := g.PrepareTree(state, "a2", mirror, origin, "main")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "apply", "--check", "-")
	cmd.Dir = fresh
	cmd.Stdin = bytes.NewReader(patch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("patch does not apply onto the base: %v\n%s", err, out)
	}
}

func TestPatchRequiresATargetBranch(t *testing.T) {
	g := testGitRunner(t)
	if _, err := g.FormatPatch(t.TempDir(), ""); err == nil {
		t.Fatal("FormatPatch accepted an empty base; the output would be meaningless")
	}
}
