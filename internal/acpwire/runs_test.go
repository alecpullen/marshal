package acpwire

import (
	"encoding/json"
	"testing"
)

// TestSubagentDrillJSON pins the bytes a subagent view's drill encodes to: a
// drillable view with every drill field set, and a view with no drill.
func TestSubagentDrillJSON(t *testing.T) {
	drilled := Subagent{
		ID:     3,
		Label:  "scout",
		Status: SubagentRunning,
		Drill: &SubagentDrill{
			ActiveRoot:  "/repo/wt",
			ActiveRoute: Route{Provider: "local", Model: "qwen3-coder", Active: true},
			Activity:    Activity{Kind: "reconnecting", Label: "retrying in 2s", StartedAt: 1700000000000},
		},
	}

	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "every drill field set",
			value: drilled,
			want:  `{"id":3,"label":"scout","status":"running","drill":{"activeRoot":"/repo/wt","activeRoute":{"provider":"local","model":"qwen3-coder","active":true},"activity":{"kind":"reconnecting","label":"retrying in 2s","startedAt":1700000000000}}}`,
		},
		{
			name:  "no drill",
			value: Subagent{ID: 4, Label: "sdd task card", Status: SubagentDone},
			want:  `{"id":4,"label":"sdd task card","status":"done"}`,
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
