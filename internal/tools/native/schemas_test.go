package native

import (
	"encoding/json"
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
