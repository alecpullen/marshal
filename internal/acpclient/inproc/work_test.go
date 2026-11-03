package inproc

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/pubsub"
	"marshal/internal/viewmodel"
)

// fakeWork is a viewmodel.WorkService for the adapter's tests. It records each
// call, and publish sends to every open subscription.
type fakeWork struct {
	mu      sync.Mutex
	snap    viewmodel.WorkSnapshot
	subs    int
	open    []chan pubsub.Event[viewmodel.WorkSnapshot]
	queries []viewmodel.WorkQuery
	page    viewmodel.WorkPage
	pageErr error
	inspect []fakeInspect
	detail  viewmodel.WorkDetail
	stops   []fakeStop
	stop    viewmodel.WorkStopResult
	stopErr error
}

type fakeInspect struct {
	target viewmodel.WorkTarget
	raw    bool
}

type fakeStop struct {
	target viewmodel.WorkTarget
	reason string
}

func (f *fakeWork) WorkSnapshot() viewmodel.WorkSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

// setSnapshot replaces the snapshot a later seed reads.
func (f *fakeWork) setSnapshot(snap viewmodel.WorkSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap = snap
}

// SubscribeWork closes the channel when the context ends.
func (f *fakeWork) SubscribeWork(ctx context.Context) <-chan pubsub.Event[viewmodel.WorkSnapshot] {
	f.mu.Lock()
	f.subs++
	ch := make(chan pubsub.Event[viewmodel.WorkSnapshot], 16)
	f.open = append(f.open, ch)
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		for i, c := range f.open {
			if c == ch {
				f.open = append(f.open[:i], f.open[i+1:]...)
				break
			}
		}
		close(ch)
		f.mu.Unlock()
	}()
	return ch
}

// publish delivers a snapshot on the newest open subscription without blocking.
// It holds the mutex across the send so it can never race a cancel's close.
func (f *fakeWork) publish(snap viewmodel.WorkSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.open) == 0 {
		return
	}
	ch := f.open[len(f.open)-1]
	select {
	case ch <- pubsub.Event[viewmodel.WorkSnapshot]{Type: "work", Payload: snap}:
	default:
	}
}

func (f *fakeWork) ListWork(_ context.Context, q viewmodel.WorkQuery) (viewmodel.WorkPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	return f.page, f.pageErr
}

func (f *fakeWork) InspectWork(_ context.Context, target viewmodel.WorkTarget, raw bool) (viewmodel.WorkDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspect = append(f.inspect, fakeInspect{target: target, raw: raw})
	return f.detail, nil
}

func (f *fakeWork) StopWork(_ context.Context, target viewmodel.WorkTarget, reason string) (viewmodel.WorkStopResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, fakeStop{target: target, reason: reason})
	return f.stop, f.stopErr
}

func (f *fakeWork) AcknowledgeWork(context.Context, viewmodel.WorkAck) error { return nil }

// setPageErr replaces the error the next ListWork returns.
func (f *fakeWork) setPageErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageErr = err
}

// subscriptionCount is how many times the adapter subscribed.
func (f *fakeWork) subscriptionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subs
}

// lastQuery is the WorkQuery the adapter last passed to ListWork.
func (f *fakeWork) lastQuery(t *testing.T) viewmodel.WorkQuery {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) == 0 {
		t.Fatalf("ListWork was not called")
	}
	return f.queries[len(f.queries)-1]
}

// lastInspect is the target and raw flag the adapter last passed to
// InspectWork.
func (f *fakeWork) lastInspect(t *testing.T) fakeInspect {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inspect) == 0 {
		t.Fatalf("InspectWork was not called")
	}
	return f.inspect[len(f.inspect)-1]
}

// lastStop is the target and reason the adapter last passed to StopWork.
func (f *fakeWork) lastStop(t *testing.T) fakeStop {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stops) == 0 {
		t.Fatalf("StopWork was not called")
	}
	return f.stops[len(f.stops)-1]
}

// wantNoWorkSignal fails if Changed fires within the window a snapshot the
// adapter should have dropped would surface in.
func wantNoWorkSignal(t *testing.T, sess *Session) {
	t.Helper()
	select {
	case <-sess.Changed():
		t.Fatal("Changed fired for a snapshot the session should have dropped")
	case <-time.After(200 * time.Millisecond):
	}
}

// waitWorkSignal fails unless Changed fires within 5 s.
func waitWorkSignal(t *testing.T, sess *Session) {
	t.Helper()
	select {
	case _, ok := <-sess.Changed():
		if !ok {
			t.Fatalf("Changed closed, want a signal")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Changed did not fire within 5s")
	}
}

// TestWorkReadsTheSeed pins the snapshot the session keeps when it subscribes:
// the service's current one, already on the wire.
func TestWorkReadsTheSeed(t *testing.T) {
	f := &fakeWork{snap: viewmodel.WorkSnapshot{
		ConversationID: "s1",
		Revision:       1,
		Items: []viewmodel.WorkItem{{
			TaskKey: "work:1",
			State:   "running",
			Label:   "go test ./...",
		}},
	}}
	_, sess := New(Deps{State: newState(t, "s1"), Work: f})

	got, ok := sess.Work()
	if !ok {
		t.Fatalf("Work reports no work, want the seed")
	}
	if want := viewmodel.WorkSnapshotOf(f.WorkSnapshot()); !reflect.DeepEqual(got, want) {
		t.Fatalf("Work =\n%+v\nwant\n%+v", got, want)
	}
	if n := f.subscriptionCount(); n != 1 {
		t.Errorf("subscriptions = %d, want 1", n)
	}
}

// TestWorkWithoutAServiceReportsNone pins that a session whose runtime supplied
// no work service reports no work, and fails every work call.
func TestWorkWithoutAServiceReportsNone(t *testing.T) {
	_, sess := New(Deps{State: newState(t, "s1")})

	got, ok := sess.Work()
	if ok {
		t.Errorf("Work reports work %+v, want none", got)
	}
	if !reflect.DeepEqual(got, acpwire.WorkSnapshot{}) {
		t.Errorf("Work snapshot = %+v, want the zero snapshot", got)
	}

	ctx := context.Background()
	if _, err := sess.ListWork(ctx, acpwire.WorkQuery{View: "history"}); err == nil {
		t.Errorf("ListWork err = nil, want a failure")
	}
	if _, err := sess.InspectWork(ctx, "work:1", false); err == nil {
		t.Errorf("InspectWork err = nil, want a failure")
	}
	if _, err := sess.StopWork(ctx, "work:1", "requested"); err == nil {
		t.Errorf("StopWork err = nil, want a failure")
	}
}

// TestWorkKeepsANewerSnapshotAndSignals pins that a published snapshot replaces
// the kept one and wakes the reader.
func TestWorkKeepsANewerSnapshotAndSignals(t *testing.T) {
	f := &fakeWork{snap: viewmodel.WorkSnapshot{ConversationID: "s1", Revision: 1}}
	_, sess := New(Deps{State: newState(t, "s1"), Work: f})

	f.publish(viewmodel.WorkSnapshot{ConversationID: "s1", Revision: 2})
	waitWorkSignal(t, sess)

	got, ok := sess.Work()
	if !ok {
		t.Fatalf("Work reports no work, want the published snapshot")
	}
	if got.Revision != 2 {
		t.Fatalf("Work revision = %d, want 2", got.Revision)
	}
}

// TestWorkDropsOlderAndForeignSnapshots pins that a snapshot older than the kept
// one, and one naming another conversation, are never kept and never signal.
func TestWorkDropsOlderAndForeignSnapshots(t *testing.T) {
	f := &fakeWork{snap: viewmodel.WorkSnapshot{ConversationID: "s1", Revision: 3}}
	_, sess := New(Deps{State: newState(t, "s1"), Work: f})

	f.publish(viewmodel.WorkSnapshot{ConversationID: "s1", Revision: 2})
	wantNoWorkSignal(t, sess)
	f.publish(viewmodel.WorkSnapshot{ConversationID: "other", Revision: 9})
	wantNoWorkSignal(t, sess)

	got, ok := sess.Work()
	if !ok {
		t.Fatalf("Work reports no work, want the seed")
	}
	if got.ConversationID != "s1" || got.Revision != 3 {
		t.Fatalf("Work = conversation %q revision %d, want the seed's s1 and 3", got.ConversationID, got.Revision)
	}

	f.publish(viewmodel.WorkSnapshot{ConversationID: "s1", Revision: 4})
	waitWorkSignal(t, sess)

	got, ok = sess.Work()
	if !ok {
		t.Fatalf("Work reports no work, want the kept snapshot")
	}
	if got.ConversationID != "s1" || got.Revision != 4 {
		t.Fatalf("Work = conversation %q revision %d, want s1 and 4", got.ConversationID, got.Revision)
	}
}

// TestASwapSubscribesTheNewSessionAfresh pins that the replacement session seeds
// from the service and subscribes again, while the replaced session keeps the
// snapshot it held.
func TestASwapSubscribesTheNewSessionAfresh(t *testing.T) {
	f := &fakeWork{snap: viewmodel.WorkSnapshot{ConversationID: "s1", Revision: 1}}
	h, old := New(Deps{State: newState(t, "s1"), Work: f})

	f.setSnapshot(viewmodel.WorkSnapshot{ConversationID: "s2", Revision: 1})
	fresh := h.swap(newState(t, "s2"))

	got, ok := fresh.Work()
	if !ok {
		t.Fatalf("the new session reports no work")
	}
	if got.ConversationID != "s2" {
		t.Errorf("new session conversation = %q, want s2", got.ConversationID)
	}
	if n := f.subscriptionCount(); n != 2 {
		t.Errorf("subscriptions = %d, want 2", n)
	}
	if kept, ok := old.Work(); !ok || kept.ConversationID != "s1" {
		t.Errorf("old session Work = %+v/%v, want its s1 snapshot", kept, ok)
	}
}

// TestListWorkNamesTheSessionsConversation pins that a page query carries the
// session's own conversation and the caller's view, query, cursor and limit.
func TestListWorkNamesTheSessionsConversation(t *testing.T) {
	f := &fakeWork{page: viewmodel.WorkPage{
		Revision: 2,
		Items:    []viewmodel.WorkItem{{TaskKey: "work:1", State: "completed"}},
	}}
	_, sess := New(Deps{State: newState(t, "s1"), Work: f})

	page, err := sess.ListWork(context.Background(), acpwire.WorkQuery{
		View:   "history",
		Query:  "go",
		Cursor: "c",
		Limit:  50,
	})
	if err != nil {
		t.Fatalf("ListWork = %v, want nil", err)
	}
	wantQuery := viewmodel.WorkQuery{
		ConversationID: "s1",
		View:           "history",
		Query:          "go",
		Cursor:         "c",
		Limit:          50,
	}
	if got := f.lastQuery(t); got != wantQuery {
		t.Errorf("ListWork query = %+v, want %+v", got, wantQuery)
	}
	if want := viewmodel.WorkPageOf(f.page); !reflect.DeepEqual(page, want) {
		t.Errorf("ListWork page =\n%+v\nwant\n%+v", page, want)
	}
}

// TestAStaleCursorIsCoded pins that a stale work cursor crosses the seam as
// CodeStaleWorkCursor, and that any other failure comes back unchanged.
func TestAStaleCursorIsCoded(t *testing.T) {
	f := &fakeWork{pageErr: viewmodel.ErrWorkStaleCursor}
	_, sess := New(Deps{State: newState(t, "s1"), Work: f})

	ctx := context.Background()
	if _, err := sess.ListWork(ctx, acpwire.WorkQuery{View: "history"}); !acpclient.IsCode(err, acpwire.CodeStaleWorkCursor) {
		t.Fatalf("stale cursor err = %v, want CodeStaleWorkCursor", err)
	}

	boom := errors.New("boom")
	f.setPageErr(boom)
	if _, err := sess.ListWork(ctx, acpwire.WorkQuery{View: "history"}); !errors.Is(err, boom) {
		t.Fatalf("other err = %v, want the service's error unchanged", err)
	}
}

// TestInspectAndStopNameTheirTarget pins that both target calls name the
// session's conversation and the caller's task key, and that a refused stop
// keeps its summary alongside the error.
func TestInspectAndStopNameTheirTarget(t *testing.T) {
	f := &fakeWork{
		detail:  viewmodel.WorkDetail{Output: "the output", Raw: true, Report: "the report"},
		stop:    viewmodel.WorkStopResult{TaskKey: "work:1", Summary: "gone", State: "running"},
		stopErr: viewmodel.ErrWorkStaleTarget,
	}
	_, sess := New(Deps{State: newState(t, "s1"), Work: f})
	ctx := context.Background()

	detail, err := sess.InspectWork(ctx, "work:1", true)
	if err != nil {
		t.Fatalf("InspectWork = %v, want nil", err)
	}
	wantInspect := fakeInspect{target: viewmodel.WorkTarget{ConversationID: "s1", TaskKey: "work:1"}, raw: true}
	if got := f.lastInspect(t); got != wantInspect {
		t.Errorf("InspectWork call = %+v, want %+v", got, wantInspect)
	}
	if want := viewmodel.WorkDetailOf(f.detail); !reflect.DeepEqual(detail, want) {
		t.Errorf("InspectWork detail =\n%+v\nwant\n%+v", detail, want)
	}

	result, err := sess.StopWork(ctx, "work:1", "requested")
	wantStop := fakeStop{target: viewmodel.WorkTarget{ConversationID: "s1", TaskKey: "work:1"}, reason: "requested"}
	if got := f.lastStop(t); got != wantStop {
		t.Errorf("StopWork call = %+v, want %+v", got, wantStop)
	}
	if result.Summary != "gone" {
		t.Errorf("StopWork summary = %q, want gone", result.Summary)
	}
	if !errors.Is(err, viewmodel.ErrWorkStaleTarget) {
		t.Errorf("StopWork err = %v, want ErrWorkStaleTarget", err)
	}
}

// TestAClosedSessionServesNoWorkCalls pins that the work calls a swap replaced
// answer with CodeSessionClosed.
func TestAClosedSessionServesNoWorkCalls(t *testing.T) {
	f := &fakeWork{}
	h, old := New(Deps{State: newState(t, "s1"), Work: f})
	h.swap(newState(t, "s2"))

	ctx := context.Background()
	_, listErr := old.ListWork(ctx, acpwire.WorkQuery{View: "history"})
	_, inspectErr := old.InspectWork(ctx, "work:1", false)
	_, stopErr := old.StopWork(ctx, "work:1", "requested")
	for name, err := range map[string]error{
		"ListWork":    listErr,
		"InspectWork": inspectErr,
		"StopWork":    stopErr,
	} {
		if !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
			t.Errorf("%s err = %v, want CodeSessionClosed", name, err)
		}
	}
}
