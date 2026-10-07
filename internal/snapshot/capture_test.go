package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fixedTestTime is a stable commit timestamp, so a capture's commit hash does
// not depend on when the test ran.
func fixedTestTime() time.Time {
	return time.Unix(1_700_000_000, 0).UTC()
}

// testCaptureEnv builds an owned manager, a catalog with one active generation,
// and a capture request for a fresh workspace.
func testCaptureEnv(t *testing.T, opts ...ManagerOption) (*Manager, *Catalog, string, string) {
	t.Helper()
	workspace := t.TempDir()
	// testCatalog needs a workspace of its own, so build the manager directly
	// here to keep the capture workspace independent of the catalog directory.
	dataDir := t.TempDir()
	m, err := NewManager(dataDir, opts...)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = m.Release() })

	cat, err := m.CatalogForRoot(workspace)
	if err != nil {
		t.Fatalf("CatalogForRoot: %v", err)
	}
	ctx := context.Background()
	_, genDir := newTestGeneration(t, ctx, cat)
	return m, cat, workspace, genDir
}

// runCapture plans and captures a workspace under a generous allowance.
func runCapture(t *testing.T, m *Manager, cat *Catalog, workspace string, req CaptureRequest) (*captureResult, *capturePlan) {
	t.Helper()
	plan, err := m.planCapture(context.Background(), cat, req)
	if err != nil {
		t.Fatalf("planCapture: %v", err)
	}
	req.Allowance = plan.Allowance
	result, err := m.captureToGeneration(context.Background(), cat, activeID(t, cat), plan, req)
	if err != nil {
		t.Fatalf("captureToGeneration: %v", err)
	}
	return result, plan
}

// activeID returns the catalog's active generation identifier.
func activeID(t *testing.T, cat *Catalog) string {
	t.Helper()
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if man == nil || man.ActiveID == "" {
		t.Fatal("catalog has no active generation")
	}
	return man.ActiveID
}

// captureRequest is a request that captures the whole workspace with no extra
// ignore rules and no per-file cap.
func captureRequest(workspace string) CaptureRequest {
	return CaptureRequest{WorkspaceRoot: workspace, Now: fixedTestTime()}
}

// writeWorkspaceFile writes one file inside a workspace.
func writeWorkspaceFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// trackedPaths lists the paths in a snapshot commit's tree, using git so the
// assertion is about what Git itself reads rather than about Marshal's own
// bookkeeping.
//
// The listing is NUL-delimited because a path may legitimately contain a
// newline: a newline-separated listing would split one name into two, and git
// would also quote it, so the assertion would be about git's display format
// rather than about what was captured.
func trackedPaths(t *testing.T, ctx context.Context, genDir, commitHash string) []string {
	t.Helper()
	out, err := gitEnvErr(ctx, genDir, "ls-tree", "-r", "--name-only", "-z", commitHash)
	if err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	out = strings.TrimSuffix(out, "\x00")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\x00")
}

// ---------------------------------------------------------------------------
// Parentless commits and reachability
// ---------------------------------------------------------------------------

// The produced commit must have NO parent. A parent would make the previous
// snapshot reachable from this one, which is how a pruned store kept ~5,000
// "deleted" commits alive through a single surviving ref.
func TestCaptureProducesAParentlessCommit(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "a.txt", "hello\n")

	result, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))

	body := gitEnv(t, context.Background(), genDir, "cat-file", "-p", result.Hash)
	if strings.Contains(body, "parent ") {
		t.Fatalf("snapshot commit has a parent:\n%s", body)
	}
	if !strings.Contains(body, "tree ") {
		t.Fatalf("snapshot commit has no tree:\n%s", body)
	}
}

// No branch, HEAD, or reflog may retain the commit. Only the explicit snapshot
// ref does. This is the reachability half of the original defect.
func TestSnapshotCommitIsRetainedOnlyByItsSnapshotRef(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "a.txt", "hello\n")

	result, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))
	ctx := context.Background()

	// HEAD is unborn, so no branch names the commit.
	if _, err := gitEnvErr(ctx, genDir, "symbolic-ref", "-q", "HEAD"); err != nil {
		t.Fatalf("HEAD is not a symbolic ref: %v", err)
	}
	if _, err := gitEnvErr(ctx, genDir, "rev-parse", "--verify", "HEAD"); err == nil {
		t.Fatal("HEAD resolves to a commit; a branch would retain it")
	}
	branches := gitEnv(t, ctx, genDir, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if branches != "" {
		t.Fatalf("branches retain commits: %q", branches)
	}
	// No reflog.
	reflogDir := filepath.Join(genDir, "logs")
	if entries, err := os.ReadDir(reflogDir); err == nil && len(entries) > 0 {
		t.Fatalf("a reflog exists at %s and would retain the commit", reflogDir)
	}

	// The only ref is the snapshot ref.
	refs := gitEnv(t, ctx, genDir, "for-each-ref", "--format=%(refname)", "refs/")
	if refs != snapshotRefPrefix+result.Hash {
		t.Fatalf("refs = %q, want only %q", refs, snapshotRefPrefix+result.Hash)
	}

	// The generation is reachable through that ref, so `rev-list --all` sees it
	// exactly once and the reachability set is the snapshot's own closure.
	reachable := gitEnv(t, ctx, genDir, "rev-list", "--all")
	if reachable != result.Hash {
		t.Fatalf("rev-list --all = %q, want just the snapshot %s", reachable, result.Hash)
	}
}

// After the generation is removed the objects are unreachable, and the store's
// own accounting agrees. Deleting the whole generation is what releases space,
// which is what the original defect could not do.
func TestRemovingTheGenerationMakesTheSnapshotUnreachable(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "a.txt", strings.Repeat("hello\n", 512))

	result, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))
	ctx := context.Background()
	genID := activeID(t, cat)

	before, err := m.MeasureWorkspaceGenerations(ctx, cat.Workspace())
	if err != nil {
		t.Fatalf("MeasureWorkspaceGenerations: %v", err)
	}
	if before.ByID[genID] == nil || before.ByID[genID].Allocated == 0 {
		t.Fatalf("generation %s was not measured: %+v", genID, before)
	}
	unreachable, err := m.UnreachableBytes(ctx, genDir)
	if err != nil {
		t.Fatalf("UnreachableBytes: %v", err)
	}
	if unreachable != 0 {
		t.Fatalf("published snapshot has %d unreachable bytes, want 0", unreachable)
	}
	if _, err := cat.MarkDeleting(genID); err != nil {
		t.Fatalf("MarkDeleting: %v", err)
	}
	if err := cat.RemoveGenerationDirectory(genID); err != nil {
		t.Fatalf("RemoveGenerationDirectory: %v", err)
	}
	if _, err := os.Lstat(genDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generation directory survived removal: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(genDir, "objects", result.Hash[:2], result.Hash[2:])); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("commit object survived: %v", err)
	}
}

// ---------------------------------------------------------------------------
// No project filters or external programs
// ---------------------------------------------------------------------------

// A project .gitattributes may declare a clean filter whose command does
// anything at all. Capture must never execute it: the bytes captured are the
// bytes on disk, and a filter that rewrites content would both change what is
// snapshotted and give a repository control of the process.
func TestCaptureNeverExecutesAProjectCleanFilter(t *testing.T) {
	requireUnixShell(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	sentinel := filepath.Join(t.TempDir(), "filter-ran")
	writeWorkspaceFile(t, workspace, "data.bin", "plain content\n")
	writeWorkspaceFile(t, workspace, ".gitattributes", "data.bin filter=evil\n")
	// The filter writes a sentinel file, so its execution is observable.
	writeWorkspaceFile(t, workspace, "evil-filter.sh",
		"#!/bin/sh\nprintf ran > "+shellQuote(sentinel)+"\ncat\n")

	// Pin the filter in the workspace's own repository configuration, which is
	// where a hostile project would put it.
	projectGit := filepath.Join(workspace, ".git")
	if err := os.MkdirAll(projectGit, 0o755); err != nil {
		t.Fatalf("mkdir project git: %v", err)
	}
	runGitInWorkspace(t, workspace, "init", "-q")
	runGitInWorkspace(t, workspace, "config", "filter.evil.clean", "sh "+shellQuote(filepath.Join(workspace, "evil-filter.sh")))

	runCapture(t, m, cat, workspace, captureRequest(workspace))

	if _, err := os.Lstat(sentinel); err == nil {
		t.Fatal("a project clean filter was executed during capture")
	}
	// The captured content is the bytes on disk, unfiltered.
	var dataBlob string
	for _, p := range trackedPaths(t, context.Background(), genDir, snapshotHead(t, genDir)) {
		if p == "data.bin" {
			dataBlob = p
		}
	}
	if dataBlob == "" {
		t.Fatal("data.bin was not captured")
	}
	commit := snapshotHead(t, genDir)
	got := gitEnv(t, context.Background(), genDir, "show", commit+":data.bin")
	if got != "plain content" {
		t.Fatalf("captured content = %q, want the unfiltered bytes", got)
	}
}

// GIT_EXTERNAL_DIFF is cleared from every managed invocation, and a repository
// configuration cannot restore it. A diff that ran a project program would give
// a repository control of the process during a read-only operation.
func TestCaptureAndManagedGitNeverRunExternalPrograms(t *testing.T) {
	requireUnixShell(t)
	sentinel := filepath.Join(t.TempDir(), "external-ran")
	script := filepath.Join(t.TempDir(), "ext.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf ran > "+shellQuote(sentinel)+"\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	m, cat, workspace, _ := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "a.txt", "hello\n")
	runGitInWorkspace(t, workspace, "init", "-q")
	runGitInWorkspace(t, workspace, "config", "diff.external", "sh "+shellQuote(script))
	// The variable is also inherited, which is the other way it reaches git.
	t.Setenv("GIT_EXTERNAL_DIFF", "sh "+shellQuote(script))

	first, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))

	// The sanitized environment clears the inherited variable...
	for _, kv := range sanitizedGitEnv(true) {
		if strings.HasPrefix(kv, "GIT_EXTERNAL_DIFF=") {
			t.Fatalf("sanitized environment still carries %q", kv)
		}
	}
	// ...and an explicit managed diff of the produced commits is run with
	// --no-ext-diff, which is what Service.Diff passes, so the
	// repository-level diff.external cannot reach it either.
	writeWorkspaceFile(t, workspace, "a.txt", "world\n")
	second, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))
	ctx := context.Background()
	out := gitEnv(t, ctx, genDirOf(t, cat), "diff", "--no-ext-diff", first.Hash, second.Hash)
	if !strings.Contains(out, "+world") {
		t.Fatalf("managed diff produced no change:\n%s", out)
	}
	if _, err := os.Lstat(sentinel); err == nil {
		t.Fatal("an external diff program was executed during a managed git invocation")
	}
}

// snapshotHead returns a generation's snapshot ref target.
func snapshotHead(t *testing.T, genDir string) string {
	t.Helper()
	out := gitEnv(t, context.Background(), genDir, "for-each-ref", "--format=%(objectname)", snapshotRefPrefix)
	if out == "" {
		t.Fatal("generation has no snapshot ref")
	}
	return out
}

// runGitInWorkspace runs git inside the project's own repository, which is what
// a hostile project would have configured.
func runGitInWorkspace(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", workspace}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// shellQuote single-quotes a path for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---------------------------------------------------------------------------
// Eligibility
// ---------------------------------------------------------------------------

// Nested .gitignore files, negations, and unusual filenames must all be decided
// by Git itself. The approximate scanner this replaces read only the root
// .gitignore and trimmed every line, so a nested ignore file was invisible and a
// pattern with meaningful leading whitespace was silently rewritten.
func TestEligibilityFollowsGitIgnoreSemantics(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	ctx := context.Background()

	writeWorkspaceFile(t, workspace, "tracked.txt", "tracked\n")
	// A nested ignore file that ignores a directory, plus a negation.
	writeWorkspaceFile(t, workspace, "sub/.gitignore", "*.log\n!keep.log\n")
	writeWorkspaceFile(t, workspace, "sub/drop.log", "ignored\n")
	writeWorkspaceFile(t, workspace, "sub/keep.log", "negated back in\n")
	writeWorkspaceFile(t, workspace, "sub/nested/.gitignore", "inner.tmp\n")
	writeWorkspaceFile(t, workspace, "sub/nested/inner.tmp", "ignored\n")
	writeWorkspaceFile(t, workspace, "sub/nested/kept.txt", "kept\n")
	// Names that a line-oriented scanner mangles: spaces, a leading dash, and
	// an embedded newline.
	writeWorkspaceFile(t, workspace, "dir with space/a b.txt", "spaces\n")
	writeWorkspaceFile(t, workspace, "-leading-dash.txt", "dash\n")
	writeWorkspaceFile(t, workspace, "new\nline.txt", "newline\n")

	result, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))
	got := trackedPaths(t, ctx, genDir, result.Hash)
	want := []string{
		"-leading-dash.txt",
		"dir with space/a b.txt",
		"new\nline.txt",
		"sub/.gitignore",
		"sub/keep.log",
		"sub/nested/.gitignore",
		"sub/nested/kept.txt",
		"tracked.txt",
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("captured paths = %q, want %q", got, want)
	}
}

// Marshal-configured ignore rules must be honoured with Git's own semantics.
func TestEligibilityHonoursConfiguredIgnoreRules(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	ctx := context.Background()
	writeWorkspaceFile(t, workspace, "keep.txt", "keep\n")
	writeWorkspaceFile(t, workspace, "drop.log", "drop\n")
	writeWorkspaceFile(t, workspace, "generated/out.bin", "drop too\n")

	req := captureRequest(workspace)
	req.Ignore = []string{"*.log", "generated/"}
	result, _ := runCapture(t, m, cat, workspace, req)

	got := trackedPaths(t, ctx, genDir, result.Hash)
	if strings.Join(got, ",") != "keep.txt" {
		t.Fatalf("captured paths = %q, want just keep.txt", got)
	}
}

// A file that was captured by an earlier run and is now oversized, and another
// that is now ignored, must both be dropped. Retaining either is the "stale
// index" regression: a persistent index kept the old entry alive.
func TestEligibilityDropsFormerlyCapturedFilesThatAreNowExcluded(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	ctx := context.Background()

	writeWorkspaceFile(t, workspace, "big.bin", strings.Repeat("x", 128))
	writeWorkspaceFile(t, workspace, "ignored.txt", "now ignored\n")
	writeWorkspaceFile(t, workspace, "keep.txt", "keep\n")

	// First capture: everything is eligible.
	first := captureRequest(workspace)
	first.MaxFileBytes = 1 << 20
	firstResult, _ := runCapture(t, m, cat, workspace, first)
	firstPaths := trackedPaths(t, ctx, genDir, firstResult.Hash)
	if len(firstPaths) != 3 {
		t.Fatalf("first capture paths = %q, want all three", firstPaths)
	}

	// Second capture: big.bin is now over the cap and ignored.txt is now
	// ignored.
	writeWorkspaceFile(t, workspace, ".gitignore", "ignored.txt\n")
	second := captureRequest(workspace)
	second.MaxFileBytes = 64
	secondResult, _ := runCapture(t, m, cat, workspace, second)
	secondPaths := trackedPaths(t, ctx, genDir, secondResult.Hash)
	joined := strings.Join(secondPaths, ",")
	if strings.Contains(joined, "big.bin") {
		t.Fatalf("a formerly captured oversized file survived: %q", secondPaths)
	}
	if strings.Contains(joined, "ignored.txt") {
		t.Fatalf("a formerly captured now-ignored file survived: %q", secondPaths)
	}
	if !strings.Contains(joined, "keep.txt") {
		t.Fatalf("keep.txt was dropped: %q", secondPaths)
	}
}

// A deleted file is not captured, and the snapshot does not resurrect it.
func TestEligibilitySkipsDeletedFiles(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	ctx := context.Background()
	writeWorkspaceFile(t, workspace, "keep.txt", "keep\n")
	runCapture(t, m, cat, workspace, captureRequest(workspace))

	if err := os.Remove(filepath.Join(workspace, "keep.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	writeWorkspaceFile(t, workspace, "other.txt", "other\n")
	result, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))
	got := trackedPaths(t, ctx, genDir, result.Hash)
	if strings.Join(got, ",") != "other.txt" {
		t.Fatalf("captured paths = %q, want just other.txt", got)
	}
}

// The project's own .git metadata must never be captured, and neither must a
// nested repository's administration directory.
func TestEligibilityExcludesRepositoryMetadata(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	ctx := context.Background()
	writeWorkspaceFile(t, workspace, "tracked.txt", "tracked\n")
	runGitInWorkspace(t, workspace, "init", "-q")
	if err := os.MkdirAll(filepath.Join(workspace, ".git", "objects", "ab"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeWorkspaceFile(t, workspace, ".git/config", "[core]\n")

	// A nested repository: Git reports it as a single directory entry.
	nested := filepath.Join(workspace, "vendor", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	runGitInWorkspace(t, nested, "init", "-q")
	writeWorkspaceFile(t, nested, "inner.txt", "inner\n")

	result, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))
	got := trackedPaths(t, ctx, genDir, result.Hash)
	for _, p := range got {
		if strings.HasPrefix(p, ".git/") || strings.Contains(p, "/.git/") {
			t.Fatalf("repository metadata was captured: %q", p)
		}
		if strings.HasPrefix(p, "vendor/nested/") {
			t.Fatalf("a nested repository's contents were captured: %q", p)
		}
	}
	if strings.Join(got, ",") != "tracked.txt" {
		t.Fatalf("captured paths = %q, want just tracked.txt", got)
	}
}

// A workspace that contains the managed store must not re-capture it. When the
// workspace is the user's home directory the store lives inside it, and each
// capture would otherwise copy every previous snapshot into the next one.
func TestEligibilityExcludesTheManagedStoreInsideTheWorkspace(t *testing.T) {
	requireGit(t)
	// The workspace IS the data directory's parent, so the store is inside it.
	base := t.TempDir()
	dataDir := filepath.Join(base, "state")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = m.Release() })

	cat, err := m.CatalogForRoot(base)
	if err != nil {
		t.Fatalf("CatalogForRoot: %v", err)
	}
	ctx := context.Background()
	_, genDir := newTestGeneration(t, ctx, cat)

	writeWorkspaceFile(t, base, "home.txt", "home\n")
	// A file inside the managed store, which the exclusion must hide.
	if err := os.MkdirAll(m.Root(), 0o755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.WriteFile(filepath.Join(m.Root(), "junk.bin"), []byte("store contents"), 0o644); err != nil {
		t.Fatalf("write store file: %v", err)
	}

	result, _ := runCapture(t, m, cat, base, captureRequest(base))
	got := trackedPaths(t, ctx, genDir, result.Hash)
	storeRel, err := filepath.Rel(base, m.Root())
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	storeRel = slashPath(storeRel)
	for _, p := range got {
		if p == storeRel || strings.HasPrefix(p, storeRel+"/") {
			t.Fatalf("the managed store was captured into itself: %q", p)
		}
	}
	if strings.Join(got, ",") != "home.txt" {
		t.Fatalf("captured paths = %q, want just home.txt", got)
	}
}

// A symbolic link contributes its target TEXT and is never followed, so a link
// pointing outside the workspace records a path string rather than the content.
func TestEligibilityCapturesSymlinkTargetTextWithoutFollowingIt(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	ctx := context.Background()
	writeWorkspaceFile(t, workspace, "real.txt", "the real content\n")

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("SECRET OUTSIDE CONTENT\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "outside-link")); err != nil {
		t.Skipf("cannot create a symlink: %v", err)
	}
	if err := os.Symlink("real.txt", filepath.Join(workspace, "inside-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	result, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))

	got := gitEnv(t, ctx, genDir, "cat-file", "-p", result.Hash+":outside-link")
	if got != outside {
		t.Fatalf("symlink blob = %q, want the target text %q", got, outside)
	}
	if strings.Contains(got, "SECRET") {
		t.Fatal("the symlink was followed and its target's content captured")
	}
	mode := gitEnv(t, ctx, genDir, "ls-tree", result.Hash, "outside-link")
	if !strings.HasPrefix(mode, "120000 ") {
		t.Fatalf("symlink tree entry = %q, want mode 120000", mode)
	}
}

// An unsupported path type aborts the whole capture rather than being skipped.
// A snapshot that silently omitted what it could not read claims a rollback
// point it does not have.
//
// Git does not report a named pipe as an untracked path at all, so the only way
// a pipe can be noticed is by inspecting the directories the capture actually
// selected from. That is what makes this an abort rather than a silent
// omission.
func TestEligibilityRefusesAnUnsupportedFileType(t *testing.T) {
	requireUnixFifo(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "ok.txt", "ok\n")
	if err := mkFifo(filepath.Join(workspace, "pipe.fifo")); err != nil {
		t.Skipf("cannot create a fifo: %v", err)
	}
	_, err := m.planCapture(context.Background(), cat, captureRequest(workspace))
	if !errors.Is(err, ErrUnsupportedFileType) {
		t.Fatalf("planCapture with a fifo = %v, want ErrUnsupportedFileType", err)
	}
	if err := os.Remove(filepath.Join(workspace, "pipe.fifo")); err != nil {
		t.Fatalf("remove fifo: %v", err)
	}
	// The scan is scoped to the directories the capture actually selects from.
	// A pipe in a directory nothing was selected from contributes nothing to
	// the snapshot, so it must NOT abort the capture — otherwise a workspace
	// with an ignored directory containing a socket could never be captured,
	// which is the unbounded whole-tree walk this design removed.
	writeWorkspaceFile(t, workspace, "ignored-dir/kept.txt", "kept\n")
	writeWorkspaceFile(t, workspace, ".gitignore", "ignored-dir/\n")
	if err := mkFifo(filepath.Join(workspace, "ignored-dir", "unused.fifo")); err != nil {
		t.Skipf("cannot create a fifo: %v", err)
	}
	if _, err := m.planCapture(context.Background(), cat, captureRequest(workspace)); err != nil {
		t.Fatalf("a pipe in an ignored directory aborted the capture: %v", err)
	}
}

// An unreadable path aborts the capture: treating "could not read" as "not
// eligible" is exactly how a multi-gigabyte file entered history despite a size
// cap.
func TestEligibilityAbortsOnUnreadablePath(t *testing.T) {
	requireReadableWorkspace(t)
	requireGit(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "ok.txt", "ok\n")
	locked := filepath.Join(workspace, "locked")
	if err := os.MkdirAll(locked, 0o000); err != nil {
		t.Fatalf("mkdir locked: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	_, err := m.planCapture(context.Background(), cat, captureRequest(workspace))
	if err == nil {
		t.Fatal("planCapture succeeded despite an unenumerable directory")
	}
	if !errors.Is(err, ErrUnreadableFile) {
		t.Fatalf("planCapture error = %v, want ErrUnreadableFile", err)
	}
}

// A capture must not leave behind an artifact only a native Git writer would
// create: an index, a pack, a reflog, a packed-refs file, or a staged message
// file. Each of those is unbounded or unaccounted, and a pack file in
// particular is what this work exists to stop accumulating.
func TestCaptureWritesNoNativeGitArtifacts(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "a.txt", "hello\n")
	writeWorkspaceFile(t, workspace, "sub/b.txt", "world\n")

	result, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))

	// The ref Marshal published must be present as a plain one-line file.
	refPath := filepath.Join(genDir, filepath.FromSlash(snapshotRefPrefix+result.Hash))
	data, err := os.ReadFile(refPath)
	if err != nil {
		t.Fatalf("published ref is not a readable file: %v", err)
	}
	if string(data) != result.Hash+"\n" {
		t.Fatalf("published ref contains %q, want %q", data, result.Hash+"\n")
	}

	// Nothing a native writer would have created may exist.
	disallowed := map[string]bool{
		"index":          true,
		"packed-refs":    true,
		"COMMIT_EDITMSG": true,
		"ORIG_HEAD":      true,
		"MERGE_HEAD":     true,
		"shallow":        true,
		filepath.Join("objects", "info", "packs"): true,
	}
	var found []string
	err = filepath.WalkDir(genDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(genDir, path)
		if relErr != nil {
			return relErr
		}
		if disallowed[rel] {
			found = append(found, rel)
		}
		if d.IsDir() && rel == filepath.Join("objects", "pack") {
			entries, readErr := os.ReadDir(path)
			if readErr != nil {
				return readErr
			}
			for _, e := range entries {
				found = append(found, filepath.Join(rel, e.Name()))
			}
		}
		if d.IsDir() && rel == "logs" {
			found = append(found, rel)
		}
		// A leftover scratch file from an interrupted write must not survive a
		// successful capture.
		if strings.HasPrefix(d.Name(), objectTempPrefix) {
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk generation: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("capture left native git artifacts behind: %v", found)
	}

	// The eligibility scratch index lives outside the store and is gone.
	matches, err := filepath.Glob(filepath.Join(m.Root(), "**", "index"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, match := range matches {
		if strings.Contains(match, objectTempPrefix) {
			continue
		}
		t.Fatalf("an index was written under the managed store: %s", match)
	}
}

// ---------------------------------------------------------------------------
// Canonical trees
// ---------------------------------------------------------------------------

// Tree entries must be written in Git's canonical order: sorted by name bytes,
// with a tree's name compared as if it had a trailing slash.
//
// The assertion is not "the order looks right" — that would only re-read the
// order this code wrote. It rebuilds the root tree with `git mktree` from the
// same entries and requires Git's own canonical ordering to produce the SAME
// tree hash. A different ordering produces a different hash, so the two
// agreeing is proof that the ordering rule matches Git's.
func TestTreeOrderingIsCanonicalForFilesAndNestedDirectories(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	ctx := context.Background()

	// The ordering trap: a directory named "a" compares as "a/", and '/' sorts
	// AFTER '-', so the file "a-b.txt" must come BEFORE the tree "a".
	writeWorkspaceFile(t, workspace, "a/inner.txt", "inner\n")
	writeWorkspaceFile(t, workspace, "a-b.txt", "dash\n")
	writeWorkspaceFile(t, workspace, "a.txt", "dot\n")

	result, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))

	// git fsck validates every object's structure, including trees.
	gitEnv(t, ctx, genDir, "fsck", "--strict")

	rootTree := commitTreeHash(t, ctx, genDir, result.Hash)
	entries := gitEnv(t, ctx, genDir, "ls-tree", result.Hash)
	wantOrder := []string{"a-b.txt", "a.txt", "a"}
	lines := strings.Split(entries, "\n")
	if len(lines) != len(wantOrder) {
		t.Fatalf("root tree has %d entries, want %d: %q", len(lines), len(wantOrder), entries)
	}
	for i, line := range lines {
		name := line[strings.LastIndex(line, "\t")+1:]
		if name != wantOrder[i] {
			t.Fatalf("tree entry %d = %q, want %q (whole tree: %q)", i, name, wantOrder[i], entries)
		}
	}
	// Git's own canonical ordering of the same entries must yield the same tree
	// object hash that Marshal computed.
	rebuilt := gitEnvStdin(t, ctx, genDir, []byte(entries+"\n"), "mktree")
	if rebuilt != rootTree {
		t.Fatalf("git mktree rebuilt the root tree as %s, but Marshal computed %s", rebuilt, rootTree)
	}
}

// commitTreeHash returns the tree hash a commit names.
func commitTreeHash(t *testing.T, ctx context.Context, genDir, commitHash string) string {
	t.Helper()
	for _, line := range strings.Split(gitEnv(t, ctx, genDir, "cat-file", "-p", commitHash), "\n") {
		if rest, ok := strings.CutPrefix(line, "tree "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("commit %s has no tree line", commitHash)
	return ""
}

// A file with the executable bit set is recorded as 100755 and one without as
// 100644, which is how a restored snapshot preserves whether a script runs.
func TestTreeRecordsExecutableMode(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	ctx := context.Background()
	writeWorkspaceFile(t, workspace, "script.sh", "#!/bin/sh\n")
	writeWorkspaceFile(t, workspace, "plain.txt", "plain\n")
	if err := os.Chmod(filepath.Join(workspace, "script.sh"), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	result, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))
	for _, tc := range []struct{ name, wantMode string }{
		{"script.sh", "100755"},
		{"plain.txt", "100644"},
	} {
		line := gitEnv(t, ctx, genDir, "ls-tree", result.Hash, tc.name)
		if !strings.HasPrefix(line, tc.wantMode+" ") {
			t.Fatalf("%s tree entry = %q, want mode %s", tc.name, line, tc.wantMode)
		}
	}
}

// ---------------------------------------------------------------------------
// Resource bounds
// ---------------------------------------------------------------------------

// The entry-count bound aborts visibly at its boundary rather than truncating
// the snapshot.
func TestCaptureEntryCountBoundAbortsAtTheBoundary(t *testing.T) {
	requireGit(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	for i := 0; i < 4; i++ {
		writeWorkspaceFile(t, workspace, "f"+string(rune('a'+i))+".txt", "x\n")
	}
	req := captureRequest(workspace)
	req.Bounds = captureBounds{MaxEntries: 4}
	if _, err := m.planCapture(context.Background(), cat, req); err != nil {
		t.Fatalf("planCapture at the bound failed: %v", err)
	}
	req.Bounds = captureBounds{MaxEntries: 3}
	if _, err := m.planCapture(context.Background(), cat, req); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("planCapture over the entry bound = %v, want ErrBudgetExhausted", err)
	}
}

// The PRODUCTION entry bound is 100,000, and it must abort at that boundary
// rather than silently truncating. The test builds just over the bound so the
// assertion is about the real configured number, not a test-only one.
func TestCaptureProductionEntryBoundAbortsAtItsBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds 100,001 files")
	}
	requireGit(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	// One file over the production bound.
	for i := 0; i <= defaultCaptureMaxEntries; i++ {
		name := fmt.Sprintf("d%02d/f%06d.txt", i%100, i)
		full := filepath.Join(workspace, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	_, err := m.planCapture(context.Background(), cat, captureRequest(workspace))
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("planCapture over the production entry bound = %v, want ErrBudgetExhausted", err)
	}
}

// The production selection bounds are the numbers the design specifies, and
// normalization must not turn an unset bound into "unlimited".
func TestCaptureProductionBoundsAndNormalization(t *testing.T) {
	def := defaultCaptureBounds()
	if def.MaxEntries != 100_000 {
		t.Fatalf("default entry bound = %d, want 100000", def.MaxEntries)
	}
	if def.MaxPathBytes != 4096 {
		t.Fatalf("default per-path bound = %d, want 4096", def.MaxPathBytes)
	}
	if def.MaxTotalPathBytes != 16<<20 {
		t.Fatalf("default total-path bound = %d, want %d", def.MaxTotalPathBytes, int64(16<<20))
	}
	// Zero means "use the default", not "no bound".
	normalized, err := captureBounds{}.normalized()
	if err != nil {
		t.Fatalf("normalized: %v", err)
	}
	if normalized != def {
		t.Fatalf("normalized zero bounds = %+v, want %+v", normalized, def)
	}
}

// The total-path bound is enforced independently of the per-path bound: two
// paths that are each individually legal are refused once their SUM crosses the
// total. That is what keeps a tree of many long names from turning into an
// unbounded tree payload.
func TestCaptureTotalPathBoundIsEnforcedIndependentlyOfPerPath(t *testing.T) {
	requireGit(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	// Two paths that each fit comfortably under the per-path bound.
	first := strings.Repeat("a", 200) + ".txt"
	second := strings.Repeat("b", 200) + ".txt"
	writeWorkspaceFile(t, workspace, first, "x\n")
	writeWorkspaceFile(t, workspace, second, "y\n")
	total := int64(len(first) + len(second))

	// At the exact sum: accepted, and the per-path bound is not what is being
	// measured here because both paths are far shorter than it.
	req := captureRequest(workspace)
	req.Bounds = captureBounds{MaxTotalPathBytes: total}
	if _, err := m.planCapture(context.Background(), cat, req); err != nil {
		t.Fatalf("planCapture at the exact total-path bound failed: %v", err)
	}
	// One byte under: refused.
	req.Bounds = captureBounds{MaxTotalPathBytes: total - 1}
	if _, err := m.planCapture(context.Background(), cat, req); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("planCapture one byte over the total-path bound = %v, want ErrBudgetExhausted", err)
	}
}

// The production per-path and total-path bounds must abort exactly at their
// boundaries.
//
// The boundary arithmetic is asserted directly rather than through real files,
// and deliberately so: a path at the 4096-byte production bound cannot exist on
// a filesystem whose PATH_MAX is 4096, so a test that could only build real
// paths could never exercise the number this code is guarding. Real-file
// behaviour at smaller bounds is covered by the tests above.
func TestCaptureProductionPathBoundsAbortExactlyAtTheirBoundaries(t *testing.T) {
	m, _, _, _ := testCaptureEnv(t)
	bounds := defaultCaptureBounds()
	const workTree = "/workspace"

	// At the per-path bound: accepted.
	atBound := strings.Repeat("a", defaultCaptureMaxPathBytes)
	total, err := m.checkPathBounds(workTree, atBound, bounds, 0)
	if err != nil {
		t.Fatalf("path at the production per-path bound was refused: %v", err)
	}
	if total != int64(defaultCaptureMaxPathBytes) {
		t.Fatalf("running total = %d, want %d", total, int64(defaultCaptureMaxPathBytes))
	}
	// One byte over: refused.
	if _, err := m.checkPathBounds(workTree, atBound+"a", bounds, 0); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("path one byte over the per-path bound = %v, want ErrBudgetExhausted", err)
	}

	// The total bound is checked against the RUNNING sum, so a path that is
	// individually legal is refused once the running total crosses it.
	chunk := strings.Repeat("b", defaultCaptureMaxPathBytes)
	if _, err := m.checkPathBounds(workTree, chunk, bounds, 1<<20); err != nil {
		t.Fatalf("running total well under the bound was refused: %v", err)
	}
	atTotal := defaultCaptureMaxTotalPathBytes - int64(len(chunk))
	if _, err := m.checkPathBounds(workTree, chunk, bounds, atTotal); err != nil {
		t.Fatalf("running total reaching exactly the total bound was refused: %v", err)
	}
	if _, err := m.checkPathBounds(workTree, chunk, bounds, atTotal+1); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("running total one byte over the total bound = %v, want ErrBudgetExhausted", err)
	}
}

// The per-path bound aborts at its boundary.
func TestCapturePerPathBoundAbortsAtTheBoundary(t *testing.T) {
	requireGit(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "name.txt", "x\n")

	req := captureRequest(workspace)
	req.Bounds = captureBounds{MaxPathBytes: len("name.txt")}
	if _, err := m.planCapture(context.Background(), cat, req); err != nil {
		t.Fatalf("planCapture at the per-path bound failed: %v", err)
	}
	req.Bounds = captureBounds{MaxPathBytes: len("name.txt") - 1}
	if _, err := m.planCapture(context.Background(), cat, req); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("planCapture over the per-path bound = %v, want ErrBudgetExhausted", err)
	}
}

// The total-path bound aborts at its boundary.
func TestCaptureTotalPathBoundAbortsAtTheBoundary(t *testing.T) {
	requireGit(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "aa.txt", "x\n")
	writeWorkspaceFile(t, workspace, "bb.txt", "y\n")
	const total int64 = 2 * int64(len("aa.txt"))

	req := captureRequest(workspace)
	req.Bounds = captureBounds{MaxTotalPathBytes: total}
	if _, err := m.planCapture(context.Background(), cat, req); err != nil {
		t.Fatalf("planCapture at the total-path bound failed: %v", err)
	}
	req.Bounds = captureBounds{MaxTotalPathBytes: total - 1}
	if _, err := m.planCapture(context.Background(), cat, req); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("planCapture over the total-path bound = %v, want ErrBudgetExhausted", err)
	}
}

// A negative bound is refused rather than treated as unlimited.
func TestCaptureBoundsRejectNegativeValues(t *testing.T) {
	for _, b := range []captureBounds{
		{MaxEntries: -1},
		{MaxPathBytes: -1},
		{MaxTotalPathBytes: -1},
	} {
		if _, err := b.normalized(); !errors.Is(err, ErrInvalidLimits) {
			t.Fatalf("normalized(%+v) = %v, want ErrInvalidLimits", b, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Write allowance
// ---------------------------------------------------------------------------

// A capture admitted with too small an allowance fails with a budget error and
// leaves an EMPTY hash. Returning a usable-looking hash for a capture that
// could not write its objects is the failure mode that makes a broken snapshot
// look like a rollback point.
func TestCaptureRefusesWhenTheAllowanceIsTooSmall(t *testing.T) {
	requireGit(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "a.txt", strings.Repeat("content\n", 1024))

	req := captureRequest(workspace)
	req.Allowance = 128
	_, err := m.captureToGeneration(context.Background(), cat, activeID(t, cat), &capturePlan{
		Entries:   eligibleEntries(t, m, cat, workspace, req),
		Allowance: req.Allowance,
	}, req)
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("capture with a tiny allowance = %v, want ErrBudgetExhausted", err)
	}
	// Nothing was published.
	if _, statErr := os.Stat(filepath.Join(genDirOf(t, cat), "refs", "snapshots")); statErr == nil {
		entries, _ := os.ReadDir(filepath.Join(genDirOf(t, cat), "refs", "snapshots"))
		if len(entries) > 0 {
			t.Fatalf("a failed capture published a ref: %v", entries)
		}
	}
}

// eligibleEntries discovers the paths a plan would select.
func eligibleEntries(t *testing.T, m *Manager, cat *Catalog, workspace string, req CaptureRequest) []eligibleEntry {
	t.Helper()
	plan, err := m.planCapture(context.Background(), cat, req)
	if err != nil {
		t.Fatalf("planCapture: %v", err)
	}
	return plan.Entries
}

// genDirOf returns a catalog's active generation directory.
func genDirOf(t *testing.T, cat *Catalog) string {
	t.Helper()
	dir, err := cat.GenerationDir(activeID(t, cat))
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	return dir
}

// The plan's estimate must be an upper bound on what a capture actually spends.
// An estimate that under-reserves is not a bound, and admission built on it
// would admit writes the budget never allowed.
func TestPlanEstimateBoundsActualSpend(t *testing.T) {
	requireGit(t)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "compressible", data: []byte(strings.Repeat("a", 200<<10))},
		{name: "incompressible", data: incompressibleBytes(t, 200<<10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, cat, workspace, _ := testCaptureEnv(t)
			writeWorkspaceFile(t, workspace, "sub/data.bin", string(tc.data))
			writeWorkspaceFile(t, workspace, "top.txt", "top\n")

			plan, err := m.planCapture(context.Background(), cat, captureRequest(workspace))
			if err != nil {
				t.Fatalf("planCapture: %v", err)
			}
			req := captureRequest(workspace)
			req.Allowance = plan.Allowance
			result, err := m.captureToGeneration(context.Background(), cat, activeID(t, cat), plan, req)
			if err != nil {
				t.Fatalf("captureToGeneration: %v", err)
			}
			if result.Spent > plan.Allowance {
				t.Fatalf("capture spent %d bytes, over its %d byte plan estimate",
					result.Spent, plan.Allowance)
			}
			if result.Verified == 0 {
				t.Fatal("no object hash was verified against git")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Cancellation
// ---------------------------------------------------------------------------

// A cancelled context aborts capture, leaves an empty hash, and does not leave
// a published ref behind.
func TestCancelledCaptureLeavesNothingPublished(t *testing.T) {
	requireGit(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "a.txt", "hello\n")

	ctx, cancel := context.WithCancel(context.Background())
	plan, err := m.planCapture(ctx, cat, captureRequest(workspace))
	if err != nil {
		t.Fatalf("planCapture: %v", err)
	}
	req := captureRequest(workspace)
	req.Allowance = plan.Allowance
	cancel()

	result, err := m.captureToGeneration(ctx, cat, activeID(t, cat), plan, req)
	if err == nil {
		t.Fatal("a cancelled capture succeeded")
	}
	if result != nil && result.Hash != "" {
		t.Fatalf("cancelled capture returned a usable hash %q", result.Hash)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled capture error = %v, want context.Canceled", err)
	}
	if !errors.Is(err, ErrInterruptedCapture) {
		t.Fatalf("cancelled capture error = %v, want ErrInterruptedCapture", err)
	}
	refsDir := filepath.Join(genDirOf(t, cat), "refs", "snapshots")
	if entries, readErr := os.ReadDir(refsDir); readErr == nil && len(entries) > 0 {
		t.Fatalf("a cancelled capture published a ref: %v", entries)
	}
}

// A cancellation between blobs removes every partially written object rather
// than leaving unaccounted bytes behind.
func TestCancelledCaptureRemovesPartialObjects(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "a.txt", "hello\n")
	writeWorkspaceFile(t, workspace, "b.txt", "world\n")

	ctx, cancel := context.WithCancel(context.Background())
	plan, err := m.planCapture(ctx, cat, captureRequest(workspace))
	if err != nil {
		t.Fatalf("planCapture: %v", err)
	}
	req := captureRequest(workspace)
	req.Allowance = plan.Allowance
	cancel()
	if _, err := m.captureToGeneration(ctx, cat, activeID(t, cat), plan, req); err == nil {
		t.Fatal("a cancelled capture succeeded")
	}
	// No temp object file survives.
	matches, err := filepath.Glob(filepath.Join(genDir, "objects", incomingDirName, objectTempPrefix+"*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("cancelled capture left partial objects: %v", matches)
	}
}

// A cancellation while a git subprocess is running must leave no child process
// alive. Releasing store ownership after a cancelled capture while a writer
// still runs is how a store is corrupted by a process nobody is tracking.
func TestCancelledEligibilityLeavesNoChildProcessAlive(t *testing.T) {
	requireGit(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "a.txt", "hello\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.planCapture(ctx, cat, captureRequest(workspace)); err == nil {
		t.Fatal("planCapture on a cancelled context succeeded")
	}
	// The manager tracks every process tree it starts and drains them on
	// release. After release, nothing this manager started may remain, and the
	// tracked set must be empty.
	if err := m.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	m.mu.Lock()
	live := len(m.trees)
	m.mu.Unlock()
	if live != 0 {
		t.Fatalf("%d process trees still tracked after release", live)
	}
}

// A cancellation DURING a git subprocess kills the tree rather than waiting for
// it. The command is a sleep, so a tree that survived cancellation would keep
// the capture (and store ownership) alive for its whole duration.
func TestCancelledGitSubprocessIsKilled(t *testing.T) {
	requireUnixShell(t)
	m, _, _, _ := testCaptureEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		// A sleep is used rather than a git subcommand because the assertion is
		// about process-tree lifetime, and git has no sleep. The manager's
		// tracked-tree machinery is the same either way.
		cmd := exec.Command("sh", "-c", "sleep 30")
		_, err := m.RunManaged(ctx, cmd)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled managed command reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a cancelled managed command was not killed")
	}
}

// ---------------------------------------------------------------------------
// Compatibility
// ---------------------------------------------------------------------------

// The objects a capture produces must be consumable by the machinery Diff and
// Restore are built on: blobs and trees must read back by path, two snapshots
// must diff, and a snapshot must restore into a work tree with a read-only
// checkout.
//
// Diffing a snapshot against a LIVE work tree additionally needs an index, and
// the store forbids native Git from writing one inside the managed root, so that
// index is Task 6's work. What is asserted here is everything that does not
// depend on it, and the checkout below pins GIT_INDEX_FILE outside the store to
// prove the mechanism Task 6 will use actually works.
func TestCapturedSnapshotIsConsumableByDiffAndRestore(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	ctx := context.Background()
	writeWorkspaceFile(t, workspace, "a.txt", "hello\n")
	first, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))

	// A blob reads back by path.
	if got := gitEnv(t, ctx, genDir, "cat-file", "-p", first.Hash+":a.txt"); got != "hello" {
		t.Fatalf("cat-file of a captured blob = %q, want %q", got, "hello")
	}

	// Two snapshots diff against each other, which is the tree comparison Diff
	// is built on and needs no index.
	writeWorkspaceFile(t, workspace, "a.txt", "world\n")
	second, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))
	out := gitEnv(t, ctx, genDir, "diff", "--no-ext-diff", first.Hash, second.Hash)
	if !strings.Contains(out, "-hello") || !strings.Contains(out, "+world") {
		t.Fatalf("diff between two snapshots is missing the change:\n%s", out)
	}

	// Restore: checkout into a work tree, with the index pinned OUTSIDE the
	// managed root so no native Git write lands in the store.
	work := t.TempDir()
	writeWorkspaceFile(t, work, "a.txt", "world\n")
	gitEnvInWorkTree(t, ctx, genDir, work, "checkout", first.Hash, "--", ".")
	data, err := os.ReadFile(filepath.Join(work, "a.txt"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("restored content = %q, want %q", data, "hello\n")
	}
	if _, err := os.Lstat(filepath.Join(genDir, "index")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore wrote an index inside the managed store: %v", err)
	}
}

// gitEnvInWorkTree runs a git command against a generation repository with an
// explicit work tree and an index pinned outside the store.
func gitEnvInWorkTree(t *testing.T, ctx context.Context, gitDir, workTree string, args ...string) string {
	t.Helper()
	cmd := newGitCmd(ctx, "git", gitDir, workTree, true, args...)
	cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+filepath.Join(t.TempDir(), "index"))
	out, err := runGitCombined(ctx, cmd, defaultMaxCommandOutput)
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, gitDir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// An empty capture is refused rather than producing an empty tree: a snapshot
// that records nothing would look like a rollback point for a workspace it
// never inspected.
func TestCaptureRefusesAWorkspaceWithNoEligiblePaths(t *testing.T) {
	requireGit(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	req := captureRequest(workspace)
	plan, err := m.planCapture(context.Background(), cat, req)
	if err != nil {
		t.Fatalf("planCapture: %v", err)
	}
	if len(plan.Entries) != 0 {
		t.Fatalf("expected no eligible paths, got %d", len(plan.Entries))
	}
	req.Allowance = plan.Allowance + int64(DefaultLimits().AllocationUnitBytes)
	if _, err := m.captureToGeneration(context.Background(), cat, activeID(t, cat), plan, req); !errors.Is(err, ErrStoreInternal) {
		t.Fatalf("capturing an empty workspace = %v, want an internal error", err)
	}
}

// Capture refuses a generation repository that is not initialised, rather than
// writing objects into whatever directory happens to be named.
func TestCaptureRefusesUninitialisedGeneration(t *testing.T) {
	requireGit(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "a.txt", "hello\n")
	req := captureRequest(workspace)
	plan, err := m.planCapture(context.Background(), cat, req)
	if err != nil {
		t.Fatalf("planCapture: %v", err)
	}
	req.Allowance = plan.Allowance
	other, err := cat.CreateGeneration(context.Background(), "")
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	dir, err := cat.GenerationDir(other.ID)
	if err != nil {
		t.Fatalf("GenerationDir: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove generation dir: %v", err)
	}
	if _, err := m.captureToGeneration(context.Background(), cat, other.ID, plan, req); !errors.Is(err, ErrUnreadableFile) {
		t.Fatalf("capture into a missing generation = %v, want ErrUnreadableFile", err)
	}
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

// CaptureSnapshot is the Task-5-facing entry point. A failure must return the
// EMPTY hash, so "capture failed" can never be recorded as a usable rollback
// point.
func TestCaptureSnapshotReturnsEmptyHashOnFailure(t *testing.T) {
	requireGit(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "a.txt", strings.Repeat("content\n", 4096))

	// Too small an allowance: the capture cannot write its objects.
	req := captureRequest(workspace)
	req.Allowance = 64
	hash, err := m.CaptureSnapshot(context.Background(), cat, activeID(t, cat), req)
	if err == nil {
		t.Fatal("CaptureSnapshot with a tiny allowance succeeded")
	}
	if hash != "" {
		t.Fatalf("failed CaptureSnapshot returned the usable-looking hash %q", hash)
	}
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("CaptureSnapshot error = %v, want ErrBudgetExhausted", err)
	}
	// No ref was published.
	refs := filepath.Join(genDirOf(t, cat), "refs", "snapshots")
	if entries, readErr := os.ReadDir(refs); readErr == nil && len(entries) > 0 {
		t.Fatalf("a failed CaptureSnapshot published a ref: %v", entries)
	}
}

// CaptureSnapshot succeeds with the plan's own estimate and returns the hash
// that names the published commit.
func TestCaptureSnapshotPublishesAndReturnsItsHash(t *testing.T) {
	requireGit(t)
	m, cat, workspace, genDir := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "a.txt", "hello\n")

	// The estimate comes from a plan, exactly as admission will supply it.
	plan, err := m.planCapture(context.Background(), cat, captureRequest(workspace))
	if err != nil {
		t.Fatalf("planCapture: %v", err)
	}
	req := captureRequest(workspace)
	req.Allowance = plan.Allowance

	hash, err := m.CaptureSnapshot(context.Background(), cat, activeID(t, cat), req)
	if err != nil {
		t.Fatalf("CaptureSnapshot: %v", err)
	}
	if !validObjectHash(hash) {
		t.Fatalf("CaptureSnapshot returned %q, which is not an object id", hash)
	}
	if got := snapshotHead(t, genDir); got != hash {
		t.Fatalf("published ref names %s but CaptureSnapshot returned %s", got, hash)
	}
	gitEnv(t, context.Background(), genDir, "fsck", "--strict")
}

// Capturing identical content with a fixed clock and identity produces the
// identical commit hash, because the commit has no parent and no environment
// input beyond those. A parent or a wall-clock timestamp would make the same
// content hash differently on every run.
func TestCaptureIsReproducibleForIdenticalContent(t *testing.T) {
	requireGit(t)
	m, cat, workspace, _ := testCaptureEnv(t)
	writeWorkspaceFile(t, workspace, "a.txt", "hello\n")
	first, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))
	second, _ := runCapture(t, m, cat, workspace, captureRequest(workspace))
	if first.Hash != second.Hash {
		t.Fatalf("identical content produced different commits: %s and %s", first.Hash, second.Hash)
	}
}
