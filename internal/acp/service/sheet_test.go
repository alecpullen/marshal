package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/repo/gitinfo"
	"marshal/internal/tools/registry"
)

// TestSheetOfAReplacedSessionKeepsItsFinalValues pins C4 for the sheet: a
// session a swap replaced keeps answering with its own state's values.
func TestSheetOfAReplacedSessionKeepsItsFinalValues(t *testing.T) {
	oldState := newState(t, "s1")
	oldState.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(10, 0), ToolName: "final-probe"})
	h, old := New(Deps{State: oldState})
	h.swap(newState(t, "s2"))

	got := old.Sheet(0)
	if want := []acpwire.SheetTool{{Name: "final-probe", Calls: 1}}; !reflect.DeepEqual(got.Tools, want) {
		t.Errorf("closed session Sheet(0).Tools = %+v, want its final %+v", got.Tools, want)
	}
}

// requireGit skips a test that shells out to git when git is not on PATH.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// runGit runs one git command in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-C", dir}, args...)
	if out, err := exec.Command("git", cmdArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// sheetRepo returns a new git repository with a.txt ("one\n") committed on
// main. It is built the way the TUI's initSheetTestRepo builds one.
func sheetRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// sheetSession builds a session over repo, whose active root is repo.
func sheetSession(t *testing.T, repo string) *Session {
	t.Helper()
	_, sess := New(Deps{State: session.New(config.Default(), repo, time.Unix(100, 0), session.Persistence{SessionID: "s1"})})
	return sess
}

// TestRefreshSheetChangedKeepsTheBase pins that a Changed read diffs against
// the cached base, so a commit made after the rebase still shows.
func TestRefreshSheetChangedKeepsTheBase(t *testing.T) {
	requireGit(t)
	repo := sheetRepo(t)
	sess := sheetSession(t, repo)

	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Rebase: true}); err != nil {
		t.Fatalf("RefreshSheet(rebase): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	runGit(t, repo, "commit", "-qam", "change")

	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Changed: true}); err != nil {
		t.Fatalf("RefreshSheet(changed): %v", err)
	}
	found := false
	for _, f := range sess.Sheet(0).Changed {
		if f.Path == "a.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("Changed = %+v, want a.txt against the earlier base", sess.Sheet(0).Changed)
	}

	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Rebase: true}); err != nil {
		t.Fatalf("RefreshSheet(rebase): %v", err)
	}
	if got := sess.Sheet(0).Changed; len(got) != 0 {
		t.Errorf("Changed after the rebase = %+v, want none", got)
	}
}

// TestRefreshSheetChangedRebasesWithoutABase pins that a Changed read with no
// base yet rebases first.
func TestRefreshSheetChangedRebasesWithoutABase(t *testing.T) {
	requireGit(t)
	repo := sheetRepo(t)
	sess := sheetSession(t, repo)

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Changed: true}); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	found := false
	for _, f := range sess.Sheet(0).Changed {
		if f.Path == "a.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("Changed = %+v, want a.txt", sess.Sheet(0).Changed)
	}
}

// TestRefreshSheetKeepsTheCacheWhenHeadIsUnreadable pins that an unreadable
// HEAD leaves the base and the changed files as they were.
func TestRefreshSheetKeepsTheCacheWhenHeadIsUnreadable(t *testing.T) {
	requireGit(t)
	repo := sheetRepo(t)
	sess := sheetSession(t, repo)

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Rebase: true}); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	if got := len(sess.Sheet(0).Changed); got != 1 {
		t.Fatalf("Changed = %d files, want 1", got)
	}

	runGit(t, repo, "symbolic-ref", "HEAD", "refs/heads/unborn")
	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Rebase: true}); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	found := false
	for _, f := range sess.Sheet(0).Changed {
		if f.Path == "a.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("Changed = %+v, want a.txt kept from the last readable HEAD", sess.Sheet(0).Changed)
	}
}

// TestRefreshSheetDropsAReadForAMovedRoot pins that a read for a root the
// workspace has since left is stored nowhere.
func TestRefreshSheetDropsAReadForAMovedRoot(t *testing.T) {
	requireGit(t)
	repo := sheetRepo(t)
	state := session.New(config.Default(), repo, time.Unix(100, 0), session.Persistence{SessionID: "s1"})
	_, sess := New(Deps{State: state})

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	other := t.TempDir()
	sess.headSHA = func(dir string) string {
		state.SetWorkspace(session.Workspace{ProjectRoot: repo, ActiveRoot: other})
		return gitinfo.HeadSHA(dir)
	}
	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Rebase: true}); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	if got := sess.Sheet(0).Changed; len(got) != 0 {
		t.Errorf("Changed = %+v, want none for a moved root", got)
	}
	if got := sess.Status().ChangedFiles; got != 0 {
		t.Errorf("Status().ChangedFiles = %d, want 0", got)
	}
}

// TestRefreshSheetReadsTheActiveRoot pins that the changed files come from the
// active root, not the project root.
func TestRefreshSheetReadsTheActiveRoot(t *testing.T) {
	requireGit(t)
	repo := sheetRepo(t)
	wt := filepath.Join(repo, "wt")
	runGit(t, repo, "worktree", "add", "-b", "feat-x", wt)

	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write worktree a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main-only.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatalf("write main-only.txt: %v", err)
	}

	state := session.New(config.Default(), repo, time.Unix(100, 0), session.Persistence{SessionID: "s1"})
	state.SetWorkspace(session.Workspace{ProjectRoot: repo, ActiveRoot: wt, Branch: "feat-x"})
	_, sess := New(Deps{State: state})

	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Rebase: true}); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	found := false
	for _, f := range sess.Sheet(0).Changed {
		if f.Path == "a.txt" {
			found = true
		}
		if f.Path == "main-only.txt" {
			t.Errorf("Changed includes main-checkout file %q, want only worktree changes", f.Path)
		}
	}
	if !found {
		t.Errorf("Changed = %+v, want the worktree's a.txt", sess.Sheet(0).Changed)
	}
}

// TestRefreshSheetReadsTheFleet pins the Fleet refresh: the agent worktrees
// under the project root.
func TestRefreshSheetReadsTheFleet(t *testing.T) {
	requireGit(t)
	repo := sheetRepo(t)
	wt := filepath.Join(repo, ".marshal", "worktrees", "x")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatalf("mkdir worktrees: %v", err)
	}
	runGit(t, repo, "worktree", "add", "-b", "agent/x", wt, "HEAD")

	sess := sheetSession(t, repo)
	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Fleet: true}); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	fleet := sess.Sheet(0).Fleet
	if len(fleet) != 1 || fleet[0].Branch != "agent/x" {
		t.Fatalf("Fleet = %+v, want one row for agent/x", fleet)
	}
}

// TestRefreshSheetFiresChanged pins that a refresh signals Changed.
func TestRefreshSheetFiresChanged(t *testing.T) {
	_, sess := New(Deps{State: newState(t, "s1")})
	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Stats: true}); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	waitChanged(t, sess)
}

// TestRefreshSheetOnAReplacedSessionFails pins C4 for the refresh: a session a
// swap replaced refuses to read.
func TestRefreshSheetOnAReplacedSessionFails(t *testing.T) {
	h, old := New(Deps{State: newState(t, "s1")})
	h.swap(newState(t, "s2"))

	err := old.RefreshSheet(context.Background(), acpwire.SheetRefresh{Stats: true})
	if !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Fatalf("RefreshSheet on a replaced session = %v, want CodeSessionClosed", err)
	}
}

// TestASubagentSheetHasTheSessionsFleetAndRepo pins that a subagent's sheet
// carries the session's fleet and repo counts, and none of the session's
// changed files, turns or totals.
func TestASubagentSheetHasTheSessionsFleetAndRepo(t *testing.T) {
	parent := newState(t, "parent")
	child := newState(t, "child")
	view := parent.RegisterSubagent("scout", child)

	_, sess := New(Deps{State: parent})
	sess.sheet = sheetCache{
		changed: []acpwire.SheetChangedFile{{Path: "a.go", Status: "M"}},
		turns:   []acpwire.SheetTurn{{StartedAt: 1000}},
		totals:  acpwire.SheetTotals{Turns: 1},
		repo:    acpwire.SheetRepo{Files: 2, Symbols: 3},
		fleet:   []acpwire.SheetFleetRow{{Branch: "agent/x"}},
	}

	got := sess.Sheet(view.ID)
	if want := []acpwire.SheetFleetRow{{Branch: "agent/x"}}; !reflect.DeepEqual(got.Fleet, want) {
		t.Errorf("Fleet = %+v, want the session's %+v", got.Fleet, want)
	}
	if want := (acpwire.SheetRepo{Files: 2, Symbols: 3}); got.Repo != want {
		t.Errorf("Repo = %+v, want the session's %+v", got.Repo, want)
	}
	if len(got.Changed) != 0 || len(got.Turns) != 0 || got.Totals != (acpwire.SheetTotals{}) {
		t.Errorf("subagent sheet carries session telemetry: changed=%+v turns=%+v totals=%+v",
			got.Changed, got.Turns, got.Totals)
	}
}
