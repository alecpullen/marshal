# Native Structured Output Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make malformed action envelopes structurally impossible where the provider supports it: JSON-schema constrained decoding for the action protocol, opted in per preset via `tool_calling = "json_schema"`.

**Architecture:** `schema.ResponseFormat` grows an optional `json_schema` payload (the OpenAI-compatible wire shape, which llama.cpp server, vLLM, LM Studio, and Ollama's OpenAI endpoint all accept). `internal/agent` owns a static JSON Schema for its action envelope, kept honest by a test that cross-checks it against the `ActionType` constants. App wiring resolves preset `tool_calling` + provider capabilities into the right `ResponseFormat` via one pure function used by both runner constructors. **Deliberately out of scope:** provider-native function/tool-calling (mapping registry tools to provider tool definitions) — that is a protocol redesign, not a decoding constraint, and the telemetry data should justify it first.

**Tech Stack:** Go stdlib only. No JSON-Schema validation library — the schema is data we emit, not validate against.

**Prerequisite:** Telemetry plan merged (eval table exists). Independent of the reliability trio and ask_user plans except one enum value: the envelope schema below includes `"ask_user"` — if the ask_user plan has not merged yet, the cross-check test in Task 2 will simply not require it (the test derives the enum from the `ActionType` constants that exist, so build order self-corrects; keep the schema enum in sync with the test's failure output).

## Global Constraints

- Work on branch `structured-output` (from `main` after prior loop-improvement branches merge).
- Build/test with `CGO_ENABLED=1 go test ./...`; `gofmt` clean; `go vet` clean except the documented pre-existing app.go mutex-copy warning.
- Wire shape must match OpenAI structured outputs exactly: `"response_format": {"type":"json_schema","json_schema":{"name":"...","strict":true,"schema":{...}}}`.
- Back-compat: `ResponseFormat{Type: "json_object"}` must serialize exactly as before (no `json_schema` key) — existing json-mode presets keep working unchanged.
- Fallback ladder when a preset asks for more than the provider can do: `json_schema` capability missing → try `json_object` → nil. Never fail runner construction over a decoding preference.
- The runner (`internal/agent/runner.go`) needs no changes — it already passes `r.ResponseFormat` through `chatOnce`; this plan only changes what gets put in that field.

---

### Task 1: ResponseFormat wire extension

**Files:**
- Modify: `internal/llm/schema/chat.go`
- Test: `internal/llm/provider/openai_compatible_test.go` (wire-serialization assertions — this package already tests the request wire shape; put the new cases beside the existing request-marshaling tests, found via `grep -n "response_format\|ResponseFormat" internal/llm/provider/openai_compatible_test.go`)

**Interfaces:**
- Consumes: `chatRequestWire` (openai_compatible_wire.go:33) — already embeds `*schema.ResponseFormat` with `json:"response_format,omitempty"`, so extending the schema struct extends the wire automatically.
- Produces: `schema.JSONSchemaSpec{Name string; Strict bool; Schema json.RawMessage}` and the extended `schema.ResponseFormat{Type string; JSONSchema *JSONSchemaSpec}`.

- [ ] **Step 1: Write the failing test**

Append to `internal/llm/provider/openai_compatible_test.go`:

```go
func TestResponseFormatWireShapes(t *testing.T) {
	t.Run("json_object serializes without json_schema key", func(t *testing.T) {
		b, err := json.Marshal(&schema.ResponseFormat{Type: "json_object"})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(b) != `{"type":"json_object"}` {
			t.Fatalf("wire = %s, want back-compat shape", b)
		}
	})

	t.Run("json_schema serializes the full structured-output shape", func(t *testing.T) {
		rf := &schema.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &schema.JSONSchemaSpec{
				Name:   "action_envelope",
				Strict: true,
				Schema: json.RawMessage(`{"type":"object"}`),
			},
		}
		b, err := json.Marshal(rf)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want := `{"type":"json_schema","json_schema":{"name":"action_envelope","strict":true,"schema":{"type":"object"}}}`
		if string(b) != want {
			t.Fatalf("wire = %s\nwant  %s", b, want)
		}
	})
}
```

(Add `"encoding/json"` and the schema import to the test file if absent.)

- [ ] **Step 2: Run to verify failure**

Run: `CGO_ENABLED=1 go test ./internal/llm/provider/ -run TestResponseFormatWireShapes -v`
Expected: compile error (`schema.JSONSchemaSpec` undefined). Red; proceed.

- [ ] **Step 3: Implement**

In `internal/llm/schema/chat.go`, replace the `ResponseFormat` declaration:

```go
// ResponseFormat selects constrained decoding. Type "json_object" is plain
// JSON mode; Type "json_schema" additionally carries the schema in the
// OpenAI structured-outputs wire shape (accepted by llama.cpp server, vLLM,
// LM Studio, and Ollama's OpenAI-compatible endpoint).
type ResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *JSONSchemaSpec `json:"json_schema,omitempty"`
}

// JSONSchemaSpec is the json_schema payload of a ResponseFormat.
type JSONSchemaSpec struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema"`
}
```

Add `"encoding/json"` to chat.go's imports.

- [ ] **Step 4: Run, format, vet, commit**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/llm/...` — expect all PASS (the wire struct passes the pointer through; existing serialization tests confirm back-compat).

```bash
gofmt -w internal/llm
go vet ./internal/llm/...
git add internal/llm/schema/chat.go internal/llm/provider/openai_compatible_test.go
git commit -m "feat(llm): json_schema response-format wire support"
```

---

### Task 2: The action-envelope schema

**Files:**
- Create: `internal/agent/envelope_schema.go`
- Test: `internal/agent/envelope_schema_test.go`

**Interfaces:**
- Consumes: `schema.ResponseFormat` / `schema.JSONSchemaSpec` (Task 1); the `ActionType` constants (protocol.go).
- Produces: `func ActionEnvelopeResponseFormat() *schema.ResponseFormat` — used by app wiring in Task 3.

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/envelope_schema_test.go`:

```go
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

// TestActionEnvelopeSchemaEnumMatchesProtocol keeps the schema's action-type
// enum in lockstep with the ActionType constants: adding an action type
// without updating the schema (or vice versa) must fail this test.
func TestActionEnvelopeSchemaEnumMatchesProtocol(t *testing.T) {
	protocolTypes := []ActionType{ActionAnswer, ActionToolCall, ActionPatch, ActionFinal}
	// NOTE: append ActionAskUser here when the ask_user plan has merged.

	schemaText := string(ActionEnvelopeResponseFormat().JSONSchema.Schema)
	for _, at := range protocolTypes {
		if !strings.Contains(schemaText, `"`+string(at)+`"`) {
			t.Errorf("schema enum missing action type %q", at)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run TestActionEnvelope -v`
Expected: compile error (`ActionEnvelopeResponseFormat` undefined). Red; proceed.

- [ ] **Step 3: Implement**

Create `internal/agent/envelope_schema.go`:

```go
package agent

import (
	"encoding/json"

	"marshal/internal/llm/schema"
)

// actionEnvelopeSchema is the JSON Schema for the action protocol envelope
// (see baseOutputFormat in prompts.go and ParseAction in protocol.go). It is
// deliberately permissive about which of action/actions is present — strict
// oneOf composition confuses several local constrained-decoding
// implementations — but locks down the shape of each: an object with a
// typed "type" and the protocol's fields, nothing else.
// TestActionEnvelopeSchemaEnumMatchesProtocol keeps the enum in sync with
// the ActionType constants; update both together.
const actionEnvelopeSchema = `{
  "type": "object",
  "properties": {
    "rationale": {"type": "string"},
    "action": {
      "type": "object",
      "properties": {
        "type": {"type": "string", "enum": ["answer", "tool_call", "patch", "final"]},
        "tool": {"type": "string"},
        "args": {"type": "object"},
        "content": {"type": "string"}
      },
      "required": ["type"],
      "additionalProperties": false
    },
    "actions": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "type": {"type": "string", "enum": ["tool_call"]},
          "tool": {"type": "string"},
          "args": {"type": "object"},
          "content": {"type": "string"}
        },
        "required": ["type", "tool"],
        "additionalProperties": false
      }
    }
  },
  "required": ["rationale"],
  "additionalProperties": false
}`

// ActionEnvelopeResponseFormat returns the constrained-decoding response
// format for Marshal's action protocol. Strict is false because the schema
// intentionally allows either "action" or "actions" (strict mode on some
// servers requires every property in required).
func ActionEnvelopeResponseFormat() *schema.ResponseFormat {
	return &schema.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &schema.JSONSchemaSpec{
			Name:   "marshal_action",
			Schema: json.RawMessage(actionEnvelopeSchema),
		},
	}
}
```

(If the ask_user plan is already merged, add `"ask_user"` to the single-action enum and `ActionAskUser` to the test's `protocolTypes` — the test tells you.)

- [ ] **Step 4: Run, format, vet, commit**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/agent/...` — expect all PASS.

```bash
gofmt -w internal/agent
go vet ./internal/agent/...
git add internal/agent/envelope_schema.go internal/agent/envelope_schema_test.go
git commit -m "feat(agent): JSON schema for the action envelope"
```

---

### Task 3: Preset opt-in and wiring

**Files:**
- Modify: `internal/app/app.go` (extract `actionResponseFormat`, use in `buildAgentRunner` and the swarm factory)
- Modify: `docs/09-configuration-examples.md` (document the new preset value)
- Test: `internal/app/response_format_test.go` (new)

**Interfaces:**
- Consumes: `agent.ActionEnvelopeResponseFormat()` (Task 2), `route.Preset.ToolCalling` (routing preset, TOML key `tool_calling`), `provider.Capabilities(ctx)` — `StructuredOutput` and `JSONMode` both default to `true` in the OpenAI-compatible provider (`defaultCapabilities()`), so `tool_calling = "json_schema"` works out of the box.
- Produces: `func actionResponseFormat(toolCalling string, caps schema.ProviderCapabilities) *schema.ResponseFormat`.

- [ ] **Step 1: Write the failing test**

Create `internal/app/response_format_test.go`:

```go
package app

import (
	"testing"

	"marshal/internal/llm/schema"
)

func TestActionResponseFormatLadder(t *testing.T) {
	all := schema.ProviderCapabilities{JSONMode: true, StructuredOutput: true}
	jsonOnly := schema.ProviderCapabilities{JSONMode: true}
	none := schema.ProviderCapabilities{}

	cases := []struct {
		name        string
		toolCalling string
		caps        schema.ProviderCapabilities
		wantType    string // "" means nil
		wantSchema  bool
	}{
		{"json_schema with full caps", "json_schema", all, "json_schema", true},
		{"json_schema degrades to json_object", "json_schema", jsonOnly, "json_object", false},
		{"json_schema degrades to nil", "json_schema", none, "", false},
		{"json with json mode", "json", all, "json_object", false},
		{"json without json mode", "json", none, "", false},
		{"unset preset", "", all, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := actionResponseFormat(tc.toolCalling, tc.caps)
			if tc.wantType == "" {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			if got == nil || got.Type != tc.wantType {
				t.Fatalf("got %+v, want type %q", got, tc.wantType)
			}
			if tc.wantSchema != (got.JSONSchema != nil) {
				t.Fatalf("JSONSchema presence = %v, want %v", got.JSONSchema != nil, tc.wantSchema)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify failure, then implement**

Run: `CGO_ENABLED=1 go test ./internal/app/ -run TestActionResponseFormatLadder -v` — expect compile error.

In `internal/app/app.go`, add:

```go
// actionResponseFormat resolves a preset's tool_calling preference against
// the provider's capabilities: "json_schema" gets constrained decoding of
// the action envelope, degrading to plain JSON mode and then to nil rather
// than failing construction.
func actionResponseFormat(toolCalling string, caps schema.ProviderCapabilities) *schema.ResponseFormat {
	switch toolCalling {
	case "json_schema":
		if caps.StructuredOutput {
			return agent.ActionEnvelopeResponseFormat()
		}
		if caps.JSONMode {
			return &schema.ResponseFormat{Type: "json_object"}
		}
	case "json":
		if caps.JSONMode {
			return &schema.ResponseFormat{Type: "json_object"}
		}
	}
	return nil
}
```

In `buildAgentRunner`, replace:

```go
	if route.Preset.ToolCalling == "json" && resolvedProvider.Capabilities(ctx).JSONMode {
		runner.ResponseFormat = &schema.ResponseFormat{Type: "json_object"}
	}
```

with:

```go
	runner.ResponseFormat = actionResponseFormat(route.Preset.ToolCalling, resolvedProvider.Capabilities(ctx))
```

In `buildSwarmRunner`'s factory, replace the analogous `if route.Preset.ToolCalling == "json" && p.Capabilities(ctx).JSONMode { ... }` block with:

```go
			r.ResponseFormat = actionResponseFormat(route.Preset.ToolCalling, p.Capabilities(ctx))
```

- [ ] **Step 3: Document the preset value**

In `docs/09-configuration-examples.md`, find a preset example that shows `tool_calling = "json"` (grep the file) and add beneath it:

```toml
# tool_calling = "json_schema" constrains decoding to Marshal's action
# envelope on servers that support OpenAI structured outputs (llama.cpp,
# vLLM, LM Studio, Ollama's OpenAI endpoint). Falls back to plain JSON
# mode, then to unconstrained output, if the provider lacks support.
```

- [ ] **Step 4: Run, format, vet, commit**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/app/... ./internal/agent/...` — expect all PASS.

```bash
gofmt -w internal/app
go vet ./internal/app/... 2>&1 | grep -v "copies lock value" || true
git add internal/app/app.go internal/app/response_format_test.go docs/09-configuration-examples.md
git commit -m "feat(app): json_schema tool_calling preset with capability fallback ladder"
```

---

### Task 4: End-to-end request assertion

Prove the schema actually reaches the wire: an httptest-backed OpenAI-compatible provider run where the request body carries the full `response_format`.

**Files:**
- Test: `internal/llm/provider/openai_compatible_test.go`

**Interfaces:**
- Consumes: the package's existing httptest server pattern (`grep -n "httptest" internal/llm/provider/openai_compatible_test.go` — mirror an existing Chat test's server + provider construction exactly).
- Produces: nothing — regression coverage.

- [ ] **Step 1: Write the test**

Append (mirroring the neighboring httptest Chat test's setup for server, provider construction, and event draining — reuse their helper if one exists):

```go
func TestChatSendsJSONSchemaResponseFormat(t *testing.T) {
	var gotBody []byte
	// Build the httptest server exactly as the existing Chat tests in this
	// file do, but capture the request body before responding:
	//   gotBody, _ = io.ReadAll(r.Body)
	// then respond with the same minimal SSE/JSON the neighboring test uses.
	...
	req := schema.ChatRequest{
		Model:          "m",
		Messages:       []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
		ResponseFormat: &schema.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &schema.JSONSchemaSpec{Name: "marshal_action", Schema: json.RawMessage(`{"type":"object"}`)},
		},
	}
	// ... invoke Chat and drain events as the neighboring test does ...

	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	rf, _ := body["response_format"].(map[string]any)
	if rf == nil || rf["type"] != "json_schema" {
		t.Fatalf("response_format = %v", body["response_format"])
	}
	js, _ := rf["json_schema"].(map[string]any)
	if js == nil || js["name"] != "marshal_action" || js["schema"] == nil {
		t.Fatalf("json_schema payload = %v", js)
	}
}
```

The two `...` sections MUST be copied from the file's existing Chat-over-httptest test (server construction and event draining are boilerplate that already exists there); the request construction and body assertions above are complete.

- [ ] **Step 2: Run, format, vet, commit**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/llm/provider/` — expect all PASS.

```bash
gofmt -w internal/llm/provider
go vet ./internal/llm/...
git add internal/llm/provider/openai_compatible_test.go
git commit -m "test(llm): assert json_schema response_format reaches the wire"
```

---

## Verification

`CGO_ENABLED=1 go test -count=1 ./...` green. Live check (the real proof): configure a preset with `tool_calling = "json_schema"` against llama.cpp server or Ollama's OpenAI endpoint, run several turns, and compare `turn_metrics.parse_failures` before/after (the telemetry table exists precisely to measure this change). If the server rejects the request shape, the provider error surfaces in the TUI — capture it and check the server's structured-output flag/version before assuming a Marshal bug.
