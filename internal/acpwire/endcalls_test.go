package acpwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestSDDCallParamsCarryTheRunsOptions pins a marshal client's plan-run call:
// the strategy, the run's role overrides and the verify commands travel, and a
// call carrying only a plan path encodes exactly the keys ACP's own params
// use.
func TestSDDCallParamsCarryTheRunsOptions(t *testing.T) {
	value := SDDCallParams{
		SessionID: "s1",
		PlanPath:  "docs/plans/p.md",
		Strategy:  StrategyAdaptive,
		Overrides: map[string]string{"sdd_implementer": "kimi/k3"},
		Verify:    &VerifyCommands{Build: "make build", Test: "make test"},
	}
	want := `{"sessionId":"s1","planPath":"docs/plans/p.md","strategy":"adaptive","overrides":{"sdd_implementer":"kimi/k3"},"verify":{"build":"make build","test":"make test"}}`
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != want {
		t.Errorf("SDDCallParams JSON:\n got: %s\nwant: %s", got, want)
	}

	var back SDDCallParams
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, value) {
		t.Errorf("round trip:\n got: %+v\nwant: %+v", back, value)
	}

	pathOnly, err := json.Marshal(SDDCallParams{SessionID: "s1", PlanPath: "docs/plans/p.md"})
	if err != nil {
		t.Fatalf("marshal path only: %v", err)
	}
	if want := `{"sessionId":"s1","planPath":"docs/plans/p.md"}`; string(pathOnly) != want {
		t.Errorf("plan-path-only JSON:\n got: %s\nwant: %s", pathOnly, want)
	}
}

// TestEndWorkUpdateRoundTrips pins the end flow's update: its kind, and the
// membership it carries.
func TestEndWorkUpdateRoundTrips(t *testing.T) {
	value := EndWorkUpdate{
		Kind: UpdateEndWork,
		Work: EndWork{Items: []EndWorkItem{{ID: "op-1", Label: "Running the tests"}}},
	}
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"kind":"end_work","work":{"items":[{"id":"op-1","label":"Running the tests"}]}}`
	if string(got) != want {
		t.Errorf("EndWorkUpdate JSON:\n got: %s\nwant: %s", got, want)
	}

	var back EndWorkUpdate
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, value) {
		t.Errorf("round trip:\n got: %+v\nwant: %+v", back, value)
	}
	if back.Kind != "end_work" {
		t.Errorf("kind = %q, want %q", back.Kind, "end_work")
	}
}

// TestNewSessionParamsNameTheReplacedSession pins a replacing session/new:
// the session it replaces, and nothing else when there is no name.
func TestNewSessionParamsNameTheReplacedSession(t *testing.T) {
	got, err := json.Marshal(NewSessionParams{Replaces: "s1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"replaces":"s1"}`; string(got) != want {
		t.Errorf("NewSessionParams JSON:\n got: %s\nwant: %s", got, want)
	}
}
