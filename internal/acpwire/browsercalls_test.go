package acpwire

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// TestNoteParamsCarryTheNote pins session/note's params: the note rides whole,
// in the shape a note already has.
func TestNoteParamsCarryTheNote(t *testing.T) {
	params := NoteParams{SessionID: "s1", Note: Note{Text: "hi", Role: RoleUser}}

	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal note params: %v", err)
	}
	want := `{"sessionId":"s1","note":{"text":"hi","role":"user"}}`
	if string(raw) != want {
		t.Errorf("note params JSON:\n got: %s\nwant: %s", raw, want)
	}

	var back NoteParams
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal note params: %v", err)
	}
	if !reflect.DeepEqual(back, params) {
		t.Errorf("note params round trip:\n got: %+v\nwant: %+v", back, params)
	}
}

// TestTheExistingMethodsKeepACPsKeys pins the params of the methods ACP already
// spells: a marshal client's call must carry exactly the keys ACP's params
// use, besides sessionId, so one handler serves both sides.
func TestTheExistingMethodsKeepACPsKeys(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  []string
	}{
		{
			name:  "command",
			value: CommandCallParams{SessionID: "s1", Name: "plan", Args: []string{"one"}},
			want:  []string{"args", "name", "sessionId"},
		},
		{
			name:  "memory delete",
			value: MemoryDeleteParams{SessionID: "s1", ID: 7},
			want:  []string{"id", "sessionId"},
		},
		{
			name:  "plugin install",
			value: PluginInstallParams{SessionID: "s1", ScanToken: "t", Scope: "global"},
			want:  []string{"scanToken", "scope", "sessionId"},
		},
		{
			name:  "plugin discard",
			value: PluginDiscardParams{SessionID: "s1", ScanToken: "t"},
			want:  []string{"scanToken", "sessionId"},
		},
		{
			name:  "plugin remove",
			value: PluginRemoveParams{SessionID: "s1", Name: "n", Scope: "project"},
			want:  []string{"name", "scope", "sessionId"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal %s params: %v", tc.name, err)
			}
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(raw, &keys); err != nil {
				t.Fatalf("unmarshal %s params: %v", tc.name, err)
			}
			got := make([]string, 0, len(keys))
			for key := range keys {
				got = append(got, key)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s params keys:\n got: %v\nwant: %v\nJSON: %s", tc.name, got, tc.want, raw)
			}
		})
	}
}

// TestTheBrowserResultsRoundTrip pins that each browser result survives a
// round trip with one element, and that an empty prompt history encodes as the
// empty object.
func TestTheBrowserResultsRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		value any
		into  any
	}{
		{
			name:  "prompt history",
			value: PromptHistoryResult{Prompts: []string{"one"}},
			into:  &PromptHistoryResult{},
		},
		{
			name:  "file index",
			value: FileIndexResult{Paths: []string{"internal/acpwire/browsers.go"}},
			into:  &FileIndexResult{},
		},
		{
			name:  "intelligence",
			value: IntelligenceResult{Checks: []IntelligenceCheck{{Name: "lsp", Status: "ok"}}},
			into:  &IntelligenceResult{},
		},
		{
			name:  "plans",
			value: PlansResult{Plans: []PlanCandidate{{Path: "docs/plan.md", Name: "plan"}}},
			into:  &PlansResult{},
		},
		{
			name:  "diff",
			value: DiffResult{Diff: "@@ -1 +1 @@\n-a\n+b\n"},
			into:  &DiffResult{},
		},
		{
			name:  "memories",
			value: MemoriesResult{Memories: []MemoryEntry{{ID: 1, Kind: "fact", Content: "c"}}},
			into:  &MemoriesResult{},
		},
		{
			name:  "plugins",
			value: PluginsResult{Plugins: []PluginEntry{{Name: "n", Source: "s", Scope: "global"}}},
			into:  &PluginsResult{},
		},
		{
			name:  "skills",
			value: SkillsResult{Skills: []SkillEntry{{Scope: "global", Source: "s", Name: "n"}}},
			into:  &SkillsResult{},
		},
		{
			name:  "skill name",
			value: SkillNameResult{Name: "n"},
			into:  &SkillNameResult{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			if err := json.Unmarshal(raw, tc.into); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.name, err)
			}
			got := reflect.ValueOf(tc.into).Elem().Interface()
			if !reflect.DeepEqual(got, tc.value) {
				t.Errorf("%s round trip:\n got: %+v\nwant: %+v\nJSON: %s", tc.name, got, tc.value, raw)
			}
		})
	}

	empty, err := json.Marshal(PromptHistoryResult{})
	if err != nil {
		t.Fatalf("marshal empty prompt history: %v", err)
	}
	if string(empty) != `{}` {
		t.Errorf("empty prompt history JSON: got %s, want {}", empty)
	}
}
