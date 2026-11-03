package app

import (
	"context"

	"marshal/internal/acpwire"
)

// endService is the runtime's end of a conversation, in the neutral shapes a
// transport speaks: the work an end would stop, the acceptance that freezes it,
// the settlement that stops and joins it, and the exit analysis. It converts
// the runtime's own shapes into acpwire's mirrors and delegates every decision
// back to the runtime, so the transport never sees a session or a process.
type endService struct {
	rt *Runtime
}

// ActiveWork reports the runtime's current conversation work.
func (e *endService) ActiveWork() acpwire.EndWork {
	return endWorkOf(e.rt.ActiveWork())
}

// AcceptEnd submits one end acceptance and reports it in acpwire's shape.
func (e *endService) AcceptEnd(params acpwire.EndAcceptParams) (acpwire.EndDecision, error) {
	decision, err := e.rt.AcceptEnd(EndAcceptRequest{
		Intent:           EndIntent(params.Intent),
		DisplayedIDs:     params.DisplayedIDs,
		RequireDisplayed: params.RequireDisplayed,
	})
	if err != nil {
		return acpwire.EndDecision{}, err
	}
	return acpwire.EndDecision{
		Accepted: decision.Accepted,
		Expanded: decision.Expanded,
		Work:     endWorkOf(decision.Work),
	}, nil
}

// WaitForEnd stops and joins the accepted end's work.
func (e *endService) WaitForEnd(ctx context.Context) error {
	return e.rt.WaitForEnd(ctx)
}

// RunExitAnalysis runs the scoped exit-analysis pass during the postmortem
// phase. The runtime owns the pass's elevation and joins its epoch before
// returning.
func (e *endService) RunExitAnalysis(ctx context.Context, goal string) error {
	return e.rt.RunExitAnalysis(ctx, goal)
}

// endWorkOf converts a runtime work snapshot to acpwire's neutral mirror. Each
// item keeps its ID, label and group, in the runtime's order.
func endWorkOf(work ActiveWork) acpwire.EndWork {
	out := acpwire.EndWork{}
	for _, item := range work.Items {
		out.Items = append(out.Items, acpwire.EndWorkItem{
			ID:    item.ID,
			Label: item.Label,
			Group: item.Group,
		})
	}
	return out
}
