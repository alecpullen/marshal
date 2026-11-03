package acpwire

import (
	"encoding/json"
	"testing"
)

// TestEndingJSON pins the bytes the end calls carry: the work an end would
// stop, one acceptance's parameters, and an acceptance's outcome. Each must
// encode exactly as the contract spells it.
func TestEndingJSON(t *testing.T) {
	work := EndWork{Items: []EndWorkItem{
		{ID: "op-1", Label: "agent turn"},
		{ID: "cmd-2", Label: "go test ./...", Group: "op-1"},
	}}
	workJSON := `{"items":[{"id":"op-1","label":"agent turn"},{"id":"cmd-2","label":"go test ./...","group":"op-1"}]}`

	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "work",
			value: work,
			want:  workJSON,
		},
		{
			name:  "work with nothing to stop",
			value: EndWork{},
			want:  `{}`,
		},
		{
			name: "acceptance with the displayed work",
			value: EndAcceptParams{
				Intent:           EndIntentExit,
				DisplayedIDs:     []string{"op-1", "cmd-2"},
				RequireDisplayed: true,
			},
			want: `{"intent":"exit","displayedIds":["op-1","cmd-2"],"requireDisplayed":true}`,
		},
		{
			name:  "acceptance of a replacement",
			value: EndAcceptParams{Intent: EndIntentReplace},
			want:  `{"intent":"replace"}`,
		},
		{
			name:  "decision that asks about expanded work",
			value: EndDecision{Expanded: []string{"cmd-3"}, Work: work},
			want:  `{"expanded":["cmd-3"],"work":` + workJSON + `}`,
		},
		{
			name:  "accepted decision",
			value: EndDecision{Accepted: true},
			want:  `{"accepted":true,"work":{}}`,
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
