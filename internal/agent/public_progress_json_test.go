package agent

import (
	"errors"
	"strings"
	"testing"
)

func TestParseActionWithProgressPreservesActionShapes(t *testing.T) {
	tests := []struct {
		name, raw string
		check     func(ModelAction) bool
	}{
		{"single", `{"action":{"type":"tool_call","tool":"file.read","args":{"path":"a"}},"progress":{"mode":"begin","headline":"Read file"}}`, func(a ModelAction) bool { return a.Type == ActionToolCall && a.Tool == "file.read" }},
		{"readonly batch", `{"actions":[{"type":"tool_call","tool":"file.read"},{"type":"tool_call","tool":"file.read"}],"progress":{"mode":"begin","headline":"Inspect files"}}`, func(a ModelAction) bool { return len(a.Actions) == 2 && a.Type == "" }},
		{"final", `{"action":{"type":"final","content":"done"},"progress":{"mode":"begin","headline":"Finish"}}`, func(a ModelAction) bool { return a.Type == ActionFinal && a.Content == "done" }},
		{"question", `{"action":{"type":"question.ask","questions":[{"question":"Which?"}]},"progress":{"mode":"begin","headline":"Clarify"}}`, func(a ModelAction) bool { return a.Type == ActionQuestionAsk && len(a.Questions) == 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := ParseAction(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if !tt.check(a) || a.Progress == nil || a.ProgressDiagnostic != "" {
				t.Fatalf("action=%+v", a)
			}
		})
	}
}

func TestOptionalProgressFailuresDoNotDiscardValidAction(t *testing.T) {
	invalid := []struct{ name, progress string }{
		{"wrong type", `[]`},
		{"oversize", `{"mode":"begin","headline":"` + strings.Repeat("x", 13000) + `"}`},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"action":{"type":"tool_call","tool":"file.read","args":{"path":"a"}},"progress":` + tt.progress + `}`
			a, err := ParseAction(raw)
			if err != nil {
				t.Fatalf("valid action rejected: %v", err)
			}
			if a.Type != ActionToolCall || a.Tool != "file.read" || a.Progress != nil || a.ProgressDiagnostic == "" {
				t.Fatalf("action=%+v", a)
			}
			if len(a.ProgressDiagnostic) > 240 {
				t.Fatalf("diagnostic too long: %d", len(a.ProgressDiagnostic))
			}
		})
	}
}

func TestProgressOnlyEnvelopeStillHasNoAction(t *testing.T) {
	_, err := ParseAction(`{"progress":{"mode":"begin","headline":"Working"}}`)
	if !errors.Is(err, ErrUnknownActionType) {
		t.Fatalf("err=%v, want missing action failure", err)
	}
}

func TestProgressUpdateIsNotAnAction(t *testing.T) {
	for _, raw := range []string{
		`{"action":{"type":"progress.update","args":{"mode":"begin"}}}`,
		`{"actions":[{"type":"progress.update","args":{"mode":"begin"}}]}`,
	} {
		if _, err := ParseAction(raw); !errors.Is(err, ErrUnknownActionType) {
			t.Fatalf("err=%v", err)
		}
	}
}

func TestOptionalProgressDoesNotChangeLegacyEnvelope(t *testing.T) {
	legacy, err := ParseAction(`{"rationale":"private","action":{"type":"final","content":"done"}}`)
	if err != nil {
		t.Fatal(err)
	}
	withProgress, err := ParseAction(`{"rationale":"private","action":{"type":"final","content":"done"},"progress":{"mode":"begin","headline":"Finish"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Type != withProgress.Type || legacy.Content != withProgress.Content || legacy.Rationale != withProgress.Rationale || len(legacy.Actions) != len(withProgress.Actions) {
		t.Fatalf("legacy=%+v with progress=%+v", legacy, withProgress)
	}
	if legacy.Progress != nil || legacy.ProgressDiagnostic != "" || withProgress.Progress == nil {
		t.Fatal("unexpected progress metadata")
	}
}
