package registry

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// todoSchema mirrors the real todo.write schema closely enough to exercise
// required, enum, nested items, and additionalProperties in one place.
const todoSchema = `{"type":"object","properties":{` +
	`"todos":{"type":"array","items":{"type":"object","properties":{` +
	`"content":{"type":"string"},` +
	`"status":{"type":"string","enum":["pending","in_progress","completed"]}},` +
	`"required":["content","status"],"additionalProperties":false}},` +
	`"limit":{"type":"integer"}},` +
	`"required":["todos"],"additionalProperties":false}`

func mustCompile(t *testing.T, raw string) *Validator {
	t.Helper()
	v, err := CompileSchema("test.tool", json.RawMessage(raw))
	if err != nil {
		t.Fatalf("CompileSchema: %v", err)
	}
	return v
}

func TestValidateAcceptsConformingArgs(t *testing.T) {
	v := mustCompile(t, todoSchema)
	args := json.RawMessage(`{"todos":[{"content":"write it","status":"pending"}],"limit":3}`)
	if err := v.Validate("test.tool", args); err != nil {
		t.Fatalf("Validate = %v, want nil", err)
	}
}

// The regression this whole change exists for.
func TestValidateRejectsMissingRequiredProperty(t *testing.T) {
	v := mustCompile(t, todoSchema)
	args := json.RawMessage(`{"todos":[{"status":"pending"}]}`)
	err := v.Validate("test.tool", args)
	if err == nil {
		t.Fatal("Validate = nil, want an error for a todo with no content")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("error does not wrap ErrInvalidArgs: %v", err)
	}
	for _, want := range []string{"test.tool", "todos[0]", "content"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err.Error(), want)
		}
	}
}

func TestValidateRejectsUnknownProperty(t *testing.T) {
	v := mustCompile(t, todoSchema)
	args := json.RawMessage(`{"todos":[{"content":"a","status":"pending"}],"extra":1}`)
	err := v.Validate("test.tool", args)
	if err == nil {
		t.Fatal("Validate = nil, want an error for an unknown property")
	}
	if !strings.Contains(err.Error(), "extra") {
		t.Fatalf("error %q does not name the offending property", err.Error())
	}
}

func TestValidateRejectsBadEnumValue(t *testing.T) {
	v := mustCompile(t, todoSchema)
	args := json.RawMessage(`{"todos":[{"content":"a","status":"done"}]}`)
	err := v.Validate("test.tool", args)
	if err == nil {
		t.Fatal("Validate = nil, want an error for an out-of-enum status")
	}
	if !strings.Contains(err.Error(), "todos[0].status") {
		t.Fatalf("error %q does not use a dotted/bracket path", err.Error())
	}
}

// Numbers must decode as json.Number, or "integer" silently accepts 3.5.
func TestValidateRejectsFractionalInteger(t *testing.T) {
	v := mustCompile(t, todoSchema)
	args := json.RawMessage(`{"todos":[{"content":"a","status":"pending"}],"limit":3.5}`)
	if err := v.Validate("test.tool", args); err == nil {
		t.Fatal("Validate = nil, want an error for a fractional integer")
	}
}

// Today empty args short-circuit to success, which would let a no-argument
// call slip past a schema with required fields.
func TestValidateRejectsEmptyArgsWhenFieldsRequired(t *testing.T) {
	v := mustCompile(t, todoSchema)
	for _, args := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`{}`)} {
		if err := v.Validate("test.tool", args); err == nil {
			t.Fatalf("Validate(%q) = nil, want an error (todos is required)", string(args))
		}
	}
}

func TestValidateAcceptsEmptyArgsWhenNothingRequired(t *testing.T) {
	v := mustCompile(t, `{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`)
	for _, args := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`{}`)} {
		if err := v.Validate("test.tool", args); err != nil {
			t.Fatalf("Validate(%q) = %v, want nil", string(args), err)
		}
	}
}

func TestValidateRejectsNonObjectArgs(t *testing.T) {
	v := mustCompile(t, todoSchema)
	if err := v.Validate("test.tool", json.RawMessage(`[1,2]`)); err == nil {
		t.Fatal("Validate = nil, want an error for a JSON array")
	}
	if err := v.Validate("test.tool", json.RawMessage(`not json`)); err == nil {
		t.Fatal("Validate = nil, want an error for malformed JSON")
	}
}

func TestNilValidatorAcceptsAnything(t *testing.T) {
	var v *Validator
	if err := v.Validate("test.tool", json.RawMessage(`{"anything":true}`)); err != nil {
		t.Fatalf("nil Validator rejected args: %v", err)
	}
}

func TestCompileSchemaEmptyReturnsNilValidator(t *testing.T) {
	v, err := CompileSchema("test.tool", nil)
	if err != nil {
		t.Fatalf("CompileSchema(nil) err = %v, want nil", err)
	}
	if v != nil {
		t.Fatal("CompileSchema(nil) returned a validator, want nil (unconstrained)")
	}
}

func TestCompileSchemaRejectsMalformedSchema(t *testing.T) {
	if _, err := CompileSchema("test.tool", json.RawMessage(`{"type":`)); err == nil {
		t.Fatal("CompileSchema = nil error for malformed JSON")
	}
	if _, err := CompileSchema("test.tool", json.RawMessage(`{"type":123}`)); err == nil {
		t.Fatal("CompileSchema = nil error for a schema whose type is not a string")
	}
}

// Determinism matters: the stall detector hashes tool results, so unstable
// cause ordering would defeat repeat detection.
func TestValidateErrorIsDeterministic(t *testing.T) {
	v := mustCompile(t, todoSchema)
	args := json.RawMessage(`{"todos":[{"status":"nope"},{"content":"b"}],"extra":1}`)
	first := v.Validate("test.tool", args).Error()
	for i := 0; i < 20; i++ {
		if got := v.Validate("test.tool", args).Error(); got != first {
			t.Fatalf("error text varies between runs:\n%q\n%q", first, got)
		}
	}
}

func TestValidateErrorTruncatesManyCauses(t *testing.T) {
	v := mustCompile(t, todoSchema)
	var items []string
	for i := 0; i < 12; i++ {
		items = append(items, `{"status":"pending"}`) // each missing content
	}
	args := json.RawMessage(`{"todos":[` + strings.Join(items, ",") + `]}`)
	msg := v.Validate("test.tool", args).Error()
	if !strings.Contains(msg, "more)") {
		t.Fatalf("error %q does not truncate with a (+N more) suffix", msg)
	}
	if len(msg) > 600 {
		t.Fatalf("error is %d chars, want it capped near 500", len(msg))
	}
}
