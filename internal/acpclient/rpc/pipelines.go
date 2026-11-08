package rpc

import (
	"context"

	"marshal/internal/acpwire"
)

// StartSwarm runs a swarm over params' goal with its role overrides.
func (s *Session) StartSwarm(ctx context.Context, params acpwire.SwarmParams) error {
	call := acpwire.SwarmCallParams{
		SessionID: s.id,
		Goal:      params.Goal,
		Overrides: params.Overrides,
	}
	return s.conn.Call(ctx, acpwire.MethodSwarmStart, call, nil)
}

// PlanPreflight inspects the plan params names, with the strategy asked for,
// before its run starts.
func (s *Session) PlanPreflight(ctx context.Context, params acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error) {
	call := acpwire.PreflightCallParams{SessionID: s.id, Preflight: params}
	var result acpwire.PlanPreflight
	if err := s.conn.Call(ctx, acpwire.MethodSDDPreflight, call, &result); err != nil {
		return acpwire.PlanPreflight{}, err
	}
	return result, nil
}

// StartSDD runs params' plan with its strategy, role overrides and verify
// commands.
func (s *Session) StartSDD(ctx context.Context, params acpwire.SDDParams) error {
	call := acpwire.SDDCallParams{
		SessionID: s.id,
		PlanPath:  params.PlanPath,
		Strategy:  params.Strategy,
		Overrides: params.Overrides,
		Verify:    params.Verify,
	}
	return s.conn.Call(ctx, acpwire.MethodSDDStart, call, nil)
}

// AnswerSDDGate answers the plan run's human gate.
func (s *Session) AnswerSDDGate(ctx context.Context, answer string) error {
	call := acpwire.SDDAnswerParams{SessionID: s.id, Answer: answer}
	return s.conn.Call(ctx, acpwire.MethodSDDAnswer, call, nil)
}

// StopSDD stops the session's plan run.
func (s *Session) StopSDD(ctx context.Context) error {
	params := acpwire.ControlParams{SessionID: s.id}
	return s.conn.Call(ctx, acpwire.MethodSDDStop, params, nil)
}

// AuthorPlan authors one plan from params' design and returns the candidate,
// for review.
func (s *Session) AuthorPlan(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error) {
	call := acpwire.AuthorCallParams{SessionID: s.id, Author: params}
	var result acpwire.AuthoredPlan
	if err := s.conn.Call(ctx, acpwire.MethodSDDAuthor, call, &result); err != nil {
		return acpwire.AuthoredPlan{}, err
	}
	return result, nil
}
