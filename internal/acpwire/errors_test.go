package acpwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestErrorMessageFallsBackToTheCode pins the Error() contract: the message
// wins when it is set, and the code stands in for it when it is not.
func TestErrorMessageFallsBackToTheCode(t *testing.T) {
	if got, want := (&Error{Code: CodeBusy}).Error(), "busy"; got != want {
		t.Errorf("(&Error{Code: CodeBusy}).Error() = %q, want %q", got, want)
	}
	if got, want := (&Error{Code: CodeBusy, Message: "a turn is running"}).Error(), "a turn is running"; got != want {
		t.Errorf("(&Error{Code: CodeBusy, Message: ...}).Error() = %q, want %q", got, want)
	}
}

// TestErrorRoundTripsAsJSON pins the wire shape: the TUI branches on code and
// renders message, so both keys must be present, in that spelling.
func TestErrorRoundTripsAsJSON(t *testing.T) {
	in := &Error{Code: CodeBusy, Message: "m"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(raw), `{"code":"busy","message":"m"}`; got != want {
		t.Fatalf("marshal = %s, want %s", got, want)
	}

	var out Error
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(&out, in) {
		t.Errorf("round trip = %+v, want %+v", out, *in)
	}
}
