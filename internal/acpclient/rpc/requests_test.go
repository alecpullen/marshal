package rpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// pendingFrame builds the session/update notification carrying one
// pending_requests update.
func pendingFrame(t *testing.T, sessionID string, state acpwire.PendingState) string {
	t.Helper()
	return updateFrame(t, sessionID, acpwire.PendingUpdate{Kind: acpwire.UpdatePending, Pending: state})
}

// pendingState is a state the tests recognize by its question's key.
func pendingState(questionKey string) acpwire.PendingState {
	return acpwire.PendingState{Requests: acpwire.PendingRequests{
		Question: &acpwire.QuestionRequest{
			Key:       questionKey,
			Questions: []acpwire.Question{{Question: "why?"}},
		},
	}}
}

// questionKey is the mirror's question key, and "" when it holds none.
func questionKey(requests acpwire.PendingRequests) string {
	if requests.Question == nil {
		return ""
	}
	return requests.Question.Key
}

// readPending starts one Pending read on its own goroutine, so the test can
// answer its call.
func readPending(session *Session) <-chan acpwire.PendingRequests {
	out := make(chan acpwire.PendingRequests, 1)
	go func() { out <- session.Pending() }()
	return out
}

// awaitPending waits for one Pending read's value.
func awaitPending(t *testing.T, out <-chan acpwire.PendingRequests) acpwire.PendingRequests {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("Pending did not return")
		return acpwire.PendingRequests{}
	}
}

// nextPending reads the client's next frame and checks it is session/pending
// for session.
func nextPending(t *testing.T, a *scriptedAgent, session *Session) fakeRequest {
	t.Helper()
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodPending {
		t.Fatalf("the client sent %q, want %q", got, acpwire.MethodPending)
	}
	var params acpwire.PendingParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode pending params: %v", err)
	}
	if params.SessionID != session.ID() {
		t.Fatalf("pending params = %+v, want session %q", params, session.ID())
	}
	return req
}

// firstRead performs the mirror's first read, answering session/pending with
// state, and returns what that read reported.
func firstRead(t *testing.T, session *Session, a *scriptedAgent, state acpwire.PendingState) acpwire.PendingRequests {
	t.Helper()
	out := readPending(session)
	req := nextPending(t, a, session)
	a.answer(t, req, state)
	return awaitPending(t, out)
}

// TestPendingFetchesOnItsFirstRead pins the two halves of the read: the first
// one asks the agent, and every later one answers from the mirror.
func TestPendingFetchesOnItsFirstRead(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	if key := questionKey(firstRead(t, session, a, pendingState("q1"))); key != "q1" {
		t.Fatalf("the first read's question key = %q, want q1", key)
	}

	if key := questionKey(session.Pending()); key != "q1" {
		t.Fatalf("the second read's question key = %q, want q1", key)
	}
	select {
	case req := <-a.reqs:
		t.Fatalf("the second read sent %q", req.Method)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestPendingInstallsTheUpdateThatPrecedesTheResponse pins the barrier: the
// agent answers session/pending with the state it had, so an update that
// reaches the client before the response is newer and must be kept.
func TestPendingInstallsTheUpdateThatPrecedesTheResponse(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	out := readPending(session)
	req := nextPending(t, a, session)
	a.send(t, pendingFrame(t, "s1", pendingState("A")))
	a.answer(t, req, pendingState("B"))

	if key := questionKey(awaitPending(t, out)); key != "A" {
		t.Fatalf("the read's question key = %q, want A: the update that preceded the response is newer", key)
	}
}

func TestPendingInstallsTheResultWhenNoUpdateCame(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	if key := questionKey(firstRead(t, session, a, pendingState("B"))); key != "B" {
		t.Fatalf("the read's question key = %q, want B", key)
	}
	if key := questionKey(session.Pending()); key != "B" {
		t.Fatalf("the mirror's question key = %q, want B", key)
	}
}

func TestAPendingUpdateReplacesTheRequestsAndFiresChanged(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	firstRead(t, session, a, pendingState("A"))
	changed := session.Changed()
	drainChanged(t, changed)

	a.send(t, pendingFrame(t, "s1", pendingState("B")))
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("Changed did not fire for the update")
	}
	if key := questionKey(session.Pending()); key != "B" {
		t.Fatalf("the mirror's question key = %q, want B", key)
	}
}

// TestAPendingUpdateBeforeTheFirstReadIsDropped pins the other side of the
// barrier: an update no read subscribed to is not buffered, and the read that
// follows asks instead.
func TestAPendingUpdateBeforeTheFirstReadIsDropped(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	a.send(t, pendingFrame(t, "s1", pendingState("A")))
	// The ping proves the read goroutine dispatched the update already.
	pingScripted(t, host, a)

	select {
	case <-session.Changed():
		t.Fatal("Changed fired for an update no read subscribed to")
	default:
	}
	if key := questionKey(firstRead(t, session, a, pendingState("B"))); key != "B" {
		t.Fatalf("the first read's question key = %q, want B", key)
	}
}

// TestReconcileAccessReadsTheMirror pins that ReconcileAccess answers from the
// mirror: the update that carries an access request surfaces it, and the one
// that carries none reports it gone, without a call either way.
func TestReconcileAccessReadsTheMirror(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	access := acpwire.AccessRequest{RequestID: "r1", SessionID: "s1", Reason: "edit this file"}
	firstRead(t, session, a, acpwire.PendingState{Access: &access})
	drainChanged(t, session.Changed())

	if got, ok := session.ReconcileAccess(context.Background()); !ok || got != access {
		t.Fatalf("ReconcileAccess = %+v, %v; want %+v, true", got, ok, access)
	}

	a.send(t, pendingFrame(t, "s1", pendingState("A")))
	wantChanged(t, session.Changed())
	if got, ok := session.ReconcileAccess(context.Background()); ok {
		t.Fatalf("ReconcileAccess = %+v, true; want none once the update carries none", got)
	}

	select {
	case req := <-a.reqs:
		t.Fatalf("ReconcileAccess sent %q", req.Method)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestAnswersSendTheirMethods pins each answer's method and params, the result
// AnswerAccess returns, and the error a coded refusal comes back as.
func TestAnswersSendTheirMethods(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	out := make(chan error, 1)

	go func() {
		out <- session.AnswerApproval(context.Background(), "ap1", acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce})
	}()
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodAnswerApproval {
		t.Fatalf("AnswerApproval sent %q, want %q", got, acpwire.MethodAnswerApproval)
	}
	var approval acpwire.AnswerApprovalParams
	if err := json.Unmarshal(req.Params, &approval); err != nil {
		t.Fatalf("decode answer_approval params: %v", err)
	}
	if approval.SessionID != "s1" || approval.Key != "ap1" || approval.Answer.Decision != acpwire.ApprovalOnce {
		t.Fatalf("answer_approval params = %+v, want session s1, key ap1 and decision once", approval)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("AnswerApproval: %v", err)
	}

	answers := []acpwire.Answer{{Question: "why?", Answer: "because"}}
	go func() { out <- session.AnswerQuestion(context.Background(), "q1", answers) }()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodAnswerQuestion {
		t.Fatalf("AnswerQuestion sent %q, want %q", got, acpwire.MethodAnswerQuestion)
	}
	var question acpwire.AnswerQuestionParams
	if err := json.Unmarshal(req.Params, &question); err != nil {
		t.Fatalf("decode answer_question params: %v", err)
	}
	if question.SessionID != "s1" || question.Key != "q1" || len(question.Answers) != 1 || question.Answers[0] != answers[0] {
		t.Fatalf("answer_question params = %+v, want session s1, key q1 and %+v", question, answers)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}

	go func() {
		out <- session.AnswerSkillGate(context.Background(), "sg1", acpwire.SkillGateAllowOnce)
	}()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodAnswerSkillGate {
		t.Fatalf("AnswerSkillGate sent %q, want %q", got, acpwire.MethodAnswerSkillGate)
	}
	var gate acpwire.AnswerSkillGateParams
	if err := json.Unmarshal(req.Params, &gate); err != nil {
		t.Fatalf("decode answer_skill_gate params: %v", err)
	}
	if gate.SessionID != "s1" || gate.Key != "sg1" || gate.Choice != acpwire.SkillGateAllowOnce {
		t.Fatalf("answer_skill_gate params = %+v, want session s1, key sg1 and allow_once", gate)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("AnswerSkillGate: %v", err)
	}

	go func() { out <- session.Rollback(context.Background()) }()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodRollback {
		t.Fatalf("Rollback sent %q, want %q", got, acpwire.MethodRollback)
	}
	var rollback acpwire.PendingParams
	if err := json.Unmarshal(req.Params, &rollback); err != nil {
		t.Fatalf("decode rollback params: %v", err)
	}
	if rollback.SessionID != "s1" {
		t.Fatalf("rollback params = %+v, want session s1", rollback)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	choice := acpwire.AccessChoice{RequestID: "r1", SessionID: "s1", Approved: true, Mode: "act"}
	accesses := make(chan accessResult, 2)
	go func() {
		result, err := session.AnswerAccess(context.Background(), choice)
		accesses <- accessResult{result: result, err: err}
	}()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodAnswerAccess {
		t.Fatalf("AnswerAccess sent %q, want %q", got, acpwire.MethodAnswerAccess)
	}
	var access acpwire.AnswerAccessParams
	if err := json.Unmarshal(req.Params, &access); err != nil {
		t.Fatalf("decode answer_access params: %v", err)
	}
	if access.SessionID != "s1" || access.Choice != choice {
		t.Fatalf("answer_access params = %+v, want session s1 and %+v", access, choice)
	}
	a.answer(t, req, acpwire.AccessResult{RequestID: "r1", SessionID: "s1", Status: acpwire.AccessGranted})
	got := awaitAccess(t, accesses)
	if got.err != nil {
		t.Fatalf("AnswerAccess: %v", got.err)
	}
	if got.result.RequestID != "r1" || got.result.Status != acpwire.AccessGranted {
		t.Fatalf("AnswerAccess result = %+v, want r1 granted", got.result)
	}

	// A coded refusal comes back as the *acpwire.Error the connection maps it
	// to.
	go func() {
		result, err := session.AnswerAccess(context.Background(), choice)
		accesses <- accessResult{result: result, err: err}
	}()
	req = a.next(t)
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeRequestSettled,
		Message: "the request is gone",
	}))
	got = awaitAccess(t, accesses)
	if !acpclient.IsCode(got.err, acpwire.CodeRequestSettled) {
		t.Fatalf("AnswerAccess error = %v, want an error with code %q", got.err, acpwire.CodeRequestSettled)
	}
}

// accessResult is one AnswerAccess call's outcome.
type accessResult struct {
	result acpwire.AccessResult
	err    error
}

// awaitAccess waits for one AnswerAccess outcome.
func awaitAccess(t *testing.T, out <-chan accessResult) accessResult {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("AnswerAccess did not return")
		return accessResult{}
	}
}

// TestAnswerSudoZeroesThePassword pins both ends of the password's journey:
// the frame carries it, base64-encoded, and the caller's slice is zeroed when
// the call returns, whatever its outcome.
func TestAnswerSudoZeroesThePassword(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	out := make(chan error, 1)

	const secret = "correct horse battery"
	password := []byte(secret)
	go func() {
		out <- session.AnswerSudo(context.Background(), "sd1", acpwire.SudoAnswer{OK: true, Password: password})
	}()
	req := a.next(t)
	if got := string(req.Method); got != acpwire.MethodAnswerSudo {
		t.Fatalf("AnswerSudo sent %q, want %q", got, acpwire.MethodAnswerSudo)
	}
	var params acpwire.AnswerSudoParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode answer_sudo params: %v", err)
	}
	if params.SessionID != "s1" || params.Key != "sd1" || !params.Answer.OK {
		t.Fatalf("answer_sudo params = %+v, want session s1, key sd1 and an ok answer", params)
	}
	if !bytes.Contains(req.Params, []byte(base64.StdEncoding.EncodeToString([]byte(secret)))) {
		t.Fatalf("answer_sudo params %s do not carry the password's base64 encoding", req.Params)
	}
	if got := string(params.Answer.Password); got != secret {
		t.Fatalf("answer_sudo password = %q, want the password", got)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("AnswerSudo: %v", err)
	}
	for i, b := range password {
		if b != 0 {
			t.Fatalf("password[%d] = %d after AnswerSudo, want 0", i, b)
		}
	}

	// The zeroing holds when the call fails too.
	password = []byte(secret)
	go func() {
		out <- session.AnswerSudo(context.Background(), "sd1", acpwire.SudoAnswer{OK: true, Password: password})
	}()
	req = a.next(t)
	if got := string(req.Method); got != acpwire.MethodAnswerSudo {
		t.Fatalf("the second AnswerSudo sent %q, want %q", got, acpwire.MethodAnswerSudo)
	}
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeRequestSettled,
		Message: "the prompt is gone",
	}))
	if err := awaitCall(t, out); !acpclient.IsCode(err, acpwire.CodeRequestSettled) {
		t.Fatalf("the second AnswerSudo error = %v, want an error with code %q", err, acpwire.CodeRequestSettled)
	}
	for i, b := range password {
		if b != 0 {
			t.Fatalf("password[%d] = %d after a failed AnswerSudo, want 0", i, b)
		}
	}
}
