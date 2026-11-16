package acpwire

import (
	"encoding/json"
	"testing"
)

// TestRestartingParamsRoundTrip pins the bytes marshal/restarting carries: the
// reason is always sent, under that key, and a restart that brings no new build
// leaves version out.
func TestRestartingParamsRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		value RestartingParams
		want  string
	}{
		{
			name:  "a restart that brings a build",
			value: RestartingParams{Reason: "upgrade", Version: "1.2.3"},
			want:  `{"reason":"upgrade","version":"1.2.3"}`,
		},
		{
			name:  "a restart that brings no build",
			value: RestartingParams{Reason: "upgrade"},
			want:  `{"reason":"upgrade"}`,
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

			var back RestartingParams
			if err := json.Unmarshal(got, &back); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.name, err)
			}
			if back != tc.value {
				t.Errorf("%s round trip:\n got: %+v\nwant: %+v", tc.name, back, tc.value)
			}
		})
	}
}
