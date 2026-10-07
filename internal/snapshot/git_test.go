package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The old shadow-repo capture path was removed in this commit, and with it the
// hand-rolled gitignore matcher that path depended on. The two tests that lived
// here asserted the matcher's behaviour directly; what has to be asserted now
// is that NOTHING in the package reimplements ignore matching, because a second
// implementation is a second answer to "would Git ignore this?" — which is
// exactly the defect the Git-native eligibility listing removed.

// The package must not grow a second ignore matcher. This is a structural
// assertion rather than a behavioural one, and it is deliberate: the behaviour
// is covered by TestEligibilityFollowsGitIgnoreSemantics against real Git, and
// a reintroduced approximation would pass a behavioural test written from the
// approximation's own rules.
func TestPackageDoesNotReimplementGitignoreMatching(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	forbidden := []string{
		"matchIgnorePattern", "allIgnoreRules", "largeFileExcludes", "isIgnored",
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, bad := range forbidden {
			if strings.Contains(string(data), bad) {
				t.Errorf("%s still references %s; ignore semantics must come from Git alone", name, bad)
			}
		}
	}
}

// The legacy capture path must not be reachable: no `git add`, no `git commit`,
// and no shadow repository layout anywhere in the package's non-test sources.
func TestPackageHasNoLegacyCapturePath(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(data)
		for _, bad := range []string{"ensureRepo", "refreshExclude", "shadowDir"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s still references %s; the legacy shadow-repo capture path must be gone", name, bad)
			}
		}
	}
}

// A git-output diagnostic must surface Git's own message with newlines
// flattened and the length capped, because these are logged on one line at
// Warn while the raw output is Debug-only. A bare "exit status 1" is
// undiagnosable, which is how a snapshot bug stayed invisible through hundreds
// of occurrences.
//
// This assertion lived in nested_gitignore_test.go, whose subject — the nested
// .gitignore regression — no longer has a code path of its own to test: the
// Git-native eligibility listing honours nested ignore files by construction,
// and TestEligibilityFollowsGitIgnoreSemantics covers it against real Git.
func TestGitOutputSuffixSurfacesCause(t *testing.T) {
	if got := gitOutputSuffix(nil); got != "" {
		t.Errorf("gitOutputSuffix(nil) = %q, want empty", got)
	}
	got := gitOutputSuffix([]byte("The following paths are ignored:\n.kilo/node_modules\n"))
	if !strings.Contains(got, "paths are ignored") {
		t.Errorf("gitOutputSuffix() = %q, want git's message", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("gitOutputSuffix() = %q, want newlines flattened for one-line logs", got)
	}
	long := gitOutputSuffix([]byte(strings.Repeat("z", 500)))
	if len(long) > 320 {
		t.Errorf("gitOutputSuffix() len = %d, want capped", len(long))
	}
}

// The nested-.gitignore shape that broke the old path must still capture
// cleanly. The old failure was a pathspec naming a path Git ignored, which made
// `git add` exit 1 and silently disabled rollback protection for that
// workspace. The new path never hands an eligibility decision to `git add` at
// all, so the assertion is simply that the capture succeeds and records the
// nested directory's own ignore file.
func TestTrackSucceedsWithNestedGitignoredLargeFile(t *testing.T) {
	requireServiceGit(t)
	dir := writeNestedIgnoredLargeFile(t)

	// maxFile below the large file's size, so it would be an exclude candidate
	// under the old scheme.
	svc := New(t.TempDir(), dir, 1024, []string{}, testLogger())
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if !validObjectHash(hash) {
		t.Fatalf("Track returned %q, which is not an object id", hash)
	}

	files := serviceTrackedPaths(t, svc, hash)
	joined := strings.Join(files, "\n")
	if strings.Contains(joined, "node_modules") {
		t.Fatalf("nested-gitignored path entered the snapshot: %v", files)
	}
	if !strings.Contains(joined, "tracked.txt") {
		t.Fatalf("tracked.txt should be in snapshot, got: %v", files)
	}
}

// writeNestedIgnoredLargeFile builds the tree shape that broke snapshots in
// practice: a subdirectory carrying its own .gitignore that ignores a directory
// holding an oversized file. Git honours .gitignore at every level; anything
// that reads only the root .gitignore does not.
func writeNestedIgnoredLargeFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, ".kilo")
	if err := os.MkdirAll(filepath.Join(nested, "node_modules", "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".gitignore"), []byte("node_modules\n"), 0644); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(nested, "node_modules", "pkg", "bundle.js")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 4096)), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}
