package acp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// callPlanRun is the service package's fakePlanRun, copied here so these
// handler tests drive a plan run without that package's tests. It gates its
// first gates runs, records each answer and its run count, and can block until
// its context is done after signalling that it started.
type callPlanRun struct {
	gates   int
	answers []string
	runs    int
	// started, when non-nil, is closed once when Run first begins.
	started   chan struct{}
	startOnce sync.Once
	// block makes Run wait for its context after it signals, and return the
	// context's error.
	block bool
	// done, when non-nil, is closed once when a blocking Run returns, so a
	// test can show the run is still going and not merely still reported.
	done     chan struct{}
	doneOnce sync.Once
}

func (f *callPlanRun) Run(ctx context.Context) error {
	f.runs++
	if f.started != nil {
		f.startOnce.Do(func() { close(f.started) })
	}
	if f.block {
		<-ctx.Done()
		if f.done != nil {
			f.doneOnce.Do(func() { close(f.done) })
		}
		return ctx.Err()
	}
	if f.runs <= f.gates {
		return &acpwire.Error{Code: acpwire.CodeHumanGateRequired, Message: "pipeline: human gate required"}
	}
	return nil
}

func (f *callPlanRun) Answer(answer string) { f.answers = append(f.answers, answer) }

// TestAMarshalSwarmRunsOnTheService pins decision 2: a marshal client's
// session/swarm_start reaches the session's service with the goal and the
// run's role overrides, and never ACP's own handler.
func TestAMarshalSwarmRunsOnTheService(t *testing.T) {
	var calls []acpwire.SwarmParams
	otherCalled := false
	conn, _ := startControlAgent(t, true, service.Deps{
		State: seamState(t, controlSession),
		Swarm: func(ctx context.Context, params acpwire.SwarmParams) error {
			calls = append(calls, params)
			return nil
		},
	}, map[string]Handler{
		acpwire.MethodSwarmStart: func(context.Context, json.RawMessage) (any, error) {
			otherCalled = true
			return struct{}{}, nil
		},
	})

	params := acpwire.SwarmCallParams{
		SessionID: controlSession,
		Goal:      "add a regression test",
		Overrides: map[string]string{"planner": "local/qwen"},
	}
	var res json.RawMessage
	if err := conn.Call(context.Background(), acpwire.MethodSwarmStart, params, &res); err != nil {
		t.Fatalf("session/swarm_start: %v", err)
	}
	want := []acpwire.SwarmParams{{
		Goal:      "add a regression test",
		Overrides: map[string]string{"planner": "local/qwen"},
	}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("the service's swarm calls = %+v, want %+v", calls, want)
	}
	if otherCalled {
		t.Error("ACP's own swarm handler ran for a marshal client")
	}
}

// TestAPipelineWithoutTheCapabilityReachesItsOtherHandler pins R9 and V6: a
// client without the capability keeps ACP's pipelines, and the service is
// never asked to run one.
func TestAPipelineWithoutTheCapabilityReachesItsOtherHandler(t *testing.T) {
	methods := []struct {
		name   string
		method string
		params any
	}{
		{"swarm_start", acpwire.MethodSwarmStart, acpwire.SwarmCallParams{SessionID: controlSession, Goal: "g"}},
		{"sdd_start", acpwire.MethodSDDStart, acpwire.SDDCallParams{SessionID: controlSession, PlanPath: "docs/plans/p.md"}},
		{"sdd_answer", acpwire.MethodSDDAnswer, acpwire.SDDAnswerParams{SessionID: controlSession, Answer: "yes"}},
	}

	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			var swarmCalls []acpwire.SwarmParams
			var runs int
			var reached []string
			others := map[string]Handler{}
			for _, name := range methods {
				method := name.method
				others[method] = func(context.Context, json.RawMessage) (any, error) {
					reached = append(reached, method)
					return struct{}{}, nil
				}
			}
			conn, _ := startControlAgent(t, false, service.Deps{
				State: seamState(t, controlSession),
				Swarm: func(ctx context.Context, params acpwire.SwarmParams) error {
					swarmCalls = append(swarmCalls, params)
					return nil
				},
				NewPlanRun: func(acpwire.SDDParams) (service.PlanRun, error) {
					runs++
					return &callPlanRun{}, nil
				},
			}, others)

			var res json.RawMessage
			if err := conn.Call(context.Background(), m.method, m.params, &res); err != nil {
				t.Fatalf("%s: %v", m.method, err)
			}
			if !reflect.DeepEqual(reached, []string{m.method}) {
				t.Errorf("others reached = %v, want only %s", reached, m.method)
			}
			if len(swarmCalls) != 0 {
				t.Errorf("the service's swarm ran %+v for a client without the capability", swarmCalls)
			}
			if runs != 0 {
				t.Errorf("the service built %d plan runs for a client without the capability", runs)
			}
		})
	}
}

// TestAGatedPlanRunCrossesAsItsCode pins that a gate's code travels as itself,
// and that an answer resumes the paused run.
func TestAGatedPlanRunCrossesAsItsCode(t *testing.T) {
	fake := &callPlanRun{gates: 1}
	conn, _ := startControlAgent(t, true, service.Deps{
		State:      seamState(t, controlSession),
		NewPlanRun: func(acpwire.SDDParams) (service.PlanRun, error) { return fake, nil },
	}, nil)

	var res json.RawMessage
	err := conn.Call(context.Background(), acpwire.MethodSDDStart, acpwire.SDDCallParams{
		SessionID: controlSession,
		PlanPath:  "docs/plans/p.md",
	}, &res)
	if !acpclient.IsCode(err, acpwire.CodeHumanGateRequired) {
		t.Fatalf("session/sdd_start = %v, want code %q", err, acpwire.CodeHumanGateRequired)
	}

	if err := conn.Call(context.Background(), acpwire.MethodSDDAnswer, acpwire.SDDAnswerParams{
		SessionID: controlSession,
		Answer:    "yes",
	}, &res); err != nil {
		t.Fatalf("session/sdd_answer: %v", err)
	}
	if !reflect.DeepEqual(fake.answers, []string{"yes"}) {
		t.Errorf("the run recorded answers %v, want [yes]", fake.answers)
	}
	if fake.runs != 2 {
		t.Errorf("the fake ran %d times, want the answer to resume it once", fake.runs)
	}
}

// TestSDDStartCarriesTheRunsOptions pins that the strategy, the role overrides
// and the verify commands reach the runtime's plan-run factory unchanged.
func TestSDDStartCarriesTheRunsOptions(t *testing.T) {
	var calls []acpwire.SDDParams
	conn, _ := startControlAgent(t, true, service.Deps{
		State: seamState(t, controlSession),
		NewPlanRun: func(params acpwire.SDDParams) (service.PlanRun, error) {
			calls = append(calls, params)
			return &callPlanRun{}, nil
		},
	}, nil)

	params := acpwire.SDDCallParams{
		SessionID: controlSession,
		PlanPath:  "docs/plans/p.md",
		Strategy:  acpwire.StrategyAdaptive,
		Overrides: map[string]string{"sdd_implementer": "kimi/k3"},
		Verify:    &acpwire.VerifyCommands{Build: "make build", Test: "make test"},
	}
	var res json.RawMessage
	if err := conn.Call(context.Background(), acpwire.MethodSDDStart, params, &res); err != nil {
		t.Fatalf("session/sdd_start: %v", err)
	}
	want := []acpwire.SDDParams{{
		PlanPath:  "docs/plans/p.md",
		Strategy:  acpwire.StrategyAdaptive,
		Overrides: map[string]string{"sdd_implementer": "kimi/k3"},
		Verify:    &acpwire.VerifyCommands{Build: "make build", Test: "make test"},
	}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("the plan run's params = %+v, want %+v", calls, want)
	}
}

// TestThePreflightAndTheAuthorReachTheService pins the three pipeline methods a
// marshal client gains, and that a stop drops the paused run's question.
func TestThePreflightAndTheAuthorReachTheService(t *testing.T) {
	state := seamState(t, controlSession)
	preflight := acpwire.PlanPreflight{
		Inspection: acpwire.PlanInspection{Strategy: acpwire.StrategyAdaptive},
		Verify:     acpwire.VerifyGate{Build: "go build ./...", Known: true},
	}
	var preflightCalls []acpwire.PlanPreflightParams
	var authorCalls []acpwire.AuthorPlanParams
	conn, _ := startControlAgent(t, true, service.Deps{
		State: state,
		PlanPreflight: func(ctx context.Context, params acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error) {
			preflightCalls = append(preflightCalls, params)
			return preflight, nil
		},
		AuthorPlan: func(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error) {
			authorCalls = append(authorCalls, params)
			return acpwire.AuthoredPlan{PlanPath: "/repo/p.md"}, nil
		},
		NewPlanRun: func(acpwire.SDDParams) (service.PlanRun, error) {
			return &callPlanRun{gates: 1}, nil
		},
	}, nil)

	wantParams := acpwire.PlanPreflightParams{PlanPath: "docs/plans/p.md", Strategy: acpwire.StrategyStrict}
	var got acpwire.PlanPreflight
	if err := conn.Call(context.Background(), acpwire.MethodSDDPreflight, acpwire.PreflightCallParams{
		SessionID: controlSession,
		Preflight: wantParams,
	}, &got); err != nil {
		t.Fatalf("session/sdd_preflight: %v", err)
	}
	if !reflect.DeepEqual(got, preflight) {
		t.Errorf("preflight = %+v, want what the runtime returned", got)
	}
	if !reflect.DeepEqual(preflightCalls, []acpwire.PlanPreflightParams{wantParams}) {
		t.Errorf("preflight calls = %+v, want the params unchanged", preflightCalls)
	}

	wantAuthor := acpwire.AuthorPlanParams{Goal: "add retries", Design: "a design"}
	var candidate acpwire.AuthoredPlan
	if err := conn.Call(context.Background(), acpwire.MethodSDDAuthor, acpwire.AuthorCallParams{
		SessionID: controlSession,
		Author:    wantAuthor,
	}, &candidate); err != nil {
		t.Fatalf("session/sdd_author: %v", err)
	}
	if candidate.PlanPath != "/repo/p.md" || candidate.StopReason != acpwire.StopReasonEndTurn {
		t.Errorf("candidate = %+v, want the runtime's candidate at end_turn", candidate)
	}
	if !reflect.DeepEqual(authorCalls, []acpwire.AuthorPlanParams{wantAuthor}) {
		t.Errorf("authoring calls = %+v, want the params unchanged", authorCalls)
	}

	// A gated run leaves its question on the state; the stop clears it.
	var res json.RawMessage
	err := conn.Call(context.Background(), acpwire.MethodSDDStart, acpwire.SDDCallParams{
		SessionID: controlSession,
		PlanPath:  "docs/plans/p.md",
	}, &res)
	if !acpclient.IsCode(err, acpwire.CodeHumanGateRequired) {
		t.Fatalf("session/sdd_start = %v, want the gate", err)
	}
	state.SetSDDGate(session.SDDGate{TaskN: 1, Question: "which database?"})
	if err := conn.Call(context.Background(), acpwire.MethodSDDStop, acpwire.ControlParams{
		SessionID: controlSession,
	}, &res); err != nil {
		t.Fatalf("session/sdd_stop: %v", err)
	}
	if q := state.SDDGate().Question; q != "" {
		t.Errorf("the gate question = %q after the stop, want it cleared", q)
	}
}

// TestCancellingAPlanRunLeavesItGoing pins P4 D4: the client's context ending
// stops its call's wait, not the plan run. The run goes on until session/stop,
// which reports it cancelled.
func TestCancellingAPlanRunLeavesItGoing(t *testing.T) {
	fake := &callPlanRun{started: make(chan struct{}), done: make(chan struct{}), block: true}
	conn, a := startControlAgent(t, true, service.Deps{
		State:      seamState(t, controlSession),
		NewPlanRun: func(acpwire.SDDParams) (service.PlanRun, error) { return fake, nil },
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		var res json.RawMessage
		done <- conn.Call(ctx, acpwire.MethodSDDStart, acpwire.SDDCallParams{
			SessionID: controlSession,
			PlanPath:  "docs/plans/p.md",
		}, &res)
	}()

	select {
	case <-fake.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the plan run never started")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled call did not return")
	}

	// The call's context ending stops the wait, and the run goes on: its work
	// has not returned, and the status still reports it.
	time.Sleep(200 * time.Millisecond)
	select {
	case <-fake.done:
		t.Fatal("the plan run returned after its call's context ended, want it still going")
	default:
	}
	if flight := a.served.Status().Run; flight == nil || flight.Kind != acpwire.RunSDD {
		t.Fatalf("the status carries %+v, want the plan run still in flight", flight)
	}

	// Only session/stop ends it.
	if err := conn.Call(context.Background(), acpwire.MethodStop, acpwire.StopParams{
		SessionID: controlSession,
		Cause:     "plan run stopped",
	}, nil); err != nil {
		t.Fatalf("session/stop: %v", err)
	}
	waitForFrame(t, 2*time.Second, "the cancelled plan run in the status", func() bool {
		last := a.served.Status().LastRun
		return last != nil && last.Outcome == acpwire.RunCancelled
	})
}

// TestThePipelineCallsRefuseBadParams pins the two refusals the six pipeline
// methods share: a missing session id is invalid params, and an unknown one is
// the unknown-session error.
func TestThePipelineCallsRefuseBadParams(t *testing.T) {
	conn, _ := startControlAgent(t, true, service.Deps{State: seamState(t, controlSession)}, nil)

	methods := []struct {
		name   string
		method string
	}{
		{"swarm_start", acpwire.MethodSwarmStart},
		{"sdd_preflight", acpwire.MethodSDDPreflight},
		{"sdd_start", acpwire.MethodSDDStart},
		{"sdd_answer", acpwire.MethodSDDAnswer},
		{"sdd_stop", acpwire.MethodSDDStop},
		{"sdd_author", acpwire.MethodSDDAuthor},
	}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			var res json.RawMessage
			err := conn.Call(context.Background(), m.method, json.RawMessage(`{}`), &res)
			var rpcErr *rpc.Error
			if err == nil || !errors.As(err, &rpcErr) || rpcErr.Code != invalidParams {
				t.Fatalf("err = %v, want the invalid-params refusal", err)
			}
			err = conn.Call(context.Background(), m.method, json.RawMessage(`{"sessionId":"nope"}`), &res)
			if err == nil || !strings.Contains(err.Error(), "unknown session") {
				t.Fatalf("err = %v, want the unknown-session refusal", err)
			}
		})
	}
}
