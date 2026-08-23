package agent

import (
	"encoding/json"
	"testing"
)

func TestSummarizeToolSchemaRendersRequiredThenOptional(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":   {"type": "string"},
			"offset": {"type": "integer"},
			"limit":  {"type": "integer"}
		},
		"required": ["path"]
	}`)
	got := summarizeToolSchema(raw)
	want := "args: path:string, limit?:integer, offset?:integer"
	if got != want {
		t.Fatalf("summarizeToolSchema = %q, want %q", got, want)
	}
}

func TestSummarizeToolSchemaRendersShortEnums(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"properties": {"mode": {"type": "string", "enum": ["substring", "regex"]}}
	}`)
	got := summarizeToolSchema(raw)
	want := "args: mode?:substring|regex"
	if got != want {
		t.Fatalf("summarizeToolSchema = %q, want %q", got, want)
	}
}

func TestSummarizeToolSchemaElidesLongEnums(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"properties": {"mode": {"type": "string", "enum": ["a-very-long-enum-value", "another-very-long-enum-value"]}}
	}`)
	got := summarizeToolSchema(raw)
	want := "args: mode?:string"
	if got != want {
		t.Fatalf("summarizeToolSchema = %q, want %q", got, want)
	}
}

func TestSummarizeToolSchemaFallbacks(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"nil":           nil,
		"malformed":     json.RawMessage(`{"type":`),
		"no properties": json.RawMessage(`{"type":"object"}`),
	} {
		if got := summarizeToolSchema(raw); got != "" {
			t.Errorf("%s: summarizeToolSchema = %q, want empty", name, got)
		}
	}
}

func TestSummarizeToolSchemaEmptyTypeBecomesAny(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"query":{}},"required":["query"]}`)
	got := summarizeToolSchema(raw)
	want := "args: query:any"
	if got != want {
		t.Fatalf("summarizeToolSchema = %q, want %q", got, want)
	}
}
