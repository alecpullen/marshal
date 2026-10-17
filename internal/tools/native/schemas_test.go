package native

import (
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestSchemasCompileAndRejectUnknownProperties(t *testing.T) {
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	for _, tool := range reg.List() {
		if _, err := registry.CompileSchema(tool.Name, tool.Schema); err != nil {
			t.Errorf("%s: %v", tool.Name, err)
			continue
		}
		if len(tool.Schema) == 0 {
			continue
		}
		var doc struct {
			AdditionalProperties *bool `json:"additionalProperties"`
		}
		if err := json.Unmarshal(tool.Schema, &doc); err != nil {
			t.Errorf("%s: schema does not decode: %v", tool.Name, err)
			continue
		}
		if doc.AdditionalProperties == nil || *doc.AdditionalProperties {
			t.Errorf("%s: schema is missing \"additionalProperties\": false", tool.Name)
		}
	}
}

// TestShellToolTextDocumentsWaitContinuationAndStop pins the model-facing
// contract for the managed tools: timeout_seconds is documented as a wait
// budget (never an execution deadline), the continuation and its await handle
// are named, and the explicit stop is described. The model's only view of this
// behaviour is the schema and description, so a silent change to it is a
// behaviour change.
func TestShellToolTextDocumentsWaitContinuationAndStop(t *testing.T) {
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	for name, wants := range map[string][]string{
		"shell.run": {
			"timeout_seconds",
			"waits",
			"background",
			"not killed by elapsed time",
			"agent.await",
		},
		"test.run": {
			"managed execution",
			"agent.await",
		},
	} {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		text := tool.Description + " " + string(tool.Schema)
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("%s text missing %q:\n%s", name, want, text)
			}
		}
	}

	// The timeout property itself must say it is a wait, not a deadline.
	tool, _ := reg.Lookup("shell.run")
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	timeout, ok := schema.Properties["timeout_seconds"]
	if !ok {
		t.Fatal("shell.run schema has no timeout_seconds property")
	}
	for _, want := range []string{"waits", "never limits the command's lifetime"} {
		if !strings.Contains(timeout.Description, want) {
			t.Errorf("timeout_seconds description missing %q: %q", want, timeout.Description)
		}
	}
}
