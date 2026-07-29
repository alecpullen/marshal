package skills

import (
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestSchemasCompileAndRejectUnknownProperties(t *testing.T) {
	reg := registry.New()
	idx := NewIndex()
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	RegisterTool(reg, idx, state)
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
