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
			Run: &Run{ID: "turn-2", Kind: RunTurn, StartedAt: 1700000000000},
			LastRun: &RunResult{
				ID:        "turn-1",
				Kind:      RunReview,
				StartedAt: 1699999999000,
				EndedAt:   1700000000000,
				Outcome:   RunFailed,
				Error: &Error{
					Code:    CodeProviderError,
					Message: "the provider rejected the call",
					Data:    json.RawMessage(`{"notice":{"message":"a failed run"}}`),
				},
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

// TestAStatusWithoutRunsOmitsThem pins the absent case: a session with no run
// in flight and no finished run carries neither key, so a reader sees them
// missing rather than zero (P4 D4).
func TestAStatusWithoutRunsOmitsThem(t *testing.T) {
	raw, err := json.Marshal(SessionStatus{WorkingDir: "/tmp/w"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"run", "lastRun"} {
		if _, ok := fields[key]; ok {
			t.Errorf("%q is present in %s, want it omitted", key, raw)
		}
	}
}
