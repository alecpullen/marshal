package acpwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestStatusUpdateRoundTrips pins the wire shape of a session_status update:
// the whole status survives the round trip, notice, turn route and quota
// included, and its kind is session_status.
func TestStatusUpdateRoundTrips(t *testing.T) {
	want := StatusUpdate{
		Kind: UpdateSessionStatus,
		Status: SessionStatus{
			WorkingDir:  "/tmp/w",
			ActiveRoute: Route{Role: "implementer", Profile: "default", Provider: "p", Model: "m"},
			TurnRoute:   &Route{Role: "implementer", Model: "m2", Thinking: "high"},
			TurnUsed:    1234,
			TurnWindow:  200000,
			Quota:       &Quota{PlanType: "pro", PrimaryUsedPercent: 42, PrimaryWindowMs: 60000},
			Notice: &Notice{
				Category: NoticeConfig,
				Severity: NoticeWarn,
				Message:  "a banner",
				Hint:     "a hint",
				SetAt:    1700000000000,
			},
		},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got StatusUpdate
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != UpdateSessionStatus {
		t.Fatalf("kind = %q, want %q", got.Kind, UpdateSessionStatus)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip =\n%#v\nwant\n%#v", got, want)
	}
}
