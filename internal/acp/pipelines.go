package acp

import (
	"context"
	"encoding/json"

	"marshal/internal/acpwire"
)

// This file holds the pipeline handlers a marshal client reaches (P3 plan 6c,
// decision 2). Each decodes the client's params, resolves the session and
// answers with the service's result or its error, unchanged: ACP's own
// handlers take no role overrides, strategy or verify commands, and report a
// gate differently, so they stay the other clients' path.

// SwarmStart handles session/swarm_start for a marshal client.
func (m *ControlManager) SwarmStart(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.SwarmCallParams
	if err := decodeParams(params, &p, acpwire.MethodSwarmStart); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	// The call's context ending stops the wait, and the run goes on until
	// session/stop (P4 D4).
	runCtx, cancel := cancellable(ctx)
	defer cancel()
	if err := sess.StartSwarm(runCtx, acpwire.SwarmParams{
		Goal:      p.Goal,
		Overrides: p.Overrides,
	}); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// SDDStart handles session/sdd_start for a marshal client. A gate's
// CodeHumanGateRequired is the service's *acpwire.Error and travels as itself.
func (m *ControlManager) SDDStart(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.SDDCallParams
	if err := decodeParams(params, &p, acpwire.MethodSDDStart); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	// The call's context ending stops the wait, and the run goes on until
	// session/stop (P4 D4).
	runCtx, cancel := cancellable(ctx)
	defer cancel()
	if err := sess.StartSDD(runCtx, acpwire.SDDParams{
		PlanPath:  p.PlanPath,
		Strategy:  p.Strategy,
		Overrides: p.Overrides,
		Verify:    p.Verify,
	}); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// SDDAnswer handles session/sdd_answer for a marshal client.
func (m *ControlManager) SDDAnswer(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.SDDAnswerParams
	if err := decodeParams(params, &p, acpwire.MethodSDDAnswer); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	// The call's context ending stops the wait, and the run goes on until
	// session/stop (P4 D4).
	runCtx, cancel := cancellable(ctx)
	defer cancel()
	if err := sess.AnswerSDDGate(runCtx, p.Answer); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// SDDPreflight handles session/sdd_preflight.
func (m *ControlManager) SDDPreflight(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.PreflightCallParams
	if err := decodeParams(params, &p, acpwire.MethodSDDPreflight); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := cancellable(ctx)
	defer cancel()
	return sess.PlanPreflight(runCtx, p.Preflight)
}

// SDDStop handles session/sdd_stop. It is not cancellable: the call drops a
// paused run, which takes no turn slot (decision 3).
func (m *ControlManager) SDDStop(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodSDDStop); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.StopSDD(ctx); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// SDDAuthor handles session/sdd_author. Its run is the caller's, unlike every
// other kind: the call's context ending ends the authoring run (P4 D4).
func (m *ControlManager) SDDAuthor(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.AuthorCallParams
	if err := decodeParams(params, &p, acpwire.MethodSDDAuthor); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := cancellable(ctx)
	defer cancel()
	return sess.AuthorPlan(runCtx, p.Author)
}
