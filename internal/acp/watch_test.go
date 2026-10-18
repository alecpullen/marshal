package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/progress"
	"marshal/internal/pubsub"
	"marshal/internal/viewmodel"
	"marshal/internal/watch"
)

type watchNotices struct {
	mu   sync.Mutex
	list []SessionUpdateParams
}

func (n *watchNotices) notify(method string, params any) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if p, ok := params.(SessionUpdateParams); ok && method == "session/update" {
		n.list = append(n.list, p)
	}
	return nil
}

func (n *watchNotices) kinds(kind string) []SessionUpdateParams {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []SessionUpdateParams
	for _, p := range n.list {
		if p.Update["kind"] == kind {
			out = append(out, p)
		}
	}
	return out
}

func newWatchTestACP(t *testing.T) (*WatchManagerACP, *watchNotices) {
	t.Helper()
	broker := pubsub.NewBroker[watch.Event]()
	wm := watch.NewManager(context.Background(), watch.Deps{
		OnEvent: func(ev watch.Event) { broker.Publish("watch", ev) },
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = wm.Close(ctx)
		broker.Close()
	})
	notices := &watchNotices{}
	acpWatch := NewWatchManagerACP(func(id string) (*watch.Manager, *pubsub.Broker[watch.Event], bool) {
		return wm, broker, id == "s1"
	}, notices.notify)
	return acpWatch, notices
}

func TestWatchStartListStopAndUpdates(t *testing.T) {
	m, notices := newWatchTestACP(t)
	target := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, _ := json.Marshal(map[string]any{
		"sessionId": "s1",
		"spec": map[string]any{
			"name": "file-watch", "kind": "file", "path": target,
			"condition": "exit_code 0", "mode": "repeat", "intervalMs": 100,
		},
	})
	v, err := m.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	res := v.(map[string]any)
	id := res["id"].(string)
	if id == "" || res["name"] != "file-watch" {
		t.Fatalf("start result = %v", res)
	}

	listed, err := m.List(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	ws := listed.(map[string]any)["watches"].([]WatchInfoWire)
	if len(ws) != 1 || ws[0].ID != id || ws[0].Owner != "studio" || ws[0].Kind != "file" || ws[0].Samples == nil {
		t.Fatalf("watches = %+v", ws)
	}

	waitForWithin(t, 8*time.Second, "a watch update for the fired sample", func() bool {
		for _, p := range notices.kinds("watch") {
			ev := p.Update["event"].(map[string]any)
			if ev["watchId"] == id && ev["state"] == "fired" && ev["owner"] == "studio" {
				return true
			}
		}
		return false
	})
	waitForWithin(t, 8*time.Second, "samples in the list", func() bool {
		l, _ := m.List(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
		got := l.(map[string]any)["watches"].([]WatchInfoWire)
		return len(got) == 1 && len(got[0].Samples) > 0 && got[0].Samples[0].Tripped
	})

	if _, err := m.Stop(context.Background(), json.RawMessage(`{"sessionId":"s1","id":"`+id+`"}`)); err != nil {
		t.Fatal(err)
	}
	listed, _ = m.List(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if got := listed.(map[string]any)["watches"].([]WatchInfoWire); len(got) != 0 {
		t.Fatalf("watches after stop = %+v", got)
	}
}

func TestWatchStartOverLimitErrors(t *testing.T) {
	m, _ := newWatchTestACP(t)
	target := filepath.Join(t.TempDir(), "f.txt")
	_ = os.WriteFile(target, []byte("x"), 0o644)
	var lastErr error
	for i := 0; i <= watch.MaxWatches; i++ {
		spec, _ := json.Marshal(map[string]any{
			"sessionId": "s1",
			"spec":      map[string]any{"name": "w", "kind": "file", "path": target, "intervalMs": 3600000},
		})
		_, lastErr = m.Start(context.Background(), spec)
	}
	if lastErr == nil || !strings.Contains(lastErr.Error(), "watch cap reached") {
		t.Fatalf("err = %v", lastErr)
	}
	rpc, ok := lastErr.(*jsonRPCError)
	if !ok || rpc.Code != invalidParams {
		t.Fatalf("err = %#v, want invalid params", lastErr)
	}
}

func TestWatchUnknownSession(t *testing.T) {
	m, _ := newWatchTestACP(t)
	if _, err := m.List(context.Background(), json.RawMessage(`{"sessionId":"nope"}`)); err == nil {
		t.Fatal("want error")
	}
	if _, err := m.Stop(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err == nil {
		t.Fatal("want error for missing id")
	}
}

// waitForWithin is waitFor with a longer deadline: watches poll no faster
// than watch.MinInterval.
func waitForWithin(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- session/work_* ---------------------------------------------------------

// fakeWorkService is a scripted viewmodel.WorkService: it records exactly what
// the ACP layer passed and returns the configured projection or error, so a
// test can prove identity/argument fidelity without a real runtime.
type fakeWorkService struct {
	mu sync.Mutex

	snapshot viewmodel.WorkSnapshot
	page     viewmodel.WorkPage
	detail   viewmodel.WorkDetail
	stop     viewmodel.WorkStopResult

	listErr    error
	inspectErr error
	stopErr    error
	ackErr     error

	lastQuery  viewmodel.WorkQuery
	lastTarget viewmodel.WorkTarget
	lastRaw    bool
	lastStop   string
	lastAck    viewmodel.WorkAck
}

func (f *fakeWorkService) WorkSnapshot() viewmodel.WorkSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot
}

func (f *fakeWorkService) SubscribeWork(context.Context) <-chan pubsub.Event[viewmodel.WorkSnapshot] {
	ch := make(chan pubsub.Event[viewmodel.WorkSnapshot])
	close(ch)
	return ch
}

func (f *fakeWorkService) ListWork(ctx context.Context, query viewmodel.WorkQuery) (viewmodel.WorkPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastQuery = query
	return f.page, f.listErr
}

func (f *fakeWorkService) InspectWork(ctx context.Context, target viewmodel.WorkTarget, raw bool) (viewmodel.WorkDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastTarget = target
	f.lastRaw = raw
	return f.detail, f.inspectErr
}

func (f *fakeWorkService) StopWork(ctx context.Context, target viewmodel.WorkTarget, reason string) (viewmodel.WorkStopResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastTarget = target
	f.lastStop = reason
	return f.stop, f.stopErr
}

func (f *fakeWorkService) AcknowledgeWork(ctx context.Context, ack viewmodel.WorkAck) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastAck = ack
	return f.ackErr
}

func newWorkTestACP(t *testing.T) (*WorkManagerACP, *fakeWorkService) {
	t.Helper()
	svc := &fakeWorkService{}
	m := NewWorkManagerACP(func(id string) (viewmodel.WorkService, bool) {
		if id != "s1" {
			return nil, false
		}
		return svc, true
	}, func(string, any) error { return nil })
	t.Cleanup(m.Close)
	return m, svc
}

func assertInvalidParams(t *testing.T, what string, err error) {
	t.Helper()
	var rpc *jsonRPCError
	if !errors.As(err, &rpc) || rpc.Code != invalidParams {
		t.Fatalf("%s err = %#v, want invalid params (%d)", what, err, invalidParams)
	}
}

func TestWorkACPListLiveAndPagedShareRuntime(t *testing.T) {
	m, svc := newWorkTestACP(t)
	ctx := context.Background()

	item := viewmodel.WorkItem{
		TaskKey: "work:cmd:1", ConversationID: "s1", RuntimeEpoch: "e1",
		Kind: "command", Label: "build", StartedAt: time.Now(),
	}
	svc.snapshot = viewmodel.WorkSnapshot{
		Version: 1, ConversationID: "s1", RuntimeEpoch: "e1", Revision: 7,
		HistoryRevision: 3, ObservedAt: time.Now(), Items: []viewmodel.WorkItem{item}, ActiveCount: 1,
	}
	svc.page = viewmodel.WorkPage{
		RuntimeEpoch: "e1", Revision: 3, Items: []viewmodel.WorkItem{item}, NextCursor: "next",
	}

	liveAny, err := m.List(ctx, json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatalf("live list: %v", err)
	}
	live, ok := liveAny.(WorkSnapshotWire)
	if !ok {
		t.Fatalf("live result = %T, want WorkSnapshotWire", liveAny)
	}
	if live.ConversationID != "s1" || live.RuntimeEpoch != "e1" || live.Revision != 7 || live.HistoryRevision != 3 {
		t.Fatalf("live snapshot = %+v", live)
	}
	if len(live.Items) != 1 || live.Items[0].TaskKey != item.TaskKey {
		t.Fatalf("live items = %+v", live.Items)
	}

	// The live view rejects history arguments instead of ignoring them.
	assertInvalidParams(t, "live with query",
		func() error { _, e := m.List(ctx, json.RawMessage(`{"sessionId":"s1","query":"x"}`)); return e }())
	assertInvalidParams(t, "live with cursor",
		func() error { _, e := m.List(ctx, json.RawMessage(`{"sessionId":"s1","cursor":"c"}`)); return e }())
	assertInvalidParams(t, "live with limit",
		func() error { _, e := m.List(ctx, json.RawMessage(`{"sessionId":"s1","limit":5}`)); return e }())

	pagedAny, err := m.List(ctx, json.RawMessage(`{"sessionId":"s1","view":"attention","limit":2}`))
	if err != nil {
		t.Fatalf("paged list: %v", err)
	}
	paged, ok := pagedAny.(WorkPageWire)
	if !ok {
		t.Fatalf("paged result = %T, want WorkPageWire", pagedAny)
	}
	if paged.RuntimeEpoch != "e1" || paged.Revision != 3 || paged.NextCursor != "next" || len(paged.Items) != 1 {
		t.Fatalf("paged page = %+v", paged)
	}
	if svc.lastQuery.ConversationID != "s1" || svc.lastQuery.View != "attention" || svc.lastQuery.Limit != 2 {
		t.Fatalf("facade query = %+v", svc.lastQuery)
	}
	if live.RuntimeEpoch != paged.RuntimeEpoch {
		t.Fatalf("live and paged epochs differ: %q vs %q", live.RuntimeEpoch, paged.RuntimeEpoch)
	}
}

func TestWorkACPMethodsRejectMissingStaleAndForeignTargets(t *testing.T) {
	m, svc := newWorkTestACP(t)
	ctx := context.Background()

	// A missing session id is invalid params.
	assertInvalidParams(t, "missing sessionId", func() error {
		_, e := m.List(ctx, json.RawMessage(`{}`))
		return e
	}())

	// An unknown session is a server error, exactly like the watch manager.
	_, err := m.List(ctx, json.RawMessage(`{"sessionId":"other"}`))
	var rpc *jsonRPCError
	if !errors.As(err, &rpc) || rpc.Code != serverError {
		t.Fatalf("unknown session err = %#v, want server error (%d)", err, serverError)
	}

	// Unknown view and out-of-range limit are explicit invalid params.
	assertInvalidParams(t, "unknown view", func() error {
		_, e := m.List(ctx, json.RawMessage(`{"sessionId":"s1","view":"bogus"}`))
		return e
	}())
	assertInvalidParams(t, "bad limit", func() error {
		_, e := m.List(ctx, json.RawMessage(`{"sessionId":"s1","view":"history","limit":999}`))
		return e
	}())

	// A stale cursor from the facade surfaces as invalid params, never success.
	svc.listErr = viewmodel.ErrWorkStaleCursor
	assertInvalidParams(t, "stale cursor", func() error {
		_, e := m.List(ctx, json.RawMessage(`{"sessionId":"s1","view":"history","cursor":"zzz"}`))
		return e
	}())
	svc.listErr = nil

	// A missing or foreign task key is rejected before the facade is called.
	assertInvalidParams(t, "inspect without taskKey", func() error {
		_, e := m.Inspect(ctx, json.RawMessage(`{"sessionId":"s1"}`))
		return e
	}())
	assertInvalidParams(t, "stop without taskKey", func() error {
		_, e := m.Stop(ctx, json.RawMessage(`{"sessionId":"s1"}`))
		return e
	}())
	assertInvalidParams(t, "ack without revision", func() error {
		_, e := m.Acknowledge(ctx, json.RawMessage(`{"sessionId":"s1","taskKey":"work:x"}`))
		return e
	}())

	// A target the facade no longer knows is invalid params.
	svc.inspectErr = viewmodel.ErrWorkStaleTarget
	assertInvalidParams(t, "foreign target", func() error {
		_, e := m.Inspect(ctx, json.RawMessage(`{"sessionId":"s1","taskKey":"work:foreign"}`))
		return e
	}())
	svc.inspectErr = nil

	// An unavailable action is a server error.
	svc.ackErr = viewmodel.ErrWorkUnsupported
	_, err = m.Acknowledge(ctx, json.RawMessage(`{"sessionId":"s1","taskKey":"work:x","terminalResultRevision":"r1"}`))
	if !errors.As(err, &rpc) || rpc.Code != serverError {
		t.Fatalf("unsupported ack err = %#v, want server error (%d)", err, serverError)
	}
}

func TestWorkACPInspectStopAckPreserveOutcome(t *testing.T) {
	m, svc := newWorkTestACP(t)
	ctx := context.Background()

	svc.detail = viewmodel.WorkDetail{
		Item:   viewmodel.WorkItem{TaskKey: "work:cmd:1", ConversationID: "s1", State: "completed", Outcome: "exit_0", ResultRevision: "r1"},
		Output: "all good", Raw: true, Report: "report body",
	}
	detailAny, err := m.Inspect(ctx, json.RawMessage(`{"sessionId":"s1","taskKey":"work:cmd:1","raw":true}`))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	detail, ok := detailAny.(WorkDetailWire)
	if !ok {
		t.Fatalf("inspect result = %T, want WorkDetailWire", detailAny)
	}
	if detail.Item.TaskKey != "work:cmd:1" || detail.Output != "all good" || !detail.Raw || detail.Report != "report body" {
		t.Fatalf("detail = %+v", detail)
	}
	if !svc.lastRaw || svc.lastTarget.TaskKey != "work:cmd:1" || svc.lastTarget.ConversationID != "s1" {
		t.Fatalf("facade inspect args = %+v raw=%v", svc.lastTarget, svc.lastRaw)
	}

	svc.stop = viewmodel.WorkStopResult{TaskKey: "work:cmd:1", State: "stopped", Summary: "stopping", Accepted: true}
	stopAny, err := m.Stop(ctx, json.RawMessage(`{"sessionId":"s1","taskKey":"work:cmd:1","reason":"operator"}`))
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	stop, ok := stopAny.(WorkStopWire)
	if !ok {
		t.Fatalf("stop result = %T, want WorkStopWire", stopAny)
	}
	if stop.TaskKey != "work:cmd:1" || stop.State != "stopped" || stop.Summary != "stopping" || !stop.Accepted {
		t.Fatalf("stop = %+v", stop)
	}
	if svc.lastStop != "operator" || svc.lastTarget.TaskKey != "work:cmd:1" || svc.lastTarget.ConversationID != "s1" {
		t.Fatalf("facade stop args = %+v reason=%q", svc.lastTarget, svc.lastStop)
	}

	// The reason is bounded at 1,024 runes; exactly the bound is accepted.
	overLong := strings.Repeat("x", 1025)
	assertInvalidParams(t, "over-long stop reason", func() error {
		_, e := m.Stop(ctx, json.RawMessage(`{"sessionId":"s1","taskKey":"work:cmd:1","reason":"`+overLong+`"}`))
		return e
	}())
	atBound := strings.Repeat("x", 1024)
	if _, err := m.Stop(ctx, json.RawMessage(`{"sessionId":"s1","taskKey":"work:cmd:1","reason":"`+atBound+`"}`)); err != nil {
		t.Fatalf("1024-rune reason must be accepted: %v", err)
	}

	ackAny, err := m.Acknowledge(ctx, json.RawMessage(`{"sessionId":"s1","taskKey":"work:cmd:1","terminalResultRevision":"r1"}`))
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	res, ok := ackAny.(map[string]any)
	if !ok || res["acknowledged"] != true {
		t.Fatalf("ack result = %#v, want {acknowledged:true}", ackAny)
	}
	if svc.lastAck.TaskKey != "work:cmd:1" || svc.lastAck.ResultRevision != "r1" || svc.lastAck.ConversationID != "s1" {
		t.Fatalf("facade ack = %+v", svc.lastAck)
	}
}

func TestWorkACPWireRoundTripKeepsProgressAndRevision(t *testing.T) {
	total, rate := 250.0, 3.5
	exit := 2
	item := viewmodel.WorkItem{
		TaskKey: "work:cmd:1", DisplayID: "job-1", ConversationID: "s1", RuntimeEpoch: "e1",
		SourceID: "job-1", SourceRevision: 4, Kind: "command", Label: "build", Owner: "studio",
		Origin: viewmodel.WorkOrigin{TurnID: "t1", StepID: "st1", ToolCallID: "tc1"},
		State:  "completed", Outcome: "exit_2", ExitCode: &exit, Attachment: "foreground",
		StartedAt: time.Unix(100, 0).UTC(), EndedAt: time.Unix(200, 0).UTC(),
		ResultRevision: "r9", Activity: "running tests", OutputTail: "tail", Warning: "warn", Attention: "failed",
		Reference:      viewmodel.WorkReference{MessageID: 42, NodeKey: "n1", ChildSessionID: "child"},
		Prompt:         &viewmodel.WorkPrompt{Kind: "permission", RequestID: "pr1", AgentID: 3},
		Capabilities:   viewmodel.WorkCapabilities{Inspect: true, Stop: true, Acknowledge: true},
		ProgressConfig: &progress.Config{Profile: "tests", Source: "go-test", Title: "Tests"},
		Progress: &progress.Snapshot{
			Frame: progress.Frame{
				Version: 1, Sequence: 5,
				Phase:           &progress.Phase{ID: "p1", Label: "run", Index: 2, Total: 3},
				PrimaryMetricID: "m1",
				Metrics:         []progress.Metric{{ID: "m1", Label: "cases", Unit: "count", Current: 12, Total: &total, Rate: &rate}},
				Rows:            []progress.Row{{ID: "r1", Label: "case", State: "passed"}},
				Detail:          "so far",
			},
			ObservedAt: time.Unix(300, 0).UTC(), MeasuredAt: time.Unix(299, 0).UTC(),
			Partial: true, Warning: "slow", ReportedError: true,
		},
	}
	snapshot := viewmodel.WorkSnapshot{
		Version: 1, ConversationID: "s1", RuntimeEpoch: "e1", Revision: 11, HistoryRevision: 6,
		ObservedAt: time.Unix(400, 0).UTC(), Items: []viewmodel.WorkItem{item},
		ActiveCount: 1, AttentionCount: 1, SuccessReceiptCount: 2,
	}

	wire, err := workWire[WorkSnapshotWire](snapshot)
	if err != nil {
		t.Fatalf("workWire snapshot: %v", err)
	}
	if wire.Revision != 11 || wire.HistoryRevision != 6 || len(wire.Items) != 1 {
		t.Fatalf("wire snapshot = %+v", wire)
	}
	got := wire.Items[0]
	if got.ResultRevision != "r9" || got.Progress == nil || got.Progress.Sequence != 5 {
		t.Fatalf("wire item/progress = %+v %+v", got, got.Progress)
	}
	if got.Progress.Phase == nil || got.Progress.Phase.Label != "run" || got.Progress.PrimaryMetricID != "m1" {
		t.Fatalf("wire phase = %+v", got.Progress.Phase)
	}
	if got.ProgressConfig == nil || got.ProgressConfig.Profile != "tests" || got.ProgressConfig.Source != "go-test" {
		t.Fatalf("wire progress config = %+v", got.ProgressConfig)
	}
	if len(got.Progress.Metrics) != 1 || got.Progress.Metrics[0].Total == nil || *got.Progress.Metrics[0].Total != 250 {
		t.Fatalf("wire metric = %+v", got.Progress.Metrics)
	}
	if !got.Progress.Partial || !got.Progress.ReportedError || got.Progress.Warning != "slow" {
		t.Fatalf("wire progress flags = %+v", got.Progress)
	}
	// Nested pointers are copied, not aliased into the wire DTO.
	if got.ExitCode == nil || *got.ExitCode != 2 || got.ExitCode == item.ExitCode {
		t.Fatal("exit code was not deep-copied into the wire DTO")
	}
	if got.Prompt == nil || got.Prompt.RequestID != "pr1" {
		t.Fatalf("wire prompt = %+v", got.Prompt)
	}
	if got.Origin.ToolCallID != "tc1" || got.Reference.ChildSessionID != "child" || !got.Capabilities.Acknowledge {
		t.Fatalf("wire origin/reference/capabilities = %+v %+v %+v", got.Origin, got.Reference, got.Capabilities)
	}

	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	for _, key := range []string{"version", "conversationId", "runtimeEpoch", "revision", "historyRevision", "observedAt", "items", "activeCount", "attentionCount", "successReceiptCount", "stoppedReceiptCount"} {
		if _, ok := top[key]; !ok {
			t.Errorf("snapshot JSON missing %q", key)
		}
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(top["items"], &items); err != nil {
		t.Fatalf("unmarshal items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	for _, key := range []string{"taskKey", "displayId", "conversationId", "runtimeEpoch", "sourceId", "sourceRevision", "kind", "label", "origin", "state", "exitCode", "attachment", "startedAt", "endedAt", "terminalResultRevision", "reference", "prompt", "capabilities", "progressConfig", "progress"} {
		if _, ok := items[0][key]; !ok {
			t.Errorf("item JSON missing %q", key)
		}
	}
	var prog map[string]json.RawMessage
	if err := json.Unmarshal(items[0]["progress"], &prog); err != nil {
		t.Fatalf("unmarshal progress: %v", err)
	}
	for _, key := range []string{"version", "sequence", "phase", "primaryMetricId", "metrics", "rows", "detail", "observedAt", "measuredAt", "partial", "warning", "reportedError"} {
		if _, ok := prog[key]; !ok {
			t.Errorf("progress JSON missing %q", key)
		}
	}
	// Times cross as RFC3339 strings, never as structs.
	var started string
	if err := json.Unmarshal(items[0]["startedAt"], &started); err != nil {
		t.Fatalf("startedAt is not a string: %v", err)
	}
	if started == "" || started[:4] != "1970" {
		t.Fatalf("startedAt = %q, want an RFC3339 timestamp", started)
	}
}

// --- work_state forwarders --------------------------------------------------

// scriptedWorkService is a WorkService whose live stream a test drives: it
// counts every SubscribeWork, publishes the snapshots the test hands it and
// reports whatever runtime epoch the test has bound. Only the stream matters
// here; the request methods are inert.
type scriptedWorkService struct {
	mu       sync.Mutex
	subs     int
	epoch    string
	snapshot viewmodel.WorkSnapshot
	broker   *pubsub.Broker[viewmodel.WorkSnapshot]
}

func newScriptedWorkService(epoch string) *scriptedWorkService {
	return &scriptedWorkService{epoch: epoch, broker: pubsub.NewBroker[viewmodel.WorkSnapshot]()}
}

func (s *scriptedWorkService) WorkSnapshot() viewmodel.WorkSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.snapshot
	snap.RuntimeEpoch = s.epoch
	return snap
}

func (s *scriptedWorkService) SubscribeWork(ctx context.Context) <-chan pubsub.Event[viewmodel.WorkSnapshot] {
	s.mu.Lock()
	s.subs++
	s.mu.Unlock()
	return s.broker.Subscribe(ctx)
}

func (s *scriptedWorkService) ListWork(context.Context, viewmodel.WorkQuery) (viewmodel.WorkPage, error) {
	return viewmodel.WorkPage{}, nil
}

func (s *scriptedWorkService) InspectWork(context.Context, viewmodel.WorkTarget, bool) (viewmodel.WorkDetail, error) {
	return viewmodel.WorkDetail{}, nil
}

func (s *scriptedWorkService) StopWork(context.Context, viewmodel.WorkTarget, string) (viewmodel.WorkStopResult, error) {
	return viewmodel.WorkStopResult{}, nil
}

func (s *scriptedWorkService) AcknowledgeWork(context.Context, viewmodel.WorkAck) error { return nil }

func (s *scriptedWorkService) subscriptions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subs
}

func (s *scriptedWorkService) setSnapshot(snap viewmodel.WorkSnapshot) {
	s.mu.Lock()
	s.snapshot = snap
	s.mu.Unlock()
}

func (s *scriptedWorkService) publish(snap viewmodel.WorkSnapshot) {
	s.broker.Publish("work", snap)
}

// workNotices records every work_state update decoded from the notify sink.
// Decoding the raw params (not the originating Go value) is deliberate: a
// revision is a uint64 on the wire, so it can only decode cleanly if the
// envelope never routed the snapshot through a map[string]any, which would
// have turned it into a float64.
type workNotices struct {
	mu        sync.Mutex
	snapshots []WorkSnapshotWire
}

func (n *workNotices) notify(method string, params any) error {
	if method != "session/update" {
		return nil
	}
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	var envelope struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.SessionID == "" {
		return nil
	}
	var tag struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(envelope.Update, &tag); err != nil || tag.Kind != "work_state" {
		return nil
	}
	var snap WorkSnapshotWire
	if err := json.Unmarshal(envelope.Update, &snap); err != nil {
		return err
	}
	n.mu.Lock()
	n.snapshots = append(n.snapshots, snap)
	n.mu.Unlock()
	return nil
}

func (n *workNotices) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.snapshots)
}

func (n *workNotices) all() []WorkSnapshotWire {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]WorkSnapshotWire(nil), n.snapshots...)
}

func newScriptedWorkACP(t *testing.T, svc *scriptedWorkService, notices *workNotices) *WorkManagerACP {
	t.Helper()
	m := NewWorkManagerACP(func(id string) (viewmodel.WorkService, bool) {
		if id != "s1" {
			return nil, false
		}
		return svc, true
	}, notices.notify)
	t.Cleanup(m.Close)
	t.Cleanup(svc.broker.Close)
	return m
}

func TestWorkACPForwarderSingleSubscriptionAndRecovery(t *testing.T) {
	svc := newScriptedWorkService("e1")
	notices := &workNotices{}
	m := newScriptedWorkACP(t, svc, notices)

	// The first valid List/Inspect opts the session into the stream and starts
	// exactly one subscription; every later request reuses it.
	requests := []func() error{
		func() error { _, e := m.List(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); return e },
		func() error {
			_, e := m.List(context.Background(), json.RawMessage(`{"sessionId":"s1","view":"attention"}`))
			return e
		},
		func() error {
			_, e := m.Inspect(context.Background(), json.RawMessage(`{"sessionId":"s1","taskKey":"work:cmd:1"}`))
			return e
		},
	}
	for i, call := range requests {
		if err := call(); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if got := svc.subscriptions(); got != 1 {
		t.Fatalf("SubscribeWork calls = %d, want exactly 1", got)
	}

	// The stream carries the exact snapshot, revision included (a uint64 the
	// wire decode above would have rejected had it travelled as a float).
	svc.publish(viewmodel.WorkSnapshot{
		ConversationID: "s1", RuntimeEpoch: "e1", Revision: 42, HistoryRevision: 7,
		Items: []viewmodel.WorkItem{{TaskKey: "work:cmd:1", ConversationID: "s1", RuntimeEpoch: "e1", Kind: "command"}},
	})
	waitFor(t, "the forwarded work_state", func() bool { return notices.count() == 1 })
	if got := notices.all(); len(got) != 1 || got[0].Revision != 42 || got[0].HistoryRevision != 7 || got[0].RuntimeEpoch != "e1" {
		t.Fatalf("forwarded snapshot = %+v", got)
	}

	// Reconciliation through work_list is the recovery path: the live view
	// returns the newest snapshot, and reading it does not open a second stream.
	svc.setSnapshot(viewmodel.WorkSnapshot{ConversationID: "s1", RuntimeEpoch: "e1", Revision: 99})
	live, err := m.List(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := live.(WorkSnapshotWire).Revision; got != 99 {
		t.Fatalf("recovered revision = %d, want 99", got)
	}
	if got := svc.subscriptions(); got != 1 {
		t.Fatalf("recovery opened a second subscription: %d", got)
	}
}

// switchWorkLookup is a session lookup a test can rebind, standing in for a
// runtime that replaced the coordinator behind a session.
type switchWorkLookup struct {
	mu sync.Mutex
	m  map[string]viewmodel.WorkService
}

func (l *switchWorkLookup) lookup(id string) (viewmodel.WorkService, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.m[id]
	return s, ok
}

func (l *switchWorkLookup) set(id string, s viewmodel.WorkService) {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]viewmodel.WorkService{}
	}
	l.m[id] = s
	l.mu.Unlock()
}

func TestWorkACPEpochReplacementRejectsLateUpdate(t *testing.T) {
	// The session starts on one coordinator epoch; the runtime later replaces
	// it and the old broker closes. The forwarder retires on that close,
	// rebinds to the new epoch, and a straggler from the old epoch is dropped.
	old := newScriptedWorkService("e1")
	next := newScriptedWorkService("e2")
	lookup := &switchWorkLookup{}
	lookup.set("s1", old)
	notices := &workNotices{}
	m := NewWorkManagerACP(lookup.lookup, notices.notify)
	t.Cleanup(m.Close)
	t.Cleanup(old.broker.Close)
	t.Cleanup(next.broker.Close)

	if _, err := m.List(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	old.publish(viewmodel.WorkSnapshot{ConversationID: "s1", RuntimeEpoch: "e1", Revision: 1})
	waitFor(t, "the first-epoch update", func() bool { return notices.count() == 1 })

	lookup.set("s1", next)
	old.broker.Close()
	waitFor(t, "the rebound subscription", func() bool { return next.subscriptions() == 1 })

	// A straggler still carrying the retired epoch must not be delivered; the
	// next delivery on the bound epoch is.
	next.publish(viewmodel.WorkSnapshot{ConversationID: "s1", RuntimeEpoch: "e1", Revision: 2})
	next.publish(viewmodel.WorkSnapshot{ConversationID: "s1", RuntimeEpoch: "e2", Revision: 3})
	waitFor(t, "the post-rebind e2 update", func() bool { return notices.count() == 2 })

	got := notices.all()
	if got[0].RuntimeEpoch != "e1" || got[0].Revision != 1 {
		t.Fatalf("first delivery = %+v, want the e1 rev 1", got[0])
	}
	if got[1].RuntimeEpoch != "e2" || got[1].Revision != 3 {
		t.Fatalf("second delivery = %+v, want the e2 rev 3 (straggler dropped)", got[1])
	}
	for _, s := range got {
		if s.RuntimeEpoch == "e1" && s.Revision == 2 {
			t.Fatal("a late update from the retired epoch was delivered")
		}
	}
}
