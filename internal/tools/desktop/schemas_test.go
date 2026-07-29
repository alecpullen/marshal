package desktop

import (
	"encoding/json"
	"testing"

	"marshal/internal/tools/registry"
)

func TestSchemasCompileAndRejectUnknownProperties(t *testing.T) {
	reg := registry.New()
	// Construct tools directly rather than calling RegisterAll, which would
	// attempt to start a browser backend.
	ts := &toolSet{}
	for _, tool := range []registry.Tool{
		ts.navigateTool(),
		ts.readTool(),
		ts.clickTool(),
		ts.fillTool(),
		ts.submitTool(),
		ts.screenshotTool(),
	} {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
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
