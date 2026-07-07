package agent

import (
	"context"
	"fmt"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func evalRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New()
	fakes := []registry.Tool{
		{Name: "file.read", Risk: registry.RiskReadOnly},
		{Name: "file.write_patch", Risk: registry.RiskWorkspaceWrite},
		{Name: "demo.test", Risk: registry.RiskReadOnly},
	}
	for _, tool := range fakes {
		tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "ok"}, nil
		}
		if err := reg.Register(tool); err != nil {
			t.Fatalf("Register %s: %v", tool.Name, err)
		}
	}
	return reg
}

func evalRead(path string) string {
	return fmt.Sprintf(`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":%q}}}`, path)
}

func TestEvalScenarios(t *testing.T) {
	finalAnswer := `{"rationale":"done","action":{"type":"final","content":"Answer."}}`

	cases := []struct {
		name       string
		responses  []string
		forceClass TaskClass
		maxIters   int
		want       func(t *testing.T, m TurnMetrics)
	}{
		{
			name: "research turn answers after distinct reads",
			responses: []string{
				evalRead("a.go"), evalRead("b.go"), evalRead("c.go"),
				evalRead("d.go"), evalRead("e.go"),
				finalAnswer,
			},
			forceClass: ClassQuestion,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "answered" || m.Iterations != 6 || m.ToolCalls != 5 ||
					m.ParseFailures != 0 || m.SoftStalls != 0 || m.HardStalls != 0 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
		{
			name: "edit turn patches and validates",
			responses: []string{
				"1. Read the file. 2. Patch it. 3. Validate.",
				evalRead("a.go"),
				`{"rationale":"apply","action":{"type":"patch","content":"File: a.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}}`,
				`{"rationale":"validate","action":{"type":"tool_call","tool":"demo.test","args":{}}}`,
				finalAnswer,
			},
			forceClass: ClassEdit,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "answered" || m.ToolCalls != 3 || m.ToolErrors != 0 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
		{
			name:       "parse failure recovers to an answer",
			responses:  []string{"not a json action at all", finalAnswer},
			forceClass: ClassQuestion,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "answered" || m.ParseFailures != 1 || m.ToolCalls != 0 || m.Iterations != 2 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
		{
			name: "exact repeat hard-stalls into salvage",
			responses: []string{
				evalRead("a.go"), evalRead("a.go"), evalRead("a.go"),
				finalAnswer,
			},
			forceClass: ClassQuestion,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "salvaged" || m.SalvageReason != "stalled" || m.HardStalls != 1 || m.ToolCalls != 3 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
		{
			name: "exhaustion salvages with reason exhausted",
			responses: []string{
				evalRead("a.go"), evalRead("b.go"),
				finalAnswer,
			},
			forceClass: ClassQuestion,
			maxIters:   2,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "salvaged" || m.SalvageReason != "exhausted" || m.Iterations != 2 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
		{
			name: "tool error recovers to an answer",
			responses: []string{
				`{"rationale":"r","action":{"type":"tool_call","tool":"missing.tool","args":{}}}`,
				evalRead("a.go"),
				finalAnswer,
			},
			forceClass: ClassQuestion,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "answered" || m.ToolErrors != 1 || m.ToolCalls != 2 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := evalRegistry(t)
			p := &scriptedProvider{responses: tc.responses}
			state := newTestState(t)
			r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
			r.SetForceClass(string(tc.forceClass))
			if tc.maxIters > 0 {
				r.MaxToolIterations = tc.maxIters
			}
			var got *TurnMetrics
			r.MetricsObserver = func(m TurnMetrics) { got = &m }

			if _, err := r.RunTask(context.Background(), "eval goal"); err != nil {
				t.Fatalf("RunTask err = %v", err)
			}
			if got == nil {
				t.Fatal("no TurnMetrics emitted")
			}
			tc.want(t, *got)
		})
	}
}
