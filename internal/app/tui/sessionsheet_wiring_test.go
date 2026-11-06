package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient/inproc"
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
	m := testModelWith(inproc.Deps{State: state, Commands: reg}, WithHomeDir(t.TempDir()))
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
	stateOf(m).Config.TUI.SidePanel.Hidden = []string{"changed"}
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
	stateOf(m).RegisterSubagent("sdd task card", nil)
	views := stateOf(m).Subagents()
	if len(views) != 1 {
		t.Fatalf("registered subagents = %d, want 1", len(views))
	}
	m.drillIntoSubagent(views[0].ID)
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
	stateOf(m).RegisterSubagent("explore repo", child)
	stateOf(m).LogToolCall(registry.AuditEvent{ToolName: "zparent-only-probe"})
	m.drillIntoSubagent(stateOf(m).Subagents()[0].ID)
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
	stateOf(m).SetSwarmProgress(session.SwarmProgress{Active: true, Goal: "ship it"})
	child := newChildState(t)
	stateOf(m).RegisterSubagent("explore repo", child)

	if got := m.sheetDataFor().Swarm.Goal; got != "ship it" {
		t.Fatalf("Swarm.Goal = %q before the drill-in, want %q", got, "ship it")
	}
	m.drillIntoSubagent(stateOf(m).Subagents()[0].ID)
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
	stateOf(m).Config.TUI.SidePanel.Hidden = hidden
	m = ctrlB(m)
	if m.dock.IsOpen() || m.sheetPanel != nil {
		t.Fatal("an empty sheet must not open: it would swallow keys invisibly")
	}
	var found bool
	for _, msg := range stateOf(m).Messages() {
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

// TestSheetSectionsAreDividers pins that every section heading is a rule
// through the panel's frame, not a header row drawn inside it.
func TestSheetSectionsAreDividers(t *testing.T) {
	m, dir := gitModel(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m = ctrlB(m)
	rows := strings.Split(stripANSI(m.viewString()), "\n")

	// The sheet's top border carries the panel title, and the first section's
	// title when that divider merge (chrome.Frame.Render).
	start := -1
	for i, row := range rows {
		if strings.Contains(row, "Session") && (strings.HasPrefix(row, "╭") || strings.HasPrefix(row, "├")) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("no sheet top border in:\n%s", strings.Join(rows, "\n"))
	}

	var dividers int
	for _, row := range rows[start:] {
		if rest, ok := strings.CutPrefix(row, "├─ "); ok {
			name := strings.TrimPrefix(rest, "▸ ")
			if name != "" && name == strings.ToUpper(name) {
				dividers++
			}
		}
		if strings.HasPrefix(row, "│ ") && strings.Contains(row, "──") {
			t.Fatalf("section rule drawn inside the frame: %q\n%s", row, strings.Join(rows, "\n"))
		}
	}
	if dividers == 0 {
		t.Fatalf("no section divider row:\n%s", strings.Join(rows, "\n"))
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

// TestRulesDetailThroughModel pins the composed routing for the sheet's rules
// detail view: Enter on RULES opens the read-only view without dispatching a
// slash command or touching a rule, paging and resizing through Model.Update
// reach the last rule in the composed frame, and Escape returns to the sheet
// rather than closing it.
func TestRulesDetailThroughModel(t *testing.T) {
	m, _ := gitModel(t)

	var rules []string
	for i := 1; i <= 24; i++ {
		rules = append(rules, fmt.Sprintf("MODEL-RULE-%02d %s",
			i, strings.Repeat("allow the shell to run go test and gofmt here ", 2)))
	}
	for _, rule := range rules {
		stateOf(m).AddSessionRule(rule)
	}
	before := stateOf(m).SessionRules()

	m = ctrlB(m)
	if !m.sheetOpen() {
		t.Fatal("setup: Ctrl+B must open the session sheet")
	}

	press := func(t *testing.T, m Model, k tea.KeyPressMsg) (Model, tea.Cmd) {
		t.Helper()
		mm, cmd := m.Update(k)
		return asModel(t, mm), cmd
	}

	// Walk the selection to RULES.
	var selected bool
	for i := 0; i < 20; i++ {
		if strings.Contains(stripANSI(m.viewString()), "▸ RULES") {
			selected = true
			break
		}
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if !selected {
		t.Fatalf("could not select RULES:\n%s", stripANSI(m.viewString()))
	}

	// Enter opens the detail view: no slash command, no rule mutation.
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if run, ok := msg.(sessionsheet.RunCommandMsg); ok {
				t.Fatalf("Enter on RULES dispatched /%s instead of opening details", run.Command)
			}
		}
	}
	if !m.sheetOpen() {
		t.Fatal("opening the rules detail view must not close the sheet")
	}
	if after := stateOf(m).SessionRules(); !reflect.DeepEqual(after, before) {
		t.Fatalf("opening details mutated the session rules:\n%v\n->\n%v", before, after)
	}
	if opened := stripANSI(m.viewString()); !strings.Contains(opened, "esc back") {
		t.Fatalf("Enter on RULES did not open the detail view:\n%s", opened)
	}

	// Page and resize through the model until the last rule is on screen.
	var frames []string
	seen := func() string {
		frame := stripANSI(m.viewString())
		frames = append(frames, frame)
		return frame
	}
	var last string
	for i := 0; i < 100 && last == ""; i++ {
		if frame := seen(); strings.Contains(frame, "MODEL-RULE-24") {
			last = frame
			break
		}
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
		if i == 20 {
			// A resize mid-scroll must keep the detail view and its content.
			mm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			m = asModel(t, mm)
		}
	}
	if last == "" {
		t.Fatalf("paging through the model never reached the last rule:\n%s",
			frames[len(frames)-1])
	}

	// Escape returns to the sheet, not to the transcript.
	m, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, ok := msg.(sessionsheet.ClosedMsg); ok {
				t.Fatal("Escape in the rules detail view closed the sheet instead of going back")
			}
		}
	}
	if !m.sheetOpen() {
		t.Fatal("Escape in the rules detail view must return to the sheet")
	}
	back := stripANSI(m.viewString())
	if !strings.Contains(back, "RULES") {
		t.Fatalf("the sheet did not come back after leaving the detail view:\n%s", back)
	}
	if strings.Contains(back, "esc back") {
		t.Fatalf("Escape left the rules detail view open:\n%s", back)
	}
	if !strings.Contains(back, "↑↓ section") {
		t.Fatalf("the ordinary sheet hints did not come back:\n%s", back)
	}
	if after := stateOf(m).SessionRules(); !reflect.DeepEqual(after, before) {
		t.Fatalf("scrolling the rules detail view mutated the session rules:\n%v\n->\n%v", before, after)
	}
}
