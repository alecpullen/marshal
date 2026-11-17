package bridge

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// This file holds one regression test per defect a branch review found and the
// fix closed. Each one fails on the code before that fix.

// TestPlannerSnapshotEmptyFileIsReviewable: a captured 0-byte file was recorded
// with the digest of no bytes while no blob was written, so the snapshot's own
// verification failed and every review of it answered 500 "snapshot corrupt".
func TestPlannerSnapshotEmptyFileIsReviewable(t *testing.T) {
	env := newPlannerTestEnv(t)
	view, dir := snapshotWorkspace(t, env)
	snapshotWriteFile(t, dir, ".gitkeep", "", 0o644)

	reply := env.mustCapture(view.ID, PlannerSnapshotRequest{Paths: []string{".gitkeep"}})
	if len(reply.Files) != 1 {
		t.Fatalf("captured %d files, want the empty one: %+v", len(reply.Files), reply)
	}
	if reply.Files[0].ContentSHA256 != plannerDigest(nil) {
		t.Fatalf("content digest = %q, want the digest of no bytes", reply.Files[0].ContentSHA256)
	}
	if w := env.getSnapshot(view.ID, reply.ID); w.Code != http.StatusOK {
		t.Fatalf("review after capture: status %d, body %s", w.Code, w.Body.String())
	}
}

// TestPlannerSnapshotTrailingNewlineIsADifference: a modify that only adds or
// removes the final newline used to render as pure context, so the reviewer was
// told "no change" for bytes that differ.
func TestPlannerSnapshotTrailingNewlineIsADifference(t *testing.T) {
	env := newPlannerTestEnv(t)
	view, dir := snapshotWorkspace(t, env)
	// The base commit holds "one\ntwo\n"; this removes only the final newline.
	snapshotWriteFile(t, dir, "a.txt", "one\ntwo", 0o644)

	reply := env.mustCapture(view.ID, PlannerSnapshotRequest{Paths: []string{"a.txt"}})
	if len(reply.Files) != 1 {
		t.Fatalf("captured %d files, want 1", len(reply.Files))
	}
	diff := reply.Files[0].Diff
	if !strings.Contains(diff, "-two") || !strings.Contains(diff, "+two") {
		t.Fatalf("diff has no removed/added line for the final newline:\n%s", diff)
	}
	if !strings.Contains(diff, "No newline at end of file") {
		t.Fatalf("diff does not mark the missing final newline:\n%s", diff)
	}
}

// TestPlannerSnapshotRefusesSymlinkedDirectory: confinement was leaf-only, so a
// path through a directory symlink read a file outside the workspace.
func TestPlannerSnapshotRefusesSymlinkedDirectory(t *testing.T) {
	env := newPlannerTestEnv(t)
	view, dir := snapshotWorkspace(t, env)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "hosts"), []byte("not the user's file\n"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "outside")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	reply := env.mustCapture(view.ID, PlannerSnapshotRequest{
		Plan:  "the plan",
		Paths: []string{"outside/hosts"},
	})
	if len(reply.Files) != 0 {
		t.Fatalf("captured a file through a directory symlink: %+v", reply.Files)
	}
	if len(reply.Rejected) != 1 || reply.Rejected[0].Reason != "symlink" {
		t.Fatalf("rejections = %+v, want one symlink refusal", reply.Rejected)
	}
}

// TestFactorySnapshotRejectsTamperedDigest: validation checked the digest's
// shape only, so a client could hand over a selection its recorded digest did
// not describe and the approval would bind the string, not the files.
func TestFactorySnapshotRejectsTamperedDigest(t *testing.T) {
	snap := EditSnapshot{
		BaseCommit: strings.Repeat("a", 40),
		Files: []SelectedFile{{
			Path:          "x.txt",
			Operation:     plannerFileCreate,
			ContentSHA256: plannerDigest([]byte("selected\n")),
		}},
	}
	digest, err := plannerSnapshotDigest(snap)
	if err != nil {
		t.Fatalf("digest the selection: %v", err)
	}
	snap.SHA256 = digest
	if err := factoryValidateSnapshot(&snap); err != nil {
		t.Fatalf("a consistent snapshot was refused: %v", err)
	}
	snap.SHA256 = plannerDigest([]byte("a different selection"))
	if err := factoryValidateSnapshot(&snap); err == nil {
		t.Fatal("a snapshot whose digest does not describe its files was accepted")
	}
}

// TestFactorySnapshotRejectsUnstageablePaths: a leading ':' survives "--" as
// git pathspec magic, and a nested selection cannot be applied in any order.
// Both used to pass validation and fail in the middle of the seed.
func TestFactorySnapshotRejectsUnstageablePaths(t *testing.T) {
	cases := []struct {
		name  string
		files []SelectedFile
	}{
		{
			name: "pathspec magic",
			files: []SelectedFile{{
				Path:          ":(literal)x.txt",
				Operation:     plannerFileCreate,
				ContentSHA256: plannerDigest([]byte("x")),
			}},
		},
		{
			name: "nested selection",
			files: []SelectedFile{
				{Path: "a", Operation: plannerFileCreate, ContentSHA256: plannerDigest([]byte("a"))},
				{Path: "a/b", Operation: plannerFileCreate, ContentSHA256: plannerDigest([]byte("b"))},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := EditSnapshot{BaseCommit: strings.Repeat("a", 40), Files: tc.files}
			digest, err := plannerSnapshotDigest(snap)
			if err != nil {
				t.Fatalf("digest the selection: %v", err)
			}
			snap.SHA256 = digest
			if err := factoryValidateSnapshot(&snap); err == nil {
				t.Fatalf("%s: accepted a selection a seed cannot apply", tc.name)
			}
		})
	}
}

// TestFactorySeedCreateRefusesPresentFile: a create means the path did not
// exist. Overwriting an untracked or ignored file in the checkout lost bytes
// the user never reviewed, and the seed then failed anyway at `git add`.
func TestFactorySeedCreateRefusesPresentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	const original = "do not lose this\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write the present file: %v", err)
	}

	err := applySeedEdit(dir, factorySeedEdit{
		Path:      "x.txt",
		Operation: plannerFileCreate,
		Content:   []byte("replacement\n"),
	})
	if err == nil {
		t.Fatal("a create overwrote a file the base commit does not hold")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != original {
		t.Fatalf("present file = %q (%v), want it untouched", got, readErr)
	}
}

// failingStartBackend refuses to start a session and records that it was
// closed, which is the disposal a failed create must perform.
type failingStartBackend struct {
	mu     sync.Mutex
	closes int
}

func (b *failingStartBackend) Start(context.Context, WorkerSessionSpec) (string, WorkerCapabilities, error) {
	return "", WorkerCapabilities{}, errors.New("the adapter refused to start")
}

func (b *failingStartBackend) Prompt(context.Context, string, string) (WorkerTurnResult, error) {
	return WorkerTurnResult{}, errors.New("no session")
}

func (b *failingStartBackend) Cancel(context.Context, string) error { return nil }

func (b *failingStartBackend) Close(context.Context, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closes++
	return nil
}

// TestPlannerCreateReleasesBackendWhenStartFails: the adapter process a failed
// Start spawned used to outlive the create forever, because nothing ever closed
// a backend that held no session.
func TestPlannerCreateReleasesBackendWhenStartFails(t *testing.T) {
	env := newPlannerTestEnv(t)
	svc := newPlannerService(env.fleet, env.stateDir)
	backend := &failingStartBackend{}
	svc.backendFor = func(WorkerSessionSpec, workerEventSink, workerRequestSink) (WorkerBackend, error) {
		return backend, nil
	}
	svc.provisionWorkspace = env.provisioner.provision

	if _, err := svc.Create(context.Background(), PlannerCreateRequest{ProjectRoot: env.root, RepoID: env.repo.ID}); err == nil {
		t.Fatal("create succeeded with a backend that refused to start a session")
	}
	backend.mu.Lock()
	closes := backend.closes
	backend.mu.Unlock()
	if closes != 1 {
		t.Fatalf("backend closes = %d, want 1: a failed create must release the process it spawned", closes)
	}
}
