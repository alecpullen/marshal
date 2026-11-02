package acpwire

import (
	"encoding/json"
	"testing"
)

// TestInteractionPatchJSON pins the bytes Session.SetInteraction takes: a
// patch that sets all three fields, the empty patch, and a patch that revokes
// system access alone, which must not encode like leaving it alone.
func TestInteractionPatchJSON(t *testing.T) {
	mode := "act"
	no := false
	yes := true

	cases := []struct {
		name  string
		value InteractionPatch
		want  string
	}{
		{
			name:  "mode act with auto revoked and system granted",
			value: InteractionPatch{Mode: &mode, Auto: &no, System: &yes},
			want:  `{"mode":"act","auto":false,"system":true}`,
		},
		{
			name:  "empty patch",
			value: InteractionPatch{},
			want:  `{}`,
		},
		{
			name:  "system revoked alone",
			value: InteractionPatch{System: &no},
			want:  `{"system":false}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			if string(got) != tc.want {
				t.Errorf("%s JSON:\n got: %s\nwant: %s", tc.name, got, tc.want)
			}
		})
	}
}
