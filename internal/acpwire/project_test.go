package acpwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestProjectStateRoundTrips pins the wire shape of project/state's answer
// (B3): the stale reasons, what keeps the child busy, and its version all
// survive the round trip.
func TestProjectStateRoundTrips(t *testing.T) {
	want := ProjectState{
		Stale:   []string{"config changed", "a new build"},
		Busy:    []string{"session 1"},
		Version: "1.2.3",
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ProjectState
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip =\n%#v\nwant\n%#v", got, want)
	}
}
