package viewmodel

import (
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// SteeringStateOf projects a session's Send now snapshot onto the wire.
func SteeringStateOf(snap session.SteeringSnapshot) acpwire.SteeringState {
	return acpwire.SteeringState{
		Version:       snap.Version,
		TurnID:        snap.TurnID,
		Active:        snap.Active,
		Supported:     snap.Supported,
		Held:          snap.Held,
		OperationID:   snap.OperationID,
		OperationKind: string(snap.OperationKind),
		PendingCount:  snap.PendingCount,
	}
}

// SteeringReceiptOf projects a session receipt onto the wire, deep-copying
// its outcomes.
func SteeringReceiptOf(rec session.SteeringReceipt) acpwire.SteeringReceipt {
	out := acpwire.SteeringReceipt{
		Version:     rec.Version,
		RequestID:   rec.RequestID,
		TurnID:      rec.TurnID,
		Status:      string(rec.Status),
		OperationID: rec.OperationID,
	}
	if len(rec.Outcomes) > 0 {
		out.Outcomes = make([]acpwire.SteeringOutcome, len(rec.Outcomes))
		for i, o := range rec.Outcomes {
			out.Outcomes[i] = acpwire.SteeringOutcome{
				Kind:        o.Kind,
				State:       o.State,
				ToolCallID:  o.ToolCallID,
				ExecutionID: o.ExecutionID,
				AgentID:     o.AgentID,
				Summary:     o.Summary,
			}
		}
	}
	return out
}
