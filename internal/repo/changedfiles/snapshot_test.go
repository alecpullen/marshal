package changedfiles

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- fixture repositories -------------------------------------------------
//
// Every fixture is a REAL repository built with the real git binary. Nothing
// in this file mocks git's output: the whole point of the rewrite is that the
// old code's assumptions about git's output were wrong, and a mock would
// encode the same wrong assumptions.

// fixture is a throwaway repository.
type fixture struct {
	t    *testing.T
	dir  string
	home string
}

// newFixture creates an empty repository in a temp dir.
//
// The environment is pinned so the fixture works on a machine with no git
// identity configured and never reads or writes the developer's real
// ~/.gitconfig: HOME points at a throwaway directory, GIT_CONFIG_GLOBAL at a
// file that does not exist, and GIT_CONFIG_SYSTEM at /dev/null. The author and
// committer identities are supplied through the environment, and the default
// branch is pinned with -c init.defaultBranch=main.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	f := &fixture{t: t, dir: t.TempDir(), home: t.TempDir()}
	f.git("-c", "init.defaultBranch=main", "init", "-q", ".")
	return f
}

// env is the environment every fixture git command runs with.
func (f *fixture) env() []string {
	return append(os.Environ(),
		"HOME="+f.home,
		"GIT_CONFIG_GLOBAL="+filepath.Join(f.home, "gitconfig"),
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=fixture",
		"GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=fixture",
		"GIT_COMMITTER_EMAIL=fixture@example.invalid",
		"GIT_TERMINAL_PROMPT=0",
	)
}

// tryGit runs git in the fixture and returns its combined output.
func (f *fixture) tryGit(args ...string) (string, error) {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", f.dir}, args...)...)
	cmd.Env = f.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// git runs git in the fixture and fails the test on error.
func (f *fixture) git(args ...string) string {
	f.t.Helper()
	out, err := f.tryGit(args...)
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

// write creates or replaces a file, creating parent directories as needed.
// The path is repository-relative and may contain spaces, tabs, newlines, and
// non-ASCII bytes — that is the point of several fixtures.
func (f *fixture) write(rel string, content []byte) {
	f.t.Helper()
	p := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatalf("mkdir for %q: %v", rel, err)
	}
	if err := os.WriteFile(p, content, 0o644); err != nil {
		f.t.Fatalf("write %q: %v", rel, err)
	}
}

// commit stages everything and commits, returning the new commit's OID.
func (f *fixture) commit(msg string) string {
	f.t.Helper()
	f.git("add", "-A")
	f.git("commit", "-q", "-m", msg)
	return strings.TrimSpace(f.git("rev-parse", "HEAD"))
}

// realDir is the fixture directory with symlinks resolved. git reports the
// resolved path from `rev-parse --show-toplevel` (on macOS /var is a symlink
// to /private/var), so a comparison against t.TempDir() would spuriously fail.
func (f *fixture) realDir() string {
	f.t.Helper()
	resolved, err := filepath.EvalSymlinks(f.dir)
	if err != nil {
		f.t.Fatalf("EvalSymlinks(%q): %v", f.dir, err)
	}
	return resolved
}

// --- argv seam ------------------------------------------------------------

// captureArgv installs the argv seam for the duration of the test and returns
// a function reporting every argv recorded so far. The seam lets the fixture
// tests assert on the EXACT command line the implementation builds — option
// ordering, `--` placement, the literal pathspec — while still running real
// git rather than a mock.
func captureArgv(t *testing.T) func() [][]string {
	t.Helper()
	var mu sync.Mutex
	var got [][]string

	argvMu.Lock()
	prev := argvHook
	argvHook = func(argv []string) {
		mu.Lock()
		got = append(got, append([]string(nil), argv...))
		mu.Unlock()
	}
	argvMu.Unlock()

	t.Cleanup(func() {
		argvMu.Lock()
		argvHook = prev
		argvMu.Unlock()
	})

	return func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		return append([][]string(nil), got...)
	}
}

// argvEqual reports whether two argument lists are identical.
func argvEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// argvString renders an argument list for a failure message.
func argvString(argv []string) string { return strings.Join(argv, " ") }

// --- byte-exact -z parsers ------------------------------------------------

// TestParseNumstatZByteExact pins the parser against literal bytes. A path
// containing a space, a tab, or a newline must round-trip exactly; the old
// implementation used strings.Fields and newline splitting and mangled all
// three.
func TestParseNumstatZByteExact(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []File
	}{
		{
			name: "plain",
			in:   "1\t2\ta.txt\x00",
			want: []File{{Path: "a.txt", Added: 1, Removed: 2, CountsKnown: true}},
		},
		{
			name: "space in path",
			in:   "1\t1\tsp ace.txt\x00",
			want: []File{{Path: "sp ace.txt", Added: 1, Removed: 1, CountsKnown: true}},
		},
		{
			name: "tab in path",
			in:   "1\t0\ttab\there.txt\x00",
			want: []File{{Path: "tab\there.txt", Added: 1, Removed: 0, CountsKnown: true}},
		},
		{
			name: "newline in path",
			in:   "1\t0\tnl\nhere.txt\x00",
			want: []File{{Path: "nl\nhere.txt", Added: 1, Removed: 0, CountsKnown: true}},
		},
		{
			name: "unicode path",
			in:   "1\t1\tcaf\xc3\xa9.txt\x00",
			want: []File{{Path: "café.txt", Added: 1, Removed: 1, CountsKnown: true}},
		},
		{
			name: "binary reports unknown counts",
			in:   "-\t-\tbin.dat\x00",
			want: []File{{Path: "bin.dat", Kind: FileBinary, CountsKnown: false}},
		},
		{
			name: "rename carries both paths",
			in:   "0\t0\t\x00old.txt\x00new.txt\x00",
			want: []File{{Path: "new.txt", OldPath: "old.txt", CountsKnown: true}},
		},
		{
			name: "rename with awkward paths",
			in:   "3\t1\t\x00old name.txt\x00new\tname.txt\x00",
			want: []File{{Path: "new\tname.txt", OldPath: "old name.txt", Added: 3, Removed: 1, CountsKnown: true}},
		},
		{
			name: "several records",
			in:   "1\t0\ta.txt\x00-\t-\tb.bin\x00",
			want: []File{
				{Path: "a.txt", Added: 1, CountsKnown: true},
				{Path: "b.bin", Kind: FileBinary},
			},
		},
		{
			name: "empty input",
			in:   "",
			want: nil,
		},
		{
			name: "truncated record is dropped, not guessed",
			in:   "1\t0\ta.txt\x00" + "2\t0\tb.txt",
			want: []File{{Path: "a.txt", Added: 1, CountsKnown: true}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseNumstatZ([]byte(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("got %d files, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("file %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseNameStatusZByteExact pins the name-status parser against literal
// bytes, including the rename form that carries two paths.
func TestParseNameStatusZByteExact(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []File
	}{
		{
			name: "modified",
			in:   "M\x00a.txt\x00",
			want: []File{{Path: "a.txt", Status: 'M', Kind: FileModified}},
		},
		{
			name: "added",
			in:   "A\x00new.txt\x00",
			want: []File{{Path: "new.txt", Status: 'A', Kind: FileAdded}},
		},
		{
			name: "deleted",
			in:   "D\x00gone.txt\x00",
			want: []File{{Path: "gone.txt", Status: 'D', Kind: FileDeleted}},
		},
		{
			name: "space in path",
			in:   "M\x00sp ace.txt\x00",
			want: []File{{Path: "sp ace.txt", Status: 'M', Kind: FileModified}},
		},
		{
			name: "tab in path",
			in:   "A\x00tab\there.txt\x00",
			want: []File{{Path: "tab\there.txt", Status: 'A', Kind: FileAdded}},
		},
		{
			name: "newline in path",
			in:   "A\x00nl\nhere.txt\x00",
			want: []File{{Path: "nl\nhere.txt", Status: 'A', Kind: FileAdded}},
		},
		{
			name: "unicode path",
			in:   "M\x00\xe6\x97\xa5\xe6\x9c\xac\xe8\xaa\x9e.txt\x00",
			want: []File{{Path: "日本語.txt", Status: 'M', Kind: FileModified}},
		},
		{
			name: "rename keeps the score and both paths",
			in:   "R100\x00old.txt\x00new.txt\x00",
			want: []File{{Path: "new.txt", OldPath: "old.txt", Status: 'R', Kind: FileRenamed}},
		},
		{
			name: "copy keeps both paths",
			in:   "C075\x00src.txt\x00dst.txt\x00",
			want: []File{{Path: "dst.txt", OldPath: "src.txt", Status: 'C', Kind: FileRenamed}},
		},
		{
			name: "type change reads as modified",
			in:   "T\x00link.txt\x00",
			want: []File{{Path: "link.txt", Status: 'T', Kind: FileModified}},
		},
		{
			name: "empty input",
			in:   "",
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseNameStatusZ([]byte(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("got %d files, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("file %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseLsFilesZByteExact pins the untracked-path parser against literal
// bytes.
func TestParseLsFilesZByteExact(t *testing.T) {
	in := "a b.txt\x00tab\there.txt\x00nl\nhere.txt\x00caf\xc3\xa9.txt\x00"
	want := []string{"a b.txt", "tab\there.txt", "nl\nhere.txt", "café.txt"}
	got := parseLsFilesZ([]byte(in))
	if len(got) != len(want) {
		t.Fatalf("got %d paths, want %d: %q", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("path %d = %q, want %q", i, got[i], want[i])
		}
	}
	if got := parseLsFilesZ(nil); len(got) != 0 {
		t.Errorf("parseLsFilesZ(nil) = %q, want empty", got)
	}
}

// --- status classification ------------------------------------------------

// TestReadSnapshotStatuses covers all six outcomes. The old Read returned nil
// for every one of them, so a clean tree and a broken repository were
// indistinguishable.
func TestReadSnapshotStatuses(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		f := newFixture(t)
		f.write("a.txt", []byte("one\n"))
		base := f.commit("init")
		f.write("a.txt", []byte("one\ntwo\n"))

		snap := ReadSnapshot(context.Background(), f.dir, base)
		if snap.Status != StatusOK {
			t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
		}
		if len(snap.Files) != 1 || snap.Files[0].Path != "a.txt" {
			t.Fatalf("Files = %+v, want one entry for a.txt", snap.Files)
		}
		if snap.Clean() {
			t.Error("Clean() = true for a dirty tree, want false")
		}
	})

	t.Run("clean tree is ok with no files", func(t *testing.T) {
		f := newFixture(t)
		f.write("a.txt", []byte("one\n"))
		base := f.commit("init")

		snap := ReadSnapshot(context.Background(), f.dir, base)
		if snap.Status != StatusOK {
			t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
		}
		if len(snap.Files) != 0 {
			t.Fatalf("Files = %+v, want none", snap.Files)
		}
		if !snap.Clean() || !snap.IsEmpty() {
			t.Error("Clean()/IsEmpty() = false for a clean tree, want true")
		}
	})

	t.Run("missing base", func(t *testing.T) {
		f := newFixture(t)
		f.write("a.txt", []byte("one\n"))
		f.commit("init")

		snap := ReadSnapshot(context.Background(), f.dir, "no-such-ref")
		if snap.Status != StatusMissingBase {
			t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusMissingBase, snap.Err)
		}
		if snap.Err == nil {
			t.Error("Err = nil, want the git failure")
		}
		// The distinction the old code erased: a missing base is NOT a clean
		// tree, even though both have zero files.
		if snap.Clean() || snap.IsEmpty() {
			t.Error("Clean()/IsEmpty() = true for a missing base, want false")
		}
	})

	t.Run("not a repo", func(t *testing.T) {
		dir := t.TempDir()
		snap := ReadSnapshot(context.Background(), dir, "HEAD")
		if snap.Status != StatusNotARepo {
			t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusNotARepo, snap.Err)
		}
		if snap.Root != "" {
			t.Errorf("Root = %q, want empty outside a repository", snap.Root)
		}
		if snap.Clean() {
			t.Error("Clean() = true outside a repository, want false")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		f := newFixture(t)
		f.write("a.txt", []byte("one\n"))
		base := f.commit("init")

		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		time.Sleep(time.Millisecond) // let the deadline actually pass

		snap := ReadSnapshot(ctx, f.dir, base)
		if snap.Status != StatusTimeout {
			t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusTimeout, snap.Err)
		}
		if snap.Clean() {
			t.Error("Clean() = true on a timeout, want false")
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		f := newFixture(t)
		f.write("a.txt", []byte("one\n"))
		base := f.commit("init")

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		snap := ReadSnapshot(ctx, f.dir, base)
		if snap.Status != StatusCancelled {
			t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusCancelled, snap.Err)
		}
		if snap.Clean() {
			t.Error("Clean() = true on a cancellation, want false")
		}
	})

	t.Run("failed", func(t *testing.T) {
		f := newFixture(t)
		f.write("a.txt", []byte("one\n"))
		base := f.commit("init")

		// A corrupt index makes the diff pass fail while the repository root
		// and the base commit still resolve, so this is a genuine command
		// failure rather than a missing base or a non-repo.
		if err := os.WriteFile(filepath.Join(f.dir, ".git", "index"), []byte("garbage"), 0o644); err != nil {
			t.Fatalf("corrupt index: %v", err)
		}

		snap := ReadSnapshot(context.Background(), f.dir, base)
		if snap.Status != StatusFailed {
			t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusFailed, snap.Err)
		}
		if snap.Err == nil {
			t.Error("Err = nil, want the git failure")
		}
		if snap.Clean() {
			t.Error("Clean() = true on a failed read, want false")
		}
	})

	t.Run("empty base ref", func(t *testing.T) {
		f := newFixture(t)
		snap := ReadSnapshot(context.Background(), f.dir, "")
		if snap.Status != StatusMissingBase {
			t.Fatalf("Status = %q, want %q", snap.Status, StatusMissingBase)
		}
	})

	t.Run("empty working dir", func(t *testing.T) {
		snap := ReadSnapshot(context.Background(), "", "HEAD")
		if snap.Status != StatusFailed {
			t.Fatalf("Status = %q, want %q", snap.Status, StatusFailed)
		}
	})
}

// TestClassifyStatusMapping pins the failure-to-Status mapping directly, so it
// does not depend on which way a particular git build fails. The context is
// checked FIRST: exec.CommandContext kills the child when the context ends, so
// a deadline or a cancel arrives as an ordinary command failure and would
// otherwise be misreported as StatusFailed.
func TestClassifyStatusMapping(t *testing.T) {
	expired, cancelExpired := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelExpired()
	time.Sleep(time.Millisecond)

	cancelled, cancelCancelled := context.WithCancel(context.Background())
	cancelCancelled()

	boom := errors.New("boom")

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want Status
	}{
		{"success", context.Background(), nil, StatusOK},
		{"plain failure", context.Background(), boom, StatusFailed},
		{"deadline wins over the command error", expired, boom, StatusTimeout},
		{"cancel wins over the command error", cancelled, boom, StatusCancelled},
		{"deadline with no command error", expired, nil, StatusTimeout},
		{"cancel with no command error", cancelled, nil, StatusCancelled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.ctx, tc.err); got != tc.want {
				t.Errorf("classify = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReadSnapshotCleanAndMissingBaseAreDistinguishable is the headline
// regression: the old Read returned nil for both, so a caller could not tell
// "nothing changed" from "the base ref is gone".
func TestReadSnapshotCleanAndMissingBaseAreDistinguishable(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	clean := ReadSnapshot(context.Background(), f.dir, base)
	missing := ReadSnapshot(context.Background(), f.dir, "no-such-ref")

	if clean.Status == missing.Status {
		t.Fatalf("clean and missing-base share Status %q; they must be distinguishable", clean.Status)
	}
	if !clean.Clean() {
		t.Error("clean.Clean() = false, want true")
	}
	if missing.Clean() {
		t.Error("missing.Clean() = true, want false")
	}
	if missing.Err == nil {
		t.Error("missing.Err = nil, want the git failure")
	}
}

// --- one resolved base OID, worktree counts -------------------------------

// TestReadSnapshotResolvesOneBaseOID pins that the comparison is against a
// single resolved commit, recorded so a caller can label it.
func TestReadSnapshotResolvesOneBaseOID(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")
	f.write("a.txt", []byte("one\ntwo\n"))

	snap := ReadSnapshot(context.Background(), f.dir, base)
	if snap.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}
	if snap.BaseRef != base {
		t.Errorf("BaseRef = %q, want the ref as requested (%q)", snap.BaseRef, base)
	}
	if snap.BaseOID != base {
		t.Errorf("BaseOID = %q, want the resolved commit %q", snap.BaseOID, base)
	}
	if snap.Root != f.realDir() {
		t.Errorf("Root = %q, want %q", snap.Root, f.realDir())
	}
	if snap.CapturedAt.IsZero() {
		t.Error("CapturedAt is zero, want the read time")
	}
}

// TestReadSnapshotResolvesSymbolicBaseRef pins that a symbolic ref is
// resolved to the commit it names, so the label does not move under the
// caller.
func TestReadSnapshotResolvesSymbolicBaseRef(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")
	f.write("a.txt", []byte("one\ntwo\n"))

	snap := ReadSnapshot(context.Background(), f.dir, "HEAD")
	if snap.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}
	if snap.BaseRef != "HEAD" {
		t.Errorf("BaseRef = %q, want HEAD", snap.BaseRef)
	}
	if snap.BaseOID != base {
		t.Errorf("BaseOID = %q, want HEAD resolved to %q", snap.BaseOID, base)
	}
}

// TestReadSnapshotWorktreeCountsWinOverIndex is the regression for the old
// `--cached` merge, which OVERWROTE the worktree counts: a file whose index
// and worktree differed was reported with the index's numbers.
func TestReadSnapshotWorktreeCountsWinOverIndex(t *testing.T) {
	f := newFixture(t)
	f.write("iw.txt", []byte("A\n"))
	base := f.commit("init")

	// Index: A -> B,C (2 added, 1 removed against the base).
	f.write("iw.txt", []byte("B\nC\n"))
	f.git("add", "iw.txt")

	// Worktree: A -> D,E,F (3 added, 1 removed against the base).
	f.write("iw.txt", []byte("D\nE\nF\n"))

	snap := ReadSnapshot(context.Background(), f.dir, base)
	if snap.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}
	got, ok := snap.file("iw.txt")
	if !ok {
		t.Fatalf("iw.txt missing from %+v", snap.Files)
	}
	if got.Added != 3 || got.Removed != 1 {
		t.Errorf("counts = +%d -%d, want +3 -1 (the WORKTREE counts); "+
			"+2 -1 would be the cached/index counts the old merge preferred",
			got.Added, got.Removed)
	}
	if !got.CountsKnown {
		t.Error("CountsKnown = false, want true for a text diff")
	}
}

// --- unknown counts -------------------------------------------------------

// TestReadSnapshotUntrackedCountsUnknown is the regression for the invented
// `Added: 1`. The plan bans it: "Report unknown counts explicitly rather than
// inventing Added: 1 for untracked files." The assertion is on the FLAG, not
// on a number.
func TestReadSnapshotUntrackedCountsUnknown(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	// A new file with several lines, left unstaged. If the implementation
	// invented a count it would say 1; if it counted the file it would say 3.
	// Neither is git's answer, so the flag must be false.
	f.write("untracked.txt", []byte("l1\nl2\nl3\n"))

	snap := ReadSnapshot(context.Background(), f.dir, base)
	if snap.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}
	got, ok := snap.file("untracked.txt")
	if !ok {
		t.Fatalf("untracked.txt missing from %+v", snap.Files)
	}
	if got.Kind != FileUntracked {
		t.Errorf("Kind = %v, want %v", got.Kind, FileUntracked)
	}
	if got.CountsKnown {
		t.Errorf("CountsKnown = true for an untracked file (Added=%d); "+
			"git reported no counts, so the flag must be false", got.Added)
	}
	if got.Added == 1 {
		t.Errorf("Added = 1 for an untracked file: that is the invented count the plan bans")
	}
}

// TestReadSnapshotBinaryCountsUnknown pins that a binary file reports unknown
// counts rather than zero. git prints "-" for both, which is not a number.
func TestReadSnapshotBinaryCountsUnknown(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	f.write("blob.bin", []byte{0x00, 0x01, 0x02, 0x00, 0xff})
	f.git("add", "blob.bin")

	snap := ReadSnapshot(context.Background(), f.dir, base)
	if snap.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}
	got, ok := snap.file("blob.bin")
	if !ok {
		t.Fatalf("blob.bin missing from %+v", snap.Files)
	}
	if got.CountsKnown {
		t.Errorf("CountsKnown = true for a binary file (Added=%d Removed=%d), want false",
			got.Added, got.Removed)
	}
	if got.Kind != FileBinary {
		t.Errorf("Kind = %v, want %v", got.Kind, FileBinary)
	}
}

// --- fixture path coverage ------------------------------------------------

// TestReadSnapshotAwkwardPaths is the end-to-end proof that paths containing a
// space, a tab, a newline, and non-ASCII bytes survive the round trip. The old
// parser split on whitespace and newlines and mangled every one of them.
func TestReadSnapshotAwkwardPaths(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	paths := []string{
		"sp ace.txt",
		"tab\there.txt",
		"nl\nhere.txt",
		"café.txt",
		"日本語.txt",
		"-dash.txt",
	}
	for _, p := range paths {
		f.write(p, []byte("content\n"))
	}

	snap := ReadSnapshot(context.Background(), f.dir, base)
	if snap.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}

	got := map[string]File{}
	for _, file := range snap.Files {
		got[file.Path] = file
	}
	for _, p := range paths {
		file, ok := got[p]
		if !ok {
			t.Errorf("path %q missing from the snapshot; got %v", p, keysOf(got))
			continue
		}
		if file.Kind != FileUntracked {
			t.Errorf("path %q Kind = %v, want %v", p, file.Kind, FileUntracked)
		}
	}
	if len(snap.Files) != len(paths) {
		t.Errorf("got %d files, want %d: %v", len(snap.Files), len(paths), keysOf(got))
	}
}

// TestReadSnapshotRenameCapturesBothPaths pins that a rename reports the new
// path as Path and the old path as OldPath.
func TestReadSnapshotRenameCapturesBothPaths(t *testing.T) {
	f := newFixture(t)
	f.write("old.txt", []byte("l1\nl2\nl3\n"))
	base := f.commit("init")

	f.git("mv", "old.txt", "new.txt")

	snap := ReadSnapshot(context.Background(), f.dir, base)
	if snap.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}
	got, ok := snap.file("new.txt")
	if !ok {
		t.Fatalf("new.txt missing from %+v", snap.Files)
	}
	if got.OldPath != "old.txt" {
		t.Errorf("OldPath = %q, want old.txt", got.OldPath)
	}
	if got.Kind != FileRenamed {
		t.Errorf("Kind = %v, want %v", got.Kind, FileRenamed)
	}
	if got.Status != 'R' {
		t.Errorf("Status = %q, want 'R' (git's own letter)", got.Status)
	}
	if _, ok := snap.file("old.txt"); ok {
		t.Error("old.txt appears as its own entry; a rename is one entry with two paths")
	}
}

// TestReadSnapshotDeletion pins that a deleted path is reported as deleted.
func TestReadSnapshotDeletion(t *testing.T) {
	f := newFixture(t)
	f.write("gone.txt", []byte("l1\nl2\n"))
	f.write("kept.txt", []byte("k\n"))
	base := f.commit("init")

	if err := os.Remove(filepath.Join(f.dir, "gone.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	snap := ReadSnapshot(context.Background(), f.dir, base)
	if snap.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}
	got, ok := snap.file("gone.txt")
	if !ok {
		t.Fatalf("gone.txt missing from %+v", snap.Files)
	}
	if got.Kind != FileDeleted {
		t.Errorf("Kind = %v, want %v", got.Kind, FileDeleted)
	}
	if got.Status != 'D' {
		t.Errorf("Status = %q, want 'D'", got.Status)
	}
	if !got.CountsKnown || got.Removed != 2 {
		t.Errorf("counts = +%d -%d known=%v, want +0 -2 known=true",
			got.Added, got.Removed, got.CountsKnown)
	}
}

// TestReadSnapshotStagedAddition pins that a staged new file is reported as an
// addition with real counts, not as an untracked file.
func TestReadSnapshotStagedAddition(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	f.write("new.txt", []byte("brand\nnew\n"))
	f.git("add", "new.txt")

	snap := ReadSnapshot(context.Background(), f.dir, base)
	if snap.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}
	got, ok := snap.file("new.txt")
	if !ok {
		t.Fatalf("new.txt missing from %+v", snap.Files)
	}
	if got.Kind != FileAdded {
		t.Errorf("Kind = %v, want %v", got.Kind, FileAdded)
	}
	if got.Status != 'A' {
		t.Errorf("Status = %q, want 'A'", got.Status)
	}
	if !got.CountsKnown || got.Added != 2 {
		t.Errorf("counts = +%d known=%v, want +2 known=true", got.Added, got.CountsKnown)
	}
}

// TestReadSnapshotIgnoresGitignoredUntracked pins that an ignored file never
// surfaces.
func TestReadSnapshotIgnoresGitignoredUntracked(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	f.write(".gitignore", []byte("ignored.txt\n"))
	f.write("ignored.txt", []byte("x\n"))

	snap := ReadSnapshot(context.Background(), f.dir, base)
	if snap.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}
	if _, ok := snap.file("ignored.txt"); ok {
		t.Fatalf("gitignored file surfaced: %+v", snap.Files)
	}
}

// TestReadSnapshotLinkedWorktreeUsesItsOwnRoot pins that a linked worktree
// reports the worktree's changes, not the main checkout's.
func TestReadSnapshotLinkedWorktreeUsesItsOwnRoot(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	wt := filepath.Join(f.dir, "wt")
	f.git("worktree", "add", "-b", "feat-x", wt)

	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write worktree a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "main-only.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatalf("write main-only.txt: %v", err)
	}

	snap := ReadSnapshot(context.Background(), wt, base)
	if snap.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}
	if _, ok := snap.file("a.txt"); !ok {
		t.Errorf("a.txt missing from the worktree snapshot: %+v", snap.Files)
	}
	if _, ok := snap.file("main-only.txt"); ok {
		t.Errorf("main-checkout file leaked into the worktree snapshot: %+v", snap.Files)
	}
}

// --- cancellation ---------------------------------------------------------

// TestReadSnapshotCancelledReturnsPromptly bounds the cancellation path so a
// regression fails the test rather than hanging it.
func TestReadSnapshotCancelledReturnsPromptly(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan Snapshot, 1)
	go func() { done <- ReadSnapshot(ctx, f.dir, base) }()

	select {
	case snap := <-done:
		if snap.Status != StatusCancelled {
			t.Errorf("Status = %q, want %q (err: %v)", snap.Status, StatusCancelled, snap.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReadSnapshot did not return within 10s of a cancelled context")
	}
}

// TestReadSnapshotDeadlineReturnsPromptly is the deadline counterpart.
func TestReadSnapshotDeadlineReturnsPromptly(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	done := make(chan Snapshot, 1)
	go func() { done <- ReadSnapshot(ctx, f.dir, base) }()

	select {
	case snap := <-done:
		if snap.Status != StatusTimeout {
			t.Errorf("Status = %q, want %q (err: %v)", snap.Status, StatusTimeout, snap.Err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReadSnapshot did not return within 10s of an expired deadline")
	}
}

// --- argv: literal pathspecs, terminators, no helpers ---------------------

// TestReadSnapshotArgvIsExact pins the exact command line. Every pass must
// terminate options with `--`, disable external diff and textconv helpers, and
// compare against the resolved OID rather than the ref. There must be no
// `--cached` pass: that is the one that used to overwrite the worktree counts.
func TestReadSnapshotArgvIsExact(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", []byte("one\n"))
	base := f.commit("init")
	f.write("a.txt", []byte("one\ntwo\n"))

	recorded := captureArgv(t)
	snap := ReadSnapshot(context.Background(), f.dir, base)
	if snap.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}
	all := recorded()

	want := [][]string{
		{"git", "-C", f.dir, "rev-parse", "--show-toplevel"},
		{"git", "-C", f.realDir(), "rev-parse", "--verify", "--end-of-options", base + "^{commit}"},
		{"git", "-C", f.realDir(), "diff", "--numstat", "-z", "-M",
			"--no-ext-diff", "--no-textconv", "--no-color", base, "--"},
		{"git", "-C", f.realDir(), "diff", "--name-status", "-z", "-M",
			"--no-ext-diff", "--no-textconv", "--no-color", base, "--"},
		{"git", "-C", f.realDir(), "ls-files", "-z", "--others", "--exclude-standard", "--"},
	}
	if len(all) != len(want) {
		t.Fatalf("ran %d git commands, want %d:\n%s", len(all), len(want), joinArgvs(all))
	}
	for i := range want {
		if !argvEqual(all[i], want[i]) {
			t.Errorf("command %d =\n  %s\nwant\n  %s", i, argvString(all[i]), argvString(want[i]))
		}
	}
	for _, argv := range all {
		for _, a := range argv {
			if a == "--cached" {
				t.Errorf("a --cached pass ran: %s", argvString(argv))
			}
		}
	}
}

// TestReadSnapshotDoesNotInvokeExternalDiffOrTextconv configures a repository
// with a diff.external program and a textconv driver, then proves neither is
// executed. The helpers write a marker file, so "not invoked" is observed
// directly rather than inferred from the output.
func TestReadSnapshotDoesNotInvokeExternalDiffOrTextconv(t *testing.T) {
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
	if snap.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (err: %v)", snap.Status, StatusOK, snap.Err)
	}
	if _, err := os.Stat(extMarker); err == nil {
		t.Error("the configured diff.external helper ran; a read-only inspection must not execute it")
	}
	if _, err := os.Stat(convMarker); err == nil {
		t.Error("the configured textconv helper ran; a read-only inspection must not execute it")
	}
}

// writeHelperScript writes an executable shell script that records that it ran
// and prints a sentinel, so a test can prove it was never invoked.
func writeHelperScript(t *testing.T, dir, name, marker, sentinel string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := "#!/bin/sh\n: > " + shellQuote(marker) + "\necho " + shellQuote(sentinel) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write helper %s: %v", name, err)
	}
	return path
}

// shellQuote single-quotes a path for the helper scripts.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// joinArgvs renders every recorded command for a failure message.
func joinArgvs(all [][]string) string {
	var b strings.Builder
	for i, argv := range all {
		b.WriteString("  ")
		b.WriteString(argvString(argv))
		if i < len(all)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// keysOf returns the sorted keys of a path→File map, for failure messages.
func keysOf(m map[string]File) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// file returns the snapshot entry for path.
func (s Snapshot) file(path string) (File, bool) {
	for _, f := range s.Files {
		if f.Path == path {
			return f, true
		}
	}
	return File{}, false
}
