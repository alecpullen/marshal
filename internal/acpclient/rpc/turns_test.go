package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// nextMethod reads the client's next frame and checks it is a call to method.
func nextMethod(t *testing.T, a *scriptedAgent, method string) fakeRequest {
	t.Helper()
	req := a.next(t)
	if got := string(req.Method); got != method {
		t.Fatalf("the client sent %q, want %q", got, method)
	}
	return req
}

// valueResult is one result-bearing call's outcome.
type valueResult[T any] struct {
	value T
	err   error
}

// startCall starts fn on its own goroutine, so the test can answer its call.
func startCall[T any](fn func() (T, error)) <-chan valueResult[T] {
	out := make(chan valueResult[T], 1)
	go func() {
		value, err := fn()
		out <- valueResult[T]{value: value, err: err}
	}()
	return out
}

// awaitValue waits for one result-bearing call's outcome.
func awaitValue[T any](t *testing.T, out <-chan valueResult[T]) valueResult[T] {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not return")
		return valueResult[T]{}
	}
}

// TestPromptSendsTheTextAsOneBlock pins the prompt's frame and its answer: the
// text travels as one text block, with queued and watch resume, and the stop
// reason comes back; a coded refusal comes back as the coded error.
func TestPromptSendsTheTextAsOneBlock(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	params := acpwire.PromptParams{
		Text:        "run the tests",
		Queued:      true,
		WatchResume: &acpwire.WatchResume{Name: "build", Seq: 7},
	}
	out := startCall(func() (acpwire.PromptResult, error) {
		return session.Prompt(context.Background(), params)
	})
	req := nextMethod(t, a, acpwire.MethodPrompt)
	var call acpwire.PromptCallParams
	if err := json.Unmarshal(req.Params, &call); err != nil {
		t.Fatalf("decode prompt params: %v", err)
	}
	if call.SessionID != "s1" {
		t.Fatalf("prompt params = %+v, want session s1", call)
	}
	if len(call.Prompt) != 1 || call.Prompt[0] != (acpwire.TextBlock{Type: "text", Text: params.Text}) {
		t.Fatalf("prompt blocks = %+v, want one text block with %q", call.Prompt, params.Text)
	}
	if !call.Queued {
		t.Fatalf("prompt params = %+v, want queued", call)
	}
	if call.WatchResume == nil || *call.WatchResume != *params.WatchResume {
		t.Fatalf("prompt watch resume = %+v, want %+v", call.WatchResume, params.WatchResume)
	}
	a.answer(t, req, acpwire.PromptResult{StopReason: acpwire.StopReasonEndTurn})
	got := awaitValue(t, out)
	if got.err != nil {
		t.Fatalf("Prompt: %v", got.err)
	}
	if got.value.StopReason != acpwire.StopReasonEndTurn {
		t.Fatalf("Prompt stop reason = %q, want %q", got.value.StopReason, acpwire.StopReasonEndTurn)
	}

	// A coded refusal comes back as the *acpwire.Error the connection maps it
	// to.
	out = startCall(func() (acpwire.PromptResult, error) {
		return session.Prompt(context.Background(), acpwire.PromptParams{Text: "again"})
	})
	req = nextMethod(t, a, acpwire.MethodPrompt)
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeBusy,
		Message: "a run holds the session",
	}))
	got = awaitValue(t, out)
	if !acpclient.IsCode(got.err, acpwire.CodeBusy) {
		t.Fatalf("Prompt error = %v, want an error with code %q", got.err, acpwire.CodeBusy)
	}
}

// TestTheTurnCallsSendTheirMethods pins the other turn calls: each sends its
// method with the session's ID and its params, and returns its result.
func TestTheTurnCallsSendTheirMethods(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	out := make(chan error, 1)

	go func() { out <- session.Stop(context.Background(), "turn stopped") }()
	req := nextMethod(t, a, acpwire.MethodStop)
	var stop acpwire.StopParams
	if err := json.Unmarshal(req.Params, &stop); err != nil {
		t.Fatalf("decode stop params: %v", err)
	}
	if stop.SessionID != "s1" || stop.Cause != "turn stopped" {
		t.Fatalf("stop params = %+v, want session s1 and cause", stop)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	review := acpwire.ReviewParams{Focus: "the diff", Model: "m1", Base: "main"}
	go func() { out <- session.Review(context.Background(), review) }()
	req = nextMethod(t, a, acpwire.MethodReview)
	var reviewCall acpwire.ReviewCallParams
	if err := json.Unmarshal(req.Params, &reviewCall); err != nil {
		t.Fatalf("decode review params: %v", err)
	}
	if reviewCall.SessionID != "s1" || reviewCall.Review != review {
		t.Fatalf("review params = %+v, want session s1 and %+v", reviewCall, review)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("Review: %v", err)
	}

	run := acpwire.AgentRunParams{Agent: "reviewer", Goal: "check the diff"}
	go func() { out <- session.RunAgent(context.Background(), run) }()
	req = nextMethod(t, a, acpwire.MethodAgentRun)
	var runCall acpwire.AgentRunCallParams
	if err := json.Unmarshal(req.Params, &runCall); err != nil {
		t.Fatalf("decode agent_run params: %v", err)
	}
	if runCall.SessionID != "s1" || runCall.Run != run {
		t.Fatalf("agent_run params = %+v, want session s1 and %+v", runCall, run)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	suggest := startCall(func() (string, error) {
		return session.Suggest(context.Background(), "the build is green")
	})
	req = nextMethod(t, a, acpwire.MethodSuggest)
	var suggestParams acpwire.SuggestParams
	if err := json.Unmarshal(req.Params, &suggestParams); err != nil {
		t.Fatalf("decode suggest params: %v", err)
	}
	if suggestParams.SessionID != "s1" || suggestParams.LastMessage != "the build is green" {
		t.Fatalf("suggest params = %+v, want session s1 and the last message", suggestParams)
	}
	a.answer(t, req, acpwire.SuggestResult{Text: "then tag it"})
	got := awaitValue(t, suggest)
	if got.err != nil {
		t.Fatalf("Suggest: %v", got.err)
	}
	if got.value != "then tag it" {
		t.Fatalf("Suggest = %q, want %q", got.value, "then tag it")
	}

	verify := startCall(func() (acpwire.VerifyCommands, error) {
		return session.VerifyProposal(context.Background())
	})
	req = nextMethod(t, a, acpwire.MethodVerifyProposal)
	var verifyParams acpwire.ControlParams
	if err := json.Unmarshal(req.Params, &verifyParams); err != nil {
		t.Fatalf("decode verify_proposal params: %v", err)
	}
	if verifyParams.SessionID != "s1" {
		t.Fatalf("verify_proposal params = %+v, want session s1", verifyParams)
	}
	commands := acpwire.VerifyCommands{Build: "go build ./...", Test: "go test ./..."}
	a.answer(t, req, commands)
	ve := awaitValue(t, verify)
	if ve.err != nil {
		t.Fatalf("VerifyProposal: %v", ve.err)
	}
	if ve.value != commands {
		t.Fatalf("VerifyProposal = %+v, want %+v", ve.value, commands)
	}
}

// TestSetInteractionNamesSystemAccess pins the one key ACP names its own way:
// the frame carries system_access, not system, and omits a nil field.
func TestSetInteractionNamesSystemAccess(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	mode := "act"
	on := true
	out := startCall(func() (acpwire.Interaction, error) {
		return session.SetInteraction(context.Background(), acpwire.InteractionPatch{Mode: &mode, System: &on})
	})
	req := nextMethod(t, a, acpwire.MethodSetMode)
	var raw map[string]any
	if err := json.Unmarshal(req.Params, &raw); err != nil {
		t.Fatalf("decode set_mode params: %v", err)
	}
	if got := raw["sessionId"]; got != "s1" {
		t.Fatalf("set_mode params = %s, want session s1", req.Params)
	}
	if got := raw["mode"]; got != mode {
		t.Fatalf("set_mode params = %s, want mode %q", req.Params, mode)
	}
	if got := raw["system_access"]; got != true {
		t.Fatalf("set_mode params = %s, want system_access true", req.Params)
	}
	if _, ok := raw["system"]; ok {
		t.Fatalf("set_mode params = %s carry system, want system_access", req.Params)
	}
	if _, ok := raw["auto"]; ok {
		t.Fatalf("set_mode params = %s carry auto, want the nil field omitted", req.Params)
	}

	want := acpwire.Interaction{Mode: mode, System: true}
	a.answer(t, req, want)
	got := awaitValue(t, out)
	if got.err != nil {
		t.Fatalf("SetInteraction: %v", got.err)
	}
	if got.value != want {
		t.Fatalf("SetInteraction = %+v, want %+v", got.value, want)
	}
}

// popAnswer is one PopSteering call's outcome.
type popAnswer struct {
	text string
	ok   bool
}

// watchResumeAnswer is one TakeWatchResume call's outcome.
type watchResumeAnswer struct {
	name   string
	repeat bool
	ok     bool
}

// TestTheControlsSendTheirMethods pins every control call's method, params and
// answer, and the zero answer a failed call reports.
func TestTheControlsSendTheirMethods(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})
	out := make(chan error, 1)

	go func() { out <- session.Steer(context.Background(), "try the other file") }()
	req := nextMethod(t, a, acpwire.MethodSteer)
	var steer acpwire.SteerCallParams
	if err := json.Unmarshal(req.Params, &steer); err != nil {
		t.Fatalf("decode steer params: %v", err)
	}
	if steer.SessionID != "s1" || steer.Text != "try the other file" || steer.Delivery != "" {
		t.Fatalf("steer params = %+v, want session s1, the text and no delivery", steer)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("Steer: %v", err)
	}

	send := startCall(func() (acpwire.SteeringReceipt, error) {
		return session.SendNow(context.Background(), acpwire.SendNowRequest{
			TurnID:    "t1",
			RequestID: "r1",
			Text:      "stop and test",
		})
	})
	req = nextMethod(t, a, acpwire.MethodSteer)
	var sendParams acpwire.SteerCallParams
	if err := json.Unmarshal(req.Params, &sendParams); err != nil {
		t.Fatalf("decode send now params: %v", err)
	}
	if sendParams.SessionID != "s1" || sendParams.Text != "stop and test" ||
		sendParams.Delivery != acpwire.SteerDeliveryNow ||
		sendParams.TurnID != "t1" || sendParams.RequestID != "r1" {
		t.Fatalf("send now params = %+v, want session s1, the text, delivery now, turn t1 and request r1", sendParams)
	}
	receipt := acpwire.SteeringReceipt{
		Version:   2,
		RequestID: "r1",
		TurnID:    "t1",
		Status:    acpwire.SteeringAccepted,
	}
	a.answer(t, req, receipt)
	sent := awaitValue(t, send)
	if sent.err != nil {
		t.Fatalf("SendNow: %v", sent.err)
	}
	if sent.value.RequestID != receipt.RequestID || sent.value.Status != receipt.Status {
		t.Fatalf("SendNow = %+v, want request r1 accepted", sent.value)
	}

	pops := make(chan popAnswer, 1)
	go func() {
		text, ok := session.PopSteering(context.Background())
		pops <- popAnswer{text: text, ok: ok}
	}()
	req = nextMethod(t, a, acpwire.MethodSteeringPop)
	var popParams acpwire.ControlParams
	if err := json.Unmarshal(req.Params, &popParams); err != nil {
		t.Fatalf("decode steering_pop params: %v", err)
	}
	if popParams.SessionID != "s1" {
		t.Fatalf("steering_pop params = %+v, want session s1", popParams)
	}
	a.answer(t, req, acpwire.SteeringPopResult{Text: "try the other file", OK: true})
	select {
	case got := <-pops:
		if got.text != "try the other file" || !got.ok {
			t.Fatalf("PopSteering = %q, %v; want the text and true", got.text, got.ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PopSteering did not return")
	}

	// A failed pop is a refusal, as a closed session answers in process.
	go func() {
		text, ok := session.PopSteering(context.Background())
		pops <- popAnswer{text: text, ok: ok}
	}()
	req = nextMethod(t, a, acpwire.MethodSteeringPop)
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeSessionClosed,
		Message: "the session is gone",
	}))
	select {
	case got := <-pops:
		if got.text != "" || got.ok {
			t.Fatalf("a failed PopSteering = %q, %v; want \"\", false", got.text, got.ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PopSteering did not return")
	}

	go func() { out <- session.ClearSteering(context.Background()) }()
	req = nextMethod(t, a, acpwire.MethodSteeringClear)
	var clearParams acpwire.ControlParams
	if err := json.Unmarshal(req.Params, &clearParams); err != nil {
		t.Fatalf("decode steering_clear params: %v", err)
	}
	if clearParams.SessionID != "s1" {
		t.Fatalf("steering_clear params = %+v, want session s1", clearParams)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("ClearSteering: %v", err)
	}

	effort := startCall(func() (string, error) {
		return session.SetEffort(context.Background(), "high")
	})
	req = nextMethod(t, a, acpwire.MethodSetEffort)
	var effortParams acpwire.EffortParams
	if err := json.Unmarshal(req.Params, &effortParams); err != nil {
		t.Fatalf("decode set_effort params: %v", err)
	}
	if effortParams.SessionID != "s1" || effortParams.Effort != "high" {
		t.Fatalf("set_effort params = %+v, want session s1 and effort high", effortParams)
	}
	a.answer(t, req, acpwire.EffortResult{Effort: "high", AppliesFrom: acpwire.EffortAppliesNextStep})
	applied := awaitValue(t, effort)
	if applied.err != nil {
		t.Fatalf("SetEffort: %v", applied.err)
	}
	if applied.value != acpwire.EffortAppliesNextStep {
		t.Fatalf("SetEffort = %q, want %q", applied.value, acpwire.EffortAppliesNextStep)
	}

	go func() { out <- session.SetSudoUnlock(context.Background(), true) }()
	req = nextMethod(t, a, acpwire.MethodSetSudoUnlock)
	var sudoParams acpwire.SudoUnlockParams
	if err := json.Unmarshal(req.Params, &sudoParams); err != nil {
		t.Fatalf("decode set_sudo_unlock params: %v", err)
	}
	if sudoParams.SessionID != "s1" || !sudoParams.On {
		t.Fatalf("set_sudo_unlock params = %+v, want session s1 and on", sudoParams)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("SetSudoUnlock: %v", err)
	}

	cancels := make(chan bool, 1)
	go func() { cancels <- session.CancelSubagent(context.Background(), 7) }()
	req = nextMethod(t, a, acpwire.MethodSubagentCancel)
	var cancelParams acpwire.SubagentCancelParams
	if err := json.Unmarshal(req.Params, &cancelParams); err != nil {
		t.Fatalf("decode subagent_cancel params: %v", err)
	}
	if cancelParams.SessionID != "s1" || cancelParams.SubagentID != 7 {
		t.Fatalf("subagent_cancel params = %+v, want session s1 and subagent 7", cancelParams)
	}
	a.answer(t, req, acpwire.SubagentCancelResult{Cancelled: true})
	select {
	case cancelled := <-cancels:
		if !cancelled {
			t.Fatal("CancelSubagent = false, want true")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CancelSubagent did not return")
	}

	go func() { out <- session.ClearFinishedRun(context.Background()) }()
	req = nextMethod(t, a, acpwire.MethodSDDClear)
	var sddParams acpwire.ControlParams
	if err := json.Unmarshal(req.Params, &sddParams); err != nil {
		t.Fatalf("decode sdd_clear params: %v", err)
	}
	if sddParams.SessionID != "s1" {
		t.Fatalf("sdd_clear params = %+v, want session s1", sddParams)
	}
	a.answer(t, req, nil)
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("ClearFinishedRun: %v", err)
	}

	takes := make(chan watchResumeAnswer, 1)
	go func() {
		name, repeat, ok := session.TakeWatchResume(context.Background())
		takes <- watchResumeAnswer{name: name, repeat: repeat, ok: ok}
	}()
	req = nextMethod(t, a, acpwire.MethodWatchResumeTake)
	var takeParams acpwire.ControlParams
	if err := json.Unmarshal(req.Params, &takeParams); err != nil {
		t.Fatalf("decode watch_resume_take params: %v", err)
	}
	if takeParams.SessionID != "s1" {
		t.Fatalf("watch_resume_take params = %+v, want session s1", takeParams)
	}
	a.answer(t, req, acpwire.WatchResumeTakeResult{Name: "build", Repeat: true, OK: true})
	select {
	case got := <-takes:
		if got.name != "build" || !got.repeat || !got.ok {
			t.Fatalf("TakeWatchResume = %q, %v, %v; want build, true, true", got.name, got.repeat, got.ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TakeWatchResume did not return")
	}
}
