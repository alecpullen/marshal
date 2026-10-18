package registry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolNoticeJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		notice ToolNotice
	}{
		{
			name: "oversize fallback",
			notice: ToolNotice{
				Kind: NoticeOversizeFallback,
				Text: "file is large; showing the head of the file",
				Data: map[string]any{
					"bytes":     int(12345),
					"path":      "internal/tools/registry/types.go",
					"truncated": true,
				},
			},
		},
		{
			name: "slice truncated",
			notice: ToolNotice{
				Kind: NoticeSliceTruncated,
				Text: "results were truncated to the requested limit",
				Data: map[string]any{
					"limit":  int(100),
					"reason": "max_results",
				},
			},
		},
		{
			name: "zero match coach",
			notice: ToolNotice{
				Kind: NoticeZeroMatchCoach,
				Text: "no matches; try broadening your search",
				Data: map[string]any{
					"pattern": "func.*Foo",
					"count":   int(0),
				},
			},
		},
		{
			name: "capped results",
			notice: ToolNotice{
				Kind: NoticeCappedResults,
				Text: "result set was capped",
				Data: map[string]any{
					"cap":  int(500),
					"kept": int(500),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.notice)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var got ToolNotice
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if got.Kind != tt.notice.Kind {
				t.Errorf("Kind = %q, want %q", got.Kind, tt.notice.Kind)
			}
			if got.Text != tt.notice.Text {
				t.Errorf("Text = %q, want %q", got.Text, tt.notice.Text)
			}
			if len(got.Data) != len(tt.notice.Data) {
				t.Fatalf("Data len = %d, want %d", len(got.Data), len(tt.notice.Data))
			}
			for k, want := range tt.notice.Data {
				gotVal, ok := got.Data[k]
				if !ok {
					t.Errorf("Data missing key %q", k)
					continue
				}
				// JSON numbers round-trip as float64; compare ints numerically.
				if wantInt, ok := want.(int); ok {
					gotFloat, ok := gotVal.(float64)
					if !ok {
						t.Errorf("Data[%q] = %#v, want numeric", k, gotVal)
						continue
					}
					if int(gotFloat) != wantInt {
						t.Errorf("Data[%q] = %v, want %d", k, gotFloat, wantInt)
					}
					continue
				}
				if gotVal != want {
					t.Errorf("Data[%q] = %#v, want %#v", k, gotVal, want)
				}
			}
		})
	}
}

func TestSubagentWorkReferenceNoticeRoundTrip(t *testing.T) {
	want := SubagentWorkReference{
		TaskKey:        "work:abc",
		RuntimeEpoch:   "epoch-1",
		AgentID:        12,
		ChildSessionID: "child-9",
	}

	valueCases := []struct {
		name  string
		value any
	}{
		{"struct", want},
		{"pointer", &want},
		{"map", map[string]any{
			"taskKey":        "work:abc",
			"runtimeEpoch":   "epoch-1",
			"agentId":        float64(12),
			"childSessionId": "child-9",
		}},
	}
	for _, tc := range valueCases {
		t.Run(tc.name, func(t *testing.T) {
			n := &ToolNotice{Kind: NoticeCommandExecution, Data: map[string]any{NoticeSubagentWork: tc.value}}
			got, ok := n.SubagentWork()
			if !ok {
				t.Fatalf("SubagentWork() ok = false for %s form", tc.name)
			}
			if got != want {
				t.Errorf("SubagentWork() = %#v, want %#v", got, want)
			}
		})
	}

	// A persisted notice decodes its reference back through the JSON map path.
	raw, err := json.Marshal(ToolNotice{
		Kind: NoticeCommandExecution,
		Data: map[string]any{NoticeSubagentWork: want},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ToolNotice
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := back.SubagentWork()
	if !ok {
		t.Fatal("SubagentWork() after JSON round trip ok = false")
	}
	if got != want {
		t.Errorf("SubagentWork() = %#v, want %#v", got, want)
	}

	// An optional child session is omitted, not fabricated.
	minimal := SubagentWorkReference{TaskKey: "work:abc", RuntimeEpoch: "epoch-1", AgentID: 1}
	gotMin, ok := (&ToolNotice{Data: map[string]any{NoticeSubagentWork: minimal}}).SubagentWork()
	if !ok || gotMin != minimal {
		t.Errorf("minimal reference = %#v, %v; want %#v, true", gotMin, ok, minimal)
	}

	bad := []struct {
		name   string
		notice *ToolNotice
	}{
		{"nil notice", nil},
		{"empty data", &ToolNotice{Kind: NoticeCommandExecution}},
		{"missing key", &ToolNotice{Data: map[string]any{"other": 1}}},
		{"nil value", &ToolNotice{Data: map[string]any{NoticeSubagentWork: nil}}},
		{"nil pointer", &ToolNotice{Data: map[string]any{NoticeSubagentWork: (*SubagentWorkReference)(nil)}}},
		{"unrecognised shape", &ToolNotice{Data: map[string]any{NoticeSubagentWork: "nope"}}},
		{"empty task key", &ToolNotice{Data: map[string]any{NoticeSubagentWork: SubagentWorkReference{RuntimeEpoch: "e", AgentID: 1}}}},
		{"empty runtime epoch", &ToolNotice{Data: map[string]any{NoticeSubagentWork: SubagentWorkReference{TaskKey: "t", AgentID: 1}}}},
		{"zero agent id", &ToolNotice{Data: map[string]any{NoticeSubagentWork: SubagentWorkReference{TaskKey: "t", RuntimeEpoch: "e"}}}},
		{"map without identity", &ToolNotice{Data: map[string]any{NoticeSubagentWork: map[string]any{"taskKey": "", "runtimeEpoch": "e", "agentId": float64(1)}}}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := tc.notice.SubagentWork(); ok {
				t.Errorf("SubagentWork() ok = true for %s, want false", tc.name)
			}
		})
	}
}

func TestToolNoticeNilDataOmitempty(t *testing.T) {
	raw, err := json.Marshal(ToolNotice{Kind: NoticeZeroMatchCoach, Text: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "\"data\"") {
		t.Errorf("marshalled JSON unexpectedly contains \"data\" key: %s", raw)
	}
}

func TestToolResultNoticeDefaultsNil(t *testing.T) {
	var res ToolResult
	if res.Notice != nil {
		t.Errorf("zero-value ToolResult.Notice = %#v, want nil", res.Notice)
	}

	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Notice is a bare (untagged) exported field, so encoding/json always
	// emits its key; a nil Notice therefore marshals as JSON null rather
	// than being omitted.
	if !strings.Contains(string(raw), "\"Notice\":null") {
		t.Errorf("marshalled ToolResult = %s, want it to contain \"Notice\":null", raw)
	}
}
