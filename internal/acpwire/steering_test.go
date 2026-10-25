package acpwire

import (
	"encoding/json"
	"testing"
)

// TestSteeringReceiptJSONIsTheACPContract pins the bytes session/steering_status
// serves a client: every field set on the receipt and on its one outcome must
// encode exactly as the contract spells them.
func TestSteeringReceiptJSONIsTheACPContract(t *testing.T) {
	receipt := SteeringReceipt{
		Version:     3,
		RequestID:   "r1",
		TurnID:      "t1",
		Status:      SteeringDelivered,
		OperationID: "op1",
		Outcomes: []SteeringOutcome{{
			Kind:        "tool",
			State:       "stopped",
			ToolCallID:  "c1",
			ExecutionID: "e1",
			AgentID:     "a1",
			Summary:     "s",
		}},
	}

	got, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	const want = `{"version":3,"requestId":"r1","turnId":"t1","status":"delivered","operationId":"op1","outcomes":[{"kind":"tool","state":"stopped","toolCallId":"c1","executionId":"e1","agentId":"a1","summary":"s"}]}`
	if string(got) != want {
		t.Errorf("receipt JSON:\n got: %s\nwant: %s", got, want)
	}
}
