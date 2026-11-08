package rpc

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpwire"
)

// workFrame builds the session/update notification carrying one work_snapshot
// update.
func workFrame(t *testing.T, sessionID string, state acpwire.WorkState) string {
	t.Helper()
	return updateFrame(t, sessionID, acpwire.WorkUpdate{Kind: acpwire.UpdateWork, Work: state})
}

// workAt is a work state the tests recognize by its snapshot's revision.
func workAt(revision uint64) acpwire.WorkState {
	return acpwire.WorkState{
		Exists: true,
		Snapshot: acpwire.WorkSnapshot{
			Version:        1,
			ConversationID: "s1",
			RuntimeEpoch:   "e1",
			Revision:       revision,
		},
	}
}

// workAnswer is one Work read's outcome.
type workAnswer struct {
	snapshot acpwire.WorkSnapshot
	exists   bool
}

// readWork starts one Work read on its own goroutine, so the test can answer
// its call.
func readWork(session *Session) <-chan workAnswer {
	out := make(chan workAnswer, 1)
	go func() {
		snapshot, exists := session.Work()
		out <- workAnswer{snapshot: snapshot, exists: exists}
	}()
	return out
}

// awaitWork waits for one Work read's outcome.
func awaitWork(t *testing.T, out <-chan workAnswer) workAnswer {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("Work did not return")
		return workAnswer{}
	}
}

// nextWorkList reads the client's next frame and checks it is session/work_list
// for session, asking for view.
func nextWorkList(t *testing.T, a *scriptedAgent, session *Session, view string) fakeRequest {
	t.Helper()
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodWorkList {
		t.Fatalf("the client sent %q, want %q", got, acpwire.MethodWorkList)
	}
	var params acpwire.WorkListParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode work_list params: %v", err)
	}
	if params.SessionID != session.ID() || params.View != view {
		t.Fatalf("work_list params = %+v, want session %q and view %q", params, session.ID(), view)
	}
	return req
}

// firstWork performs the mirror's first read, answering session/work_list with
// state, and returns what that read reported.
func firstWork(t *testing.T, session *Session, a *scriptedAgent, state acpwire.WorkState) workAnswer {
	t.Helper()
	out := readWork(session)
	req := nextWorkList(t, a, session, "")
	a.answer(t, req, state)
	return awaitWork(t, out)
}

// TestWorkFetchesOnItsFirstRead pins the two halves of the read: the first one
// asks the agent for the live view, and every later one answers from the
// mirror. A failed read leaves nothing behind, so the read after it asks again.
func TestWorkFetchesOnItsFirstRead(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	got := firstWork(t, session, a, workAt(7))
	if !got.exists || got.snapshot.Revision != 7 {
		t.Fatalf("the first read = revision %d, %v; want revision 7 and exists", got.snapshot.Revision, got.exists)
	}

	snapshot, exists := session.Work()
	if !exists || snapshot.Revision != 7 {
		t.Fatalf("the second read = revision %d, %v; want revision 7 and exists", snapshot.Revision, exists)
	}
	select {
	case req := <-a.reqs:
		t.Fatalf("the second read sent %q", req.Method)
	case <-time.After(250 * time.Millisecond):
	}

	// A session whose first read failed asks again on the read after it.
	other := attachScripted(t, host, a, "s2", acpwire.SessionStatus{WorkingDir: "B"})
	failed := readWork(other)
	req := nextWorkList(t, a, other, "")
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeSessionClosed,
		Message: "the session is gone",
	}))
	if failedRead := awaitWork(t, failed); failedRead.exists || failedRead.snapshot.Revision != 0 {
		t.Fatalf("a failed read = revision %d, %v; want a zero snapshot and no work",
			failedRead.snapshot.Revision, failedRead.exists)
	}
	if retried := firstWork(t, other, a, workAt(3)); !retried.exists || retried.snapshot.Revision != 3 {
		t.Fatalf("the read after the failure = revision %d, %v; want revision 3 and exists",
			retried.snapshot.Revision, retried.exists)
	}
}

// TestWorkInstallsTheUpdateThatPrecedesTheResponse pins the barrier: the agent
// answers session/work_list with the state it had, so an update that reaches
// the client before the response is newer and must be kept.
func TestWorkInstallsTheUpdateThatPrecedesTheResponse(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	out := readWork(session)
	req := nextWorkList(t, a, session, "")
	a.send(t, workFrame(t, "s1", workAt(5)))
	a.answer(t, req, workAt(3))

	if got := awaitWork(t, out); got.snapshot.Revision != 5 {
		t.Fatalf("the read's revision = %d, want 5: the update that preceded the response is newer", got.snapshot.Revision)
	}
}

func TestWorkInstallsTheResultWhenNoUpdateCame(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	if got := firstWork(t, session, a, workAt(3)); got.snapshot.Revision != 3 {
		t.Fatalf("the read's revision = %d, want 3", got.snapshot.Revision)
	}
	if snapshot, exists := session.Work(); !exists || snapshot.Revision != 3 {
		t.Fatalf("the mirror = revision %d, %v; want revision 3 and exists", snapshot.Revision, exists)
	}
}

// TestAWorkUpdateReplacesTheStateAndFiresChanged pins the work stream: an
// update replaces the mirrored state and taps Changed.
func TestAWorkUpdateReplacesTheStateAndFiresChanged(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	firstWork(t, session, a, workAt(1))
	changed := session.Changed()
	drainChanged(t, changed)

	a.send(t, workFrame(t, "s1", workAt(2)))
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("Changed did not fire for the update")
	}
	snapshot, exists := session.Work()
	if !exists || snapshot.Revision != 2 {
		t.Fatalf("the mirror = revision %d, %v; want revision 2 and exists", snapshot.Revision, exists)
	}
}

// TestTheWorkCallsSendTheirMethods pins the three work calls: each sends its
// method with the session's ID and its params, and returns its result.
func TestTheWorkCallsSendTheirMethods(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	query := acpwire.WorkQuery{View: "history", Query: "go test", Cursor: "c1", Limit: 5}
	list := startCall(func() (acpwire.WorkPage, error) {
		return session.ListWork(context.Background(), query)
	})
	req := nextMethod(t, a, acpwire.MethodWorkList)
	var listParams acpwire.WorkListParams
	if err := json.Unmarshal(req.Params, &listParams); err != nil {
		t.Fatalf("decode work_list params: %v", err)
	}
	wantList := acpwire.WorkListParams{
		SessionID: "s1",
		View:      query.View,
		Query:     query.Query,
		Cursor:    query.Cursor,
		Limit:     query.Limit,
	}
	if listParams != wantList {
		t.Fatalf("work_list params = %+v, want %+v", listParams, wantList)
	}
	page := acpwire.WorkPage{
		RuntimeEpoch: "e1",
		Revision:     4,
		Items: []acpwire.WorkItem{{
			TaskKey: "work:1",
			Kind:    "command",
			Label:   "go test ./...",
			State:   "succeeded",
		}},
		NextCursor: "c2",
	}
	a.answer(t, req, page)
	listed := awaitValue(t, list)
	if listed.err != nil {
		t.Fatalf("ListWork: %v", listed.err)
	}
	if !reflect.DeepEqual(listed.value, page) {
		t.Fatalf("ListWork = %+v, want %+v", listed.value, page)
	}

	detail := acpwire.WorkDetail{
		Item:   acpwire.WorkItem{TaskKey: "work:1", Kind: "command", State: "succeeded"},
		Output: "PASS",
		Raw:    true,
	}
	inspect := startCall(func() (acpwire.WorkDetail, error) {
		return session.InspectWork(context.Background(), "work:1", true)
	})
	req = nextMethod(t, a, acpwire.MethodWorkInspect)
	var inspectParams acpwire.WorkTargetParams
	if err := json.Unmarshal(req.Params, &inspectParams); err != nil {
		t.Fatalf("decode work_inspect params: %v", err)
	}
	wantInspect := acpwire.WorkTargetParams{SessionID: "s1", TaskKey: "work:1", Raw: true}
	if inspectParams != wantInspect {
		t.Fatalf("work_inspect params = %+v, want %+v", inspectParams, wantInspect)
	}
	a.answer(t, req, detail)
	inspected := awaitValue(t, inspect)
	if inspected.err != nil {
		t.Fatalf("InspectWork: %v", inspected.err)
	}
	if !reflect.DeepEqual(inspected.value, detail) {
		t.Fatalf("InspectWork = %+v, want %+v", inspected.value, detail)
	}

	stop := startCall(func() (acpwire.WorkStopResult, error) {
		return session.StopWork(context.Background(), "work:1", "no longer needed")
	})
	req = nextMethod(t, a, acpwire.MethodWorkStop)
	var stopParams acpwire.WorkTargetParams
	if err := json.Unmarshal(req.Params, &stopParams); err != nil {
		t.Fatalf("decode work_stop params: %v", err)
	}
	wantStop := acpwire.WorkTargetParams{SessionID: "s1", TaskKey: "work:1", Reason: "no longer needed"}
	if stopParams != wantStop {
		t.Fatalf("work_stop params = %+v, want %+v", stopParams, wantStop)
	}
	stopped := acpwire.WorkStopResult{
		TaskKey:  "work:1",
		State:    "stopping",
		Summary:  "asked the command to stop",
		Accepted: true,
	}
	a.answer(t, req, stopped)
	stopAnswer := awaitValue(t, stop)
	if stopAnswer.err != nil {
		t.Fatalf("StopWork: %v", stopAnswer.err)
	}
	if stopAnswer.value != stopped {
		t.Fatalf("StopWork = %+v, want %+v", stopAnswer.value, stopped)
	}
}
