package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// PlanRun is one plan-execution run. Run runs it, or resumes it after Answer.
// A run its human gate pauses returns an error carrying CodeHumanGateRequired.
type PlanRun interface {
	Run(ctx context.Context) error
	Answer(answer string)
}

var (
	errNoSwarm      = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no swarm runner"}
	errNoPlanRunner = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no plan runner"}
	errNoPreflight  = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no plan preflight"}
	errNoPlanAuthor = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no plan author"}
	errNoGoal       = errors.New("inproc: a run needs a goal")
	errNoPlanPath   = errors.New("inproc: a plan run needs a plan path")
	errNoAnswer     = errors.New("inproc: the gate needs an answer")
	errNoPausedPlan = errors.New("inproc: no plan run is waiting on an answer")
)

// StartSwarm runs a swarm over params.Goal on the run path. A stopped run
// returns no error.
func (s *Session) StartSwarm(ctx context.Context, params acpwire.SwarmParams) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	if s.swarm == nil {
		return errNoSwarm
	}
	if strings.TrimSpace(params.Goal) == "" {
		return errNoGoal
	}
	stopped, _, err := s.runTurn(ctx, func(ctx context.Context) error {
		return s.swarm(ctx, params)
	})
	// A stopped swarm is not an error: the stop already reported the cancel,
	// and the swarm's own context error is that stop, not a failure.
	if stopped {
		return nil
	}
	return err
}

// PlanPreflight inspects the plan a run would execute and resolves its verify
// gate. Like Suggest it takes no turn slot: it inspects the plan without
// running it.
func (s *Session) PlanPreflight(ctx context.Context, params acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error) {
	if s.closed.Load() {
		return acpwire.PlanPreflight{}, errSessionClosed
	}
	if s.planPreflight == nil {
		return acpwire.PlanPreflight{}, errNoPreflight
	}
	if strings.TrimSpace(params.PlanPath) == "" {
		return acpwire.PlanPreflight{}, errNoPlanPath
	}
	if err := checkStrategy(params.Strategy); err != nil {
		return acpwire.PlanPreflight{}, err
	}
	return s.planPreflight(ctx, params)
}

// StartSDD runs the plan params names on the run path. A run its human gate
// pauses returns CodeHumanGateRequired and becomes the session's paused run;
// a stopped run returns no error.
func (s *Session) StartSDD(ctx context.Context, params acpwire.SDDParams) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	if s.newPlanRun == nil {
		return errNoPlanRunner
	}
	if strings.TrimSpace(params.PlanPath) == "" {
		return errNoPlanPath
	}
	if err := checkStrategy(params.Strategy); err != nil {
		return err
	}
	stopped, _, err := s.runTurn(ctx, func(ctx context.Context) error {
		run, err := s.newPlanRun(params)
		if err != nil {
			return err
		}
		if run == nil {
			return errNoPlanRunner
		}
		// A new run replaces one paused at its gate: the older run can no
		// longer be answered.
		s.mu.Lock()
		s.paused = nil
		s.mu.Unlock()
		err = run.Run(ctx)
		s.settlePlan(run, err)
		return err
	})
	// A stopped run is not an error, with one exception: a run that reached
	// its human gate is paused and awaiting an answer, which the stop did not
	// cancel. The TUI only opens the gate panel on that code, so it has to
	// arrive or the paused run waits for an answer nobody was asked for.
	if stopped {
		return stoppedErr(err)
	}
	return err
}

// stoppedErr is what a stopped run reports. A stop that lands on a run which
// reached its human gate still reports the gate: the run is paused, not over.
func stoppedErr(err error) error {
	if acpclient.IsCode(err, acpwire.CodeHumanGateRequired) {
		return err
	}
	return nil
}

// AnswerSDDGate records answer as the user's message and resumes the paused
// plan run on the run path. A refusal at admission never reaches the run, so
// the run stays paused (decision 5).
func (s *Session) AnswerSDDGate(ctx context.Context, answer string) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	if strings.TrimSpace(answer) == "" {
		return errNoAnswer
	}
	s.mu.Lock()
	run := s.paused
	s.mu.Unlock()
	if run == nil {
		return errNoPausedPlan
	}
	stopped, _, err := s.runTurn(ctx, func(ctx context.Context) error {
		s.mu.Lock()
		// A stop or a newer run that landed first dropped this one: it can no
		// longer be answered.
		if s.paused != run {
			s.mu.Unlock()
			return errNoPausedPlan
		}
		s.paused = nil
		s.mu.Unlock()
		s.state.AddMessage(session.RoleUser, answer, session.ContentTypePlain)
		run.Answer(answer)
		err := run.Run(ctx)
		s.settlePlan(run, err)
		return err
	})
	if stopped {
		return stoppedErr(err)
	}
	return err
}

// StopSDD abandons the plan run paused at its human gate. With nothing paused
// it only clears the question, so a stale one never outlives its run.
func (s *Session) StopSDD(ctx context.Context) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	s.mu.Lock()
	s.paused = nil
	s.mu.Unlock()
	s.state.ClearSDDGate()
	return nil
}

// AuthorPlan runs the plan author on the run path and returns the candidate it
// wrote. A stopped run reports StopReasonCancelled and no error.
func (s *Session) AuthorPlan(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error) {
	if s.closed.Load() {
		return acpwire.AuthoredPlan{}, errSessionClosed
	}
	if s.authorPlan == nil {
		return acpwire.AuthoredPlan{}, errNoPlanAuthor
	}
	if strings.TrimSpace(params.Goal) == "" {
		return acpwire.AuthoredPlan{}, errNoGoal
	}
	var out acpwire.AuthoredPlan
	stopped, _, err := s.runTurn(ctx, func(ctx context.Context) error {
		var runErr error
		out, runErr = s.authorPlan(ctx, params)
		return runErr
	})
	if stopped {
		return acpwire.AuthoredPlan{StopReason: acpwire.StopReasonCancelled}, nil
	}
	if err != nil {
		return acpwire.AuthoredPlan{}, err
	}
	out.StopReason = acpwire.StopReasonEndTurn
	return out, nil
}

// settlePlan records how run ended: a run its gate paused becomes the
// session's paused run.
func (s *Session) settlePlan(run PlanRun, err error) {
	if !acpclient.IsCode(err, acpwire.CodeHumanGateRequired) {
		return
	}
	s.mu.Lock()
	s.paused = run
	s.mu.Unlock()
}

// checkStrategy returns an error naming strategy unless it is empty or one of
// the three strategies.
func checkStrategy(strategy string) error {
	switch strategy {
	case "", acpwire.StrategyAgent, acpwire.StrategyAdaptive, acpwire.StrategyStrict:
		return nil
	}
	return fmt.Errorf("inproc: unknown strategy %q (want agent, adaptive or strict)", strategy)
}
