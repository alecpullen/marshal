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

// TestSteeringReceiptOmitsEmptyFields pins the other half of the contract: a
// receipt with no outcomes and no operation encodes without those keys at all,
// so a client must not expect them to be present-but-empty.
func TestSteeringReceiptOmitsEmptyFields(t *testing.T) {
	got, err := json.Marshal(SteeringReceipt{})
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	const want = `{"version":0,"requestId":"","turnId":"","status":""}`
	if string(got) != want {
		t.Errorf("empty receipt JSON:\n got: %s\nwant: %s", got, want)
	}
}

// TestSendNowRequestOmitsEmptyText pins the client-to-server direction: an
// empty text omits the key rather than sending "".
func TestSendNowRequestOmitsEmptyText(t *testing.T) {
	got, err := json.Marshal(SendNowRequest{SessionID: "s1", TurnID: "t1", RequestID: "r1"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	const want = `{"sessionId":"s1","turnId":"t1","requestId":"r1"}`
	if string(got) != want {
		t.Errorf("request JSON:\n got: %s\nwant: %s", got, want)
	}
}
