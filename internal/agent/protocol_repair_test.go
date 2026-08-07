package agent

import (
	"strings"
	"testing"
)

func knownToolSet(names ...string) func(string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(name string) bool { return set[name] }
}

// TestRepairToolNameAsActionType reproduces the "todo.write" failure from
// session 1786111806174657000.
func TestRepairToolNameAsActionType(t *testing.T) {
	raw := `{"rationale":"track tasks","action":{"type":"todo.write","args":{"items":[]}}}`
	action, repairs, err := ParseActionRepairing(raw, knownToolSet("todo.write"))
	if err != nil {
		t.Fatalf("ParseActionRepairing: %v", err)
	}
	if action.Type != ActionToolCall {
		t.Errorf("Type = %q, want tool_call", action.Type)
	}
	if action.Tool != "todo.write" {
		t.Errorf("Tool = %q, want todo.write", action.Tool)
	}
	if string(action.Args) != `{"items":[]}` {
		t.Errorf("Args = %s, want the original args preserved", action.Args)
	}
	if len(repairs) != 1 {
		t.Errorf("repairs = %v, want one note", repairs)
	}
}

func TestUnknownActionTypeStillErrors(t *testing.T) {
	raw := `{"action":{"type":"not.a.real.tool"}}`
	if _, _, err := ParseActionRepairing(raw, knownToolSet("todo.write")); err == nil {
		t.Fatal("an unregistered name must still error, not be guessed into a tool call")
	}
}

func TestNoRegistryMeansNoToolNameRepair(t *testing.T) {
	raw := `{"action":{"type":"todo.write"}}`
	if _, _, err := ParseActionRepairing(raw, nil); err == nil {
		t.Fatal("without a registry to verify against, the repair must not fire")
	}
}

func TestRepairSingleActionUnderActionsKey(t *testing.T) {
	raw := `{"action":{"type":"final","content":"done"},"actions":null}`
	action, repairs, err := ParseActionRepairing(raw, nil)
	if err != nil {
		t.Fatalf("ParseActionRepairing: %v", err)
	}
	if action.Type != ActionFinal || action.Content != "done" {
		t.Errorf("action = %+v, want a final/done", action)
	}
	if len(repairs) != 0 {
		t.Errorf("repairs = %v, want none for a well-formed envelope", repairs)
	}
}

func TestRepairActionWrappedInArray(t *testing.T) {
	raw := `{"action":[{"type":"final","content":"done"}]}`
	action, repairs, err := ParseActionRepairing(raw, nil)
	if err != nil {
		t.Fatalf("ParseActionRepairing: %v", err)
	}
	if action.Type != ActionFinal || action.Content != "done" {
		t.Errorf("action = %+v, want final/done", action)
	}
	if len(repairs) != 1 {
		t.Errorf("repairs = %v, want one note about the array wrapper", repairs)
	}
}

// A one-element "actions" array must stay a batch: the parallel form carries
// a read-only restriction that collapsing would bypass.
func TestSingleElementActionsArrayStaysBatch(t *testing.T) {
	raw := `{"actions":[{"type":"tool_call","tool":"file.read","args":{}}]}`
	action, _, err := ParseActionRepairing(raw, nil)
	if err != nil {
		t.Fatalf("ParseActionRepairing: %v", err)
	}
	if len(action.Actions) != 1 {
		t.Fatalf("Actions = %+v, want a one-element batch, not a collapsed single action", action.Actions)
	}
	if action.Type != "" {
		t.Errorf("Type = %q, want empty on the batch form", action.Type)
	}
}

func TestRepairTruncatedEnvelope(t *testing.T) {
	raw := `{"rationale":"x","action":{"type":"final","content":"done"}`
	action, repairs, err := ParseActionRepairing(raw, nil)
	if err != nil {
		t.Fatalf("ParseActionRepairing: %v", err)
	}
	if action.Content != "done" {
		t.Errorf("Content = %q, want done", action.Content)
	}
	if len(repairs) != 1 || !strings.Contains(repairs[0], "unbalanced") {
		t.Errorf("repairs = %v, want an unbalanced-envelope note", repairs)
	}
}

func TestRepairStrayArrayCloseTail(t *testing.T) {
	// The "]}" tail the model emitted twice in the observed session.
	raw := `{"rationale":"x","action":{"type":"final","content":"done"}]}`
	action, repairs, err := ParseActionRepairing(raw, nil)
	if err != nil {
		t.Fatalf("ParseActionRepairing: %v", err)
	}
	if action.Type != ActionFinal || action.Content != "done" {
		t.Errorf("action = %+v, want final/done", action)
	}
	if len(repairs) == 0 {
		t.Error("a repaired envelope must report at least one note")
	}
}

func TestWellFormedEnvelopeReportsNoRepairs(t *testing.T) {
	raw := `{"rationale":"x","action":{"type":"final","content":"done"}}`
	_, repairs, err := ParseActionRepairing(raw, knownToolSet("todo.write"))
	if err != nil {
		t.Fatalf("ParseActionRepairing: %v", err)
	}
	if len(repairs) != 0 {
		t.Errorf("repairs = %v, want none", repairs)
	}
}

func TestBuildRepairNoticeMessage(t *testing.T) {
	if BuildRepairNoticeMessage(nil) != nil {
		t.Error("no repairs must produce no message")
	}
	msg := BuildRepairNoticeMessage([]string{"note one"})
	if msg == nil {
		t.Fatal("expected a message")
	}
	if !strings.Contains(msg.Content, "note one") {
		t.Errorf("content %q must include the repair note", msg.Content)
	}
	if !strings.Contains(msg.Content, "do not resend") {
		t.Errorf("content %q must make clear the action already ran", msg.Content)
	}
}
