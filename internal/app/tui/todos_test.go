package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/tools/native"
	"marshal/internal/viewmodel"
)

func sampleTodos(n, doneCount, inProgressAt int) []native.TodoItem {
	out := make([]native.TodoItem, 0, n)
	for i := 0; i < n; i++ {
		status := native.TodoPending
		switch {
		case i < doneCount:
			status = native.TodoCompleted
		case i == inProgressAt:
			status = native.TodoInProgress
		}
		out = append(out, native.TodoItem{Content: "task " + string(rune('a'+i)), Status: status})
	}
	return out
}

func TestTodoLineFitsWidthWithWideRunes(t *testing.T) {
	out := todoLine(native.TodoItem{Content: "これはとても長い日本語のタスク内容で幅を超えます", Status: native.TodoInProgress}, 20)
	if w := ansi.StringWidth(out); w > 20 {
		t.Fatalf("todo line is %d cells wide, budget 20: %q", w, out)
	}
}

// Completed todos are muted, not struck through: SGR 9 support is patchy and
// muted-plus-strikethrough was the least legible pairing in the UI. The ✓
// glyph carries the "done" meaning on its own.
func TestTodoLineDoneIsMutedNotStruckThrough(t *testing.T) {
	item := native.TodoItem{Content: "done task", Status: native.TodoCompleted}
	out := todoLine(item, 80)
	if strings.Contains(out, ";9m") || strings.Contains(out, "[9m") {
		t.Fatalf("completed todo must not use strikethrough:\n%q", out)
	}
	if !strings.Contains(stripANSITodo(out), "✓ done task") {
		t.Fatalf("completed todo missing ✓ glyph:\n%q", out)
	}
}

func TestTodoLineNoStrikethroughWhenPending(t *testing.T) {
	item := native.TodoItem{Content: "pending task", Status: native.TodoPending}
	out := todoLine(item, 80)
	if strings.Contains(out, "\x1b[9m") {
		t.Fatalf("pending todo must not use strikethrough style:\n%q", out)
	}
}

// stripANSITodo removes SGR sequences for glyph assertions.
func stripANSITodo(s string) string { return ansiTodoRe.ReplaceAllString(s, "") }

var ansiTodoRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// workTask builds a live Work item (a running command) for the Tasks-panel
// tests: kind "command" with no attachment counts as active membership.
func workTask(key, label string) viewmodel.WorkItem {
	return viewmodel.WorkItem{TaskKey: key, Kind: "command", Label: label, State: "running"}
}

// panelKey feeds one keypress through the model and returns the resulting model.
func panelKey(m Model, k tea.KeyPressMsg) Model {
	mm, _ := m.Update(k)
	return mm.(Model)
}

// panelKeyCmd feeds one keypress and returns the resulting model plus the
// command the panel emitted (the work request, for an item's Enter/Space).
func panelKeyCmd(m Model, k tea.KeyPressMsg) (Model, tea.Cmd) {
	mm, cmd := m.Update(k)
	return mm.(Model), cmd
}

// panelAction finds a Work row by ID and runs its action, returning the
// command (and therefore the request message) it emits. It is the direct
// entry point for rows a keypress would have to scroll the cursor onto.
func panelAction(p *tasksPanelModel, id string) tea.Cmd {
	for _, r := range p.list.Rows() {
		if r.ID == id && r.Act != nil {
			return r.Act()
		}
	}
	return nil
}

func TestWorkTasksInitialAndRememberedTab(t *testing.T) {
	// With todos and no Work projection, the first open defaults to Plan.
	m := newTestModel(t)
	if err := m.state.SetTodos([]native.TodoItem{{ID: "t1", Content: "a task", Status: native.TodoPending}}); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	m = ctrlT(m)
	if !m.tasksOpen() {
		t.Fatal("Ctrl+T with todos must open the Tasks panel")
	}
	if got := m.tasksPanel.activeTab; got != tasksTabPlan {
		t.Fatalf("first open tab = %v, want Plan", got)
	}

	// With no todos but a live Work projection, the first open defaults to Work.
	empty := newTestModel(t)
	empty.workSnapshot = workSnapOf(workTask("work:a", "Alpha"))
	empty = ctrlT(empty)
	if empty.tasksPanel.activeTab != tasksTabWork {
		t.Fatalf("first open with only work = %v, want Work", empty.tasksPanel.activeTab)
	}

	// Switching to Work, closing and reopening restores the last tab.
	m = panelKey(m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.tasksPanel.activeTab != tasksTabWork {
		t.Fatalf("after right = %v, want Work", m.tasksPanel.activeTab)
	}
	m = ctrlT(m)
	if m.dock.IsOpen() {
		t.Fatal("a second Ctrl+T must close the Tasks panel")
	}
	m = ctrlT(m)
	if got := m.tasksPanel.activeTab; got != tasksTabWork {
		t.Fatalf("reopened tab = %v, want the remembered Work", got)
	}
}

func TestWorkTasksPlanKeepsChildStats(t *testing.T) {
	m := newTestModel(t)
	if err := m.state.SetTodos([]native.TodoItem{{ID: "p1", Content: "parent task", Status: native.TodoPending}}); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	child := session.New(m.state.Config, t.TempDir(), time.Now(), session.Persistence{})
	if err := child.SetTodos([]native.TodoItem{{ID: "c1", Content: "child task", Status: native.TodoInProgress}}); err != nil {
		t.Fatalf("child SetTodos: %v", err)
	}
	m.viewStack = append(m.viewStack, session.SubagentView{ID: 1, Status: session.SubagentRunning, Child: child})

	m = ctrlT(m)
	if m.tasksPanel.activeTab != tasksTabPlan {
		t.Fatalf("todos must default to the Plan tab, got %v", m.tasksPanel.activeTab)
	}
	view := stripANSI(m.dock.View(m.leftWidth, m.height))
	if !strings.Contains(view, "child task") || strings.Contains(view, "parent task") {
		t.Fatalf("Plan must show the drilled-in child's list, not the parent's:\n%s", view)
	}

	// The Plan doc source drives the rows, so step counts come from the same
	// taskStats the transcript groups them with.
	p := m.tasksPanel
	p.SetSource(func() commands.Doc {
		return tasksDoc(child.Todos(), map[string]viewmodel.TaskStat{"c1": {Steps: 2, Work: time.Minute}})
	})
	view = stripANSI(m.dock.View(m.leftWidth, m.height))
	if !strings.Contains(view, "2 steps") {
		t.Fatalf("Plan must render the stats its source supplies:\n%s", view)
	}
}

func TestWorkTasksSearchPagingAndSelection(t *testing.T) {
	m := newTestModel(t)
	m.workSnapshot = workSnapOf(workTask("work:live1", "Live one"))
	m = ctrlT(m)
	if m.tasksPanel.activeTab != tasksTabWork {
		t.Fatalf("want the Work tab, got %v", m.tasksPanel.activeTab)
	}
	p := m.tasksPanel

	// "/" focuses the search box; a paste lands in it (paste support).
	m = panelKey(m, tea.KeyPressMsg{Code: '/', Text: "/"})
	if !p.searchOpen {
		t.Fatal("/ did not open the search box")
	}
	p.search, _ = p.search.Update(tea.PasteMsg{Content: "arch"})
	if got := p.search.Value(); got != "arch" {
		t.Fatalf("search value = %q, want the pasted arch", got)
	}

	// Enter applies the query and asks for page 1 (empty cursor).
	m, cmd := panelKeyCmd(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on the search box emitted no request")
	}
	req, ok := cmd().(workRequestMsg)
	if !ok {
		t.Fatalf("search request = %T, want workRequestMsg", cmd())
	}
	if req.kind != workReqList || req.query.View != "history" || req.query.Query != "arch" || req.query.Cursor != "" {
		t.Fatalf("page-1 request = %+v, want a history list for %q with no cursor", req, "arch")
	}
	if req.panelID != p.panelID {
		t.Fatalf("request panelID = %q, want %q", req.panelID, p.panelID)
	}

	// Page 1 arrives: the list replaces, and the opaque cursor is retained.
	if !p.applyPage(req.requestID, viewmodel.WorkPage{
		Items:      []viewmodel.WorkItem{workTask("work:a", "A"), workTask("work:b", "B")},
		NextCursor: "cur1",
	}, nil) {
		t.Fatal("page 1 was not applied")
	}
	if len(p.items) != 2 || p.nextCursor != "cur1" {
		t.Fatalf("after page 1: items=%d cursor=%q", len(p.items), p.nextCursor)
	}

	// A late page for a superseded request is ignored, never concatenated.
	if p.applyPage("bogus-request", viewmodel.WorkPage{Items: []viewmodel.WorkItem{workTask("work:z", "Z")}}, nil) {
		t.Fatal("a foreign request ID must be ignored")
	}

	// Select the second item; the next page asks for the retained cursor and
	// appends without moving the selection off TaskKey work:b.
	m = panelKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if p.selectedKey != "work:b" {
		t.Fatalf("selection = %q, want work:b", p.selectedKey)
	}
	cmd = panelAction(p, "work-next")
	if cmd == nil {
		t.Fatal("no next-page row after page 1")
	}
	req = cmd().(workRequestMsg)
	if req.query.Cursor != "cur1" {
		t.Fatalf("next-page cursor = %q, want cur1", req.query.Cursor)
	}
	p.applyPage(req.requestID, viewmodel.WorkPage{
		Items:      []viewmodel.WorkItem{workTask("work:c", "C")},
		NextCursor: "cur2",
	}, nil)
	if len(p.items) != 3 {
		t.Fatalf("after appending page 2: items=%d, want 3", len(p.items))
	}
	if p.selectedKey != "work:b" {
		t.Fatalf("selection moved to %q; it must reconcile by TaskKey", p.selectedKey)
	}

	// A stale cursor must not concatenate incompatible pages: the list is
	// unchanged and a refresh row asks for page 1.
	cmd = panelAction(p, "work-next")
	if cmd == nil {
		t.Fatal("no next-page row after page 2")
	}
	req = cmd().(workRequestMsg)
	before := len(p.items)
	if !p.applyPage(req.requestID, viewmodel.WorkPage{}, viewmodel.ErrWorkStaleCursor) {
		t.Fatal("the stale-cursor result was dropped")
	}
	if !p.stale || len(p.items) != before {
		t.Fatalf("stale cursor: stale=%v items=%d (want true, %d)", p.stale, len(p.items), before)
	}
	refresh := panelAction(p, "work-refresh")
	if refresh == nil {
		t.Fatal("a stale list must offer a refresh")
	}
	if rreq := refresh().(workRequestMsg); rreq.query.Cursor != "" {
		t.Fatalf("refresh cursor = %q, want page 1", rreq.query.Cursor)
	}
}

func TestWorkTasksInspectNeverAcknowledges(t *testing.T) {
	m := newTestModel(t)
	m.workSnapshot = workSnapOf(workTask("work:a", "Alpha"))
	m = ctrlT(m)
	p := m.tasksPanel

	// Enter on the selected live item inspects it; it never acknowledges.
	m, cmd := panelKeyCmd(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on an item emitted no request")
	}
	req, ok := cmd().(workRequestMsg)
	if !ok {
		t.Fatalf("Enter request = %T, want workRequestMsg", cmd())
	}
	if req.kind != workReqInspect || req.taskKey != "work:a" {
		t.Fatalf("Enter emitted %s/%s, want inspect/work:a", req.kind, req.taskKey)
	}
	if req.resultRevision != "" {
		t.Fatalf("inspect carried a revision %q; it must not acknowledge", req.resultRevision)
	}

	// Opening the detail does not acknowledge either; only the explicit action
	// does, and it carries the same revision the renderer would show.
	item := viewmodel.WorkItem{
		TaskKey: "work:a", State: "failed",
		Capabilities: viewmodel.WorkCapabilities{Inspect: true, Acknowledge: true},
	}
	if !p.applyDetail(req.requestID, viewmodel.WorkDetail{Item: item}, nil) {
		t.Fatal("the inspect result was not applied")
	}
	if !p.detailOpen {
		t.Fatal("detail did not open")
	}
	ack := panelAction(p, "d-ack")
	if ack == nil {
		t.Fatal("an acknowledgeable item must offer an ack action")
	}
	areq := ack().(workRequestMsg)
	if areq.kind != workReqAck || areq.taskKey != "work:a" || areq.resultRevision == "" {
		t.Fatalf("ack = %+v, want an acknowledgment with a revision", areq)
	}
	if want := viewmodel.WorkResultRevision(item); areq.resultRevision != want {
		t.Fatalf("ack revision = %q, want %q", areq.resultRevision, want)
	}

	// A result for a request the panel never made is dropped.
	if p.applyDetail("bogus-request", viewmodel.WorkDetail{}, nil) {
		t.Fatal("a foreign detail request must be ignored")
	}
}

func TestWorkTasksKeyboardEmitsExactTargets(t *testing.T) {
	m := newTestModel(t)
	m.workSnapshot = workSnapOf(workTask("work:a", "Alpha"), workTask("work:b", "Beta"))
	m = ctrlT(m)
	p := m.tasksPanel
	if p.selectedKey != "work:a" {
		t.Fatalf("initial selection = %q, want work:a", p.selectedKey)
	}

	// j moves to the second item; Enter inspects exactly that target.
	m = panelKey(m, tea.KeyPressMsg{Code: 'j', Text: "j"})
	if p.selectedKey != "work:b" {
		t.Fatalf("after j selection = %q, want work:b", p.selectedKey)
	}
	m, cmd := panelKeyCmd(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter emitted no request")
	}
	req := cmd().(workRequestMsg)
	if req.kind != workReqInspect || req.taskKey != "work:b" {
		t.Fatalf("Enter = %s/%s, want inspect/work:b", req.kind, req.taskKey)
	}
	if req.conversationID != m.state.SessionID() || req.panelID != p.panelID {
		t.Fatalf("request identity = %q/%q, want %q/%q", req.conversationID, req.panelID, m.state.SessionID(), p.panelID)
	}

	// k returns to the first item; Space expands exactly that live target.
	m = panelKey(m, tea.KeyPressMsg{Code: 'k', Text: "k"})
	if p.selectedKey != "work:a" {
		t.Fatalf("after k selection = %q, want work:a", p.selectedKey)
	}
	m, cmd = panelKeyCmd(m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if cmd == nil {
		t.Fatal("Space emitted no request")
	}
	req = cmd().(workRequestMsg)
	if req.kind != workReqExpand || req.taskKey != "work:a" {
		t.Fatalf("Space = %s/%s, want expand/work:a", req.kind, req.taskKey)
	}
}

// TestWorkAsyncReplyCannotReopenClosedPanel pins that a list reply arriving
// after its panel closed is dropped: it can neither reopen the panel nor mutate
// the closed one.
func TestWorkAsyncReplyCannotReopenClosedPanel(t *testing.T) {
	svc := &fakeWorkService{snap: viewmodel.WorkSnapshot{ConversationID: "conv", Revision: 1}}
	m := newWorkModel(t, svc)
	m.workSnapshot = viewmodel.WorkSnapshot{ConversationID: "conv", Revision: 1, Items: []viewmodel.WorkItem{workTask("work:a", "Alpha")}}
	m = ctrlT(m)
	if !m.tasksOpen() {
		t.Fatal("the Tasks panel did not open")
	}
	p := m.tasksPanel

	hist := panelAction(p, "work-history")
	if hist == nil {
		t.Fatal("no history action on the live view")
	}
	req := hist().(workRequestMsg)

	// Servicing happens outside Update: the model returns a cmd and the service
	// is not touched yet.
	updated, cmd := m.Update(req)
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("the list request produced no service cmd")
	}
	if svc.listCalls != 0 {
		t.Fatal("the Work service was called inside Update")
	}

	// The panel closes before the slow reply returns.
	panelID, conversationID, requestID := p.panelID, req.conversationID, req.requestID
	m = ctrlT(m)
	if m.tasksOpen() {
		t.Fatal("the Tasks panel did not close")
	}
	if m.tasksPanel != nil {
		t.Fatal("closing left a stale panel pointer")
	}

	// The late page cannot reopen or mutate the closed panel.
	updated, _ = m.Update(workPageMsg{
		conversationID: conversationID,
		panelID:        panelID,
		requestID:      requestID,
		page:           viewmodel.WorkPage{Items: []viewmodel.WorkItem{workTask("work:x", "X")}},
	})
	m = updated.(Model)
	if m.tasksPanel != nil || m.dock.IsOpen() {
		t.Fatal("a late page resurrected the closed Tasks panel")
	}
	if p.items != nil {
		t.Fatal("a late page mutated the closed panel's list")
	}
}

// TestWorkScrollAndMouseTargetsSurviveRefresh pins stable scroll anchoring and
// that the now bar's mouse targets are rebuilt from the planner on every frame,
// so a departed card leaves no stale target behind.
func TestWorkScrollAndMouseTargetsSurviveRefresh(t *testing.T) {
	svc := &fakeWorkService{snap: viewmodel.WorkSnapshot{ConversationID: "conv", Revision: 1}}
	m := newWorkModel(t, svc)
	// Tall enough that the live-work bar expands past its one-row summary.
	m.resize(120, 40)
	for i := 0; i < 200; i++ {
		m.state.AddMessage(session.RoleUser, fmt.Sprintf("line %d", i), session.ContentTypePlain)
	}
	m.workSnapshot = viewmodel.WorkSnapshot{ConversationID: "conv", Revision: 1, Items: []viewmodel.WorkItem{
		workTask("work:a", "Alpha"), workTask("work:b", "Beta"),
	}}
	m.invalidateTranscript()
	m.refreshViewport()
	_ = m.viewString()

	if len(m.workHits) != 2 {
		t.Fatalf("mouse targets = %d, want 2", len(m.workHits))
	}
	if _, ok := m.workHits["work:a"]; !ok {
		t.Fatal("missing mouse target for work:a")
	}

	// Anchor the viewport, then deliver a newer snapshot.
	m.viewport.SetYOffset(3)
	m.viewportFollow = false
	anchor := m.viewport.YOffset()

	updated, _ := m.Update(workMsg{
		snapshot: viewmodel.WorkSnapshot{ConversationID: "conv", Revision: 2, Items: []viewmodel.WorkItem{
			workTask("work:a", "Alpha"), workTask("work:c", "Gamma"),
		}},
		subscriptionID: m.workSubscriptionID,
	})
	m = updated.(Model)
	_ = m.viewString()

	if m.viewport.YOffset() != anchor {
		t.Fatalf("scroll anchor moved on refresh: %d -> %d", anchor, m.viewport.YOffset())
	}
	if _, ok := m.workHits["work:b"]; ok {
		t.Fatal("a departed card left a stale mouse target")
	}
	if _, ok := m.workHits["work:c"]; !ok {
		t.Fatal("the new card has no mouse target")
	}
}

// A stop or acknowledgement reply must release the pending entry its request
// booked, or a slow reply leaks it for the panel's whole lifetime.
func TestWorkActionReplyClearsPending(t *testing.T) {
	m := newTestModel(t)
	m.workSnapshot = workSnapOf(workTask("work:a", "Alpha"))
	m = ctrlT(m)
	p := m.tasksPanel
	if p.activeTab != tasksTabWork {
		t.Fatalf("want the Work tab, got %v", p.activeTab)
	}

	for _, kind := range []string{workReqStop, workReqAck} {
		cmd := p.request(kind, "work:a", "rev-1", viewmodel.WorkQuery{ConversationID: p.conversationID}, false)
		req := cmd().(workRequestMsg)
		if _, ok := p.pending[req.requestID]; !ok {
			t.Fatalf("the %s request was not booked", kind)
		}
		updated, _ := m.Update(workActionMsg{
			conversationID: req.conversationID,
			taskKey:        req.taskKey,
			resultRevision: req.resultRevision,
			requestID:      req.requestID,
			kind:           req.kind,
			stop:           viewmodel.WorkStopResult{TaskKey: "work:a", Accepted: true},
		})
		m = updated.(Model)
		if _, ok := m.tasksPanel.pending[req.requestID]; ok {
			t.Fatalf("the %s reply left its pending entry behind", kind)
		}
	}
}
