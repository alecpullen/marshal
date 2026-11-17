package contract

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestTheAnswerBodiesKeepTheirBytes pins the answer bodies' wire form: moving
// the declarations into the contract must not change one byte of what the
// bridge's handlers decoded and the agent received.
func TestTheAnswerBodiesKeepTheirBytes(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			name: "a decision with an edit and a typed choice",
			value: Decision{
				Approved: true,
				Edited:   "rm -rf ./build",
				ModeAccess: &ModeAccessChoice{
					RequestID: "r-1",
					SessionID: "s-1",
					Current:   AccessState{Mode: "ask", Revision: 2},
					Mode:      "act",
				},
			},
			want: `{"approved":true,"edited":"rm -rf ./build","modeAccess":{"requestId":"r-1","sessionId":"s-1","current":{"mode":"ask","auto":false,"system_access":false,"access_revision":2},"mode":"act"}}`,
		},
		{
			name:  "a declined answer",
			value: Answers{Declined: true},
			want:  `{"declined":true}`,
		},
		{
			name: "a set of answers, in order",
			value: Answers{Answers: []Answer{
				{Question: "Which branch?", Answer: "main"},
				{Question: "Squash?", Answer: "yes"},
			}},
			want: `{"answers":[{"question":"Which branch?","answer":"main"},{"question":"Squash?","answer":"yes"}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("encoded bytes changed:\n got: %s\nwant: %s", got, tc.want)
			}
			// The handlers decoded bodies written by this shape, so the same
			// bytes must decode into the contract type and back out again.
			fresh := reflect.New(reflect.TypeOf(tc.value))
			if err := json.Unmarshal([]byte(tc.want), fresh.Interface()); err != nil {
				t.Fatalf("Unmarshal(%s): %v", tc.want, err)
			}
			back, err := json.Marshal(fresh.Elem().Interface())
			if err != nil {
				t.Fatalf("Marshal after decode: %v", err)
			}
			if string(back) != tc.want {
				t.Fatalf("re-encoded bytes changed:\n got: %s\nwant: %s", back, tc.want)
			}
		})
	}
}
