package registry

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateArgsAcceptsEmptyArgs(t *testing.T) {
	if err := ValidateArgs(testTool("example.empty"), nil); err != nil {
		t.Fatalf("ValidateArgs returned error: %v", err)
	}
}

func TestValidateArgsAcceptsObjectArgs(t *testing.T) {
	args := json.RawMessage(`{"path":"README.md"}`)
	if err := ValidateArgs(testTool("example.object"), args); err != nil {
		t.Fatalf("ValidateArgs returned error: %v", err)
	}
}

func TestValidateArgsRejectsMalformedJSON(t *testing.T) {
	err := ValidateArgs(testTool("example.bad"), json.RawMessage(`{"path":`))
	if err == nil {
		t.Fatal("ValidateArgs returned nil error, want invalid args")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("ValidateArgs error = %v, want ErrInvalidArgs", err)
	}
}

func TestValidateArgsRejectsNonObjectJSON(t *testing.T) {
	for _, args := range []json.RawMessage{
		json.RawMessage(`[]`),
		json.RawMessage(`"README.md"`),
		json.RawMessage(`true`),
	} {
		err := ValidateArgs(testTool("example.non_object"), args)
		if err == nil {
			t.Fatalf("ValidateArgs(%s) returned nil error, want invalid args", args)
		}
		if !errors.Is(err, ErrInvalidArgs) {
			t.Fatalf("ValidateArgs(%s) error = %v, want ErrInvalidArgs", args, err)
		}
	}
}
