package inproc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/worktree"
)

// newProjectTestSession builds a session over a fresh state in dir, with deps
// applied on top.
func newProjectTestSession(t *testing.T, dir string, deps Deps) *Session {
	t.Helper()
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
	deps.State = state
	_, sess := New(deps)
	return sess
}

// writeProjectTestPlan writes a plan file into dir's plans directory.
func writeProjectTestPlan(t *testing.T, dir, name, body string) string {
	t.Helper()
	plansDir := filepath.Join(dir, ".marshal", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	path := filepath.Join(plansDir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

// TestFileIndexReadsTheDeps pins the three FileIndex outcomes: no index
// dependency, one that answers, and one that fails.
func TestFileIndexReadsTheDeps(t *testing.T) {
	ctx := context.Background()

	sess := newProjectTestSession(t, t.TempDir(), Deps{})
	got, err := sess.FileIndex(ctx)
	if err != nil || got != nil {
		t.Errorf("FileIndex() without Deps.FileIndex = %v, %v; want nil, nil", got, err)
	}

	want := []string{"a.go", "b.go"}
	sess = newProjectTestSession(t, t.TempDir(), Deps{
		FileIndex: func() ([]string, error) { return want, nil },
	})
	got, err = sess.FileIndex(ctx)
	if err != nil {
		t.Fatalf("FileIndex(): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FileIndex() = %v, want %v", got, want)
	}

	boom := errors.New("boom")
	sess = newProjectTestSession(t, t.TempDir(), Deps{
		FileIndex: func() ([]string, error) { return nil, boom },
	})
	if _, err := sess.FileIndex(ctx); !errors.Is(err, boom) {
		t.Errorf("FileIndex() error = %v, want %v", err, boom)
	}
}

// TestPlansListsTheDiscoveredPlans keeps a plan that does not parse in the
// list, with its error, so the picker can say why.
func TestPlansListsTheDiscoveredPlans(t *testing.T) {
	dir := t.TempDir()
	writeProjectTestPlan(t, dir, "good.md",
		"# Test Plan\n\n## Global Constraints\n\n- keep it green\n\n## Task 1: Do it\n\nBody.\n")
	writeProjectTestPlan(t, dir, "broken.md", "# No tasks here\n")

	sess := newProjectTestSession(t, dir, Deps{})
	plans, err := sess.Plans(context.Background())
	if err != nil {
		t.Fatalf("Plans(): %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("Plans() returned %d plans, want 2: %+v", len(plans), plans)
	}
	byName := map[string]acpwire.PlanCandidate{}
	for _, p := range plans {
		byName[p.Name] = p
	}
	good, ok := byName["good.md"]
	if !ok {
		t.Fatalf("Plans() = %+v, want a good.md entry", plans)
	}
	if good.Tasks != 1 {
		t.Errorf("good.md Tasks = %d, want 1", good.Tasks)
	}
	if good.Error != "" {
		t.Errorf("good.md Error = %q, want empty", good.Error)
	}
	broken, ok := byName["broken.md"]
	if !ok {
		t.Fatalf("Plans() = %+v, want a broken.md entry", plans)
	}
	if broken.Error == "" {
		t.Error("broken.md Error is empty, want the parse error")
	}
}

// TestDiscardCandidateRefusesOutsideThePlansDirectory keeps the plans
// directory's guard: only a direct file inside it can be removed.
func TestDiscardCandidateRefusesOutsideThePlansDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	inside := writeProjectTestPlan(t, dir, "candidate.md", "# Plan\n\n## Task 1: Do it\n")
	outside := filepath.Join(dir, "outside.md")
	if err := os.WriteFile(outside, []byte("# Plan\n\n## Task 1: Do it\n"), 0o644); err != nil {
		t.Fatalf("write outside plan: %v", err)
	}

	sess := newProjectTestSession(t, dir, Deps{})
	if err := sess.DiscardCandidate(ctx, inside); err != nil {
		t.Fatalf("DiscardCandidate(inside): %v", err)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Errorf("inside candidate still present after discard: %v", err)
	}

	if err := sess.DiscardCandidate(ctx, outside); err == nil {
		t.Error("DiscardCandidate(outside) = nil, want a refusal")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("outside file is gone after a refused discard: %v", err)
	}
}

// TestScaffoldPlanReportsAnExistingFile pins the second call's answer: the
// file is left exactly as the first call wrote it.
func TestScaffoldPlanReportsAnExistingFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sess := newProjectTestSession(t, dir, Deps{})

	first, err := sess.ScaffoldPlan(ctx)
	if err != nil {
		t.Fatalf("ScaffoldPlan() first: %v", err)
	}
	if first.Existed {
		t.Error("ScaffoldPlan() first Existed = true, want false")
	}
	written, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatalf("read scaffolded plan: %v", err)
	}

	second, err := sess.ScaffoldPlan(ctx)
	if err != nil {
		t.Fatalf("ScaffoldPlan() second: %v", err)
	}
	if !second.Existed {
		t.Error("ScaffoldPlan() second Existed = false, want true")
	}
	kept, err := os.ReadFile(second.Path)
	if err != nil {
		t.Fatalf("read plan after second call: %v", err)
	}
	if string(kept) != string(written) {
		t.Error("ScaffoldPlan() rewrote an existing plan")
	}
}

// TestDiffReadsWithoutMutating moves the TUI's assertion to the adapter: the
// diff path only ever reads, and an incomplete range is refused.
func TestDiffReadsWithoutMutating(t *testing.T) {
	ctx := context.Background()
	git := worktree.NewFakeGitOps()
	git.DiffOut = "diff body"
	sess := newProjectTestSession(t, t.TempDir(), Deps{Git: git})

	got, err := sess.Diff(ctx, acpwire.DiffQuery{Base: "main", Head: "pipeline/add-retries"})
	if err != nil {
		t.Fatalf("Diff(): %v", err)
	}
	if got != "diff body" {
		t.Errorf("Diff() = %q, want %q", got, "diff body")
	}
	for _, call := range git.Calls() {
		switch call {
		case "WorktreeAdd", "WorktreeRemove", "CommitAll", "WorktreePrune":
			t.Errorf("Diff performed a mutating git operation: %s", call)
		}
	}

	if _, err := sess.Diff(ctx, acpwire.DiffQuery{Base: "main"}); err == nil ||
		err.Error() != "inproc: a diff needs a base and a head" {
		t.Errorf("Diff() without a head error = %v, want the pinned message", err)
	}
}

// TestReadFileStaysInTheWorkingDirectory pins the containment rule, the
// regular-file rule and the binary answer.
func TestReadFileStaysInTheWorkingDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("write blob.bin: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	sess := newProjectTestSession(t, dir, Deps{})

	got, err := sess.ReadFile(ctx, "a.txt")
	if err != nil {
		t.Fatalf("ReadFile(a.txt): %v", err)
	}
	if got.Content != "hello" || got.Size != 5 || got.Binary {
		t.Errorf("ReadFile(a.txt) = %+v, want content hello, size 5, text", got)
	}

	if _, err := sess.ReadFile(ctx, "../x"); err == nil {
		t.Error("ReadFile(../x) = nil error, want a refusal")
	}
	if _, err := sess.ReadFile(ctx, filepath.Join(dir, "a.txt")); err == nil {
		t.Error("ReadFile(absolute) = nil error, want a refusal")
	}
	if _, err := sess.ReadFile(ctx, "sub"); err == nil ||
		err.Error() != "inproc: sub is not a regular file" {
		t.Errorf("ReadFile(sub) error = %v, want the pinned message", err)
	}

	bin, err := sess.ReadFile(ctx, "blob.bin")
	if err != nil {
		t.Fatalf("ReadFile(blob.bin): %v", err)
	}
	if !bin.Binary {
		t.Error("ReadFile(blob.bin) Binary = false, want true")
	}
	if bin.Content != "" {
		t.Errorf("ReadFile(blob.bin) Content = %q, want empty", bin.Content)
	}
}
