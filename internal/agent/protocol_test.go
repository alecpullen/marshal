package agent

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseActionAnswer(t *testing.T) {
	raw := `{"rationale":"straightforward question","action":{"type":"answer","content":"Marshal is a TUI coding agent."}}`

	action, err := ParseAction(raw)
	if err != nil {
		t.Fatalf("ParseAction returned error: %v", err)
	}
	if action.Type != ActionAnswer {
		t.Fatalf("Type = %q, want %q", action.Type, ActionAnswer)
	}
	if action.Content != "Marshal is a TUI coding agent." {
		t.Fatalf("Content = %q, want the answer text", action.Content)
	}
	if action.Rationale != "straightforward question" {
		t.Fatalf("Rationale = %q, want %q", action.Rationale, "straightforward question")
	}
}

func TestParseActionToolCall(t *testing.T) {
	raw := `{"rationale":"need to see the file","action":{"type":"tool_call","tool":"file.read","args":{"path":"main.go"}}}`

	action, err := ParseAction(raw)
	if err != nil {
		t.Fatalf("ParseAction returned error: %v", err)
	}
	if action.Type != ActionToolCall {
		t.Fatalf("Type = %q, want %q", action.Type, ActionToolCall)
	}
	if action.Tool != "file.read" {
		t.Fatalf("Tool = %q, want %q", action.Tool, "file.read")
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(action.Args, &args); err != nil {
		t.Fatalf("Args did not decode: %v", err)
	}
	if args.Path != "main.go" {
		t.Fatalf("args.Path = %q, want %q", args.Path, "main.go")
	}
}

func TestParseActionStripsMarkdownFence(t *testing.T) {
	raw := "```json\n{\"rationale\":\"r\",\"action\":{\"type\":\"final\",\"content\":\"done\"}}\n```"

	action, err := ParseAction(raw)
	if err != nil {
		t.Fatalf("ParseAction returned error: %v", err)
	}
	if action.Type != ActionFinal || action.Content != "done" {
		t.Fatalf("action = %#v, want type=final content=done", action)
	}
}

func TestParseActionRejectsNoJSONObject(t *testing.T) {
	_, err := ParseAction("I think the answer is 42.")
	if !errors.Is(err, ErrNoActionFound) {
		t.Fatalf("err = %v, want ErrNoActionFound", err)
	}
}

func TestParseActionRejectsUnknownType(t *testing.T) {
	_, err := ParseAction(`{"rationale":"r","action":{"type":"guess"}}`)
	if !errors.Is(err, ErrUnknownActionType) {
		t.Fatalf("err = %v, want ErrUnknownActionType", err)
	}
}

func TestParseActionRejectsToolCallMissingTool(t *testing.T) {
	_, err := ParseAction(`{"rationale":"r","action":{"type":"tool_call","args":{}}}`)
	if !errors.Is(err, ErrMissingTool) {
		t.Fatalf("err = %v, want ErrMissingTool", err)
	}
}

func TestParseActionRejectsMalformedJSON(t *testing.T) {
	_, err := ParseAction(`{"rationale": "r", "action": {`)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestParseActionAcceptsActionsArray(t *testing.T) {
	raw := `{"rationale":"read two files","actions":[{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}},{"type":"tool_call","tool":"file.read","args":{"path":"b.go"}}]}`

	action, err := ParseAction(raw)
	if err != nil {
		t.Fatalf("ParseAction returned error: %v", err)
	}
	if len(action.Actions) != 2 {
		t.Fatalf("len(Actions) = %d, want 2", len(action.Actions))
	}
	if action.Actions[0].Tool != "file.read" {
		t.Fatalf("first tool = %q, want file.read", action.Actions[0].Tool)
	}
	if action.Type != "" {
		t.Fatalf("single-action Type should be empty when Actions is set, got %q", action.Type)
	}
}

func TestParseActionRejectsActionsWithMissingTool(t *testing.T) {
	raw := `{"rationale":"bad","actions":[{"type":"tool_call","args":{}}]}`

	_, err := ParseAction(raw)
	if !errors.Is(err, ErrMissingTool) {
		t.Fatalf("err = %v, want ErrMissingTool", err)
	}
}

func TestParseActionBackwardCompatibleWithSingleAction(t *testing.T) {
	raw := `{"rationale":"r","action":{"type":"final","content":"done"}}`

	action, err := ParseAction(raw)
	if err != nil {
		t.Fatalf("ParseAction returned error: %v", err)
	}
	if action.Type != ActionFinal || action.Content != "done" {
		t.Fatalf("action = %#v", action)
	}
	if len(action.Actions) != 0 {
		t.Fatalf("Actions should be empty for single-action envelope")
	}
}
