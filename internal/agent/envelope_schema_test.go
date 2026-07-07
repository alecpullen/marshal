package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestActionEnvelopeResponseFormatShape(t *testing.T) {
	rf := ActionEnvelopeResponseFormat()
	if rf.Type != "json_schema" || rf.JSONSchema == nil || rf.JSONSchema.Name != "marshal_action" {
		t.Fatalf("rf = %+v", rf)
	}
	var parsed map[string]any
	if err := json.Unmarshal(rf.JSONSchema.Schema, &parsed); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if parsed["type"] != "object" {
		t.Fatalf("schema root type = %v, want object", parsed["type"])
	}
	required, _ := parsed["required"].([]any)
	if len(required) != 1 || required[0] != "rationale" {
		t.Fatalf("required = %v, want exactly [rationale] (action/actions are mutually exclusive, so neither can be required)", required)
	}
}

func TestActionEnvelopeSchemaEnumMatchesProtocol(t *testing.T) {
	protocolTypes := []ActionType{ActionAnswer, ActionToolCall, ActionPatch, ActionFinal, ActionAskUser}

	schemaText := string(ActionEnvelopeResponseFormat().JSONSchema.Schema)
	for _, at := range protocolTypes {
		if !strings.Contains(schemaText, `"`+string(at)+`"`) {
			t.Errorf("schema enum missing action type %q", at)
		}
	}
}
