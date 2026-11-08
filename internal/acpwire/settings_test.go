package acpwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The config mirror's update carries the whole value as raw JSON: the client
// decodes it into config.Config and config.Layers on its own side.
func TestSessionConfigUpdateRoundTrips(t *testing.T) {
	want := SessionConfigUpdate{
		Kind: UpdateSessionConfig,
		Config: SessionConfig{
			Config: json.RawMessage(`{"agent":{"mode":"act"}}`),
			Layers: json.RawMessage(`{"Merged":{"agent":{"mode":"act"}}}`),
			Tools:  []string{"file.read", "file.write"},
		},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	var got SessionConfigUpdate
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal update: %v", err)
	}
	if got.Kind != "session_config" {
		t.Fatalf("kind = %q, want session_config", got.Kind)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-tripped update differs\n got: %+v\nwant: %+v", got, want)
	}
}

// A reload's error travels in the result beside the flag: a JSON-RPC error
// response could not carry both.
func TestAReloadResultCarriesItsError(t *testing.T) {
	want := ReloadResult{
		Reloaded: true,
		Error:    &Error{Code: CodeKeyringUnavailable, Message: "m"},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var got ReloadResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-tripped result differs\n got: %+v\nwant: %+v", got, want)
	}
}
