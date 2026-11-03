package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/castlist"
	"marshal/internal/app/tui/gatepanel"
)

// fakePlanRun is a plan run whose first gates runs pause at the human gate,
// and which records each answer.
type fakePlanRun struct {
	mu      sync.Mutex
	gates   int
	runs    int
	answers []string
}

func (r *fakePlanRun) Run(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs++
	if r.gates > 0 {
		r.gates--
		return &acpwire.Error{Code: acpwire.CodeHumanGateRequired, Message: "pipeline: human gate required"}
	}
	return nil
}

func (r *fakePlanRun) Answer(answer string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers = append(r.answers, answer)
}

// preflightOf returns a Deps.PlanPreflight that always returns pf.
func preflightOf(pf acpwire.PlanPreflight) func(context.Context, acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error) {
	return func(context.Context, acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error) {
		return pf, nil
	}
}

// planRunOf returns a Deps.NewPlanRun that appends each params to *got (when
// got is not nil) and returns run.
func planRunOf(run *fakePlanRun, got *[]acpwire.SDDParams) func(acpwire.SDDParams) (inproc.PlanRun, error) {
	return func(params acpwire.SDDParams) (inproc.PlanRun, error) {
		if got != nil {
			*got = append(*got, params)
		}
		return run, nil
	}
}

// pausedGateModel builds a sized model whose session holds a plan run paused
// at its gate, with gate set in the state and the gate panel open.
func pausedGateModel(t *testing.T, gate session.SDDGate) (Model, *fakePlanRun, *turnRecorder) {
	t.Helper()
	run := &fakePlanRun{gates: 1}
	rec := &turnRecorder{}
	m := testModelTurns(t, inproc.Deps{
		State:      controlsState(t),
		Runner:     runnerOf(&testAgentRunner{}),
		NewPlanRun: planRunOf(run, nil),
	}, rec)
	if err := m.sess.StartSDD(m.ctx, acpwire.SDDParams{PlanPath: "/plans/p.md"}); !acpclient.IsCode(err, acpwire.CodeHumanGateRequired) {
		t.Fatalf("StartSDD = %v, want code %q", err, acpwire.CodeHumanGateRequired)
	}
	m.state.SetSDDGate(gate)
	m.dock.Open(gatepanel.New(m.sess.Status().SDDGate))
	return m, run, rec
}

// TestTheProposalReachesThePlanRun pins decision 10 at the TUI: the commands
// the user approved at the preflight travel on the plan run's parameters, not
// through config or a rebuilt runner.
func TestTheProposalReachesThePlanRun(t *testing.T) {
	var got []acpwire.SDDParams
	run := &fakePlanRun{}
	rec := &turnRecorder{}
	m := testModelTurns(t, inproc.Deps{
		State:  controlsState(t),
		Runner: runnerOf(&testAgentRunner{}),
		VerifyProposal: func(context.Context) (acpwire.VerifyCommands, error) {
			return acpwire.VerifyCommands{Build: "make build", Test: "make test"}, nil
		},
		PlanPreflight: preflightOf(acpwire.PlanPreflight{
			Inspection: acpwire.PlanInspection{Strategy: acpwire.StrategyAgent},
		}),
		NewPlanRun: planRunOf(run, &got),
	}, rec, WithCommandRegistry(setupCmdReg(t)))

	updated, _ := m.dispatchCommand("/sdd plan.md")
	m = asModel(t, updated)

	// Press f to dispatch the proposal.
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'f'})
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("expected a proposal command from pressing f")
	}
	updated, _ = m.Update(cmd())
	m = asModel(t, updated)

	// Confirm the run and drain it.
	updated, cmd = m.Update(castlist.StartMsg{})
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("confirm should return a non-nil cmd")
	}
	m = drainCmds(m, cmd)

	if len(got) != 1 {
		t.Fatalf("plan run params = %+v, want one", got)
	}
	if got[0].Verify == nil || got[0].Verify.Build != "make build" || got[0].Verify.Test != "make test" {
		t.Fatalf("plan run Verify = %+v, want both commands", got[0].Verify)
	}
}

// TestAPlanThePreflightRejectsIsReportedNotRun pins decision 9 at the TUI: a
// plan the session's preflight rejects is reported before the preflight opens,
// and nothing runs.
func TestAPlanThePreflightRejectsIsReportedNotRun(t *testing.T) {
	rec := &turnRecorder{}
	m := testModelTurns(t, inproc.Deps{
		State:  controlsState(t),
		Runner: runnerOf(&testAgentRunner{}),
		PlanPreflight: func(context.Context, acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error) {
			return acpwire.PlanPreflight{}, errors.New("pipeline plan: read missing.md: no such file")
		},
	}, rec, WithCommandRegistry(setupCmdReg(t)))

	updated, cmd := m.dispatchCommand("/sdd missing.md")
	m = asModel(t, updated)
	if cmd != nil {
		t.Fatal("a rejected preflight returned a command")
	}
	if m.dock.IsOpen() {
		t.Fatal("a rejected preflight must not open the dock")
	}
	if m.busy {
		t.Fatal("a rejected preflight must leave the model idle")
	}
	msgs := m.state.Messages()
	last := ""
	if len(msgs) > 0 {
		last = msgs[len(msgs)-1].Content
	}
	want := "Cannot start plan run: pipeline plan: read missing.md: no such file"
	if last != want {
		t.Fatalf("last message = %q, want %q", last, want)
	}
}

// TestTheGateStopsThroughTheSession pins that stopping a plan run paused at
// its gate is a session call, and the TUI only writes the note.
func TestTheGateStopsThroughTheSession(t *testing.T) {
	m, _, rec := pausedGateModel(t, session.SDDGate{TaskN: 1, Question: "Which timeout applies?"})

	updated, _ := m.Update(gatepanel.StopMsg{})
	m = asModel(t, updated)

	if rec.sddStops != 1 {
		t.Fatalf("StopSDD calls = %d, want 1", rec.sddStops)
	}
	if q := m.sess.Status().SDDGate.Question; q != "" {
		t.Fatalf("gate question = %q after stop, want it cleared", q)
	}
	var stopped bool
	for _, msg := range m.state.Messages() {
		if strings.Contains(msg.Content, "Plan run stopped.") {
			stopped = true
		}
	}
	if !stopped {
		t.Fatal("stopping must write the \"Plan run stopped.\" note")
	}
}

// TestSwarmWithoutAnAgentIsUnavailable pins the refusal: /swarm with no runner
// says so and opens nothing.
func TestSwarmWithoutAnAgentIsUnavailable(t *testing.T) {
	rec := &turnRecorder{}
	m := testModelTurns(t, inproc.Deps{State: controlsState(t)}, rec, WithCommandRegistry(setupCmdReg(t)))

	updated, cmd := m.dispatchCommand("/swarm fix it")
	m = asModel(t, updated)
	if cmd != nil {
		t.Fatal("an unavailable swarm returned a command")
	}
	if m.dock.IsOpen() {
		t.Fatal("an unavailable swarm must not open the dock")
	}
	msgs := m.state.Messages()
	last := ""
	if len(msgs) > 0 {
		last = msgs[len(msgs)-1].Content
	}
	if last != "Swarm is not available (agent failed to initialise)." {
		t.Fatalf("last message = %q, want the unavailable note", last)
	}
}
