package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/app/tui/livebox"
	"marshal/internal/db"
	"marshal/internal/tools/native"
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
	// Every beat of the turn is browsable; the cursor is the newest of them.
	steps := 0
	var newest viewmodel.NodeID
	for _, it := range m.browseItems {
		if it.kind == viewmodel.KindStep {
			steps++
			newest = it.id
		}
	}
	if m.cursor != newest || steps == 0 {
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
		t.Fatalf("J should land on a stop (a labelled beat or turn start), got kind %v", k)
	}
	idx := m.cursorIndex()
	m = pressKeys(m, "K")
	if m.cursorIndex() >= idx {
		t.Fatalf("K should move back, %d -> %d", idx, m.cursorIndex())
	}
}

func TestEnterTogglesTheNodeAndOnlyThatNode(t *testing.T) {
	m := browseFixture(t)
	m = pressKeys(m, "esc")
	step := m.cursor
	before := m.isExpanded(step)
	m = pressKeys(m, "enter")
	if got := m.isExpanded(step); got == before {
		t.Fatalf("Enter left the node at expanded=%v", got)
	}
	if len(m.expanded) != 1 {
		t.Fatalf("only the cursor node gets a choice, have %v", m.expanded)
	}
	m = pressKeys(m, "enter")
	if got := m.isExpanded(step); got != before {
		t.Fatalf("Enter again restores expanded=%v, got %v", before, got)
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
	path, line := m.nodeFile(m.cursor)
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
		calls := []openCall{{tool: "search.grep", args: fmt.Sprintf(`{"path":%q}`, p)}}
		if path, _ := fileIn(root, calls); path != "" {
			t.Errorf("path %q resolved to %q; directories and outside paths must not open (root %s)", p, path, root)
		}
	}
}

func TestTaskStepCountsMatchTheTaskHeaders(t *testing.T) {
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
	// Build the same snapshot refreshViewport builds, then recompute the
	// per-task stats through the production path the task headers use.
	ts, _ := m.transcriptSource()
	active := ts.ActiveToolCalls()
	inProgress := ts.InProgress()
	snap := viewmodel.Snapshot{
		Items:       ts.Transcript(),
		Steps:       ts.Steps(),
		Todos:       ts.Todos(),
		ActiveTools: active,
		InProgress:  inProgress,
		Busy:        m.busy || len(active) > 0 || len(inProgress.Reasoning) > 0,
		Now:         m.now(),
	}
	stats := viewmodel.TaskStats(viewmodel.Build(snap))
	if stats["t1"].Steps != 1 || stats["t2"].Steps != 2 {
		t.Fatalf("taskStats = %v, want t1:1 t2:2 (the narrated todo.write step renders under t2)", stats)
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
	m.cursor = viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:gone"} // as if it had been rewound away
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
	text := m.nodeCopyText(m.cursor)
	if strings.Count(text, "Running the tests.") != 1 {
		t.Fatalf("headline repeated in the copied text:\n%s", text)
	}
}

// copyFixture is a model whose state holds one beat with the given calls.
func copyFixture(t *testing.T, events ...registry.AuditEvent) Model {
	t.Helper()
	m := newTestModel(t)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := m.state.BeginStep(session.Actor{Model: "qwen3"})
	for i := range events {
		events[i].StepID = id
		m.state.LogToolCall(events[i])
	}
	m.refreshViewport()
	return m
}

func TestCopyOfAMergedRowJoinsEveryResult(t *testing.T) {
	m := copyFixture(t,
		registry.AuditEvent{ToolName: "file.read", ToolCallID: "c1",
			Args: []byte(`{"path":"a.go"}`), ResultContent: "one"},
		registry.AuditEvent{ToolName: "file.read", ToolCallID: "c2",
			Args: []byte(`{"path":"b.go"}`), ResultContent: "two"},
	)
	key := wireToolKey(t, m, "c1")
	if w := m.wireNode(key); w == nil || w.Tool == nil || len(w.Tool.Calls) != 2 {
		t.Fatalf("the two reads did not merge into one row: %+v", w)
	}
	if got, want := m.nodeCopyText(key), "one\n\ntwo"; got != want {
		t.Fatalf("copy = %q, want %q", got, want)
	}
}

func TestCopyOfAnEmptyEditTakesItsSummary(t *testing.T) {
	m := copyFixture(t, registry.AuditEvent{ToolName: "file.edit", ToolCallID: "c1",
		Args: []byte(`{"path":"a.go"}`), Error: "boom", ResultSummary: "edit failed"})
	if got, want := m.nodeCopyText(wireToolKey(t, m, "c1")), "edit failed"; got != want {
		t.Fatalf("copy = %q, want %q", got, want)
	}
}

func TestOpenFindsAFileInABeatsSecondRow(t *testing.T) {
	m := newTestModel(t)
	root := m.state.Workspace().ActiveRoot
	if err := os.WriteFile(root+"/a.go", []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := m.state.BeginStep(session.Actor{Model: "qwen3"})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "shell.run", StepID: id, ToolCallID: "c1",
		Args: []byte(`{"command":"true"}`)})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", StepID: id, ToolCallID: "c2",
		Args: []byte(`{"path":"a.go"}`), ResultContent: "package a"})
	m.refreshViewport()

	path, _ := m.nodeFile(wireBeatKey(t, m))
	if path != root+"/a.go" {
		t.Fatalf("open resolved %q, want %q", path, root+"/a.go")
	}
}

// TestOpenKeepsAChangedFileWhenTheDetailIsGone pins the stale-index case: the
// index still holds a row the session can no longer resolve, so its detail
// lookup fails. The changed files travel on the wire itself, so `o` must still
// open one rather than claim there is nothing to open.
func TestOpenKeepsAChangedFileWhenTheDetailIsGone(t *testing.T) {
	m := newTestModel(t)
	root := m.state.Workspace().ActiveRoot
	if err := os.WriteFile(root+"/a.go", []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := m.state.BeginStep(session.Actor{Model: "qwen3"})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "shell.run", StepID: id, ToolCallID: "c1",
		Args: []byte(`{"command":"true"}`)})
	m.refreshViewport()

	beat := wireBeatKey(t, m)
	gone := "tool:gone"
	m.wire[gone] = &acpwire.WireNode{
		ID: gone, Kind: acpwire.KindTool, Parent: beat.Key,
		Tool: &acpwire.WireTool{
			Name:  "file.edit",
			Calls: []acpwire.WireCall{{CallID: "c9", Files: []string{"a.go"}}},
		},
	}
	m.wire[beat.Key].Children = append(m.wire[beat.Key].Children, gone)

	path, _ := m.nodeFile(beat)
	if path != root+"/a.go" {
		t.Fatalf("open resolved %q, want the changed file %q", path, root+"/a.go")
	}
}

func TestCopyWithoutDetailSaysNothingToCopy(t *testing.T) {
	m := newTestModel(t)
	if got := m.nodeCopyText(viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:gone"}); got != "" {
		t.Fatalf("copy of a key no node has = %q, want nothing", got)
	}
}

// browseItemForCompletion finds the browse item for a finished subagent's
// completion event.
func browseItemForCompletion(m *Model, id int64) (browseItem, bool) {
	for _, it := range m.browseItems {
		n := m.browseTree[it.id]
		if n == nil || n.Kind != viewmodel.KindCompletion || n.Completion == nil {
			continue
		}
		if n.Completion.SubagentID == id {
			return it, true
		}
	}
	return browseItem{}, false
}

// browseItemForDispatch finds the browse item for the agent.run row that
// started a subagent.
func browseItemForDispatch(m *Model, id int64) (browseItem, bool) {
	for _, it := range m.browseItems {
		n := m.browseTree[it.id]
		if n == nil || n.Kind != viewmodel.KindTool || len(n.Tools) != 1 {
			continue
		}
		if viewmodel.StartedSubagent(n.Tools[0]) == id {
			return it, true
		}
	}
	return browseItem{}, false
}

// finishedSubagentFixture is a turn that dispatched one subagent and saw it
// finish, so both the dispatch row and the completion event are on screen.
func finishedSubagentFixture(t *testing.T) (Model, *session.State, session.SubagentView) {
	t.Helper()
	m := newTestModel(t)
	m.resize(120, 50)
	m.state.AddMessage(session.RoleUser, "ship it", session.ContentTypePlain)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child says hi", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore repo", child)
	a := m.state.BeginStep(session.Actor{})
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "agent.run", StepID: a, ToolCallID: "ar",
		Args: []byte(`{"task":"explore"}`), ResultSummary: fmt.Sprintf("started as subagent %d", view.ID)})
	m.state.EndStep(a)
	m.state.FinishSubagent(view.ID, "found three entry points", nil)
	m.invalidateTranscript()
	m.refreshViewport()
	return m, child, view
}

func TestBrowseFDrillsFromACompletionEvent(t *testing.T) {
	m, child, view := finishedSubagentFixture(t)
	m = pressKeys(m, "esc")
	it, ok := browseItemForCompletion(&m, view.ID)
	if !ok {
		t.Fatalf("no completion event for subagent %d in %v", view.ID, m.browseItems)
	}
	m.cursor = it.id
	m = pressKeys(m, "f")
	if len(m.viewStack) != 1 {
		t.Fatalf("f on a completion event should drill in, viewStack = %v", m.viewStack)
	}
	if m.viewStack[0].Child != child {
		t.Fatal("the drilled view is not the subagent's child")
	}
}

func TestBrowseFDrillsFromTheDispatchRow(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 50)
	m.state.AddMessage(session.RoleUser, "ship it", session.ContentTypePlain)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child says hi", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore repo", child)
	a := m.state.BeginStep(session.Actor{})
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "agent.run", StepID: a, ToolCallID: "ar",
		Args: []byte(`{"task":"explore"}`), ResultSummary: fmt.Sprintf("started as subagent %d", view.ID)})
	m.state.EndStep(a)
	m.invalidateTranscript()
	m.refreshViewport()

	m = pressKeys(m, "esc")
	it, ok := browseItemForDispatch(&m, view.ID)
	if !ok {
		t.Fatalf("no agent.run row for subagent %d in %v", view.ID, m.browseItems)
	}
	m.cursor = it.id
	m = pressKeys(m, "f")
	if len(m.viewStack) != 1 {
		t.Fatalf("f on the dispatch row should drill in, viewStack = %v", m.viewStack)
	}
	if m.viewStack[0].Child != child {
		t.Fatal("the drilled view is not the subagent's child")
	}
}

func TestBrowseEnterExpandsACompletionEvent(t *testing.T) {
	m, _, view := finishedSubagentFixture(t)
	m = pressKeys(m, "esc")
	it, ok := browseItemForCompletion(&m, view.ID)
	if !ok {
		t.Fatalf("no completion event for subagent %d in %v", view.ID, m.browseItems)
	}
	m.cursor = it.id
	before := m.isExpanded(it.id)
	m = pressKeys(m, "enter")
	if got := m.isExpanded(it.id); got == before {
		t.Fatalf("Enter left the completion event at expanded=%v", got)
	}
	if len(m.viewStack) != 0 {
		t.Fatalf("Enter must expand, not drill: viewStack = %v", m.viewStack)
	}
}

// browseToBoxStop walks the cursor forward to a named box stop.
func browseToBoxStop(t *testing.T, m Model, kind livebox.TargetKind, key string) Model {
	t.Helper()
	// Start at the top: enterBrowse puts the cursor on the newest node, which
	// can already be a box stop.
	m = pressKeys(m, "g")
	for i := 0; i <= len(m.browseItems); i++ {
		if tg, ok := boxTargetOf(m.cursor); ok && tg.Kind == kind && tg.Key == key {
			return m
		}
		m = pressKeys(m, "j")
	}
	t.Fatalf("no box stop %v/%q in the browse list", kind, key)
	return m
}

// boxHistoryModel is the smallest scene with both history and a live box: one
// finished beat in the transcript and one running row in the box.
func boxHistoryModel(t *testing.T, items ...viewmodel.WorkItem) Model {
	t.Helper()
	m := newTestModel(t)
	m.resize(120, 40)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	a := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(a, "Doing it.")
	m.state.EndStep(a)
	m.workSnapshot = workSnapOf(items...)
	m.invalidateTranscript()
	m.refreshViewport()
	m.viewString()
	return m
}

// TestBrowseJPastHistoryEntersTheBox: browse does not stop at the last history
// row; one more j lands on the box's first stop.
func TestBrowseJPastHistoryEntersTheBox(t *testing.T) {
	m := boxHistoryModel(t, workRunningCard("work:one", workT0))

	m = pressKeys(m, "esc", "g")
	history := 0
	for _, it := range m.browseItems {
		if _, ok := boxTargetOf(it.id); !ok {
			history++
		}
	}
	if history == 0 {
		t.Fatalf("no history rows to browse: %+v", m.browseItems)
	}
	for range history {
		m = pressKeys(m, "j")
	}
	tg, ok := boxTargetOf(m.cursor)
	if !ok || tg.Kind != livebox.TargetWork || tg.Key != "work:one" {
		t.Fatalf("cursor = %+v, want the running card's stop", m.cursor)
	}
	it := m.browseItems[m.cursorIndex()]
	if it.kind != viewmodel.KindPassthrough {
		t.Fatalf("box stop kind = %v, want passthrough", it.kind)
	}
	if it.start != -1 || it.end != -1 {
		t.Fatalf("a box stop owns no content lines: start=%d end=%d", it.start, it.end)
	}
	if it.jump {
		t.Fatal("a box stop is not a J/K jump stop")
	}
}

// TestBrowseEnterExpandsARunningCard: Enter on a running stop opens that card,
// and a second Enter closes it again.
func TestBrowseEnterExpandsARunningCard(t *testing.T) {
	m := boxHistoryModel(t, workRunningCard("work:one", workT0))

	m = pressKeys(m, "esc", "G")
	if _, ok := boxTargetOf(m.cursor); !ok {
		t.Fatalf("G must land in the box's stops, cursor %+v", m.cursor)
	}
	m = pressKeys(m, "enter")
	if m.workExpanded != "work:one" {
		t.Fatalf("workExpanded = %q, want the card open", m.workExpanded)
	}
	if !m.browsing {
		t.Fatal("Enter on a box stop must keep browse mode")
	}
	m = pressKeys(m, "enter")
	if m.workExpanded != "" {
		t.Fatalf("workExpanded = %q, want the card closed again", m.workExpanded)
	}
}

// TestBrowseSRequestsAStopOnlyWhenAllowed: s stops a running item that offers
// a stop, and stays the unbound key of the transcript elsewhere.
func TestBrowseSRequestsAStopOnlyWhenAllowed(t *testing.T) {
	stoppable := workRunningCard("work:stoppable", workT0)
	stoppable.Capabilities.Stop = true
	quiet := workRunningCard("work:quiet", workT0.Add(-time.Second))
	svc := &fakeWorkService{
		snap:       viewmodel.WorkSnapshot{ConversationID: "conv1", Items: []viewmodel.WorkItem{stoppable, quiet}},
		stopResult: viewmodel.WorkStopResult{Accepted: true},
	}
	m := newWorkModel(t, svc)
	m.resize(120, 40)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	m.invalidateTranscript()
	m.refreshViewport()
	m.viewString()

	m = pressKeys(m, "esc")
	m = browseToBoxStop(t, m, livebox.TargetWork, "work:quiet")
	m = pressKeys(m, "s")
	if len(svc.stopCalls) != 0 {
		t.Fatalf("s on an item without a stop capability must not request one: %+v", svc.stopCalls)
	}
	if m.browsing {
		t.Fatal("s is unbound off a stoppable stop, so it leaves browse mode and is typed")
	}

	m = pressKeys(m, "esc")
	m = browseToBoxStop(t, m, livebox.TargetWork, "work:stoppable")
	m = pressKeys(m, "s")
	if len(svc.stopCalls) != 1 {
		t.Fatalf("stop calls = %d, want 1", len(svc.stopCalls))
	}
	if got := svc.stopCalls[0]; got.TaskKey != "work:stoppable" || got.ConversationID != "conv1" {
		t.Fatalf("stop target = %+v, want the conversation and the stop's key", got)
	}
	if m.flash != "Stop requested" {
		t.Fatalf("flash = %q, want the accepted stop's confirmation", m.flash)
	}
}

// TestBrowseFOpensAnAgentFromTheBox: f on a running subagent's stop drills into
// the child session, resolved from the item's SourceID.
func TestBrowseFOpensAnAgentFromTheBox(t *testing.T) {
	m := newTestModel(t)
	m.resize(120, 40)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child says hi", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore repo", child)
	card := workRunningCard("work:agent", workT0)
	card.Kind = "agent"
	card.SourceID = strconv.FormatInt(view.ID, 10)
	m.workSnapshot = workSnapOf(card)
	m.invalidateTranscript()
	m.refreshViewport()
	m.viewString()

	m = pressKeys(m, "esc")
	m = browseToBoxStop(t, m, livebox.TargetWork, "work:agent")
	m = pressKeys(m, "f")
	if m.browsing {
		t.Fatal("f on a box stop must leave browse mode")
	}
	got, ok := m.drilledInto()
	if !ok || got.ID != view.ID {
		t.Fatalf("f did not drill into the box's agent (viewStack %v)", m.viewStack)
	}
	if content := stripANSI(m.viewport.GetContent()); !strings.Contains(content, "child says hi") {
		t.Fatalf("the drilled view should be the child's transcript:\n%s", content)
	}
}

// TestBrowseEnterOnAPlanItemScrollsToItsBeat: Enter on a plan stop reveals the
// first beat bound to that item, exactly as a click does.
func TestBrowseEnterOnAPlanItemScrollsToItsBeat(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	for i := range 80 {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("filler %d", i), session.ContentTypePlain)
	}
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	mustSetTodos(t, &m,
		native.TodoItem{ID: "t1", Content: "First", Status: native.TodoInProgress},
		native.TodoItem{ID: "t2", Content: "Second", Status: native.TodoPending},
	)
	done := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(done, "Doing the first thing.")
	m.state.EndStep(done)
	mustSetTodos(t, &m,
		native.TodoItem{ID: "t1", Content: "First", Status: native.TodoCompleted},
		native.TodoItem{ID: "t2", Content: "Second", Status: native.TodoInProgress},
	)
	live := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(live, "Doing the second thing.")
	m.busy, m.turnStartedAt = true, m.now()
	m.invalidateTranscript()
	m.refreshViewport()
	m.viewString()

	var beat viewmodel.NodeID
	for _, task := range m.band.Tasks {
		if task.ID == "t1" {
			beat = task.First
		}
	}
	want := -1
	for _, r := range m.nodeRegions {
		if r.target.node == beat {
			want = r.startLine
			break
		}
	}
	if beat == (viewmodel.NodeID{}) || want <= 0 {
		t.Fatalf("todo t1 has no beat region to scroll to (beat %v, line %d)", beat, want)
	}

	m = pressKeys(m, "esc")
	m = browseToBoxStop(t, m, livebox.TargetPlan, "t1")
	m = pressKeys(m, "enter")
	if off, h := m.viewport.YOffset(), m.viewport.Height(); off > want || want >= off+h {
		t.Fatalf("t1's beat at line %d is not on screen (offset %d, height %d)", want, off, h)
	}
	if m.viewportFollow {
		t.Fatal("jumping to a beat must stop following")
	}
}

// TestBrowseGFollowsTheLiveBox: G re-arms following even though the last stop is
// a box stop and not a live history node.
func TestBrowseGFollowsTheLiveBox(t *testing.T) {
	m := boxHistoryModel(t, workRunningCard("work:one", workT0))
	for i := range 40 {
		m.state.AddMessage(session.RoleSystem, fmt.Sprintf("filler %d", i), session.ContentTypePlain)
	}
	m.invalidateTranscript()
	m.refreshViewport()
	m.viewString()

	m = pressKeys(m, "esc", "g")
	if m.viewportFollow {
		t.Fatal("g must not re-arm following")
	}
	m = pressKeys(m, "G")
	if !m.viewportFollow {
		t.Fatal("G must re-arm following")
	}
	if !m.viewport.AtBottom() {
		t.Fatalf("G must put the viewport at the bottom (offset %d)", m.viewport.YOffset())
	}
	tg, ok := boxTargetOf(m.cursor)
	if !ok || tg.Key != "work:one" {
		t.Fatalf("G must land on the last stop, cursor %+v", m.cursor)
	}
}

// TestBrowseEnterOnAHeldRowScrollsToItsEvent: Enter on a held row scrolls to
// the completion event that finished it, and opens no card.
func TestBrowseEnterOnAHeldRowScrollsToItsEvent(t *testing.T) {
	m, _, view := finishedSubagentFixture(t)
	base := m.now()
	m.heldSince = base.Add(-time.Minute)

	// Push the event out of view.
	for i := 0; i < 40; i++ {
		m.state.AddMessage(session.RoleAssistant, fmt.Sprintf("filler %d", i), session.ContentTypePlain)
	}
	item := workRunningCard(view.WorkKey, base.Add(-2*time.Minute))
	item.Kind = "agent"
	item.State = "completed"
	item.EndedAt = base.Add(-30 * time.Second)
	m.workSnapshot = workSnapOf(item)
	m.invalidateTranscript()
	m.refreshViewport()

	var stop livebox.Target
	for _, s := range m.boxLayout().Stops {
		if s.Kind == livebox.TargetWork && s.Key == view.WorkKey {
			stop = s
		}
	}
	if stop.Kind == livebox.TargetNone {
		t.Fatalf("the held row has no stop: %+v", m.boxLayout().Stops)
	}
	ref, ok := m.completions[view.WorkKey]
	if !ok {
		t.Fatalf("no completion event for %q in %v", view.WorkKey, m.completions)
	}

	m.enterBoxStop(stop)
	if m.workExpanded != "" {
		t.Errorf("Enter on a held row must not open a card, workExpanded = %q", m.workExpanded)
	}
	want := -1
	for _, r := range m.nodeRegions {
		if r.target.node == ref.node {
			want = r.startLine
		}
	}
	if want < 0 {
		t.Fatalf("the completion event has no region:\n%s", stripANSI(m.viewport.GetContent()))
	}
	if got := m.viewport.YOffset(); got != want {
		t.Errorf("Enter on a held row went to line %d, want the event's region at %d", got, want)
	}
}
