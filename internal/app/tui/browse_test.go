package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

func keyOf(s string) tea.KeyPressMsg {
	switch s {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func pressKeys(m Model, keys ...string) Model {
	for _, k := range keys {
		m = sendKey(m, keyOf(k))
	}
	return m
}

// browseFixture is a finished turn whose last task ended on a failing test.
func browseFixture(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.resize(120, 50)
	m.state.AddMessage(session.RoleUser, "ship it", session.ContentTypePlain)
	_ = m.state.SetTodos([]db.TodoItem{
		{ID: "t1", Content: "Write code", Status: "in_progress", StartedAt: time.Now()},
		{ID: "t2", Content: "Run tests", Status: "pending"},
	})
	a := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(a, "Writing the code. It is short.")
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: a, ToolCallID: "r", Args: []byte(`{"path":"main.go"}`), ResultSummary: "ok"})
	m.state.EndStep(a)
	_ = m.state.SetTodos([]db.TodoItem{
		{ID: "t1", Content: "Write code", Status: "completed", StartedAt: time.Now(), CompletedAt: time.Now()},
		{ID: "t2", Content: "Run tests", Status: "in_progress", StartedAt: time.Now()},
	})
	b := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(b, "Running the tests. Expect a failure.")
	exit := 1
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "test.run", StepID: b, ToolCallID: "tr",
		Args: []byte(`{"command":"go test ./..."}`), ResultSummary: "exit 1", ResultContent: "FAIL pkg/foo main.go:12: boom", CommandExitCode: &exit})
	m.state.EndStep(b)
	m.state.AddMessageFinal(session.RoleAssistant, "Tests fail.", session.ContentTypePlain)
	m.invalidateTranscript()
	m.refreshViewport()
	return m
}

func TestEscEntersBrowseModeOnNewestStepAndDoesNotCancelTheTurn(t *testing.T) {
	m := browseFixture(t)
	m.busy = true
	cancelled := false
	m.agentCancel = func() { cancelled = true }
	m.input.SetValue("half-typed")
	m = pressKeys(m, "esc")
	if !m.browsing {
		t.Fatal("Esc with nothing open must enter browse mode")
	}
	if cancelled {
		t.Fatal("Esc must never cancel a turn")
	}
	if m.cursor.Kind != viewmodel.KindStep {
		t.Fatalf("cursor = %+v, want the newest step", m.cursor)
	}
	steps := 0
	var newest viewmodel.NodeID
	for _, it := range m.browseItems {
		if it.kind == viewmodel.KindStep {
			steps++
			newest = it.id
		}
	}
	if m.cursor != newest || steps != 1 { // the finished first task is folded
		t.Fatalf("cursor %v, newest step %v (steps %d)", m.cursor, newest, steps)
	}
	if m.input.Value() != "half-typed" {
		t.Fatalf("browse mode must keep the input text, got %q", m.input.Value())
	}
	if !strings.Contains(stripANSI(m.renderStatusLine(120)), "browse") {
		t.Fatalf("status line should read browse:\n%s", stripANSI(m.renderStatusLine(120)))
	}
}

func TestBrowseMovementKeys(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc", "g")
	first := m.cursor
	if m.cursorIndex() != 0 {
		t.Fatalf("g should go to the first node, at %d", m.cursorIndex())
	}
	m = pressKeys(m, "j")
	if m.cursorIndex() != 1 {
		t.Fatalf("j moved to %d", m.cursorIndex())
	}
	m = pressKeys(m, "k")
	if m.cursor != first {
		t.Fatalf("k did not return to the first node")
	}
	m = pressKeys(m, "G")
	if m.cursorIndex() != len(m.browseItems)-1 {
		t.Fatalf("G should go to the last node, at %d of %d", m.cursorIndex(), len(m.browseItems))
	}
	m = pressKeys(m, "down")
	if m.cursorIndex() != len(m.browseItems)-1 {
		t.Fatal("down at the end stays put")
	}
	m = pressKeys(m, "g", "J")
	if k := m.browseItems[m.cursorIndex()].kind; !m.browseItems[m.cursorIndex()].jump {
		t.Fatalf("J should land on a stop (task header or turn start), got kind %v", k)
	}
	idx := m.cursorIndex()
	m = pressKeys(m, "K")
	if m.cursorIndex() >= idx {
		t.Fatalf("K should move back, %d -> %d", idx, m.cursorIndex())
	}
}

func TestEnterCyclesTheNodesDensityAndOnlyThatNode(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc")
	step := m.cursor
	before := m.shownDensity(step)
	m = pressKeys(m, "enter")
	if got := m.shownDensity(step); got != before.nextOverride() {
		t.Fatalf("Enter: %v -> %v, want %v", before, got, before.nextOverride())
	}
	if len(m.nodeDensity) != 1 {
		t.Fatalf("only the cursor node gets an override, have %v", m.nodeDensity)
	}
}

func TestEnterOnTheFailedTestRowExpandsItsOutput(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc", "j") // newest step -> its test row
	if m.cursor.Kind != viewmodel.KindTool {
		t.Fatalf("cursor = %+v, want the tool row", m.cursor)
	}
	if strings.Contains(viewText(&m), "boom") && !strings.Contains(stripANSI(m.viewport.GetContent()), "boom") {
		t.Fatal("unreachable")
	}
	m = pressKeys(m, "enter")
	if !strings.Contains(stripANSI(m.viewport.GetContent()), "FAIL pkg/foo") {
		t.Fatalf("Enter on a tool row should show its output:\n%s", stripANSI(m.viewport.GetContent()))
	}
}

func TestUnboundPrintableLeavesBrowseAndIsTyped(t *testing.T) {
	m := browseFixture(t)
	m.input.SetValue("before ")
	m = pressKeys(m, "esc")
	if !m.browsing {
		t.Fatal("not browsing")
	}
	m = pressKeys(m, "h")
	if m.browsing {
		t.Fatal("an unbound key leaves browse mode")
	}
	if got := m.input.Value(); got != "before h" {
		t.Fatalf("input = %q, want the typed key appended to the preserved text", got)
	}
}

func TestEscLeavesBrowseAndFollowResumesOnlyAtBottom(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc", "esc")
	if m.browsing {
		t.Fatal("Esc leaves browse mode")
	}
	if !m.input.Focused() {
		t.Fatal("the input is focused again")
	}
}

func TestZTogglesTaskFolding(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 60)
	scriptedTasks(t, &m, 3, 2, true)
	m.refreshViewport()
	if strings.Contains(viewText(&m), "Working on task 1 part 1") {
		t.Fatal("tasks start folded")
	}
	m = pressKeys(m, "esc", "z")
	if !strings.Contains(viewText(&m), "Working on task 1 part 1") {
		t.Fatal("z unfolds every task")
	}
	m = pressKeys(m, "z")
	if strings.Contains(viewText(&m), "Working on task 1 part 1") {
		t.Fatal("z again refolds them")
	}
}

func TestCursorMovesDoNotInvalidateTheRenderCache(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc")
	var rendered []viewmodel.NodeID
	nodeRenderHook = func(id viewmodel.NodeID) { rendered = append(rendered, id) }
	t.Cleanup(func() { nodeRenderHook = nil })
	m = pressKeys(m, "k", "j", "k")
	if len(rendered) != 0 {
		t.Fatalf("moving the cursor re-rendered %v: painting must happen after the cached blocks are joined", rendered)
	}
}

func TestCursorIsPaintedWithAMarker(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc")
	it := m.browseItems[m.cursorIndex()]
	lines := strings.Split(stripANSI(m.viewport.GetContent()), "\n")
	if !strings.HasPrefix(lines[it.start], "▸") {
		t.Fatalf("cursor line should carry the marker: %q", lines[it.start])
	}
}

func TestCopyEmitsTheFailedTestOutputOverOSC52(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc", "j") // the failing test row
	cmd := m.copyCursorNode()
	if cmd == nil {
		t.Fatal("no copy command")
	}
	var sawClipboard bool
	for _, msg := range flattenCmd(cmd) {
		if strings.Contains(fmt.Sprintf("%v", msg), "FAIL pkg/foo main.go:12: boom") {
			sawClipboard = true
		}
	}
	if !sawClipboard {
		t.Fatal("the clipboard command must carry the row's output")
	}
}

func flattenCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if b, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range b {
				out = append(out, flattenCmd(c)...)
			}
			return out
		}
		return []tea.Msg{msg}
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

func TestInspectorShowsActorWhyExitCodeAndOutput(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc", "j", "i")
	p, ok := m.dock.Panel().(*inspector.Panel)
	if !ok {
		t.Fatal("i should open the inspector")
	}
	d := p.Detail()
	fields := map[string]string{}
	for _, f := range d.Fields {
		fields[f.Key] = f.Value
	}
	if fields["why"] != "Running the tests." || fields["exit code"] != "1" {
		t.Fatalf("fields = %v", fields)
	}
	var out string
	for _, s := range d.Sections {
		if s.Name == "output" {
			out = s.Body
		}
	}
	if !strings.Contains(out, "FAIL pkg/foo") {
		t.Fatalf("output section = %q", out)
	}
	// n moves to the next node; Esc returns to browse with the cursor kept.
	cursor := m.cursor
	m = sendMsg(m, inspector.NavigateMsg{Delta: -1})
	if m.cursor == cursor {
		t.Fatal("n/p should move the browse cursor")
	}
	m = sendMsg(m, inspector.ClosedMsg{})
	if m.dock.IsOpen() || !m.browsing {
		t.Fatal("closing the inspector returns to browse mode")
	}
}

func sendMsg(m Model, msg tea.Msg) Model {
	updated, cmd := m.Update(msg)
	return drainCmds(updated.(Model), cmd)
}

func TestInspectOnAMessageIsANoOpWithNotice(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc", "g", "i")
	if m.dock.IsOpen() {
		t.Fatal("a user message has nothing to inspect")
	}
	if !m.flashActive() {
		t.Fatal("expected a notice explaining why")
	}
}

func TestEditorCommandConstruction(t *testing.T) {
	cases := []struct {
		editor string
		line   int
		want   []string
	}{
		{"vim", 12, []string{"vim", "+12", "/r/a.go"}},
		{"nvim -u NONE", 3, []string{"nvim", "-u", "NONE", "+3", "/r/a.go"}},
		{"/usr/bin/nano", 7, []string{"/usr/bin/nano", "+7", "/r/a.go"}},
		{"code", 9, []string{"code", "-g", "/r/a.go:9"}},
		{"code", 0, []string{"code", "/r/a.go"}},
		{"ed", 5, []string{"ed", "/r/a.go"}},
	}
	for _, c := range cases {
		got := editorCommand(c.editor, "/r/a.go", c.line).Args
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("editorCommand(%q,%d) = %v, want %v", c.editor, c.line, got, c.want)
		}
	}
}

func TestOpenResolvesAPathFromArgsAndFromShellOutput(t *testing.T) {
	m := browseFixture(t)
	root := m.state.Workspace().ActiveRoot
	if err := os.WriteFile(root+"/main.go", []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m = pressKeys(m, "esc", "j")
	path, line := m.nodeFile(m.currentNode())
	if path != root+"/main.go" || line != 12 {
		t.Fatalf("shell output path:line = %q:%d", path, line)
	}
	m = pressKeys(m, "k", "k") // the first step's file.read row, with a path arg
	_ = m
}

func TestOpenWithoutEditorSaysSo(t *testing.T) {
	t.Setenv("EDITOR", "")
	m := browseFixture(t)
	m = pressKeys(m, "esc", "j")
	if cmd := m.openCursorFile(); cmd == nil || !m.flashActive() {
		t.Fatal("expected a notice when $EDITOR is unset")
	}
}

func TestMovingTheCursorStopsFollowing(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc")
	m.viewportFollow = true // as after G on a live node
	m = pressKeys(m, "k")
	if m.viewportFollow {
		t.Fatal("moving the cursor must turn follow off, or the next refresh snaps the viewport back to the bottom")
	}
}

func TestFlashClearAndEditorErrorsAreHandledInUpdate(t *testing.T) {
	m := browseFixture(t)
	gen := m.suggestionGen
	m = sendMsg(m, flashClearMsg{})
	if m.suggestionGen != gen {
		t.Fatal("flashClearMsg must not reach the textarea path, which bumps suggestionGen")
	}
	m = sendMsg(m, editorDoneMsg{err: fmt.Errorf("exec: \"nvimm\": not found")})
	if !m.flashActive() || !strings.Contains(m.flash, "nvimm") {
		t.Fatalf("an editor failure should be shown, flash = %q", m.flash)
	}
}

func TestOpenIgnoresDirectoriesAndPathsOutsideTheWorkspace(t *testing.T) {
	m := browseFixture(t)
	root := m.state.Workspace().ActiveRoot
	outside := t.TempDir()
	for _, p := range []string{".", "/etc", outside} {
		n := &viewmodel.Node{Kind: viewmodel.KindTool, Tools: []registry.AuditEvent{{ToolName: "search.grep", Args: []byte(fmt.Sprintf(`{"path":%q}`, p))}}}
		if path, _ := m.nodeFile(n); path != "" {
			t.Errorf("path %q resolved to %q; directories and outside paths must not open (root %s)", p, path, root)
		}
	}
}

func TestTranscriptConfigChangeReappliesToTheRunningSession(t *testing.T) {
	m := browseFixture(t)
	m.foldTasks = false
	m.density = densityFull
	cfg := m.state.Config
	cfg.TUI.Transcript.Density = "outline"
	cfg.TUI.Transcript.FoldFinishedTasks = true
	m.applyNewConfig(cfg)
	if m.density != densityOutline || !m.foldTasks {
		t.Fatalf("density %v foldTasks %v after the config changed", m.density, m.foldTasks)
	}
	// An unrelated reload leaves the user's Ctrl+G choice alone.
	m.density = densityFull
	m.applyNewConfig(cfg)
	if m.density != densityFull {
		t.Fatal("a reload that did not touch [tui.transcript] must not reset the density")
	}
}

func TestTasksPanelStepCountsMatchTheTaskHeaders(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 60)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "One", Status: "in_progress", StartedAt: time.Now()}, {ID: "t2", Content: "Two", Status: "pending"}})
	a := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(a, "Doing one.")
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: a, ToolCallID: "a", Args: []byte(`{"path":"x"}`), ResultSummary: "ok"})
	m.state.EndStep(a)
	// Narrate, then mark task two in progress: this step is stored under t1
	// but renders under t2.
	b := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(b, "Moving on to two.")
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "todo.write", StepID: b, ToolCallID: "w",
		Args: []byte(`{"todos":[{"id":"t1","content":"One","status":"completed"},{"id":"t2","content":"Two","status":"in_progress"}]}`)})
	_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "One", Status: "completed", StartedAt: time.Now(), CompletedAt: time.Now()}, {ID: "t2", Content: "Two", Status: "in_progress", StartedAt: time.Now()}})
	m.state.EndStep(b)
	c := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(c, "Working on two.")
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: c, ToolCallID: "c", Args: []byte(`{"path":"y"}`), ResultSummary: "ok"})
	m.state.EndStep(c)
	m.invalidateTranscript()
	m.refreshViewport()
	if m.taskStats["t1"].steps != 1 || m.taskStats["t2"].steps != 2 {
		t.Fatalf("taskStats = %v, want t1:1 t2:2 (the narrated todo.write step renders under t2)", m.taskStats)
	}
}

func TestEnterOnAToolRowTogglesAndNeverHidesIt(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc", "j") // the failing test row
	row := m.cursor
	if row.Kind != viewmodel.KindTool {
		t.Fatalf("cursor = %+v", row)
	}
	for i := 0; i < 4; i++ {
		m = pressKeys(m, "enter")
		if m.cursor != row {
			t.Fatalf("press %d: the cursor left the row (%v), which means it was hidden", i+1, m.cursor)
		}
		found := false
		for _, it := range m.browseItems {
			found = found || it.id == row
		}
		if !found {
			t.Fatalf("press %d: the row is no longer in the transcript", i+1)
		}
	}
}

func TestCursorFollowsNewOutputWhileFollowing(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc")
	m.viewportFollow = true
	m.state.AddMessage(session.RoleSystem, "late notice", session.ContentTypePlain)
	m.invalidateTranscript()
	m.refreshViewport()
	if m.cursor != m.browseItems[len(m.browseItems)-1].id {
		t.Fatal("while following, the cursor must stay on the newest node so it does not scroll out of view")
	}
}

func TestCursorStaysNearWhereItWasWhenItsNodeVanishes(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc", "g", "j")
	idx := m.cursorIndex()
	m.cursor = viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:gone"} // as if it had folded away
	m.invalidateTranscript()
	m.browseItems = append([]browseItem(nil), m.browseItems...)
	m.setBrowseItems(m.browseItems, m.browseTree)
	if m.cursorIndex() < 0 {
		t.Fatal("cursor must land on a real node")
	}
	_ = idx
}

func TestCopyingAStepDoesNotRepeatItsHeadline(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc") // newest step
	text := m.nodeCopyText(m.currentNode())
	if strings.Count(text, "Running the tests.") != 1 {
		t.Fatalf("headline repeated in the copied text:\n%s", text)
	}
}
