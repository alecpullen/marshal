package acpwire

import (
	"encoding/json"
	"testing"
)

// TestTurnsJSON pins the bytes the turn calls carry: one prompt, its result,
// a review's parameters, an agent run's, and the verify proposal. Each must
// encode exactly as the contract spells it.
func TestTurnsJSON(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "prompt",
			value: PromptParams{Text: "fix the build"},
			want:  `{"text":"fix the build"}`,
		},
		{
			name:  "queued prompt",
			value: PromptParams{Text: "and the tests", Queued: true},
			want:  `{"text":"and the tests","queued":true}`,
		},
		{
			name:  "cancelled result",
			value: PromptResult{StopReason: StopReasonCancelled},
			want:  `{"stopReason":"cancelled"}`,
		},
		{
			name:  "review with a base",
			value: ReviewParams{Focus: "main.go", Base: "main"},
			want:  `{"focus":"main.go","base":"main"}`,
		},
		{
			name:  "review with nothing named",
			value: ReviewParams{},
			want:  `{}`,
		},
		{
			name:  "agent run with a goal",
			value: AgentRunParams{Agent: "linter", Goal: "check style"},
			want:  `{"agent":"linter","goal":"check style"}`,
		},
		{
			name:  "agent run without a goal",
			value: AgentRunParams{Agent: "linter"},
			want:  `{"agent":"linter"}`,
		},
		{
			name:  "verify proposal",
			value: VerifyCommands{Build: "go build ./...", Test: "go test ./..."},
			want:  `{"build":"go build ./...","test":"go test ./..."}`,
		},
		{
			name:  "verify proposal with nothing",
			value: VerifyCommands{},
			want:  `{}`,
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
