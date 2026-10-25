package viewmodel

import (
	"reflect"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

func TestSteeringStateOfCopiesEveryField(t *testing.T) {
	snap := session.SteeringSnapshot{
		Version:       3,
		TurnID:        "t1",
		Active:        true,
		Supported:     true,
		Held:          true,
		OperationID:   "op1",
		OperationKind: session.OperationTools,
		PendingCount:  2,
	}
	want := acpwire.SteeringState{
		Version:       3,
		TurnID:        "t1",
		Active:        true,
		Supported:     true,
		Held:          true,
		OperationID:   "op1",
		OperationKind: "tools",
		PendingCount:  2,
	}
	got := SteeringStateOf(snap)
	if got != want {
		t.Fatalf("SteeringStateOf = %+v, want %+v", got, want)
	}
}

func TestSteeringReceiptOfCopiesEveryField(t *testing.T) {
	rec := session.SteeringReceipt{
		Version:     2,
		RequestID:   "r1",
		TurnID:      "t1",
		Status:      session.SteeringInterrupting,
		OperationID: "op1",
		Outcomes: []session.SteeringOutcome{{
			Kind:        "command",
			State:       "stopped",
			ToolCallID:  "call-1",
			ExecutionID: "exec-1",
			AgentID:     "agent-1",
			Summary:     "stopped",
		}},
	}
	want := acpwire.SteeringReceipt{
		Version:     2,
		RequestID:   "r1",
		TurnID:      "t1",
		Status:      "interrupting",
		OperationID: "op1",
		Outcomes: []acpwire.SteeringOutcome{{
			Kind:        "command",
			State:       "stopped",
			ToolCallID:  "call-1",
			ExecutionID: "exec-1",
			AgentID:     "agent-1",
			Summary:     "stopped",
		}},
	}
	got := SteeringReceiptOf(rec)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SteeringReceiptOf = %+v, want %+v", got, want)
	}

	// The projected receipt owns its outcome slice: mutating the source
	// afterwards leaves the wire value unchanged.
	rec.Outcomes[0].Kind = "mutated"
	rec.Outcomes[0].Summary = "mutated"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SteeringReceiptOf aliased the session's outcome slice: %+v", got)
	}
}
