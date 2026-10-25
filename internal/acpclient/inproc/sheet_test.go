package inproc

import (
	"context"
	"encoding/json"
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
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/repo/gitinfo"
	"marshal/internal/tools/registry"
)

// TestSheetCarriesTheSessionsActivity pins every in-memory value the sheet
// reads: the tool-call log, the files it touched, the rules, the skills, the
// budget, the loaded tools, the sandbox and the context pack.
func TestSheetCarriesTheSessionsActivity(t *testing.T) {
	state := newState(t, "s1")
	state.LogToolCall(registry.AuditEvent{
		Timestamp: time.Unix(10, 0),
		ToolName:  "file.read",
		Args:      json.RawMessage(`{"path":"a.go"}`),
		Duration:  10 * time.Millisecond,
	})
	state.LogToolCall(registry.AuditEvent{
		Timestamp:    time.Unix(20, 0),
		ToolName:     "file.write_patch",
		FilesChanged: []string{"b.go"},
		Duration:     30 * time.Millisecond,
	})
	state.LogToolCall(registry.AuditEvent{
		Timestamp: time.Unix(30, 0),
		ToolName:  "shell.run",
		Error:     "exit 1",
		Duration:  50 * time.Millisecond,
	})
	state.AddSessionRule("go test")
	state.ActivateSkill("tdd")
	state.SetToolBudget(session.ToolBudget{Used: 3, Max: 100})
	state.AddLoadedToolNames([]string{"web.fetch"})
	state.SetSandboxInfo(session.SandboxInfo{Backend: "restricted"})
	state.Config.Tools.Shell.AllowNetwork = true
	state.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 3400},
		Sections: []contextpack.Section{
			{Title: "repo map", EstimatedTokens: 2400},
			{Title: "todos"},
		},
	})

	_, sess := New(Deps{State: state})
	got := sess.Sheet(0)
	want := acpwire.SessionSheet{
		StartedAt: 100000,
		Context: acpwire.SheetContext{
			MaxTokens:       12000,
			EstimatedTokens: 3400,
			Sections: []acpwire.SheetContextSection{
				{Title: "repo map", EstimatedTokens: 2400},
				{Title: "todos"},
			},
		},
		Tools: []acpwire.SheetTool{
			{Name: "file.read", Calls: 1, SlowestMs: 10},
			{Name: "file.write_patch", Calls: 1, SlowestMs: 30},
			{Name: "shell.run", Calls: 1, Errors: 1, SlowestMs: 50},
		},
		Files: []acpwire.SheetFile{
			{Path: "b.go", Edits: 1, LastAt: 20000},
			{Path: "a.go", Reads: 1, LastAt: 10000},
		},
		Rules:       []string{"go test"},
		Skills:      []string{"tdd"},
		ToolBudget:  acpwire.SheetToolBudget{Used: 3, Max: 100},
		LoadedTools: 1,
		Sandbox:     acpwire.SheetSandbox{Backend: "restricted", AllowNetwork: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sheet(0) =\n%+v\nwant\n%+v", got, want)
	}
}

// TestSheetReadsASubagentsOwnActivity pins that a subagent's ID reads the
// child's activity, not the parent's, and that only the session's own sheet
// carries a context pack.
func TestSheetReadsASubagentsOwnActivity(t *testing.T) {
	parent := newState(t, "parent")
	parent.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(10, 0), ToolName: "parent-probe"})
	parent.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 100},
		Sections:   []contextpack.Section{{Title: "repo map", EstimatedTokens: 100}},
	})

	child := session.New(config.Default(), t.TempDir(), time.Unix(200, 0), session.Persistence{SessionID: "child"})
	child.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(20, 0), ToolName: "child-probe"})
	child.AddSessionRule("child rule")
	view := parent.RegisterSubagent("scout", child)

	_, sess := New(Deps{State: parent})
	got := sess.Sheet(view.ID)

	if got.SubagentID != view.ID {
		t.Errorf("SubagentID = %d, want the view's %d", got.SubagentID, view.ID)
	}
	if want := []acpwire.SheetTool{{Name: "child-probe", Calls: 1}}; !reflect.DeepEqual(got.Tools, want) {
		t.Errorf("Tools = %+v, want only the child's %+v", got.Tools, want)
	}
	if want := []string{"child rule"}; !reflect.DeepEqual(got.Rules, want) {
		t.Errorf("Rules = %v, want %v", got.Rules, want)
	}
	if got.StartedAt != 200000 {
		t.Errorf("StartedAt = %d, want the child's 200000", got.StartedAt)
	}
	if !reflect.DeepEqual(got.Context, acpwire.SheetContext{}) {
		t.Errorf("Context = %+v, want empty for a subagent", got.Context)
	}
}

// TestSheetFindsANestedSubagent pins that the search descends into a child's
// own subagents.
func TestSheetFindsANestedSubagent(t *testing.T) {
	parent := newState(t, "parent")
	child := newState(t, "child")
	parent.RegisterSubagent("scout", child)

	grandchild := newState(t, "grandchild")
	grandchild.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(30, 0), ToolName: "grandchild-probe"})
	grandView := child.RegisterSubagent("scout-of-scout", grandchild)

	_, sess := New(Deps{State: parent})
	got := sess.Sheet(grandView.ID)

	if got.SubagentID != grandView.ID {
		t.Errorf("SubagentID = %d, want the grandchild's %d", got.SubagentID, grandView.ID)
	}
	if want := []acpwire.SheetTool{{Name: "grandchild-probe", Calls: 1}}; !reflect.DeepEqual(got.Tools, want) {
		t.Errorf("Tools = %+v, want the grandchild's %+v", got.Tools, want)
	}
}

// TestSheetReadsAnUnknownSubagentAsTheSession pins that an ID naming no child
// session, and a card registered with no child, both read as the session's own
// sheet.
func TestSheetReadsAnUnknownSubagentAsTheSession(t *testing.T) {
	parent := newState(t, "parent")
	parent.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(10, 0), ToolName: "parent-probe"})
	card := parent.RegisterSubagent("pipeline", nil)

	_, sess := New(Deps{State: parent})
	for _, id := range []int64{999999, card.ID} {
		got := sess.Sheet(id)
		if got.SubagentID != 0 {
			t.Errorf("Sheet(%d).SubagentID = %d, want 0", id, got.SubagentID)
		}
		if want := []acpwire.SheetTool{{Name: "parent-probe", Calls: 1}}; !reflect.DeepEqual(got.Tools, want) {
			t.Errorf("Sheet(%d).Tools = %+v, want the parent's %+v", id, got.Tools, want)
		}
	}
}

// TestSheetOfAReplacedSessionKeepsItsFinalValues pins C4 for the sheet: a
// session a swap replaced keeps answering with its own state's values.
func TestSheetOfAReplacedSessionKeepsItsFinalValues(t *testing.T) {
	oldState := newState(t, "s1")
	oldState.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(10, 0), ToolName: "final-probe"})
	h, old := New(Deps{State: oldState})
	h.Swap(newState(t, "s2"))

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

// TestRefreshSheetRebasesOntoHead pins that a Rebase reads HEAD and the
// changed files against it, and that the status line's count follows.
func TestRefreshSheetRebasesOntoHead(t *testing.T) {
	requireGit(t)
	repo := sheetRepo(t)
	sess := sheetSession(t, repo)

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Rebase: true}); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	want := []acpwire.SheetChangedFile{{Path: "a.txt", Status: "M", Added: 1}}
	if got := sess.Sheet(0).Changed; !reflect.DeepEqual(got, want) {
		t.Errorf("Changed = %+v, want %+v", got, want)
	}
	if got := sess.Status().ChangedFiles; got != 1 {
		t.Errorf("Status().ChangedFiles = %d, want 1", got)
	}

	runGit(t, repo, "commit", "-qam", "change")
	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Rebase: true}); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	if got := sess.Sheet(0).Changed; len(got) != 0 {
		t.Errorf("Changed after the commit = %+v, want none", got)
	}
	if got := sess.Status().ChangedFiles; got != 0 {
		t.Errorf("Status().ChangedFiles after the commit = %d, want 0", got)
	}
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

// TestRefreshSheetReadsTurnsTotalsAndRepoCounts pins the Stats refresh: the
// recent turn metrics, the session's usage totals and the repo's counts.
func TestRefreshSheetReadsTurnsTotalsAndRepoCounts(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.CreateSession("s1", projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for _, row := range []db.TurnMetricsRow{
		{ProjectID: projectID, SessionID: "s1", StartedAt: time.Unix(1000, 0).UTC(), PromptTokens: 100, CompletionTokens: 20, CacheReadTokens: 50},
		{ProjectID: projectID, SessionID: "s1", StartedAt: time.Unix(2000, 0).UTC(), PromptTokens: 200, CompletionTokens: 40},
	} {
		if _, err := database.InsertTurnMetrics(row); err != nil {
			t.Fatalf("InsertTurnMetrics: %v", err)
		}
	}
	indexedAt := time.Unix(3000, 0).UTC()
	if err := database.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "a.go", Language: "go", Hash: "h1", LastIndexedAt: indexedAt},
		{Path: "b.go", Language: "go", Hash: "h2", LastIndexedAt: indexedAt},
	}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}
	if err := database.SaveSymbols(projectID, []db.Symbol{
		{FilePath: "a.go", Kind: "function", Name: "A"},
		{FilePath: "a.go", Kind: "function", Name: "B"},
		{FilePath: "b.go", Kind: "type", Name: "C"},
	}); err != nil {
		t.Fatalf("SaveSymbols: %v", err)
	}

	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{DB: database, SessionID: "s1"})
	_, sess := New(Deps{State: state, ProjectID: projectID})

	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Stats: true}); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	got := sess.Sheet(0)
	wantTurns := []acpwire.SheetTurn{
		{StartedAt: 2000000, PromptTokens: 200, CompletionTokens: 40},
		{StartedAt: 1000000, PromptTokens: 100, CompletionTokens: 20, CacheReadTokens: 50},
	}
	if !reflect.DeepEqual(got.Turns, wantTurns) {
		t.Errorf("Turns = %+v, want %+v", got.Turns, wantTurns)
	}
	wantTotals := acpwire.SheetTotals{Turns: 2, PromptTokens: 300, CompletionTokens: 60, CacheReadTokens: 50}
	if got.Totals != wantTotals {
		t.Errorf("Totals = %+v, want %+v", got.Totals, wantTotals)
	}
	if want := (acpwire.SheetRepo{Files: 2, Symbols: 3}); got.Repo != want {
		t.Errorf("Repo = %+v, want %+v", got.Repo, want)
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
	h.Swap(newState(t, "s2"))

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
