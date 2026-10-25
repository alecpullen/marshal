package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/sessionsheet"
	"marshal/internal/commands"
	"marshal/internal/tools/registry"
)

func ctrlB(m Model) Model {
	mm, _ := m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	return mm.(Model)
}

// gitModel builds a model over a one-commit git repo with a tracked file,
// so changed-file tests have a real base ref to diff against.
func gitModel(t *testing.T) (Model, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v\n%s", err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	if err := commands.RegisterAll(reg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := testModel(state, WithCommandRegistry(reg), WithHomeDir(t.TempDir()))
	m.resize(100, 40)
	m.refreshViewport()
	return m, dir
}

func TestCtrlBOpensAndClosesSessionSheet(t *testing.T) {
	m, dir := gitModel(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m = ctrlB(m)
	if m.sheetPanel == nil || !m.dock.IsOpen() {
		t.Fatal("Ctrl+B must open the session sheet")
	}
	if got := m.sess.Status().ChangedFiles; got != 1 {
		t.Fatalf("opening the sheet must refresh the changed files, got %d", got)
	}
	if view := stripANSI(m.viewString()); !strings.Contains(view, "CHANGED") || !strings.Contains(view, "a.txt") {
		t.Fatalf("sheet should list the changed file:\n%s", view)
	}

	m = ctrlB(m)
	if m.dock.IsOpen() || m.sheetPanel != nil {
		t.Fatal("a second Ctrl+B must close the sheet")
	}
}

func TestEscClosesSessionSheet(t *testing.T) {
	m, dir := gitModel(t)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o644)
	m = ctrlB(m)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("Esc must ask to close the sheet")
	}
	mm, _ = m.Update(cmd())
	m = mm.(Model)
	if m.dock.IsOpen() {
		t.Fatal("sheet still open after Esc")
	}
}

func TestSessionSheetEnterRunsSectionCommand(t *testing.T) {
	m, dir := gitModel(t)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o644)
	m = ctrlB(m)

	// Walk the selection to the "changed" section.
	var cmd tea.Cmd
	for range 8 {
		view := stripANSI(m.viewString())
		if strings.Contains(view, "▸ CHANGED") {
			break
		}
		mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m = mm.(Model)
	}
	if !strings.Contains(stripANSI(m.viewString()), "▸ CHANGED") {
		t.Fatalf("could not select CHANGED:\n%s", stripANSI(m.viewString()))
	}
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("Enter on CHANGED must emit a command")
	}
	msg := cmd()
	run, ok := msg.(sessionsheet.RunCommandMsg)
	if !ok || run.Command != "diff" {
		t.Fatalf("Enter on CHANGED = %#v, want RunCommandMsg{diff}", msg)
	}
	mm, _ = m.Update(msg)
	m = asModel(t, mm)
	if m.sheetPanel != nil {
		t.Fatal("dispatching a section command must close the sheet")
	}
}

func TestSessionSheetHonoursHiddenFilter(t *testing.T) {
	m, dir := gitModel(t)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o644)
	m.state.Config.TUI.SidePanel.Hidden = []string{"changed"}
	m = ctrlB(m)
	if strings.Contains(stripANSI(m.viewString()), "CHANGED") {
		t.Fatal("hidden section still rendered in the sheet")
	}
}

// sheetData must hand the session-scoped totals to the sheet; without this
// the footer renders zeroes regardless of what the query returned.
func TestSheetDataCarriesTotals(t *testing.T) {
	stub := &stubSession{sheetPatch: func(sh *acpwire.SessionSheet) {
		sh.Totals = acpwire.SheetTotals{Turns: 5, PromptTokens: 1000, CompletionTokens: 200}
	}}
	m := testModelStub(seamState(t, "s1"), stub)
	got := m.sheetDataFor()
	if got.Totals.Turns != 5 || got.Totals.PromptTokens != 1000 {
		t.Errorf("Totals = %+v, want the session's aggregate", got.Totals)
	}
}

// Pipeline/SDD cards have no child state, so drillIntoSubagent must refuse
// them — the child-scoped sheet must never see a nil child.
func TestNilChildCardCannotDrillIn(t *testing.T) {
	m := newTestModel(t)
	m.state.RegisterSubagent("sdd task card", nil)
	views := m.state.Subagents()
	if len(views) != 1 {
		t.Fatalf("registered subagents = %d, want 1", len(views))
	}
	m.drillIntoSubagent(views[0])
	if len(m.viewStack) != 0 {
		t.Fatal("nil-Child card must not be pushed onto the view stack")
	}
	if got := m.drilledSheetID(); got != 0 {
		t.Errorf("drilledSheetID = %d with an empty view stack, want 0", got)
	}
}

// While drilled into a real subagent the sheet is child-scoped: it reads the
// child's tool-call log, not the parent's.
func TestSheetDataChildScopedDuringDrillIn(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	child.LogToolCall(registry.AuditEvent{ToolName: "zchild-only-probe"})
	m.state.RegisterSubagent("explore repo", child)
	m.state.LogToolCall(registry.AuditEvent{ToolName: "zparent-only-probe"})
	m.drillIntoSubagent(m.state.Subagents()[0])
	if len(m.viewStack) != 1 {
		t.Fatalf("viewStack len = %d, want 1 after drill-in", len(m.viewStack))
	}
	var names []string
	for _, tool := range m.sheetDataFor().Tools {
		names = append(names, tool.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "zchild-only-probe") || strings.Contains(joined, "zparent-only-probe") {
		t.Errorf("sheet tools = %q, want the child's only", joined)
	}
}

// A subagent's sheet carries no run progress: the swarm and SDD goals belong
// to the session.
func TestChildSheetDataLeavesOutTheRunProgress(t *testing.T) {
	m := newTestModel(t)
	m.state.SetSwarmProgress(session.SwarmProgress{Active: true, Goal: "ship it"})
	child := newChildState(t)
	m.state.RegisterSubagent("explore repo", child)

	if got := m.sheetDataFor().Swarm.Goal; got != "ship it" {
		t.Fatalf("Swarm.Goal = %q before the drill-in, want %q", got, "ship it")
	}
	m.drillIntoSubagent(m.state.Subagents()[0])
	if got := m.sheetDataFor().Swarm.Goal; got != "" {
		t.Errorf("Swarm.Goal = %q during the drill-in, want empty", got)
	}
}

func TestCtrlBSwitchesFromWorkToTheSheet(t *testing.T) {
	m, dir := gitModel(t)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o644)
	m.workSnapshot = workSnapOf(workRunningCard("work:a", workT0))

	updated, _ := m.dispatchCommand("/work")
	m = asModel(t, updated)
	if !m.tasksOpen() {
		t.Fatal("setup: the Work list should be open")
	}
	m = ctrlB(m)
	if !m.sheetOpen() || m.tasksOpen() {
		t.Fatal("Ctrl+B with the Work list open must switch to the sheet")
	}
	m = ctrlB(m)
	if m.dock.IsOpen() {
		t.Fatal("Ctrl+B on the open sheet must still close it")
	}
}

func TestSessionSheetWithNothingToShowDoesNotOpen(t *testing.T) {
	m := newTestModel(t)
	var hidden []string
	for _, s := range sessionsheet.DefaultSections() {
		hidden = append(hidden, s.ID())
	}
	m.state.Config.TUI.SidePanel.Hidden = hidden
	m = ctrlB(m)
	if m.dock.IsOpen() || m.sheetPanel != nil {
		t.Fatal("an empty sheet must not open: it would swallow keys invisibly")
	}
	var found bool
	for _, msg := range m.state.Messages() {
		if strings.Contains(msg.Content, "Nothing to show in the session sheet.") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a notice explaining why nothing opened")
	}
}

// If every section goes quiet while the sheet is open, the panel must still
// render something visible and Esc must still close it.
func TestSessionSheetThatEmptiesWhileOpenStaysVisible(t *testing.T) {
	panel := sessionsheet.NewPanel(nil, func() sessionsheet.Data { return sessionsheet.Data{} })
	if out := stripANSI(panel.View(100, 20)); !strings.Contains(out, "Nothing to show") {
		t.Fatalf("empty panel rendered %q", out)
	}
	if cmd := panel.Update(tea.KeyPressMsg{Code: tea.KeyEsc}); cmd == nil {
		t.Fatal("Esc must close an empty panel")
	}
}

// ±N files must be right from startup: Init asks the session to read HEAD and
// the diff off the UI thread.
func TestInitLoadsChangedFilesOffThread(t *testing.T) {
	m, dir := gitModel(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := m.sess.Status().ChangedFiles; got != 0 {
		t.Fatalf("New must not run git for the changed files, got %d", got)
	}
	runCmdChain(t, m.Init())
	if got := stripANSI(m.renderStatusLine(140)); !strings.Contains(got, "±1 file") {
		t.Fatalf("status line missing ±1 file after startup:\n%s", got)
	}
}

// Turn end does no git work on the UI thread: the diff arrives with the
// session's off-thread refresh, not from a synchronous read.
func TestTurnEndDoesNotDiffSynchronously(t *testing.T) {
	m, dir := gitModel(t)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644)
	m.busy = true
	mm, cmd := m.Update(agentFinishedMsg{})
	m = asModel(t, mm)
	if got := m.sess.Status().ChangedFiles; got != 0 {
		t.Fatal("handleAgentFinished diffed on the UI thread")
	}
	runCmdChain(t, cmd)
	if got := m.sess.Status().ChangedFiles; got != 1 {
		t.Fatalf("ChangedFiles = %d after the off-thread refresh, want 1", got)
	}
}

// Opening the sheet re-reads its cached values once, synchronously, so the
// first frame shows fresh values.
func TestOpeningTheSheetRefreshesIt(t *testing.T) {
	state := seamState(t, "s1")
	stub := &stubSession{}
	var got []acpwire.SheetRefresh
	stub.onRefreshSheet = func(req acpwire.SheetRefresh) { got = append(got, req) }
	m := testModelStub(state, stub)
	m.resize(100, 40)

	ctrlB(m)
	if want := []acpwire.SheetRefresh{{Changed: true, Stats: true, Fleet: true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RefreshSheet requests = %+v, want %+v", got, want)
	}
}

// Turn end rebases the sheet off the UI thread, and reads nothing
// synchronously.
func TestTurnEndRebasesTheSheet(t *testing.T) {
	state := seamState(t, "s1")
	stub := &stubSession{}
	var got []acpwire.SheetRefresh
	stub.onRefreshSheet = func(req acpwire.SheetRefresh) { got = append(got, req) }
	m := testModelStub(state, stub)
	m.resize(100, 40)
	m.busy = true

	mm, cmd := m.handleAgentFinished(agentFinishedMsg{err: nil})
	m = mm
	if len(got) != 0 {
		t.Fatalf("handleAgentFinished refreshed synchronously: %+v", got)
	}
	runCmdChain(t, cmd)
	if want := []acpwire.SheetRefresh{{Rebase: true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RefreshSheet requests = %+v, want %+v", got, want)
	}

	// With the sheet open, the turn end also refreshes its turn and fleet
	// values, still off the UI thread.
	m = ctrlB(m)
	if !m.sheetOpen() {
		t.Fatal("setup: the sheet should be open")
	}
	got = nil
	m.busy = true
	mm, cmd = m.handleAgentFinished(agentFinishedMsg{err: nil})
	m = mm
	if len(got) != 0 {
		t.Fatalf("handleAgentFinished refreshed synchronously with the sheet open: %+v", got)
	}
	runCmdChain(t, cmd)
	if want := []acpwire.SheetRefresh{{Rebase: true, Stats: true, Fleet: true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RefreshSheet requests = %+v, want %+v", got, want)
	}
}
