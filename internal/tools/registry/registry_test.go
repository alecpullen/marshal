package registry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestRiskLevelValidAcceptsDocumentedValues(t *testing.T) {
	for _, risk := range []RiskLevel{
		RiskReadOnly,
		RiskWorkspaceWrite,
		RiskCommand,
		RiskNetwork,
		RiskDestructive,
		RiskEnvironment,
	} {
		if !risk.Valid() {
			t.Fatalf("%q Valid() = false, want true", risk)
		}
	}
}

func TestRiskLevelValidRejectsUnknownValue(t *testing.T) {
	if RiskLevel("surprise").Valid() {
		t.Fatal(`RiskLevel("surprise").Valid() = true, want false`)
	}
}

func TestToolHandlerSignature(t *testing.T) {
	handler := ToolHandler(func(ctx context.Context, call ToolCall) (ToolResult, error) {
		if call.Name != "example.tool" {
			t.Fatalf("call.Name = %q, want example.tool", call.Name)
		}
		return ToolResult{Summary: "ok"}, nil
	})

	result, err := handler(context.Background(), ToolCall{Name: "example.tool"})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if result.Summary != "ok" {
		t.Fatalf("result.Summary = %q, want ok", result.Summary)
	}
}

func TestRegisterAcceptsValidTool(t *testing.T) {
	reg := New()
	tool := testTool("example.beta")

	if err := reg.Register(tool); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	got, ok := reg.Lookup("example.beta")
	if !ok {
		t.Fatal("Lookup returned ok=false")
	}
	if got.Name != "example.beta" {
		t.Fatalf("Lookup tool name = %q, want example.beta", got.Name)
	}
}

func TestRegisterCopiesSchemaBytes(t *testing.T) {
	reg := New()
	tool := testTool("example.copy_on_register")

	if err := reg.Register(tool); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	tool.Schema[0] = '!'

	got, ok := reg.Lookup("example.copy_on_register")
	if !ok {
		t.Fatal("Lookup returned ok=false")
	}
	if string(got.Schema) != `{"type":"object"}` {
		t.Fatalf("Lookup schema = %q, want %q", got.Schema, `{"type":"object"}`)
	}
}

func TestRegisterRejectsEmptyName(t *testing.T) {
	reg := New()
	tool := testTool("   ")

	err := reg.Register(tool)
	if err == nil {
		t.Fatal("Register returned nil error, want invalid tool")
	}
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("Register error = %v, want ErrInvalidTool", err)
	}
}

func TestRegisterRejectsNilHandler(t *testing.T) {
	reg := New()
	tool := testTool("example.no_handler")
	tool.Handler = nil

	err := reg.Register(tool)
	if err == nil {
		t.Fatal("Register returned nil error, want invalid tool")
	}
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("Register error = %v, want ErrInvalidTool", err)
	}
}

func TestRegisterRejectsUnknownRisk(t *testing.T) {
	reg := New()
	tool := testTool("example.risky")
	tool.Risk = RiskLevel("unknown")

	err := reg.Register(tool)
	if err == nil {
		t.Fatal("Register returned nil error, want invalid tool")
	}
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("Register error = %v, want ErrInvalidTool", err)
	}
}

func TestRegisterRejectsInvalidSchemaJSON(t *testing.T) {
	reg := New()
	tool := testTool("example.bad_schema")
	tool.Schema = json.RawMessage(`{"type":`)

	err := reg.Register(tool)
	if err == nil {
		t.Fatal("Register returned nil error, want invalid tool")
	}
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("Register error = %v, want ErrInvalidTool", err)
	}
}

func TestRegisterRejectsDuplicateName(t *testing.T) {
	reg := New()

	if err := reg.Register(testTool("example.same")); err != nil {
		t.Fatalf("first Register returned error: %v", err)
	}
	err := reg.Register(testTool("example.same"))
	if err == nil {
		t.Fatal("second Register returned nil error, want duplicate")
	}
	if !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("Register error = %v, want ErrDuplicateTool", err)
	}
}

func TestLookupMissReturnsFalse(t *testing.T) {
	reg := New()

	_, ok := reg.Lookup("missing.tool")
	if ok {
		t.Fatal("Lookup ok = true, want false")
	}
}

func TestLookupReturnsSchemaCopy(t *testing.T) {
	reg := New()

	if err := reg.Register(testTool("example.copy_on_lookup")); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	got, ok := reg.Lookup("example.copy_on_lookup")
	if !ok {
		t.Fatal("Lookup returned ok=false")
	}

	got.Schema[0] = '!'

	again, ok := reg.Lookup("example.copy_on_lookup")
	if !ok {
		t.Fatal("second Lookup returned ok=false")
	}
	if string(again.Schema) != `{"type":"object"}` {
		t.Fatalf("Lookup schema = %q, want %q", again.Schema, `{"type":"object"}`)
	}
}

func TestListReturnsToolsSortedByName(t *testing.T) {
	reg := New()
	for _, name := range []string{"example.zed", "example.alpha", "example.middle"} {
		if err := reg.Register(testTool(name)); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
	}

	got := reg.List()
	if len(got) != 3 {
		t.Fatalf("len(List()) = %d, want 3", len(got))
	}
	for i, want := range []string{"example.alpha", "example.middle", "example.zed"} {
		if got[i].Name != want {
			t.Fatalf("List()[%d].Name = %q, want %q", i, got[i].Name, want)
		}
	}
}

func TestListDeferredReturnsOnlyDeferredToolsSortedByName(t *testing.T) {
	reg := New()
	a := testTool("example.alpha")
	a.Deferred = true
	b := testTool("example.beta")
	c := testTool("example.gamma")
	c.Deferred = true
	for _, tool := range []Tool{a, b, c} {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("Register(%q): %v", tool.Name, err)
		}
	}

	got := reg.ListDeferred()
	if len(got) != 2 {
		t.Fatalf("len(ListDeferred()) = %d, want 2", len(got))
	}
	if got[0].Name != "example.alpha" || got[1].Name != "example.gamma" {
		t.Fatalf("ListDeferred() order = %s, %s; want example.alpha,example.gamma", got[0].Name, got[1].Name)
	}
}

func TestListLoadedReturnsOnlyMatchingRegisteredTools(t *testing.T) {
	reg := New()
	for _, name := range []string{"example.alpha", "example.beta", "example.gamma"} {
		if err := reg.Register(testTool(name)); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
	}

	got := reg.ListLoaded([]string{"example.beta", "missing", "example.alpha"})
	if len(got) != 2 {
		t.Fatalf("len(ListLoaded()) = %d, want 2", len(got))
	}
	if got[0].Name != "example.alpha" || got[1].Name != "example.beta" {
		t.Fatalf("ListLoaded() order = %s, %s; want example.alpha,example.beta", got[0].Name, got[1].Name)
	}

	if reg.ListLoaded(nil) != nil {
		t.Fatalf("ListLoaded(nil) should be nil")
	}
}

func TestListReturnsSchemaCopies(t *testing.T) {
	reg := New()
	for _, name := range []string{"example.zed", "example.alpha"} {
		if err := reg.Register(testTool(name)); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
	}

	got := reg.List()
	got[0].Schema[0] = '!'

	again := reg.List()
	if string(again[0].Schema) != `{"type":"object"}` {
		t.Fatalf("List()[0].Schema = %q, want %q", again[0].Schema, `{"type":"object"}`)
	}
}

func TestRegisterRejectsUncompilableSchema(t *testing.T) {
	r := New()
	err := r.Register(Tool{
		Name:    "bad.schema",
		Schema:  json.RawMessage(`{"type":123}`),
		Risk:    RiskReadOnly,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err == nil {
		t.Fatal("Register = nil, want an error for a schema that is not a valid JSON Schema")
	}
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("error does not wrap ErrInvalidTool: %v", err)
	}
	if _, ok := r.Lookup("bad.schema"); ok {
		t.Fatal("a tool with an uncompilable schema was registered anyway")
	}
}

func TestRegisterRejectsMalformedSchemaJSON(t *testing.T) {
	r := New()
	err := r.Register(Tool{
		Name:    "bad.json",
		Schema:  json.RawMessage(`{"type":`),
		Risk:    RiskReadOnly,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err == nil {
		t.Fatal("Register = nil, want an error for malformed schema JSON")
	}
}

func TestRegisterAcceptsToolWithNoSchema(t *testing.T) {
	r := New()
	err := r.Register(Tool{
		Name:    "no.schema",
		Risk:    RiskReadOnly,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err != nil {
		t.Fatalf("Register = %v, want nil (an absent schema means unconstrained)", err)
	}
	tool, _ := r.Lookup("no.schema")
	if err := ValidateArgs(tool, json.RawMessage(`{"whatever":1}`)); err != nil {
		t.Fatalf("ValidateArgs on an unconstrained tool = %v, want nil", err)
	}
}

// Lookup and List clone tools; the compiled schema must survive the clone or
// every dispatched call would validate against nothing.
func TestClonedToolKeepsValidator(t *testing.T) {
	r := New()
	if err := r.Register(Tool{
		Name:    "strict",
		Schema:  json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false}`),
		Risk:    RiskReadOnly,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tool, ok := r.Lookup("strict")
	if !ok {
		t.Fatal("Lookup failed")
	}
	if err := ValidateArgs(tool, json.RawMessage(`{}`)); err == nil {
		t.Fatal("cloned tool validated empty args, want an error (a is required)")
	}
	for _, listed := range r.List() {
		if listed.Name == "strict" {
			if err := ValidateArgs(listed, json.RawMessage(`{}`)); err == nil {
				t.Fatal("listed tool lost its validator")
			}
		}
	}
}

func TestDispatchRejectsInvalidArgsWithoutCallingHandler(t *testing.T) {
	r := New()
	called := false
	if err := r.Register(Tool{
		Name:   "strict.dispatch",
		Schema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false}`),
		Risk:   RiskReadOnly,
		Handler: func(context.Context, ToolCall) (ToolResult, error) {
			called = true
			return ToolResult{}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := r.Dispatch(context.Background(), ToolCall{Name: "strict.dispatch", Args: json.RawMessage(`{"b":1}`)})
	if err == nil {
		t.Fatal("Dispatch = nil, want an error for invalid args")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("error does not wrap ErrInvalidArgs: %v", err)
	}
	if called {
		t.Fatal("handler ran despite invalid arguments")
	}
}

func TestDispatchInvokesHandlerOnValidArgs(t *testing.T) {
	r := New()
	if err := r.Register(Tool{
		Name:   "ok.dispatch",
		Schema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false}`),
		Risk:   RiskReadOnly,
		Handler: func(_ context.Context, call ToolCall) (ToolResult, error) {
			return ToolResult{Summary: "ran " + call.Name}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	res, err := r.Dispatch(context.Background(), ToolCall{Name: "ok.dispatch", Args: json.RawMessage(`{"a":"x"}`)})
	if err != nil {
		t.Fatalf("Dispatch = %v, want nil", err)
	}
	if res.Summary != "ran ok.dispatch" {
		t.Fatalf("Summary = %q", res.Summary)
	}
}

func TestDispatchUnknownTool(t *testing.T) {
	r := New()
	_, err := r.Dispatch(context.Background(), ToolCall{Name: "nope"})
	if !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("error = %v, want ErrToolNotFound", err)
	}
}

func testTool(name string) Tool {
	return Tool{
		Name:        name,
		Description: "test tool",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Risk:        RiskReadOnly,
		Handler: func(ctx context.Context, call ToolCall) (ToolResult, error) {
			return ToolResult{Summary: "ok"}, nil
		},
	}
}
