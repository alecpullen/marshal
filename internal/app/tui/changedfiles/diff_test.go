package changedfiles

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- tracked patches ------------------------------------------------------

// TestReadDiffTrackedModified pins the ordinary case: a modified tracked file
// yields git's own unified patch.
func TestReadDiffTrackedModified(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\ntwo\n"))
	base := f.commit("init")
	f.write("a.txt", []byte("one\ntwo\nthree\n"))

	snap := ReadSnapshot(context.Background(), f.dir, base)
	if snap.Status != StatusOK {
		t.Fatalf("snapshot Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}

	d := ReadDiff(context.Background(), snap, "a.txt")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if d.Kind != FileModified {
		t.Errorf("Kind = %v, want %v", d.Kind, FileModified)
	}
	if !strings.Contains(d.Patch, "+three") {
		t.Errorf("patch missing the added line:\n%s", d.Patch)
	}
	if !strings.Contains(d.Patch, "--- a/a.txt") || !strings.Contains(d.Patch, "+++ b/a.txt") {
		t.Errorf("patch missing the file headers:\n%s", d.Patch)
	}
	if d.Truncated {
		t.Error("Truncated = true for a three-line patch, want false")
	}
	if d.Binary || d.Deleted || d.Renamed || d.Untracked {
		t.Errorf("classification = binary:%v deleted:%v renamed:%v untracked:%v, want all false",
			d.Binary, d.Deleted, d.Renamed, d.Untracked)
	}
	if !d.CountsKnown || d.Added != 1 {
		t.Errorf("counts = +%d known=%v, want +1 known=true", d.Added, d.CountsKnown)
	}
}

// TestReadDiffDeleted pins that a deleted file is classified and its patch
// shows the removal.
func TestReadDiffDeleted(t *testing.T) {
	f := newFixture(t)
	f.write("gone.txt", []byte("l1\nl2\n"))
	base := f.commit("init")
	if err := os.Remove(filepath.Join(f.dir, "gone.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "gone.txt")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if !d.Deleted {
		t.Error("Deleted = false, want true")
	}
	if !strings.Contains(d.Patch, "-l1") {
		t.Errorf("patch missing the removed line:\n%s", d.Patch)
	}
}

// TestReadDiffRename pins that a rename is classified and that both paths are
// available to the caller.
func TestReadDiffRename(t *testing.T) {
	f := newFixture(t)
	f.write("old.txt", []byte("l1\nl2\nl3\n"))
	base := f.commit("init")
	f.git("mv", "old.txt", "new.txt")

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "new.txt")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if !d.Renamed {
		t.Error("Renamed = false, want true")
	}
	if d.OldPath != "old.txt" {
		t.Errorf("OldPath = %q, want old.txt", d.OldPath)
	}
	// A pure rename has no content change, so the patch is git's rename
	// record. It is only produced when BOTH paths are named, which is why the
	// implementation retries with the old path.
	if !strings.Contains(d.Patch, "rename from old.txt") || !strings.Contains(d.Patch, "rename to new.txt") {
		t.Errorf("patch is not the rename record:\n%s", d.Patch)
	}
}

// TestReadDiffTrackedBinary pins that a tracked binary file is classified as
// binary and its patch carries git's metadata rather than the bytes.
func TestReadDiffTrackedBinary(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")
	f.write("blob.bin", []byte{0x00, 0x01, 0x02, 0x00, 0xff})
	f.git("add", "blob.bin")

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "blob.bin")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if !d.Binary {
		t.Error("Binary = false, want true")
	}
	if !strings.Contains(d.Patch, "Binary files") {
		t.Errorf("patch is not git's binary notice:\n%s", d.Patch)
	}
	if d.CountsKnown {
		t.Error("CountsKnown = true for a binary file, want false")
	}
}

// TestReadDiffPathBeginningWithDash pins that a path that looks like an option
// is still a path: `--` terminates option parsing.
func TestReadDiffPathBeginningWithDash(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")
	f.write("-dash.txt", []byte("dash\n"))
	// `--` here too: git add would otherwise read -dash.txt as a switch.
	f.git("add", "--", "-dash.txt")

	snap := ReadSnapshot(context.Background(), f.dir, base)
	if _, ok := snap.file("-dash.txt"); !ok {
		t.Fatalf("-dash.txt missing from the snapshot: %+v", snap.Files)
	}
	d := ReadDiff(context.Background(), snap, "-dash.txt")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if !strings.Contains(d.Patch, "+dash") {
		t.Errorf("patch for a dash-leading path is empty or wrong:\n%s", d.Patch)
	}
}

// TestReadDiffPathWithSpace pins that a path containing a space is passed as
// one argument and diffed correctly.
func TestReadDiffPathWithSpace(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")
	f.write("sp ace.txt", []byte("spaced\n"))
	f.git("add", "sp ace.txt")

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "sp ace.txt")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if !strings.Contains(d.Patch, "+spaced") {
		t.Errorf("patch for a spaced path is empty or wrong:\n%s", d.Patch)
	}
}

// --- argv: literal pathspec, terminators, no helpers ----------------------

// TestReadDiffArgvIsExact pins the exact command line for a per-file patch:
// the literal pathspec is the LAST argument, `--` immediately precedes it, and
// the helper-disabling options are present.
func TestReadDiffArgvIsExact(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")
	f.write("a.txt", []byte("one\ntwo\n"))

	snap := ReadSnapshot(context.Background(), f.dir, base)
	recorded := captureArgv(t)
	d := ReadDiff(context.Background(), snap, "a.txt")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}

	all := recorded()
	if len(all) != 1 {
		t.Fatalf("ran %d git commands, want 1:\n%s", len(all), joinArgvs(all))
	}
	got := all[0]
	want := []string{
		"git", "-C", f.realDir(), "diff",
		"--no-ext-diff", "--no-textconv", "--no-color", "--unified=3",
		base, "--", "a.txt",
	}
	if !argvEqual(got, want) {
		t.Fatalf("argv =\n  %s\nwant\n  %s", argvString(got), argvString(want))
	}

	// The structural assertions, stated separately so a failure names the
	// property that broke rather than just a mismatched list.
	if got[len(got)-1] != "a.txt" {
		t.Errorf("last argument = %q, want the literal path a.txt", got[len(got)-1])
	}
	if got[len(got)-2] != "--" {
		t.Errorf("argument before the path = %q, want -- (the option terminator)", got[len(got)-2])
	}
	for _, flag := range []string{"--no-ext-diff", "--no-textconv", "--no-color"} {
		found := false
		for _, a := range got {
			if a == flag {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("argv is missing %s: %s", flag, argvString(got))
		}
	}
}

// TestReadDiffDoesNotInvokeExternalDiffOrTextconv configures a repository with
// a diff.external program and a textconv driver, then proves neither runs when
// a patch is read. The helpers write a marker file, so "not invoked" is
// observed directly.
func TestReadDiffDoesNotInvokeExternalDiffOrTextconv(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\ntwo\n"))
	base := f.commit("init")
	f.write("a.txt", []byte("one\ntwo\nthree\n"))

	extMarker := filepath.Join(f.home, "extdiff-ran")
	convMarker := filepath.Join(f.home, "textconv-ran")
	extScript := writeHelperScript(t, f.home, "extdiff.sh", extMarker, "EXTERNAL-DIFF-SENTINEL")
	convScript := writeHelperScript(t, f.home, "textconv.sh", convMarker, "TEXTCONV-SENTINEL")

	f.git("config", "diff.external", extScript)
	f.git("config", "diff.sentinel.textconv", convScript)
	f.write(".gitattributes", []byte("*.txt diff=sentinel\n"))

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "a.txt")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if strings.Contains(d.Patch, "EXTERNAL-DIFF-SENTINEL") {
		t.Errorf("the patch came from the configured diff.external helper:\n%s", d.Patch)
	}
	if strings.Contains(d.Patch, "TEXTCONV-SENTINEL") {
		t.Errorf("the patch came from the configured textconv helper:\n%s", d.Patch)
	}
	if !strings.Contains(d.Patch, "+three") {
		t.Errorf("patch is not git's own diff:\n%s", d.Patch)
	}
	if _, err := os.Stat(extMarker); err == nil {
		t.Error("the configured diff.external helper ran; a read-only inspection must not execute it")
	}
	if _, err := os.Stat(convMarker); err == nil {
		t.Error("the configured textconv helper ran; a read-only inspection must not execute it")
	}
}

// --- untracked previews ---------------------------------------------------

// TestReadDiffUntrackedPreviewDoesNotStage pins that previewing an untracked
// regular text file renders it as an addition WITHOUT staging it. The
// assertion is on git's own view of the index afterwards, not on the
// implementation's intent.
func TestReadDiffUntrackedPreviewDoesNotStage(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")
	f.write("untracked.txt", []byte("l1\nl2\n"))

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "untracked.txt")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if !d.Untracked {
		t.Error("Untracked = false, want true")
	}
	if !strings.Contains(d.Patch, "+l1") || !strings.Contains(d.Patch, "+l2") {
		t.Errorf("preview is not an addition of the file's lines:\n%s", d.Patch)
	}
	if !strings.Contains(d.Patch, "--- /dev/null") {
		t.Errorf("preview is not framed as a new file:\n%s", d.Patch)
	}
	// The flag, not a number: git never reported counts for this path.
	if d.CountsKnown {
		t.Errorf("CountsKnown = true for an untracked preview (Added=%d), want false", d.Added)
	}

	// The file must still be untracked. `git status --porcelain` is git's own
	// answer, so this cannot be satisfied by the implementation merely
	// believing it did not stage anything.
	status := f.git("status", "--porcelain")
	if !strings.Contains(status, "?? untracked.txt") {
		t.Errorf("untracked.txt is no longer untracked after the preview; git status:\n%s", status)
	}
}

// TestReadDiffUntrackedBinaryYieldsMetadata pins that a binary untracked file
// yields metadata rather than its bytes.
func TestReadDiffUntrackedBinaryYieldsMetadata(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	blob := []byte{0x00, 0x01, 0x02, 0x00, 0xff, 0xfe}
	f.write("blob.bin", blob)

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "blob.bin")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if !d.Binary {
		t.Error("Binary = false for a file containing NUL bytes, want true")
	}
	if !strings.Contains(d.Patch, "Binary file") {
		t.Errorf("patch is not a binary notice:\n%s", d.Patch)
	}
	if strings.Contains(d.Patch, "\x00") {
		t.Error("the patch contains raw NUL bytes; a binary preview must be metadata only")
	}
	if d.CountsKnown {
		t.Error("CountsKnown = true for a binary preview, want false")
	}
}

// TestReadDiffUntrackedSymlinkIsMetadata pins that a symlink is reported as a
// symlink, not followed.
func TestReadDiffUntrackedSymlinkIsMetadata(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	f.write("target.txt", []byte("target content\n"))
	if err := os.Symlink("target.txt", filepath.Join(f.dir, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "link.txt")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if !strings.Contains(d.Patch, "symlink") || !strings.Contains(d.Patch, "target.txt") {
		t.Errorf("patch is not symlink metadata:\n%s", d.Patch)
	}
	if strings.Contains(d.Patch, "target content") {
		t.Errorf("the symlink was followed; its target's content leaked into the patch:\n%s", d.Patch)
	}
}

// TestReadDiffSymlinkOutsideRepoIsNotFollowed pins the security property: a
// symlink pointing outside the repository must not have its target read.
func TestReadDiffSymlinkOutsideRepoIsNotFollowed(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	const secret = "OUTSIDE-THE-REPOSITORY-SECRET"
	outside := filepath.Join(f.home, "outside.txt")
	if err := os.WriteFile(outside, []byte(secret+"\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(f.dir, "escape.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "escape.txt")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if strings.Contains(d.Patch, secret) {
		t.Fatalf("content from outside the repository was read through a symlink:\n%s", d.Patch)
	}
	if !strings.Contains(d.Patch, "symlink") {
		t.Errorf("patch is not symlink metadata:\n%s", d.Patch)
	}
}

// TestReadDiffRejectsPathEscapingRoot pins that a caller-supplied path cannot
// walk out of the repository.
func TestReadDiffRejectsPathEscapingRoot(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	const secret = "OUTSIDE-THE-REPOSITORY-SECRET"
	if err := os.WriteFile(filepath.Join(f.home, "outside.txt"), []byte(secret+"\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	snap := ReadSnapshot(context.Background(), f.dir, base)
	for _, p := range []string{"../outside.txt", "../../outside.txt", "/etc/hosts"} {
		d := ReadDiff(context.Background(), snap, p)
		if d.Status == StatusOK {
			t.Errorf("ReadDiff(%q) Status = ok, want a failure", p)
		}
		if strings.Contains(d.Patch, secret) {
			t.Errorf("ReadDiff(%q) read a file outside the repository:\n%s", p, d.Patch)
		}
	}
}

// --- bounded and truncating ----------------------------------------------

// TestReadDiffUntrackedPreviewTruncates pins that a preview larger than
// MaxPreviewBytes is cut AND flagged. Truncation is never silent.
func TestReadDiffUntrackedPreviewTruncates(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	// Comfortably over the cap, and made of lines so the preview is text.
	big := strings.Repeat("0123456789abcdef\n", (MaxPreviewBytes/17)+64)
	if len(big) <= MaxPreviewBytes {
		t.Fatalf("fixture is only %d bytes; it must exceed MaxPreviewBytes (%d)", len(big), MaxPreviewBytes)
	}
	f.write("big.txt", []byte(big))

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "big.txt")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if !d.Truncated {
		t.Error("Truncated = false for a preview larger than MaxPreviewBytes, want true")
	}
	if len(d.Patch) > MaxPreviewBytes {
		t.Errorf("patch is %d bytes, want at most MaxPreviewBytes (%d)", len(d.Patch), MaxPreviewBytes)
	}
	if d.Patch == "" {
		t.Error("patch is empty; a truncated preview must still carry the prefix it read")
	}
}

// TestReadDiffTrackedPatchTruncates is the tracked counterpart: a patch larger
// than the cap is cut and flagged.
func TestReadDiffTrackedPatchTruncates(t *testing.T) {
	f := newFixture(t)
	f.write("big.txt", []byte("seed\n"))
	base := f.commit("init")

	big := strings.Repeat("0123456789abcdef\n", (MaxPreviewBytes/17)+64)
	f.write("big.txt", []byte(big))

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "big.txt")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if !d.Truncated {
		t.Error("Truncated = false for a patch larger than MaxPreviewBytes, want true")
	}
	if len(d.Patch) > MaxPreviewBytes {
		t.Errorf("patch is %d bytes, want at most MaxPreviewBytes (%d)", len(d.Patch), MaxPreviewBytes)
	}
}

// TestReadDiffSmallPatchIsNotTruncated is the guard against a flag that is
// always true: a small patch must report Truncated false.
func TestReadDiffSmallPatchIsNotTruncated(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")
	f.write("a.txt", []byte("one\ntwo\n"))

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "a.txt")
	if d.Truncated {
		t.Error("Truncated = true for a tiny patch, want false")
	}
}

// --- failure and cancellation --------------------------------------------

// TestReadDiffPropagatesSnapshotFailure pins that a failed snapshot is not
// silently turned into an empty patch: the caller must be able to tell "no
// patch" from "the read failed".
func TestReadDiffPropagatesSnapshotFailure(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	f.commit("init")

	snap := ReadSnapshot(context.Background(), f.dir, "no-such-ref")
	if snap.Status != StatusMissingBase {
		t.Fatalf("snapshot Status = %q, want %q", snap.Status, StatusMissingBase)
	}

	d := ReadDiff(context.Background(), snap, "a.txt")
	if d.Status != StatusMissingBase {
		t.Errorf("Status = %q, want the snapshot's %q", d.Status, StatusMissingBase)
	}
	if d.Err == nil {
		t.Error("Err = nil, want the snapshot's error")
	}
	if d.Patch != "" {
		t.Errorf("Patch = %q, want empty for a failed snapshot", d.Patch)
	}
}

// TestReadDiffCancelledReturnsPromptly bounds the cancellation path so a
// regression fails the test rather than hanging it.
func TestReadDiffCancelledReturnsPromptly(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")
	f.write("a.txt", []byte("one\ntwo\n"))

	snap := ReadSnapshot(context.Background(), f.dir, base)
	if snap.Status != StatusOK {
		t.Fatalf("snapshot Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan Diff, 1)
	go func() { done <- ReadDiff(ctx, snap, "a.txt") }()

	select {
	case d := <-done:
		if d.Status != StatusCancelled {
			t.Errorf("Status = %q, want %q (err: %v)", d.Status, StatusCancelled, d.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReadDiff did not return within 10s of a cancelled context")
	}
}

// TestReadDiffDeadlineReturnsPromptly is the deadline counterpart.
func TestReadDiffDeadlineReturnsPromptly(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")
	f.write("a.txt", []byte("one\ntwo\n"))

	snap := ReadSnapshot(context.Background(), f.dir, base)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	done := make(chan Diff, 1)
	go func() { done <- ReadDiff(ctx, snap, "a.txt") }()

	select {
	case d := <-done:
		if d.Status != StatusTimeout {
			t.Errorf("Status = %q, want %q (err: %v)", d.Status, StatusTimeout, d.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReadDiff did not return within 10s of an expired deadline")
	}
}

// TestReadDiffEmptyPath pins that an empty path is a failure, not a silent
// empty patch.
func TestReadDiffEmptyPath(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "")
	if d.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", d.Status, StatusFailed)
	}
	if d.Err == nil {
		t.Error("Err = nil, want a failure")
	}
}

// TestReadDiffUnknownPathIsNotAFailure pins that a path the snapshot does not
// know about still reads: the caller may be looking at a stale list, and git's
// own answer (an empty patch) is more useful than a fabricated error.
func TestReadDiffUnknownPathIsNotAFailure(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	snap := ReadSnapshot(context.Background(), f.dir, base)
	d := ReadDiff(context.Background(), snap, "never-changed.txt")
	if d.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", d.Status, StatusOK, d.Err)
	}
	if d.Patch != "" {
		t.Errorf("Patch = %q, want empty for an unchanged path", d.Patch)
	}
}
