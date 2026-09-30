package changedfiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests exercise the compatibility adapter Read. They are the original
// suite, kept so the adapter's contract is pinned; the fixture helper they now
// use is the same real-git one the snapshot tests use.
//
// Two tests changed from the original suite, both because they asserted the
// OLD buggy behaviour:
//
//   - TestParseNameStatusRenameKeysByNewPath tested parseNameStatus, the
//     newline-splitting parser that mangled awkward paths. It is replaced by
//     TestParseNameStatusZRenameKeysByNewPath, which pins the same property
//     (a rename is keyed by its new path) against the byte-exact parser.
//   - TestReadUntrackedUnstagedNewFileIncluded asserted only the status
//     letter, which is unchanged, but it now also pins that the adapter does
//     NOT report the invented Added: 1 the old implementation wrote.

func TestReadModifiedAndAdded(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\ntwo\n"))
	base := f.commit("init")

	f.write("a.txt", []byte("one\ntwo\nthree\n"))
	f.write("b.txt", []byte("new\n"))
	f.git("add", "b.txt")

	got := Read(f.dir, base)
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2: %+v", len(got), got)
	}
	byPath := map[string]int{}
	for _, file := range got {
		byPath[file.Path] = file.Added
	}
	if byPath["a.txt"] != 1 {
		t.Errorf("a.txt added = %d, want 1", byPath["a.txt"])
	}
	if byPath["b.txt"] != 1 {
		t.Errorf("b.txt added = %d, want 1", byPath["b.txt"])
	}
}

func TestReadCleanTree(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\ntwo\n"))
	base := f.commit("init")

	if got := Read(f.dir, base); len(got) != 0 {
		t.Errorf("Read(clean) = %+v, want empty", got)
	}
}

func TestReadNonRepoReturnsNil(t *testing.T) {
	if got := Read(t.TempDir(), "HEAD"); got != nil {
		t.Errorf("Read(non-repo) = %+v, want nil", got)
	}
}

func TestReadEmptyBaseRefReturnsNil(t *testing.T) {
	if got := Read(t.TempDir(), ""); got != nil {
		t.Errorf("Read(no base ref) = %+v, want nil", got)
	}
}

func TestReadModifiedFileOnlyAdditionsGetsM(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\ntwo\n"))
	base := f.commit("init")

	// Append lines to existing file — numstat shows additions-only.
	f.write("a.txt", []byte("one\ntwo\nthree\nfour\nfive\n"))

	got := Read(f.dir, base)
	if len(got) != 1 {
		t.Fatalf("got %d files, want 1: %+v", len(got), got)
	}
	if got[0].Path != "a.txt" {
		t.Errorf("path = %q, want a.txt", got[0].Path)
	}
	if got[0].Status != 'M' {
		t.Errorf("status = %q, want 'M' (modified, not added)", got[0].Status)
	}
}

// TestParseNameStatusZRenameKeysByNewPath replaces the original
// TestParseNameStatusRenameKeysByNewPath, which exercised the deleted
// newline-splitting parser. The property is the same: a rename is one entry
// keyed by its NEW path, with the old path carried alongside.
func TestParseNameStatusZRenameKeysByNewPath(t *testing.T) {
	files := parseNameStatusZ([]byte("R100\x00old.txt\x00new.txt\x00M\x00modified.txt\x00"))
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2: %+v", len(files), files)
	}
	if files[0].Path != "new.txt" || files[0].OldPath != "old.txt" || files[0].Status != 'R' {
		t.Errorf("rename entry = %+v, want Path=new.txt OldPath=old.txt Status='R'", files[0])
	}
	if files[1].Path != "modified.txt" || files[1].Status != 'M' {
		t.Errorf("modified entry = %+v, want Path=modified.txt Status='M'", files[1])
	}
}

func TestReadUntrackedUnstagedNewFileIncluded(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\ntwo\n"))
	base := f.commit("init")

	// Create a new file but do NOT stage it. The diff passes won't see it;
	// only the ls-files --others pass reports it.
	f.write("untracked.txt", []byte("new\n"))

	got := Read(f.dir, base)
	found := false
	for _, file := range got {
		if file.Path == "untracked.txt" {
			found = true
			if file.Status != 'A' {
				t.Errorf("status = %q, want 'A' (added)", file.Status)
			}
			// The old implementation wrote Added: 1 here, inventing a count
			// git never reported. The plan bans it: "Report unknown counts
			// explicitly rather than inventing Added: 1 for untracked files."
			// The adapter has no CountsKnown field, so it reports the honest
			// zero.
			if file.Added != 0 {
				t.Errorf("added = %d for an untracked file, want 0 "+
					"(the old implementation invented 1)", file.Added)
			}
		}
	}
	if !found {
		t.Fatalf("untracked.txt not in results: %+v", got)
	}
}

func TestReadIgnoresGitignoredUntracked(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\ntwo\n"))
	base := f.commit("init")

	// A gitignored file must never surface in the rail.
	f.write(".gitignore", []byte("ignored.txt\n"))
	f.write("ignored.txt", []byte("x\n"))

	got := Read(f.dir, base)
	for _, file := range got {
		if file.Path == "ignored.txt" {
			t.Fatalf("gitignored file should not appear: %+v", got)
		}
	}
}

func TestReadNewFileGetsA(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\ntwo\n"))
	base := f.commit("init")

	// Create a genuinely new file and stage it.
	f.write("new.txt", []byte("brand new\n"))
	f.git("add", "new.txt")

	got := Read(f.dir, base)
	found := false
	for _, file := range got {
		if file.Path == "new.txt" {
			found = true
			if file.Status != 'A' {
				t.Errorf("status = %q, want 'A' (added)", file.Status)
			}
		}
	}
	if !found {
		t.Fatalf("new.txt not in results: %+v", got)
	}
}

// TestReadWorktreeCountsWinOverIndex pins the adapter against the old
// `--cached` merge, which overwrote the worktree counts.
func TestReadWorktreeCountsWinOverIndex(t *testing.T) {
	f := newFixture(t)
	f.write("iw.txt", []byte("A\n"))
	base := f.commit("init")

	f.write("iw.txt", []byte("B\nC\n"))
	f.git("add", "iw.txt")
	f.write("iw.txt", []byte("D\nE\nF\n"))

	got := Read(f.dir, base)
	if len(got) != 1 {
		t.Fatalf("got %d files, want 1: %+v", len(got), got)
	}
	if got[0].Added != 3 || got[0].Removed != 1 {
		t.Errorf("counts = +%d -%d, want +3 -1 (the WORKTREE counts)", got[0].Added, got[0].Removed)
	}
}

// TestReadAwkwardPathsSurvive pins that the adapter reports paths containing a
// space, a tab, and a newline exactly. The old parser split on whitespace and
// newlines and mangled all three.
func TestReadAwkwardPathsSurvive(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\ntwo\n"))
	base := f.commit("init")

	paths := []string{"sp ace.txt", "tab\there.txt", "nl\nhere.txt"}
	for _, p := range paths {
		f.write(p, []byte("content\n"))
	}

	got := Read(f.dir, base)
	seen := map[string]bool{}
	for _, file := range got {
		seen[file.Path] = true
	}
	for _, p := range paths {
		if !seen[p] {
			t.Errorf("path %q missing from Read's output: %+v", p, got)
		}
	}
}

// TestReadNonRepoAndCleanTreeBothNil documents the adapter's retained flaw:
// both a failure and a clean tree come back as nil, which is exactly why
// ReadSnapshot exists. The test pins the adapter's contract so a future change
// to it is deliberate.
func TestReadNonRepoAndCleanTreeBothNil(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\ntwo\n"))
	base := f.commit("init")

	clean := Read(f.dir, base)
	broken := Read(t.TempDir(), "HEAD")
	if clean != nil || broken != nil {
		t.Fatalf("clean = %+v, broken = %+v; the adapter returns nil for both", clean, broken)
	}

	// The snapshot API distinguishes them, which is the point.
	cleanSnap := ReadSnapshot(t.Context(), f.dir, base)
	brokenSnap := ReadSnapshot(t.Context(), t.TempDir(), "HEAD")
	if cleanSnap.Clean() == brokenSnap.Clean() {
		t.Errorf("Clean() agrees for a clean tree (%v) and a non-repo (%v)",
			cleanSnap.Clean(), brokenSnap.Clean())
	}
}

// TestReadDoesNotStageAnything pins that the read-only adapter leaves the
// index alone.
func TestReadDoesNotStageAnything(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\ntwo\n"))
	base := f.commit("init")
	f.write("untracked.txt", []byte("new\n"))

	Read(f.dir, base)

	status := f.git("status", "--porcelain")
	if want := "?? untracked.txt"; !strings.Contains(status, want) {
		t.Errorf("git status after Read:\n%s\nwant it to still contain %q", status, want)
	}
}

// TestReadFixtureSanity guards the fixture helper itself: if the pinned
// environment stopped working, every other test in this package would fail for
// a confusing reason.
func TestReadFixtureSanity(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")
	if len(base) != 40 {
		t.Fatalf("commit OID = %q, want a 40-character SHA", base)
	}
	if _, err := os.Stat(filepath.Join(f.dir, ".git")); err != nil {
		t.Fatalf("fixture is not a repository: %v", err)
	}
}
