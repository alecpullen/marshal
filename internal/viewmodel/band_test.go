package viewmodel

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/progress"
)

func TestBandProgressNextAndStale(t *testing.T) {
	items := []session.TranscriptItem{userMsg(1, 0), todoWrite(1, "w", 1, `{"todos":[]}`)}
	var steps []session.Step
	for i := 1; i <= 9; i++ {
		steps = append(steps, stepOf(session.StepID(i), 1, i*40, i*40+5))
		items = append(items, narration(int64(10+i), session.StepID(i), i*40, "Still on it."))
	}
	for i := range steps {
		steps[i].TodoID = "b"
	}
	s := Snapshot{Items: items, Steps: steps, Busy: true, Now: at(400),
		Todos: todos(db.TodoItem{ID: "a", Content: "A", Status: "completed"}, db.TodoItem{ID: "b", Content: "B", Status: "in_progress"},
			db.TodoItem{ID: "c", Content: "C", Status: "pending"}, db.TodoItem{ID: "d", Content: "D", Status: "pending"})}
	b := BandOf(s, Build(s))
	if !b.Todos || b.Done != 1 || b.Total != 4 || b.Active != "B" || len(b.Next) != 2 || b.Next[0] != "C" {
		t.Fatalf("band = %+v", b)
	}
	if !b.Stale {
		t.Fatalf("399s and 9 responses since the write must be stale: %+v", b)
	}
}

func TestBandEntitiesAreRunningCards(t *testing.T) {
	sub := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 3, Label: "Task 1", Status: session.SubagentRunning, StartedAt: at(2)}}
	s := Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Go."), sub, jobStart(1, "j", 3, "job-4")},
		Steps: []session.Step{stepOf(1, 1, 1, 0)}, Busy: true, Now: at(10)}
	b := BandOf(s, Build(s))
	labels := map[string]bool{}
	for _, e := range b.Entities {
		labels[e.Label] = true
	}
	if len(b.Entities) != 2 || !labels["#3 Task 1"] || !labels["job-4"] {
		t.Fatalf("entities = %+v", b.Entities)
	}
}

// The runner nudges on its own response count, whatever its role. The band
// must count the same responses: the turn opener's, not only the general
// role's, and not a different actor's sharing the session.
func TestTodoClockCountsTheTurnOpenersResponses(t *testing.T) {
	var steps []session.Step
	for i := 1; i <= 4; i++ {
		st := stepOf(session.StepID(i), 1, i*10, i*10+5)
		st.Actor = session.Actor{Role: "architect", Label: "architect"}
		steps = append(steps, st)
	}
	other := stepOf(5, 1, 60, 65)
	other.Actor = session.Actor{Role: "implementer", Label: "implementer"}
	steps = append(steps, other)
	c := TodoClockOf(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0)}, Steps: steps})
	if c.Responses != 4 {
		t.Fatalf("responses = %d, want the 4 by the turn's opener", c.Responses)
	}
}

// An unfinished list is pinned only while a turn runs, in the band itself, so
// the web client hides it when idle exactly as the TUI does.
func TestBandHidesTodosWhenIdle(t *testing.T) {
	s := Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), todoWrite(1, "w", 1, `{"todos":[]}`), narration(2, 1, 1, "On A.")},
		Steps: []session.Step{stepOf(1, 1, 1, 2)}, Now: at(10),
		Todos: todos(db.TodoItem{ID: "a", Content: "A", Status: "in_progress"}, db.TodoItem{ID: "b", Content: "B", Status: "pending"})}
	steps := s.Steps
	steps[0].TodoID = "a"
	if b := BandOf(s, Build(s)); b.Todos {
		t.Fatalf("idle band shows todos: %+v", b)
	}
	s.Busy = true
	b := BandOf(s, Build(s))
	if !b.Todos {
		t.Fatalf("busy band hides todos: %+v", b)
	}
	if want := at(1).Add(session.TodoStaleAfter); !b.StaleAt.Equal(want) {
		t.Fatalf("StaleAt = %v, want %v (the write plus the stale age)", b.StaleAt, want)
	}
}

// The Tasks views show the whole list with each item's share of the
// transcript, idle included: the list outlives the band's own gating, so an
// unfinished one is still there to read once the turn ends.
func TestBandTasksCarryTheWholeListWhenIdle(t *testing.T) {
	s := Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), todoWrite(1, "w", 1, `{"todos":[]}`), narration(2, 1, 1, "On A.")},
		Steps: []session.Step{stepOf(1, 1, 1, 4)}, Now: at(10),
		Todos: todos(db.TodoItem{ID: "a", Content: "A\x07", Status: "in_progress", StartedAt: at(1)}, db.TodoItem{ID: "b", Content: "B", Status: "pending"})}
	s.Steps[0].TodoID = "a"
	turns := Build(s)
	var beat NodeID
	for _, n := range turns[len(turns)-1].Children {
		if n.Kind == KindStep {
			beat = n.ID
		}
	}
	b := BandOf(s, turns)
	if b.Todos || len(b.Tasks) != 2 {
		t.Fatalf("idle band: todos=%v tasks=%+v, want the list without the pinned rows", b.Todos, b.Tasks)
	}
	a := b.Tasks[0]
	if a.Content != "A" || a.Status != "in_progress" || !a.StartedAt.Equal(at(1)) {
		t.Fatalf("task a = %+v, want scrubbed content and its status and start", a)
	}
	if a.Steps != 1 || a.Work != at(4).Sub(at(1)) || beat == (NodeID{}) || a.First != beat {
		t.Fatalf("task a stat = %+v, want the one beat bound to it", a.TaskStat)
	}
	if b.Tasks[1].Steps != 0 || b.Tasks[1].First != (NodeID{}) {
		t.Fatalf("task b = %+v, want no beats yet", b.Tasks[1])
	}
	w := ProjectBand(b).Band
	if len(w.Tasks) != 2 || w.Tasks[0].Ref != a.First.Key || w.Tasks[0].WorkMs != 3000 || w.Tasks[1].Ref != "" {
		t.Fatalf("wire tasks = %+v", w.Tasks)
	}
}

func workItem(key, state string) WorkItem {
	return WorkItem{TaskKey: key, Kind: "command", Label: key, State: state, StartedAt: at(1)}
}

func TestWorkIdentitySeparatesConversationEpochAndSource(t *testing.T) {
	base := WorkKey("c1", "e1", "command", "s1")
	if base != WorkKey("c1", "e1", "command", "s1") {
		t.Fatal("WorkKey is not stable for identical inputs")
	}
	variants := map[string]string{
		"conversation": WorkKey("c2", "e1", "command", "s1"),
		"epoch":        WorkKey("c1", "e2", "command", "s1"),
		"kind":         WorkKey("c1", "e1", "watch", "s1"),
		"source":       WorkKey("c1", "e1", "command", "s2"),
	}
	for name, got := range variants {
		if got == base {
			t.Fatalf("%s variant collided with the base key %q", name, base)
		}
	}
}

func TestWorkCloneOwnsNestedValues(t *testing.T) {
	exit := 3
	item := WorkItem{
		TaskKey:        "work:1",
		State:          "completed",
		ExitCode:       &exit,
		Prompt:         &WorkPrompt{Kind: "ask", RequestID: "r1"},
		ProgressConfig: &progress.Config{Profile: "tests", Source: "go-test-json", Title: "T"},
		Progress: &progress.Snapshot{
			Frame: progress.Frame{
				Metrics: []progress.Metric{{ID: "passed", Label: "Passed", Unit: "tests", Current: 1}},
				Rows:    []progress.Row{{ID: "t", Label: "Test", State: "done"}},
			},
		},
	}
	clone := CloneWorkItem(item)
	*clone.ExitCode = 9
	clone.Prompt.RequestID = "r2"
	clone.ProgressConfig.Title = "changed"
	clone.Progress.Metrics[0].Current = 42
	clone.Progress.Rows[0].Label = "changed"

	if *item.ExitCode != 3 || item.Prompt.RequestID != "r1" || item.ProgressConfig.Title != "T" ||
		item.Progress.Metrics[0].Current != 1 || item.Progress.Rows[0].Label != "Test" {
		t.Fatalf("clone mutation leaked into the source: %+v", item)
	}
	if clone.Progress == item.Progress {
		t.Fatal("clone shares the progress pointer with the source")
	}

	snap := WorkSnapshot{Version: 1, Items: []WorkItem{item}}
	sc := CloneWorkSnapshot(snap)
	sc.Items[0].Label = "changed"
	if snap.Items[0].Label != item.Label {
		t.Fatalf("snapshot clone mutation leaked: %+v", snap.Items[0])
	}
}

func TestWorkReceiptsDoNotReplayOrHideFailure(t *testing.T) {
	end := time.Unix(10_100, 0)
	success := workItem("work:ok", "completed")
	success.EndedAt = end
	if ok, stopped := WorkReceipts(success, end.Add(4*time.Second)); !ok || stopped {
		t.Fatalf("fresh success: success=%v stopped=%v, want true/false", ok, stopped)
	}
	if ok, stopped := WorkReceipts(success, end.Add(5*time.Second)); ok || stopped {
		t.Fatalf("replayed success at the window edge: success=%v stopped=%v", ok, stopped)
	}
	if ok, stopped := WorkReceipts(success, end.Add(time.Hour)); ok || stopped {
		t.Fatalf("replayed an old success: success=%v stopped=%v", ok, stopped)
	}

	// The same instant expressed in another time zone is the same deadline.
	zoned := success
	zoned.EndedAt = end.In(time.FixedZone("E", -5*3600))
	if ok, _ := WorkReceipts(zoned, end.Add(time.Second)); !ok {
		t.Fatal("time-zone equivalent EndedAt did not produce a receipt")
	}

	// No end time can never restart a receipt.
	missing := workItem("work:none", "completed")
	if ok, stopped := WorkReceipts(missing, end); ok || stopped {
		t.Fatalf("missing EndedAt produced a receipt: %v/%v", ok, stopped)
	}

	// A failure is attention, never a receipt.
	failed := workItem("work:bad", "failed")
	failed.Outcome = "startup_failure"
	failed.EndedAt = end
	if ok, stopped := WorkReceipts(failed, end.Add(time.Second)); ok || stopped {
		t.Fatalf("failure produced a receipt: %v/%v", ok, stopped)
	}

	// Running work has not settled.
	running := workItem("work:run", "running")
	running.EndedAt = end
	if ok, stopped := WorkReceipts(running, end.Add(time.Second)); ok || stopped {
		t.Fatalf("running work produced a receipt: %v/%v", ok, stopped)
	}

	// An explicit stop without another error is a stopped receipt, not a success.
	stoppedItem := workItem("work:stop", "stopped")
	stoppedItem.Outcome = "stopped"
	stoppedItem.EndedAt = end
	if ok, stopped := WorkReceipts(stoppedItem, end.Add(time.Second)); ok || !stopped {
		t.Fatalf("explicit stop: success=%v stopped=%v, want false/true", ok, stopped)
	}

	// A watch that ends on an expected shutdown is stopped, not a failure.
	watchShutdown := workItem("work:watch", "stopped")
	watchShutdown.Kind = "watch"
	watchShutdown.Outcome = "shutdown"
	watchShutdown.EndedAt = end
	if WorkNeedsAttention(watchShutdown) {
		t.Fatal("an expected watch shutdown became attention")
	}
	if ok, stopped := WorkReceipts(watchShutdown, end.Add(time.Second)); ok || !stopped {
		t.Fatalf("watch shutdown: success=%v stopped=%v, want false/true", ok, stopped)
	}
}

func TestWorkSemanticErrorPreservesActualExit(t *testing.T) {
	zero := 0
	item := workItem("work:tests", "completed")
	item.ExitCode = &zero
	item.Progress = &progress.Snapshot{ReportedError: true}
	if !WorkNeedsAttention(item) {
		t.Fatal("a terminal semantic error must need attention")
	}
	if item.State != "completed" || *item.ExitCode != 0 {
		t.Fatalf("semantic error rewrote the process state/exit: %+v", item)
	}
	clean := item
	clean.Progress = &progress.Snapshot{}
	if WorkNeedsAttention(clean) {
		t.Fatal("a clean completed result must not need attention")
	}

	// A running row with an error is not acknowledgeable yet.
	running := item
	running.State = "running"
	if WorkNeedsAttention(running) {
		t.Fatal("a running error row must not need attention")
	}

	// The revision is computed from the actual exit, not the progress flag.
	nonzero := 2
	other := item
	other.ExitCode = &nonzero
	if WorkResultRevision(item) == WorkResultRevision(other) {
		t.Fatal("a different exit code must change the result revision")
	}
	if WorkResultRevision(item) != WorkResultRevision(clean) {
		t.Fatal("semantic progress changed the result revision")
	}
}

func TestWorkActiveExcludesForegroundCommands(t *testing.T) {
	for _, state := range []string{"running", "stopping", "watching", "blocked"} {
		if !WorkActive(workItem("work:x", state)) {
			t.Fatalf("state %q must be active", state)
		}
	}
	fg := workItem("work:fg", "running")
	fg.Attachment = "foreground"
	if WorkActive(fg) {
		t.Fatal("a foreground-attached command must not be pinned")
	}
	for _, state := range []string{"completed", "failed", "stopped", "unknown"} {
		if WorkActive(workItem("work:x", state)) {
			t.Fatalf("terminal state %q must not be active", state)
		}
	}
}

func TestWorkPagingBoundsSearchAndStaleCursor(t *testing.T) {
	items := []WorkItem{
		{TaskKey: "work:a", Kind: "command", Label: "Build the docs", Owner: "architect", State: "completed", StartedAt: at(30)},
		{TaskKey: "work:b", Kind: "watch", Label: "Watch disk", State: "watching", StartedAt: at(20)},
		{TaskKey: "work:c", Kind: "command", Label: "Run tests", Owner: "implementer", State: "failed", StartedAt: at(10)},
	}
	// View, query and limit are validated before any cursor work.
	if _, err := PageWork(items, "e1", 1, WorkQuery{View: "live"}); err == nil {
		t.Fatal("the live view must not be pageable")
	}
	if _, err := PageWork(items, "e1", 1, WorkQuery{View: "history", Query: strings.Repeat("x", 201)}); err == nil {
		t.Fatal("an over-long query must be rejected")
	}
	if _, err := PageWork(items, "e1", 1, WorkQuery{View: "history", Limit: 101}); err == nil {
		t.Fatal("a limit above the maximum must be rejected")
	}
	if _, err := PageWork(items, "e1", 1, WorkQuery{View: "history", Limit: -1}); err == nil {
		t.Fatal("a negative limit must be rejected")
	}

	// History is newest-start first; page size two leaves a cursor.
	page, err := PageWork(items, "e1", 7, WorkQuery{View: "history", Limit: 2})
	if err != nil {
		t.Fatalf("PageWork: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].TaskKey != "work:a" || page.Items[1].TaskKey != "work:b" || page.NextCursor == "" {
		t.Fatalf("history page = %+v", page)
	}
	if page.RuntimeEpoch != "e1" || page.Revision != 7 {
		t.Fatalf("page epoch/revision = %q/%d", page.RuntimeEpoch, page.Revision)
	}

	// Following the cursor returns the rest without skipping or duplicating.
	next, err := PageWork(items, "e1", 7, WorkQuery{View: "history", Limit: 2, Cursor: page.NextCursor})
	if err != nil {
		t.Fatalf("cursor page: %v", err)
	}
	if len(next.Items) != 1 || next.Items[0].TaskKey != "work:c" || next.NextCursor != "" {
		t.Fatalf("cursor did not resume: %+v", next)
	}

	// The query matches safe label/kind/owner, case-folded.
	if p, err := PageWork(items, "e1", 7, WorkQuery{View: "history", Query: "ARCHITECT"}); err != nil || len(p.Items) != 1 || p.Items[0].TaskKey != "work:a" {
		t.Fatalf("label search = %+v err=%v", p, err)
	}
	if p, err := PageWork(items, "e1", 7, WorkQuery{View: "history", Query: "watch"}); err != nil || len(p.Items) != 1 || p.Items[0].TaskKey != "work:b" {
		t.Fatalf("kind search = %+v err=%v", p, err)
	}
	if p, err := PageWork(items, "e1", 7, WorkQuery{View: "history", Query: "implementer"}); err != nil || len(p.Items) != 1 || p.Items[0].TaskKey != "work:c" {
		t.Fatalf("owner search = %+v err=%v", p, err)
	}

	// Attention selects only results needing attention, in the same order.
	att, err := PageWork(items, "e1", 7, WorkQuery{View: "attention"})
	if err != nil || len(att.Items) != 1 || att.Items[0].TaskKey != "work:c" {
		t.Fatalf("attention page = %+v err=%v", att, err)
	}

	// A stale cursor is rejected, never silently reinterpreted.
	if _, err := PageWork(items, "e1", 8, WorkQuery{View: "history", Limit: 1, Cursor: page.NextCursor}); err != ErrWorkStaleCursor {
		t.Fatalf("revision-mismatched cursor err = %v, want ErrWorkStaleCursor", err)
	}
	if _, err := PageWork(items, "e2", 7, WorkQuery{View: "history", Limit: 1, Cursor: page.NextCursor}); err != ErrWorkStaleCursor {
		t.Fatalf("epoch-mismatched cursor err = %v, want ErrWorkStaleCursor", err)
	}
	if _, err := PageWork(items, "e1", 7, WorkQuery{View: "history", Limit: 1, Query: "other", Cursor: page.NextCursor}); err != ErrWorkStaleCursor {
		t.Fatalf("query-mismatched cursor err = %v, want ErrWorkStaleCursor", err)
	}
	overlong := base64.RawURLEncoding.EncodeToString(make([]byte, workCursorMaxBytes+1))
	if _, err := PageWork(items, "e1", 7, WorkQuery{View: "history", Cursor: overlong}); err != ErrWorkStaleCursor {
		t.Fatalf("oversize cursor err = %v, want ErrWorkStaleCursor", err)
	}
	raw, _ := json.Marshal(workCursor{Epoch: "e1", Revision: 7, View: "history", Query: "", Offset: -2})
	if _, err := PageWork(items, "e1", 7, WorkQuery{View: "history", Cursor: base64.RawURLEncoding.EncodeToString(raw)}); err != ErrWorkStaleCursor {
		t.Fatalf("negative offset err = %v, want ErrWorkStaleCursor", err)
	}
	raw, _ = json.Marshal(workCursor{Epoch: "e1", Revision: 7, View: "history", Query: "", Offset: 99})
	if _, err := PageWork(items, "e1", 7, WorkQuery{View: "history", Cursor: base64.RawURLEncoding.EncodeToString(raw)}); err != ErrWorkStaleCursor {
		t.Fatalf("out-of-range offset err = %v, want ErrWorkStaleCursor", err)
	}

	// The query must be valid UTF-8.
	if _, err := PageWork(items, "e1", 7, WorkQuery{View: "history", Query: string([]byte{0xff, 0xfe})}); err == nil {
		t.Fatal("invalid UTF-8 query must be rejected")
	}
}

func TestWorkPagingStableAcrossProgressOnlyUpdates(t *testing.T) {
	equalA := WorkItem{TaskKey: "work:a", Kind: "command", Label: "A", State: "completed", StartedAt: at(20)}
	equalB := WorkItem{TaskKey: "work:b", Kind: "command", Label: "B", State: "completed", StartedAt: at(20)}
	newest := WorkItem{TaskKey: "work:c", Kind: "command", Label: "C", State: "completed", StartedAt: at(30)}
	items := []WorkItem{equalA, newest, equalB}

	first, err := PageWork(items, "e1", 3, WorkQuery{View: "history", Limit: 2})
	if err != nil {
		t.Fatalf("PageWork: %v", err)
	}
	// Newest start first, then TaskKey breaks the tie.
	got := []string{first.Items[0].TaskKey, first.Items[1].TaskKey}
	if got[0] != "work:c" || got[1] != "work:a" || first.NextCursor == "" {
		t.Fatalf("order = %v, next=%q", got, first.NextCursor)
	}

	// Only progress and observation change; the same revision keeps the cursor
	// valid and the page order identical.
	for i := range items {
		items[i].Activity = "working"
		items[i].Progress = &progress.Snapshot{Frame: progress.Frame{Detail: "later"}}
	}
	second, err := PageWork(items, "e1", 3, WorkQuery{View: "history", Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("cursor rejected after a progress-only update: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].TaskKey != "work:b" {
		t.Fatalf("progress-only update changed the page: %+v", second.Items)
	}

	// The returned items are copies: mutating one cannot touch the input.
	second.Items[0].Label = "changed"
	if items[2].Label == "changed" {
		t.Fatal("PageWork returned an aliased item")
	}
}
