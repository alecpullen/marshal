package acpwire

import (
	"encoding/json"
	"testing"
)

// TestPipelinesJSON pins the bytes the pipeline calls carry: a swarm's
// parameters, a plan run's, a preflight's parameters and result, an inspection,
// a diagnostic, and an authoring run's parameters and result. Each must encode
// exactly as the contract spells it.
func TestPipelinesJSON(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "swarm",
			value: SwarmParams{Goal: "add a regression test"},
			want:  `{"goal":"add a regression test"}`,
		},
		{
			name: "swarm with overrides",
			value: SwarmParams{
				Goal:      "add a regression test",
				Overrides: map[string]string{"planner": "local/qwen"},
			},
			want: `{"goal":"add a regression test","overrides":{"planner":"local/qwen"}}`,
		},
		{
			name: "plan run with everything",
			value: SDDParams{
				PlanPath:  "docs/plans/p.md",
				Strategy:  StrategyAdaptive,
				Overrides: map[string]string{"sdd_implementer": "kimi/k3"},
				Verify:    &VerifyCommands{Build: "make build", Test: "make test"},
			},
			want: `{"planPath":"docs/plans/p.md","strategy":"adaptive","overrides":{"sdd_implementer":"kimi/k3"},"verify":{"build":"make build","test":"make test"}}`,
		},
		{
			name:  "plan run with the path only",
			value: SDDParams{PlanPath: "docs/plans/p.md"},
			want:  `{"planPath":"docs/plans/p.md"}`,
		},
		{
			name:  "preflight parameters",
			value: PlanPreflightParams{PlanPath: "docs/plans/p.md", Strategy: StrategyStrict},
			want:  `{"planPath":"docs/plans/p.md","strategy":"strict"}`,
		},
		{
			name: "preflight with an inspection and a gate",
			value: PlanPreflight{
				Inspection: PlanInspection{
					Strategy:  StrategyAdaptive,
					HasBlocks: true,
					DetOps:    1,
					Tasks:     []PlanTask{{TaskN: 1, Title: "Add file", DetOps: 1}},
				},
				Verify: VerifyGate{
					Build:    "go build ./...",
					Test:     "go test ./...",
					Known:    true,
					Detected: true,
				},
			},
			want: `{"inspection":{"strategy":"adaptive","hasBlocks":true,"detOps":1,"tasks":[{"taskN":1,"title":"Add file","detOps":1}]},"verify":{"build":"go build ./...","test":"go test ./...","known":true,"detected":true}}`,
		},
		{
			name:  "preflight with nothing resolved",
			value: PlanPreflight{Inspection: PlanInspection{Strategy: StrategyAgent}},
			want:  `{"inspection":{"strategy":"agent"},"verify":{}}`,
		},
		{
			name: "diagnostic",
			value: PlanDiagnostic{
				TaskN:    2,
				Severity: DiagnosticError,
				Message:  "unknown block",
				Line:     12,
			},
			want: `{"taskN":2,"severity":"error","message":"unknown block","line":12}`,
		},
		{
			name:  "authoring parameters",
			value: AuthorPlanParams{Goal: "add retries"},
			want:  `{"goal":"add retries"}`,
		},
		{
			name: "authoring parameters with a design",
			value: AuthorPlanParams{
				Goal:   "Convert the approved plan into an executable SDD plan.",
				Design: "approved plan content",
			},
			want: `{"goal":"Convert the approved plan into an executable SDD plan.","design":"approved plan content"}`,
		},
		{
			name: "authored plan",
			value: AuthoredPlan{
				StopReason: StopReasonEndTurn,
				PlanPath:   "/repo/.marshal/plans/add-retries.md",
				Inspection: &PlanInspection{Strategy: StrategyAgent},
			},
			want: `{"stopReason":"end_turn","planPath":"/repo/.marshal/plans/add-retries.md","inspection":{"strategy":"agent"}}`,
		},
		{
			name:  "authored plan that was stopped",
			value: AuthoredPlan{StopReason: StopReasonCancelled},
			want:  `{"stopReason":"cancelled"}`,
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
