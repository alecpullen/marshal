package rpc

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"marshal/internal/acpwire"
)

// startStack starts one Stack read on its own goroutine.
func startStack(session *Session, subagentID int64, busy bool) <-chan acpwire.StackSnapshot {
	out := make(chan acpwire.StackSnapshot, 1)
	go func() { out <- session.Stack(subagentID, busy) }()
	return out
}

// awaitSnapshot waits for one Stack read's outcome.
func awaitSnapshot(t *testing.T, out <-chan acpwire.StackSnapshot) acpwire.StackSnapshot {
	t.Helper()
	select {
	case snap := <-out:
		return snap
	case <-time.After(5 * time.Second):
		t.Fatal("Stack did not return")
		return acpwire.StackSnapshot{}
	}
}

// answerBusy answers the session/stack_busy the client sent next.
func answerBusy(t *testing.T, a *scriptedAgent, sessionID string, busy bool) {
	t.Helper()
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodStackBusy {
		t.Fatalf("the client's next frame = %q, want %q", got, acpwire.MethodStackBusy)
	}
	var params acpwire.StackBusyParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode stack_busy params: %v", err)
	}
	if params.SessionID != sessionID || params.Busy != busy {
		t.Fatalf("stack_busy params = %+v, want session %q and busy %t", params, sessionID, busy)
	}
	a.answer(t, req, struct{}{})
}

// answerStack answers the session/stack the client sent next with snap.
func answerStack(t *testing.T, a *scriptedAgent, sessionID string, subagentID int64, snap acpwire.StackSnapshot) {
	t.Helper()
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodStack {
		t.Fatalf("the client's next frame = %q, want %q", got, acpwire.MethodStack)
	}
	var params acpwire.StackParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode stack params: %v", err)
	}
	if params.SessionID != sessionID || params.SubagentID != subagentID {
		t.Fatalf("stack params = %+v, want session %q and subagent %d", params, sessionID, subagentID)
	}
	a.answer(t, req, snap)
}

// startSheet starts one Sheet read on its own goroutine.
func startSheet(session *Session, subagentID int64) <-chan acpwire.SessionSheet {
	out := make(chan acpwire.SessionSheet, 1)
	go func() { out <- session.Sheet(subagentID) }()
	return out
}

// awaitSheet waits for one Sheet read's outcome.
func awaitSheet(t *testing.T, out <-chan acpwire.SessionSheet) acpwire.SessionSheet {
	t.Helper()
	select {
	case sheet := <-out:
		return sheet
	case <-time.After(5 * time.Second):
		t.Fatal("Sheet did not return")
		return acpwire.SessionSheet{}
	}
}

// sheetFrame builds the session/update notification carrying one sheet update.
func sheetFrame(t *testing.T, sessionID string, update acpwire.SheetUpdate) string {
	t.Helper()
	update.Kind = acpwire.UpdateSheet
	return updateFrame(t, sessionID, update)
}

// sheet is a SessionSheet told apart by its StartedAt.
func sheet(subagentID, startedAt int64) acpwire.SessionSheet {
	return acpwire.SessionSheet{SubagentID: subagentID, StartedAt: startedAt}
}

// TestStackSendsTheBusyFlagBeforeItsFirstRead pins the order of a first read:
// the TUI's busy flag goes out, then the fetch.
func TestStackSendsTheBusyFlagBeforeItsFirstRead(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	reads := startStack(session, 0, true)
	answerBusy(t, a, "s1", true)
	answerStack(t, a, "s1", 0, turnSnapshot("s1", 0, 3, "step:1"))

	checkSnapshot(t, awaitSnapshot(t, reads), 3, []string{"turn:1"}, []string{"turn:1", "step:1"})
}

// TestStackSendsTheBusyFlagOnlyWhenItChanges pins that the flag is sent once
// per change, and that a read of a stack the mirror holds sends nothing.
func TestStackSendsTheBusyFlagOnlyWhenItChanges(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	first := startStack(session, 0, true)
	answerBusy(t, a, "s1", true)
	answerStack(t, a, "s1", 0, turnSnapshot("s1", 0, 3, "step:1"))
	awaitSnapshot(t, first)

	// The same flag is not sent again, and the mirror answers without a fetch.
	second := startStack(session, 0, true)
	checkSnapshot(t, awaitSnapshot(t, second), 3, []string{"turn:1"}, []string{"turn:1", "step:1"})
	pingScripted(t, host, a)

	// A flag that differs goes out, then the mirror answers again.
	third := startStack(session, 0, false)
	answerBusy(t, a, "s1", false)
	checkSnapshot(t, awaitSnapshot(t, third), 3, []string{"turn:1"}, []string{"turn:1", "step:1"})
	pingScripted(t, host, a)
}

// TestARefusedStackReadIsEmpty pins decision 9: a subagent ID the agent refuses
// reads as an empty snapshot carrying that ID, and the next read asks again.
func TestARefusedStackReadIsEmpty(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	reads := startStack(session, 9, false)
	answerBusy(t, a, "s1", false)
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodStack {
		t.Fatalf("the client's next frame = %q, want %q", got, acpwire.MethodStack)
	}
	a.send(t, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"error":{"code":-32602,"message":"no such subagent"}}`)

	snap := awaitSnapshot(t, reads)
	if snap.SubagentID != 9 {
		t.Fatalf("refused read's SubagentID = %d, want 9", snap.SubagentID)
	}
	if snap.Rev != 0 || len(snap.Nodes) != 0 || len(snap.Roots) != 0 {
		t.Fatalf("refused read = %+v, want an empty snapshot", snap)
	}

	// No entry was kept, so the next read sends session/stack again; the busy
	// flag did not change, so nothing precedes it.
	reads = startStack(session, 9, false)
	answerStack(t, a, "s1", 9, turnSnapshot("s1", 9, 2, "step:1"))
	checkSnapshot(t, awaitSnapshot(t, reads), 2, []string{"turn:1"}, []string{"turn:1", "step:1"})
}

// TestAStackPatchFiresChanged pins that a patch the mirror applies taps the
// session's Changed, and that the next read shows it.
func TestAStackPatchFiresChanged(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	reads := startStack(session, 0, true)
	answerBusy(t, a, "s1", true)
	answerStack(t, a, "s1", 0, turnSnapshot("s1", 0, 1, "step:1"))
	awaitSnapshot(t, reads)

	changed := session.Changed()
	drainChanged(t, changed)

	a.send(t, patchFrame(t, "s1", acpwire.StackPatch{
		Rev:     2,
		BaseRev: 1,
		Roots:   []string{"turn:1"},
		Upsert: []acpwire.WireNode{
			{ID: "turn:1", Kind: acpwire.KindTurn, Children: []string{"step:1", "step:2"}},
			{ID: "step:2", Kind: acpwire.KindStep, Parent: "turn:1"},
		},
	}))
	wantChanged(t, changed)

	next := startStack(session, 0, true)
	checkSnapshot(t, awaitSnapshot(t, next), 2, []string{"turn:1"},
		[]string{"turn:1", "step:1", "step:2"})
	// The read came from the mirror, so the client's next frame is the ping's.
	pingScripted(t, host, a)
}

// TestSheetInstallsTheUpdateThatPrecedesTheResponse pins the sheet's barrier:
// an update that reaches the client before the response is newer, so the read
// answers with it.
func TestSheetInstallsTheUpdateThatPrecedesTheResponse(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	out := startSheet(session, 0)
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodSheet {
		t.Fatalf("Sheet sent %q, want %q", got, acpwire.MethodSheet)
	}
	var params acpwire.SheetParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode sheet params: %v", err)
	}
	if params.SessionID != "s1" || params.SubagentID != 0 || params.Refresh != nil {
		t.Fatalf("sheet params = %+v, want session s1, subagent 0 and no refresh", params)
	}

	a.send(t, sheetFrame(t, "s1", acpwire.SheetUpdate{Sheet: sheet(0, 1)}))
	a.answer(t, req, sheet(0, 2))

	if got := awaitSheet(t, out); got.StartedAt != 1 {
		t.Fatalf("Sheet().StartedAt = %d, want 1: the update that preceded the response is newer", got.StartedAt)
	}
}

// TestSheetInstallsTheResultWhenNoUpdateCame pins that a first read installs
// the response, and that the next read is served from the mirror.
func TestSheetInstallsTheResultWhenNoUpdateCame(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	out := startSheet(session, 0)
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodSheet {
		t.Fatalf("Sheet sent %q, want %q", got, acpwire.MethodSheet)
	}
	a.answer(t, req, sheet(0, 2))

	if got := awaitSheet(t, out); got.StartedAt != 2 {
		t.Fatalf("Sheet().StartedAt = %d, want 2", got.StartedAt)
	}
	if got := session.Sheet(0); got.StartedAt != 2 {
		t.Fatalf("the mirrored Sheet().StartedAt = %d, want 2", got.StartedAt)
	}
	// The second read is served from the mirror, so nothing went out.
	pingScripted(t, host, a)
}

// TestASheetUpdateReplacesTheSheetAndFiresChanged pins that an update for a
// sheet the session read replaces it and taps Changed.
func TestASheetUpdateReplacesTheSheetAndFiresChanged(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	out := startSheet(session, 0)
	req := a.next(t)
	a.answer(t, req, sheet(0, 1))
	awaitSheet(t, out)

	changed := session.Changed()
	drainChanged(t, changed)

	a.send(t, sheetFrame(t, "s1", acpwire.SheetUpdate{Sheet: sheet(0, 7)}))
	wantChanged(t, changed)

	if got := session.Sheet(0); got.StartedAt != 7 {
		t.Fatalf("Sheet().StartedAt = %d, want 7", got.StartedAt)
	}
	pingScripted(t, host, a)
}

// TestASheetUpdateForAnUnreadSheetIsDropped pins that an update for an ID the
// session has not read changes nothing, so the first read still fetches.
func TestASheetUpdateForAnUnreadSheetIsDropped(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	changed := session.Changed()
	drainChanged(t, changed)

	a.send(t, sheetFrame(t, "s1", acpwire.SheetUpdate{Sheet: sheet(0, 7)}))
	// The ping proves the read goroutine dispatched the update already.
	pingScripted(t, host, a)
	select {
	case <-changed:
		t.Fatal("Changed fired for a sheet the session had not read")
	default:
	}

	// The dropped update was not remembered: the read asks the agent.
	out := startSheet(session, 0)
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodSheet {
		t.Fatalf("Sheet sent %q, want %q", got, acpwire.MethodSheet)
	}
	a.answer(t, req, sheet(0, 3))
	if got := awaitSheet(t, out); got.StartedAt != 3 {
		t.Fatalf("Sheet().StartedAt = %d, want 3", got.StartedAt)
	}
}

// TestRefreshSheetSendsItsFlagsAndFiresChanged pins that RefreshSheet names the
// session's own sheet, carries the flags, and taps Changed even when nothing
// differed.
func TestRefreshSheetSendsItsFlagsAndFiresChanged(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	changed := session.Changed()
	drainChanged(t, changed)

	out := make(chan error, 1)
	go func() {
		out <- session.RefreshSheet(context.Background(), acpwire.SheetRefresh{Changed: true})
	}()
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodSheet {
		t.Fatalf("RefreshSheet sent %q, want %q", got, acpwire.MethodSheet)
	}
	var params acpwire.SheetParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode sheet params: %v", err)
	}
	if params.SessionID != "s1" || params.SubagentID != 0 {
		t.Fatalf("sheet params = %+v, want session s1 and subagent 0", params)
	}
	if params.Refresh == nil || !params.Refresh.Changed || params.Refresh.Rebase || params.Refresh.Stats {
		t.Fatalf("sheet params refresh = %+v, want only changed", params.Refresh)
	}
	a.answer(t, req, sheet(0, 4))

	if err := awaitCall(t, out); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	wantChanged(t, changed)
	if got := session.Sheet(0); got.StartedAt != 4 {
		t.Fatalf("Sheet().StartedAt = %d, want the refreshed 4", got.StartedAt)
	}
}

// TestReleaseSubagentUnsubscribesAndForgetsTheSheet pins that a subagent's
// release unsubscribes its stack and drops its sheet, and that releasing the
// main transcript sends nothing.
func TestReleaseSubagentUnsubscribesAndForgetsTheSheet(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	// Read subagent 3's stack and sheet.
	reads := startStack(session, 3, false)
	answerBusy(t, a, "s1", false)
	answerStack(t, a, "s1", 3, turnSnapshot("s1", 3, 1, "step:1"))
	awaitSnapshot(t, reads)

	sheetOut := startSheet(session, 3)
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodSheet {
		t.Fatalf("Sheet sent %q, want %q", got, acpwire.MethodSheet)
	}
	a.answer(t, req, sheet(3, 1))
	awaitSheet(t, sheetOut)

	out := make(chan error, 1)
	go func() { out <- session.ReleaseSubagent(context.Background(), 3) }()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodStackUnsubscribe {
		t.Fatalf("ReleaseSubagent sent %q, want %q", got, acpwire.MethodStackUnsubscribe)
	}
	var params acpwire.StackParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode unsubscribe params: %v", err)
	}
	if params.SessionID != "s1" || params.SubagentID != 3 {
		t.Fatalf("unsubscribe params = %+v, want session s1 and subagent 3", params)
	}
	a.answer(t, req, struct{}{})
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("ReleaseSubagent: %v", err)
	}

	// The sheet is forgotten, so the next read asks the agent again.
	sheetOut = startSheet(session, 3)
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodSheet {
		t.Fatalf("Sheet after release sent %q, want %q", got, acpwire.MethodSheet)
	}
	a.answer(t, req, sheet(3, 2))
	if got := awaitSheet(t, sheetOut); got.StartedAt != 2 {
		t.Fatalf("Sheet after release = %+v, want the re-fetched sheet", got)
	}

	// The main transcript cannot be released, so nothing goes out.
	go func() { out <- session.ReleaseSubagent(context.Background(), 0) }()
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("ReleaseSubagent(0): %v", err)
	}
	select {
	case req := <-a.reqs:
		t.Fatalf("ReleaseSubagent(0) sent %q", req.Method)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestStackNodeAndCallersSendTheirMethods pins both detail calls' methods and
// params, and that an omitted refs list reads as nil.
func TestStackNodeAndCallersSendTheirMethods(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	type nodeOutcome struct {
		result acpwire.StackNodeResult
		err    error
	}
	nodeOut := make(chan nodeOutcome, 1)
	go func() {
		result, err := session.StackNode(context.Background(), "step:1", 2)
		nodeOut <- nodeOutcome{result: result, err: err}
	}()
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodStackNode {
		t.Fatalf("StackNode sent %q, want %q", got, acpwire.MethodStackNode)
	}
	var nodeParams acpwire.StackNodeParams
	if err := json.Unmarshal(req.Params, &nodeParams); err != nil {
		t.Fatalf("decode stack_node params: %v", err)
	}
	if nodeParams.SessionID != "s1" || nodeParams.NodeID != "step:1" || nodeParams.SubagentID != 2 {
		t.Fatalf("stack_node params = %+v, want session s1, node step:1 and subagent 2", nodeParams)
	}
	a.answer(t, req, acpwire.StackNodeResult{Node: acpwire.WireNode{ID: "step:1", Kind: acpwire.KindStep}})

	var node nodeOutcome
	select {
	case node = <-nodeOut:
	case <-time.After(5 * time.Second):
		t.Fatal("StackNode did not return")
	}
	if node.err != nil {
		t.Fatalf("StackNode: %v", node.err)
	}
	if node.result.Node.ID != "step:1" {
		t.Fatalf("StackNode().Node.ID = %q, want step:1", node.result.Node.ID)
	}

	type callersOutcome struct {
		refs []string
		err  error
	}
	callersOut := make(chan callersOutcome, 1)
	go func() {
		refs, err := session.Callers(context.Background(), "step:1")
		callersOut <- callersOutcome{refs: refs, err: err}
	}()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodCallers {
		t.Fatalf("Callers sent %q, want %q", got, acpwire.MethodCallers)
	}
	var callersParams acpwire.CallersParams
	if err := json.Unmarshal(req.Params, &callersParams); err != nil {
		t.Fatalf("decode callers params: %v", err)
	}
	if callersParams.SessionID != "s1" || callersParams.NodeID != "step:1" {
		t.Fatalf("callers params = %+v, want session s1 and node step:1", callersParams)
	}
	a.answer(t, req, acpwire.CallersResult{Refs: []string{"a.go:1", "b.go:2"}})

	var callers callersOutcome
	select {
	case callers = <-callersOut:
	case <-time.After(5 * time.Second):
		t.Fatal("Callers did not return")
	}
	if callers.err != nil {
		t.Fatalf("Callers: %v", callers.err)
	}
	if !slices.Equal(callers.refs, []string{"a.go:1", "b.go:2"}) {
		t.Fatalf("Callers() = %v, want the agent's refs", callers.refs)
	}

	// An omitted refs list reads as nil.
	callersOut = make(chan callersOutcome, 1)
	go func() {
		refs, err := session.Callers(context.Background(), "step:2")
		callersOut <- callersOutcome{refs: refs, err: err}
	}()
	req = a.next(t)
	a.answer(t, req, acpwire.CallersResult{})

	select {
	case callers = <-callersOut:
	case <-time.After(5 * time.Second):
		t.Fatal("the second Callers did not return")
	}
	if callers.err != nil {
		t.Fatalf("Callers with no refs: %v", callers.err)
	}
	if callers.refs != nil {
		t.Fatalf("Callers with no refs = %v, want nil", callers.refs)
	}
}
