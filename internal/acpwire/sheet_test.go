package acpwire

import (
	"encoding/json"
	"reflect"
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

// TestSheetUpdateRoundTrips pins the sheet update: the subscription's ID
// travels beside the sheet, which carries the reading subagent's own ID.
func TestSheetUpdateRoundTrips(t *testing.T) {
	update := SheetUpdate{
		Kind:       UpdateSheet,
		SubagentID: 7,
		Sheet: SessionSheet{
			Tools:   []SheetTool{{Name: "file.read", Calls: 2}},
			Files:   []SheetFile{{Path: "a.go", Reads: 1}},
			Context: SheetContext{MaxTokens: 128000, Sections: []SheetContextSection{{Title: "system"}}},
		},
	}
	raw, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := update.Kind; got != "sheet" {
		t.Errorf("Kind = %q, want %q", got, "sheet")
	}
	var decoded SheetUpdate
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, update) {
		t.Errorf("round trip =\n%+v\nwant\n%+v", decoded, update)
	}
}

// TestSheetParamsOmitAnAbsentRefresh pins the refresh pointer: a read that
// re-reads nothing carries no refresh key at all.
func TestSheetParamsOmitAnAbsentRefresh(t *testing.T) {
	without, err := json.Marshal(SheetParams{SessionID: "s1"})
	if err != nil {
		t.Fatalf("marshal without refresh: %v", err)
	}
	if want := `{"sessionId":"s1"}`; string(without) != want {
		t.Errorf("SheetParams JSON =\n%s\nwant\n%s", without, want)
	}
	with, err := json.Marshal(SheetParams{SessionID: "s1", Refresh: &SheetRefresh{Stats: true}})
	if err != nil {
		t.Fatalf("marshal with refresh: %v", err)
	}
	if want := `{"sessionId":"s1","refresh":{"stats":true}}`; string(with) != want {
		t.Errorf("SheetParams JSON =\n%s\nwant\n%s", with, want)
	}
}
