package agent

import (
	"encoding/json"
	"marshal/internal/llm/schema"
)

const actionEnvelopeSchema = `{
  "type": "object",
  "properties": {
    "rationale": {
      "type": "string"
    },
    "action": {
      "type": "object",
      "properties": {
        "type": {
          "type": "string",
          "enum": ["answer", "tool_call", "patch", "final", "ask_user", "question.ask"]
        },
        "tool": { "type": "string" },
        "args": { "type": "object" },
        "content": { "type": "string" }
      },
      "required": ["type"],
      "additionalProperties": false
    },
    "actions": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "type": {
            "type": "string",
            "enum": ["tool_call"]
          },
          "tool": { "type": "string" },
          "args": { "type": "object" },
          "content": { "type": "string" }
        },
        "required": ["type", "tool"],
        "additionalProperties": false
      },
      "minItems": 1
    }
  },
  "required": ["rationale"],
  "additionalProperties": false
}`

func ActionEnvelopeResponseFormat() *schema.ResponseFormat {
	return &schema.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &schema.JSONSchemaSpec{
			Name:   "marshal_action",
			Strict: false,
			Schema: json.RawMessage(actionEnvelopeSchema),
		},
	}
}
