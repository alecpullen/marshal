package acpwire

import (
	"encoding/json"
	"testing"
	"time"
)

// TestSessionSheetJSON pins the bytes Session.Sheet returns and
// Session.RefreshSheet takes: a sheet with every field set, the zero sheet and
// a refresh with every flag set must each encode exactly as the contract
// spells them.
func TestSessionSheetJSON(t *testing.T) {
	full := SessionSheet{
		SubagentID:  7,
		StartedAt:   Millis(time.Unix(1, 0)),
		Context:     SheetContext{MaxTokens: 128000, EstimatedTokens: 14000, Sections: []SheetContextSection{{Title: "system", EstimatedTokens: 2100}}},
		Changed:     []SheetChangedFile{{Path: "a.go", Status: "M", Added: 3, Removed: 1}},
		Turns:       []SheetTurn{{StartedAt: Millis(time.Unix(2, 0)), PromptTokens: 100, CompletionTokens: 20, CacheReadTokens: 50}},
		Totals:      SheetTotals{Turns: 4, PromptTokens: 400, CompletionTokens: 80, CacheReadTokens: 200},
		Fleet:       []SheetFleetRow{{Branch: "feat/x", Ahead: 2, Behind: 1, Dirty: true, AgeMs: 60000}},
		Repo:        SheetRepo{Files: 412, Symbols: 3100},
		Tools:       []SheetTool{{Name: "file.read", Calls: 2, Errors: 1, SlowestMs: 20}},
		Files:       []SheetFile{{Path: "a.go", Edits: 1, Reads: 2, LastAt: Millis(time.Unix(3, 0))}},
		Rules:       []string{"go test"},
		Skills:      []string{"tdd"},
		ToolBudget:  SheetToolBudget{Used: 7, Max: 100},
		LoadedTools: 3,
		Sandbox:     SheetSandbox{Backend: "restricted", AllowNetwork: true},
	}

	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "every field set",
			value: full,
			want:  `{"subagentId":7,"startedAt":1000,"context":{"maxTokens":128000,"estimatedTokens":14000,"sections":[{"title":"system","estimatedTokens":2100}]},"changed":[{"path":"a.go","status":"M","added":3,"removed":1}],"turns":[{"startedAt":2000,"promptTokens":100,"completionTokens":20,"cacheReadTokens":50}],"totals":{"turns":4,"promptTokens":400,"completionTokens":80,"cacheReadTokens":200},"fleet":[{"branch":"feat/x","ahead":2,"behind":1,"dirty":true,"ageMs":60000}],"repo":{"files":412,"symbols":3100},"tools":[{"name":"file.read","calls":2,"errors":1,"slowestMs":20}],"files":[{"path":"a.go","edits":1,"reads":2,"lastAt":3000}],"rules":["go test"],"skills":["tdd"],"toolBudget":{"used":7,"max":100},"loadedTools":3,"sandbox":{"backend":"restricted","allowNetwork":true}}`,
		},
		{
			name:  "zero sheet",
			value: SessionSheet{},
			want:  `{"context":{},"totals":{},"repo":{},"toolBudget":{},"sandbox":{}}`,
		},
		{
			name:  "every refresh flag set",
			value: SheetRefresh{Rebase: true, Changed: true, Stats: true, Fleet: true},
			want:  `{"rebase":true,"changed":true,"stats":true,"fleet":true}`,
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
