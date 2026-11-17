package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The snapshot tests drive the real routes, the real service and real git — but
// everything they touch is a t.TempDir(): no adapter process, no network, no
// user file. A capture that must see a concurrent edit, or be cancelled
// mid-write, is steered through the service's injected seams instead of a
// sleep.

// snapshotWorkspace creates a planner whose workspace is a real checkout with
// one commit, so the base commit, its contents and the repository's ignore
// rules are git's own answers rather than a fake this test invented.
func snapshotWorkspace(t *testing.T, env *plannerTestEnv) (PlannerView, string) {
	t.Helper()
	if env.fleet.git == nil {
		t.Skip("git is not installed")
	}
	view := env.mustCreate(nil)
	dir := workspaceDirFor(env.stateDir, view.WorkspaceID)
	mustGit(t, dir, "init", "--initial-branch=main")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "Test")
	snapshotWriteFile(t, dir, "a.txt", "one\ntwo\n", 0o644)
	snapshotWriteFile(t, dir, "sub/b.txt", "bee\n", 0o644)
	snapshotWriteFile(t, dir, ".gitignore", "*.log\nbuild/\n", 0o644)
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-m", "base")
	return view, dir
}

func snapshotWriteFile(t *testing.T, dir, rel, content string, mode os.FileMode) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatalf("create directory for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	if err := os.Chmod(abs, mode); err != nil {
		t.Fatalf("chmod %s: %v", rel, err)
	}
}

func snapshotGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (e *plannerTestEnv) captureSnapshot(id string, req PlannerSnapshotRequest) *httptest.ResponseRecorder {
	e.t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		e.t.Fatalf("encode snapshot body: %v", err)
	}
	return e.do(http.MethodPost, "/api/planners/"+id+"/snapshots", string(raw))
}

func decodeSnapshotReply(t *testing.T, w *httptest.ResponseRecorder) PlannerSnapshotReply {
	t.Helper()
	if w.Code != http.StatusCreated {
		t.Fatalf("capture snapshot: status %d, body %s", w.Code, w.Body.String())
	}
	var reply PlannerSnapshotReply
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode snapshot reply: %v (body %s)", err, w.Body.String())
	}
	return reply
}

func (e *plannerTestEnv) mustCapture(id string, req PlannerSnapshotRequest) PlannerSnapshotReply {
	e.t.Helper()
	return decodeSnapshotReply(e.t, e.captureSnapshot(id, req))
}

func (e *plannerTestEnv) getSnapshot(id, snapshotID string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodGet, "/api/planners/"+id+"/snapshots/"+snapshotID, "")
}

// blobDigests lists the content-addressed store, so a test can prove that a
// refused path wrote nothing.
func (e *plannerTestEnv) blobDigests(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.stateDir, plannerDirName, plannerBlobDirName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("read blob directory: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		out = append(out, entry.Name())
	}
	sort.Strings(out)
	return out
}

// snapshotRecordIDs lists the durable snapshot records.
func (e *plannerTestEnv) snapshotRecordIDs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.stateDir, plannerDirName, plannerSnapshotDirName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("read snapshot directory: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		out = append(out, entry.Name())
	}
	return out
}

func snapshotMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func snapshotFilesByPath(reply PlannerSnapshotReply) map[string]PlannerSnapshotFile {
	out := make(map[string]PlannerSnapshotFile, len(reply.Files))
	for _, file := range reply.Files {
		out[file.Path] = file
	}
	return out
}

func snapshotArtifactOfKind(t *testing.T, reply PlannerSnapshotReply, kind string) PlannerSnapshotArtifact {
	t.Helper()
	for _, artifact := range reply.Artifacts {
		if artifact.Kind == kind {
			return artifact
		}
	}
	t.Fatalf("snapshot has no %q artifact: %v", kind, reply.Artifacts)
	return PlannerSnapshotArtifact{}
}

// TestPlannerSnapshotSelectedOnly: a capture freezes exactly the selected
// files and the approved text, and reports nothing a caller did not send.
func TestPlannerSnapshotSelectedOnly(t *testing.T) {
	env := newPlannerTestEnv(t)
	view, dir := snapshotWorkspace(t, env)

	// The worktree moves after the base commit: one tracked file edited, one
	// untracked file added, one tracked file left alone and unselected.
	snapshotWriteFile(t, dir, "a.txt", "one\nTWO\n", 0o644)
	snapshotWriteFile(t, dir, "new.txt", "fresh\n", 0o644)

	w := env.captureSnapshot(view.ID, PlannerSnapshotRequest{
		Plan:  "the plan",
		Spec:  "the spec",
		Paths: []string{"a.txt", "new.txt"},
	})
	reply := decodeSnapshotReply(t, w)

	if !validPlannerID(reply.ID) {
		t.Fatalf("snapshot id %q is not bridge-minted", reply.ID)
	}
	if head := snapshotGitOut(t, dir, "rev-parse", "HEAD"); reply.BaseCommit != head {
		t.Errorf("base commit = %q, want the workspace HEAD %q", reply.BaseCommit, head)
	}
	if len(reply.Files) != 2 {
		t.Fatalf("captured %d files, want 2: %v", len(reply.Files), reply.Files)
	}
	files := snapshotFilesByPath(reply)

	modified, ok := files["a.txt"]
	if !ok {
		t.Fatalf("a.txt was not captured: %v", reply.Files)
	}
	if modified.Operation != plannerFileModify {
		t.Errorf("a.txt operation = %q, want %q", modified.Operation, plannerFileModify)
	}
	if modified.BaseSHA256 != plannerDigest([]byte("one\ntwo\n")) {
		t.Errorf("a.txt base hash does not name the base commit's bytes")
	}
	if modified.ContentSHA256 != plannerDigest([]byte("one\nTWO\n")) {
		t.Errorf("a.txt content hash does not name the captured bytes")
	}
	if modified.Executable {
		t.Errorf("a.txt is reported executable")
	}
	for _, want := range []string{"--- a/a.txt", "+++ b/a.txt", "@@ -1,2 +1,2 @@", " one", "-two", "+TWO"} {
		if !strings.Contains(modified.Diff, want) {
			t.Errorf("a.txt diff is missing %q:\n%s", want, modified.Diff)
		}
	}

	created, ok := files["new.txt"]
	if !ok {
		t.Fatalf("new.txt was not captured: %v", reply.Files)
	}
	if created.Operation != plannerFileCreate {
		t.Errorf("new.txt operation = %q, want %q", created.Operation, plannerFileCreate)
	}
	if created.BaseSHA256 != "" {
		t.Errorf("an untracked file has base hash %q, want none", created.BaseSHA256)
	}
	for _, want := range []string{"--- /dev/null", "+++ b/new.txt", "+fresh"} {
		if !strings.Contains(created.Diff, want) {
			t.Errorf("new.txt diff is missing %q:\n%s", want, created.Diff)
		}
	}
	if _, extra := files["sub/b.txt"]; extra {
		t.Errorf("an unselected file was captured: %v", reply.Files)
	}

	// The documents are frozen with their own hashes.
	if len(reply.Artifacts) != 2 {
		t.Fatalf("artifacts = %v, want plan and spec", reply.Artifacts)
	}
	plan := snapshotArtifactOfKind(t, reply, plannerArtifactPlan)
	if plan.Content != "the plan" || plan.SHA256 != plannerDigest([]byte("the plan")) {
		t.Errorf("plan artifact = %+v, want the supplied text and its hash", plan)
	}
	spec := snapshotArtifactOfKind(t, reply, plannerArtifactSpec)
	if spec.Content != "the spec" || spec.SHA256 != plannerDigest([]byte("the spec")) {
		t.Errorf("spec artifact = %+v, want the supplied text and its hash", spec)
	}

	// The snapshot hash covers the base commit and the ordered files, nothing
	// else: recomputing it is the check a reviewer performs.
	edit := EditSnapshot{BaseCommit: reply.BaseCommit}
	for _, file := range reply.Files {
		edit.Files = append(edit.Files, SelectedFile{
			Path:          file.Path,
			Operation:     file.Operation,
			BaseSHA256:    file.BaseSHA256,
			ContentSHA256: file.ContentSHA256,
			Executable:    file.Executable,
		})
	}
	wantDigest, err := plannerSnapshotDigest(edit)
	if err != nil {
		t.Fatalf("recompute snapshot digest: %v", err)
	}
	if reply.SHA256 != wantDigest {
		t.Errorf("snapshot hash = %q, want %q over the base commit and files", reply.SHA256, wantDigest)
	}

	// Bytes live in the bridge's own store, private, keyed by their hash.
	blobs := env.blobDigests(t)
	if len(blobs) != 2 {
		t.Fatalf("blob store holds %v, want the two captured files", blobs)
	}
	for _, digest := range blobs {
		path := filepath.Join(env.stateDir, plannerDirName, plannerBlobDirName, digest)
		if mode := snapshotMode(t, path); mode != plannerFileMode {
			t.Errorf("blob %s mode = %o, want %o", digest, mode, plannerFileMode)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read blob %s: %v", digest, err)
		}
		if plannerDigest(data) != digest {
			t.Errorf("blob %s does not hold its own bytes", digest)
		}
	}
	if mode := snapshotMode(t, filepath.Join(env.stateDir, plannerDirName, plannerBlobDirName)); mode != plannerDirMode {
		t.Errorf("blob directory mode = %o, want %o", mode, plannerDirMode)
	}
	records := env.snapshotRecordIDs(t)
	if len(records) != 1 || records[0] != reply.ID+".json" {
		t.Fatalf("snapshot records = %v, want just %s.json", records, reply.ID)
	}
	if mode := snapshotMode(t, filepath.Join(env.stateDir, plannerDirName, plannerSnapshotDirName, reply.ID+".json")); mode != plannerFileMode {
		t.Errorf("snapshot record mode = %o, want %o", mode, plannerFileMode)
	}

	// No host path is reported for review.
	body := w.Body.String()
	for _, hostPath := range []string{dir, env.stateDir, env.root} {
		if strings.Contains(body, hostPath) {
			t.Errorf("snapshot response leaks the host path %q: %s", hostPath, body)
		}
	}

	// The review is the same handoff, read back through the route.
	review := env.getSnapshot(view.ID, reply.ID)
	if review.Code != http.StatusOK {
		t.Fatalf("GET snapshot status = %d, want 200 (body %s)", review.Code, review.Body.String())
	}
	var frozen PlannerSnapshotView
	if err := json.Unmarshal(review.Body.Bytes(), &frozen); err != nil {
		t.Fatalf("decode snapshot view: %v (body %s)", err, review.Body.String())
	}
	if frozen.SHA256 != reply.SHA256 || frozen.BaseCommit != reply.BaseCommit || len(frozen.Files) != 2 {
		t.Errorf("review = %+v, want the captured snapshot", frozen)
	}
	for _, file := range frozen.Files {
		if file.Diff == "" {
			t.Errorf("%s has no diff in the review", file.Path)
		}
	}
	for _, hostPath := range []string{dir, env.stateDir, env.root} {
		if strings.Contains(review.Body.String(), hostPath) {
			t.Errorf("snapshot review leaks the host path %q: %s", hostPath, review.Body.String())
		}
	}

	// Capturing a handoff is not a factory action: the planner's own record is
	// untouched.
	after, err := env.svc.Get(view.ID)
	if err != nil {
		t.Fatalf("get planner after capture: %v", err)
	}
	if len(after.Turns) != 0 || len(after.Artifacts) != 0 {
		t.Errorf("capture changed the planner record: turns %v artifacts %v", after.Turns, after.Artifacts)
	}
}

// TestPlannerSnapshotUntrackedAndDelete: a delete is a tracked file removed from
// the worktree, an untracked file is a create, and a delete stores no content
// blob because the worktree no longer has one.
func TestPlannerSnapshotUntrackedAndDelete(t *testing.T) {
	env := newPlannerTestEnv(t)
	view, dir := snapshotWorkspace(t, env)

	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatalf("remove tracked file: %v", err)
	}
	snapshotWriteFile(t, dir, "u.txt", "untracked\n", 0o640)

	reply := env.mustCapture(view.ID, PlannerSnapshotRequest{Paths: []string{"a.txt", "u.txt"}})
	files := snapshotFilesByPath(reply)

	deleted, ok := files["a.txt"]
	if !ok {
		t.Fatalf("a.txt was not captured: %v", reply.Files)
	}
	if deleted.Operation != plannerFileDelete {
		t.Errorf("a.txt operation = %q, want %q", deleted.Operation, plannerFileDelete)
	}
	if deleted.BaseSHA256 != plannerDigest([]byte("one\ntwo\n")) {
		t.Errorf("a.txt base hash does not name the base commit's bytes")
	}
	if deleted.ContentSHA256 != "" {
		t.Errorf("a deleted file reports content hash %q", deleted.ContentSHA256)
	}
	for _, want := range []string{"--- a/a.txt", "+++ /dev/null", "-one", "-two"} {
		if !strings.Contains(deleted.Diff, want) {
			t.Errorf("a.txt diff is missing %q:\n%s", want, deleted.Diff)
		}
	}

	added, ok := files["u.txt"]
	if !ok {
		t.Fatalf("u.txt was not captured: %v", reply.Files)
	}
	if added.Operation != plannerFileCreate {
		t.Errorf("u.txt operation = %q, want %q", added.Operation, plannerFileCreate)
	}
	if added.ContentSHA256 != plannerDigest([]byte("untracked\n")) {
		t.Errorf("u.txt content hash does not name the captured bytes")
	}
	if added.Executable {
		t.Errorf("a 0640 file is reported executable")
	}

	// Only the created file has bytes to store.
	blobs := env.blobDigests(t)
	if len(blobs) != 1 || blobs[0] != added.ContentSHA256 {
		t.Fatalf("blob store = %v, want just %s", blobs, added.ContentSHA256)
	}

	review := env.getSnapshot(view.ID, reply.ID)
	if review.Code != http.StatusOK {
		t.Fatalf("GET snapshot status = %d, want 200 (body %s)", review.Code, review.Body.String())
	}
}

// TestPlannerSnapshotRejectsUnsafePathsAndSymlinks: every path a handoff must
// not capture is refused with a reason, never silently dropped, and nothing is
// written for it.
func TestPlannerSnapshotRejectsUnsafePathsAndSymlinks(t *testing.T) {
	env := newPlannerTestEnv(t)
	view, dir := snapshotWorkspace(t, env)

	snapshotWriteFile(t, dir, ".env", "SECRET=one\n", 0o600)
	snapshotWriteFile(t, dir, ".env.local", "SECRET=two\n", 0o600)
	snapshotWriteFile(t, dir, "key.pem", "pem\n", 0o600)
	snapshotWriteFile(t, dir, "id.key", "key\n", 0o600)
	snapshotWriteFile(t, dir, "ignored.log", "log\n", 0o600)
	snapshotWriteFile(t, dir, ".marshal/config.toml", "x\n", 0o600)
	if err := os.Symlink("a.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	snapshotWriteFile(t, dir, "a.txt", "one\nTWO\n", 0o644)

	w := env.captureSnapshot(view.ID, PlannerSnapshotRequest{
		Plan: "a plan",
		Paths: []string{
			"/etc/passwd", "../escape.txt", ".git/config", ".marshal/config.toml",
			".env", ".env.local", "key.pem", "id.key", "ignored.log",
			"link.txt", "sub", "missing.txt", "a.txt",
		},
	})
	reply := decodeSnapshotReply(t, w)
	if len(reply.Files) != 1 || reply.Files[0].Path != "a.txt" {
		t.Fatalf("captured %v, want just a.txt", reply.Files)
	}

	wanted := map[string]string{
		"/etc/passwd":          "absolute",
		"../escape.txt":        "escapes",
		".git/config":          ".git",
		".marshal/config.toml": ".marshal",
		".env":                 ".env",
		".env.local":           ".env",
		"key.pem":              ".pem",
		"id.key":               ".key",
		"ignored.log":          "ignored",
		"link.txt":             "symlink",
		"sub":                  "regular file",
		"missing.txt":          "not present",
	}
	if len(reply.Rejected) != len(wanted) {
		t.Fatalf("rejected %v, want one entry per refused path (%d)", reply.Rejected, len(wanted))
	}
	for _, rejection := range reply.Rejected {
		want, ok := wanted[rejection.Path]
		if !ok {
			t.Errorf("unexpected rejection of %q: %s", rejection.Path, rejection.Reason)
			continue
		}
		if !strings.Contains(rejection.Reason, want) {
			t.Errorf("rejection of %q = %q, want a reason mentioning %q", rejection.Path, rejection.Reason, want)
		}
	}

	// Nothing was stored for a refused path.
	if blobs := env.blobDigests(t); len(blobs) != 1 {
		t.Errorf("blob store = %v, want just the captured file", blobs)
	}
	if records := env.snapshotRecordIDs(t); len(records) != 1 {
		t.Errorf("snapshot records = %v, want one", records)
	}
	for _, hostPath := range []string{dir, env.stateDir} {
		if strings.Contains(w.Body.String(), hostPath) {
			t.Errorf("snapshot response leaks the host path %q", hostPath)
		}
	}

	// A selection that captures nothing and carries no text is the caller's
	// mistake, not an empty snapshot.
	w = env.captureSnapshot(view.ID, PlannerSnapshotRequest{Paths: []string{"link.txt"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty capture status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
}

// TestPlannerSnapshotLimits: the text, per-file, total, file-count and diff
// bounds are enforced before a blob is written.
func TestPlannerSnapshotLimits(t *testing.T) {
	env := newPlannerTestEnv(t)
	view, dir := snapshotWorkspace(t, env)

	t.Run("plan text", func(t *testing.T) {
		w := env.captureSnapshot(view.ID, PlannerSnapshotRequest{Plan: strings.Repeat("p", plannerSnapshotPlanLimit+1)})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("oversize plan status = %d, want 400 (body %s)", w.Code, w.Body.String())
		}
	})

	t.Run("spec text", func(t *testing.T) {
		// Both documents at their bound together do not fit one request body,
		// so the spec bound is exercised through the service the route calls.
		_, err := env.svc.CreateSnapshot(context.Background(), view.ID, PlannerSnapshotRequest{Spec: strings.Repeat("s", plannerSnapshotSpecLimit+1)})
		if !errors.Is(err, errPlannerInvalid) {
			t.Fatalf("oversize spec error = %v, want %v", err, errPlannerInvalid)
		}
	})

	t.Run("one file over 1 MiB", func(t *testing.T) {
		snapshotWriteFile(t, dir, "big.txt", strings.Repeat("x", plannerSnapshotFileLimit+1), 0o644)
		reply := decodeSnapshotReply(t, env.captureSnapshot(view.ID, PlannerSnapshotRequest{Plan: "p", Paths: []string{"big.txt"}}))
		if len(reply.Files) != 0 {
			t.Fatalf("captured %v, want the oversize file refused", reply.Files)
		}
		if len(reply.Rejected) != 1 || !strings.Contains(reply.Rejected[0].Reason, strconv.Itoa(plannerSnapshotFileLimit)) {
			t.Fatalf("rejections = %v, want the per-file limit", reply.Rejected)
		}
	})

	t.Run("total over 10 MiB", func(t *testing.T) {
		paths := make([]string, 0, 11)
		for i := 0; i < 11; i++ {
			rel := "total/f" + strconv.Itoa(i) + ".bin"
			snapshotWriteFile(t, dir, rel, strings.Repeat("t", plannerSnapshotFileLimit), 0o644)
			paths = append(paths, rel)
		}
		reply := decodeSnapshotReply(t, env.captureSnapshot(view.ID, PlannerSnapshotRequest{Plan: "p", Paths: paths}))
		if len(reply.Files) != 10 {
			t.Fatalf("captured %d files, want 10 within the total bound", len(reply.Files))
		}
		if len(reply.Rejected) != 1 || !strings.Contains(reply.Rejected[0].Reason, strconv.Itoa(plannerSnapshotTotalLimit)) {
			t.Fatalf("rejections = %v, want the total bound", reply.Rejected)
		}
	})

	t.Run("more than 100 files", func(t *testing.T) {
		paths := make([]string, 0, plannerSnapshotFileCount+1)
		for i := 0; i <= plannerSnapshotFileCount; i++ {
			rel := "many/f" + strconv.Itoa(i) + ".txt"
			snapshotWriteFile(t, dir, rel, "x\n", 0o644)
			paths = append(paths, rel)
		}
		reply := decodeSnapshotReply(t, env.captureSnapshot(view.ID, PlannerSnapshotRequest{Plan: "p", Paths: paths}))
		if len(reply.Files) != plannerSnapshotFileCount {
			t.Fatalf("captured %d files, want %d", len(reply.Files), plannerSnapshotFileCount)
		}
		if len(reply.Rejected) != 1 || !strings.Contains(reply.Rejected[0].Reason, strconv.Itoa(plannerSnapshotFileCount)) {
			t.Fatalf("rejections = %v, want the file-count bound", reply.Rejected)
		}
	})

	t.Run("diff text", func(t *testing.T) {
		var big strings.Builder
		for i := 0; i < 30000; i++ {
			big.WriteString("line-")
			big.WriteString(strconv.Itoa(i))
			big.WriteString("\n")
		}
		snapshotWriteFile(t, dir, "long.txt", big.String(), 0o644)
		reply := decodeSnapshotReply(t, env.captureSnapshot(view.ID, PlannerSnapshotRequest{Plan: "p", Paths: []string{"long.txt"}}))
		if len(reply.Files) != 1 {
			t.Fatalf("captured %v, want long.txt", reply.Files)
		}
		diff := reply.Files[0].Diff
		if len(diff) > plannerSnapshotDiffLimit+128 {
			t.Errorf("diff is %d bytes, want it capped at %d", len(diff), plannerSnapshotDiffLimit)
		}
		if !strings.Contains(diff, "diff truncated at "+strconv.Itoa(plannerSnapshotDiffLimit)+" bytes") {
			t.Errorf("diff does not say it was truncated")
		}
	})
}

// TestPlannerSnapshotChangingFileRejected: a file that changes between the two
// reads is refused, and a capture the caller cancelled writes no snapshot.
func TestPlannerSnapshotChangingFileRejected(t *testing.T) {
	env := newPlannerTestEnv(t)
	view, dir := snapshotWorkspace(t, env)
	snapshotWriteFile(t, dir, "a.txt", "one\nTWO\n", 0o644)

	var mu sync.Mutex
	reads := 0
	env.svc.readFile = func(path string) ([]byte, os.FileInfo, error) {
		mu.Lock()
		reads++
		count := reads
		mu.Unlock()
		data, info, err := plannerSnapshotReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		if count == 2 {
			// The second read sees a file a model edited in between.
			data = append(data, "late\n"...)
		}
		return data, info, nil
	}
	w := env.captureSnapshot(view.ID, PlannerSnapshotRequest{Plan: "p", Paths: []string{"a.txt"}})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "file changed during capture; retry") {
		t.Errorf("body does not ask the user to retry: %s", w.Body.String())
	}
	if records := env.snapshotRecordIDs(t); len(records) != 0 {
		t.Fatalf("a refused capture wrote %v", records)
	}

	t.Run("a cancelled capture writes no snapshot", func(t *testing.T) {
		env.svc.readFile = plannerSnapshotReadFile
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		env.svc.beforeBlobWrite = cancel
		body, err := json.Marshal(PlannerSnapshotRequest{Plan: "p", Paths: []string{"a.txt"}})
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/planners/"+view.ID+"/snapshots", bytes.NewReader(body)).WithContext(ctx)
		rec := httptest.NewRecorder()
		env.mux.ServeHTTP(rec, r)
		if rec.Code < 400 {
			t.Fatalf("cancelled capture status = %d, want a failure (body %s)", rec.Code, rec.Body.String())
		}
		if records := env.snapshotRecordIDs(t); len(records) != 0 {
			t.Fatalf("a cancelled capture wrote %v", records)
		}
	})
}

// TestPlannerSnapshotPersistsAfterWorkspaceRemoval: the review is served from
// the bridge's own store, so deleting the planner's workspace changes nothing.
func TestPlannerSnapshotPersistsAfterWorkspaceRemoval(t *testing.T) {
	env := newPlannerTestEnv(t)
	view, dir := snapshotWorkspace(t, env)
	snapshotWriteFile(t, dir, "a.txt", "one\nTWO\n", 0o644)
	snapshotWriteFile(t, dir, "new.txt", "fresh\n", 0o644)

	reply := env.mustCapture(view.ID, PlannerSnapshotRequest{Plan: "the plan", Paths: []string{"a.txt", "new.txt"}})
	before := env.getSnapshot(view.ID, reply.ID)
	if before.Code != http.StatusOK {
		t.Fatalf("GET snapshot status = %d, want 200 (body %s)", before.Code, before.Body.String())
	}
	var reviewed PlannerSnapshotView
	if err := json.Unmarshal(before.Body.Bytes(), &reviewed); err != nil {
		t.Fatalf("decode snapshot view: %v", err)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}

	after := env.getSnapshot(view.ID, reply.ID)
	if after.Code != http.StatusOK {
		t.Fatalf("GET snapshot after workspace removal = %d, want 200 (body %s)", after.Code, after.Body.String())
	}
	var reread PlannerSnapshotView
	if err := json.Unmarshal(after.Body.Bytes(), &reread); err != nil {
		t.Fatalf("decode snapshot view: %v", err)
	}
	if !reflect.DeepEqual(reviewed, reread) {
		t.Errorf("the review changed after the workspace was removed:\n%+v\n%+v", reviewed, reread)
	}
	for _, file := range reread.Files {
		if file.Diff == "" {
			t.Errorf("%s has no diff after the workspace was removed", file.Path)
		}
	}
	if reread.BaseCommit != reply.BaseCommit || reread.SHA256 != reply.SHA256 {
		t.Errorf("the snapshot's identity changed: %+v", reread)
	}
}

// TestPlannerSnapshotBinaryAndExecutable: binary bytes are retained and
// reported as binary, and the executable bit survives capture.
func TestPlannerSnapshotBinaryAndExecutable(t *testing.T) {
	env := newPlannerTestEnv(t)
	view, dir := snapshotWorkspace(t, env)

	binary := []byte("head\x00\x01\x02tail")
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), binary, 0o644); err != nil {
		t.Fatalf("write binary file: %v", err)
	}
	snapshotWriteFile(t, dir, "run.sh", "#!/bin/sh\necho hi\n", 0o755)
	snapshotWriteFile(t, dir, "run64.sh", "#!/bin/sh\necho hi\n", 0o644)

	reply := env.mustCapture(view.ID, PlannerSnapshotRequest{Paths: []string{"bin.dat", "run.sh", "run64.sh"}})
	files := snapshotFilesByPath(reply)

	bin, ok := files["bin.dat"]
	if !ok {
		t.Fatalf("bin.dat was not captured: %v", reply.Files)
	}
	if want := "Binary files a/bin.dat and b/bin.dat differ\n"; bin.Diff != want {
		t.Errorf("bin.dat diff = %q, want %q", bin.Diff, want)
	}
	if bin.ContentSHA256 != plannerDigest(binary) {
		t.Errorf("bin.dat content hash does not name the captured bytes")
	}
	blob, err := os.ReadFile(filepath.Join(env.stateDir, plannerDirName, plannerBlobDirName, bin.ContentSHA256))
	if err != nil {
		t.Fatalf("read binary blob: %v", err)
	}
	if !bytes.Equal(blob, binary) {
		t.Errorf("stored binary bytes = %q, want the captured bytes", blob)
	}

	if !files["run.sh"].Executable {
		t.Errorf("a 0755 file is not reported executable")
	}
	if files["run64.sh"].Executable {
		t.Errorf("a 0644 file is reported executable")
	}
	for _, want := range []string{"--- /dev/null", "+++ b/run.sh", "+#!/bin/sh", "+echo hi"} {
		if !strings.Contains(files["run.sh"].Diff, want) {
			t.Errorf("run.sh diff is missing %q:\n%s", want, files["run.sh"].Diff)
		}
	}
}

// TestPlannerSnapshotCorruptionDetected: a snapshot whose stored bytes no
// longer hash to what it recorded is reported, never repaired.
func TestPlannerSnapshotCorruptionDetected(t *testing.T) {
	env := newPlannerTestEnv(t)
	view, dir := snapshotWorkspace(t, env)
	snapshotWriteFile(t, dir, "a.txt", "one\nTWO\n", 0o644)

	reply := env.mustCapture(view.ID, PlannerSnapshotRequest{Plan: "p", Paths: []string{"a.txt"}})
	if len(reply.Files) != 1 || reply.Files[0].ContentSHA256 == "" {
		t.Fatalf("captured %v, want one file with stored bytes", reply.Files)
	}
	blobPath := filepath.Join(env.stateDir, plannerDirName, plannerBlobDirName, reply.Files[0].ContentSHA256)
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatalf("stat blob: %v", err)
	}

	t.Run("different bytes", func(t *testing.T) {
		if err := os.WriteFile(blobPath, []byte("tampered\n"), 0o600); err != nil {
			t.Fatalf("tamper with blob: %v", err)
		}
		w := env.getSnapshot(view.ID, reply.ID)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (body %s)", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "snapshot corrupt") {
			t.Errorf("body does not say the snapshot is corrupt: %s", w.Body.String())
		}
	})

	t.Run("missing bytes", func(t *testing.T) {
		if err := os.Remove(blobPath); err != nil {
			t.Fatalf("remove blob: %v", err)
		}
		w := env.getSnapshot(view.ID, reply.ID)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (body %s)", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "snapshot corrupt") {
			t.Errorf("body does not say the snapshot is corrupt: %s", w.Body.String())
		}
	})

	t.Run("unknown snapshot", func(t *testing.T) {
		w := env.getSnapshot(view.ID, strings.Repeat("b", plannerIDLength))
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
		}
		// A snapshot ID never becomes a path.
		if _, err := env.svc.snapshotPath("../../etc/passwd"); err == nil {
			t.Fatal("a traversal string was accepted as a snapshot id")
		}
	})
}

func decodePlannerContext(t *testing.T, w *httptest.ResponseRecorder) PlannerContextView {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("context status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var view PlannerContextView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode context: %v (body %s)", err, w.Body.String())
	}
	return view
}

func plannerContextAbsent(view PlannerContextView, name string) bool {
	for _, absent := range view.Absent {
		if absent == name {
			return true
		}
	}
	return false
}

func plannerContextArtifact(t *testing.T, view PlannerContextView, kind string) PlannerContextArtifact {
	t.Helper()
	for _, artifact := range view.Artifacts {
		if artifact.Kind == kind {
			return artifact
		}
	}
	t.Fatalf("context has no %q artifact: %v", kind, view.Artifacts)
	return PlannerContextArtifact{}
}

// TestPlannerContextDistinguishesApprovedAndProposal: the briefing reports an
// approved artifact and an unapproved proposal as different things, names the
// sources it used, and names the ones it does not have.
func TestPlannerContextDistinguishesApprovedAndProposal(t *testing.T) {
	env := newPlannerTestEnv(t)
	view, _ := snapshotWorkspace(t, env)

	// Nothing is invented for an empty record.
	empty := decodePlannerContext(t, env.do(http.MethodGet, "/api/planners/"+view.ID+"/context", ""))
	if len(empty.Artifacts) != 0 || len(empty.Decisions) != 0 || len(empty.Unresolved) != 0 {
		t.Fatalf("empty briefing = %+v, want no artifacts, decisions or questions", empty)
	}
	for _, source := range []string{plannerArtifactPlan, plannerArtifactSpec, "decisions", "unresolved"} {
		if !plannerContextAbsent(empty, source) {
			t.Errorf("an empty record does not report %q as absent: %v", source, empty.Absent)
		}
	}

	// A captured plan is approved: it carries the snapshot it was frozen in.
	reply := env.mustCapture(view.ID, PlannerSnapshotRequest{Plan: "the approved plan"})
	context := decodePlannerContext(t, env.do(http.MethodGet, "/api/planners/"+view.ID+"/context", ""))
	approved := plannerContextArtifact(t, context, plannerArtifactPlan)
	if !approved.Approved {
		t.Errorf("a captured plan is not reported as approved: %+v", approved)
	}
	if approved.SnapshotID != reply.ID || approved.Source != "snapshot:"+reply.ID {
		t.Errorf("approved plan source = %q snapshot %q, want snapshot:%s", approved.Source, approved.SnapshotID, reply.ID)
	}
	if approved.SHA256 != plannerDigest([]byte("the approved plan")) {
		t.Errorf("approved plan hash = %q, want the captured artifact's", approved.SHA256)
	}
	if !plannerContextAbsent(context, plannerArtifactSpec) {
		t.Errorf("a record with no spec does not report it absent: %v", context.Absent)
	}

	// A reference the planner produced on its own is a proposal: unapproved,
	// with no captured content and no hash this bridge can vouch for.
	inst, ok := env.svc.live(view.ID)
	if !ok {
		t.Fatal("the planner is not live")
	}
	inst.mu.Lock()
	inst.rec.Artifacts = append(inst.rec.Artifacts, PlannerArtifact{Kind: "proposal", Ref: "proposal-1", Label: "an idea"})
	persistErr := inst.persistLocked()
	inst.mu.Unlock()
	if persistErr != nil {
		t.Fatalf("persist planner record: %v", persistErr)
	}

	raw := env.do(http.MethodGet, "/api/planners/"+view.ID+"/context", "")
	context = decodePlannerContext(t, raw)
	proposal := plannerContextArtifact(t, context, "proposal")
	if proposal.Approved {
		t.Errorf("an unapproved proposal is reported as approved: %+v", proposal)
	}
	if proposal.Ref != "proposal-1" || proposal.Source != "planner:"+view.ID {
		t.Errorf("proposal = %+v, want the record's own reference", proposal)
	}
	if proposal.SHA256 != "" {
		t.Errorf("a proposal reports hash %q, want none", proposal.SHA256)
	}
	if strings.Contains(raw.Body.String(), `"content"`) {
		t.Errorf("the briefing carries artifact content: %s", raw.Body.String())
	}

	// A recorded question is unresolved until it is answered, and both carry
	// the request they came from as their source.
	env.backend.requestOnPrompt = true
	env.backend.requestID = "req-ctx"
	if w := env.prompt(view.ID, "turn-ctx", "do the thing"); w.Code != http.StatusAccepted {
		t.Fatalf("prompt status = %d, want 202 (body %s)", w.Code, w.Body.String())
	}
	plannerWaitFor(t, "the request to be recorded as pending", func() bool {
		current, err := env.svc.Get(view.ID)
		if err != nil {
			return false
		}
		for _, decision := range current.Requests {
			if decision.RequestID == "req-ctx" && decision.State == plannerDecisionPending {
				return true
			}
		}
		return false
	})
	context = decodePlannerContext(t, env.do(http.MethodGet, "/api/planners/"+view.ID+"/context", ""))
	if len(context.Unresolved) != 1 || context.Unresolved[0].Source != "request:req-ctx" {
		t.Fatalf("unresolved = %+v, want req-ctx with its source", context.Unresolved)
	}
	if len(context.Decisions) != 0 || !plannerContextAbsent(context, "decisions") {
		t.Errorf("a pending question is reported as a decision: %+v", context.Decisions)
	}

	if w := env.do(http.MethodPost, "/api/planners/"+view.ID+"/requests/req-ctx", `{"response":{"allow":true}}`); w.Code != http.StatusOK {
		t.Fatalf("answer status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	context = decodePlannerContext(t, env.do(http.MethodGet, "/api/planners/"+view.ID+"/context", ""))
	if len(context.Decisions) != 1 || context.Decisions[0].Source != "request:req-ctx" || context.Decisions[0].State != plannerDecisionAnswered {
		t.Fatalf("decisions = %+v, want the answered request with its source", context.Decisions)
	}
	if len(context.Unresolved) != 0 || !plannerContextAbsent(context, "unresolved") {
		t.Errorf("an answered request is still unresolved: %+v", context.Unresolved)
	}
	plannerWaitFor(t, "the turn to finish", func() bool { return env.turnState(view.ID, "turn-ctx") == plannerTurnDone })

	t.Run("additional prompt context is bounded and never journaled raw", func(t *testing.T) {
		env.backend.requestOnPrompt = false
		long, err := json.Marshal(map[string]any{
			"requestId": "turn-long",
			"text":      "the prompt",
			"context":   strings.Repeat("c", plannerSnapshotContextLimit+1),
		})
		if err != nil {
			t.Fatalf("encode prompt body: %v", err)
		}
		w := env.do(http.MethodPost, "/api/planners/"+view.ID+"/prompt", string(long))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("oversize context status = %d, want 400 (body %s)", w.Code, w.Body.String())
		}

		const extra = "context marker only in the prompt"
		small, err := json.Marshal(map[string]any{"requestId": "turn-ctx2", "text": "the prompt", "context": extra})
		if err != nil {
			t.Fatalf("encode prompt body: %v", err)
		}
		w = env.do(http.MethodPost, "/api/planners/"+view.ID+"/prompt", string(small))
		if w.Code != http.StatusAccepted {
			t.Fatalf("prompt status = %d, want 202 (body %s)", w.Code, w.Body.String())
		}
		plannerWaitFor(t, "the turn with context to finish", func() bool { return env.turnState(view.ID, "turn-ctx2") == plannerTurnDone })

		prompts := env.backend.promptTexts()
		if len(prompts) == 0 {
			t.Fatal("the backend saw no prompt")
		}
		if want := "the prompt\n\n" + extra; prompts[len(prompts)-1] != want {
			t.Errorf("dispatched text = %q, want the context after the prompt", prompts[len(prompts)-1])
		}

		journalPath, err := env.svc.store.journalPath(view.ID)
		if err != nil {
			t.Fatalf("journal path: %v", err)
		}
		journal, err := os.ReadFile(journalPath)
		if err != nil {
			t.Fatalf("read journal: %v", err)
		}
		if strings.Contains(string(journal), extra) {
			t.Errorf("the journal holds the raw context: %s", journal)
		}
		if !strings.Contains(string(journal), plannerTextHash(extra)) {
			t.Errorf("the journal does not record the context's hash: %s", journal)
		}
		// The briefing itself never carries prompt text either.
		if strings.Contains(raw.Body.String(), extra) {
			t.Errorf("the briefing carries prompt text: %s", raw.Body.String())
		}
	})
}
