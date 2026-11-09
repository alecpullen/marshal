package rpc

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// startErr starts an error-only call on its own goroutine, so the test can
// answer its call.
func startErr(fn func() error) <-chan error {
	out := make(chan error, 1)
	go func() { out <- fn() }()
	return out
}

// decodeCall decodes one request's params into a T.
func decodeCall[T any](t *testing.T, req fakeRequest) T {
	t.Helper()
	var params T
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode %q params: %v", string(req.Method), err)
	}
	return params
}

// TestThePipelineCallsSendTheirMethods pins the six pipeline calls: each sends
// its method with the session's ID and its params, and decodes its result. A
// refused gate answer comes back as the coded error.
func TestThePipelineCallsSendTheirMethods(t *testing.T) {
	host, a := dialScripted(t, DialOptions{})
	session := attachScripted(t, host, a, "s1", acpwire.SessionStatus{WorkingDir: "B"})

	swarm := acpwire.SwarmParams{Goal: "ship it", Overrides: map[string]string{"implementer": "fast"}}
	out := startErr(func() error { return session.StartSwarm(context.Background(), swarm) })
	req := nextMethod(t, a, acpwire.MethodSwarmStart)
	wantSwarm := acpwire.SwarmCallParams{SessionID: "s1", Goal: swarm.Goal, Overrides: swarm.Overrides}
	if got := decodeCall[acpwire.SwarmCallParams](t, req); !reflect.DeepEqual(got, wantSwarm) {
		t.Fatalf("swarm_start params = %+v, want %+v", got, wantSwarm)
	}
	a.answer(t, req, struct{}{})
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("StartSwarm: %v", err)
	}

	sdd := acpwire.SDDParams{
		PlanPath:  "docs/plan.md",
		Strategy:  acpwire.StrategyAdaptive,
		Overrides: map[string]string{"reviewer": "slow"},
		Verify:    &acpwire.VerifyCommands{Build: "go build ./...", Test: "go test ./..."},
	}
	out = startErr(func() error { return session.StartSDD(context.Background(), sdd) })
	req = nextMethod(t, a, acpwire.MethodSDDStart)
	wantSDD := acpwire.SDDCallParams{
		SessionID: "s1",
		PlanPath:  sdd.PlanPath,
		Strategy:  sdd.Strategy,
		Overrides: sdd.Overrides,
		Verify:    sdd.Verify,
	}
	if got := decodeCall[acpwire.SDDCallParams](t, req); !reflect.DeepEqual(got, wantSDD) {
		t.Fatalf("sdd_start params = %+v, want %+v", got, wantSDD)
	}
	a.answer(t, req, struct{}{})
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("StartSDD: %v", err)
	}

	out = startErr(func() error { return session.AnswerSDDGate(context.Background(), "approve") })
	req = nextMethod(t, a, acpwire.MethodSDDAnswer)
	wantAnswer := acpwire.SDDAnswerParams{SessionID: "s1", Answer: "approve"}
	if got := decodeCall[acpwire.SDDAnswerParams](t, req); got != wantAnswer {
		t.Fatalf("sdd_answer params = %+v, want %+v", got, wantAnswer)
	}
	a.answer(t, req, struct{}{})
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("AnswerSDDGate: %v", err)
	}

	// A refused answer comes back as the coded error, not as text.
	out = startErr(func() error { return session.AnswerSDDGate(context.Background(), "later") })
	req = nextMethod(t, a, acpwire.MethodSDDAnswer)
	a.send(t, codedErrorFrame(t, string(req.ID), acpwire.Error{
		Code:    acpwire.CodeHumanGateRequired,
		Message: "the gate needs a person's answer",
	}))
	if err := awaitCall(t, out); !acpclient.IsCode(err, acpwire.CodeHumanGateRequired) {
		t.Fatalf("the refused answer's error = %v, want an error with code %q", err, acpwire.CodeHumanGateRequired)
	}

	preflight := acpwire.PlanPreflightParams{PlanPath: "docs/plan.md", Strategy: acpwire.StrategyStrict}
	pre := startCall(func() (acpwire.PlanPreflight, error) {
		return session.PlanPreflight(context.Background(), preflight)
	})
	req = nextMethod(t, a, acpwire.MethodSDDPreflight)
	wantPreflight := acpwire.PreflightCallParams{SessionID: "s1", Preflight: preflight}
	if got := decodeCall[acpwire.PreflightCallParams](t, req); !reflect.DeepEqual(got, wantPreflight) {
		t.Fatalf("sdd_preflight params = %+v, want %+v", got, wantPreflight)
	}
	inspection := acpwire.PlanPreflight{
		Inspection: acpwire.PlanInspection{
			Strategy:  acpwire.StrategyStrict,
			HasBlocks: true,
			DetOps:    3,
			AgentOps:  1,
			Tasks:     []acpwire.PlanTask{{TaskN: 1, Title: "the first", DetOps: 3, AgentOps: 1}},
		},
		Verify: acpwire.VerifyGate{Build: "go build ./...", Known: true, Detected: true},
	}
	a.answer(t, req, inspection)
	preflighted := awaitValue(t, pre)
	if preflighted.err != nil {
		t.Fatalf("PlanPreflight: %v", preflighted.err)
	}
	if !reflect.DeepEqual(preflighted.value, inspection) {
		t.Fatalf("PlanPreflight = %+v, want %+v", preflighted.value, inspection)
	}

	author := acpwire.AuthorPlanParams{Goal: "ship it", Design: "the design"}
	authored := startCall(func() (acpwire.AuthoredPlan, error) {
		return session.AuthorPlan(context.Background(), author)
	})
	req = nextMethod(t, a, acpwire.MethodSDDAuthor)
	wantAuthor := acpwire.AuthorCallParams{SessionID: "s1", Author: author}
	if got := decodeCall[acpwire.AuthorCallParams](t, req); !reflect.DeepEqual(got, wantAuthor) {
		t.Fatalf("sdd_author params = %+v, want %+v", got, wantAuthor)
	}
	candidate := acpwire.AuthoredPlan{
		StopReason:   "end_turn",
		PlanPath:     "docs/plan.md",
		AutoAccepted: true,
		Inspection:   &inspection.Inspection,
	}
	a.answer(t, req, candidate)
	authoredPlan := awaitValue(t, authored)
	if authoredPlan.err != nil {
		t.Fatalf("AuthorPlan: %v", authoredPlan.err)
	}
	if !reflect.DeepEqual(authoredPlan.value, candidate) {
		t.Fatalf("AuthorPlan = %+v, want %+v", authoredPlan.value, candidate)
	}

	out = startErr(func() error { return session.StopSDD(context.Background()) })
	req = nextMethod(t, a, acpwire.MethodSDDStop)
	wantStop := acpwire.ControlParams{SessionID: "s1"}
	if got := decodeCall[acpwire.ControlParams](t, req); got != wantStop {
		t.Fatalf("sdd_stop params = %+v, want %+v", got, wantStop)
	}
	a.answer(t, req, struct{}{})
	if err := awaitCall(t, out); err != nil {
		t.Fatalf("StopSDD: %v", err)
	}
}
