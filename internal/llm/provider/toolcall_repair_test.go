package provider

import (
	"encoding/json"
	"testing"

	"marshal/internal/llm/schema"
)

func TestRepairToolCalls_PassthroughValidSingleObject(t *testing.T) {
	in := []schema.ToolCall{{ID: "call_1", Name: "file.read", Args: json.RawMessage(`{"path":"a.go"}`)}}
	out, repaired := repairToolCalls(in)
	if repaired {
		t.Error("expected no repair for a valid single object")
	}
	if len(out) != 1 || string(out[0].Args) != `{"path":"a.go"}` {
		t.Fatalf("out = %+v", out)
	}
}

func TestRepairToolCalls_SplitsConcatenatedObjects(t *testing.T) {
	in := []schema.ToolCall{{ID: "call_1", Name: "file.read", Args: json.RawMessage(`{"path":"a.go"}{"path":"b.go"}`)}}
	out, repaired := repairToolCalls(in)
	if !repaired {
		t.Fatal("expected repair for concatenated objects")
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2: %+v", len(out), out)
	}
	if string(out[0].Args) != `{"path":"a.go"}` {
		t.Errorf("out[0].Args = %s", out[0].Args)
	}
	if string(out[1].Args) != `{"path":"b.go"}` {
		t.Errorf("out[1].Args = %s", out[1].Args)
	}
	if out[0].ID != "call_1" {
		t.Errorf("out[0].ID = %q, want call_1", out[0].ID)
	}
	if out[1].ID != "call_1_r1" {
		t.Errorf("out[1].ID = %q, want call_1_r1", out[1].ID)
	}
}

func TestRepairToolCalls_PassthroughUnrecoverable(t *testing.T) {
	in := []schema.ToolCall{{ID: "call_1", Name: "file.read", Args: json.RawMessage(`not json`)}}
	out, repaired := repairToolCalls(in)
	if repaired {
		t.Error("expected no repair for unrecoverable arguments")
	}
	if len(out) != 1 || string(out[0].Args) != "not json" {
		t.Fatalf("out = %+v", out)
	}
}

func TestRepairToolCalls_MixedBatch(t *testing.T) {
	in := []schema.ToolCall{
		{ID: "call_1", Name: "file.read", Args: json.RawMessage(`{"path":"a.go"}{"path":"b.go"}`)},
		{ID: "call_2", Name: "shell.run", Args: json.RawMessage(`{"command":"go test"}`)},
	}
	out, repaired := repairToolCalls(in)
	if !repaired {
		t.Fatal("expected repair for mixed batch")
	}
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3: %+v", len(out), out)
	}
	if out[0].Name != "file.read" || string(out[0].Args) != `{"path":"a.go"}` {
		t.Errorf("out[0] = %+v", out[0])
	}
	if out[1].Name != "file.read" || string(out[1].Args) != `{"path":"b.go"}` {
		t.Errorf("out[1] = %+v", out[1])
	}
	if out[2].Name != "shell.run" || string(out[2].Args) != `{"command":"go test"}` {
		t.Errorf("out[2] = %+v", out[2])
	}
}

func TestRepairToolCalls_EmptyArgsUnchanged(t *testing.T) {
	in := []schema.ToolCall{{ID: "call_1", Name: "repo.index", Args: json.RawMessage(``)}}
	out, repaired := repairToolCalls(in)
	if repaired {
		t.Error("expected no repair for empty args")
	}
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
}

func TestSplitJSONObjects(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"single", `{"a":1}`, []string{`{"a":1}`}},
		{"two", `{"a":1}{"b":2}`, []string{`{"a":1}`, `{"b":2}`}},
		{"with whitespace", `{"a":1} {"b":2}`, []string{`{"a":1}`, `{"b":2}`}},
		{"nested", `{"a":{"b":1}}{"c":[2]}`, []string{`{"a":{"b":1}}`, `{"c":[2]}`}},
		{"invalid", `not json`, nil},
		{"empty", "", nil},
		{"trailing garbage", `{"a":1} trailing`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitJSONObjects(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("obj[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
